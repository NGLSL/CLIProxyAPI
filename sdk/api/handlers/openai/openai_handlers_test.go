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

type chatCaptureExecutor struct {
	calls          int
	streamCalls    int
	sourceFormat   string
	payloads       [][]byte
	streamPayloads [][]byte
}

func (e *chatCaptureExecutor) Identifier() string { return "test-provider" }

func (e *chatCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	e.sourceFormat = opts.SourceFormat.String()
	e.payloads = append(e.payloads, append([]byte(nil), req.Payload...))
	return coreexecutor.Response{Payload: []byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}}`)}, nil
}

func (e *chatCaptureExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.streamCalls++
	e.sourceFormat = opts.SourceFormat.String()
	e.streamPayloads = append(e.streamPayloads, append([]byte(nil), req.Payload...))
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`)}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *chatCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *chatCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *chatCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func newOpenAIChatTestRouter(t *testing.T, executor *chatCaptureExecutor) http.Handler {
	t.Helper()

	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth-chat", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/chat/completions", h.ChatCompletions)
	router.POST("/v1/completions", h.Completions)
	return router
}

func TestChatCompletionsRejectsResponsesPayload(t *testing.T) {
	executor := &chatCaptureExecutor{}
	router := newOpenAIChatTestRouter(t, executor)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","input":"hello","instructions":"be helpful"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadRequest)
	}
	if executor.calls != 0 {
		t.Fatalf("executor calls = %d, want 0", executor.calls)
	}
	if !strings.Contains(resp.Body.String(), "/v1/responses") {
		t.Fatalf("body = %q, want mention of /v1/responses", resp.Body.String())
	}
}

func TestChatCompletionsAcceptsChatPayload(t *testing.T) {
	executor := &chatCaptureExecutor{}
	router := newOpenAIChatTestRouter(t, executor)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if executor.sourceFormat != "openai" {
		t.Fatalf("source format = %q, want %q", executor.sourceFormat, "openai")
	}
}

func TestConvertCompletionsRequestToChatCompletionsPreservesCompatibleFields(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-4.1",
		"prompt":"hello",
		"max_tokens":64,
		"metadata":{"tags":["a","b"]},
		"service_tier":"priority",
		"store":true,
		"seed":7,
		"parallel_tool_calls":true,
		"response_format":{"type":"json_schema","json_schema":{"name":"demo"}},
		"modalities":["text","audio"],
		"audio":{"voice":"alloy","format":"wav"},
		"prediction":{"type":"content","content":"preview"},
		"prompt_cache_key":"cache-key",
		"prompt_cache_retention":"short",
		"extra_headers":{"X-Test":"header-value"},
		"extra_query":{"provider":"openrouter"},
		"extra_body":{"user":"abc"}
	}`)

	out := convertCompletionsRequestToChatCompletions(raw, true)

	if got := gjson.GetBytes(out, "model").String(); got != "gpt-4.1" {
		t.Fatalf("model = %q, want %q", got, "gpt-4.1")
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "user" {
		t.Fatalf("messages.0.role = %q, want %q", got, "user")
	}
	if got := gjson.GetBytes(out, "messages.0.content").String(); got != "hello" {
		t.Fatalf("messages.0.content = %q, want %q", got, "hello")
	}
	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 64 {
		t.Fatalf("max_tokens = %d, want %d", got, 64)
	}
	if got := gjson.GetBytes(out, "metadata.tags.1").String(); got != "b" {
		t.Fatalf("metadata.tags.1 = %q, want %q", got, "b")
	}
	if got := gjson.GetBytes(out, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want %q", got, "priority")
	}
	if got := gjson.GetBytes(out, "store").Bool(); !got {
		t.Fatal("store = false, want true")
	}
	if got := gjson.GetBytes(out, "seed").Int(); got != 7 {
		t.Fatalf("seed = %d, want %d", got, 7)
	}
	if got := gjson.GetBytes(out, "parallel_tool_calls").Bool(); !got {
		t.Fatal("parallel_tool_calls = false, want true")
	}
	if got := gjson.GetBytes(out, "response_format.type").String(); got != "json_schema" {
		t.Fatalf("response_format.type = %q, want %q", got, "json_schema")
	}
	if got := gjson.GetBytes(out, "modalities.1").String(); got != "audio" {
		t.Fatalf("modalities.1 = %q, want %q", got, "audio")
	}
	if got := gjson.GetBytes(out, "audio.voice").String(); got != "alloy" {
		t.Fatalf("audio.voice = %q, want %q", got, "alloy")
	}
	if got := gjson.GetBytes(out, "prediction.content").String(); got != "preview" {
		t.Fatalf("prediction.content = %q, want %q", got, "preview")
	}
	if got := gjson.GetBytes(out, "prompt_cache_key").String(); got != "cache-key" {
		t.Fatalf("prompt_cache_key = %q, want %q", got, "cache-key")
	}
	if got := gjson.GetBytes(out, "prompt_cache_retention").String(); got != "short" {
		t.Fatalf("prompt_cache_retention = %q, want %q", got, "short")
	}
	if got := gjson.GetBytes(out, "extra_headers.X-Test").String(); got != "header-value" {
		t.Fatalf("extra_headers.X-Test = %q, want %q", got, "header-value")
	}
	if got := gjson.GetBytes(out, "extra_query.provider").String(); got != "openrouter" {
		t.Fatalf("extra_query.provider = %q, want %q", got, "openrouter")
	}
	if got := gjson.GetBytes(out, "extra_body.user").String(); got != "abc" {
		t.Fatalf("extra_body.user = %q, want %q", got, "abc")
	}
}

func TestShouldRejectResponsesFormat(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "chat payload", body: `{"messages":[{"role":"user","content":"hi"}]}`, want: false},
		{name: "responses input", body: `{"input":"hi"}`, want: true},
		{name: "responses instructions", body: `{"instructions":"be helpful"}`, want: true},
		{name: "responses previous response id", body: `{"previous_response_id":"resp_1"}`, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRejectResponsesFormat([]byte(tt.body)); got != tt.want {
				t.Fatalf("shouldRejectResponsesFormat() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestChatCompletionsStreamingDropsMetadata(t *testing.T) {
	executor := &chatCaptureExecutor{}
	router := newOpenAIChatTestRouter(t, executor)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","stream":true,"messages":[{"role":"user","content":"hello"}],"metadata":{"source":"explicit"}}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if executor.streamCalls != 1 {
		t.Fatalf("stream calls = %d, want 1", executor.streamCalls)
	}
	if gjson.GetBytes(executor.streamPayloads[0], "metadata").Exists() {
		t.Fatalf("metadata leaked into chat streaming payload: %s", executor.streamPayloads[0])
	}
}

func TestCompletionsNonStreamingPreservesMetadata(t *testing.T) {
	executor := &chatCaptureExecutor{}
	router := newOpenAIChatTestRouter(t, executor)

	req := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(`{"model":"test-model","prompt":"hello","metadata":{"source":"explicit"}}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if got := gjson.GetBytes(executor.payloads[0], "metadata.source").String(); got != "explicit" {
		t.Fatalf("metadata.source = %q, want %q; payload=%s", got, "explicit", executor.payloads[0])
	}
}

func TestCompletionsStreamingDropsMetadata(t *testing.T) {
	executor := &chatCaptureExecutor{}
	router := newOpenAIChatTestRouter(t, executor)

	req := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(`{"model":"test-model","prompt":"hello","stream":true,"metadata":{"source":"explicit"}}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if executor.streamCalls != 1 {
		t.Fatalf("stream calls = %d, want 1", executor.streamCalls)
	}
	if gjson.GetBytes(executor.streamPayloads[0], "metadata").Exists() {
		t.Fatalf("metadata leaked into completions streaming payload: %s", executor.streamPayloads[0])
	}
}
