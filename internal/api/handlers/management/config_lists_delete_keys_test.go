package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/config"
	"github.com/NGLSL/CLIProxyAPI/v6/internal/watcher/synthesizer"
	coreauth "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/gin-gonic/gin"
)

func registerTestAuth(t *testing.T, manager *coreauth.Manager, auth *coreauth.Auth) string {
	t.Helper()

	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth %q: %v", auth.ID, errRegister)
	}
	return auth.EnsureIndex()
}

func decodeResponseMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var body map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("decode response body: %v; raw=%s", errDecode, rec.Body.String())
	}
	return body
}

func getResponseItems[T any](t *testing.T, body map[string]any, key string) []T {
	t.Helper()

	raw, ok := body[key]
	if !ok {
		t.Fatalf("response missing key %q", key)
	}
	encoded, errMarshal := json.Marshal(raw)
	if errMarshal != nil {
		t.Fatalf("marshal response key %q: %v", key, errMarshal)
	}
	var items []T
	if errUnmarshal := json.Unmarshal(encoded, &items); errUnmarshal != nil {
		t.Fatalf("unmarshal response key %q: %v", key, errUnmarshal)
	}
	return items
}

func writeTestConfigFile(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if errWrite := os.WriteFile(path, []byte("{}\n"), 0o600); errWrite != nil {
		t.Fatalf("failed to write test config: %v", errWrite)
	}
	return path
}

func TestGetConfigListsIncludeLiveAuthIndex(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		GeminiKey:          []config.GeminiKey{{APIKey: "gemini-key", BaseURL: "https://gemini.example.com"}},
		ClaudeKey:          []config.ClaudeKey{{APIKey: "claude-key", BaseURL: "https://claude.example.com"}},
		CodexKey:           []config.CodexKey{{APIKey: "codex-key", BaseURL: "https://codex.example.com"}},
		VertexCompatAPIKey: []config.VertexCompatKey{{APIKey: "vertex-key", BaseURL: "https://vertex.example.com", ProxyURL: "http://vertex-proxy.example.com:8080"}},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "bohe",
			BaseURL: "https://bohe.example.com",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
				APIKey:   "compat-key",
				ProxyURL: "http://compat-proxy.example.com:8080",
			}},
		}},
	}

	manager := coreauth.NewManager(nil, nil, nil)
	idGen := synthesizer.NewStableIDGenerator()

	geminiID, _ := idGen.Next("gemini:apikey", "gemini-key", "https://gemini.example.com")
	geminiIndex := registerTestAuth(t, manager, &coreauth.Auth{
		ID:       geminiID,
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key": "gemini-key",
		},
	})

	claudeID, _ := idGen.Next("claude:apikey", "claude-key", "https://claude.example.com")
	claudeIndex := registerTestAuth(t, manager, &coreauth.Auth{
		ID:       claudeID,
		Provider: "claude",
		Attributes: map[string]string{
			"api_key": "claude-key",
		},
	})

	codexID, _ := idGen.Next("codex:apikey", "codex-key", "https://codex.example.com")
	codexIndex := registerTestAuth(t, manager, &coreauth.Auth{
		ID:       codexID,
		Provider: "codex",
		Attributes: map[string]string{
			"api_key": "codex-key",
		},
	})

	vertexID, _ := idGen.Next("vertex:apikey", "vertex-key", "https://vertex.example.com", "http://vertex-proxy.example.com:8080")
	vertexIndex := registerTestAuth(t, manager, &coreauth.Auth{
		ID:       vertexID,
		Provider: "vertex",
		Attributes: map[string]string{
			"api_key": "vertex-key",
		},
	})

	compatID, _ := idGen.Next("openai-compatibility:bohe", "compat-key", "https://bohe.example.com", "http://compat-proxy.example.com:8080")
	compatIndex := registerTestAuth(t, manager, &coreauth.Auth{
		ID:       compatID,
		Provider: "bohe",
		Label:    "bohe",
		Attributes: map[string]string{
			"api_key":      "compat-key",
			"compat_name":  "bohe",
			"provider_key": "bohe",
		},
	})

	h := &Handler{cfg: cfg, authManager: manager}

	t.Run("gemini", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/gemini-api-key", nil)

		h.GetGeminiKeys(c)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		items := getResponseItems[geminiKeyWithAuthIndex](t, decodeResponseMap(t, rec), "gemini-api-key")
		if len(items) != 1 {
			t.Fatalf("items len = %d, want 1", len(items))
		}
		if items[0].AuthIndex != geminiIndex {
			t.Fatalf("auth-index = %q, want %q", items[0].AuthIndex, geminiIndex)
		}
	})

	t.Run("claude", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/claude-api-key", nil)

		h.GetClaudeKeys(c)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		items := getResponseItems[claudeKeyWithAuthIndex](t, decodeResponseMap(t, rec), "claude-api-key")
		if len(items) != 1 {
			t.Fatalf("items len = %d, want 1", len(items))
		}
		if items[0].AuthIndex != claudeIndex {
			t.Fatalf("auth-index = %q, want %q", items[0].AuthIndex, claudeIndex)
		}
	})

	t.Run("codex", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/codex-api-key", nil)

		h.GetCodexKeys(c)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		items := getResponseItems[codexKeyWithAuthIndex](t, decodeResponseMap(t, rec), "codex-api-key")
		if len(items) != 1 {
			t.Fatalf("items len = %d, want 1", len(items))
		}
		if items[0].AuthIndex != codexIndex {
			t.Fatalf("auth-index = %q, want %q", items[0].AuthIndex, codexIndex)
		}
	})

	t.Run("vertex", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/vertex-api-key", nil)

		h.GetVertexCompatKeys(c)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		items := getResponseItems[vertexCompatKeyWithAuthIndex](t, decodeResponseMap(t, rec), "vertex-api-key")
		if len(items) != 1 {
			t.Fatalf("items len = %d, want 1", len(items))
		}
		if items[0].AuthIndex != vertexIndex {
			t.Fatalf("auth-index = %q, want %q", items[0].AuthIndex, vertexIndex)
		}
	})

	t.Run("openai-compatibility", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/openai-compatibility", nil)

		h.GetOpenAICompat(c)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		items := getResponseItems[openAICompatibilityWithAuthIndex](t, decodeResponseMap(t, rec), "openai-compatibility")
		if len(items) != 1 {
			t.Fatalf("items len = %d, want 1", len(items))
		}
		if len(items[0].APIKeyEntries) != 1 {
			t.Fatalf("api-key-entries len = %d, want 1", len(items[0].APIKeyEntries))
		}
		if items[0].APIKeyEntries[0].AuthIndex != compatIndex {
			t.Fatalf("auth-index = %q, want %q", items[0].APIKeyEntries[0].AuthIndex, compatIndex)
		}
	})
}

func TestDeleteGeminiKey_RequiresBaseURLWhenAPIKeyDuplicated(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			GeminiKey: []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/gemini-api-key?api-key=shared-key", nil)

	h.DeleteGeminiKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := len(h.cfg.GeminiKey); got != 2 {
		t.Fatalf("gemini keys len = %d, want 2", got)
	}
}

func TestDeleteGeminiKey_DeletesOnlyMatchingBaseURL(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			GeminiKey: []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/gemini-api-key?api-key=shared-key&base-url=https://a.example.com", nil)

	h.DeleteGeminiKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.GeminiKey); got != 1 {
		t.Fatalf("gemini keys len = %d, want 1", got)
	}
	if got := h.cfg.GeminiKey[0].BaseURL; got != "https://b.example.com" {
		t.Fatalf("remaining base-url = %q, want %q", got, "https://b.example.com")
	}
}

func TestDeleteClaudeKey_DeletesEmptyBaseURLWhenExplicitlyProvided(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			ClaudeKey: []config.ClaudeKey{
				{APIKey: "shared-key", BaseURL: ""},
				{APIKey: "shared-key", BaseURL: "https://claude.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/claude-api-key?api-key=shared-key&base-url=", nil)

	h.DeleteClaudeKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.ClaudeKey); got != 1 {
		t.Fatalf("claude keys len = %d, want 1", got)
	}
	if got := h.cfg.ClaudeKey[0].BaseURL; got != "https://claude.example.com" {
		t.Fatalf("remaining base-url = %q, want %q", got, "https://claude.example.com")
	}
}

func TestDeleteVertexCompatKey_DeletesOnlyMatchingBaseURL(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			VertexCompatAPIKey: []config.VertexCompatKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/vertex-api-key?api-key=shared-key&base-url=https://b.example.com", nil)

	h.DeleteVertexCompatKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.VertexCompatAPIKey); got != 1 {
		t.Fatalf("vertex keys len = %d, want 1", got)
	}
	if got := h.cfg.VertexCompatAPIKey[0].BaseURL; got != "https://a.example.com" {
		t.Fatalf("remaining base-url = %q, want %q", got, "https://a.example.com")
	}
}

func TestDeleteCodexKey_RequiresBaseURLWhenAPIKeyDuplicated(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			CodexKey: []config.CodexKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/codex-api-key?api-key=shared-key", nil)

	h.DeleteCodexKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := len(h.cfg.CodexKey); got != 2 {
		t.Fatalf("codex keys len = %d, want 2", got)
	}
}
