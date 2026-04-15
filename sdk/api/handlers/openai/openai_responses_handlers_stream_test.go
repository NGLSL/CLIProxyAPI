package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func newResponsesStreamTestHandler(t *testing.T) (*OpenAIResponsesAPIHandler, *httptest.ResponseRecorder, *gin.Context, http.Flusher) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatalf("expected gin writer to implement http.Flusher")
	}

	return h, recorder, c, flusher
}

func TestForwardResponsesStreamSeparatesDataOnlySSEChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"arguments\":\"{}\"}}")
	data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
	body := recorder.Body.String()
	parts := strings.Split(strings.TrimSpace(body), "\n\n")
	if len(parts) != 2 {
		t.Fatalf("expected 2 SSE events, got %d. Body: %q", len(parts), body)
	}

	expectedPart1 := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"arguments\":\"{}\"}}"
	if parts[0] != expectedPart1 {
		t.Errorf("unexpected first event.\nGot: %q\nWant: %q", parts[0], expectedPart1)
	}

	expectedPart2 := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}"
	if parts[1] != expectedPart2 {
		t.Errorf("unexpected second event.\nGot: %q\nWant: %q", parts[1], expectedPart2)
	}
}

func TestForwardResponsesStreamReassemblesSplitSSEEventChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 4)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("event: response.created")
	data <- []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}")
	data <- []byte("\n")
	data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := recorder.Body.String()
	want := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}\n\n\n"
	if got != want {
		t.Fatalf("unexpected split-event framing.\nGot:  %q\nWant: %q", got, want)
	}
}

func TestForwardResponsesStreamPreservesValidFullSSEEventChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	chunk := []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n")
	data <- chunk
	data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := recorder.Body.String()
	want := string(chunk) + "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}\n\n\n"
	if got != want {
		t.Fatalf("unexpected full-event framing.\nGot:  %q\nWant: %q", got, want)
	}
}

func TestForwardResponsesStreamBuffersSplitDataPayloadChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 3)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.created\"")
	data <- []byte(",\"response\":{\"id\":\"resp-1\"}}")
	data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := recorder.Body.String()
	want := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}\n\n\n"
	if got != want {
		t.Fatalf("unexpected split-data framing.\nGot:  %q\nWant: %q", got, want)
	}
}

func TestResponsesSSENeedsLineBreakSkipsChunksThatAlreadyStartWithNewline(t *testing.T) {
	if responsesSSENeedsLineBreak([]byte("event: response.created"), []byte("\n")) {
		t.Fatal("expected no injected newline before newline-only chunk")
	}
	if responsesSSENeedsLineBreak([]byte("event: response.created"), []byte("\r\n")) {
		t.Fatal("expected no injected newline before CRLF chunk")
	}
}

func TestForwardResponsesStreamWritesTerminalErrorWhenEOFComesBeforeResponsesTerminalEvent(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	body := recorder.Body.String()
	if !strings.Contains(body, "event: error\ndata: {") {
		t.Fatalf("expected terminal SSE error event on clean EOF before responses terminal event.\nGot: %q", body)
	}
	if !strings.Contains(body, `"type":"error"`) {
		t.Fatalf("expected responses error chunk.\nGot: %q", body)
	}
	if strings.HasSuffix(body, "\n") && !strings.Contains(body, "event: error") {
		t.Fatalf("expected more than trailing success newline.\nGot: %q", body)
	}
}

func TestForwardResponsesStreamKeepsCleanEOFSuccessAfterResponseCompleted(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	body := recorder.Body.String()
	if strings.Contains(body, "event: error") {
		t.Fatalf("expected clean EOF after response.completed to remain successful.\nGot: %q", body)
	}
	want := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}\n\n\n"
	if body != want {
		t.Fatalf("unexpected completed EOF output.\nGot:  %q\nWant: %q", body, want)
	}
}

func TestForwardResponsesStreamKeepsCleanEOFSuccessAfterResponseIncomplete(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp-1\",\"status\":\"incomplete\",\"output\":[]}}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	body := recorder.Body.String()
	if strings.Contains(body, "event: error") {
		t.Fatalf("expected clean EOF after response.incomplete to remain successful.\nGot: %q", body)
	}
	want := "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp-1\",\"status\":\"incomplete\",\"output\":[]}}\n\n\n"
	if body != want {
		t.Fatalf("unexpected incomplete EOF output.\nGot:  %q\nWant: %q", body, want)
	}
}

func TestForwardResponsesStreamDropsIncompleteTrailingDataChunkOnFlush(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.created\"")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	body := recorder.Body.String()
	if !strings.Contains(body, "event: error\ndata: {") {
		t.Fatalf("expected incomplete trailing data to terminate with SSE error event.\nGot: %q", body)
	}
	if !strings.Contains(body, `"type":"error"`) {
		t.Fatalf("expected responses error chunk after incomplete trailing data.\nGot: %q", body)
	}
}

type responsesStickyCaptureExecutor struct {
	mu          sync.Mutex
	authIDs     []string
	responseSeq int
}

func (e *responsesStickyCaptureExecutor) Identifier() string { return "test-provider" }

func (e *responsesStickyCaptureExecutor) Execute(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	responseID := e.recordCall(auth)
	return coreexecutor.Response{
		Payload: []byte(`{"id":"` + responseID + `","object":"response","status":"completed","output":[]}`),
	}, nil
}

func (e *responsesStickyCaptureExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	responseID := e.recordCall(auth)
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"" + responseID + "\",\"output\":[]}}\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *responsesStickyCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *responsesStickyCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *responsesStickyCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *responsesStickyCaptureExecutor) recordCall(auth *coreauth.Auth) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if auth != nil {
		e.authIDs = append(e.authIDs, auth.ID)
	}
	e.responseSeq++
	return "resp-" + strconv.Itoa(e.responseSeq)
}

func (e *responsesStickyCaptureExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

func newResponsesStickyTestRouter(t *testing.T, selector coreauth.Selector, executor *responsesStickyCaptureExecutor) http.Handler {
	t.Helper()

	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(executor)

	auths := []*coreauth.Auth{
		{ID: "auth-a", Provider: executor.Identifier(), Status: coreauth.StatusActive},
		{ID: "auth-b", Provider: executor.Identifier(), Status: coreauth.StatusActive},
	}
	for _, auth := range auths {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register auth %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		}
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)
	return router
}

func performResponsesRequest(t *testing.T, router http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func TestResponsesHTTPStreamPinsAuthByPreviousResponseID(t *testing.T) {
	selector := &orderedWebsocketSelector{order: []string{"auth-a", "auth-b"}}
	executor := &responsesStickyCaptureExecutor{}
	router := newResponsesStickyTestRouter(t, selector, executor)

	resp1 := performResponsesRequest(t, router, `{"model":"test-model","stream":true,"input":"hello"}`)
	if resp1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", resp1.Code, http.StatusOK)
	}
	if !strings.Contains(resp1.Body.String(), `"id":"resp-1"`) {
		t.Fatalf("first response body = %q, want response id resp-1", resp1.Body.String())
	}

	resp2 := performResponsesRequest(t, router, `{"model":"test-model","stream":true,"previous_response_id":"resp-1","input":"again"}`)
	if resp2.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d", resp2.Code, http.StatusOK)
	}

	if got := executor.AuthIDs(); len(got) != 2 || got[0] != "auth-a" || got[1] != "auth-a" {
		t.Fatalf("selected auth IDs = %v, want [auth-a auth-a]", got)
	}
}

func TestResponsesHTTPStreamPinsAuthBySessionID(t *testing.T) {
	selector := &orderedWebsocketSelector{order: []string{"auth-a", "auth-b"}}
	executor := &responsesStickyCaptureExecutor{}
	router := newResponsesStickyTestRouter(t, selector, executor)

	resp1 := performResponsesRequest(t, router, `{"model":"test-model","stream":true,"session_id":"session-1","input":"hello"}`)
	if resp1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", resp1.Code, http.StatusOK)
	}

	resp2 := performResponsesRequest(t, router, `{"model":"test-model","stream":true,"session_id":"session-1","input":"again"}`)
	if resp2.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d", resp2.Code, http.StatusOK)
	}

	if got := executor.AuthIDs(); len(got) != 2 || got[0] != "auth-a" || got[1] != "auth-a" {
		t.Fatalf("selected auth IDs = %v, want [auth-a auth-a]", got)
	}
}

func TestResponsesHTTPStreamSessionIDDoesNotCollideWithResponseIDAffinity(t *testing.T) {
	selector := &orderedWebsocketSelector{order: []string{"auth-a", "auth-b"}}
	executor := &responsesStickyCaptureExecutor{}
	router := newResponsesStickyTestRouter(t, selector, executor)

	resp1 := performResponsesRequest(t, router, `{"model":"test-model","stream":true,"input":"hello"}`)
	if resp1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", resp1.Code, http.StatusOK)
	}
	if !strings.Contains(resp1.Body.String(), `"id":"resp-1"`) {
		t.Fatalf("first response body = %q, want response id resp-1", resp1.Body.String())
	}

	resp2 := performResponsesRequest(t, router, `{"model":"test-model","stream":true,"session_id":"resp-1","input":"new conversation"}`)
	if resp2.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d", resp2.Code, http.StatusOK)
	}

	if got := executor.AuthIDs(); len(got) != 2 || got[0] != "auth-a" || got[1] != "auth-b" {
		t.Fatalf("selected auth IDs = %v, want [auth-a auth-b]", got)
	}
}

func TestResponsesHTTPRequestsWithoutContinuityKeysRemainNonSticky(t *testing.T) {
	selector := &orderedWebsocketSelector{order: []string{"auth-a", "auth-b"}}
	executor := &responsesStickyCaptureExecutor{}
	router := newResponsesStickyTestRouter(t, selector, executor)

	resp1 := performResponsesRequest(t, router, `{"model":"test-model","stream":true,"input":"hello"}`)
	if resp1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", resp1.Code, http.StatusOK)
	}

	resp2 := performResponsesRequest(t, router, `{"model":"test-model","stream":true,"input":"again"}`)
	if resp2.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d", resp2.Code, http.StatusOK)
	}

	if got := executor.AuthIDs(); len(got) != 2 || got[0] != "auth-a" || got[1] != "auth-b" {
		t.Fatalf("selected auth IDs = %v, want [auth-a auth-b]", got)
	}
}

func TestResponsesHTTPNonStreamPinsAuthByPreviousResponseID(t *testing.T) {
	selector := &orderedWebsocketSelector{order: []string{"auth-a", "auth-b"}}
	executor := &responsesStickyCaptureExecutor{}
	router := newResponsesStickyTestRouter(t, selector, executor)

	resp1 := performResponsesRequest(t, router, `{"model":"test-model","input":"hello"}`)
	if resp1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", resp1.Code, http.StatusOK)
	}
	if !strings.Contains(resp1.Body.String(), `"id":"resp-1"`) {
		t.Fatalf("first response body = %q, want response id resp-1", resp1.Body.String())
	}

	resp2 := performResponsesRequest(t, router, `{"model":"test-model","previous_response_id":"resp-1","input":"again"}`)
	if resp2.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d", resp2.Code, http.StatusOK)
	}

	if got := executor.AuthIDs(); len(got) != 2 || got[0] != "auth-a" || got[1] != "auth-a" {
		t.Fatalf("selected auth IDs = %v, want [auth-a auth-a]", got)
	}
}
