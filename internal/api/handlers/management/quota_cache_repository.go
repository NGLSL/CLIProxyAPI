package management

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/util"
)

const quotaCacheFileName = "quota-cache.json"

var defaultQuotaCachePathParts = []string{"CLIProxyAPI", "management", quotaCacheFileName}

func defaultQuotaCacheBasePath() string {
	if cacheDir, err := os.UserCacheDir(); err == nil {
		return cacheDir
	}
	if configDir, err := os.UserConfigDir(); err == nil {
		return configDir
	}
	return "."
}

type quotaCacheRepository struct {
	path string
}

func newQuotaCacheRepository(configFilePath string) *quotaCacheRepository {
	return &quotaCacheRepository{path: quotaCacheFilePath(configFilePath)}
}

func quotaCacheFilePath(configFilePath string) string {
	if writable := util.WritablePath(); writable != "" {
		parts := append([]string{writable}, defaultQuotaCachePathParts...)
		return filepath.Join(parts...)
	}

	parts := append([]string{defaultQuotaCacheBasePath()}, defaultQuotaCachePathParts...)
	return filepath.Join(parts...)
}

func (r *quotaCacheRepository) Load() (quotaCacheSnapshot, bool, error) {
	snapshot := quotaCacheSnapshot{Version: quotaCacheVersion}
	if r == nil || strings.TrimSpace(r.path) == "" {
		return snapshot, false, nil
	}

	data, err := os.ReadFile(r.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return snapshot, false, nil
		}
		return snapshot, false, fmt.Errorf("read quota cache file: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return snapshot, true, nil
	}
	if err = json.Unmarshal(data, &snapshot); err != nil {
		return quotaCacheSnapshot{Version: quotaCacheVersion}, true, fmt.Errorf("decode quota cache file: %w", err)
	}
	if snapshot.Version == 0 {
		snapshot.Version = quotaCacheVersion
	}
	if snapshot.Entries == nil {
		snapshot.Entries = []quotaCacheEntry{}
	}
	return snapshot, true, nil
}

func (r *quotaCacheRepository) Save(snapshot quotaCacheSnapshot) error {
	if r == nil || strings.TrimSpace(r.path) == "" {
		return fmt.Errorf("quota cache path is empty")
	}
	if snapshot.Version == 0 {
		snapshot.Version = quotaCacheVersion
	}
	if snapshot.Entries == nil {
		snapshot.Entries = []quotaCacheEntry{}
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode quota cache snapshot: %w", err)
	}
	data = append(data, '\n')

	if err = os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("prepare quota cache directory: %w", err)
	}
	if err = atomicWriteQuotaCacheFile(r.path, data); err != nil {
		return fmt.Errorf("write quota cache file: %w", err)
	}
	return nil
}

func atomicWriteQuotaCacheFile(path string, data []byte) error {
	tmpFile, err := os.CreateTemp(filepath.Dir(path), "quota-cache-*.json")
	if err != nil {
		return err
	}

	tmpName := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
	}()

	if _, err = tmpFile.Write(data); err != nil {
		return err
	}
	if err = tmpFile.Chmod(0o644); err != nil {
		return err
	}
	if err = tmpFile.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
	}
	if err = os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}

func quotaCacheEntryMap(entries []quotaCacheEntry) map[string]quotaCacheEntry {
	out := make(map[string]quotaCacheEntry, len(entries))
	for _, entry := range entries {
		key := quotaCacheKey(entry.Provider, entry.Name)
		if key == "\x00" {
			continue
		}
		out[key] = entry
	}
	return out
}

func quotaCacheSnapshotWithEntries(entries []quotaCacheEntry, updatedAt time.Time) quotaCacheSnapshot {
	if entries == nil {
		entries = []quotaCacheEntry{}
	}
	return quotaCacheSnapshot{
		Version:   quotaCacheVersion,
		UpdatedAt: updatedAt,
		Entries:   entries,
	}
}
