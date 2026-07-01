package helps

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	internalusage "github.com/NGLSL/CLIProxyAPI/v7/internal/usage"
	coreusage "github.com/NGLSL/CLIProxyAPI/v7/sdk/cliproxy/usage"
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

func TestParseOpenAIUsageDeepSeek(t *testing.T) {
	data := []byte(`{"usage":{"prompt_tokens":11,"completion_tokens":22,"total_tokens":33,"prompt_cache_hit_tokens":4,"prompt_cache_miss_tokens":7,"completion_tokens_details":{"reasoning_tokens":6}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 11 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 11)
	}
	if detail.OutputTokens != 22 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 22)
	}
	if detail.TotalTokens != 33 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 33)
	}
	if detail.CachedTokens != 4 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 4)
	}
	if detail.ReasoningTokens != 6 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 6)
	}
}

func TestParseOpenAIUsageSkipsMissingTokenFields(t *testing.T) {
	for _, data := range [][]byte{
		[]byte(`{"usage":null}`),
		[]byte(`{"usage":{}}`),
	} {
		if detail := ParseOpenAIUsage(data); detail != (coreusage.Detail{}) {
			t.Fatalf("ParseOpenAIUsage(%s) = %+v, want zero detail", data, detail)
		}
	}
}

func TestParseOpenAIStreamUsageSkipsMissingTokenFields(t *testing.T) {
	for _, line := range [][]byte{
		[]byte(`data: {"choices":[{"delta":{"content":"hello"}}],"usage":null}`),
		[]byte(`data: {"choices":[{"delta":{"content":"hello"}}],"usage":{}}`),
	} {
		if detail, ok := ParseOpenAIStreamUsage(line); ok {
			t.Fatalf("ParseOpenAIStreamUsage(%s) ok = true, detail = %+v, want false", line, detail)
		}
	}
}

func TestParseOpenAIStreamUsageDeepSeekFinalChunk(t *testing.T) {
	line := []byte(`data: {"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":22,"total_tokens":33,"prompt_cache_hit_tokens":4,"prompt_cache_miss_tokens":7,"completion_tokens_details":{"reasoning_tokens":6}}}`)
	detail, ok := ParseOpenAIStreamUsage(line)
	if !ok {
		t.Fatalf("ParseOpenAIStreamUsage ok = false, want true")
	}
	if detail.InputTokens != 11 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 11)
	}
	if detail.OutputTokens != 22 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 22)
	}
	if detail.TotalTokens != 33 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 33)
	}
	if detail.CachedTokens != 4 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 4)
	}
	if detail.ReasoningTokens != 6 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 6)
	}
}

func TestParseClaudeUsageIncludesCacheTokensInTotal(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":3085,"output_tokens":253,"cache_read_input_tokens":7,"cache_creation_input_tokens":19514}}`)
	detail := ParseClaudeUsage(data)
	if detail.InputTokens != 3085 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 3085)
	}
	if detail.OutputTokens != 253 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 253)
	}
	if detail.CacheReadTokens != 7 {
		t.Fatalf("cache read tokens = %d, want %d", detail.CacheReadTokens, 7)
	}
	if detail.CacheCreationTokens != 19514 {
		t.Fatalf("cache creation tokens = %d, want %d", detail.CacheCreationTokens, 19514)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.TotalTokens != 22859 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 22859)
	}
}

func TestParseClaudeUsageFallsBackCachedTokensToCacheCreation(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":3085,"output_tokens":253,"cache_creation_input_tokens":19514}}`)
	detail := ParseClaudeUsage(data)
	if detail.CachedTokens != 19514 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 19514)
	}
	if detail.TotalTokens != 22852 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 22852)
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
	if record.APIFirstByteLatency != 250*time.Millisecond {
		t.Fatalf("api first byte latency = %v, want 250ms", record.APIFirstByteLatency)
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
	if record.APIFirstByteLatency != 250*time.Millisecond {
		t.Fatalf("api first byte latency = %v, want 250ms", record.APIFirstByteLatency)
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
	if record.FirstByteLatency <= 0 {
		t.Fatalf("first byte latency = %v, want positive client write latency", record.FirstByteLatency)
	}
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

func TestParseClaudeUsageIncludesCacheBreakdown(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":13,"output_tokens":4,"cache_read_input_tokens":22000,"cache_creation_input_tokens":31}}`)
	detail := ParseClaudeUsage(data)
	if detail.InputTokens != 22044 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 22044)
	}
	if detail.OutputTokens != 4 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 4)
	}
	if detail.CachedTokens != 22000 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 22000)
	}
	if detail.CacheReadTokens != 22000 {
		t.Fatalf("cache read tokens = %d, want %d", detail.CacheReadTokens, 22000)
	}
	if detail.CacheCreationTokens != 31 {
		t.Fatalf("cache creation tokens = %d, want %d", detail.CacheCreationTokens, 31)
	}
	if detail.TotalTokens != 22048 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 22048)
	}
}

func TestUsageReporterBuildRecordIncludesReasoningEffort(t *testing.T) {
	ctx := coreusage.WithReasoningEffort(context.Background(), "medium")
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(ctx, coreusage.Detail{TotalTokens: 3}, false)
	if record.ReasoningEffort != "medium" {
		t.Fatalf("reasoning effort = %q, want %q", record.ReasoningEffort, "medium")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type TestUsageExecutor struct{}

func (TestUsageExecutor) Identifier() string {
	return "test-provider"
}
