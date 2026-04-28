package responses

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func setRawJSONField(dst []byte, root gjson.Result, srcPath, dstPath string) []byte {
	value := root.Get(srcPath)
	if !value.Exists() {
		return dst
	}
	updated, err := sjson.SetRawBytes(dst, dstPath, []byte(value.Raw))
	if err != nil {
		return dst
	}
	return updated
}

func copyRawJSONFields(dst []byte, root gjson.Result, fields ...string) []byte {
	for _, field := range fields {
		dst = setRawJSONField(dst, root, field, field)
	}
	return dst
}

func extractResponsesReasoningText(item gjson.Result) string {
	var parts []string
	for _, path := range []string{"content", "summary"} {
		value := item.Get(path)
		if !value.Exists() || !value.IsArray() {
			continue
		}
		value.ForEach(func(_, part gjson.Result) bool {
			text := strings.TrimSpace(part.Get("text").String())
			if text != "" {
				parts = append(parts, text)
			}
			return true
		})
		if len(parts) > 0 {
			break
		}
	}
	return strings.Join(parts, "\n")
}

func responsesInputItemType(item gjson.Result) string {
	itemType := item.Get("type").String()
	if itemType == "" && item.Get("role").String() != "" {
		itemType = "message"
	}
	return itemType
}

func responsesCallID(item gjson.Result) string {
	return strings.TrimSpace(item.Get("call_id").String())
}

func buildResponsesToolCall(item gjson.Result) []byte {
	toolCall := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
	if callID := responsesCallID(item); callID != "" {
		toolCall, _ = sjson.SetBytes(toolCall, "id", callID)
	}
	if name := item.Get("name"); name.Exists() {
		toolCall, _ = sjson.SetBytes(toolCall, "function.name", name.String())
	}
	if arguments := item.Get("arguments"); arguments.Exists() {
		toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", arguments.String())
	}
	return toolCall
}

func buildResponsesToolMessage(item gjson.Result) []byte {
	toolMessage := []byte(`{"role":"tool","tool_call_id":"","content":""}`)
	if callID := responsesCallID(item); callID != "" {
		toolMessage, _ = sjson.SetBytes(toolMessage, "tool_call_id", callID)
	}
	if output := item.Get("output"); output.Exists() {
		toolMessage, _ = sjson.SetBytes(toolMessage, "content", output.String())
	}
	return toolMessage
}

func isResponsesAssistantMessage(item gjson.Result) bool {
	return responsesInputItemType(item) == "message" && item.Get("role").String() == "assistant"
}

func collectResponsesToolOutputs(items []gjson.Result, start int) ([]gjson.Result, int) {
	var outputs []gjson.Result
	i := start
	for i < len(items) {
		item := items[i]
		itemType := responsesInputItemType(item)
		switch itemType {
		case "function_call_output":
			outputs = append(outputs, item)
			i++
		case "message":
			if isResponsesAssistantMessage(item) && len(outputs) == 0 {
				// My Claude 的 /responses 历史里，工具调用后可能先写入一两条
				// assistant commentary / title / 兜底文本，再补 function_call_output。
				// Chat Completions 不允许 tool_calls 与 tool message 之间夹普通
				// assistant message，所以只有后面确实找到 tool output 时，才会
				// 吞掉这些中间态消息并把 call/output 合并成一组合法历史。
				i++
				continue
			}
			if len(outputs) == 0 {
				return outputs, start
			}
			return outputs, i
		default:
			if len(outputs) == 0 {
				return outputs, start
			}
			return outputs, i
		}
	}
	if len(outputs) == 0 {
		return outputs, start
	}
	return outputs, i
}

func setResponsesReasoningContent(message []byte, reasoningContent string) []byte {
	if reasoningContent == "" {
		return message
	}
	updated, err := sjson.SetBytes(message, "reasoning_content", reasoningContent)
	if err != nil {
		return message
	}
	return updated
}

func appendResponsesAssistantMessage(out []byte, message []byte, reasoningContent string) []byte {
	if len(message) == 0 {
		return out
	}
	message = setResponsesReasoningContent(message, reasoningContent)
	out, _ = sjson.SetRawBytes(out, "messages.-1", message)
	return out
}

func flushPendingResponsesAssistantMessage(out []byte, pendingAssistantMessage *[]byte, pendingReasoningContent *string) []byte {
	if len(*pendingAssistantMessage) == 0 {
		*pendingReasoningContent = ""
		return out
	}
	out = appendResponsesAssistantMessage(out, *pendingAssistantMessage, *pendingReasoningContent)
	*pendingAssistantMessage = nil
	*pendingReasoningContent = ""
	return out
}

func buildResponsesChatMessage(item gjson.Result) []byte {
	role := item.Get("role").String()
	if role == "developer" {
		role = "user"
	}

	message := []byte(`{"role":"","content":[]}`)
	message, _ = sjson.SetBytes(message, "role", role)

	if content := item.Get("content"); content.Exists() && content.IsArray() {
		content.ForEach(func(_, contentItem gjson.Result) bool {
			contentType := contentItem.Get("type").String()
			if contentType == "" {
				contentType = "input_text"
			}

			switch contentType {
			case "input_text", "output_text":
				text := contentItem.Get("text").String()
				contentPart := []byte(`{"type":"text","text":""}`)
				contentPart, _ = sjson.SetBytes(contentPart, "text", text)
				message, _ = sjson.SetRawBytes(message, "content.-1", contentPart)
			case "input_image":
				imageURL := contentItem.Get("image_url").String()
				contentPart := []byte(`{"type":"image_url","image_url":{"url":""}}`)
				contentPart, _ = sjson.SetBytes(contentPart, "image_url.url", imageURL)
				message, _ = sjson.SetRawBytes(message, "content.-1", contentPart)
			}
			return true
		})
	} else if content.Type == gjson.String {
		message, _ = sjson.SetBytes(message, "content", content.String())
	}

	return message
}

func appendResponsesToolCallGroup(out []byte, assistantMessage []byte, calls, outputs []gjson.Result, reasoningContent string) ([]byte, bool) {
	outputCallIDs := make(map[string]struct{}, len(outputs))
	for _, output := range outputs {
		if callID := responsesCallID(output); callID != "" {
			outputCallIDs[callID] = struct{}{}
		}
	}

	if len(assistantMessage) == 0 {
		assistantMessage = []byte(`{"role":"assistant","tool_calls":[]}`)
	}
	pairedCallIDs := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		callID := responsesCallID(call)
		if callID == "" {
			continue
		}
		if _, ok := outputCallIDs[callID]; !ok {
			continue
		}
		assistantMessage, _ = sjson.SetRawBytes(assistantMessage, "tool_calls.-1", buildResponsesToolCall(call))
		pairedCallIDs[callID] = struct{}{}
	}
	if len(pairedCallIDs) == 0 {
		return out, false
	}
	out = appendResponsesAssistantMessage(out, assistantMessage, reasoningContent)

	for _, output := range outputs {
		callID := responsesCallID(output)
		if _, ok := pairedCallIDs[callID]; !ok {
			continue
		}
		out, _ = sjson.SetRawBytes(out, "messages.-1", buildResponsesToolMessage(output))
	}
	return out, true
}

// ConvertOpenAIResponsesRequestToOpenAIChatCompletions converts OpenAI responses format to OpenAI chat completions format.
// It transforms the OpenAI responses API format (with instructions and input array) into the standard
// OpenAI chat completions format (with messages array and system content).
//
// The conversion handles:
// 1. Model name and streaming configuration
// 2. Instructions to system message conversion
// 3. Input array to messages array transformation
// 4. Tool definitions and tool choice conversion
// 5. Function calls and function results handling
// 6. Generation parameters mapping (max_tokens, reasoning, etc.)
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data in OpenAI responses format
//   - stream: A boolean indicating if the request is for a streaming response
//
// Returns:
//   - []byte: The transformed request data in OpenAI chat completions format
func ConvertOpenAIResponsesRequestToOpenAIChatCompletions(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	// Base OpenAI chat completions template with default values
	out := []byte(`{"model":"","messages":[],"stream":false}`)

	root := gjson.ParseBytes(rawJSON)

	// Set model name
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Set stream configuration
	out, _ = sjson.SetBytes(out, "stream", stream)

	// Map generation parameters from responses format to chat completions format
	if maxTokens := root.Get("max_output_tokens"); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Int())
	}

	if parallelToolCalls := root.Get("parallel_tool_calls"); parallelToolCalls.Exists() {
		out, _ = sjson.SetBytes(out, "parallel_tool_calls", parallelToolCalls.Bool())
	}

	out = copyRawJSONFields(out, root,
		"metadata",
		"service_tier",
		"store",
		"temperature",
		"top_p",
		"top_logprobs",
		"prompt_cache_key",
		"prompt_cache_retention",
		"extra_headers",
		"extra_query",
		"extra_body",
	)
	out = setRawJSONField(out, root, "text.format", "response_format")
	out = setRawJSONField(out, root, "reasoning.summary", "reasoning.summary")

	// Convert instructions to system message
	if instructions := root.Get("instructions"); instructions.Exists() {
		systemMessage := []byte(`{"role":"system","content":""}`)
		systemMessage, _ = sjson.SetBytes(systemMessage, "content", instructions.String())
		out, _ = sjson.SetRawBytes(out, "messages.-1", systemMessage)
	}

	// Convert input array to messages
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		pendingReasoningContent := ""
		var pendingAssistantMessage []byte
		items := input.Array()
		for i := 0; i < len(items); i++ {
			item := items[i]
			itemType := responsesInputItemType(item)

			switch itemType {
			case "reasoning":
				// Responses 会把同一个 assistant turn 拆成 reasoning / message / function_call 多个 item。
				// 如果前一个 assistant message 还没落盘，先冲刷掉，避免这段 reasoning 串到后面的 turn。
				out = flushPendingResponsesAssistantMessage(out, &pendingAssistantMessage, &pendingReasoningContent)
				pendingReasoningContent = extractResponsesReasoningText(item)

			case "message", "":
				message := buildResponsesChatMessage(item)
				if item.Get("role").String() == "assistant" {
					// assistant text 先暂存；如果后面紧跟 function_call，说明这是同一个 turn，
					// 需要把 content / reasoning_content / tool_calls 合并回一条 assistant message。
					if len(pendingAssistantMessage) > 0 {
						out = flushPendingResponsesAssistantMessage(out, &pendingAssistantMessage, &pendingReasoningContent)
					}
					pendingAssistantMessage = message
					continue
				}

				out = flushPendingResponsesAssistantMessage(out, &pendingAssistantMessage, &pendingReasoningContent)
				out, _ = sjson.SetRawBytes(out, "messages.-1", message)

			case "function_call":
				var calls []gjson.Result
				for i < len(items) && responsesInputItemType(items[i]) == "function_call" {
					calls = append(calls, items[i])
					i++
				}

				// Responses 历史里，tool outputs 不一定紧跟在 function_call 后面；
				// 某些客户端会先插入 assistant commentary/status message。
				// 这里要继续向后找真正的 function_call_output，避免把同一轮工具调用
				// 错拆成“孤立 tool_calls + 孤立 tool outputs”。
				outputs, nextIndex := collectResponsesToolOutputs(items, i)
				i = nextIndex - 1

				var appended bool
				out, appended = appendResponsesToolCallGroup(out, pendingAssistantMessage, calls, outputs, pendingReasoningContent)
				if appended {
					pendingAssistantMessage = nil
					pendingReasoningContent = ""
					continue
				}

				out = flushPendingResponsesAssistantMessage(out, &pendingAssistantMessage, &pendingReasoningContent)

			case "function_call_output":
				// Drop orphaned tool outputs here because Chat Completions requires every
				// tool message to immediately follow an assistant tool_calls message.
			}
		}
		out = flushPendingResponsesAssistantMessage(out, &pendingAssistantMessage, &pendingReasoningContent)
	} else if input.Type == gjson.String {
		msg := []byte(`{}`)
		msg, _ = sjson.SetBytes(msg, "role", "user")
		msg, _ = sjson.SetBytes(msg, "content", input.String())
		out, _ = sjson.SetRawBytes(out, "messages.-1", msg)
	}

	// Convert tools from responses format to chat completions format
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		var chatCompletionsTools []interface{}

		tools.ForEach(func(_, tool gjson.Result) bool {
			// Built-in tools (e.g. {"type":"web_search"}) are already compatible with the Chat Completions schema.
			// Only function tools need structural conversion because Chat Completions nests details under "function".
			toolType := tool.Get("type").String()
			if toolType != "" && toolType != "function" && tool.IsObject() {
				// Almost all providers lack built-in tools, so we just ignore them.
				// chatCompletionsTools = append(chatCompletionsTools, tool.Value())
				return true
			}

			chatTool := []byte(`{"type":"function","function":{}}`)

			// Convert tool structure from responses format to chat completions format
			function := []byte(`{"name":"","description":"","parameters":{}}`)

			if name := tool.Get("name"); name.Exists() {
				function, _ = sjson.SetBytes(function, "name", name.String())
			}

			if description := tool.Get("description"); description.Exists() {
				function, _ = sjson.SetBytes(function, "description", description.String())
			}

			if parameters := tool.Get("parameters"); parameters.Exists() {
				function, _ = sjson.SetRawBytes(function, "parameters", []byte(parameters.Raw))
			}

			chatTool, _ = sjson.SetRawBytes(chatTool, "function", function)
			chatCompletionsTools = append(chatCompletionsTools, gjson.ParseBytes(chatTool).Value())

			return true
		})

		if len(chatCompletionsTools) > 0 {
			out, _ = sjson.SetBytes(out, "tools", chatCompletionsTools)
		}
	}

	if reasoningEffort := root.Get("reasoning.effort"); reasoningEffort.Exists() {
		effort := strings.ToLower(strings.TrimSpace(reasoningEffort.String()))
		if effort != "" {
			out, _ = sjson.SetBytes(out, "reasoning_effort", effort)
		}
	}

	// Convert tool_choice if present
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		if toolChoice.IsObject() || toolChoice.IsArray() || toolChoice.Type == gjson.JSON {
			out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(toolChoice.Raw))
		} else {
			out, _ = sjson.SetBytes(out, "tool_choice", toolChoice.String())
		}
	}

	return out
}
