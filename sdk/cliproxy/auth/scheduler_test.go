package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/NGLSL/CLIProxyAPI/v6/internal/config"
	"github.com/NGLSL/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type schedulerTestExecutor struct{}

func (schedulerTestExecutor) Identifier() string { return "test" }

func (schedulerTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (schedulerTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (schedulerTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (schedulerTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (schedulerTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

type trackingSelector struct {
	calls      int
	lastAuthID []string
}

func (s *trackingSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	s.calls++
	s.lastAuthID = s.lastAuthID[:0]
	for _, auth := range auths {
		s.lastAuthID = append(s.lastAuthID, auth.ID)
	}
	if len(auths) == 0 {
		return nil, nil
	}
	return auths[len(auths)-1], nil
}

func newSchedulerForTest(selector Selector, auths ...*Auth) *authScheduler {
	scheduler := newAuthScheduler(selector)
	scheduler.rebuild(auths)
	return scheduler
}

func registerSchedulerModels(t *testing.T, provider string, model string, authIDs ...string) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	for _, authID := range authIDs {
		reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		for _, authID := range authIDs {
			reg.UnregisterClient(authID)
		}
	})
}

func TestSchedulerPick_RoundRobinHighestPriority(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "low", Provider: "gemini", Attributes: map[string]string{"priority": "0"}},
		&Auth{ID: "high-b", Provider: "gemini", Attributes: map[string]string{"priority": "10"}},
		&Auth{ID: "high-a", Provider: "gemini", Attributes: map[string]string{"priority": "10"}},
	)

	want := []string{"high-a", "high-b", "high-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_FillFirstSticksToFirstReady(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&FillFirstSelector{},
		&Auth{ID: "b", Provider: "gemini"},
		&Auth{ID: "a", Provider: "gemini"},
		&Auth{ID: "c", Provider: "gemini"},
	)

	for index := 0; index < 3; index++ {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != "a" {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, "a")
		}
	}
}

func TestSchedulerPick_PromotesExpiredCooldownBeforePick(t *testing.T) {
	t.Parallel()

	model := "gemini-2.5-pro"
	registerSchedulerModels(t, "gemini", model, "cooldown-expired")
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{
			ID:       "cooldown-expired",
			Provider: "gemini",
			ModelStates: map[string]*ModelState{
				model: {
					Status:         StatusError,
					Unavailable:    true,
					NextRetryAfter: time.Now().Add(-1 * time.Second),
				},
			},
		},
	)

	got, errPick := scheduler.pickSingle(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickSingle() auth = nil")
	}
	if got.ID != "cooldown-expired" {
		t.Fatalf("pickSingle() auth.ID = %q, want %q", got.ID, "cooldown-expired")
	}
}

func TestSchedulerPick_GeminiVirtualParentUsesTwoLevelRotation(t *testing.T) {
	t.Parallel()

	registerSchedulerModels(t, "gemini-cli", "gemini-2.5-pro", "cred-a::proj-1", "cred-a::proj-2", "cred-b::proj-1", "cred-b::proj-2")
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "cred-a::proj-1", Provider: "gemini-cli", Attributes: map[string]string{"gemini_virtual_parent": "cred-a"}},
		&Auth{ID: "cred-a::proj-2", Provider: "gemini-cli", Attributes: map[string]string{"gemini_virtual_parent": "cred-a"}},
		&Auth{ID: "cred-b::proj-1", Provider: "gemini-cli", Attributes: map[string]string{"gemini_virtual_parent": "cred-b"}},
		&Auth{ID: "cred-b::proj-2", Provider: "gemini-cli", Attributes: map[string]string{"gemini_virtual_parent": "cred-b"}},
	)

	wantParents := []string{"cred-a", "cred-b", "cred-a", "cred-b"}
	wantIDs := []string{"cred-a::proj-1", "cred-b::proj-1", "cred-a::proj-2", "cred-b::proj-2"}
	for index := range wantIDs {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini-cli", "gemini-2.5-pro", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
		if got.Attributes["gemini_virtual_parent"] != wantParents[index] {
			t.Fatalf("pickSingle() #%d parent = %q, want %q", index, got.Attributes["gemini_virtual_parent"], wantParents[index])
		}
	}
}

func TestSchedulerPick_CodexWebsocketPrefersWebsocketEnabledSubset(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "codex-http", Provider: "codex"},
		&Auth{ID: "codex-ws-a", Provider: "codex", Attributes: map[string]string{"websockets": "true"}},
		&Auth{ID: "codex-ws-b", Provider: "codex", Attributes: map[string]string{"websockets": "true"}},
	)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	want := []string{"codex-ws-a", "codex-ws-b", "codex-ws-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(ctx, "codex", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_CodexWebsocketPrefersWebsocketEnabledAcrossPriorities(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "codex-http", Provider: "codex", Attributes: map[string]string{"priority": "10"}},
		&Auth{ID: "codex-ws-a", Provider: "codex", Attributes: map[string]string{"priority": "0", "websockets": "true"}},
		&Auth{ID: "codex-ws-b", Provider: "codex", Attributes: map[string]string{"priority": "0", "websockets": "true"}},
	)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	want := []string{"codex-ws-a", "codex-ws-b", "codex-ws-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(ctx, "codex", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_MixedProvidersUsesWeightedProviderRotationOverReadyCandidates(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "gemini-a", Provider: "gemini"},
		&Auth{ID: "gemini-b", Provider: "gemini"},
		&Auth{ID: "claude-a", Provider: "claude"},
	)

	wantProviders := []string{"gemini", "gemini", "claude", "gemini"}
	wantIDs := []string{"gemini-a", "gemini-b", "claude-a", "gemini-a"}
	for index := range wantProviders {
		got, provider, errPick := scheduler.pickMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestSchedulerPick_MixedProvidersPrefersHighestPriorityTier(t *testing.T) {
	t.Parallel()

	model := "gpt-default"
	registerSchedulerModels(t, "provider-low", model, "low")
	registerSchedulerModels(t, "provider-high-a", model, "high-a")
	registerSchedulerModels(t, "provider-high-b", model, "high-b")

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "low", Provider: "provider-low", Attributes: map[string]string{"priority": "4"}},
		&Auth{ID: "high-a", Provider: "provider-high-a", Attributes: map[string]string{"priority": "7"}},
		&Auth{ID: "high-b", Provider: "provider-high-b", Attributes: map[string]string{"priority": "7"}},
	)

	providers := []string{"provider-low", "provider-high-a", "provider-high-b"}
	wantProviders := []string{"provider-high-a", "provider-high-b", "provider-high-a", "provider-high-b"}
	wantIDs := []string{"high-a", "high-b", "high-a", "high-b"}
	for index := range wantProviders {
		got, provider, errPick := scheduler.pickMixed(context.Background(), providers, model, cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestManager_PickNextMixed_UsesWeightedProviderRotationBeforeCredentialRotation(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}

	wantProviders := []string{"gemini", "gemini", "claude", "gemini"}
	wantIDs := []string{"gemini-a", "gemini-b", "claude-a", "gemini-a"}
	for index := range wantProviders {
		got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, map[string]struct{}{})
		if errPick != nil {
			t.Fatalf("pickNextMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickNextMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickNextMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickNextMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestManagerCustomSelector_FallsBackToLegacyPath(t *testing.T) {
	t.Parallel()

	selector := &trackingSelector{}
	manager := NewManager(nil, selector, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.auths["auth-a"] = &Auth{ID: "auth-a", Provider: "gemini"}
	manager.auths["auth-b"] = &Auth{ID: "auth-b", Provider: "gemini"}

	got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, map[string]struct{}{})
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNext() auth = nil")
	}
	if selector.calls != 1 {
		t.Fatalf("selector.calls = %d, want %d", selector.calls, 1)
	}
	if len(selector.lastAuthID) != 2 {
		t.Fatalf("len(selector.lastAuthID) = %d, want %d", len(selector.lastAuthID), 2)
	}
	if got.ID != selector.lastAuthID[len(selector.lastAuthID)-1] {
		t.Fatalf("pickNext() auth.ID = %q, want selector-picked %q", got.ID, selector.lastAuthID[len(selector.lastAuthID)-1])
	}
}

func TestManager_InitializesSchedulerForBuiltInSelector(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if manager.scheduler == nil {
		t.Fatalf("manager.scheduler = nil")
	}
	if manager.scheduler.strategy != schedulerStrategyRoundRobin {
		t.Fatalf("manager.scheduler.strategy = %v, want %v", manager.scheduler.strategy, schedulerStrategyRoundRobin)
	}

	manager.SetSelector(&FillFirstSelector{})
	if manager.scheduler.strategy != schedulerStrategyFillFirst {
		t.Fatalf("manager.scheduler.strategy = %v, want %v", manager.scheduler.strategy, schedulerStrategyFillFirst)
	}
}

func TestManager_SchedulerTracksRegisterAndUpdate(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}

	got, errPick := manager.scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler.pickSingle() error = %v", errPick)
	}
	if got == nil || got.ID != "auth-a" {
		t.Fatalf("scheduler.pickSingle() auth = %v, want auth-a", got)
	}

	if _, errUpdate := manager.Update(context.Background(), &Auth{ID: "auth-a", Provider: "gemini", Disabled: true}); errUpdate != nil {
		t.Fatalf("Update(auth-a) error = %v", errUpdate)
	}

	got, errPick = manager.scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler.pickSingle() after update error = %v", errPick)
	}
	if got == nil || got.ID != "auth-b" {
		t.Fatalf("scheduler.pickSingle() after update auth = %v, want auth-b", got)
	}
}

func TestManager_PickNextMixed_UsesSchedulerRotation(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}

	wantProviders := []string{"gemini", "gemini", "claude", "gemini"}
	wantIDs := []string{"gemini-a", "gemini-b", "claude-a", "gemini-a"}
	for index := range wantProviders {
		got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNextMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickNextMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickNextMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickNextMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestManager_PickNextMixed_SkipsProvidersWithoutExecutors(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}

	got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNextMixed() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNextMixed() auth = nil")
	}
	if provider != "claude" {
		t.Fatalf("pickNextMixed() provider = %q, want %q", provider, "claude")
	}
	if got.ID != "claude-a" {
		t.Fatalf("pickNextMixed() auth.ID = %q, want %q", got.ID, "claude-a")
	}
}

func TestManager_SchedulerTracksMarkResultCooldownAndRecovery(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("auth-a", "gemini", []*registry.ModelInfo{{ID: "test-model"}})
	reg.RegisterClient("auth-b", "gemini", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient("auth-a")
		reg.UnregisterClient("auth-b")
	})
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-b) error = %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   "auth-a",
		Provider: "gemini",
		Model:    "test-model",
		Success:  false,
		Error:    &Error{HTTPStatus: 429, Message: "quota"},
	})

	got, errPick := manager.scheduler.pickSingle(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler.pickSingle() after cooldown error = %v", errPick)
	}
	if got == nil || got.ID != "auth-b" {
		t.Fatalf("scheduler.pickSingle() after cooldown auth = %v, want auth-b", got)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   "auth-a",
		Provider: "gemini",
		Model:    "test-model",
		Success:  true,
	})

	seen := make(map[string]struct{}, 2)
	for index := 0; index < 2; index++ {
		got, errPick = manager.scheduler.pickSingle(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("scheduler.pickSingle() after recovery #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("scheduler.pickSingle() after recovery #%d auth = nil", index)
		}
		seen[got.ID] = struct{}{}
	}
	if len(seen) != 2 {
		t.Fatalf("len(seen) = %d, want %d", len(seen), 2)
	}
}

func TestSchedulerPick_StickyRoundRobinBindsPerRouteKey(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&StickyRoundRobinSelector{},
		&Auth{ID: "b", Provider: "gemini"},
		&Auth{ID: "a", Provider: "gemini"},
	)

	routeA := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.StickyRouteMetadataKey: "route-a"}}
	got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", routeA, nil)
	if errPick != nil {
		t.Fatalf("pickSingle(route-a first) error = %v", errPick)
	}
	if got == nil || got.ID != "a" {
		t.Fatalf("pickSingle(route-a first) auth = %v, want auth a", got)
	}

	bindingKey := scheduler.stickyBindingKey("gemini", "", "route-a")
	binding, ok := scheduler.stickyBindingForTest(bindingKey)
	if !ok {
		t.Fatalf("stickyBindingForTest(route-a) ok = false")
	}
	if binding.authID != "a" {
		t.Fatalf("stickyBindingForTest(route-a).authID = %q, want %q", binding.authID, "a")
	}

	got, errPick = scheduler.pickSingle(context.Background(), "gemini", "", routeA, nil)
	if errPick != nil {
		t.Fatalf("pickSingle(route-a second) error = %v", errPick)
	}
	if got == nil || got.ID != "a" {
		t.Fatalf("pickSingle(route-a second) auth = %v, want auth a", got)
	}

	routeB := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.StickyRouteMetadataKey: "route-b"}}
	got, errPick = scheduler.pickSingle(context.Background(), "gemini", "", routeB, nil)
	if errPick != nil {
		t.Fatalf("pickSingle(route-b first) error = %v", errPick)
	}
	if got == nil || got.ID != "b" {
		t.Fatalf("pickSingle(route-b first) auth = %v, want auth b", got)
	}
}

func TestSchedulerPick_StickyRoundRobinFallsBackWhenBoundAuthUnavailable(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&StickyRoundRobinSelector{},
		&Auth{
			ID:             "a",
			Provider:       "gemini",
			Unavailable:    true,
			NextRetryAfter: time.Now().Add(time.Minute),
			Quota: QuotaState{
				Exceeded: true,
			},
		},
		&Auth{ID: "b", Provider: "gemini"},
	)

	bindingKey := scheduler.stickyBindingKey("gemini", "", "route-a")
	scheduler.setStickyBindingForTest(bindingKey, "a", time.Now().Add(time.Minute))

	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.StickyRouteMetadataKey: "route-a"}}
	got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", opts, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got == nil || got.ID != "b" {
		t.Fatalf("pickSingle() auth = %v, want auth b", got)
	}

	binding, ok := scheduler.stickyBindingForTest(bindingKey)
	if !ok {
		t.Fatalf("stickyBindingForTest() ok = false")
	}
	if binding.authID != "b" {
		t.Fatalf("stickyBindingForTest().authID = %q, want %q", binding.authID, "b")
	}
}

func TestSchedulerPick_ApiFirstKeepsSelectionInsideAPILayer(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "api-low", Provider: "gemini", Attributes: map[string]string{"source_type": "api", "priority": "0"}},
		&Auth{ID: "file-high", Provider: "gemini", Attributes: map[string]string{"source_type": "file", "priority": "10"}},
	)
	scheduler.sourcePreference = routingSourcePreferenceAPIFirst

	got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got == nil || got.ID != "api-low" {
		t.Fatalf("pickSingle() auth = %v, want auth api-low", got)
	}
}

func TestSchedulerPick_ApiFirstFallsBackToFileLayer(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{
			ID:             "api-cooldown",
			Provider:       "gemini",
			Attributes:     map[string]string{"source_type": "api", "priority": "10"},
			Unavailable:    true,
			NextRetryAfter: time.Now().Add(time.Minute),
			Quota:          QuotaState{Exceeded: true},
		},
		&Auth{ID: "file-ready", Provider: "gemini", Attributes: map[string]string{"source_type": "file", "priority": "0"}},
	)
	scheduler.sourcePreference = routingSourcePreferenceAPIFirst

	got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got == nil || got.ID != "file-ready" {
		t.Fatalf("pickSingle() auth = %v, want auth file-ready", got)
	}
}

func TestSchedulerPick_FileFirstKeepsSelectionInsideFileLayer(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "api-high", Provider: "gemini", Attributes: map[string]string{"source_type": "api", "priority": "10"}},
		&Auth{ID: "file-low", Provider: "gemini", Attributes: map[string]string{"source_type": "file", "priority": "0"}},
	)
	scheduler.sourcePreference = routingSourcePreferenceFileFirst

	got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got == nil || got.ID != "file-low" {
		t.Fatalf("pickSingle() auth = %v, want auth file-low", got)
	}
}

func TestSchedulerPick_FileFirstFallsBackToAPILayerForUnsupportedModel(t *testing.T) {
	t.Parallel()

	model := "api-only-model"
	registerSchedulerModels(t, "gemini", "file-only-model", "file-auth")
	registerSchedulerModels(t, "gemini", model, "api-a", "api-b")
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "file-auth", Provider: "gemini", Attributes: map[string]string{"source_type": "file"}},
		&Auth{ID: "api-b", Provider: "gemini", Attributes: map[string]string{"source_type": "api"}},
		&Auth{ID: "api-a", Provider: "gemini", Attributes: map[string]string{"source_type": "api"}},
	)
	scheduler.sourcePreference = routingSourcePreferenceFileFirst

	want := []string{"api-a", "api-b", "api-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil || got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth = %v, want %s", index, got, wantID)
		}
	}
}

func TestSchedulerPick_StickyRoundRobinRebindsWhenPreferredLayerReturns(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&StickyRoundRobinSelector{},
		&Auth{ID: "api-ready", Provider: "gemini", Attributes: map[string]string{"source_type": "api"}},
		&Auth{ID: "file-ready", Provider: "gemini", Attributes: map[string]string{"source_type": "file"}},
	)
	scheduler.sourcePreference = routingSourcePreferenceAPIFirst

	bindingKey := scheduler.stickyBindingKey("gemini", "", "route-a")
	scheduler.setStickyBindingForTest(bindingKey, "file-ready", time.Now().Add(time.Minute))

	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.StickyRouteMetadataKey: "route-a"}}
	got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", opts, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got == nil || got.ID != "api-ready" {
		t.Fatalf("pickSingle() auth = %v, want auth api-ready", got)
	}

	binding, ok := scheduler.stickyBindingForTest(bindingKey)
	if !ok {
		t.Fatalf("stickyBindingForTest() ok = false")
	}
	if binding.authID != "api-ready" {
		t.Fatalf("stickyBindingForTest().authID = %q, want %q", binding.authID, "api-ready")
	}
}

func TestSchedulerPick_MixedProvidersStickyRoundRobinBindsProviderAndAuth(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&StickyRoundRobinSelector{},
		&Auth{ID: "gemini-a", Provider: "gemini"},
		&Auth{ID: "claude-a", Provider: "claude"},
	)

	providers := []string{"gemini", "claude"}
	routeA := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.StickyRouteMetadataKey: "route-a"}}
	got, provider, errPick := scheduler.pickMixed(context.Background(), providers, "", routeA, nil)
	if errPick != nil {
		t.Fatalf("pickMixed(route-a first) error = %v", errPick)
	}
	if got == nil || provider != "gemini" || got.ID != "gemini-a" {
		t.Fatalf("pickMixed(route-a first) got auth=%v provider=%q, want gemini-a/gemini", got, provider)
	}

	bindingKey := scheduler.stickyBindingProviderSetKey(providers, "", "route-a")
	binding, ok := scheduler.stickyBindingForTest(bindingKey)
	if !ok {
		t.Fatalf("stickyBindingForTest(route-a) ok = false")
	}
	if binding.authID != "gemini-a" {
		t.Fatalf("stickyBindingForTest(route-a).authID = %q, want %q", binding.authID, "gemini-a")
	}

	got, provider, errPick = scheduler.pickMixed(context.Background(), providers, "", routeA, nil)
	if errPick != nil {
		t.Fatalf("pickMixed(route-a second) error = %v", errPick)
	}
	if got == nil || provider != "gemini" || got.ID != "gemini-a" {
		t.Fatalf("pickMixed(route-a second) got auth=%v provider=%q, want gemini-a/gemini", got, provider)
	}

	routeB := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.StickyRouteMetadataKey: "route-b"}}
	got, provider, errPick = scheduler.pickMixed(context.Background(), providers, "", routeB, nil)
	if errPick != nil {
		t.Fatalf("pickMixed(route-b first) error = %v", errPick)
	}
	if got == nil || provider != "claude" || got.ID != "claude-a" {
		t.Fatalf("pickMixed(route-b first) got auth=%v provider=%q, want claude-a/claude", got, provider)
	}
}

func TestManager_InitializesSchedulerForStickyRoundRobinSelector(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &StickyRoundRobinSelector{}, nil)
	if manager.scheduler == nil {
		t.Fatalf("manager.scheduler = nil")
	}
	if manager.scheduler.strategy != schedulerStrategyStickyRoundRobin {
		t.Fatalf("manager.scheduler.strategy = %v, want %v", manager.scheduler.strategy, schedulerStrategyStickyRoundRobin)
	}
}

func TestManager_SetConfig_UpdatesSchedulerStickyTTL(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &StickyRoundRobinSelector{}, nil)
	if got := manager.scheduler.stickyTTLSecondsLocked(); got != internalconfig.DefaultRoutingStickyTTL {
		t.Fatalf("stickyTTLSecondsLocked() = %d, want %d", got, internalconfig.DefaultRoutingStickyTTL)
	}

	manager.SetConfig(&internalconfig.Config{Routing: internalconfig.RoutingConfig{StickyTTL: 42}})
	if got := manager.scheduler.stickyTTLSecondsLocked(); got != 42 {
		t.Fatalf("stickyTTLSecondsLocked() after SetConfig = %d, want %d", got, 42)
	}

	manager.SetConfig(&internalconfig.Config{Routing: internalconfig.RoutingConfig{StickyTTL: 0}})
	if got := manager.scheduler.stickyTTLSecondsLocked(); got != internalconfig.DefaultRoutingStickyTTL {
		t.Fatalf("stickyTTLSecondsLocked() after defaulting = %d, want %d", got, internalconfig.DefaultRoutingStickyTTL)
	}
}

func TestSchedulerPick_MixedProvidersSourcePreferenceUsesPreferredLayerAcrossProviderSet(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "gemini-file-high", Provider: "gemini", Attributes: map[string]string{"source_type": "file", "priority": "10"}},
		&Auth{ID: "claude-api-low", Provider: "claude", Attributes: map[string]string{"source_type": "api", "priority": "0"}},
	)
	scheduler.sourcePreference = routingSourcePreferenceAPIFirst

	got, provider, errPick := scheduler.pickMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickMixed() error = %v", errPick)
	}
	if got == nil || got.ID != "claude-api-low" || provider != "claude" {
		t.Fatalf("pickMixed() got auth=%v provider=%q, want claude-api-low/claude", got, provider)
	}
}

func TestSchedulerPick_MixedProvidersSourcePreferenceFallsBackAcrossProviderSet(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{
			ID:             "gemini-api-cooldown",
			Provider:       "gemini",
			Attributes:     map[string]string{"source_type": "api", "priority": "10"},
			Unavailable:    true,
			NextRetryAfter: time.Now().Add(time.Minute),
			Quota:          QuotaState{Exceeded: true},
		},
		&Auth{ID: "claude-file-ready", Provider: "claude", Attributes: map[string]string{"source_type": "file", "priority": "0"}},
	)
	scheduler.sourcePreference = routingSourcePreferenceAPIFirst

	got, provider, errPick := scheduler.pickMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickMixed() error = %v", errPick)
	}
	if got == nil || got.ID != "claude-file-ready" || provider != "claude" {
		t.Fatalf("pickMixed() got auth=%v provider=%q, want claude-file-ready/claude", got, provider)
	}
}

func TestManager_SetConfig_UpdatesSchedulerSourcePreference(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &StickyRoundRobinSelector{}, nil)
	if got := manager.scheduler.sourcePreferenceStringLocked(); got != string(routingSourcePreferenceNone) {
		t.Fatalf("sourcePreferenceStringLocked() = %q, want %q", got, routingSourcePreferenceNone)
	}

	manager.SetConfig(&internalconfig.Config{Routing: internalconfig.RoutingConfig{SourcePreference: "api-first"}})
	if got := manager.scheduler.sourcePreferenceStringLocked(); got != string(routingSourcePreferenceAPIFirst) {
		t.Fatalf("sourcePreferenceStringLocked() after SetConfig = %q, want %q", got, routingSourcePreferenceAPIFirst)
	}

	manager.SetConfig(&internalconfig.Config{Routing: internalconfig.RoutingConfig{SourcePreference: "invalid"}})
	if got := manager.scheduler.sourcePreferenceStringLocked(); got != string(routingSourcePreferenceNone) {
		t.Fatalf("sourcePreferenceStringLocked() after normalization = %q, want %q", got, routingSourcePreferenceNone)
	}
}
