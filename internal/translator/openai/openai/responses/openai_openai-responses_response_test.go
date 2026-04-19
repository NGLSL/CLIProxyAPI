package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func parseOpenAIResponsesSSEEvent(t *testing.T, chunk []byte) (string, gjson.Result) {
	t.Helper()

	lines := strings.Split(string(chunk), "\n")
	if len(lines) < 2 {
		t.Fatalf("unexpected SSE chunk: %q", chunk)
	}

	event := strings.TrimSpace(strings.TrimPrefix(lines[0], "event:"))
	dataLine := strings.TrimSpace(strings.TrimPrefix(lines[1], "data:"))
	if !gjson.Valid(dataLine) {
		t.Fatalf("invalid SSE data JSON: %q", dataLine)
	}
	return event, gjson.Parse(dataLine)
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_ResponseCompletedWaitsForDone(t *testing.T) {
	t.Parallel()

	request := []byte(`{"model":"gpt-5.4","tool_choice":"auto","parallel_tool_calls":true}`)

	tests := []struct {
		name                  string
		in                    []string
		doneInputIndex        int // Index in tt.in where the terminal [DONE] chunk arrives and response.completed must be emitted.
		hasUsage              bool
		inputTokens           int64
		outputTokens          int64
		totalTokens           int64
		wantMessageText       string
		wantFunctionCallID    string
		wantFunctionCallName  string
		wantFunctionArguments string
	}{
		{
			// A provider may send finish_reason first and only attach usage in a later chunk (e.g. Vertex AI),
			// so response.completed must wait for [DONE] to include that usage.
			name: "late usage after finish reason",
			in: []string{
				`data: {"id":"resp_late_usage","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_late_usage","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`,
				`data: {"id":"resp_late_usage","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"filePath\":\"C:\\\\repo\\\\README.md\"}"}}]},"finish_reason":"tool_calls"}]}`,
				`data: {"id":"resp_late_usage","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`,
				`data: [DONE]`,
			},
			doneInputIndex: 3,
			hasUsage:       true,
			inputTokens:    11,
			outputTokens:   7,
			totalTokens:    18,
		},
		{
			// When usage arrives on the same chunk as finish_reason, we still expect a
			// single response.completed event and it should remain deferred until [DONE].
			name: "usage on finish reason chunk",
			in: []string{
				`data: {"id":"resp_usage_same_chunk","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_usage_same_chunk","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`,
				`data: {"id":"resp_usage_same_chunk","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"filePath\":\"C:\\\\repo\\\\README.md\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":13,"completion_tokens":5,"total_tokens":18}}`,
				`data: [DONE]`,
			},
			doneInputIndex: 2,
			hasUsage:       true,
			inputTokens:    13,
			outputTokens:   5,
			totalTokens:    18,
		},
		{
			// An OpenAI-compatible streams from a buggy server might never send usage, so response.completed should
			// still wait for [DONE] but omit the usage object entirely.
			name: "no usage chunk",
			in: []string{
				`data: {"id":"resp_no_usage","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_no_usage","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`,
				`data: {"id":"resp_no_usage","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"filePath\":\"C:\\\\repo\\\\README.md\"}"}}]},"finish_reason":"tool_calls"}]}`,
				`data: [DONE]`,
			},
			doneInputIndex: 2,
			hasUsage:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			completedCount := 0
			completedInputIndex := -1
			var completedData gjson.Result

			// Reuse converter state across input lines to simulate one streaming response.
			var param any

			for i, line := range tt.in {
				// One upstream chunk can emit multiple downstream SSE events.
				for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(line), &param) {
					event, data := parseOpenAIResponsesSSEEvent(t, chunk)
					if event != "response.completed" {
						continue
					}

					completedCount++
					completedInputIndex = i
					completedData = data
					if i < tt.doneInputIndex {
						t.Fatalf("unexpected early response.completed on input index %d", i)
					}
				}
			}

			if completedCount != 1 {
				t.Fatalf("expected exactly 1 response.completed event, got %d", completedCount)
			}
			if completedInputIndex != tt.doneInputIndex {
				t.Fatalf("expected response.completed on terminal [DONE] chunk at input index %d, got %d", tt.doneInputIndex, completedInputIndex)
			}

			// Missing upstream usage should stay omitted in the final completed event.
			if !tt.hasUsage {
				if completedData.Get("response.usage").Exists() {
					t.Fatalf("expected response.completed to omit usage when none was provided, got %s", completedData.Get("response.usage").Raw)
				}
				return
			}

			// When usage is present, the final response.completed event must preserve the usage values.
			if got := completedData.Get("response.usage.input_tokens").Int(); got != tt.inputTokens {
				t.Fatalf("unexpected response.usage.input_tokens: got %d want %d", got, tt.inputTokens)
			}
			if got := completedData.Get("response.usage.output_tokens").Int(); got != tt.outputTokens {
				t.Fatalf("unexpected response.usage.output_tokens: got %d want %d", got, tt.outputTokens)
			}
			if got := completedData.Get("response.usage.total_tokens").Int(); got != tt.totalTokens {
				t.Fatalf("unexpected response.usage.total_tokens: got %d want %d", got, tt.totalTokens)
			}
		})
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_MessageWithoutFinishReasonCompletesOnDone(t *testing.T) {
	t.Parallel()

	request := []byte(`{"model":"gpt-5.4","tool_choice":"auto","parallel_tool_calls":true}`)
	in := []string{
		`data: {"id":"resp_msg_no_finish","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":"hello world","reasoning_content":null,"tool_calls":null},"finish_reason":null}]}`,
		`data: [DONE]`,
	}

	var param any
	completedInputIndex := -1
	var completedData gjson.Result
	for i, line := range in {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			if event != "response.completed" {
				continue
			}
			completedInputIndex = i
			completedData = data
		}
	}

	if completedInputIndex != 1 {
		t.Fatalf("expected response.completed on terminal [DONE] chunk at input index 1, got %d", completedInputIndex)
	}
	if got := completedData.Get("response.output.0.type").String(); got != "message" {
		t.Fatalf("unexpected response.output.0.type: got %q want %q", got, "message")
	}
	if got := completedData.Get("response.output.0.content.0.text").String(); got != "hello world" {
		t.Fatalf("unexpected response.output.0.content.0.text: got %q want %q", got, "hello world")
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_FunctionCallWithoutFinishReasonCompletesOnDone(t *testing.T) {
	t.Parallel()

	request := []byte(`{"model":"gpt-5.4","tool_choice":"auto","parallel_tool_calls":true}`)
	in := []string{
		`data: {"id":"resp_func_no_finish","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_func_no_finish","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_func_no_finish","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"filePath\":\"C:\\\\repo\\\\README.md\"}"}}]},"finish_reason":null}]}`,
		`data: [DONE]`,
	}

	var param any
	completedInputIndex := -1
	var completedData gjson.Result
	for i, line := range in {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			if event != "response.completed" {
				continue
			}
			completedInputIndex = i
			completedData = data
		}
	}

	if completedInputIndex != 2 {
		t.Fatalf("expected response.completed on terminal [DONE] chunk at input index 2, got %d", completedInputIndex)
	}
	if got := completedData.Get("response.output.0.type").String(); got != "function_call" {
		t.Fatalf("unexpected response.output.0.type: got %q want %q", got, "function_call")
	}
	if got := completedData.Get("response.output.0.call_id").String(); got != "call_func_no_finish" {
		t.Fatalf("unexpected response.output.0.call_id: got %q want %q", got, "call_func_no_finish")
	}
	if got := completedData.Get("response.output.0.name").String(); got != "read" {
		t.Fatalf("unexpected response.output.0.name: got %q want %q", got, "read")
	}
	args := completedData.Get("response.output.0.arguments").String()
	if !gjson.Valid(args) {
		t.Fatalf("expected response.output.0.arguments to be valid JSON, got %q", args)
	}
	if got := gjson.Get(args, "filePath").String(); got != `C:\repo\README.md` {
		t.Fatalf("unexpected response.output.0.arguments.filePath: got %q want %q", got, `C:\repo\README.md`)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_MultipleToolCallsRemainSeparate(t *testing.T) {
	in := []string{
		`data: {"id":"resp_test","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_read","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_test","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"filePath\":\"C:\\\\repo\",\"limit\":400,\"offset\":1}"}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_test","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":1,"id":"call_glob","type":"function","function":{"name":"glob","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_test","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":1,"function":{"arguments":"{\"path\":\"C:\\\\repo\",\"pattern\":\"*.{yml,yaml}\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_test","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":"tool_calls"}],"usage":{"completion_tokens":10,"total_tokens":20,"prompt_tokens":10}}`,
		`data: [DONE]`,
	}

	request := []byte(`{"model":"gpt-5.4","tool_choice":"auto","parallel_tool_calls":true}`)

	var param any
	var out [][]byte
	for _, line := range in {
		out = append(out, ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(line), &param)...)
	}

	addedNames := map[string]string{}
	doneArgs := map[string]string{}
	doneNames := map[string]string{}
	outputItems := map[string]gjson.Result{}

	for _, chunk := range out {
		ev, data := parseOpenAIResponsesSSEEvent(t, chunk)
		switch ev {
		case "response.output_item.added":
			if data.Get("item.type").String() != "function_call" {
				continue
			}
			addedNames[data.Get("item.call_id").String()] = data.Get("item.name").String()
		case "response.output_item.done":
			if data.Get("item.type").String() != "function_call" {
				continue
			}
			callID := data.Get("item.call_id").String()
			doneArgs[callID] = data.Get("item.arguments").String()
			doneNames[callID] = data.Get("item.name").String()
		case "response.completed":
			output := data.Get("response.output")
			for _, item := range output.Array() {
				if item.Get("type").String() == "function_call" {
					outputItems[item.Get("call_id").String()] = item
				}
			}
		}
	}

	if len(addedNames) != 2 {
		t.Fatalf("expected 2 function_call added events, got %d", len(addedNames))
	}
	if len(doneArgs) != 2 {
		t.Fatalf("expected 2 function_call done events, got %d", len(doneArgs))
	}

	if addedNames["call_read"] != "read" {
		t.Fatalf("unexpected added name for call_read: %q", addedNames["call_read"])
	}
	if addedNames["call_glob"] != "glob" {
		t.Fatalf("unexpected added name for call_glob: %q", addedNames["call_glob"])
	}

	if !gjson.Valid(doneArgs["call_read"]) {
		t.Fatalf("invalid JSON args for call_read: %q", doneArgs["call_read"])
	}
	if !gjson.Valid(doneArgs["call_glob"]) {
		t.Fatalf("invalid JSON args for call_glob: %q", doneArgs["call_glob"])
	}
	if strings.Contains(doneArgs["call_read"], "}{") {
		t.Fatalf("call_read args were concatenated: %q", doneArgs["call_read"])
	}
	if strings.Contains(doneArgs["call_glob"], "}{") {
		t.Fatalf("call_glob args were concatenated: %q", doneArgs["call_glob"])
	}

	if doneNames["call_read"] != "read" {
		t.Fatalf("unexpected done name for call_read: %q", doneNames["call_read"])
	}
	if doneNames["call_glob"] != "glob" {
		t.Fatalf("unexpected done name for call_glob: %q", doneNames["call_glob"])
	}

	if got := gjson.Get(doneArgs["call_read"], "filePath").String(); got != `C:\repo` {
		t.Fatalf("unexpected filePath for call_read: %q", got)
	}
	if got := gjson.Get(doneArgs["call_glob"], "path").String(); got != `C:\repo` {
		t.Fatalf("unexpected path for call_glob: %q", got)
	}
	if got := gjson.Get(doneArgs["call_glob"], "pattern").String(); got != "*.{yml,yaml}" {
		t.Fatalf("unexpected pattern for call_glob: %q", got)
	}

	if len(outputItems) != 2 {
		t.Fatalf("expected 2 function_call items in response.output, got %d", len(outputItems))
	}
	if outputItems["call_read"].Get("name").String() != "read" {
		t.Fatalf("unexpected response.output name for call_read: %q", outputItems["call_read"].Get("name").String())
	}
	if outputItems["call_glob"].Get("name").String() != "glob" {
		t.Fatalf("unexpected response.output name for call_glob: %q", outputItems["call_glob"].Get("name").String())
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_MultiChoiceToolCallsUseDistinctOutputIndexes(t *testing.T) {
	in := []string{
		`data: {"id":"resp_multi_choice","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_choice0","type":"function","function":{"name":"glob","arguments":""}}]},"finish_reason":null},{"index":1,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_choice1","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_multi_choice","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"C:\\\\repo\",\"pattern\":\"*.go\"}"}}]},"finish_reason":null},{"index":1,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"filePath\":\"C:\\\\repo\\\\README.md\",\"limit\":20,\"offset\":1}"}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_multi_choice","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":"tool_calls"},{"index":1,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":"tool_calls"}],"usage":{"completion_tokens":10,"total_tokens":20,"prompt_tokens":10}}`,
		`data: [DONE]`,
	}

	request := []byte(`{"model":"gpt-5.4","tool_choice":"auto","parallel_tool_calls":true}`)

	var param any
	var out [][]byte
	for _, line := range in {
		out = append(out, ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(line), &param)...)
	}

	type fcEvent struct {
		outputIndex int64
		name        string
		arguments   string
	}

	added := map[string]fcEvent{}
	done := map[string]fcEvent{}

	for _, chunk := range out {
		ev, data := parseOpenAIResponsesSSEEvent(t, chunk)
		switch ev {
		case "response.output_item.added":
			if data.Get("item.type").String() != "function_call" {
				continue
			}
			callID := data.Get("item.call_id").String()
			added[callID] = fcEvent{
				outputIndex: data.Get("output_index").Int(),
				name:        data.Get("item.name").String(),
			}
		case "response.output_item.done":
			if data.Get("item.type").String() != "function_call" {
				continue
			}
			callID := data.Get("item.call_id").String()
			done[callID] = fcEvent{
				outputIndex: data.Get("output_index").Int(),
				name:        data.Get("item.name").String(),
				arguments:   data.Get("item.arguments").String(),
			}
		}
	}

	if len(added) != 2 {
		t.Fatalf("expected 2 function_call added events, got %d", len(added))
	}
	if len(done) != 2 {
		t.Fatalf("expected 2 function_call done events, got %d", len(done))
	}

	if added["call_choice0"].name != "glob" {
		t.Fatalf("unexpected added name for call_choice0: %q", added["call_choice0"].name)
	}
	if added["call_choice1"].name != "read" {
		t.Fatalf("unexpected added name for call_choice1: %q", added["call_choice1"].name)
	}
	if added["call_choice0"].outputIndex == added["call_choice1"].outputIndex {
		t.Fatalf("expected distinct output indexes for different choices, both got %d", added["call_choice0"].outputIndex)
	}

	if !gjson.Valid(done["call_choice0"].arguments) {
		t.Fatalf("invalid JSON args for call_choice0: %q", done["call_choice0"].arguments)
	}
	if !gjson.Valid(done["call_choice1"].arguments) {
		t.Fatalf("invalid JSON args for call_choice1: %q", done["call_choice1"].arguments)
	}
	if done["call_choice0"].outputIndex == done["call_choice1"].outputIndex {
		t.Fatalf("expected distinct done output indexes for different choices, both got %d", done["call_choice0"].outputIndex)
	}
	if done["call_choice0"].name != "glob" {
		t.Fatalf("unexpected done name for call_choice0: %q", done["call_choice0"].name)
	}
	if done["call_choice1"].name != "read" {
		t.Fatalf("unexpected done name for call_choice1: %q", done["call_choice1"].name)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_MixedMessageAndToolUseDistinctOutputIndexes(t *testing.T) {
	in := []string{
		`data: {"id":"resp_mixed","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":"hello","reasoning_content":null,"tool_calls":null},"finish_reason":null},{"index":1,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_choice1","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_mixed","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":"stop"},{"index":1,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"filePath\":\"C:\\\\repo\\\\README.md\",\"limit\":20,\"offset\":1}"}}]},"finish_reason":"tool_calls"}],"usage":{"completion_tokens":10,"total_tokens":20,"prompt_tokens":10}}`,
		`data: [DONE]`,
	}

	request := []byte(`{"model":"gpt-5.4","tool_choice":"auto","parallel_tool_calls":true}`)

	var param any
	var out [][]byte
	for _, line := range in {
		out = append(out, ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(line), &param)...)
	}

	var messageOutputIndex int64 = -1
	var toolOutputIndex int64 = -1

	for _, chunk := range out {
		ev, data := parseOpenAIResponsesSSEEvent(t, chunk)
		if ev != "response.output_item.added" {
			continue
		}
		switch data.Get("item.type").String() {
		case "message":
			if data.Get("item.id").String() == "msg_resp_mixed_0" {
				messageOutputIndex = data.Get("output_index").Int()
			}
		case "function_call":
			if data.Get("item.call_id").String() == "call_choice1" {
				toolOutputIndex = data.Get("output_index").Int()
			}
		}
	}

	if messageOutputIndex < 0 {
		t.Fatal("did not find message output index")
	}
	if toolOutputIndex < 0 {
		t.Fatal("did not find tool output index")
	}
	if messageOutputIndex == toolOutputIndex {
		t.Fatalf("expected distinct output indexes for message and tool call, both got %d", messageOutputIndex)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_FunctionCallDoneAndCompletedOutputStayAscending(t *testing.T) {
	in := []string{
		`data: {"id":"resp_order","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_glob","type":"function","function":{"name":"glob","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_order","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"C:\\\\repo\",\"pattern\":\"*.go\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_order","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":1,"id":"call_read","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_order","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":1,"function":{"arguments":"{\"filePath\":\"C:\\\\repo\\\\README.md\",\"limit\":20,\"offset\":1}"}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_order","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":"tool_calls"}],"usage":{"completion_tokens":10,"total_tokens":20,"prompt_tokens":10}}`,
		`data: [DONE]`,
	}

	request := []byte(`{"model":"gpt-5.4","tool_choice":"auto","parallel_tool_calls":true}`)

	var param any
	var out [][]byte
	for _, line := range in {
		out = append(out, ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(line), &param)...)
	}

	var doneIndexes []int64
	var completedOrder []string

	for _, chunk := range out {
		ev, data := parseOpenAIResponsesSSEEvent(t, chunk)
		switch ev {
		case "response.output_item.done":
			if data.Get("item.type").String() == "function_call" {
				doneIndexes = append(doneIndexes, data.Get("output_index").Int())
			}
		case "response.completed":
			for _, item := range data.Get("response.output").Array() {
				if item.Get("type").String() == "function_call" {
					completedOrder = append(completedOrder, item.Get("call_id").String())
				}
			}
		}
	}

	if len(doneIndexes) != 2 {
		t.Fatalf("expected 2 function_call done indexes, got %d", len(doneIndexes))
	}
	if doneIndexes[0] >= doneIndexes[1] {
		t.Fatalf("expected ascending done output indexes, got %v", doneIndexes)
	}
	if len(completedOrder) != 2 {
		t.Fatalf("expected 2 function_call items in completed output, got %d", len(completedOrder))
	}
	if completedOrder[0] != "call_glob" || completedOrder[1] != "call_read" {
		t.Fatalf("unexpected completed function_call order: %v", completedOrder)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream_PreservesResponsesRequestFields(t *testing.T) {
	originalRequest := []byte(`{
		"model":"ignored-model",
		"instructions":"system prompt",
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
		"text":{"format":{"type":"json_schema","json_schema":{"name":"demo","schema":{"type":"object"}}}},
		"reasoning":{"effort":"high","summary":"auto"},
		"tool_choice":{"type":"function","function":{"name":"lookup"}},
		"tools":[{"type":"function","name":"lookup","description":"Find records","parameters":{"type":"object"}}]
	}`)
	request := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-4.1", originalRequest, false)
	response := []byte(`{
		"id":"chatcmpl-123",
		"object":"chat.completion",
		"created":1773896263,
		"model":"gpt-4.1",
		"choices":[{
			"index":0,
			"message":{"role":"assistant","content":"hello"},
			"finish_reason":"stop"
		}],
		"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}
	}`)

	out := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "gpt-4.1", originalRequest, request, response, nil)

	if got := gjson.GetBytes(out, "instructions").String(); got != "system prompt" {
		t.Fatalf("instructions = %q, want %q", got, "system prompt")
	}
	if got := gjson.GetBytes(out, "max_output_tokens").Int(); got != 128 {
		t.Fatalf("max_output_tokens = %d, want %d", got, 128)
	}
	if got := gjson.GetBytes(out, "parallel_tool_calls").Bool(); !got {
		t.Fatal("parallel_tool_calls = false, want true")
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
	if got := gjson.GetBytes(out, "text.format.type").String(); got != "json_schema" {
		t.Fatalf("text.format.type = %q, want %q", got, "json_schema")
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "high" {
		t.Fatalf("reasoning.effort = %q, want %q", got, "high")
	}
	if got := gjson.GetBytes(out, "reasoning.summary").String(); got != "auto" {
		t.Fatalf("reasoning.summary = %q, want %q", got, "auto")
	}
	if got := gjson.GetBytes(out, "tool_choice.function.name").String(); got != "lookup" {
		t.Fatalf("tool_choice.function.name = %q, want %q", got, "lookup")
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "lookup" {
		t.Fatalf("tools.0.name = %q, want %q", got, "lookup")
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream_ReasoningItemUsesContentOnly(t *testing.T) {
	originalRequest := []byte(`{"model":"gpt-4.1","reasoning":{"effort":"high","summary":"auto"}}`)
	request := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-4.1", originalRequest, false)
	response := []byte(`{
		"id":"chatcmpl-reasoning",
		"object":"chat.completion",
		"created":1773896263,
		"model":"gpt-4.1",
		"choices":[{
			"index":0,
			"message":{"role":"assistant","content":"hello","reasoning_content":"step-1"},
			"finish_reason":"stop"
		}],
		"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}
	}`)

	out := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "gpt-4.1", originalRequest, request, response, nil)

	if got := gjson.GetBytes(out, "output.0.type").String(); got != "reasoning" {
		t.Fatalf("output.0.type = %q, want %q", got, "reasoning")
	}
	if got := gjson.GetBytes(out, "output.0.content.0.text").String(); got != "step-1" {
		t.Fatalf("output.0.content.0.text = %q, want %q", got, "step-1")
	}
	if got := gjson.GetBytes(out, "output.0.summary.#").Int(); got != 0 {
		t.Fatalf("output.0.summary length = %d, want 0", got)
	}
	if got := gjson.GetBytes(out, "output.1.type").String(); got != "message" {
		t.Fatalf("output.1.type = %q, want %q", got, "message")
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_EmitsReasoningTextDelta(t *testing.T) {
	in := []string{
		`data: {"id":"resp_reasoning","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":"step-1","tool_calls":null},"finish_reason":null}]}`,
		`data: [DONE]`,
	}
	request := []byte(`{"model":"gpt-5.4","reasoning":{"effort":"high","summary":"auto"}}`)

	var param any
	var sawDelta bool
	var sawSummaryDelta bool
	var completed gjson.Result
	for _, line := range in {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			switch event {
			case "response.reasoning_text.delta":
				sawDelta = true
				if got := data.Get("delta").String(); got != "step-1" {
					t.Fatalf("delta = %q, want %q", got, "step-1")
				}
				if got := data.Get("content_index").Int(); got != 0 {
					t.Fatalf("content_index = %d, want 0", got)
				}
			case "response.reasoning_summary_text.delta":
				sawSummaryDelta = true
			case "response.completed":
				completed = data.Get("response")
			}
		}
	}
	if !sawDelta {
		t.Fatal("expected response.reasoning_text.delta event")
	}
	if sawSummaryDelta {
		t.Fatal("did not expect response.reasoning_summary_text.delta event")
	}
	if !completed.Exists() {
		t.Fatal("expected response.completed event")
	}
	if got := completed.Get("output.0.type").String(); got != "reasoning" {
		t.Fatalf("output.0.type = %q, want %q", got, "reasoning")
	}
	if got := completed.Get("output.0.content.0.text").String(); got != "step-1" {
		t.Fatalf("output.0.content.0.text = %q, want %q", got, "step-1")
	}
	if got := completed.Get("output.0.summary.#").Int(); got != 0 {
		t.Fatalf("output.0.summary length = %d, want 0", got)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_EmitsCustomToolCallInputDelta(t *testing.T) {
	request := []byte(`{"model":"gpt-5.4","tools":[{"type":"custom","name":"apply_patch"}]}`)
	in := []string{
		`data: {"id":"resp_custom","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":[{"index":0,"id":"call_patch","type":"function","function":{"name":"apply_patch","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"resp_custom","object":"chat.completion.chunk","created":1773896263,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":[{"index":0,"function":{"arguments":"*** Begin Patch"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}

	var param any
	var sawAdded bool
	var sawDelta bool
	for _, line := range in {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			switch event {
			case "response.output_item.added":
				if data.Get("item.type").String() == "custom_tool_call" {
					sawAdded = true
				}
			case "response.custom_tool_call_input.delta":
				sawDelta = true
				if got := data.Get("call_id").String(); got != "call_patch" {
					t.Fatalf("call_id = %q, want %q", got, "call_patch")
				}
				if got := data.Get("delta").String(); got != "*** Begin Patch" {
					t.Fatalf("delta = %q, want %q", got, "*** Begin Patch")
				}
			}
		}
	}
	if !sawAdded {
		t.Fatal("expected custom_tool_call output item")
	}
	if !sawDelta {
		t.Fatal("expected response.custom_tool_call_input.delta event")
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_ResponseCompletedPreservesTranslatedResponsesRequestFields(t *testing.T) {
	originalRequest := []byte(`{
		"model":"ignored-model",
		"instructions":"system prompt",
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
		"text":{"format":{"type":"json_schema","json_schema":{"name":"demo","schema":{"type":"object"}}}},
		"reasoning":{"effort":"high","summary":"auto"},
		"tool_choice":{"type":"function","function":{"name":"lookup"}},
		"tools":[{"type":"function","name":"lookup","description":"Find records","parameters":{"type":"object"}}]
	}`)
	request := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-4.1", originalRequest, true)
	in := []string{
		`data: {"id":"resp_fields","object":"chat.completion.chunk","created":1773896263,"model":"gpt-4.1","choices":[{"index":0,"delta":{"role":"assistant","content":"hello","reasoning_content":null,"tool_calls":null},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`,
		`data: [DONE]`,
	}

	var param any
	var completed gjson.Result
	for _, line := range in {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "gpt-4.1", originalRequest, request, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			if event == "response.completed" {
				completed = data.Get("response")
			}
		}
	}

	if !completed.Exists() {
		t.Fatal("expected response.completed event")
	}
	if got := completed.Get("instructions").String(); got != "system prompt" {
		t.Fatalf("instructions = %q, want %q", got, "system prompt")
	}
	if got := completed.Get("max_output_tokens").Int(); got != 128 {
		t.Fatalf("max_output_tokens = %d, want %d", got, 128)
	}
	if got := completed.Get("parallel_tool_calls").Bool(); !got {
		t.Fatal("parallel_tool_calls = false, want true")
	}
	if got := completed.Get("metadata.nested.value").Int(); got != 1 {
		t.Fatalf("metadata.nested.value = %d, want %d", got, 1)
	}
	if got := completed.Get("service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want %q", got, "priority")
	}
	if got := completed.Get("store").Bool(); !got {
		t.Fatal("store = false, want true")
	}
	if got := completed.Get("temperature").Float(); got != 0.7 {
		t.Fatalf("temperature = %v, want %v", got, 0.7)
	}
	if got := completed.Get("top_p").Float(); got != 0.8 {
		t.Fatalf("top_p = %v, want %v", got, 0.8)
	}
	if got := completed.Get("top_logprobs").Int(); got != 3 {
		t.Fatalf("top_logprobs = %d, want %d", got, 3)
	}
	if got := completed.Get("prompt_cache_key").String(); got != "cache-key" {
		t.Fatalf("prompt_cache_key = %q, want %q", got, "cache-key")
	}
	if got := completed.Get("prompt_cache_retention").String(); got != "short" {
		t.Fatalf("prompt_cache_retention = %q, want %q", got, "short")
	}
	if got := completed.Get("text.format.type").String(); got != "json_schema" {
		t.Fatalf("text.format.type = %q, want %q", got, "json_schema")
	}
	if got := completed.Get("reasoning.effort").String(); got != "high" {
		t.Fatalf("reasoning.effort = %q, want %q", got, "high")
	}
	if got := completed.Get("reasoning.summary").String(); got != "auto" {
		t.Fatalf("reasoning.summary = %q, want %q", got, "auto")
	}
	if got := completed.Get("tool_choice.function.name").String(); got != "lookup" {
		t.Fatalf("tool_choice.function.name = %q, want %q", got, "lookup")
	}
	if got := completed.Get("tools.0.name").String(); got != "lookup" {
		t.Fatalf("tools.0.name = %q, want %q", got, "lookup")
	}
}
