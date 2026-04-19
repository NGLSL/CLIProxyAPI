package helps

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	internalusage "github.com/NGLSL/CLIProxyAPI/v6/internal/usage"
	coreusage "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/usage"
	"github.com/gin-gonic/gin"
)

func TestParseOpenAIUsageChatCompletions(t *testing.T) {
	data := []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":5}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 1 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 1)
	}
	if detail.OutputTokens != 2 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 2)
	}
	if detail.TotalTokens != 3 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 3)
	}
	if detail.CachedTokens != 4 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 4)
	}
	if detail.ReasoningTokens != 5 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 5)
	}
}

func TestParseOpenAIUsageResponses(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":9}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 20 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 20)
	}
	if detail.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 30)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.ReasoningTokens != 9 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 9)
	}
}

func TestUsageReporterBuildRecordIncludesLatency(t *testing.T) {
	reporter := &UsageReporter{
		provider:    "openai",
		model:       "gpt-5.4",
		requestedAt: time.Now().Add(-1500 * time.Millisecond),
	}

	record := reporter.buildRecord(context.Background(), coreusage.Detail{TotalTokens: 3}, false)
	if record.Latency < time.Second {
		t.Fatalf("latency = %v, want >= 1s", record.Latency)
	}
	if record.Latency > 3*time.Second {
		t.Fatalf("latency = %v, want <= 3s", record.Latency)
	}
	if record.FirstByteLatency != 0 {
		t.Fatalf("first byte latency = %v, want 0", record.FirstByteLatency)
	}
}

func TestUsageReporterBuildRecordIncludesFirstByteLatency(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	requestedAt := time.Now().Add(-1500 * time.Millisecond)
	firstByteAt := requestedAt.Add(250 * time.Millisecond)
	ginCtx.Set("API_RESPONSE_TIMESTAMP", firstByteAt)

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	reporter := &UsageReporter{
		provider:    "openai",
		model:       "gpt-5.4",
		requestedAt: requestedAt,
	}

	record := reporter.buildRecord(ctx, coreusage.Detail{TotalTokens: 3}, false)
	if record.FirstByteLatency != 250*time.Millisecond {
		t.Fatalf("first byte latency = %v, want 250ms", record.FirstByteLatency)
	}
}

func TestUsageReporterBuildRecordPrefersAttemptFirstByteLatency(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	requestedAt := time.Now().Add(-1500 * time.Millisecond)
	staleFirstByteAt := requestedAt.Add(-200 * time.Millisecond)
	attemptFirstByteAt := requestedAt.Add(250 * time.Millisecond)
	ginCtx.Set("API_RESPONSE_TIMESTAMP", staleFirstByteAt)
	ginCtx.Set(apiAttemptResponseTimestampKey, attemptFirstByteAt)

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	reporter := &UsageReporter{
		provider:    "openai",
		model:       "gpt-5.4",
		requestedAt: requestedAt,
	}

	record := reporter.buildRecord(ctx, coreusage.Detail{TotalTokens: 3}, false)
	if record.FirstByteLatency != 250*time.Millisecond {
		t.Fatalf("first byte latency = %v, want 250ms", record.FirstByteLatency)
	}
}

func TestUsageReporterBuildRecordDoesNotFallbackToGlobalTimestampAfterAttemptReset(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	requestedAt := time.Now().Add(-1500 * time.Millisecond)
	staleFirstByteAt := requestedAt.Add(-200 * time.Millisecond)
	ginCtx.Set("API_RESPONSE_TIMESTAMP", staleFirstByteAt)
	ginCtx.Set(apiAttemptResponseTimestampKey, time.Time{})

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	reporter := &UsageReporter{
		provider:    "openai",
		model:       "gpt-5.4",
		requestedAt: requestedAt,
	}

	record := reporter.buildRecord(ctx, coreusage.Detail{TotalTokens: 3}, false)
	if record.FirstByteLatency != 0 {
		t.Fatalf("first byte latency = %v, want 0", record.FirstByteLatency)
	}
}

func TestUsageReporterBuildRecordIncludesRequestMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	internalusage.EnsureRequestMetrics(ginCtx)
	internalusage.ObserveResponseWrite(ginCtx, 12, true)
	internalusage.ObserveResponseWrite(ginCtx, 8, true)
	internalusage.ObserveAPIResponseChunk(ginCtx, 21)

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	reporter := &UsageReporter{
		provider:    "openai",
		model:       "gpt-5.4",
		requestedAt: time.Now().Add(-time.Second),
	}

	record := reporter.buildRecord(ctx, coreusage.Detail{TotalTokens: 3}, false)
	if record.ChunkCount != 2 {
		t.Fatalf("chunk count = %d, want 2", record.ChunkCount)
	}
	if record.ResponseBytes != 20 {
		t.Fatalf("response bytes = %d, want 20", record.ResponseBytes)
	}
	if record.APIResponseBytes != 21 {
		t.Fatalf("api response bytes = %d, want 21", record.APIResponseBytes)
	}
}
