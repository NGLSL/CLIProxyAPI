package executor

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// normalizeOpenAICompatToolOutputs 在请求真正发往 OpenAI 兼容供应商前，
// 对工具结果做最后一次防御性去重。
//
// 长会话在发生流式重试、账号轮换或客户端重放时，可能把同一个工具调用的结果
// 重复放进请求历史。OpenAI Chat Completions 要求一个 tool_call_id 只对应一条
// role=tool 消息；不少兼容供应商遇到重复结果时只返回泛化的
// "Invalid request body"，不会指出具体是哪一条消息有问题。
//
// 这里保留最后一次结果，因为重试后的结果通常比前面的旧结果更完整。普通消息、
// assistant 的 tool_calls 以及没有调用 ID 的消息一律原样保留，避免误改会话语义。
// 同时兼容 Responses 请求体，供 /responses/compact 等复用同一执行器的路径使用。
func normalizeOpenAICompatToolOutputs(body []byte) []byte {
	if len(body) == 0 {
		return body
	}

	body = dedupeOpenAICompatArray(body, "input", func(item gjson.Result) (string, bool) {
		itemType := strings.TrimSpace(item.Get("type").String())
		if !isOpenAICompatToolOutputType(itemType) {
			return "", false
		}
		callID := strings.TrimSpace(item.Get("call_id").String())
		return callID, callID != ""
	})

	body = dedupeOpenAICompatArray(body, "messages", func(item gjson.Result) (string, bool) {
		if !strings.EqualFold(strings.TrimSpace(item.Get("role").String()), "tool") {
			return "", false
		}
		callID := strings.TrimSpace(item.Get("tool_call_id").String())
		if callID == "" {
			// 部分兼容客户端使用 call_id；虽然不是标准 Chat 字段，
			// 仍应把它视为同一工具调用的结果标识。
			callID = strings.TrimSpace(item.Get("call_id").String())
		}
		return callID, callID != ""
	})

	return body
}

// isOpenAICompatToolOutputType 判断 Responses input item 是否属于工具结果。
// 除列出的官方/常见类型外，也兼容供应商扩展的 *_call_output 类型。
func isOpenAICompatToolOutputType(itemType string) bool {
	switch itemType {
	case "function_call_output", "tool_search_output", "web_search_call_output",
		"computer_call_output", "custom_tool_call_output", "local_shell_call_output":
		return true
	default:
		return strings.HasSuffix(itemType, "_call_output")
	}
}

// dedupeOpenAICompatArray 按 keyFn 返回的调用 ID 对指定根数组去重。
// 先记录每个 ID 最后出现的位置，再按原顺序重建数组，因此除重复工具结果外，
// 其他元素的相对顺序不会发生变化。
func dedupeOpenAICompatArray(body []byte, path string, keyFn func(gjson.Result) (string, bool)) []byte {
	array := gjson.GetBytes(body, path)
	if !array.IsArray() {
		return body
	}
	items := array.Array()
	lastIndexByCallID := make(map[string]int, len(items))
	candidateIndexes := make([]int, 0, len(items))
	for index, item := range items {
		callID, ok := keyFn(item)
		if !ok {
			continue
		}
		candidateIndexes = append(candidateIndexes, index)
		lastIndexByCallID[callID] = index
	}
	if len(candidateIndexes) == len(lastIndexByCallID) {
		return body
	}

	keepIndexes := make(map[int]struct{}, len(lastIndexByCallID))
	for _, index := range lastIndexByCallID {
		keepIndexes[index] = struct{}{}
	}
	candidateSet := make(map[int]struct{}, len(candidateIndexes))
	for _, index := range candidateIndexes {
		candidateSet[index] = struct{}{}
	}

	filtered := make([]byte, 0, len(array.Raw))
	filtered = append(filtered, '[')
	first := true
	for index, item := range items {
		if _, candidate := candidateSet[index]; candidate {
			if _, keep := keepIndexes[index]; !keep {
				continue
			}
		}
		if !first {
			filtered = append(filtered, ',')
		}
		filtered = append(filtered, item.Raw...)
		first = false
	}
	filtered = append(filtered, ']')

	updated, err := sjson.SetRawBytes(body, path, filtered)
	if err != nil {
		// 原请求体已经通过 gjson 解析；这里仍保留失败回退，确保规范化失败
		// 不会把请求体破坏成半截 JSON。
		return body
	}
	return updated
}
