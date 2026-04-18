package middleware

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/interfaces"
	"github.com/NGLSL/CLIProxyAPI/v6/internal/logging"
	"github.com/gin-gonic/gin"
)

func TestExtractRequestBodyWithoutTruncationDoesNotAddNote(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{
		requestInfo: &RequestInfo{Body: []byte("original-body")},
	}

	body := wrapper.extractRequestBody(c)
	if string(body) != "original-body" {
		t.Fatalf("request body = %q, want %q", string(body), "original-body")
	}
	if bytes.Contains(body, []byte(truncatedRequestBodyLogNote)) {
		t.Fatal("non-truncated request body should not include truncation note")
	}
}

func TestExtractRequestBodyAddsTruncationNote(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{
		requestInfo: &RequestInfo{Body: []byte("prefix-body"), BodyTruncated: true},
	}

	body := wrapper.extractRequestBody(c)
	want := "prefix-body" + truncatedRequestBodyLogNote
	if string(body) != want {
		t.Fatalf("request body = %q, want %q", string(body), want)
	}
}

func TestExtractRequestBodyPrefersOverrideAndLeavesCloneBehaviorUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{
		requestInfo: &RequestInfo{Body: []byte("original-body"), BodyTruncated: true},
	}

	c.Set(requestBodyOverrideContextKey, []byte("override-body"))
	body := wrapper.extractRequestBody(c)
	if string(body) != "override-body" {
		t.Fatalf("request body = %q, want %q", string(body), "override-body")
	}

	body[0] = 'X'
	if got := wrapper.extractRequestBody(c); string(got) != "override-body" {
		t.Fatalf("request override should be cloned, got %q", string(got))
	}
}

func TestExtractRequestBodyPrefersOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{
		requestInfo: &RequestInfo{Body: []byte("original-body")},
	}

	body := wrapper.extractRequestBody(c)
	if string(body) != "original-body" {
		t.Fatalf("request body = %q, want %q", string(body), "original-body")
	}

	c.Set(requestBodyOverrideContextKey, []byte("override-body"))
	body = wrapper.extractRequestBody(c)
	if string(body) != "override-body" {
		t.Fatalf("request body = %q, want %q", string(body), "override-body")
	}
}

func TestDetectStreamingFallsBackToTruncatedRequestPrefix(t *testing.T) {
	wrapper := &ResponseWriterWrapper{
		requestInfo: &RequestInfo{
			Body:          []byte(`{"stream": true, "input":"partial"}`),
			BodyTruncated: true,
		},
	}

	if !wrapper.detectStreaming("") {
		t.Fatal("expected detectStreaming to fall back to request body prefix")
	}
}

func TestDetectStreamingPrefersContentTypeOverRequestBodyFallback(t *testing.T) {
	wrapper := &ResponseWriterWrapper{
		requestInfo: &RequestInfo{
			Body:          []byte(`{"stream": true}`),
			BodyTruncated: true,
		},
	}

	if wrapper.detectStreaming("application/json") {
		t.Fatal("expected concrete content type to keep non-streaming decision")
	}
}

func TestExtractRequestBodySupportsStringOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{body: &bytes.Buffer{}}
	c.Set(requestBodyOverrideContextKey, "override-as-string")

	body := wrapper.extractRequestBody(c)
	if string(body) != "override-as-string" {
		t.Fatalf("request body = %q, want %q", string(body), "override-as-string")
	}
}

func TestExtractResponseBodyPrefersBufferedBodyWithoutCloneWhenNotTruncated(t *testing.T) {
	logger := &testRequestLogger{enabled: true}
	_, _, wrapper := newTestWrapper(t, logger)
	wrapper.body.WriteString("buffered-response")

	body := wrapper.extractResponseBody(nil)
	if string(body) != "buffered-response" {
		t.Fatalf("response body = %q, want %q", string(body), "buffered-response")
	}

	body[0] = 'B'
	if got := wrapper.body.String(); got != "Buffered-response" {
		t.Fatalf("expected non-truncated response body to share buffer, got %q", got)
	}
}

func TestExtractResponseBodyAddsTruncationNote(t *testing.T) {
	logger := &testRequestLogger{enabled: true}
	_, _, wrapper := newTestWrapper(t, logger)
	wrapper.body.WriteString("partial")
	wrapper.responseBodyTruncated = true

	body := wrapper.extractResponseBody(nil)
	want := "partial" + truncatedResponseBodyLogNote
	if string(body) != want {
		t.Fatalf("response body = %q, want %q", string(body), want)
	}
}

func TestExtractResponseBodyPrefersOverrideAndClones(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{body: &bytes.Buffer{}}
	wrapper.body.WriteString("original-response")

	body := wrapper.extractResponseBody(c)
	if string(body) != "original-response" {
		t.Fatalf("response body = %q, want %q", string(body), "original-response")
	}

	c.Set(responseBodyOverrideContextKey, []byte("override-response"))
	body = wrapper.extractResponseBody(c)
	if string(body) != "override-response" {
		t.Fatalf("response body = %q, want %q", string(body), "override-response")
	}

	body[0] = 'X'
	if got := wrapper.extractResponseBody(c); string(got) != "override-response" {
		t.Fatalf("response override should be cloned, got %q", string(got))
	}
}

func TestExtractResponseBodySupportsStringOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{}
	c.Set(responseBodyOverrideContextKey, "override-response-as-string")

	body := wrapper.extractResponseBody(c)
	if string(body) != "override-response-as-string" {
		t.Fatalf("response body = %q, want %q", string(body), "override-response-as-string")
	}
}

func TestExtractBodyOverrideClonesBytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	override := []byte("body-override")
	c.Set(requestBodyOverrideContextKey, override)

	body := extractBodyOverride(c, requestBodyOverrideContextKey)
	if !bytes.Equal(body, override) {
		t.Fatalf("body override = %q, want %q", string(body), string(override))
	}

	body[0] = 'X'
	if !bytes.Equal(override, []byte("body-override")) {
		t.Fatalf("override mutated: %q", string(override))
	}
}

func TestExtractWebsocketTimelineUsesOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{}
	if got := wrapper.extractWebsocketTimeline(c); got != nil {
		t.Fatalf("expected nil websocket timeline, got %q", string(got))
	}

	c.Set(websocketTimelineOverrideContextKey, []byte("timeline"))
	body := wrapper.extractWebsocketTimeline(c)
	if string(body) != "timeline" {
		t.Fatalf("websocket timeline = %q, want %q", string(body), "timeline")
	}
}

func TestFinalizeStreamingWritesAPIWebsocketTimeline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	streamWriter := &testStreamingLogWriter{}
	wrapper := &ResponseWriterWrapper{
		ResponseWriter: c.Writer,
		logger:         &testRequestLogger{enabled: true},
		requestInfo: &RequestInfo{
			URL:       "/v1/responses",
			Method:    "POST",
			Headers:   map[string][]string{"Content-Type": {"application/json"}},
			RequestID: "req-1",
			Timestamp: time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC),
		},
		isStreaming:  true,
		streamWriter: streamWriter,
	}

	c.Set("API_WEBSOCKET_TIMELINE", []byte("Timestamp: 2026-04-01T12:00:00Z\nEvent: api.websocket.request\n{}"))

	if err := wrapper.Finalize(c); err != nil {
		t.Fatalf("Finalize error: %v", err)
	}
	if string(streamWriter.apiWebsocketTimeline) != "Timestamp: 2026-04-01T12:00:00Z\nEvent: api.websocket.request\n{}" {
		t.Fatalf("stream writer websocket timeline = %q", string(streamWriter.apiWebsocketTimeline))
	}
	if !streamWriter.closed {
		t.Fatal("expected stream writer to be closed")
	}
}

func TestFinalizeTruncatesLargeSuccessfulNonStreamingResponseForLogging(t *testing.T) {
	logger := &testRequestLogger{enabled: true}
	c, recorder, wrapper := newTestWrapper(t, logger)
	body := repeatBytes('a', nonStreamingSuccessResponseLogBodyLimit+1024)

	if _, err := wrapper.Write(body); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if got := recorder.Body.Bytes(); !bytes.Equal(got, body) {
		t.Fatalf("client body length = %d, want %d", len(got), len(body))
	}

	requireFinalize(t, wrapper, c)
	if !logger.logRequestCalled {
		t.Fatal("expected request log to be written")
	}
	if logger.lastStatusCode != 200 {
		t.Fatalf("logged status code = %d, want 200", logger.lastStatusCode)
	}
	requireTruncatedLogBody(t, logger, body[:nonStreamingSuccessResponseLogBodyLimit])
}

func TestFinalizeKeepsSmallSuccessfulNonStreamingResponseForLogging(t *testing.T) {
	logger := &testRequestLogger{enabled: true}
	c, recorder, wrapper := newTestWrapper(t, logger)
	body := []byte("small-success-response")

	if _, err := wrapper.Write(body); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if got := recorder.Body.Bytes(); !bytes.Equal(got, body) {
		t.Fatalf("client body = %q, want %q", string(got), string(body))
	}

	requireFinalize(t, wrapper, c)
	requireLogBodyEquals(t, logger, body)
	if bytes.Contains(logger.lastResponseBody, []byte(truncatedResponseBodyLogNote)) {
		t.Fatal("small success response should not include truncation note")
	}
}

func TestFinalizeDoesNotTruncateLargeErrorResponseWhenFullLoggingEnabled(t *testing.T) {
	logger := &testRequestLogger{enabled: true}
	c, recorder, wrapper := newTestWrapper(t, logger)
	body := repeatBytes('e', nonStreamingSuccessResponseLogBodyLimit+1024)

	wrapper.WriteHeader(500)
	if _, err := wrapper.Write(body); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if got := recorder.Code; got != 500 {
		t.Fatalf("client status code = %d, want 500", got)
	}
	if got := recorder.Body.Bytes(); !bytes.Equal(got, body) {
		t.Fatalf("client body length = %d, want %d", len(got), len(body))
	}

	requireFinalize(t, wrapper, c)
	if logger.lastStatusCode != 500 {
		t.Fatalf("logged status code = %d, want 500", logger.lastStatusCode)
	}
	requireLogBodyEquals(t, logger, body)
}

func TestFinalizeDoesNotTruncateLargeErrorResponseWhenLoggingOnlyErrors(t *testing.T) {
	logger := &testRequestLogger{enabled: false}
	c, recorder, wrapper := newTestWrapper(t, logger)
	wrapper.logOnErrorOnly = true
	body := repeatBytes('f', nonStreamingSuccessResponseLogBodyLimit+1024)

	wrapper.WriteHeader(502)
	if _, err := wrapper.Write(body); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if got := recorder.Code; got != 502 {
		t.Fatalf("client status code = %d, want 502", got)
	}

	requireFinalize(t, wrapper, c)
	if !logger.logRequestCalled {
		t.Fatal("expected forced error log to be written")
	}
	if !logger.logRequestForce {
		t.Fatal("expected error-only path to force logging")
	}
	if logger.lastStatusCode != 502 {
		t.Fatalf("logged status code = %d, want 502", logger.lastStatusCode)
	}
	requireLogBodyEquals(t, logger, body)
}

func TestWriteStringAppliesSameSuccessResponseLogLimit(t *testing.T) {
	logger := &testRequestLogger{enabled: true}
	c, recorder, wrapper := newTestWrapper(t, logger)
	body := repeatString('s', nonStreamingSuccessResponseLogBodyLimit+1024)

	if _, err := wrapper.WriteString(body); err != nil {
		t.Fatalf("WriteString error: %v", err)
	}
	if got := recorder.Body.String(); got != body {
		t.Fatalf("client body length = %d, want %d", len(got), len(body))
	}

	requireFinalize(t, wrapper, c)
	requireTruncatedLogBody(t, logger, []byte(body[:nonStreamingSuccessResponseLogBodyLimit]))
}

func TestMultipleWritesTruncateExactlyAtBoundary(t *testing.T) {
	logger := &testRequestLogger{enabled: true}
	c, recorder, wrapper := newTestWrapper(t, logger)
	first := repeatBytes('x', nonStreamingSuccessResponseLogBodyLimit-8)
	second := []byte("1234567890")
	third := []byte("ignored")
	fullBody := append(append(bytes.Clone(first), second...), third...)

	if _, err := wrapper.Write(first); err != nil {
		t.Fatalf("first Write error: %v", err)
	}
	if _, err := wrapper.Write(second); err != nil {
		t.Fatalf("second Write error: %v", err)
	}
	if _, err := wrapper.Write(third); err != nil {
		t.Fatalf("third Write error: %v", err)
	}
	if got := recorder.Body.Bytes(); !bytes.Equal(got, fullBody) {
		t.Fatalf("client body length = %d, want %d", len(got), len(fullBody))
	}

	requireFinalize(t, wrapper, c)
	wantPrefix := append(bytes.Clone(first), second[:8]...)
	requireTruncatedLogBody(t, logger, wantPrefix)
}

type testRequestLogger struct {
	enabled               bool
	logRequestCalled      bool
	logRequestForce       bool
	lastRequestBody       []byte
	lastStatusCode        int
	lastResponseHeaders   map[string][]string
	lastResponseBody      []byte
	lastWebsocketTimeline []byte
	lastAPIRequestBody    []byte
	lastAPIResponseBody   []byte
	lastAPIWebsocketBody  []byte
	lastAPIResponseErrors []*interfaces.ErrorMessage
	lastRequestID         string
	lastRequestTimestamp  time.Time
	lastAPIResponseTime   time.Time
}

func (l *testRequestLogger) LogRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, websocketTimeline, apiRequest, apiResponse, apiWebsocketTimeline []byte, apiResponseErrors []*interfaces.ErrorMessage, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	return l.capture(false, body, statusCode, responseHeaders, response, websocketTimeline, apiRequest, apiResponse, apiWebsocketTimeline, apiResponseErrors, requestID, requestTimestamp, apiResponseTimestamp)
}

func (l *testRequestLogger) LogRequestWithOptions(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, websocketTimeline, apiRequest, apiResponse, apiWebsocketTimeline []byte, apiResponseErrors []*interfaces.ErrorMessage, force bool, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	return l.capture(force, body, statusCode, responseHeaders, response, websocketTimeline, apiRequest, apiResponse, apiWebsocketTimeline, apiResponseErrors, requestID, requestTimestamp, apiResponseTimestamp)
}

func (l *testRequestLogger) capture(force bool, body []byte, statusCode int, responseHeaders map[string][]string, response, websocketTimeline, apiRequest, apiResponse, apiWebsocketTimeline []byte, apiResponseErrors []*interfaces.ErrorMessage, requestID string, requestTimestamp, apiResponseTimestamp time.Time) error {
	l.logRequestCalled = true
	l.logRequestForce = force
	l.lastRequestBody = bytes.Clone(body)
	l.lastStatusCode = statusCode
	l.lastResponseHeaders = cloneHeaderMap(responseHeaders)
	l.lastResponseBody = bytes.Clone(response)
	l.lastWebsocketTimeline = bytes.Clone(websocketTimeline)
	l.lastAPIRequestBody = bytes.Clone(apiRequest)
	l.lastAPIResponseBody = bytes.Clone(apiResponse)
	l.lastAPIWebsocketBody = bytes.Clone(apiWebsocketTimeline)
	if len(apiResponseErrors) > 0 {
		l.lastAPIResponseErrors = append([]*interfaces.ErrorMessage(nil), apiResponseErrors...)
	} else {
		l.lastAPIResponseErrors = nil
	}
	l.lastRequestID = requestID
	l.lastRequestTimestamp = requestTimestamp
	l.lastAPIResponseTime = apiResponseTimestamp
	return nil
}

func (l *testRequestLogger) LogStreamingRequest(string, string, map[string][]string, []byte, string) (logging.StreamingLogWriter, error) {
	return &testStreamingLogWriter{}, nil
}

func (l *testRequestLogger) IsEnabled() bool {
	return l.enabled
}

func cloneHeaderMap(headers map[string][]string) map[string][]string {
	if headers == nil {
		return nil
	}
	cloned := make(map[string][]string, len(headers))
	for key, values := range headers {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func newTestWrapper(t *testing.T, logger *testRequestLogger) (*gin.Context, *httptest.ResponseRecorder, *ResponseWriterWrapper) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	wrapper := NewResponseWriterWrapper(c.Writer, logger, &RequestInfo{
		URL:       "/v1/responses",
		Method:    "POST",
		Headers:   map[string][]string{"Content-Type": {"application/json"}},
		Body:      []byte("{\"input\":\"test\"}"),
		RequestID: "req-test",
		Timestamp: time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC),
	})
	c.Writer = wrapper
	return c, recorder, wrapper
}

func repeatBytes(ch byte, size int) []byte {
	return bytes.Repeat([]byte{ch}, size)
}

func repeatString(ch byte, size int) string {
	return string(repeatBytes(ch, size))
}

func requireFinalize(t *testing.T, wrapper *ResponseWriterWrapper, c *gin.Context) {
	t.Helper()
	if err := wrapper.Finalize(c); err != nil {
		t.Fatalf("Finalize error: %v", err)
	}
}

func requireLogBodyEquals(t *testing.T, logger *testRequestLogger, want []byte) {
	t.Helper()
	if !bytes.Equal(logger.lastResponseBody, want) {
		t.Fatalf("logged response body length = %d, want %d", len(logger.lastResponseBody), len(want))
	}
}

func requireTruncatedLogBody(t *testing.T, logger *testRequestLogger, prefix []byte) {
	t.Helper()
	want := append(bytes.Clone(prefix), []byte(truncatedResponseBodyLogNote)...)
	requireLogBodyEquals(t, logger, want)
}

type testStreamingLogWriter struct {
	apiWebsocketTimeline []byte
	closed               bool
}

func (w *testStreamingLogWriter) WriteChunkAsync([]byte) {}

func (w *testStreamingLogWriter) WriteStatus(int, map[string][]string) error {
	return nil
}

func (w *testStreamingLogWriter) WriteAPIRequest([]byte) error {
	return nil
}

func (w *testStreamingLogWriter) WriteAPIResponse([]byte) error {
	return nil
}

func (w *testStreamingLogWriter) WriteAPIWebsocketTimeline(apiWebsocketTimeline []byte) error {
	w.apiWebsocketTimeline = bytes.Clone(apiWebsocketTimeline)
	return nil
}

func (w *testStreamingLogWriter) SetFirstChunkTimestamp(time.Time) {}

func (w *testStreamingLogWriter) Close() error {
	w.closed = true
	return nil
}
