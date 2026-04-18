package logging

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileRequestLoggerLogRequestDecompressesGzipResponseInLog(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0)

	responseText := "decoded gzip response"
	responseBody := gzipBytes(t, responseText)

	errLog := logger.LogRequest(
		"/v1/chat/completions",
		"POST",
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"hello":"world"}`),
		200,
		map[string][]string{"Content-Encoding": {"gzip"}, "Content-Type": {"text/plain"}},
		responseBody,
		nil,
		nil,
		nil,
		nil,
		nil,
		"gzip-success",
		time.Unix(100, 0),
		time.Unix(101, 0),
	)
	if errLog != nil {
		t.Fatalf("LogRequest returned error: %v", errLog)
	}

	logContent := readSingleLogFile(t, logsDir)
	if !strings.Contains(logContent, "=== RESPONSE ===\nStatus: 200\n") {
		t.Fatalf("expected response section in log, got: %s", logContent)
	}
	if !strings.Contains(logContent, "\n\ndecoded gzip response\n") {
		t.Fatalf("expected decompressed response body in log, got: %s", logContent)
	}
	if strings.Contains(logContent, string(responseBody)) {
		t.Fatalf("expected compressed bytes to stay out of log, got: %q", logContent)
	}
	if strings.Contains(logContent, "[DECOMPRESSION ERROR:") {
		t.Fatalf("expected no decompression error annotation, got: %s", logContent)
	}
	assertNoTempFilesRemain(t, logsDir)
}

func TestFileRequestLoggerLogRequestKeepsUncompressedResponseBody(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0)

	responseBody := []byte("plain response body")
	errLog := logger.LogRequest(
		"/v1/messages",
		"POST",
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"hello":"world"}`),
		201,
		map[string][]string{"Content-Type": {"text/plain"}},
		responseBody,
		nil,
		nil,
		nil,
		nil,
		nil,
		"plain-response",
		time.Unix(200, 0),
		time.Unix(201, 0),
	)
	if errLog != nil {
		t.Fatalf("LogRequest returned error: %v", errLog)
	}

	logContent := readSingleLogFile(t, logsDir)
	if !strings.Contains(logContent, "\n\nplain response body\n") {
		t.Fatalf("expected plain response body in log, got: %s", logContent)
	}
	if strings.Contains(logContent, "[DECOMPRESSION ERROR:") {
		t.Fatalf("expected no decompression error annotation, got: %s", logContent)
	}
	assertNoTempFilesRemain(t, logsDir)
}

func TestFileRequestLoggerLogRequestFallsBackToOriginalBodyOnInvalidGzip(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0)

	responseBody := []byte("not-a-valid-gzip-stream")
	errLog := logger.LogRequest(
		"/v1/responses",
		"POST",
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"hello":"world"}`),
		502,
		map[string][]string{"Content-Encoding": {"gzip"}, "Content-Type": {"application/octet-stream"}},
		responseBody,
		nil,
		nil,
		nil,
		nil,
		nil,
		"invalid-gzip",
		time.Unix(300, 0),
		time.Unix(301, 0),
	)
	if errLog != nil {
		t.Fatalf("LogRequest returned error: %v", errLog)
	}

	logContent := readSingleLogFile(t, logsDir)
	if !strings.Contains(logContent, string(responseBody)) {
		t.Fatalf("expected original response body in log, got: %s", logContent)
	}
	if !strings.Contains(logContent, "[DECOMPRESSION ERROR: failed to create gzip reader:") {
		t.Fatalf("expected decompression error annotation in log, got: %s", logContent)
	}
	assertNoTempFilesRemain(t, logsDir)
}

func TestFileRequestLoggerLogRequestPreservesLeadingNewlineResponseSemantics(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0)

	responseBody := gzipBytes(t, "\nbody starts after exactly one blank line")
	errLog := logger.LogRequest(
		"/v1/responses",
		"POST",
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"hello":"world"}`),
		200,
		map[string][]string{"Content-Encoding": {"gzip"}, "Content-Type": {"text/plain"}},
		responseBody,
		nil,
		nil,
		nil,
		nil,
		nil,
		"leading-newline",
		time.Unix(400, 0),
		time.Unix(401, 0),
	)
	if errLog != nil {
		t.Fatalf("LogRequest returned error: %v", errLog)
	}

	logContent := readSingleLogFile(t, logsDir)
	responseIndex := strings.Index(logContent, "=== RESPONSE ===\n")
	if responseIndex < 0 {
		t.Fatalf("expected response section in log, got: %s", logContent)
	}
	responseSection := logContent[responseIndex:]
	if !strings.Contains(responseSection, "Content-Type: text/plain\n\nbody starts after exactly one blank line") {
		t.Fatalf("expected response body to begin after exactly one blank line, got: %q", responseSection)
	}
	if strings.Contains(responseSection, "Content-Type: text/plain\n\n\nbody starts after exactly one blank line") {
		t.Fatalf("expected no extra blank line before response body, got: %q", responseSection)
	}
	assertNoTempFilesRemain(t, logsDir)
}

func gzipBytes(t *testing.T, value string) []byte {
	t.Helper()

	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, errWrite := writer.Write([]byte(value)); errWrite != nil {
		t.Fatalf("write gzip payload: %v", errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close gzip writer: %v", errClose)
	}
	return buffer.Bytes()
}

func readSingleLogFile(t *testing.T, logsDir string) string {
	t.Helper()

	entries, errRead := os.ReadDir(logsDir)
	if errRead != nil {
		t.Fatalf("read logs dir: %v", errRead)
	}
	var logFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) == ".log" {
			logFiles = append(logFiles, filepath.Join(logsDir, entry.Name()))
		}
	}
	if len(logFiles) != 1 {
		t.Fatalf("expected exactly one log file, got %d entries: %#v", len(logFiles), logFiles)
	}

	content, errRead := os.ReadFile(logFiles[0])
	if errRead != nil {
		t.Fatalf("read log file: %v", errRead)
	}
	return string(content)
}

func assertNoTempFilesRemain(t *testing.T, logsDir string) {
	t.Helper()

	entries, errRead := os.ReadDir(logsDir)
	if errRead != nil {
		t.Fatalf("read logs dir: %v", errRead)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("expected temp files to be cleaned up, found %s", entry.Name())
		}
	}
}
