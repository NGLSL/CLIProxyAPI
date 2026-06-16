package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/NGLSL/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type ineffectiveRefreshExecutor struct {
	provider string
}

func (e ineffectiveRefreshExecutor) Identifier() string { return e.provider }

func (e ineffectiveRefreshExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e ineffectiveRefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e ineffectiveRefreshExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	// Intentionally return refresh metadata that still evaluates as expired after a
	// successful refresh. This reproduces the ineffective-refresh loop that should
	// now be throttled by refreshIneffectiveBackoff.
	return &Auth{
		ID:       "auth-refresh-ineffective",
		Provider: e.provider,
		Metadata: map[string]any{
			"email":                    "x@example.com",
			"refresh_interval_seconds": 3600,
			"expires_at":               time.Now().Add(-time.Minute).Format(time.RFC3339),
		},
	}, nil
}

func (e ineffectiveRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e ineffectiveRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type successfulRefreshExecutor struct {
	provider string
}

func (e successfulRefreshExecutor) Identifier() string { return e.provider }

func (e successfulRefreshExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e successfulRefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e successfulRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "new-access-token"
	return auth, nil
}

func (e successfulRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e successfulRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type metadataMutatingExecutor struct {
	provider string
}

func (e metadataMutatingExecutor) Identifier() string { return e.provider }

func (e metadataMutatingExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["refresh_token"] = "new-refresh-token"
	return cliproxyexecutor.Response{}, nil
}

func (e metadataMutatingExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e metadataMutatingExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, nil
}

func (e metadataMutatingExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e metadataMutatingExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type captureAuthStore struct {
	saved []*Auth
}

func (s *captureAuthStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *captureAuthStore) Save(_ context.Context, auth *Auth) (string, error) {
	if auth != nil {
		s.saved = append(s.saved, auth.Clone())
	}
	return "", nil
}

func (s *captureAuthStore) Delete(context.Context, string) error { return nil }

func TestManager_Execute_PersistsExecutorMetadataChanges(t *testing.T) {
	ctx := context.Background()
	store := &captureAuthStore{}
	m := NewManager(store, nil, nil)
	m.scheduler = nil
	m.RegisterExecutor(metadataMutatingExecutor{provider: "claude"})

	if _, errRegister := m.Register(ctx, &Auth{
		ID:       "auth-metadata-refresh",
		Provider: "claude",
		Metadata: map[string]any{
			"email":         "x@example.com",
			"refresh_token": "old-refresh-token",
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	store.saved = nil

	_, errExecute := m.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute: %v", errExecute)
	}
	if len(store.saved) == 0 {
		t.Fatal("expected persisted auth after metadata change")
	}
	lastSaved := store.saved[len(store.saved)-1]
	if got := lastSaved.Metadata["refresh_token"]; got != "new-refresh-token" {
		t.Fatalf("persisted refresh_token = %v, want new-refresh-token", got)
	}
}

func TestManager_Update_PreservesModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	model := "test-model"
	backoffLevel := 7
	now := time.Now().UTC()
	nextRetryAfter := now.Add(10 * time.Minute)
	nextRecoverAt := now.Add(20 * time.Minute)

	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{"k": "v"},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				StatusMessage:  "cooldown",
				Unavailable:    true,
				NextRetryAfter: nextRetryAfter,
				LastError:      &Error{Code: "rate_limit", Message: "retry later", Retryable: true, HTTPStatus: 429},
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: nextRecoverAt, BackoffLevel: backoffLevel},
				UpdatedAt:      now,
			},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	if _, errUpdate := m.Update(context.Background(), &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{"k": "v2"},
	}); errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}

	updated, ok := m.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) == 0 {
		t.Fatalf("expected ModelStates to be preserved")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if state.Quota.BackoffLevel != backoffLevel {
		t.Fatalf("expected BackoffLevel to be %d, got %d", backoffLevel, state.Quota.BackoffLevel)
	}
}

func TestManager_Update_DisabledExistingDoesNotInheritModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	// Register a disabled auth with existing ModelStates.
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-disabled",
		Provider: "claude",
		Disabled: true,
		Status:   StatusDisabled,
		ModelStates: map[string]*ModelState{
			"stale-model": {
				Quota: QuotaState{BackoffLevel: 5},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Update with empty ModelStates — should NOT inherit stale states.
	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-disabled",
		Provider: "claude",
		Disabled: true,
		Status:   StatusDisabled,
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-disabled")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected disabled auth NOT to inherit ModelStates, got %d entries", len(updated.ModelStates))
	}
}

func TestManager_Update_ActiveToDisabledDoesNotInheritModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	// Register an active auth with ModelStates (simulates existing live auth).
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-a2d",
		Provider: "claude",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			"stale-model": {
				Quota: QuotaState{BackoffLevel: 9},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// File watcher deletes config → synthesizes Disabled=true auth → Update.
	// Even though existing is active, incoming auth is disabled → skip inheritance.
	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-a2d",
		Provider: "claude",
		Disabled: true,
		Status:   StatusDisabled,
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-a2d")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected active→disabled transition NOT to inherit ModelStates, got %d entries", len(updated.ModelStates))
	}
}

func TestManager_Update_DisabledToActiveDoesNotInheritStaleModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	// Register a disabled auth with stale ModelStates.
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-d2a",
		Provider: "claude",
		Disabled: true,
		Status:   StatusDisabled,
		ModelStates: map[string]*ModelState{
			"stale-model": {
				Quota: QuotaState{BackoffLevel: 4},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Re-enable: incoming auth is active, existing is disabled → skip inheritance.
	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-d2a",
		Provider: "claude",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-d2a")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected disabled→active transition NOT to inherit stale ModelStates, got %d entries", len(updated.ModelStates))
	}
}

func TestManager_Update_ActiveInheritsModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	model := "active-model"
	backoffLevel := 3
	now := time.Now().UTC()
	nextRetryAfter := now.Add(5 * time.Minute)
	nextRecoverAt := now.Add(15 * time.Minute)

	// Register an active auth with ModelStates.
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-active",
		Provider: "claude",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				StatusMessage:  "cooldown",
				Unavailable:    true,
				NextRetryAfter: nextRetryAfter,
				LastError:      &Error{Code: "model_rate_limit", Message: "busy", Retryable: true, HTTPStatus: 429},
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: nextRecoverAt, BackoffLevel: backoffLevel},
				UpdatedAt:      now,
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Update with empty ModelStates — both sides active → SHOULD inherit.
	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-active",
		Provider: "claude",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-active")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) == 0 {
		t.Fatalf("expected active auth to inherit ModelStates")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if state.Quota.BackoffLevel != backoffLevel {
		t.Fatalf("expected BackoffLevel to be %d, got %d", backoffLevel, state.Quota.BackoffLevel)
	}
}

func TestManager_RefreshAuth_ClearsInheritedRuntimeBlockAfterSuccessfulRefresh(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := successfulRefreshExecutor{provider: "claude"}
	m.RegisterExecutor(executor)

	now := time.Now().UTC()
	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:       "auth-refresh-success",
		Provider: "claude",
		Status:   StatusError,
		Metadata: map[string]any{
			"access_token": "old-access-token",
		},
		ModelStates: map[string]*ModelState{
			"claude-sonnet": {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: now.Add(30 * time.Minute),
				LastError:      &Error{Message: "unauthorized", HTTPStatus: http.StatusUnauthorized},
				UpdatedAt:      now,
			},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	m.refreshAuth(context.Background(), "auth-refresh-success")

	updated, ok := m.GetByID("auth-refresh-success")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present after refresh")
	}
	if got := updated.Metadata["access_token"]; got != "new-access-token" {
		t.Fatalf("access_token = %v, want new-access-token", got)
	}
	if updated.Unavailable || !updated.NextRetryAfter.IsZero() || len(updated.ModelStates) != 0 {
		t.Fatalf("expected successful refresh to clear runtime block, got unavailable=%v next_retry=%s model_states=%d", updated.Unavailable, updated.NextRetryAfter, len(updated.ModelStates))
	}
	if updated.LastRefreshedAt.IsZero() {
		t.Fatal("expected LastRefreshedAt to be set")
	}
}

func TestManager_RefreshAuth_SetsBackoffWhenRefreshIsIneffective(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := ineffectiveRefreshExecutor{provider: "claude"}
	m.RegisterExecutor(executor)

	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:       "auth-refresh-ineffective",
		Provider: "claude",
		Metadata: map[string]any{
			"email":                    "x@example.com",
			"refresh_interval_seconds": 3600,
			"expires_at":               time.Now().Add(-time.Minute).Format(time.RFC3339),
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	startedAt := time.Now()
	m.refreshAuth(context.Background(), "auth-refresh-ineffective")
	finishedAt := time.Now()

	updated, ok := m.GetByID("auth-refresh-ineffective")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present after refresh")
	}
	if updated.NextRefreshAfter.IsZero() {
		t.Fatal("expected NextRefreshAfter to be set for ineffective refresh")
	}

	minWant := startedAt.Add(refreshIneffectiveBackoff)
	maxWant := finishedAt.Add(refreshIneffectiveBackoff)
	if updated.NextRefreshAfter.Before(minWant) || updated.NextRefreshAfter.After(maxWant) {
		t.Fatalf("expected NextRefreshAfter in [%s, %s], got %s", minWant, maxWant, updated.NextRefreshAfter)
	}
}
