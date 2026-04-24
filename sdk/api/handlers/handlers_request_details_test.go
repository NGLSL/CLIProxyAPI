package handlers

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/NGLSL/CLIProxyAPI/v6/sdk/config"
)

type requestDetailsTestClient struct {
	id       string
	provider string
	models   []*registry.ModelInfo
}

func registerClientsForRequestDetailsTests(t *testing.T, modelRegistry *registry.ModelRegistry, clients []*requestDetailsTestClient) {
	t.Helper()

	for _, client := range clients {
		modelRegistry.RegisterClient(client.id, client.provider, client.models)
		clientID := client.id
		t.Cleanup(func() {
			modelRegistry.UnregisterClient(clientID)
		})
	}
}

func TestGetRequestDetails_PreservesSuffix(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()

	registerClientsForRequestDetailsTests(t, modelRegistry, []*requestDetailsTestClient{
		{
			id:       "test-request-details-gemini",
			provider: "gemini",
			models: []*registry.ModelInfo{
				{ID: "gemini-2.5-pro", Created: now + 30},
				{ID: "gemini-2.5-flash", Created: now + 25},
			},
		},
		{
			id:       "test-request-details-openai",
			provider: "openai",
			models: []*registry.ModelInfo{
				{ID: "gpt-5.2", Created: now + 20},
			},
		},
		{
			id:       "test-request-details-claude",
			provider: "claude",
			models: []*registry.ModelInfo{
				{ID: "claude-sonnet-4-5", Created: now + 5},
			},
		},
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	tests := []struct {
		name          string
		inputModel    string
		wantProviders []string
		wantModel     string
		wantErr       bool
	}{
		{
			name:          "numeric suffix preserved",
			inputModel:    "gemini-2.5-pro(8192)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-pro(8192)",
			wantErr:       false,
		},
		{
			name:          "level suffix preserved",
			inputModel:    "gpt-5.2(high)",
			wantProviders: []string{"openai"},
			wantModel:     "gpt-5.2(high)",
			wantErr:       false,
		},
		{
			name:          "no suffix unchanged",
			inputModel:    "claude-sonnet-4-5",
			wantProviders: []string{"claude"},
			wantModel:     "claude-sonnet-4-5",
			wantErr:       false,
		},
		{
			name:          "unknown model with suffix",
			inputModel:    "unknown-model(8192)",
			wantProviders: nil,
			wantModel:     "",
			wantErr:       true,
		},
		{
			name:          "auto suffix resolved",
			inputModel:    "auto(high)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-pro(high)",
			wantErr:       false,
		},
		{
			name:          "special suffix none preserved",
			inputModel:    "gemini-2.5-flash(none)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-flash(none)",
			wantErr:       false,
		},
		{
			name:          "special suffix auto preserved",
			inputModel:    "claude-sonnet-4-5(auto)",
			wantProviders: []string{"claude"},
			wantModel:     "claude-sonnet-4-5(auto)",
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providers, model, errMsg := handler.getRequestDetails(tt.inputModel)
			if (errMsg != nil) != tt.wantErr {
				t.Fatalf("getRequestDetails() error = %v, wantErr %v", errMsg, tt.wantErr)
			}
			if errMsg != nil {
				return
			}
			if !reflect.DeepEqual(providers, tt.wantProviders) {
				t.Fatalf("getRequestDetails() providers = %v, want %v", providers, tt.wantProviders)
			}
			if model != tt.wantModel {
				t.Fatalf("getRequestDetails() model = %v, want %v", model, tt.wantModel)
			}
		})
	}
}

func TestGetRequestDetails_MergesBaseAndResolvedModelProviders(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()

	registerClientsForRequestDetailsTests(t, modelRegistry, []*requestDetailsTestClient{
		{
			id:       "test-request-details-merge-base",
			provider: "openai",
			models: []*registry.ModelInfo{
				{ID: "gpt-5.4", Created: now + 20},
			},
		},
		{
			id:       "test-request-details-merge-full",
			provider: "openai-compatibility",
			models: []*registry.ModelInfo{
				{ID: "gpt-5.4(xhigh)", Created: now + 10},
			},
		},
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	providers, model, errMsg := handler.getRequestDetails("gpt-5.4(xhigh)")
	if errMsg != nil {
		t.Fatalf("getRequestDetails() error = %v, want nil", errMsg)
	}
	if !reflect.DeepEqual(providers, []string{"openai", "openai-compatibility"}) {
		t.Fatalf("getRequestDetails() providers = %v, want %v", providers, []string{"openai", "openai-compatibility"})
	}
	if model != "gpt-5.4(xhigh)" {
		t.Fatalf("getRequestDetails() model = %v, want %v", model, "gpt-5.4(xhigh)")
	}
}

func TestGetRequestDetails_DeduplicatesMergedProviders(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()

	registerClientsForRequestDetailsTests(t, modelRegistry, []*requestDetailsTestClient{
		{
			id:       "test-request-details-dedupe-base",
			provider: "openai",
			models: []*registry.ModelInfo{
				{ID: "gpt-5.4", Created: now + 20},
			},
		},
		{
			id:       "test-request-details-dedupe-full",
			provider: "openai",
			models: []*registry.ModelInfo{
				{ID: "gpt-5.4(xhigh)", Created: now + 10},
			},
		},
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	providers, model, errMsg := handler.getRequestDetails("gpt-5.4(xhigh)")
	if errMsg != nil {
		t.Fatalf("getRequestDetails() error = %v, want nil", errMsg)
	}
	if !reflect.DeepEqual(providers, []string{"openai"}) {
		t.Fatalf("getRequestDetails() providers = %v, want %v", providers, []string{"openai"})
	}
	if model != "gpt-5.4(xhigh)" {
		t.Fatalf("getRequestDetails() model = %v, want %v", model, "gpt-5.4(xhigh)")
	}
}

func TestGetRequestDetails_UsesResolvedModelProvidersWhenBaseModelIsMissing(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()

	registerClientsForRequestDetailsTests(t, modelRegistry, []*requestDetailsTestClient{
		{
			id:       "test-request-details-full-only",
			provider: "openai-compatibility",
			models: []*registry.ModelInfo{
				{ID: "gpt-5.4(xhigh)", Created: now + 10},
			},
		},
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	providers, model, errMsg := handler.getRequestDetails("gpt-5.4(xhigh)")
	if errMsg != nil {
		t.Fatalf("getRequestDetails() error = %v, want nil", errMsg)
	}
	if !reflect.DeepEqual(providers, []string{"openai-compatibility"}) {
		t.Fatalf("getRequestDetails() providers = %v, want %v", providers, []string{"openai-compatibility"})
	}
	if model != "gpt-5.4(xhigh)" {
		t.Fatalf("getRequestDetails() model = %v, want %v", model, "gpt-5.4(xhigh)")
	}
}

func TestGetRequestDetails_RejectsImageGenerationToolModel(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	_, _, errMsg := handler.getRequestDetails("gpt-image-2")
	if errMsg == nil {
		t.Fatal("expected error for gpt-image-2, got nil")
	}
	if errMsg.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", errMsg.StatusCode, http.StatusServiceUnavailable)
	}
	if errMsg.Error == nil {
		t.Fatal("expected underlying error")
	}
	if !strings.Contains(errMsg.Error.Error(), "/v1/images/generations") {
		t.Fatalf("error = %q, want generations endpoint hint", errMsg.Error.Error())
	}
	if !strings.Contains(errMsg.Error.Error(), "/v1/images/edits") {
		t.Fatalf("error = %q, want edits endpoint hint", errMsg.Error.Error())
	}
}
