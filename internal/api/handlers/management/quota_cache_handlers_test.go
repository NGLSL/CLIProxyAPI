package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/gin-gonic/gin"
)

type stubQuotaProbeResult struct {
	payload map[string]any
	status  quotaCacheStatus
	err     error
}

func TestGetQuotaCacheReturnsLoadedSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, coreauth.NewManager(&memoryAuthStore{}, nil, nil))
	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	h.quotaCache.mu.Lock()
	h.quotaCache.snapshot = quotaCacheSnapshot{
		Version:   quotaCacheVersion,
		UpdatedAt: now,
		Entries: []quotaCacheEntry{{
			Name:      "claude.json",
			Provider:  "claude",
			AuthIndex: "idx-1",
			Status:    quotaCacheStatusFresh,
			Payload:   mustMarshalQuotaPayload(t, map[string]any{"windows": []any{}}),
		}},
	}
	h.quotaCache.mu.Unlock()

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/quota-cache", nil)

	h.GetQuotaCache(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var got quotaCacheSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Version != quotaCacheVersion {
		t.Fatalf("version = %d, want %d", got.Version, quotaCacheVersion)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("entries len = %d, want 1", len(got.Entries))
	}
	if got.Entries[0].Name != "claude.json" {
		t.Fatalf("entry name = %q, want claude.json", got.Entries[0].Name)
	}
}

func TestRefreshQuotaCacheRejectsDisabledAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	auth := &coreauth.Auth{
		ID:       "disabled-auth",
		Provider: "claude",
		FileName: "disabled.json",
		Disabled: true,
		Status:   coreauth.StatusDisabled,
	}
	auth.EnsureIndex()
	if _, err := manager.Register(nil, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)

	body := quotaCacheRefreshRequest{
		AuthIndexes: []string{auth.Index},
		Force:       true,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/quota-cache/refresh", bytes.NewReader(bodyBytes))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.RefreshQuotaCache(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestRefreshQuotaCacheUpdatesEntriesFromStubbedProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	auth := &coreauth.Auth{
		ID:       "claude-auth",
		Provider: "claude",
		FileName: "claude.json",
		Status:   coreauth.StatusActive,
	}
	auth.EnsureIndex()
	if _, err := manager.Register(nil, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	h.quotaCache.probe = func(_ *quotaCacheService, _ any, target quotaRefreshTarget) quotaProbeResult {
		if target.AuthIndex != auth.Index {
			t.Fatalf("target auth index = %q, want %q", target.AuthIndex, auth.Index)
		}
		return quotaProbeResult{
			Payload: mustMarshalQuotaPayload(t, map[string]any{
				"windows": []map[string]any{{
					"id":          "five-hour",
					"label":       "five-hour",
					"labelKey":    "claude_quota.five_hour",
					"usedPercent": 42,
					"resetLabel":  "2026-04-20T18:00:00Z",
				}},
				"planType": "plan_pro",
			}),
		}
	}

	body := quotaCacheRefreshRequest{
		AuthIndexes: []string{auth.Index},
		Force:       true,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/quota-cache/refresh", bytes.NewReader(bodyBytes))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.RefreshQuotaCache(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var got quotaCacheRefreshResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("entries len = %d, want 1", len(got.Entries))
	}
	if got.Entries[0].Status != quotaCacheStatusFresh {
		t.Fatalf("entry status = %q, want %q", got.Entries[0].Status, quotaCacheStatusFresh)
	}
	if !bytes.Contains(got.Entries[0].Payload, []byte(`"planType":"plan_pro"`)) {
		t.Fatalf("payload = %s, want planType", string(got.Entries[0].Payload))
	}

	snapshot := h.quotaCache.Snapshot()
	if len(snapshot.Entries) != 1 {
		t.Fatalf("snapshot entries len = %d, want 1", len(snapshot.Entries))
	}
	if snapshot.Entries[0].Status != quotaCacheStatusFresh {
		t.Fatalf("snapshot entry status = %q, want %q", snapshot.Entries[0].Status, quotaCacheStatusFresh)
	}
}

func TestQuotaCacheRefreshKeepsPreviousPayloadOnGenericError(t *testing.T) {
	t.Setenv("WRITABLE_PATH", t.TempDir())

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	auth := &coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		FileName: "codex.json",
		Status:   coreauth.StatusActive,
	}
	auth.EnsureIndex()
	if _, err := manager.Register(nil, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	service := newQuotaCacheService(&config.Config{}, filepath.Join(t.TempDir(), "config.yaml"), manager)
	previousPayload := mustMarshalQuotaPayload(t, map[string]any{"windows": []map[string]any{{"id": "weekly"}}})
	service.snapshot = quotaCacheSnapshotWithEntries([]quotaCacheEntry{{
		Name:          auth.FileName,
		Provider:      supportedQuotaProvider(auth),
		AuthIndex:     auth.Index,
		Status:        quotaCacheStatusFresh,
		LastRefreshAt: timePointer(time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)),
		Payload:       previousPayload,
	}}, time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC))
	service.probe = func(_ *quotaCacheService, _ any, target quotaRefreshTarget) quotaProbeResult {
		if target.AuthIndex != auth.Index {
			t.Fatalf("target auth index = %q, want %q", target.AuthIndex, auth.Index)
		}
		return quotaProbeResult{Err: http.ErrHandlerTimeout}
	}

	response, err := service.Refresh(nil, quotaCacheRefreshRequest{AuthIndexes: []string{auth.Index}, Force: true})
	if err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	if len(response.Entries) != 1 {
		t.Fatalf("response entries len = %d, want 1", len(response.Entries))
	}
	entry := response.Entries[0]
	if entry.Status != quotaCacheStatusError {
		t.Fatalf("status = %q, want %q", entry.Status, quotaCacheStatusError)
	}
	if !bytes.Equal(entry.Payload, previousPayload) {
		t.Fatalf("payload = %s, want %s", string(entry.Payload), string(previousPayload))
	}

	snapshot := service.Snapshot()
	if len(snapshot.Entries) != 1 {
		t.Fatalf("snapshot entries len = %d, want 1", len(snapshot.Entries))
	}
	if !bytes.Equal(snapshot.Entries[0].Payload, previousPayload) {
		t.Fatalf("snapshot payload = %s, want %s", string(snapshot.Entries[0].Payload), string(previousPayload))
	}
}

func TestQuotaCacheRefreshUpdatesMemoryWhenSaveFails(t *testing.T) {
	t.Setenv("WRITABLE_PATH", t.TempDir())

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	auth := &coreauth.Auth{
		ID:       "claude-auth",
		Provider: "claude",
		FileName: "claude.json",
		Status:   coreauth.StatusActive,
	}
	auth.EnsureIndex()
	if _, err := manager.Register(nil, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	service := newQuotaCacheService(&config.Config{}, filepath.Join(t.TempDir(), "config.yaml"), manager)
	service.repo = &quotaCacheRepository{}
	service.probe = func(_ *quotaCacheService, _ any, _ quotaRefreshTarget) quotaProbeResult {
		return quotaProbeResult{Payload: mustMarshalQuotaPayload(t, map[string]any{"windows": []any{}})}
	}

	_, err := service.Refresh(nil, quotaCacheRefreshRequest{AuthIndexes: []string{auth.Index}, Force: true})
	if err == nil {
		t.Fatal("expected refresh to return save error")
	}

	snapshot := service.Snapshot()
	if len(snapshot.Entries) != 1 {
		t.Fatalf("snapshot entries len = %d, want 1", len(snapshot.Entries))
	}
	if snapshot.Entries[0].Status != quotaCacheStatusFresh {
		t.Fatalf("status = %q, want %q", snapshot.Entries[0].Status, quotaCacheStatusFresh)
	}
	if len(snapshot.Entries[0].Payload) == 0 {
		t.Fatal("expected in-memory payload to be updated even when save fails")
	}
}

func TestQuotaCacheSchedulerRunsImmediateRefreshOnStartupWhenCacheMissing(t *testing.T) {
	t.Setenv("WRITABLE_PATH", t.TempDir())

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	auth := &coreauth.Auth{
		ID:       "gemini-auth",
		Provider: "gemini-cli",
		FileName: "gemini.json",
		Status:   coreauth.StatusActive,
	}
	auth.EnsureIndex()
	if _, err := manager.Register(nil, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	service := newQuotaCacheService(&config.Config{}, filepath.Join(t.TempDir(), "config.yaml"), manager)
	probeCalled := make(chan struct{}, 1)
	service.probe = func(_ *quotaCacheService, _ any, target quotaRefreshTarget) quotaProbeResult {
		if target.AuthIndex != auth.Index {
			t.Fatalf("target auth index = %q, want %q", target.AuthIndex, auth.Index)
		}
		select {
		case probeCalled <- struct{}{}:
		default:
		}
		return quotaProbeResult{Payload: mustMarshalQuotaPayload(t, map[string]any{"buckets": []any{}})}
	}

	scheduler := newQuotaCacheScheduler(service)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go scheduler.run(ctx, done)

	select {
	case <-probeCalled:
		cancel()
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("expected scheduler to run an immediate automatic refresh when cache is missing")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not stop after context cancellation")
	}
}

func TestQuotaCacheSchedulerDoesNotRunImmediateRefreshWhenCacheExists(t *testing.T) {
	writablePath := t.TempDir()
	t.Setenv("WRITABLE_PATH", writablePath)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	auth := &coreauth.Auth{
		ID:       "gemini-auth",
		Provider: "gemini-cli",
		FileName: "gemini.json",
		Status:   coreauth.StatusActive,
	}
	auth.EnsureIndex()
	if _, err := manager.Register(nil, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	configDir := t.TempDir()
	repo := &quotaCacheRepository{path: filepath.Join(writablePath, "CLIProxyAPI", "management", quotaCacheFileName)}
	if err := repo.Save(quotaCacheSnapshotWithEntries([]quotaCacheEntry{{
		Name:          auth.FileName,
		Provider:      supportedQuotaProvider(auth),
		AuthIndex:     auth.Index,
		Status:        quotaCacheStatusFresh,
		LastRefreshAt: timePointer(time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)),
		Payload:       mustMarshalQuotaPayload(t, map[string]any{"buckets": []any{}}),
	}}, time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC))); err != nil {
		t.Fatalf("save cache snapshot: %v", err)
	}

	service := newQuotaCacheService(&config.Config{}, filepath.Join(configDir, "config.yaml"), manager)
	probeCalled := make(chan struct{}, 1)
	service.probe = func(_ *quotaCacheService, _ any, _ quotaRefreshTarget) quotaProbeResult {
		select {
		case probeCalled <- struct{}{}:
		default:
		}
		return quotaProbeResult{Payload: mustMarshalQuotaPayload(t, map[string]any{"buckets": []any{}})}
	}

	scheduler := newQuotaCacheScheduler(service)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go scheduler.run(ctx, done)

	select {
	case <-probeCalled:
		cancel()
		t.Fatal("expected scheduler not to run immediate refresh when cache already exists")
	case <-time.After(200 * time.Millisecond):
		cancel()
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not stop after context cancellation")
	}
}

func TestQuotaCacheSchedulerRecalculatesWaitAfterConfigChange(t *testing.T) {
	writablePath := t.TempDir()
	t.Setenv("WRITABLE_PATH", writablePath)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	auth := &coreauth.Auth{
		ID:       "gemini-auth",
		Provider: "gemini-cli",
		FileName: "gemini.json",
		Status:   coreauth.StatusActive,
	}
	auth.EnsureIndex()
	if _, err := manager.Register(nil, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	now := time.Now().UTC()
	repo := &quotaCacheRepository{path: filepath.Join(writablePath, "CLIProxyAPI", "management", quotaCacheFileName)}
	if err := repo.Save(quotaCacheSnapshotWithEntries([]quotaCacheEntry{{
		Name:          auth.FileName,
		Provider:      supportedQuotaProvider(auth),
		AuthIndex:     auth.Index,
		Status:        quotaCacheStatusFresh,
		LastRefreshAt: timePointer(now.Add(-120 * time.Millisecond)),
		Payload:       mustMarshalQuotaPayload(t, map[string]any{"buckets": []any{}}),
	}}, now.Add(-120*time.Millisecond))); err != nil {
		t.Fatalf("save cache snapshot: %v", err)
	}

	h := NewHandler(&config.Config{QuotaCacheRefreshInterval: 2}, filepath.Join(t.TempDir(), "config.yaml"), manager)
	probeCalled := make(chan time.Time, 2)
	h.quotaCache.probe = func(_ *quotaCacheService, _ any, target quotaRefreshTarget) quotaProbeResult {
		if target.AuthIndex != auth.Index {
			t.Fatalf("target auth index = %q, want %q", target.AuthIndex, auth.Index)
		}
		select {
		case probeCalled <- time.Now():
		default:
		}
		return quotaProbeResult{Payload: mustMarshalQuotaPayload(t, map[string]any{"buckets": []any{}})}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer h.StopQuotaCacheScheduler(context.Background())
	start := time.Now()
	h.StartQuotaCacheScheduler(ctx)

	select {
	case calledAt := <-probeCalled:
		t.Fatalf("expected scheduler to keep waiting before config change, but refreshed after %v", calledAt.Sub(start))
	case <-time.After(200 * time.Millisecond):
	}

	changeAt := time.Now()
	h.SetConfig(&config.Config{QuotaCacheRefreshInterval: 1})

	select {
	case calledAt := <-probeCalled:
		if calledAt.Sub(changeAt) >= 1300*time.Millisecond {
			t.Fatalf("expected config change to use the new interval, refresh happened %v after change", calledAt.Sub(changeAt))
		}
		if calledAt.Sub(start) >= 1800*time.Millisecond {
			t.Fatalf("expected config change to interrupt the old wait, refresh happened after %v", calledAt.Sub(start))
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("expected scheduler to refresh soon after config change")
	}
}

func TestBuildCodexQuotaWindowsClassifiesWindowsByDuration(t *testing.T) {
	windows := buildCodexQuotaWindows(map[string]any{
		"rate_limit": map[string]any{
			"primary_window": map[string]any{
				"used_percent":         84,
				"limit_window_seconds": 604800,
				"reset_at":             1770000000,
			},
			"secondary_window": map[string]any{
				"used_percent":         21,
				"limit_window_seconds": 18000,
				"reset_at":             1760000000,
			},
		},
		"code_review_rate_limit": map[string]any{
			"primary_window": map[string]any{
				"used_percent":         66,
				"limit_window_seconds": 604800,
				"reset_after_seconds":  7200,
			},
			"secondary_window": map[string]any{
				"used_percent":         13,
				"limit_window_seconds": 18000,
				"reset_after_seconds":  1800,
			},
		},
		"additional_rate_limits": []any{
			map[string]any{
				"limit_name": "Responses API",
				"rate_limit": map[string]any{
					"primary_window": map[string]any{
						"used_percent":         92,
						"limit_window_seconds": 604800,
					},
					"secondary_window": map[string]any{
						"used_percent":         31,
						"limit_window_seconds": 18000,
					},
				},
			},
		},
	})

	if got := quotaWindowByID(windows, "five-hour"); got == nil {
		t.Fatal("missing five-hour window")
	} else if usedPercent := quotaWindowUsedPercent(t, got); usedPercent != 21 {
		t.Fatalf("five-hour usedPercent = %v, want 21", usedPercent)
	}
	if got := quotaWindowByID(windows, "weekly"); got == nil {
		t.Fatal("missing weekly window")
	} else if usedPercent := quotaWindowUsedPercent(t, got); usedPercent != 84 {
		t.Fatalf("weekly usedPercent = %v, want 84", usedPercent)
	}
	if got := quotaWindowByID(windows, "code-review-five-hour"); got == nil {
		t.Fatal("missing code-review-five-hour window")
	} else if usedPercent := quotaWindowUsedPercent(t, got); usedPercent != 13 {
		t.Fatalf("code-review-five-hour usedPercent = %v, want 13", usedPercent)
	}
	if got := quotaWindowByID(windows, "code-review-weekly"); got == nil {
		t.Fatal("missing code-review-weekly window")
	} else if usedPercent := quotaWindowUsedPercent(t, got); usedPercent != 66 {
		t.Fatalf("code-review-weekly usedPercent = %v, want 66", usedPercent)
	}
	if got := quotaWindowByID(windows, "responses-api-five-hour-0"); got == nil {
		t.Fatal("missing responses-api-five-hour-0 window")
	} else if usedPercent := quotaWindowUsedPercent(t, got); usedPercent != 31 {
		t.Fatalf("responses-api-five-hour-0 usedPercent = %v, want 31", usedPercent)
	}
	if got := quotaWindowByID(windows, "responses-api-weekly-0"); got == nil {
		t.Fatal("missing responses-api-weekly-0 window")
	} else if usedPercent := quotaWindowUsedPercent(t, got); usedPercent != 92 {
		t.Fatalf("responses-api-weekly-0 usedPercent = %v, want 92", usedPercent)
	}
}

func TestBuildCodexQuotaWindowsFallsBackToWindowOrderWithoutDuration(t *testing.T) {
	windows := buildCodexQuotaWindows(map[string]any{
		"rate_limit": map[string]any{
			"primary_window": map[string]any{
				"used_percent":        44,
				"reset_after_seconds": 300,
			},
			"secondary_window": map[string]any{
				"used_percent":        88,
				"reset_after_seconds": 600,
			},
		},
	})

	if len(windows) != 2 {
		t.Fatalf("windows len = %d, want 2", len(windows))
	}
	if got := quotaWindowByID(windows, "five-hour"); got == nil {
		t.Fatal("missing five-hour window")
	} else if usedPercent := quotaWindowUsedPercent(t, got); usedPercent != 44 {
		t.Fatalf("five-hour usedPercent = %v, want 44", usedPercent)
	}
	if got := quotaWindowByID(windows, "weekly"); got == nil {
		t.Fatal("missing weekly window")
	} else if usedPercent := quotaWindowUsedPercent(t, got); usedPercent != 88 {
		t.Fatalf("weekly usedPercent = %v, want 88", usedPercent)
	}
}

func TestBuildCodexQuotaWindowsDoesNotFallbackUnexpectedDuration(t *testing.T) {
	t.Run("primary unexpected duration does not become five-hour", func(t *testing.T) {
		windows := buildCodexQuotaWindows(map[string]any{
			"rate_limit": map[string]any{
				"primary_window": map[string]any{
					"used_percent":         44,
					"limit_window_seconds": 86400,
				},
				"secondary_window": map[string]any{
					"used_percent":         88,
					"limit_window_seconds": 604800,
				},
			},
		})

		if got := quotaWindowByID(windows, "five-hour"); got != nil {
			t.Fatalf("unexpected five-hour window: %#v", got)
		}
		if got := quotaWindowByID(windows, "weekly"); got == nil {
			t.Fatal("missing weekly window")
		} else if usedPercent := quotaWindowUsedPercent(t, got); usedPercent != 88 {
			t.Fatalf("weekly usedPercent = %v, want 88", usedPercent)
		}
	})

	t.Run("secondary unexpected duration does not become weekly", func(t *testing.T) {
		windows := buildCodexQuotaWindows(map[string]any{
			"rate_limit": map[string]any{
				"primary_window": map[string]any{
					"used_percent":         11,
					"limit_window_seconds": 18000,
				},
				"secondary_window": map[string]any{
					"used_percent":         77,
					"limit_window_seconds": 86400,
				},
			},
		})

		if got := quotaWindowByID(windows, "five-hour"); got == nil {
			t.Fatal("missing five-hour window")
		} else if usedPercent := quotaWindowUsedPercent(t, got); usedPercent != 11 {
			t.Fatalf("five-hour usedPercent = %v, want 11", usedPercent)
		}
		if got := quotaWindowByID(windows, "weekly"); got != nil {
			t.Fatalf("unexpected weekly window: %#v", got)
		}
	})
}

func quotaWindowByID(windows []map[string]any, id string) map[string]any {
	for _, window := range windows {
		if quotaString(window["id"]) == id {
			return window
		}
	}
	return nil
}

func quotaWindowUsedPercent(t *testing.T, window map[string]any) float64 {
	t.Helper()
	usedPercent, ok := quotaFloat64(window["usedPercent"])
	if !ok {
		t.Fatalf("missing usedPercent in window %#v", window)
	}
	return usedPercent
}

func mustMarshalQuotaPayload(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return json.RawMessage(data)
}
