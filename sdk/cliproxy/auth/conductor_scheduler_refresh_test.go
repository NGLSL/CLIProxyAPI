package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	internalconfig "github.com/NGLSL/CLIProxyAPI/v7/internal/config"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/registry"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/util"
	cliproxyexecutor "github.com/NGLSL/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type schedulerProviderTestExecutor struct {
	provider string
}

func (e schedulerProviderTestExecutor) Identifier() string { return e.provider }

func (e schedulerProviderTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e schedulerProviderTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e schedulerProviderTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e schedulerProviderTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e schedulerProviderTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

type requestInterruptedTestExecutor struct {
	provider string
}

func (e requestInterruptedTestExecutor) Identifier() string { return e.provider }

func (e requestInterruptedTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if auth != nil && auth.ID == "a-request-interrupted" {
		return cliproxyexecutor.Response{}, errors.New("Request interrupted by user")
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e requestInterruptedTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e requestInterruptedTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e requestInterruptedTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e requestInterruptedTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManager_RefreshSchedulerEntry_RebuildsSupportedModelSetAfterModelRegistration(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name  string
		prime func(*Manager, *Auth) error
	}{
		{
			name: "register",
			prime: func(manager *Manager, auth *Auth) error {
				_, errRegister := manager.Register(ctx, auth)
				return errRegister
			},
		},
		{
			name: "update",
			prime: func(manager *Manager, auth *Auth) error {
				_, errRegister := manager.Register(ctx, auth)
				if errRegister != nil {
					return errRegister
				}
				updated := auth.Clone()
				updated.Metadata = map[string]any{"updated": true}
				_, errUpdate := manager.Update(ctx, updated)
				return errUpdate
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			auth := &Auth{
				ID:       "refresh-entry-" + testCase.name,
				Provider: "gemini",
			}
			if errPrime := testCase.prime(manager, auth); errPrime != nil {
				t.Fatalf("prime auth %s: %v", testCase.name, errPrime)
			}

			registerSchedulerModels(t, "gemini", "scheduler-refresh-model", auth.ID)

			got, errPick := manager.scheduler.pickSingle(ctx, "gemini", "scheduler-refresh-model", cliproxyexecutor.Options{}, nil)
			var authErr *Error
			if !errors.As(errPick, &authErr) || authErr == nil {
				t.Fatalf("pickSingle() before refresh error = %v, want auth_not_found", errPick)
			}
			if authErr.Code != "auth_not_found" {
				t.Fatalf("pickSingle() before refresh code = %q, want %q", authErr.Code, "auth_not_found")
			}
			if got != nil {
				t.Fatalf("pickSingle() before refresh auth = %v, want nil", got)
			}

			manager.RefreshSchedulerEntry(auth.ID)

			got, errPick = manager.scheduler.pickSingle(ctx, "gemini", "scheduler-refresh-model", cliproxyexecutor.Options{}, nil)
			if errPick != nil {
				t.Fatalf("pickSingle() after refresh error = %v", errPick)
			}
			if got == nil || got.ID != auth.ID {
				t.Fatalf("pickSingle() after refresh auth = %v, want %q", got, auth.ID)
			}
		})
	}
}

func TestManager_MarkResult_OpenAICompatServerErrorDoesNotBlockScheduler(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	model := "mimo-v2.5-pro"
	auth := &Auth{
		ID:       "openai-compat-server-error",
		Provider: "xiaomi-mimo",
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":      "test-key",
			"compat_name":  "xiaomi-mimo",
			"provider_key": "xiaomi-mimo",
		},
	}

	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "xiaomi-mimo", model, auth.ID)
	manager.RefreshSchedulerEntry(auth.ID)

	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream overloaded"},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if state.Unavailable || !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected 5xx not to block OpenAI-compatible auth, got unavailable=%v next_retry=%s", state.Unavailable, state.NextRetryAfter)
	}

	got, errPick := manager.scheduler.pickSingle(ctx, util.OpenAICompatibleProviderKey("xiaomi-mimo"), model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() after 5xx error = %v", errPick)
	}
	if got == nil || got.ID != auth.ID {
		t.Fatalf("pickSingle() auth = %v, want %q", got, auth.ID)
	}
}

func TestManager_Execute_RequestInterruptedByUserPassesThroughWithoutFallbackOrState(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(requestInterruptedTestExecutor{provider: "gemini"})
	model := "request-interrupted-model"

	registerSchedulerModels(t, "gemini", model, "a-request-interrupted", "b-success")
	for _, auth := range []*Auth{
		{ID: "a-request-interrupted", Provider: "gemini", Status: StatusActive},
		{ID: "b-success", Provider: "gemini", Status: StatusActive},
	} {
		if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}

	resp, errExecute := manager.Execute(ctx, []string{"gemini"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil || errExecute.Error() != "Request interrupted by user" {
		t.Fatalf("Execute() error = %v, want Request interrupted by user", errExecute)
	}
	if len(resp.Payload) != 0 {
		t.Fatalf("Execute() payload = %s, want empty", resp.Payload)
	}

	interrupted, ok := manager.GetByID("a-request-interrupted")
	if !ok || interrupted == nil {
		t.Fatal("expected interrupted auth to be present")
	}
	if interrupted.Status == StatusError || interrupted.LastError != nil || len(interrupted.ModelStates) != 0 {
		t.Fatalf("interrupted auth state was mutated: status=%s last_error=%v model_states=%d", interrupted.Status, interrupted.LastError, len(interrupted.ModelStates))
	}

	success, ok := manager.GetByID("b-success")
	if !ok || success == nil {
		t.Fatal("expected fallback auth to be present")
	}
	if len(success.ModelStates) != 0 {
		t.Fatalf("fallback auth was used after request interruption, model_states=%d", len(success.ModelStates))
	}
}

func TestManager_MarkResult_RequestInterruptedByUserDoesNotMutateSchedulerState(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	model := "request-interrupted-mark-result-model"
	auth := &Auth{ID: "request-interrupted-mark-result", Provider: "gemini", Status: StatusActive}

	registerSchedulerModels(t, "gemini", model, auth.ID)
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusInternalServerError, Message: "Request interrupted by user"},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be present")
	}
	if updated.Status == StatusError || updated.LastError != nil || len(updated.ModelStates) != 0 {
		t.Fatalf("request interruption mutated auth state: status=%s last_error=%v model_states=%d", updated.Status, updated.LastError, len(updated.ModelStates))
	}

	got, errPick := manager.scheduler.pickSingle(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() after interruption error = %v", errPick)
	}
	if got == nil || got.ID != auth.ID {
		t.Fatalf("pickSingle() auth = %v, want %q", got, auth.ID)
	}
}

func TestManager_PickNextLegacy_FileFirstFallsBackToAPILayerForUnsupportedModel(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})
	manager.SetConfig(&internalconfig.Config{Routing: internalconfig.RoutingConfig{SourcePreference: "file-first"}})

	registerSchedulerModels(t, "gemini", "file-only-model", "file-auth")
	registerSchedulerModels(t, "gemini", "api-only-model", "api-a", "api-b")

	auths := []*Auth{
		{ID: "file-auth", Provider: "gemini", Attributes: map[string]string{"source_type": "file"}},
		{ID: "api-b", Provider: "gemini", Attributes: map[string]string{"source_type": "api"}},
		{ID: "api-a", Provider: "gemini", Attributes: map[string]string{"source_type": "api"}},
	}
	for _, auth := range auths {
		if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}

	want := []string{"api-a", "api-b", "api-a"}
	for index, wantID := range want {
		got, _, errPick := manager.pickNextLegacy(ctx, "gemini", "api-only-model", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNextLegacy() #%d error = %v", index, errPick)
		}
		if got == nil || got.ID != wantID {
			t.Fatalf("pickNextLegacy() #%d auth = %v, want %s", index, got, wantID)
		}
	}
}

func TestManager_PickNextLegacy_FileFirstPrefersFileLayerWhenSupported(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})
	manager.SetConfig(&internalconfig.Config{Routing: internalconfig.RoutingConfig{SourcePreference: "file-first"}})

	registerSchedulerModels(t, "gemini", "shared-model", "file-auth", "api-auth")

	auths := []*Auth{
		{ID: "api-auth", Provider: "gemini", Attributes: map[string]string{"source_type": "api"}},
		{ID: "file-auth", Provider: "gemini", Attributes: map[string]string{"source_type": "file"}},
	}
	for _, auth := range auths {
		if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}

	got, _, errPick := manager.pickNextLegacy(ctx, "gemini", "shared-model", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNextLegacy() error = %v", errPick)
	}
	if got == nil || got.ID != "file-auth" {
		t.Fatalf("pickNextLegacy() auth = %v, want file-auth", got)
	}
}
func TestManager_PickNext_RebuildsSchedulerAfterModelCooldownError(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	registerSchedulerModels(t, "gemini", "scheduler-cooldown-rebuild-model", "cooldown-stale-old")

	oldAuth := &Auth{
		ID:       "cooldown-stale-old",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, oldAuth); errRegister != nil {
		t.Fatalf("register old auth: %v", errRegister)
	}

	manager.MarkResult(ctx, Result{
		AuthID:   oldAuth.ID,
		Provider: "gemini",
		Model:    "scheduler-cooldown-rebuild-model",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
	})

	newAuth := &Auth{
		ID:       "cooldown-stale-new",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, newAuth); errRegister != nil {
		t.Fatalf("register new auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(newAuth.ID, "gemini", []*registry.ModelInfo{{ID: "scheduler-cooldown-rebuild-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient(newAuth.ID)
	})

	got, errPick := manager.scheduler.pickSingle(ctx, "gemini", "scheduler-cooldown-rebuild-model", cliproxyexecutor.Options{}, nil)
	var cooldownErr *modelCooldownError
	if !errors.As(errPick, &cooldownErr) {
		t.Fatalf("pickSingle() before sync error = %v, want modelCooldownError", errPick)
	}
	if got != nil {
		t.Fatalf("pickSingle() before sync auth = %v, want nil", got)
	}

	got, executor, errPick := manager.pickNext(ctx, "gemini", "scheduler-cooldown-rebuild-model", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if executor == nil {
		t.Fatal("pickNext() executor = nil")
	}
	if got == nil || got.ID != newAuth.ID {
		t.Fatalf("pickNext() auth = %v, want %q", got, newAuth.ID)
	}
}
