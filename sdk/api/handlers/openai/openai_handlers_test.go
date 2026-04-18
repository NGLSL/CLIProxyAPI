package openai

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCompletionsRequestToChatCompletionsPreservesCompatibleFields(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-4.1",
		"prompt":"hello",
		"max_tokens":64,
		"metadata":{"tags":["a","b"]},
		"service_tier":"priority",
		"store":true,
		"seed":7,
		"parallel_tool_calls":true,
		"response_format":{"type":"json_schema","json_schema":{"name":"demo"}},
		"modalities":["text","audio"],
		"audio":{"voice":"alloy","format":"wav"},
		"prediction":{"type":"content","content":"preview"},
		"prompt_cache_key":"cache-key",
		"prompt_cache_retention":"short",
		"extra_headers":{"X-Test":"header-value"},
		"extra_query":{"provider":"openrouter"},
		"extra_body":{"user":"abc"}
	}`)

	out := convertCompletionsRequestToChatCompletions(raw)

	if got := gjson.GetBytes(out, "model").String(); got != "gpt-4.1" {
		t.Fatalf("model = %q, want %q", got, "gpt-4.1")
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "user" {
		t.Fatalf("messages.0.role = %q, want %q", got, "user")
	}
	if got := gjson.GetBytes(out, "messages.0.content").String(); got != "hello" {
		t.Fatalf("messages.0.content = %q, want %q", got, "hello")
	}
	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 64 {
		t.Fatalf("max_tokens = %d, want %d", got, 64)
	}
	if got := gjson.GetBytes(out, "metadata.tags.1").String(); got != "b" {
		t.Fatalf("metadata.tags.1 = %q, want %q", got, "b")
	}
	if got := gjson.GetBytes(out, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want %q", got, "priority")
	}
	if got := gjson.GetBytes(out, "store").Bool(); !got {
		t.Fatal("store = false, want true")
	}
	if got := gjson.GetBytes(out, "seed").Int(); got != 7 {
		t.Fatalf("seed = %d, want %d", got, 7)
	}
	if got := gjson.GetBytes(out, "parallel_tool_calls").Bool(); !got {
		t.Fatal("parallel_tool_calls = false, want true")
	}
	if got := gjson.GetBytes(out, "response_format.type").String(); got != "json_schema" {
		t.Fatalf("response_format.type = %q, want %q", got, "json_schema")
	}
	if got := gjson.GetBytes(out, "modalities.1").String(); got != "audio" {
		t.Fatalf("modalities.1 = %q, want %q", got, "audio")
	}
	if got := gjson.GetBytes(out, "audio.voice").String(); got != "alloy" {
		t.Fatalf("audio.voice = %q, want %q", got, "alloy")
	}
	if got := gjson.GetBytes(out, "prediction.content").String(); got != "preview" {
		t.Fatalf("prediction.content = %q, want %q", got, "preview")
	}
	if got := gjson.GetBytes(out, "prompt_cache_key").String(); got != "cache-key" {
		t.Fatalf("prompt_cache_key = %q, want %q", got, "cache-key")
	}
	if got := gjson.GetBytes(out, "prompt_cache_retention").String(); got != "short" {
		t.Fatalf("prompt_cache_retention = %q, want %q", got, "short")
	}
	if got := gjson.GetBytes(out, "extra_headers.X-Test").String(); got != "header-value" {
		t.Fatalf("extra_headers.X-Test = %q, want %q", got, "header-value")
	}
	if got := gjson.GetBytes(out, "extra_query.provider").String(); got != "openrouter" {
		t.Fatalf("extra_query.provider = %q, want %q", got, "openrouter")
	}
	if got := gjson.GetBytes(out, "extra_body.user").String(); got != "abc" {
		t.Fatalf("extra_body.user = %q, want %q", got, "abc")
	}
}
