package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeSnapshotSaveAndLoadRoundTrip(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "runtime", "auth-state.json")
	now := time.Date(2026, time.April, 18, 12, 0, 0, 0, time.UTC)
	nextRetryAfter := now.Add(15 * time.Minute)
	nextRecoverAt := now.Add(30 * time.Minute)

	snapshot := RuntimeSnapshot{Auths: map[string]*AuthRuntimeState{
		"auth-1": {
			Status:         StatusError,
			StatusMessage:  "cooldown",
			Unavailable:    true,
			Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: nextRecoverAt, BackoffLevel: 3},
			LastError:      &Error{Code: "rate_limit", Message: "retry later", Retryable: true, HTTPStatus: 429},
			UpdatedAt:      now,
			NextRetryAfter: nextRetryAfter,
			ModelStates: map[string]*ModelState{
				"gpt-5": {
					Status:         StatusError,
					StatusMessage:  "model cooldown",
					Unavailable:    true,
					NextRetryAfter: nextRetryAfter,
					LastError:      &Error{Code: "model_rate_limit", Message: "busy", Retryable: true, HTTPStatus: 429},
					Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: nextRecoverAt, BackoffLevel: 2},
					UpdatedAt:      now,
				},
			},
		},
	}}

	if err := SaveRuntimeSnapshotToFile(path, snapshot); err != nil {
		t.Fatalf("SaveRuntimeSnapshotToFile() error = %v", err)
	}

	loaded, err := LoadRuntimeSnapshotFromFile(path)
	if err != nil {
		t.Fatalf("LoadRuntimeSnapshotFromFile() error = %v", err)
	}
	if loaded.Len() != 1 {
		t.Fatalf("loaded.Len() = %d, want 1", loaded.Len())
	}
	state := loaded.Auths["auth-1"]
	if state == nil {
		t.Fatalf("expected auth-1 state to be present")
	}
	if state.Status != StatusError {
		t.Fatalf("state.Status = %v, want %v", state.Status, StatusError)
	}
	if !state.Unavailable {
		t.Fatalf("state.Unavailable = false, want true")
	}
	if !state.NextRetryAfter.Equal(nextRetryAfter) {
		t.Fatalf("state.NextRetryAfter = %v, want %v", state.NextRetryAfter, nextRetryAfter)
	}
	if state.LastError == nil || state.LastError.Code != "rate_limit" {
		t.Fatalf("state.LastError = %#v, want rate_limit", state.LastError)
	}
	modelState := state.ModelStates["gpt-5"]
	if modelState == nil {
		t.Fatalf("expected model state to be present")
	}
	if !modelState.Unavailable {
		t.Fatalf("modelState.Unavailable = false, want true")
	}
	if modelState.Quota.BackoffLevel != 2 {
		t.Fatalf("modelState.Quota.BackoffLevel = %d, want 2", modelState.Quota.BackoffLevel)
	}
}

func TestManagerExportRuntimeSnapshotSkipsCleanAuths(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	now := time.Date(2026, time.April, 18, 12, 0, 0, 0, time.UTC)

	if _, err := manager.Register(context.Background(), &Auth{
		ID:       "clean-auth",
		Provider: "claude",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("register clean auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), &Auth{
		ID:             "dirty-auth",
		Provider:       "claude",
		Status:         StatusError,
		StatusMessage:  "cooldown",
		Unavailable:    true,
		NextRetryAfter: now.Add(10 * time.Minute),
		LastError:      &Error{Code: "rate_limit", Message: "retry later", Retryable: true, HTTPStatus: 429},
	}); err != nil {
		t.Fatalf("register dirty auth: %v", err)
	}

	snapshot := manager.ExportRuntimeSnapshot(now)
	if snapshot.Len() != 1 {
		t.Fatalf("snapshot.Len() = %d, want 1", snapshot.Len())
	}
	if _, ok := snapshot.Auths["clean-auth"]; ok {
		t.Fatalf("expected clean auth to be skipped")
	}
	if state := snapshot.Auths["dirty-auth"]; state == nil {
		t.Fatalf("expected dirty auth to be exported")
	}
}

func TestManagerApplyRuntimeSnapshotClearsExpiredAuthLevelState(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	now := time.Date(2026, time.April, 18, 12, 0, 0, 0, time.UTC)

	if _, err := manager.Register(context.Background(), &Auth{
		ID:       "auth-expired",
		Provider: "claude",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	applied := manager.ApplyRuntimeSnapshot(RuntimeSnapshot{Auths: map[string]*AuthRuntimeState{
		"auth-expired": {
			Status:         StatusError,
			StatusMessage:  "expired cooldown",
			Unavailable:    true,
			Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(-time.Minute), BackoffLevel: 2},
			LastError:      &Error{Code: "rate_limit", Message: "expired", Retryable: true, HTTPStatus: 429},
			UpdatedAt:      now.Add(-2 * time.Minute),
			NextRetryAfter: now.Add(-time.Minute),
		},
	}}, now)
	if len(applied) != 1 || applied[0] != "auth-expired" {
		t.Fatalf("applied = %v, want [auth-expired]", applied)
	}

	updated, ok := manager.GetByID("auth-expired")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if updated.Status != StatusActive {
		t.Fatalf("updated.Status = %v, want %v", updated.Status, StatusActive)
	}
	if updated.Unavailable {
		t.Fatalf("updated.Unavailable = true, want false")
	}
	if !updated.NextRetryAfter.IsZero() {
		t.Fatalf("updated.NextRetryAfter = %v, want zero", updated.NextRetryAfter)
	}
	if updated.LastError != nil {
		t.Fatalf("updated.LastError = %#v, want nil", updated.LastError)
	}
	if updated.StatusMessage != "" {
		t.Fatalf("updated.StatusMessage = %q, want empty", updated.StatusMessage)
	}
	if !quotaStateIsClean(updated.Quota) {
		t.Fatalf("updated.Quota = %#v, want clean", updated.Quota)
	}
}

func TestManagerApplyRuntimeSnapshotPreservesFutureModelState(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	now := time.Date(2026, time.April, 18, 12, 0, 0, 0, time.UTC)
	nextRetryAfter := now.Add(10 * time.Minute)
	nextRecoverAt := now.Add(20 * time.Minute)

	if _, err := manager.Register(context.Background(), &Auth{
		ID:       "auth-model",
		Provider: "claude",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	applied := manager.ApplyRuntimeSnapshot(RuntimeSnapshot{Auths: map[string]*AuthRuntimeState{
		"auth-model": {
			Status: StatusError,
			ModelStates: map[string]*ModelState{
				"gpt-5": {
					Status:         StatusError,
					StatusMessage:  "model cooldown",
					Unavailable:    true,
					NextRetryAfter: nextRetryAfter,
					LastError:      &Error{Code: "model_rate_limit", Message: "busy", Retryable: true, HTTPStatus: 429},
					Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: nextRecoverAt, BackoffLevel: 4},
					UpdatedAt:      now.Add(-time.Minute),
				},
			},
		},
	}}, now)
	if len(applied) != 1 || applied[0] != "auth-model" {
		t.Fatalf("applied = %v, want [auth-model]", applied)
	}

	updated, ok := manager.GetByID("auth-model")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates["gpt-5"]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if !state.Unavailable {
		t.Fatalf("state.Unavailable = false, want true")
	}
	if !state.NextRetryAfter.Equal(nextRetryAfter) {
		t.Fatalf("state.NextRetryAfter = %v, want %v", state.NextRetryAfter, nextRetryAfter)
	}
	if !updated.Unavailable {
		t.Fatalf("updated.Unavailable = false, want true")
	}
	if !updated.NextRetryAfter.Equal(nextRetryAfter) {
		t.Fatalf("updated.NextRetryAfter = %v, want %v", updated.NextRetryAfter, nextRetryAfter)
	}
	if !updated.Quota.Exceeded {
		t.Fatalf("updated.Quota.Exceeded = false, want true")
	}
	if updated.Quota.BackoffLevel != 4 {
		t.Fatalf("updated.Quota.BackoffLevel = %d, want 4", updated.Quota.BackoffLevel)
	}
}

func TestManagerApplyRuntimeSnapshotDoesNotOverrideExistingRuntimeState(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	now := time.Date(2026, time.April, 18, 12, 0, 0, 0, time.UTC)
	currentRetryAfter := now.Add(5 * time.Minute)
	snapshotRetryAfter := now.Add(30 * time.Minute)

	if _, err := manager.Register(context.Background(), &Auth{
		ID:             "auth-current",
		Provider:       "claude",
		Status:         StatusError,
		StatusMessage:  "current state",
		Unavailable:    true,
		NextRetryAfter: currentRetryAfter,
		LastError:      &Error{Code: "current", Message: "current state", Retryable: true, HTTPStatus: 429},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	applied := manager.ApplyRuntimeSnapshot(RuntimeSnapshot{Auths: map[string]*AuthRuntimeState{
		"auth-current": {
			Status:         StatusError,
			StatusMessage:  "stale snapshot",
			Unavailable:    true,
			NextRetryAfter: snapshotRetryAfter,
			LastError:      &Error{Code: "snapshot", Message: "stale snapshot", Retryable: true, HTTPStatus: 429},
		},
	}}, now)
	if len(applied) != 0 {
		t.Fatalf("applied = %v, want none", applied)
	}

	updated, ok := manager.GetByID("auth-current")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if updated.StatusMessage != "current state" {
		t.Fatalf("updated.StatusMessage = %q, want current state", updated.StatusMessage)
	}
	if !updated.NextRetryAfter.Equal(currentRetryAfter) {
		t.Fatalf("updated.NextRetryAfter = %v, want %v", updated.NextRetryAfter, currentRetryAfter)
	}
	if updated.LastError == nil || updated.LastError.Code != "current" {
		t.Fatalf("updated.LastError = %#v, want current", updated.LastError)
	}
}

func TestManagerUpdatePreservesAuthLevelRuntimeState(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	now := time.Date(2026, time.April, 18, 12, 0, 0, 0, time.UTC)
	nextRetryAfter := now.Add(10 * time.Minute)

	if _, err := manager.Register(context.Background(), &Auth{
		ID:             "auth-update",
		Provider:       "claude",
		Status:         StatusError,
		StatusMessage:  "cooldown",
		Unavailable:    true,
		NextRetryAfter: nextRetryAfter,
		LastError:      &Error{Code: "rate_limit", Message: "retry later", Retryable: true, HTTPStatus: 429},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	if _, err := manager.Update(context.Background(), &Auth{
		ID:       "auth-update",
		Provider: "claude",
		Metadata: map[string]any{"k": "v"},
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := manager.GetByID("auth-update")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if updated.Status != StatusError {
		t.Fatalf("updated.Status = %v, want %v", updated.Status, StatusError)
	}
	if !updated.Unavailable {
		t.Fatalf("updated.Unavailable = false, want true")
	}
	if !updated.NextRetryAfter.Equal(nextRetryAfter) {
		t.Fatalf("updated.NextRetryAfter = %v, want %v", updated.NextRetryAfter, nextRetryAfter)
	}
	if updated.LastError == nil || updated.LastError.Code != "rate_limit" {
		t.Fatalf("updated.LastError = %#v, want rate_limit", updated.LastError)
	}
}
