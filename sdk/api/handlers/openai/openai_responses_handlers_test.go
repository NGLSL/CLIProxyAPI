package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/registry"
	"github.com/NGLSL/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/NGLSL/CLIProxyAPI/v6/sdk/config"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type responsesCaptureExecutor struct {
	calls        int
	sourceFormat string
	payloads     [][]byte
}

func (e *responsesCaptureExecutor) Identifier() string { return "test-provider" }

func (e *responsesCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	e.sourceFormat = opts.SourceFormat.String()
	e.payloads = append(e.payloads, append([]byte(nil), req.Payload...))
	return coreexecutor.Response{Payload: []byte(`{"id":"resp_1","object":"response","output":[]}`)}, nil
}

func (e *responsesCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *responsesCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *responsesCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *responsesCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func newOpenAIResponsesTestRouter(t *testing.T, executor *responsesCaptureExecutor) http.Handler {
	t.Helper()

	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth-responses", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)
	return router
}

func TestResponsesAcceptsResponsesPayload(t *testing.T) {
	executor := &responsesCaptureExecutor{}
	router := newOpenAIResponsesTestRouter(t, executor)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":"hello","instructions":"be helpful"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if executor.sourceFormat != "openai-response" {
		t.Fatalf("source format = %q, want %q", executor.sourceFormat, "openai-response")
	}
}

func TestResponsesAcceptsPreviousResponseIDPayload(t *testing.T) {
	executor := &responsesCaptureExecutor{}
	router := newOpenAIResponsesTestRouter(t, executor)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","previous_response_id":"resp_1","input":"continue"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
}

func TestResponsesUsesClientMetadataAlias(t *testing.T) {
	executor := &responsesCaptureExecutor{}
	router := newOpenAIResponsesTestRouter(t, executor)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":"hello","client_metadata":{"source":"codex"}}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if len(executor.payloads) != 1 {
		t.Fatalf("payload count = %d, want 1", len(executor.payloads))
	}
	if got := gjson.GetBytes(executor.payloads[0], "metadata.source").String(); got != "codex" {
		t.Fatalf("metadata.source = %q, want %q; payload=%s", got, "codex", executor.payloads[0])
	}
	if gjson.GetBytes(executor.payloads[0], "client_metadata").Exists() {
		t.Fatalf("client_metadata leaked into executor payload: %s", executor.payloads[0])
	}
}

func TestResponsesDropsExplicitMetadata(t *testing.T) {
	executor := &responsesCaptureExecutor{}
	router := newOpenAIResponsesTestRouter(t, executor)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":"hello","metadata":{"source":"explicit"}}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if gjson.GetBytes(executor.payloads[0], "metadata").Exists() {
		t.Fatalf("metadata leaked into executor payload: %s", executor.payloads[0])
	}
	if gjson.GetBytes(executor.payloads[0], "client_metadata").Exists() {
		t.Fatalf("client_metadata leaked into executor payload: %s", executor.payloads[0])
	}
}
