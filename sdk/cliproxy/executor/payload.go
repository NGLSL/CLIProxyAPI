package executor

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// DedupeRequestToolOutputs 在进入具体 provider executor 前清理请求历史中的重复工具结果。
// 多 key / 多 auth 重试会让同一个客户端请求被多次翻译和尝试；如果上游历史里已经带了重复的
// tool output，这里先按 call_id/tool_call_id/tool_use_id 保留最后一次，保证所有 provider 共用同一份干净历史。
func DedupeRequestToolOutputs(req Request, opts Options) (Request, Options) {
	req.Payload = DedupeToolOutputs(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		opts.OriginalRequest = DedupeToolOutputs(opts.OriginalRequest)
	}
	return req, opts
}

// DedupeToolOutputs 移除常见 API 形态中的重复工具结果，保留最后一次出现的结果。
// 覆盖 Responses input[].call_id、Chat messages[].tool_call_id/call_id，以及 Claude tool_result.tool_use_id。
func DedupeToolOutputs(body []byte) []byte {
	if len(body) == 0 {
		return body
	}

	body = dedupeRootArray(body, "input", func(item gjson.Result) (string, bool) {
		itemType := strings.TrimSpace(item.Get("type").String())
		if !isResponsesToolOutputType(itemType) {
			return "", false
		}
		id := strings.TrimSpace(item.Get("call_id").String())
		return id, id != ""
	})

	body = dedupeRootArray(body, "messages", func(item gjson.Result) (string, bool) {
		if strings.TrimSpace(item.Get("role").String()) != "tool" {
			return "", false
		}
		id := strings.TrimSpace(item.Get("tool_call_id").String())
		if id == "" {
			id = strings.TrimSpace(item.Get("call_id").String())
		}
		return id, id != ""
	})

	body = dedupeClaudeToolResultParts(body)
	return body
}

func isResponsesToolOutputType(itemType string) bool {
	switch itemType {
	case "function_call_output", "tool_search_output", "web_search_call_output", "computer_call_output", "custom_tool_call_output", "local_shell_call_output":
		return true
	default:
		return strings.HasSuffix(itemType, "_call_output")
	}
}

func dedupeRootArray(body []byte, path string, keyFn func(gjson.Result) (string, bool)) []byte {
	arr := gjson.GetBytes(body, path)
	if !arr.IsArray() {
		return body
	}
	deduped, changed := dedupeJSONResults(arr.Array(), keyFn)
	if !changed {
		return body
	}
	updated, err := sjson.SetRawBytes(body, path, deduped)
	if err != nil {
		return body
	}
	return updated
}

func dedupeJSONResults(items []gjson.Result, keyFn func(gjson.Result) (string, bool)) ([]byte, bool) {
	lastIdxByKey := make(map[string]int, len(items))
	candidateIdx := make([]int, 0, len(items))
	for i, item := range items {
		key, ok := keyFn(item)
		if !ok {
			continue
		}
		candidateIdx = append(candidateIdx, i)
		lastIdxByKey[key] = i
	}

	if len(lastIdxByKey) == len(candidateIdx) {
		return nil, false
	}

	keep := make(map[int]struct{}, len(lastIdxByKey))
	for _, idx := range lastIdxByKey {
		keep[idx] = struct{}{}
	}
	candidates := make(map[int]struct{}, len(candidateIdx))
	for _, idx := range candidateIdx {
		candidates[idx] = struct{}{}
	}

	filtered := make([]byte, 0)
	filtered = append(filtered, '[')
	first := true
	for i, item := range items {
		if _, isCandidate := candidates[i]; isCandidate {
			if _, ok := keep[i]; !ok {
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
	return filtered, true
}

func dedupeClaudeToolResultParts(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}

	messageItems := messages.Array()
	type partRef struct {
		messageIdx int
		partIdx    int
	}
	lastRefByID := make(map[string]partRef)
	candidateRefs := make([]partRef, 0)

	for msgIdx, msg := range messageItems {
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		for partIdx, part := range content.Array() {
			if strings.TrimSpace(part.Get("type").String()) != "tool_result" {
				continue
			}
			id := strings.TrimSpace(part.Get("tool_use_id").String())
			if id == "" {
				continue
			}
			ref := partRef{messageIdx: msgIdx, partIdx: partIdx}
			candidateRefs = append(candidateRefs, ref)
			lastRefByID[id] = ref
		}
	}

	if len(lastRefByID) == len(candidateRefs) {
		return body
	}

	keep := make(map[partRef]struct{}, len(lastRefByID))
	for _, ref := range lastRefByID {
		keep[ref] = struct{}{}
	}
	drop := make(map[partRef]struct{}, len(candidateRefs)-len(lastRefByID))
	for _, ref := range candidateRefs {
		if _, ok := keep[ref]; !ok {
			drop[ref] = struct{}{}
		}
	}

	filteredMessages := make([]byte, 0, len(messages.Raw))
	filteredMessages = append(filteredMessages, '[')
	firstMessage := true
	for msgIdx, msg := range messageItems {
		content := msg.Get("content")
		messageRaw := []byte(msg.Raw)
		if content.IsArray() {
			parts := content.Array()
			filteredParts := make([]byte, 0, len(content.Raw))
			filteredParts = append(filteredParts, '[')
			firstPart := true
			for partIdx, part := range parts {
				if _, shouldDrop := drop[partRef{messageIdx: msgIdx, partIdx: partIdx}]; shouldDrop {
					continue
				}
				if !firstPart {
					filteredParts = append(filteredParts, ',')
				}
				filteredParts = append(filteredParts, part.Raw...)
				firstPart = false
			}
			filteredParts = append(filteredParts, ']')
			if len(gjson.ParseBytes(filteredParts).Array()) == 0 {
				continue
			}
			updated, err := sjson.SetRawBytes(messageRaw, "content", filteredParts)
			if err == nil {
				messageRaw = updated
			}
		}
		if !firstMessage {
			filteredMessages = append(filteredMessages, ',')
		}
		filteredMessages = append(filteredMessages, messageRaw...)
		firstMessage = false
	}
	filteredMessages = append(filteredMessages, ']')

	updated, err := sjson.SetRawBytes(body, "messages", filteredMessages)
	if err != nil {
		return body
	}
	return updated
}
