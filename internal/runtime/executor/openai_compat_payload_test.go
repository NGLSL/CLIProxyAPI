package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAICompatToolOutputsKeepsLatestChatToolResult(t *testing.T) {
	body := []byte(`{"model":"test","messages":[` +
		`{"role":"user","content":"run"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":"old"},` +
		`{"role":"tool","tool_call_id":"call_2","content":"other"},` +
		`{"role":"tool","tool_call_id":"call_1","content":"latest"}` +
		`]}`)

	got := normalizeOpenAICompatToolOutputs(body)
	messages := gjson.GetBytes(got, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("messages length = %d, want 4; body=%s", len(messages), got)
	}
	if content := messages[3].Get("content").String(); content != "latest" {
		t.Fatalf("last call_1 tool result = %q, want latest; body=%s", content, got)
	}
	if old := gjson.GetBytes(got, `messages.#(content=="old")`); old.Exists() {
		t.Fatalf("stale duplicate tool result should be removed: %s", got)
	}
}

func TestNormalizeOpenAICompatToolOutputsKeepsLatestResponsesOutput(t *testing.T) {
	body := []byte(`{"model":"test","input":[` +
		`{"type":"function_call_output","call_id":"call_1","output":"old"},` +
		`{"type":"message","role":"user","content":"next"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"latest"}` +
		`]}`)

	got := normalizeOpenAICompatToolOutputs(body)
	items := gjson.GetBytes(got, "input").Array()
	if len(items) != 2 {
		t.Fatalf("input length = %d, want 2; body=%s", len(items), got)
	}
	if output := items[1].Get("output").String(); output != "latest" {
		t.Fatalf("remaining tool output = %q, want latest; body=%s", output, got)
	}
}
