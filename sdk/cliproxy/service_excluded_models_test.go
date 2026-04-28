package cliproxy

import (
	"strings"
	"testing"

	internalregistry "github.com/NGLSL/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/NGLSL/CLIProxyAPI/v6/sdk/config"
)

func TestRegisterModelsForAuth_UsesPreMergedExcludedModelsAttribute(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OAuthExcludedModels: map[string][]string{
				"gemini-cli": {"gemini-2.5-pro"},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-gemini-cli",
		Provider: "gemini-cli",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":       "oauth",
			"excluded_models": "gemini-2.5-flash",
		},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	models := registry.GetAvailableModelsByProvider("gemini-cli")
	if len(models) == 0 {
		t.Fatal("expected gemini-cli models to be registered")
	}

	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if strings.EqualFold(modelID, "gemini-2.5-flash") {
			t.Fatalf("expected model %q to be excluded by auth attribute", modelID)
		}
	}

	seenGlobalExcluded := false
	for _, model := range models {
		if model == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(model.ID), "gemini-2.5-pro") {
			seenGlobalExcluded = true
			break
		}
	}
	if !seenGlobalExcluded {
		t.Fatal("expected global excluded model to be present when attribute override is set")
	}
}

func TestRegisterModelsForAuth_UsesAuthAllowedModelsAttribute(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "auth-allowed-models",
		Provider: "gemini-cli",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"models":    "gemini-2.5-flash,custom-file-only-model",
		},
	}

	registry := internalregistry.GetGlobalRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	if !registry.ClientSupportsModel(auth.ID, "gemini-2.5-flash") {
		t.Fatal("expected auth client to support allowed static model")
	}
	if !registry.ClientSupportsModel(auth.ID, "custom-file-only-model") {
		t.Fatal("expected auth client to support allowed custom model")
	}
	if registry.ClientSupportsModel(auth.ID, "gemini-2.5-pro") {
		t.Fatal("expected auth client not to support model outside allow list")
	}

	models := registry.GetModelsForClient(auth.ID)
	if len(models) != 2 {
		t.Fatalf("expected 2 registered client models, got %d", len(models))
	}

	seenCustom := false
	for _, model := range models {
		if model == nil || model.ID != "custom-file-only-model" {
			continue
		}
		seenCustom = true
		if !model.UserDefined {
			t.Fatal("expected custom allowed model to be marked user defined")
		}
	}
	if !seenCustom {
		t.Fatal("expected custom allowed model to be registered")
	}
}
