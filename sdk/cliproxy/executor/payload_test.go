package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestDedupeToolOutputsResponsesKeepsLastOutput(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"message","role":"user","content":"x"},
			{"type":"function_call_output","call_id":"call_a","output":"old"},
			{"type":"tool_search_output","call_id":"call_b","output":"search-old"},
			{"type":"function_call_output","call_id":"call_a","output":"new"},
			{"type":"tool_search_output","call_id":"call_b","output":"search-new"}
		]
	}`)

	out := DedupeToolOutputs(body)
	items := gjson.GetBytes(out, "input").Array()
	if len(items) != 3 {
		t.Fatalf("input length = %d, want 3; payload=%s", len(items), out)
	}
	if got := items[1].Get("output").String(); got != "new" {
		t.Fatalf("call_a output = %q, want new", got)
	}
	if got := items[2].Get("output").String(); got != "search-new" {
		t.Fatalf("call_b output = %q, want search-new", got)
	}
}

func TestDedupeToolOutputsChatHandlesToolCallIDAndCallID(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":""},
			{"role":"tool","call_id":"call_a","content":"old-a"},
			{"role":"tool","tool_call_id":"call_a","content":"new-a"},
			{"role":"tool","call_id":"call_b","content":"old-b"},
			{"role":"tool","call_id":"call_b","content":"new-b"}
		]
	}`)

	out := DedupeToolOutputs(body)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 3 {
		t.Fatalf("messages length = %d, want 3; payload=%s", len(messages), out)
	}
	if got := messages[1].Get("content").String(); got != "new-a" {
		t.Fatalf("call_a content = %q, want new-a", got)
	}
	if got := messages[2].Get("content").String(); got != "new-b" {
		t.Fatalf("call_b content = %q, want new-b", got)
	}
}

func TestDedupeToolOutputsClaudeToolResults(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_a","content":"old"}]},
			{"role":"user","content":[{"type":"text","text":"keep"},{"type":"tool_result","tool_use_id":"tool_a","content":"new"}]}
		]
	}`)

	out := DedupeToolOutputs(body)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 1 {
		t.Fatalf("messages length = %d, want 1; payload=%s", len(messages), out)
	}
	content := messages[0].Get("content").Array()
	if len(content) != 2 {
		t.Fatalf("content length = %d, want 2; payload=%s", len(content), out)
	}
	if got := content[1].Get("content").String(); got != "new" {
		t.Fatalf("tool_result content = %q, want new", got)
	}
}

func TestDedupeRequestToolOutputsCleansPayloadAndOriginalRequest(t *testing.T) {
	req := Request{Payload: []byte(`{"input":[{"type":"function_call_output","call_id":"call_a","output":"old"},{"type":"function_call_output","call_id":"call_a","output":"new"}]}`)}
	opts := Options{OriginalRequest: []byte(`{"messages":[{"role":"tool","tool_call_id":"call_b","content":"old"},{"role":"tool","tool_call_id":"call_b","content":"new"}]}`)}

	req, opts = DedupeRequestToolOutputs(req, opts)
	if got := len(gjson.GetBytes(req.Payload, "input").Array()); got != 1 {
		t.Fatalf("request input length = %d, want 1; payload=%s", got, req.Payload)
	}
	if got := len(gjson.GetBytes(opts.OriginalRequest, "messages").Array()); got != 1 {
		t.Fatalf("original messages length = %d, want 1; payload=%s", got, opts.OriginalRequest)
	}
}
