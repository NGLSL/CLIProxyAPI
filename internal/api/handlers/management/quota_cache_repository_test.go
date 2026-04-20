package management

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQuotaCacheRepositoryMissingFileReturnsEmptySnapshot(t *testing.T) {
	t.Parallel()

	repo := &quotaCacheRepository{path: filepath.Join(t.TempDir(), "quota-cache.json")}

	snapshot, existed, err := repo.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if existed {
		t.Fatal("expected missing file to report existed=false")
	}
	if snapshot.Version != quotaCacheVersion {
		t.Fatalf("Version = %d, want %d", snapshot.Version, quotaCacheVersion)
	}
	if !snapshot.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt = %v, want zero", snapshot.UpdatedAt)
	}
	if len(snapshot.Entries) != 0 {
		t.Fatalf("Entries length = %d, want 0", len(snapshot.Entries))
	}
}

func TestQuotaCacheRepositorySaveLoadRoundTrip(t *testing.T) {
	t.Parallel()

	repo := &quotaCacheRepository{path: filepath.Join(t.TempDir(), "nested", "quota-cache.json")}
	lastRefreshAt := time.Date(2026, 4, 20, 1, 2, 3, 0, time.UTC)
	recoverAt := time.Date(2026, 4, 20, 6, 0, 0, 0, time.UTC)
	snapshot := quotaCacheSnapshot{
		Version:   quotaCacheVersion,
		UpdatedAt: time.Date(2026, 4, 20, 2, 0, 0, 0, time.UTC),
		Entries: []quotaCacheEntry{{
			Name:            "codex.json",
			Provider:        "codex",
			AuthIndex:       "auth-1",
			Disabled:        false,
			Status:          quotaCacheStatusRateLimited,
			LastRefreshAt:   &lastRefreshAt,
			LastError:       "rate limited",
			LastErrorStatus: 429,
			QuotaRecoverAt:  &recoverAt,
			Payload:         json.RawMessage(`{"windows":[{"id":"weekly"}]}`),
		}},
	}

	if err := repo.Save(snapshot); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	loaded, existed, err := repo.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !existed {
		t.Fatal("expected saved file to report existed=true")
	}
	if loaded.Version != snapshot.Version {
		t.Fatalf("Version = %d, want %d", loaded.Version, snapshot.Version)
	}
	if !loaded.UpdatedAt.Equal(snapshot.UpdatedAt) {
		t.Fatalf("UpdatedAt = %v, want %v", loaded.UpdatedAt, snapshot.UpdatedAt)
	}
	if len(loaded.Entries) != 1 {
		t.Fatalf("Entries length = %d, want 1", len(loaded.Entries))
	}
	entry := loaded.Entries[0]
	if entry.Name != "codex.json" || entry.Provider != "codex" || entry.AuthIndex != "auth-1" {
		t.Fatalf("loaded entry identity = %#v", entry)
	}
	if entry.Status != quotaCacheStatusRateLimited || entry.LastErrorStatus != 429 || entry.LastError != "rate limited" {
		t.Fatalf("loaded entry status = %#v", entry)
	}
	if entry.LastRefreshAt == nil || !entry.LastRefreshAt.Equal(lastRefreshAt) {
		t.Fatalf("LastRefreshAt = %v, want %v", entry.LastRefreshAt, lastRefreshAt)
	}
	if entry.QuotaRecoverAt == nil || !entry.QuotaRecoverAt.Equal(recoverAt) {
		t.Fatalf("QuotaRecoverAt = %v, want %v", entry.QuotaRecoverAt, recoverAt)
	}
	var gotPayload map[string]any
	if err := json.Unmarshal(entry.Payload, &gotPayload); err != nil {
		t.Fatalf("failed to decode payload: %v", err)
	}
	windows, ok := gotPayload["windows"].([]any)
	if !ok || len(windows) != 1 {
		t.Fatalf("payload windows = %#v", gotPayload["windows"])
	}
}

func TestQuotaCacheRepositoryRejectsInvalidJSON(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "quota-cache.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatalf("failed to write invalid JSON: %v", err)
	}
	repo := &quotaCacheRepository{path: path}

	_, existed, err := repo.Load()
	if err == nil {
		t.Fatal("expected invalid JSON error")
	}
	if !existed {
		t.Fatal("expected invalid file to report existed=true")
	}
}

func TestQuotaCacheRepositoryDoesNotPersistSecretFields(t *testing.T) {
	t.Parallel()

	repo := &quotaCacheRepository{path: filepath.Join(t.TempDir(), "quota-cache.json")}
	snapshot := quotaCacheSnapshot{
		Version:   quotaCacheVersion,
		UpdatedAt: time.Date(2026, 4, 20, 2, 0, 0, 0, time.UTC),
		Entries: []quotaCacheEntry{{
			Name:      "claude.json",
			Provider:  "claude",
			AuthIndex: "auth-1",
			Status:    quotaCacheStatusFresh,
			Payload:   json.RawMessage(`{"windows":[]}`),
		}},
	}

	if err := repo.Save(snapshot); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	data, err := os.ReadFile(repo.path)
	if err != nil {
		t.Fatalf("failed to read cache file: %v", err)
	}
	serialized := string(data)
	for _, secret := range []string{"access_token", "refresh_token", "id_token", "cookie", "Authorization"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("serialized cache contains secret marker %q: %s", secret, serialized)
		}
	}
}

func TestNewQuotaCacheRepositoryPathResolution(t *testing.T) {

	writable := t.TempDir()
	t.Setenv("WRITABLE_PATH", writable)
	if got := newQuotaCacheRepository(filepath.Join(t.TempDir(), "config.yaml")).path; got != filepath.Join(writable, "CLIProxyAPI", "management", "quota-cache.json") {
		t.Fatalf("path with WRITABLE_PATH = %q", got)
	}

	t.Setenv("WRITABLE_PATH", "")
	if got := newQuotaCacheRepository(filepath.Join(t.TempDir(), "config.yaml")).path; got != filepath.Join(defaultQuotaCacheBasePath(), "CLIProxyAPI", "management", "quota-cache.json") {
		t.Fatalf("path without WRITABLE_PATH = %q", got)
	}

	if got := newQuotaCacheRepository("").path; got != filepath.Join(defaultQuotaCacheBasePath(), "CLIProxyAPI", "management", "quota-cache.json") {
		t.Fatalf("fallback path = %q", got)
	}

}
