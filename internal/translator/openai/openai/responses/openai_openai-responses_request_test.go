package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletionsPreservesCompatibleFields(t *testing.T) {
	raw := []byte(`{
		"model":"ignored-model",
		"instructions":"system prompt",
		"input":"hello",
		"max_output_tokens":128,
		"parallel_tool_calls":true,
		"metadata":{"nested":{"value":1}},
		"service_tier":"priority",
		"store":true,
		"temperature":0.7,
		"top_p":0.8,
		"top_logprobs":3,
		"prompt_cache_key":"cache-key",
		"prompt_cache_retention":"short",
		"extra_headers":{"X-Test":"header-value"},
		"extra_query":{"provider":"openrouter","tags":["a","b"]},
		"extra_body":{"seed":7},
		"text":{"format":{"type":"json_schema","json_schema":{"name":"demo","schema":{"type":"object"}}}},
		"reasoning":{"effort":"HIGH","summary":"auto"},
		"tool_choice":{"type":"function","function":{"name":"lookup"}}
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-4.1", raw, true)

	if got := gjson.GetBytes(out, "model").String(); got != "gpt-4.1" {
		t.Fatalf("model = %q, want %q", got, "gpt-4.1")
	}
	if got := gjson.GetBytes(out, "stream").Bool(); !got {
		t.Fatal("stream = false, want true")
	}
	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 128 {
		t.Fatalf("max_tokens = %d, want %d", got, 128)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "system" {
		t.Fatalf("messages.0.role = %q, want %q", got, "system")
	}
	if got := gjson.GetBytes(out, "messages.1.content").String(); got != "hello" {
		t.Fatalf("messages.1.content = %q, want %q", got, "hello")
	}
	if got := gjson.GetBytes(out, "metadata.nested.value").Int(); got != 1 {
		t.Fatalf("metadata.nested.value = %d, want %d", got, 1)
	}
	if got := gjson.GetBytes(out, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want %q", got, "priority")
	}
	if got := gjson.GetBytes(out, "store").Bool(); !got {
		t.Fatal("store = false, want true")
	}
	if got := gjson.GetBytes(out, "temperature").Float(); got != 0.7 {
		t.Fatalf("temperature = %v, want %v", got, 0.7)
	}
	if got := gjson.GetBytes(out, "top_p").Float(); got != 0.8 {
		t.Fatalf("top_p = %v, want %v", got, 0.8)
	}
	if got := gjson.GetBytes(out, "top_logprobs").Int(); got != 3 {
		t.Fatalf("top_logprobs = %d, want %d", got, 3)
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
	if got := gjson.GetBytes(out, "extra_query.tags.1").String(); got != "b" {
		t.Fatalf("extra_query.tags.1 = %q, want %q", got, "b")
	}
	if got := gjson.GetBytes(out, "extra_body.seed").Int(); got != 7 {
		t.Fatalf("extra_body.seed = %d, want %d", got, 7)
	}
	if got := gjson.GetBytes(out, "response_format.type").String(); got != "json_schema" {
		t.Fatalf("response_format.type = %q, want %q", got, "json_schema")
	}
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "high" {
		t.Fatalf("reasoning_effort = %q, want %q", got, "high")
	}
	if got := gjson.GetBytes(out, "reasoning.summary").String(); got != "auto" {
		t.Fatalf("reasoning.summary = %q, want %q", got, "auto")
	}
	if got := gjson.GetBytes(out, "tool_choice.function.name").String(); got != "lookup" {
		t.Fatalf("tool_choice.function.name = %q, want %q", got, "lookup")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletionsReplaysReasoningForToolCall(t *testing.T) {
	raw := []byte(`{
		"input":[
			{
				"type":"reasoning",
				"content":[{"type":"reasoning_text","text":"需要先查询文件"}],
				"summary":[]
			},
			{
				"type":"function_call",
				"call_id":"call_123",
				"name":"Read",
				"arguments":"{\"file_path\":\"demo.txt\"}"
			},
			{
				"type":"function_call_output",
				"call_id":"call_123",
				"output":"file contents"
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro", raw, false)

	assistantMessage := gjson.GetBytes(out, "messages.0")
	if got := assistantMessage.Get("role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want %q", got, "assistant")
	}
	if got := assistantMessage.Get("reasoning_content").String(); got != "需要先查询文件" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q", got, "需要先查询文件")
	}
	if got := assistantMessage.Get("tool_calls.0.id").String(); got != "call_123" {
		t.Fatalf("messages.0.tool_calls.0.id = %q, want %q", got, "call_123")
	}
	if got := assistantMessage.Get("tool_calls.0.function.name").String(); got != "Read" {
		t.Fatalf("messages.0.tool_calls.0.function.name = %q, want %q", got, "Read")
	}
	if got := assistantMessage.Get("tool_calls.0.function.arguments").String(); got != "{\"file_path\":\"demo.txt\"}" {
		t.Fatalf("messages.0.tool_calls.0.function.arguments = %q, want %q", got, "{\"file_path\":\"demo.txt\"}")
	}

	toolMessage := gjson.GetBytes(out, "messages.1")
	if got := toolMessage.Get("role").String(); got != "tool" {
		t.Fatalf("messages.1.role = %q, want %q", got, "tool")
	}
	if got := toolMessage.Get("tool_call_id").String(); got != "call_123" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "call_123")
	}
	if got := toolMessage.Get("content").String(); got != "file contents" {
		t.Fatalf("messages.1.content = %q, want %q", got, "file contents")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletionsDropsUnansweredToolCalls(t *testing.T) {
	raw := []byte(`{
		"input":[
			{"type":"message","role":"user","content":"next"},
			{"type":"reasoning","content":[{"type":"reasoning_text","text":"准备调用工具"}],"summary":[]},
			{"type":"function_call","call_id":"call_done","name":"Read","arguments":"{\"file_path\":\"done.txt\"}"},
			{"type":"function_call","call_id":"call_orphan","name":"Read","arguments":"{\"file_path\":\"orphan.txt\"}"},
			{"type":"function_call_output","call_id":"call_done","output":"done contents"},
			{"type":"message","role":"user","content":"continue"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro", raw, false)

	assistantMessage := gjson.GetBytes(out, "messages.1")
	if got := assistantMessage.Get("role").String(); got != "assistant" {
		t.Fatalf("messages.1.role = %q, want %q", got, "assistant")
	}
	if got := assistantMessage.Get("reasoning_content").String(); got != "准备调用工具" {
		t.Fatalf("messages.1.reasoning_content = %q, want %q", got, "准备调用工具")
	}
	if got := assistantMessage.Get("tool_calls.#").Int(); got != 1 {
		t.Fatalf("messages.1.tool_calls length = %d, want %d; out = %s", got, 1, string(out))
	}
	if got := assistantMessage.Get("tool_calls.0.id").String(); got != "call_done" {
		t.Fatalf("messages.1.tool_calls.0.id = %q, want %q", got, "call_done")
	}
	if assistantMessage.Get("tool_calls.1.id").Exists() {
		t.Fatalf("unexpected orphan tool call kept: %s", assistantMessage.Raw)
	}
	if got := gjson.GetBytes(out, "messages.2.role").String(); got != "tool" {
		t.Fatalf("messages.2.role = %q, want %q", got, "tool")
	}
	if got := gjson.GetBytes(out, "messages.2.tool_call_id").String(); got != "call_done" {
		t.Fatalf("messages.2.tool_call_id = %q, want %q", got, "call_done")
	}
	if got := gjson.GetBytes(out, "messages.3.content").String(); got != "continue" {
		t.Fatalf("messages.3.content = %q, want %q", got, "continue")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletionsMergesAssistantMessageWithToolCalls(t *testing.T) {
	raw := []byte(`{
		"input":[
			{
				"type":"reasoning",
				"content":[{"type":"reasoning_text","text":"需要先读取核心文件"}],
				"summary":[]
			},
			{
				"type":"message",
				"role":"assistant",
				"content":[{"type":"output_text","text":"接着读取核心架构文件。"}]
			},
			{
				"type":"function_call",
				"call_id":"call_1",
				"name":"Read",
				"arguments":"{\"file_path\":\"cli.tsx\"}"
			},
			{
				"type":"function_call",
				"call_id":"call_2",
				"name":"Read",
				"arguments":"{\"file_path\":\"main.tsx\"}"
			},
			{
				"type":"function_call_output",
				"call_id":"call_1",
				"output":"cli contents"
			},
			{
				"type":"function_call_output",
				"call_id":"call_2",
				"output":"main contents"
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro", raw, false)

	assistantMessage := gjson.GetBytes(out, "messages.0")
	if got := assistantMessage.Get("role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want %q", got, "assistant")
	}
	if got := assistantMessage.Get("reasoning_content").String(); got != "需要先读取核心文件" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q", got, "需要先读取核心文件")
	}
	if got := assistantMessage.Get("content.0.text").String(); got != "接着读取核心架构文件。" {
		t.Fatalf("messages.0.content.0.text = %q, want %q", got, "接着读取核心架构文件。")
	}
	if got := assistantMessage.Get("tool_calls.#").Int(); got != 2 {
		t.Fatalf("messages.0.tool_calls length = %d, want %d; out = %s", got, 2, string(out))
	}
	if got := assistantMessage.Get("tool_calls.1.id").String(); got != "call_2" {
		t.Fatalf("messages.0.tool_calls.1.id = %q, want %q", got, "call_2")
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "tool" {
		t.Fatalf("messages.1.role = %q, want %q", got, "tool")
	}
	if got := gjson.GetBytes(out, "messages.2.tool_call_id").String(); got != "call_2" {
		t.Fatalf("messages.2.tool_call_id = %q, want %q", got, "call_2")
	}
	if got := gjson.GetBytes(out, "messages.#").Int(); got != 3 {
		t.Fatalf("messages length = %d, want %d; out = %s", got, 3, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletionsReplaysReasoningForAssistantMessage(t *testing.T) {
	raw := []byte(`{
		"input":[
			{
				"type":"reasoning",
				"content":[{"type":"reasoning_text","text":"先总结上一轮结果"}],
				"summary":[]
			},
			{
				"type":"message",
				"role":"assistant",
				"content":[{"type":"output_text","text":"这是整理后的说明。"}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro", raw, false)

	assistantMessage := gjson.GetBytes(out, "messages.0")
	if got := assistantMessage.Get("role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want %q", got, "assistant")
	}
	if got := assistantMessage.Get("reasoning_content").String(); got != "先总结上一轮结果" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q", got, "先总结上一轮结果")
	}
	if got := assistantMessage.Get("content.0.text").String(); got != "这是整理后的说明。" {
		t.Fatalf("messages.0.content.0.text = %q, want %q", got, "这是整理后的说明。")
	}
	if got := gjson.GetBytes(out, "messages.#").Int(); got != 1 {
		t.Fatalf("messages length = %d, want %d; out = %s", got, 1, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletionsSkipsInterleavedAssistantMessagesBeforeToolOutputs(t *testing.T) {
	raw := []byte(`{
		"input":[
			{
				"type":"reasoning",
				"summary":[{"type":"summary_text","text":"Let me now read the key source files to understand the architecture better."}]
			},
			{
				"role":"assistant",
				"content":"接着读取核心架构文件。"
			},
			{
				"type":"function_call",
				"call_id":"call_00_gXdjsKEi0Ma9lEf1l4mStqmA",
				"name":"Read",
				"arguments":"{\"file_path\":\"D:\\\\Project\\\\my-claude-code\\\\my-claude\\\\src\\\\entrypoints\\\\cli.tsx\"}"
			},
			{
				"type":"function_call",
				"call_id":"call_01_f4SGPENHZwP2ziukEhTuXP7p",
				"name":"Read",
				"arguments":"{\"file_path\":\"D:\\\\Project\\\\my-claude-code\\\\my-claude\\\\src\\\\main.tsx\"}"
			},
			{
				"type":"function_call",
				"call_id":"call_02_841H80XW9YqdrUuamo2jEHiX",
				"name":"Read",
				"arguments":"{\"file_path\":\"D:\\\\Project\\\\my-claude-code\\\\my-claude\\\\src\\\\commands.ts\"}"
			},
			{
				"role":"assistant",
				"content":"{\"title\":\"Read project files\"}"
			},
			{
				"role":"assistant",
				"content":"{\"title\":\"我现在还不能直接读取当前工作区内容，请先发我项目目录树或关键文件\"}"
			},
			{
				"type":"function_call_output",
				"call_id":"call_00_gXdjsKEi0Ma9lEf1l4mStqmA",
				"output":"cli.tsx contents"
			},
			{
				"type":"function_call_output",
				"call_id":"call_01_f4SGPENHZwP2ziukEhTuXP7p",
				"output":"main.tsx contents"
			},
			{
				"type":"function_call_output",
				"call_id":"call_02_841H80XW9YqdrUuamo2jEHiX",
				"output":"commands.ts contents"
			},
			{
				"role":"user",
				"content":"continue"
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro", raw, false)

	assistantMessage := gjson.GetBytes(out, "messages.0")
	if got := assistantMessage.Get("role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want %q", got, "assistant")
	}
	if got := assistantMessage.Get("reasoning_content").String(); got != "Let me now read the key source files to understand the architecture better." {
		t.Fatalf("messages.0.reasoning_content = %q, want reasoning summary", got)
	}
	if got := assistantMessage.Get("content").String(); got != "接着读取核心架构文件。" {
		t.Fatalf("messages.0.content = %q, want %q", got, "接着读取核心架构文件。")
	}
	if got := assistantMessage.Get("tool_calls.#").Int(); got != 3 {
		t.Fatalf("messages.0.tool_calls length = %d, want %d; out = %s", got, 3, string(out))
	}
	if got := assistantMessage.Get("tool_calls.2.id").String(); got != "call_02_841H80XW9YqdrUuamo2jEHiX" {
		t.Fatalf("messages.0.tool_calls.2.id = %q, want %q", got, "call_02_841H80XW9YqdrUuamo2jEHiX")
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "tool" {
		t.Fatalf("messages.1.role = %q, want %q", got, "tool")
	}
	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "call_00_gXdjsKEi0Ma9lEf1l4mStqmA" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "call_00_gXdjsKEi0Ma9lEf1l4mStqmA")
	}
	if got := gjson.GetBytes(out, "messages.3.tool_call_id").String(); got != "call_02_841H80XW9YqdrUuamo2jEHiX" {
		t.Fatalf("messages.3.tool_call_id = %q, want %q", got, "call_02_841H80XW9YqdrUuamo2jEHiX")
	}
	if got := gjson.GetBytes(out, "messages.4.role").String(); got != "user" {
		t.Fatalf("messages.4.role = %q, want %q", got, "user")
	}
	if got := gjson.GetBytes(out, "messages.4.content").String(); got != "continue" {
		t.Fatalf("messages.4.content = %q, want %q", got, "continue")
	}
	if got := gjson.GetBytes(out, "messages.#").Int(); got != 5 {
		t.Fatalf("messages length = %d, want %d; out = %s", got, 5, string(out))
	}
}
