package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

type openAIClaudeSSEEvent struct {
	Type    string
	Payload string
}

func runOpenAIClaudeStream(t *testing.T, chunks ...string) []openAIClaudeSSEEvent {
	t.Helper()

	var param any
	var emitted [][]byte
	originalRequest := []byte(`{"stream":true}`)
	for _, chunk := range chunks {
		emitted = append(emitted, ConvertOpenAIResponseToClaude(
			context.Background(),
			"test-model",
			originalRequest,
			nil,
			[]byte("data: "+chunk),
			&param,
		)...)
	}
	emitted = append(emitted, ConvertOpenAIResponseToClaude(
		context.Background(),
		"test-model",
		originalRequest,
		nil,
		[]byte("data: [DONE]"),
		&param,
	)...)

	var events []openAIClaudeSSEEvent
	for _, raw := range emitted {
		text := string(raw)
		if !strings.HasPrefix(text, "event: ") {
			continue
		}
		lineEnd := strings.IndexByte(text, '\n')
		if lineEnd < 0 {
			continue
		}
		eventType := strings.TrimPrefix(text[:lineEnd], "event: ")
		data := text[lineEnd+1:]
		if !strings.HasPrefix(data, "data: ") {
			continue
		}
		events = append(events, openAIClaudeSSEEvent{
			Type:    eventType,
			Payload: strings.TrimRight(strings.TrimPrefix(data, "data: "), "\n"),
		})
	}
	return events
}

func countOpenAIClaudeEvents(events []openAIClaudeSSEEvent, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func openAIClaudeToolUseStarts(events []openAIClaudeSSEEvent) []openAIClaudeSSEEvent {
	var starts []openAIClaudeSSEEvent
	for _, event := range events {
		if event.Type != "content_block_start" {
			continue
		}
		if gjson.Get(event.Payload, "content_block.type").String() == "tool_use" {
			starts = append(starts, event)
		}
	}
	return starts
}

func lastOpenAIClaudeStopReason(events []openAIClaudeSSEEvent) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "message_delta" {
			return gjson.Get(events[i].Payload, "delta.stop_reason").String()
		}
	}
	return ""
}

func TestOpenAIClaudeStreamSuppressesInvalidToolBlocks(t *testing.T) {
	events := runOpenAIClaudeStream(t,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_skip","function":{"name":"","arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":null,"arguments":"{\"x\":1}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	if got := len(openAIClaudeToolUseStarts(events)); got != 0 {
		t.Fatalf("expected zero tool_use starts for invalid tool metadata, got %d", got)
	}
	if got := countOpenAIClaudeEvents(events, "content_block_delta"); got != 0 {
		t.Fatalf("expected zero tool deltas for suppressed tool blocks, got %d", got)
	}
	if got := countOpenAIClaudeEvents(events, "content_block_stop"); got != 0 {
		t.Fatalf("expected zero content_block_stop for suppressed tool blocks, got %d", got)
	}
	if got := lastOpenAIClaudeStopReason(events); got == "tool_use" {
		t.Fatalf("stop_reason must not be tool_use without a tool_use block, got %q", got)
	}
}

func TestOpenAIClaudeStreamStartsToolWhenIDArrivesLater(t *testing.T) {
	events := runOpenAIClaudeStream(t,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"function":{"name":"do_it"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_real"}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := openAIClaudeToolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected exactly one tool_use start when id arrives later, got %d", len(starts))
	}
	if id := gjson.Get(starts[0].Payload, "content_block.id").String(); id != "call_real" {
		t.Fatalf("tool id = %q, want call_real", id)
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "do_it" {
		t.Fatalf("tool name = %q, want do_it", name)
	}
	if got := countOpenAIClaudeEvents(events, "content_block_stop"); got != 1 {
		t.Fatalf("expected exactly one tool block stop, got %d", got)
	}
}

func TestOpenAIClaudeStreamKeepsSingleToolStartForSplitArguments(t *testing.T) {
	events := runOpenAIClaudeStream(t,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":"do_it","arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"do_it","arguments":"{\"x\""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"do_it","arguments":":1}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := openAIClaudeToolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected one tool_use start for repeated name chunks, got %d", len(starts))
	}
	if got := countOpenAIClaudeEvents(events, "content_block_delta"); got != 1 {
		t.Fatalf("expected one aggregated input_json_delta, got %d", got)
	}
	if got := lastOpenAIClaudeStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", got)
	}
}

func TestOpenAIClaudeStreamOrdersMultipleToolBlocks(t *testing.T) {
	events := runOpenAIClaudeStream(t,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":1,"id":"call_b","function":{"name":"tool_b","arguments":"{}"}},{"index":0,"id":"call_a","function":{"name":"tool_a","arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := openAIClaudeToolUseStarts(events)
	if len(starts) != 2 {
		t.Fatalf("expected two tool_use starts, got %d", len(starts))
	}
	if firstName := gjson.Get(starts[0].Payload, "content_block.name").String(); firstName != "tool_b" {
		t.Fatalf("first emitted live tool name = %q, want tool_b", firstName)
	}
	if got := countOpenAIClaudeEvents(events, "content_block_stop"); got != 2 {
		t.Fatalf("expected two ordered tool block stops, got %d", got)
	}
}
