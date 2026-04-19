package usage

import (
	"context"
	"fmt"
	"testing"
	"time"

	coreusage "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestRequestStatisticsRecordIncludesLatency(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		Latency:     1500 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	if details[0].LatencyMs != 1500 {
		t.Fatalf("latency_ms = %d, want 1500", details[0].LatencyMs)
	}
	if details[0].FirstByteLatencyMs != nil {
		t.Fatalf("first_byte_latency_ms = %v, want nil", *details[0].FirstByteLatencyMs)
	}
}

func TestRequestStatisticsRecordIncludesFirstByteLatency(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:           "test-key",
		Model:            "gpt-5.4",
		RequestedAt:      time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		FirstByteLatency: 250 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	if details[0].FirstByteLatencyMs == nil {
		t.Fatal("first_byte_latency_ms = nil, want value")
	}
	if *details[0].FirstByteLatencyMs != 250 {
		t.Fatalf("first_byte_latency_ms = %d, want 250", *details[0].FirstByteLatencyMs)
	}
}

func TestRequestStatisticsMergeSnapshotDedupIgnoresLatency(t *testing.T) {
	stats := NewRequestStatistics()
	timestamp := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	first := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 0,
							Source:    "user@example.com",
							AuthIndex: "0",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}
	second := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 2500,
							Source:    "user@example.com",
							AuthIndex: "0",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}

	result := stats.MergeSnapshot(first)
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("first merge = %+v, want added=1 skipped=0", result)
	}

	result = stats.MergeSnapshot(second)
	if result.Added != 0 || result.Skipped != 1 {
		t.Fatalf("second merge = %+v, want added=0 skipped=1", result)
	}

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
}

func TestRequestStatisticsRecordTrimsDetailsButKeepsAggregateTotals(t *testing.T) {
	stats := NewRequestStatistics()
	baseTime := time.Date(2026, 3, 21, 8, 0, 0, 0, time.UTC)

	var expectedTotalTokens int64
	var expectedSuccessCount int64
	var expectedFailureCount int64

	for i := 0; i < maxRequestDetailsPerModel+25; i++ {
		failed := i%3 == 0
		tokens := int64(i + 1)
		expectedTotalTokens += tokens
		if failed {
			expectedFailureCount++
		} else {
			expectedSuccessCount++
		}

		stats.Record(context.Background(), coreusage.Record{
			APIKey:      "test-key",
			Model:       "gpt-5.4",
			RequestedAt: baseTime.Add(time.Duration(i) * time.Minute),
			Failed:      failed,
			Source:      fmt.Sprintf("source-%d", i),
			AuthIndex:   fmt.Sprintf("auth-%d", i),
			Detail: coreusage.Detail{
				InputTokens: tokens,
				TotalTokens: tokens,
			},
		})
	}

	snapshot := stats.Snapshot()
	apiSnapshot := snapshot.APIs["test-key"]
	modelSnapshot := apiSnapshot.Models["gpt-5.4"]

	if got := len(modelSnapshot.Details); got != maxRequestDetailsPerModel {
		t.Fatalf("details len = %d, want %d", got, maxRequestDetailsPerModel)
	}

	firstKeptIndex := 25
	lastKeptIndex := maxRequestDetailsPerModel + 24
	if got := modelSnapshot.Details[0].Source; got != fmt.Sprintf("source-%d", firstKeptIndex) {
		t.Fatalf("first kept source = %q, want %q", got, fmt.Sprintf("source-%d", firstKeptIndex))
	}
	if got := modelSnapshot.Details[0].AuthIndex; got != fmt.Sprintf("auth-%d", firstKeptIndex) {
		t.Fatalf("first kept auth index = %q, want %q", got, fmt.Sprintf("auth-%d", firstKeptIndex))
	}
	if got := modelSnapshot.Details[maxRequestDetailsPerModel-1].Source; got != fmt.Sprintf("source-%d", lastKeptIndex) {
		t.Fatalf("last kept source = %q, want %q", got, fmt.Sprintf("source-%d", lastKeptIndex))
	}
	if got := modelSnapshot.Details[maxRequestDetailsPerModel-1].AuthIndex; got != fmt.Sprintf("auth-%d", lastKeptIndex) {
		t.Fatalf("last kept auth index = %q, want %q", got, fmt.Sprintf("auth-%d", lastKeptIndex))
	}

	if snapshot.TotalRequests != int64(maxRequestDetailsPerModel+25) {
		t.Fatalf("total requests = %d, want %d", snapshot.TotalRequests, maxRequestDetailsPerModel+25)
	}
	if snapshot.SuccessCount != expectedSuccessCount {
		t.Fatalf("success count = %d, want %d", snapshot.SuccessCount, expectedSuccessCount)
	}
	if snapshot.FailureCount != expectedFailureCount {
		t.Fatalf("failure count = %d, want %d", snapshot.FailureCount, expectedFailureCount)
	}
	if snapshot.TotalTokens != expectedTotalTokens {
		t.Fatalf("total tokens = %d, want %d", snapshot.TotalTokens, expectedTotalTokens)
	}

	if apiSnapshot.TotalRequests != int64(maxRequestDetailsPerModel+25) {
		t.Fatalf("api total requests = %d, want %d", apiSnapshot.TotalRequests, maxRequestDetailsPerModel+25)
	}
	if apiSnapshot.TotalTokens != expectedTotalTokens {
		t.Fatalf("api total tokens = %d, want %d", apiSnapshot.TotalTokens, expectedTotalTokens)
	}
	if modelSnapshot.TotalRequests != int64(maxRequestDetailsPerModel+25) {
		t.Fatalf("model total requests = %d, want %d", modelSnapshot.TotalRequests, maxRequestDetailsPerModel+25)
	}
	if modelSnapshot.TotalTokens != expectedTotalTokens {
		t.Fatalf("model total tokens = %d, want %d", modelSnapshot.TotalTokens, expectedTotalTokens)
	}
}

func TestRequestStatisticsMergeSnapshotNormalisesRequestMetricFields(t *testing.T) {
	stats := NewRequestStatistics()
	result := stats.MergeSnapshot(StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"import-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp:        time.Date(2026, 3, 22, 9, 0, 0, 0, time.UTC),
							ChunkCount:       -1,
							ResponseBytes:    -2,
							APIResponseBytes: -3,
							Tokens:           TokenStats{InputTokens: 1, TotalTokens: 1},
						}},
					},
				},
			},
		},
	})
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("merge result = %+v, want added=1 skipped=0", result)
	}

	detail := stats.Snapshot().APIs["import-key"].Models["gpt-5.4"].Details[0]
	if detail.ChunkCount != 0 {
		t.Fatalf("chunk_count = %d, want 0", detail.ChunkCount)
	}
	if detail.ResponseBytes != 0 {
		t.Fatalf("response_bytes = %d, want 0", detail.ResponseBytes)
	}
	if detail.APIResponseBytes != 0 {
		t.Fatalf("api_response_bytes = %d, want 0", detail.APIResponseBytes)
	}
}

func TestRequestStatisticsMergeSnapshotTrimsDetailsButKeepsAggregateTotals(t *testing.T) {
	stats := NewRequestStatistics()
	baseTime := time.Date(2026, 3, 22, 9, 0, 0, 0, time.UTC)

	details := make([]RequestDetail, 0, maxRequestDetailsPerModel+15)
	var expectedTotalTokens int64
	var expectedSuccessCount int64
	var expectedFailureCount int64

	for i := 0; i < maxRequestDetailsPerModel+15; i++ {
		failed := i%4 == 0
		tokens := int64((i + 1) * 2)
		expectedTotalTokens += tokens
		if failed {
			expectedFailureCount++
		} else {
			expectedSuccessCount++
		}
		details = append(details, RequestDetail{
			Timestamp: baseTime.Add(time.Duration(i) * time.Minute),
			Source:    fmt.Sprintf("import-source-%d", i),
			AuthIndex: fmt.Sprintf("import-auth-%d", i),
			Failed:    failed,
			Tokens: TokenStats{
				InputTokens: tokens,
				TotalTokens: tokens,
			},
		})
	}

	result := stats.MergeSnapshot(StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"import-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: details,
					},
				},
			},
		},
	})
	if result.Added != int64(maxRequestDetailsPerModel+15) || result.Skipped != 0 {
		t.Fatalf("merge result = %+v, want added=%d skipped=0", result, maxRequestDetailsPerModel+15)
	}

	snapshot := stats.Snapshot()
	apiSnapshot := snapshot.APIs["import-key"]
	modelSnapshot := apiSnapshot.Models["gpt-5.4"]

	if got := len(modelSnapshot.Details); got != maxRequestDetailsPerModel {
		t.Fatalf("details len = %d, want %d", got, maxRequestDetailsPerModel)
	}

	firstKeptIndex := 15
	lastKeptIndex := maxRequestDetailsPerModel + 14
	if got := modelSnapshot.Details[0].Source; got != fmt.Sprintf("import-source-%d", firstKeptIndex) {
		t.Fatalf("first kept source = %q, want %q", got, fmt.Sprintf("import-source-%d", firstKeptIndex))
	}
	if got := modelSnapshot.Details[maxRequestDetailsPerModel-1].Source; got != fmt.Sprintf("import-source-%d", lastKeptIndex) {
		t.Fatalf("last kept source = %q, want %q", got, fmt.Sprintf("import-source-%d", lastKeptIndex))
	}

	if snapshot.TotalRequests != int64(maxRequestDetailsPerModel+15) {
		t.Fatalf("total requests = %d, want %d", snapshot.TotalRequests, maxRequestDetailsPerModel+15)
	}
	if snapshot.SuccessCount != expectedSuccessCount {
		t.Fatalf("success count = %d, want %d", snapshot.SuccessCount, expectedSuccessCount)
	}
	if snapshot.FailureCount != expectedFailureCount {
		t.Fatalf("failure count = %d, want %d", snapshot.FailureCount, expectedFailureCount)
	}
	if snapshot.TotalTokens != expectedTotalTokens {
		t.Fatalf("total tokens = %d, want %d", snapshot.TotalTokens, expectedTotalTokens)
	}
	if apiSnapshot.TotalRequests != int64(maxRequestDetailsPerModel+15) {
		t.Fatalf("api total requests = %d, want %d", apiSnapshot.TotalRequests, maxRequestDetailsPerModel+15)
	}
	if apiSnapshot.TotalTokens != expectedTotalTokens {
		t.Fatalf("api total tokens = %d, want %d", apiSnapshot.TotalTokens, expectedTotalTokens)
	}
	if modelSnapshot.TotalRequests != int64(maxRequestDetailsPerModel+15) {
		t.Fatalf("model total requests = %d, want %d", modelSnapshot.TotalRequests, maxRequestDetailsPerModel+15)
	}
	if modelSnapshot.TotalTokens != expectedTotalTokens {
		t.Fatalf("model total tokens = %d, want %d", modelSnapshot.TotalTokens, expectedTotalTokens)
	}
}
