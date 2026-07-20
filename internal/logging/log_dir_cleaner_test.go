package logging

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnforceLogDirSizeLimitDeletesOldest(t *testing.T) {
	dir := t.TempDir()

	writeLogFile(t, filepath.Join(dir, "old.log"), 60, time.Unix(1, 0))
	writeLogFile(t, filepath.Join(dir, "mid.log"), 60, time.Unix(2, 0))
	protected := filepath.Join(dir, "main.log")
	writeLogFile(t, protected, 60, time.Unix(3, 0))

	deleted, err := enforceLogDirSizeLimit(dir, 120, protected)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted file, got %d", deleted)
	}

	if _, err := os.Stat(filepath.Join(dir, "old.log")); !os.IsNotExist(err) {
		t.Fatalf("expected old.log to be removed, stat error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mid.log")); err != nil {
		t.Fatalf("expected mid.log to remain, stat error: %v", err)
	}
	if _, err := os.Stat(protected); err != nil {
		t.Fatalf("expected protected main.log to remain, stat error: %v", err)
	}
}

func TestEnforceLogDirSizeLimitSkipsProtected(t *testing.T) {
	dir := t.TempDir()

	protected := filepath.Join(dir, "main.log")
	writeLogFile(t, protected, 200, time.Unix(1, 0))
	writeLogFile(t, filepath.Join(dir, "other.log"), 50, time.Unix(2, 0))

	deleted, err := enforceLogDirSizeLimit(dir, 100, protected)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted file, got %d", deleted)
	}

	if _, err := os.Stat(protected); err != nil {
		t.Fatalf("expected protected main.log to remain, stat error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "other.log")); !os.IsNotExist(err) {
		t.Fatalf("expected other.log to be removed, stat error: %v", err)
	}
}

// TestEnforceLogDirSizeLimitDeletesTempArtifacts verifies that request-log temp files and
// request-log-parts directories are part of the size budget and can be reclaimed.
func TestEnforceLogDirSizeLimitDeletesTempArtifacts(t *testing.T) {
	dir := t.TempDir()

	writeLogFile(t, filepath.Join(dir, "request-body-aaa.tmp"), 80, time.Unix(1, 0))
	partsDir := filepath.Join(dir, "request-log-parts-api-request-xyz")
	if err := os.MkdirAll(partsDir, 0o755); err != nil {
		t.Fatalf("mkdir parts: %v", err)
	}
	writeLogFile(t, filepath.Join(partsDir, "part-1.tmp"), 80, time.Unix(1, 0))
	writeLogFile(t, filepath.Join(dir, "keep.log"), 40, time.Unix(3, 0))

	// Budget 50: both temp artifacts (80 each) must go, keep.log (40) can remain.
	deleted, err := enforceLogDirSizeLimit(dir, 50, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted < 2 {
		t.Fatalf("expected at least 2 deleted artifacts, got %d", deleted)
	}
	if _, err := os.Stat(filepath.Join(dir, "request-body-aaa.tmp")); !os.IsNotExist(err) {
		t.Fatalf("expected request-body temp to be removed, stat error: %v", err)
	}
	if _, err := os.Stat(partsDir); !os.IsNotExist(err) {
		t.Fatalf("expected request-log-parts dir to be removed, stat error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.log")); err != nil {
		t.Fatalf("expected keep.log to remain, stat error: %v", err)
	}
}

// TestCleanStaleLogArtifactsRemovesOldTempsOnly ensures age-based cleanup only touches
// temporary artifacts and never durable .log files.
func TestCleanStaleLogArtifactsRemovesOldTempsOnly(t *testing.T) {
	dir := t.TempDir()

	oldTmp := filepath.Join(dir, "response-body-old.tmp")
	writeLogFile(t, oldTmp, 10, time.Unix(1, 0))
	freshTmp := filepath.Join(dir, "response-body-fresh.tmp")
	writeLogFile(t, freshTmp, 10, time.Now())
	durable := filepath.Join(dir, "v1-chat-old.log")
	writeLogFile(t, durable, 10, time.Unix(1, 0))

	partsDir := filepath.Join(dir, "request-log-parts-api-response-old")
	if err := os.MkdirAll(partsDir, 0o755); err != nil {
		t.Fatalf("mkdir parts: %v", err)
	}
	writeLogFile(t, filepath.Join(partsDir, "part.tmp"), 10, time.Unix(1, 0))

	deleted := cleanStaleLogArtifacts(dir, time.Unix(100, 0))
	if deleted < 2 {
		t.Fatalf("expected at least 2 stale temp artifacts deleted, got %d", deleted)
	}
	if _, err := os.Stat(oldTmp); !os.IsNotExist(err) {
		t.Fatalf("expected old temp to be removed, stat error: %v", err)
	}
	if _, err := os.Stat(partsDir); !os.IsNotExist(err) {
		t.Fatalf("expected old parts dir to be removed, stat error: %v", err)
	}
	if _, err := os.Stat(freshTmp); err != nil {
		t.Fatalf("expected fresh temp to remain, stat error: %v", err)
	}
	if _, err := os.Stat(durable); err != nil {
		t.Fatalf("expected durable log to remain, stat error: %v", err)
	}
}

// TestRequestLogDiskConcurrencyBudgetDropsWhenSaturated verifies that when the concurrent
// disk-log budget is exhausted, additional non-streaming logs are skipped instead of
// blocking or creating unbounded files.
func TestRequestLogDiskConcurrencyBudgetDropsWhenSaturated(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0)
	// Replace the default semaphore with a tiny budget so the second log is dropped.
	logger.diskSem = make(chan struct{}, 1)
	logger.diskSem <- struct{}{} // pre-fill: budget already exhausted

	errLog := logger.LogRequest(
		"/v1/chat/completions",
		"POST",
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"hello":"world"}`),
		200,
		map[string][]string{"Content-Type": {"text/plain"}},
		[]byte("ok"),
		nil,
		nil,
		nil,
		nil,
		nil,
		"budget-drop",
		time.Unix(100, 0),
		time.Unix(101, 0),
	)
	if errLog != nil {
		t.Fatalf("LogRequest returned error: %v", errLog)
	}

	entries, errRead := os.ReadDir(logsDir)
	if errRead != nil {
		t.Fatalf("ReadDir: %v", errRead)
	}
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".log" {
			t.Fatalf("expected no log file when disk budget is exhausted, found %s", entry.Name())
		}
	}
}

func writeLogFile(t *testing.T, path string, size int, modTime time.Time) {
	t.Helper()

	data := make([]byte, size)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("set times: %v", err)
	}
}
