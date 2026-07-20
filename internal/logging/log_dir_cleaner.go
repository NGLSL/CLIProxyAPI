package logging

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	// logDirCleanerInterval is how often the background cleaner scans the logs directory.
	logDirCleanerInterval = time.Minute

	// staleLogArtifactAge controls when temporary request-log artifacts left behind by
	// crashed/interrupted requests are considered safe to delete even when the size limit
	// has not been exceeded. Active requests keep writing fresher mtimes, so they are not
	// removed by this path under normal conditions.
	staleLogArtifactAge = 30 * time.Minute
)

var logDirCleanerCancel context.CancelFunc

// configureLogDirCleanerLocked starts (or restarts) the background logs-directory cleaner.
// It always runs stale temp-artifact cleanup. When maxTotalSizeMB > 0 it also enforces a
// total size budget across request logs, app logs, temp bodies, and request-log-parts dirs.
// Caller must hold writerMu (or otherwise guarantee exclusive access).
func configureLogDirCleanerLocked(logDir string, maxTotalSizeMB int, protectedPath string) {
	stopLogDirCleanerLocked()

	dir := strings.TrimSpace(logDir)
	if dir == "" {
		return
	}

	maxBytes := int64(0)
	if maxTotalSizeMB > 0 {
		maxBytes = int64(maxTotalSizeMB) * 1024 * 1024
		if maxBytes <= 0 {
			// Overflow guard: treat as disabled rather than wrapping to a tiny limit.
			maxBytes = 0
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	logDirCleanerCancel = cancel
	go runLogDirCleaner(ctx, filepath.Clean(dir), maxBytes, strings.TrimSpace(protectedPath))
}

func stopLogDirCleanerLocked() {
	if logDirCleanerCancel == nil {
		return
	}
	logDirCleanerCancel()
	logDirCleanerCancel = nil
}

func runLogDirCleaner(ctx context.Context, logDir string, maxBytes int64, protectedPath string) {
	ticker := time.NewTicker(logDirCleanerInterval)
	defer ticker.Stop()

	cleanOnce := func() {
		// First reclaim orphan temp parts left by process crashes / aborted requests.
		// This path is independent of the size budget so disk cannot fill forever with
		// abandoned request-log-parts-* directories even when the admin set the limit to 0.
		if deletedStale := cleanStaleLogArtifacts(logDir, time.Now().Add(-staleLogArtifactAge)); deletedStale > 0 {
			log.Debugf("logging: removed %d stale request-log temp artifact(s)", deletedStale)
		}

		if maxBytes <= 0 {
			return
		}
		deleted, errClean := enforceLogDirSizeLimit(logDir, maxBytes, protectedPath)
		if errClean != nil {
			log.WithError(errClean).Warn("logging: failed to enforce log directory size limit")
			return
		}
		if deleted > 0 {
			log.Debugf("logging: removed %d old log artifact(s) to enforce log directory size limit", deleted)
		}
	}

	cleanOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanOnce()
		}
	}
}

// logArtifact is one deletable unit under the logs directory (a regular log file, a temp
// body file, or a whole request-log-parts-* directory).
type logArtifact struct {
	path    string
	size    int64
	modTime time.Time
}

// enforceLogDirSizeLimit deletes the oldest log artifacts until total size is within maxBytes.
// Protected path (usually main.log currently being written by lumberjack) is never deleted.
func enforceLogDirSizeLimit(logDir string, maxBytes int64, protectedPath string) (int, error) {
	if maxBytes <= 0 {
		return 0, nil
	}

	dir := strings.TrimSpace(logDir)
	if dir == "" {
		return 0, nil
	}
	dir = filepath.Clean(dir)

	files, total, errCollect := collectLogArtifacts(dir)
	if errCollect != nil {
		return 0, errCollect
	}
	if total <= maxBytes {
		return 0, nil
	}

	protected := strings.TrimSpace(protectedPath)
	if protected != "" {
		protected = filepath.Clean(protected)
	}

	// Oldest first so recent request logs / the active main.log backups are preferred.
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})

	deleted := 0
	for _, file := range files {
		if total <= maxBytes {
			break
		}
		if protected != "" && filepath.Clean(file.path) == protected {
			continue
		}
		if errRemove := os.RemoveAll(file.path); errRemove != nil {
			log.WithError(errRemove).Warnf("logging: failed to remove old log artifact: %s", filepath.Base(file.path))
			continue
		}
		total -= file.size
		deleted++
	}

	return deleted, nil
}

// cleanStaleLogArtifacts removes temporary request-log leftovers whose mtime is older than cutoff.
// Only .tmp files and request-log-parts-* directories are considered; durable .log files are left alone.
func cleanStaleLogArtifacts(logDir string, cutoff time.Time) int {
	dir := strings.TrimSpace(logDir)
	if dir == "" {
		return 0
	}
	dir = filepath.Clean(dir)

	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return 0
		}
		log.WithError(errRead).Warn("logging: failed to scan logs directory for stale temp artifacts")
		return 0
	}

	deleted := 0
	for _, entry := range entries {
		name := entry.Name()
		if !isTemporaryLogArtifactName(name) {
			continue
		}
		path := filepath.Join(dir, name)
		info, errInfo := entry.Info()
		if errInfo != nil {
			continue
		}
		modTime := info.ModTime()
		if entry.IsDir() {
			// For part directories, use the newest nested mtime so an active writer is not
			// treated as stale just because the directory inode is older than its contents.
			if newest, errNewest := newestModTime(path); errNewest == nil {
				modTime = newest
			}
		}
		if modTime.After(cutoff) {
			continue
		}
		if errRemove := os.RemoveAll(path); errRemove != nil {
			log.WithError(errRemove).Warnf("logging: failed to remove stale log artifact: %s", name)
			continue
		}
		deleted++
	}
	return deleted
}

// collectLogArtifacts walks the top-level logs directory and returns every artifact that
// participates in the size budget, together with the aggregate size.
func collectLogArtifacts(dir string) ([]logArtifact, int64, error) {
	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return nil, 0, nil
		}
		return nil, 0, errRead
	}

	var (
		files []logArtifact
		total int64
	)
	for _, entry := range entries {
		name := entry.Name()
		if !isLogArtifactName(name) {
			continue
		}
		path := filepath.Join(dir, name)
		if entry.IsDir() {
			size, modTime, errDir := dirSizeAndModTime(path)
			if errDir != nil {
				continue
			}
			files = append(files, logArtifact{path: path, size: size, modTime: modTime})
			total += size
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		files = append(files, logArtifact{
			path:    path,
			size:    info.Size(),
			modTime: info.ModTime(),
		})
		total += info.Size()
	}
	return files, total, nil
}

func dirSizeAndModTime(dir string) (int64, time.Time, error) {
	var (
		total   int64
		modTime time.Time
		found   bool
	)
	errWalk := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Skip unreadable entries instead of failing the whole cleanup pass.
			return nil
		}
		if info.IsDir() {
			return nil
		}
		total += info.Size()
		if !found || info.ModTime().After(modTime) {
			modTime = info.ModTime()
			found = true
		}
		return nil
	})
	if errWalk != nil {
		return 0, time.Time{}, errWalk
	}
	if !found {
		// Empty directory: fall back to the directory's own mtime.
		info, errStat := os.Stat(dir)
		if errStat != nil {
			return 0, time.Time{}, errStat
		}
		return 0, info.ModTime(), nil
	}
	return total, modTime, nil
}

func newestModTime(dir string) (time.Time, error) {
	_, modTime, err := dirSizeAndModTime(dir)
	return modTime, err
}

// isLogFileName reports whether name is a durable log file (*.log / *.log.gz).
func isLogFileName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	return strings.HasSuffix(lower, ".log") || strings.HasSuffix(lower, ".log.gz")
}

// isTemporaryLogArtifactName is the subset that is safe to delete by age alone
// (crash leftovers). Durable *.log files are intentionally excluded.
// Covered:
//   - *.tmp                 : request-body / response-body temp spools
//   - request-log-parts-*   : per-request multi-part temp directories
func isTemporaryLogArtifactName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.HasSuffix(lower, ".tmp") {
		return true
	}
	return strings.HasPrefix(lower, "request-log-parts-")
}

// isLogArtifactName reports whether a top-level name under the logs directory should be
// counted toward the size budget and is eligible for deletion by the cleaner.
func isLogArtifactName(name string) bool {
	return isLogFileName(name) || isTemporaryLogArtifactName(name)
}
