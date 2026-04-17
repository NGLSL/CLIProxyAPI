package usage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestDefaultSnapshotPathPrefersWritablePath(t *testing.T) {
	t.Setenv("WRITABLE_PATH", filepath.Join(t.TempDir(), "writable"))

	got := DefaultSnapshotPath(filepath.Join(t.TempDir(), "config.yaml"))
	want := filepath.Join(os.Getenv("WRITABLE_PATH"), "usage", "statistics.json")
	if got != want {
		t.Fatalf("DefaultSnapshotPath() = %q, want %q", got, want)
	}
}

func TestDefaultSnapshotPathFallsBackToConfigDirectory(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	got := DefaultSnapshotPath(configPath)
	want := filepath.Join(filepath.Dir(configPath), "usage", "statistics.json")
	if got != want {
		t.Fatalf("DefaultSnapshotPath() = %q, want %q", got, want)
	}
}

func TestLoadSnapshotFromFileMissingFileReturnsNotExist(t *testing.T) {
	_, err := LoadSnapshotFromFile(filepath.Join(t.TempDir(), "missing.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadSnapshotFromFile() error = %v, want os.ErrNotExist", err)
	}
}

func TestSaveAndLoadSnapshotRoundTrip(t *testing.T) {
	stats := NewRequestStatistics()
	timestamp := time.Date(2026, 4, 17, 9, 30, 0, 0, time.UTC)
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: timestamp,
		Failed:      true,
		Latency:     1400 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  12,
			OutputTokens: 30,
			TotalTokens:  42,
		},
	})

	path := filepath.Join(t.TempDir(), "usage", "statistics.json")
	if err := SaveRequestStatisticsToFile(path, stats); err != nil {
		t.Fatalf("SaveRequestStatisticsToFile() error = %v", err)
	}

	loaded, err := LoadSnapshotFromFile(path)
	if err != nil {
		t.Fatalf("LoadSnapshotFromFile() error = %v", err)
	}

	merged := NewRequestStatistics()
	result := merged.MergeSnapshot(loaded)
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("MergeSnapshot() = %+v, want added=1 skipped=0", result)
	}

	snapshot := merged.Snapshot()
	if snapshot.TotalRequests != 1 {
		t.Fatalf("total requests = %d, want 1", snapshot.TotalRequests)
	}
	if snapshot.FailureCount != 1 {
		t.Fatalf("failure count = %d, want 1", snapshot.FailureCount)
	}
	if snapshot.TotalTokens != 42 {
		t.Fatalf("total tokens = %d, want 42", snapshot.TotalTokens)
	}
	detail := snapshot.APIs["test-key"].Models["gpt-5.4"].Details[0]
	if detail.LatencyMs != 1400 {
		t.Fatalf("latency ms = %d, want 1400", detail.LatencyMs)
	}
	if !detail.Failed {
		t.Fatal("failed = false, want true")
	}
}

func TestSaveSnapshotToFileOverwritesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage", "statistics.json")
	first := StatisticsSnapshot{TotalRequests: 1, TotalTokens: 10}
	second := StatisticsSnapshot{TotalRequests: 2, TotalTokens: 20}

	if err := SaveSnapshotToFile(path, first); err != nil {
		t.Fatalf("first SaveSnapshotToFile() error = %v", err)
	}
	if err := SaveSnapshotToFile(path, second); err != nil {
		t.Fatalf("second SaveSnapshotToFile() error = %v", err)
	}

	loaded, err := LoadSnapshotFromFile(path)
	if err != nil {
		t.Fatalf("LoadSnapshotFromFile() error = %v", err)
	}
	if loaded.TotalRequests != second.TotalRequests {
		t.Fatalf("total requests = %d, want %d", loaded.TotalRequests, second.TotalRequests)
	}
	if loaded.TotalTokens != second.TotalTokens {
		t.Fatalf("total tokens = %d, want %d", loaded.TotalTokens, second.TotalTokens)
	}
}

func TestLoadSnapshotFromFileRejectsUnsupportedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage", "statistics.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data := []byte(`{"version":999,"usage":{"total_requests":3}}`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := LoadSnapshotFromFile(path)
	if err == nil {
		t.Fatal("LoadSnapshotFromFile() error = nil, want error")
	}
}

func TestSaveRequestStatisticsToFileWithNilStatsWritesEmptySnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage", "statistics.json")
	if err := SaveRequestStatisticsToFile(path, nil); err != nil {
		t.Fatalf("SaveRequestStatisticsToFile() error = %v", err)
	}

	loaded, err := LoadSnapshotFromFile(path)
	if err != nil {
		t.Fatalf("LoadSnapshotFromFile() error = %v", err)
	}
	if loaded.TotalRequests != 0 || loaded.TotalTokens != 0 {
		t.Fatalf("loaded snapshot = %+v, want empty snapshot", loaded)
	}
}
