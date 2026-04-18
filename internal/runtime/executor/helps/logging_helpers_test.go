package helps

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/config"
	"github.com/gin-gonic/gin"
)

func TestAPIResponseAggregationSingleAttemptPreservesFormat(t *testing.T) {
	t.Parallel()

	ctx, ginCtx := newLoggingHelpersTestContext(t)
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}

	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{
		URL:     "https://example.com/v1/messages",
		Method:  http.MethodPost,
		Headers: http.Header{"X-Test": {"request"}},
		Body:    []byte(`{"input":"hello"}`),
	})
	RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, http.Header{"Content-Type": {"application/json"}})
	AppendAPIResponseChunk(ctx, cfg, []byte(`{"type":"message_start"}`))
	AppendAPIResponseChunk(ctx, cfg, []byte(`{"type":"message_delta"}`))

	got := string(mustAPIResponseBytes(t, ginCtx))
	assertContainsInOrder(t, got,
		"=== API RESPONSE 1 ===\n",
		"Timestamp: ",
		"\n\nStatus: 200\n",
		"Headers:\n",
		"Content-Type: application/json\n",
		"\nBody:\n",
		`{"type":"message_start"}`,
		"\n\n",
		`{"type":"message_delta"}`,
	)
	if strings.Contains(got, `{"type":"message_delta"}`+"\n\n") {
		t.Fatalf("single attempt should not end with duplicated blank lines, got %q", got)
	}
}

func TestAPIResponseAggregationMultipleAttemptsPreservesSeparator(t *testing.T) {
	t.Parallel()

	ctx, ginCtx := newLoggingHelpersTestContext(t)
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}

	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{URL: "https://example.com/1", Method: http.MethodPost})
	RecordAPIResponseMetadata(ctx, cfg, http.StatusBadGateway, http.Header{"X-Attempt": {"1"}})
	AppendAPIResponseChunk(ctx, cfg, []byte("first-body"))

	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{URL: "https://example.com/2", Method: http.MethodPost})
	RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, http.Header{"X-Attempt": {"2"}})
	AppendAPIResponseChunk(ctx, cfg, []byte("second-body"))

	got := string(mustAPIResponseBytes(t, ginCtx))
	assertContainsInOrder(t, got,
		"=== API RESPONSE 1 ===\n",
		"Status: 502\n",
		"Body:\nfirst-body\n\n=== API RESPONSE 2 ===\n",
		"Status: 200\n",
		"Body:\nsecond-body",
	)
}

func TestAPIResponseAggregationAppendsErrorsInOrder(t *testing.T) {
	t.Parallel()

	ctx, ginCtx := newLoggingHelpersTestContext(t)
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}

	RecordAPIResponseMetadata(ctx, cfg, http.StatusBadRequest, nil)
	AppendAPIResponseChunk(ctx, cfg, []byte("partial-body"))
	RecordAPIResponseError(ctx, cfg, errors.New("first failure"))
	RecordAPIResponseError(ctx, cfg, errors.New("second failure"))

	got := string(mustAPIResponseBytes(t, ginCtx))
	assertContainsInOrder(t, got,
		"Status: 400\n",
		"Headers:\n<none>\n\n",
		"Body:\npartial-body\nError: first failure\n\nError: second failure\n",
	)
}

func TestAPIResponseAggregationSnapshotsRemainStableDuringSameAttempt(t *testing.T) {
	t.Parallel()

	ctx, ginCtx := newLoggingHelpersTestContext(t)
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}

	RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, nil)
	AppendAPIResponseChunk(ctx, cfg, []byte("chunk-one"))
	snapshotAfterFirstChunk := string(mustAPIResponseBytes(t, ginCtx))
	if !strings.HasSuffix(snapshotAfterFirstChunk, "chunk-one\n") {
		t.Fatalf("first snapshot should keep a stable trailing newline, got %q", snapshotAfterFirstChunk)
	}

	AppendAPIResponseChunk(ctx, cfg, []byte("chunk-two"))
	snapshotAfterSecondChunk := string(mustAPIResponseBytes(t, ginCtx))
	if strings.Contains(snapshotAfterSecondChunk, "chunk-one\n\n\nchunk-two") {
		t.Fatalf("synthetic newline should be removed before appending next chunk, got %q", snapshotAfterSecondChunk)
	}
	assertContainsInOrder(t, snapshotAfterSecondChunk,
		"Body:\nchunk-one\n\nchunk-two",
	)

	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{URL: "https://example.com/retry", Method: http.MethodPost})
	RecordAPIResponseMetadata(ctx, cfg, http.StatusCreated, nil)
	snapshotAfterSecondAttempt := string(mustAPIResponseBytes(t, ginCtx))
	if !strings.Contains(snapshotAfterSecondAttempt, "chunk-two\n\n=== API RESPONSE 2 ===\n") {
		t.Fatalf("attempt separator should remain a single blank line, got %q", snapshotAfterSecondAttempt)
	}
}

func newLoggingHelpersTestContext(t *testing.T) (context.Context, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ginCtx := &gin.Context{}
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	return ctx, ginCtx
}

func mustAPIResponseBytes(t *testing.T, ginCtx *gin.Context) []byte {
	t.Helper()
	value, exists := ginCtx.Get(apiResponseKey)
	if !exists {
		t.Fatal("API_RESPONSE was not set")
	}
	data, ok := value.([]byte)
	if !ok {
		t.Fatalf("API_RESPONSE type = %T, want []byte", value)
	}
	return data
}

func assertContainsInOrder(t *testing.T, text string, parts ...string) {
	t.Helper()
	start := 0
	for _, part := range parts {
		idx := strings.Index(text[start:], part)
		if idx < 0 {
			t.Fatalf("text %q does not contain %q after offset %d", text, part, start)
		}
		start += idx + len(part)
	}
}
