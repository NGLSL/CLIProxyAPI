package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/interfaces"
	"github.com/NGLSL/CLIProxyAPI/v6/internal/registry"
	"github.com/NGLSL/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/NGLSL/CLIProxyAPI/v6/sdk/config"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type imagesCaptureExecutor struct {
	streamCalls    int
	sourceFormat   string
	streamPayloads [][]byte
}

func (e *imagesCaptureExecutor) Identifier() string { return "test-provider" }

func (e *imagesCaptureExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *imagesCaptureExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.streamCalls++
	e.sourceFormat = opts.SourceFormat.String()
	e.streamPayloads = append(e.streamPayloads, append([]byte(nil), req.Payload...))
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":1700000000,\"output\":[{\"type\":\"image_generation_call\",\"output_format\":\"png\",\"result\":\"aGVsbG8=\",\"revised_prompt\":\"revised\"}],\"tool_usage\":{\"image_gen\":{\"images\":1}}}}\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *imagesCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *imagesCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *imagesCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func newOpenAIImagesTestRouter(t *testing.T, executor *imagesCaptureExecutor) http.Handler {
	t.Helper()

	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth-images", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: defaultImagesMainModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/images/generations", h.ImagesGenerations)
	return router
}

func TestImagesGenerationsRejectsMissingPrompt(t *testing.T) {
	executor := &imagesCaptureExecutor{}
	router := newOpenAIImagesTestRouter(t, executor)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gpt-image-2"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadRequest)
	}
	if executor.streamCalls != 0 {
		t.Fatalf("stream calls = %d, want 0", executor.streamCalls)
	}
}

func TestImagesGenerationsReturnsImageResponse(t *testing.T) {
	executor := &imagesCaptureExecutor{}
	router := newOpenAIImagesTestRouter(t, executor)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"draw a cat"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.streamCalls != 1 {
		t.Fatalf("stream calls = %d, want 1", executor.streamCalls)
	}
	if executor.sourceFormat != "openai-response" {
		t.Fatalf("source format = %q, want %q", executor.sourceFormat, "openai-response")
	}
	if len(executor.streamPayloads) != 1 {
		t.Fatalf("payload count = %d, want 1", len(executor.streamPayloads))
	}

	payload := executor.streamPayloads[0]
	if got := gjson.GetBytes(payload, "model").String(); got != defaultImagesMainModel {
		t.Fatalf("model = %q, want %q", got, defaultImagesMainModel)
	}
	if got := gjson.GetBytes(payload, "input.0.content.0.text").String(); got != "draw a cat" {
		t.Fatalf("prompt = %q, want %q", got, "draw a cat")
	}
	if got := gjson.GetBytes(payload, "tools.0.type").String(); got != "image_generation" {
		t.Fatalf("tool type = %q, want %q", got, "image_generation")
	}
	if got := gjson.GetBytes(payload, "tools.0.action").String(); got != "generate" {
		t.Fatalf("tool action = %q, want %q", got, "generate")
	}
	if got := gjson.GetBytes(payload, "tools.0.model").String(); got != defaultImagesToolModel {
		t.Fatalf("tool model = %q, want %q", got, defaultImagesToolModel)
	}

	body := resp.Body.Bytes()
	if got := gjson.GetBytes(body, "created").Int(); got != 1700000000 {
		t.Fatalf("created = %d, want %d", got, int64(1700000000))
	}
	if got := gjson.GetBytes(body, "data.0.b64_json").String(); got != "aGVsbG8=" {
		t.Fatalf("data.0.b64_json = %q, want %q", got, "aGVsbG8=")
	}
	if got := gjson.GetBytes(body, "data.0.revised_prompt").String(); got != "revised" {
		t.Fatalf("data.0.revised_prompt = %q, want %q", got, "revised")
	}
}

func TestBuildImagesResponsesRequestIncludesInputImages(t *testing.T) {
	tool := []byte(`{"type":"image_generation","action":"edit","model":"gpt-image-2"}`)
	out := buildImagesResponsesRequest("edit this", []string{"data:image/png;base64,AAAA"}, tool)

	if got := gjson.GetBytes(out, "model").String(); got != defaultImagesMainModel {
		t.Fatalf("model = %q, want %q", got, defaultImagesMainModel)
	}
	if got := gjson.GetBytes(out, "input.0.content.0.type").String(); got != "input_text" {
		t.Fatalf("first content type = %q, want %q", got, "input_text")
	}
	if got := gjson.GetBytes(out, "input.0.content.1.type").String(); got != "input_image" {
		t.Fatalf("second content type = %q, want %q", got, "input_image")
	}
	if got := gjson.GetBytes(out, "input.0.content.1.image_url").String(); got != "data:image/png;base64,AAAA" {
		t.Fatalf("input image url = %q, want expected data url", got)
	}
	if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "image_generation" {
		t.Fatalf("tool_choice.type = %q, want %q", got, "image_generation")
	}
}

func TestCollectImagesFromResponsesStreamSupportsURLFormat(t *testing.T) {
	data := make(chan []byte, 1)
	errChan := make(chan *interfaces.ErrorMessage, 1)
	data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1700000001,\"output\":[{\"type\":\"image_generation_call\",\"output_format\":\"png\",\"result\":\"aGVsbG8=\"}]}}\n\n")
	close(data)
	close(errChan)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errChan, "url")
	if errMsg != nil {
		t.Fatalf("collectImagesFromResponsesStream() error = %v", errMsg)
	}
	if got := gjson.GetBytes(out, "data.0.url").String(); got != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("data.0.url = %q, want %q", got, "data:image/png;base64,aGVsbG8=")
	}
}
