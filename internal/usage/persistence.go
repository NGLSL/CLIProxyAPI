package usage

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

const statisticsSnapshotFileVersion = 1

var defaultStatisticsSnapshotPathParts = []string{"usage", "statistics.json"}

type statisticsSnapshotPayload struct {
	Version    int                `json:"version"`
	ExportedAt time.Time          `json:"exported_at"`
	Usage      StatisticsSnapshot `json:"usage"`
}

func DefaultSnapshotPath(configFilePath string) string {
	if writable := util.WritablePath(); writable != "" {
		parts := append([]string{writable}, defaultStatisticsSnapshotPathParts...)
		return filepath.Join(parts...)
	}

	configFilePath = strings.TrimSpace(configFilePath)
	if configFilePath == "" {
		return ""
	}

	base := filepath.Dir(configFilePath)
	if info, err := os.Stat(configFilePath); err == nil && info.IsDir() {
		base = configFilePath
	}

	parts := append([]string{base}, defaultStatisticsSnapshotPathParts...)
	return filepath.Join(parts...)
}

func LoadSnapshotFromFile(path string) (StatisticsSnapshot, error) {
	var snapshot StatisticsSnapshot

	path = strings.TrimSpace(path)
	if path == "" {
		return snapshot, fmt.Errorf("usage: snapshot path is empty")
	}

	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return snapshot, err
	}

	var payload statisticsSnapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return snapshot, err
	}
	if payload.Version != 0 && payload.Version != statisticsSnapshotFileVersion {
		return snapshot, fmt.Errorf("usage: unsupported snapshot version %d", payload.Version)
	}

	return payload.Usage, nil
}

func SaveSnapshotToFile(path string, snapshot StatisticsSnapshot) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("usage: snapshot path is empty")
	}
	path = filepath.Clean(path)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := json.Marshal(statisticsSnapshotPayload{
		Version:    statisticsSnapshotFileVersion,
		ExportedAt: time.Now().UTC(),
		Usage:      snapshot,
	})
	if err != nil {
		return err
	}

	return atomicWriteFile(path, data)
}

func SaveRequestStatisticsToFile(path string, stats *RequestStatistics) error {
	if stats == nil {
		stats = NewRequestStatistics()
	}
	return SaveSnapshotToFile(path, stats.Snapshot())
}

func atomicWriteFile(path string, data []byte) error {
	tmpFile, err := os.CreateTemp(filepath.Dir(path), "usage-*.json")
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
