package responses

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	translatorcommon "github.com/NGLSL/CLIProxyAPI/v6/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type oaiToResponsesStateReasoning struct {
	ReasoningID      string
	ReasoningData    string
	ReasoningSummary string
	OutputIndex      int
}

type oaiToResponsesStateTool struct {
	ItemID      string
	CallID      string
	Name        string
	Input       string
	OutputIndex int
	Custom      bool
}

func (t oaiToResponsesStateTool) itemType() string {
	if t.Custom {
		return "custom_tool_call"
	}
	return "function_call"
}

func (t oaiToResponsesStateTool) itemField() string {
	if t.Custom {
		return "input"
	}
	return "arguments"
}

func (t oaiToResponsesStateTool) deltaEventType() string {
	if t.Custom {
		return "response.custom_tool_call_input.delta"
	}
	return "response.function_call_arguments.delta"
}

func (t oaiToResponsesStateTool) doneEventType() string {
	if t.Custom {
		return ""
	}
	return "response.function_call_arguments.done"
}

func (t oaiToResponsesStateTool) payloadText() string {
	if t.Custom {
		return t.Input
	}
	return t.Input
}

func (t oaiToResponsesStateTool) withSnapshot() []byte {
	item := []byte(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
	if t.Custom {
		item = []byte(`{"id":"","type":"custom_tool_call","status":"completed","input":"","call_id":"","name":""}`)
	}
	item, _ = sjson.SetBytes(item, "id", t.ItemID)
	item, _ = sjson.SetBytes(item, "call_id", t.CallID)
	item, _ = sjson.SetBytes(item, "name", t.Name)
	item, _ = sjson.SetBytes(item, t.itemField(), t.payloadText())
	return item
}

func buildResponsesReasoningItem(itemID, content, summary string) []byte {
	item := []byte(`{"id":"","type":"reasoning","status":"completed","summary":[],"content":[]}`)
	item, _ = sjson.SetBytes(item, "id", itemID)
	if content == "" {
		item, _ = sjson.DeleteBytes(item, "content")
	} else {
		item, _ = sjson.SetBytes(item, "content.0.type", "reasoning_text")
		item, _ = sjson.SetBytes(item, "content.0.text", content)
	}
	if summary == "" {
		item, _ = sjson.SetRawBytes(item, "summary", []byte("[]"))
	} else {
		item, _ = sjson.SetBytes(item, "summary.0.type", "summary_text")
		item, _ = sjson.SetBytes(item, "summary.0.text", summary)
	}
	return item
}

func responsesToolStateFromRequest(originalRequestRawJSON, requestRawJSON []byte, callID, name string, outputIndex int) oaiToResponsesStateTool {
	callID = strings.TrimSpace(callID)
	name = strings.TrimSpace(name)
	itemID := fmt.Sprintf("fc_%s", callID)
	custom := false
	lookupRoots := []gjson.Result{}
	if len(originalRequestRawJSON) > 0 {
		lookupRoots = append(lookupRoots, gjson.ParseBytes(originalRequestRawJSON))
	}
	if len(requestRawJSON) > 0 {
		lookupRoots = append(lookupRoots, gjson.ParseBytes(requestRawJSON))
	}
	for _, root := range lookupRoots {
		tools := root.Get("tools")
		if !tools.Exists() || !tools.IsArray() {
			continue
		}
		tools.ForEach(func(_, tool gjson.Result) bool {
			if strings.TrimSpace(tool.Get("name").String()) != name {
				return true
			}
			if strings.EqualFold(strings.TrimSpace(tool.Get("type").String()), "custom") {
				custom = true
				itemID = fmt.Sprintf("ctc_%s", callID)
				return false
			}
			return true
		})
		if custom {
			break
		}
	}
	return oaiToResponsesStateTool{ItemID: itemID, CallID: callID, Name: name, OutputIndex: outputIndex, Custom: custom}
}

func buildResponsesToolAddedEvent(tool oaiToResponsesStateTool, nextSeq func() int) []byte {
	item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"in_progress","arguments":"","call_id":"","name":""}}`)
	if tool.Custom {
		item = []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"custom_tool_call","status":"in_progress","input":"","call_id":"","name":""}}`)
	}
	item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
	item, _ = sjson.SetBytes(item, "output_index", tool.OutputIndex)
	item, _ = sjson.SetBytes(item, "item.id", tool.ItemID)
	item, _ = sjson.SetBytes(item, "item.call_id", tool.CallID)
	item, _ = sjson.SetBytes(item, "item.name", tool.Name)
	item, _ = sjson.SetBytes(item, "item.status", "in_progress")
	item, _ = sjson.SetBytes(item, "item."+tool.itemField(), "")
	return emitRespEvent("response.output_item.added", item)
}

func buildResponsesToolDeltaEvent(tool oaiToResponsesStateTool, delta string, nextSeq func() int) []byte {
	msg := []byte(`{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"","output_index":0,"delta":""}`)
	if tool.Custom {
		msg = []byte(`{"type":"response.custom_tool_call_input.delta","sequence_number":0,"item_id":"","output_index":0,"call_id":"","delta":""}`)
	}
	msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
	msg, _ = sjson.SetBytes(msg, "item_id", tool.ItemID)
	msg, _ = sjson.SetBytes(msg, "output_index", tool.OutputIndex)
	msg, _ = sjson.SetBytes(msg, "delta", delta)
	if tool.Custom {
		msg, _ = sjson.SetBytes(msg, "call_id", tool.CallID)
	}
	return emitRespEvent(tool.deltaEventType(), msg)
}

func buildResponsesToolDoneEvents(tool oaiToResponsesStateTool, nextSeq func() int) [][]byte {
	out := make([][]byte, 0, 2)
	if doneType := tool.doneEventType(); doneType != "" {
		done := []byte(`{"type":"response.function_call_arguments.done","sequence_number":0,"item_id":"","output_index":0,"arguments":""}`)
		done, _ = sjson.SetBytes(done, "sequence_number", nextSeq())
		done, _ = sjson.SetBytes(done, "item_id", tool.ItemID)
		done, _ = sjson.SetBytes(done, "output_index", tool.OutputIndex)
		done, _ = sjson.SetBytes(done, tool.itemField(), tool.payloadText())
		out = append(out, emitRespEvent(doneType, done))
	}
	itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}}`)
	if tool.Custom {
		itemDone = []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"custom_tool_call","status":"completed","input":"","call_id":"","name":""}}`)
	}
	itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", nextSeq())
	itemDone, _ = sjson.SetBytes(itemDone, "output_index", tool.OutputIndex)
	itemDone, _ = sjson.SetRawBytes(itemDone, "item", tool.withSnapshot())
	out = append(out, emitRespEvent("response.output_item.done", itemDone))
	return out
}

func buildResponsesReasoningDeltaEvent(itemID string, outputIndex int, delta string, nextSeq func() int) []byte {
	msg := []byte(`{"type":"response.reasoning_text.delta","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"delta":""}`)
	msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
	msg, _ = sjson.SetBytes(msg, "item_id", itemID)
	msg, _ = sjson.SetBytes(msg, "output_index", outputIndex)
	msg, _ = sjson.SetBytes(msg, "content_index", 0)
	msg, _ = sjson.SetBytes(msg, "delta", delta)
	return emitRespEvent("response.reasoning_text.delta", msg)
}

func buildResponsesReasoningStartEvents(itemID string, outputIndex int, nextSeq func() int) [][]byte {
	item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"in_progress","summary":[],"content":[]}}`)
	item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
	item, _ = sjson.SetBytes(item, "output_index", outputIndex)
	item, _ = sjson.SetBytes(item, "item.id", itemID)
	part := []byte(`{"type":"response.reasoning_summary_part.added","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
	part, _ = sjson.SetBytes(part, "sequence_number", nextSeq())
	part, _ = sjson.SetBytes(part, "item_id", itemID)
	part, _ = sjson.SetBytes(part, "output_index", outputIndex)
	return [][]byte{emitRespEvent("response.output_item.added", item), emitRespEvent("response.reasoning_summary_part.added", part)}
}

func buildResponsesReasoningDoneEvents(itemID string, outputIndex int, content, summary string, nextSeq func() int) [][]byte {
	out := make([][]byte, 0, 3)
	textDone := []byte(`{"type":"response.reasoning_summary_text.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"text":""}`)
	textDone, _ = sjson.SetBytes(textDone, "sequence_number", nextSeq())
	textDone, _ = sjson.SetBytes(textDone, "item_id", itemID)
	textDone, _ = sjson.SetBytes(textDone, "output_index", outputIndex)
	textDone, _ = sjson.SetBytes(textDone, "text", summary)
	out = append(out, emitRespEvent("response.reasoning_summary_text.done", textDone))
	partDone := []byte(`{"type":"response.reasoning_summary_part.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
	partDone, _ = sjson.SetBytes(partDone, "sequence_number", nextSeq())
	partDone, _ = sjson.SetBytes(partDone, "item_id", itemID)
	partDone, _ = sjson.SetBytes(partDone, "output_index", outputIndex)
	partDone, _ = sjson.SetBytes(partDone, "part.text", summary)
	out = append(out, emitRespEvent("response.reasoning_summary_part.done", partDone))
	itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"completed","summary":[],"content":[]}}`)
	itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", nextSeq())
	itemDone, _ = sjson.SetBytes(itemDone, "output_index", outputIndex)
	itemDone, _ = sjson.SetRawBytes(itemDone, "item", buildResponsesReasoningItem(itemID, content, summary))
	out = append(out, emitRespEvent("response.output_item.done", itemDone))
	return out
}

func responseSummaryReasoningText(requestRawJSON []byte) string {
	if len(requestRawJSON) == 0 {
		return ""
	}
	reasoning := gjson.GetBytes(requestRawJSON, "reasoning")
	if !reasoning.Exists() {
		return ""
	}
	summary := strings.TrimSpace(reasoning.Get("summary").String())
	generateSummary := strings.TrimSpace(reasoning.Get("generate_summary").String())
	if strings.EqualFold(summary, "none") || strings.EqualFold(generateSummary, "none") {
		return ""
	}
	return summary
}

func responseIncludeReasoningSummary(requestRawJSON []byte) bool {
	return responseSummaryReasoningText(requestRawJSON) != ""
}

func responseIncludeReasoningContent(requestRawJSON []byte) bool {
	if len(requestRawJSON) == 0 {
		return false
	}
	reasoning := gjson.GetBytes(requestRawJSON, "reasoning")
	return reasoning.Exists()
}

func responseSupportsCustomTool(name string, originalRequestRawJSON, requestRawJSON []byte) bool {
	tool := responsesToolStateFromRequest(originalRequestRawJSON, requestRawJSON, "probe", name, 0)
	return tool.Custom
}

func responseToolState(originalRequestRawJSON, requestRawJSON []byte, callID, name string, outputIndex int) oaiToResponsesStateTool {
	return responsesToolStateFromRequest(originalRequestRawJSON, requestRawJSON, callID, name, outputIndex)
}

func responseBuildToolSnapshot(tool oaiToResponsesStateTool) []byte {
	return tool.withSnapshot()
}

type oaiToResponsesState struct {
	Seq               int
	ResponseID        string
	Created           int64
	Started           bool
	CompletionPending bool
	CompletedEmitted  bool
	ReasoningID       string
	ReasoningIndex    int
	// aggregation buffers for response.output
	// Per-output message text buffers by index
	MsgTextBuf       map[int]*strings.Builder
	MsgRefusalBuf    map[int]*strings.Builder
	MsgAnnotations   map[int][]byte
	MsgTextLogprobs  map[int][]byte
	ReasoningBuf     strings.Builder
	Reasonings       []oaiToResponsesStateReasoning
	FuncArgsBuf      map[string]*strings.Builder
	FuncNames        map[string]string
	FuncCallIDs      map[string]string
	FuncOutputIx     map[string]int
	MsgOutputIx      map[int]int
	FuncCustom       map[string]bool
	FuncItemIDs      map[string]string
	ReasoningSummary string
	MsgTextContentIx map[int]int
	MsgRefusalPartIx map[int]int
	MsgNextContentIx map[int]int
	NextOutputIx     int
	// message item state per output index
	MsgItemAdded    map[int]bool // whether response.output_item.added emitted for message
	MsgContentAdded map[int]bool // whether response.content_part.added emitted for text content
	MsgRefusalAdded map[int]bool // whether response.content_part.added emitted for refusal content
	MsgItemDone     map[int]bool // whether message done events were emitted
	// function item done state
	FuncArgsDone map[string]bool
	FuncItemDone map[string]bool
	// usage aggregation
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	ReasoningTokens  int64
	UsageSeen        bool
}

// responseIDCounter provides a process-wide unique counter for synthesized response identifiers.
var responseIDCounter uint64

func emitRespEvent(event string, payload []byte) []byte {
	return translatorcommon.SSEEventData(event, payload)
}

type requestFieldSource struct {
	root gjson.Result
	path string
}

func setRawJSONBytes(dst []byte, path string, raw []byte) []byte {
	updated, err := sjson.SetRawBytes(dst, path, raw)
	if err != nil {
		return dst
	}
	return updated
}

func setRawJSONResult(dst []byte, path string, value gjson.Result) []byte {
	if !value.Exists() {
		return dst
	}
	return setRawJSONBytes(dst, path, []byte(value.Raw))
}

func setFirstRawJSONField(dst []byte, path string, sources ...requestFieldSource) []byte {
	for _, source := range sources {
		if !source.root.Exists() {
			continue
		}
		if value := source.root.Get(source.path); value.Exists() {
			return setRawJSONResult(dst, path, value)
		}
	}
	return dst
}

func echoOpenAIResponsesRequestFields(dst []byte, prefix string, originalRequestRawJSON, requestRawJSON []byte) []byte {
	var originalReq gjson.Result
	if len(originalRequestRawJSON) > 0 {
		originalReq = gjson.ParseBytes(originalRequestRawJSON)
	}
	var translatedReq gjson.Result
	if len(requestRawJSON) > 0 {
		translatedReq = gjson.ParseBytes(requestRawJSON)
	}

	dst = setFirstRawJSONField(dst, prefix+"instructions",
		requestFieldSource{root: originalReq, path: "instructions"},
	)
	dst = setFirstRawJSONField(dst, prefix+"max_output_tokens",
		requestFieldSource{root: originalReq, path: "max_output_tokens"},
		requestFieldSource{root: translatedReq, path: "max_output_tokens"},
		requestFieldSource{root: translatedReq, path: "max_tokens"},
	)
	dst = setFirstRawJSONField(dst, prefix+"max_tool_calls",
		requestFieldSource{root: originalReq, path: "max_tool_calls"},
		requestFieldSource{root: translatedReq, path: "max_tool_calls"},
	)
	dst = setFirstRawJSONField(dst, prefix+"model",
		requestFieldSource{root: translatedReq, path: "model"},
		requestFieldSource{root: originalReq, path: "model"},
	)
	dst = setFirstRawJSONField(dst, prefix+"parallel_tool_calls",
		requestFieldSource{root: originalReq, path: "parallel_tool_calls"},
		requestFieldSource{root: translatedReq, path: "parallel_tool_calls"},
	)
	dst = setFirstRawJSONField(dst, prefix+"previous_response_id",
		requestFieldSource{root: originalReq, path: "previous_response_id"},
		requestFieldSource{root: translatedReq, path: "previous_response_id"},
	)
	dst = setFirstRawJSONField(dst, prefix+"prompt_cache_key",
		requestFieldSource{root: originalReq, path: "prompt_cache_key"},
		requestFieldSource{root: translatedReq, path: "prompt_cache_key"},
	)
	dst = setFirstRawJSONField(dst, prefix+"prompt_cache_retention",
		requestFieldSource{root: originalReq, path: "prompt_cache_retention"},
		requestFieldSource{root: translatedReq, path: "prompt_cache_retention"},
	)
	if value := originalReq.Get("reasoning"); value.Exists() {
		dst = setRawJSONResult(dst, prefix+"reasoning", value)
	} else {
		reasoning := []byte(`{}`)
		hasReasoning := false
		if value := translatedReq.Get("reasoning.summary"); value.Exists() {
			reasoning, _ = sjson.SetRawBytes(reasoning, "summary", []byte(value.Raw))
			hasReasoning = true
		}
		if value := translatedReq.Get("reasoning_effort"); value.Exists() {
			reasoning, _ = sjson.SetRawBytes(reasoning, "effort", []byte(value.Raw))
			hasReasoning = true
		}
		if hasReasoning {
			dst = setRawJSONBytes(dst, prefix+"reasoning", reasoning)
		}
	}
	dst = setFirstRawJSONField(dst, prefix+"safety_identifier",
		requestFieldSource{root: originalReq, path: "safety_identifier"},
		requestFieldSource{root: translatedReq, path: "safety_identifier"},
	)
	dst = setFirstRawJSONField(dst, prefix+"service_tier",
		requestFieldSource{root: originalReq, path: "service_tier"},
		requestFieldSource{root: translatedReq, path: "service_tier"},
	)
	dst = setFirstRawJSONField(dst, prefix+"store",
		requestFieldSource{root: originalReq, path: "store"},
		requestFieldSource{root: translatedReq, path: "store"},
	)
	dst = setFirstRawJSONField(dst, prefix+"temperature",
		requestFieldSource{root: originalReq, path: "temperature"},
		requestFieldSource{root: translatedReq, path: "temperature"},
	)
	if value := originalReq.Get("text"); value.Exists() {
		dst = setRawJSONResult(dst, prefix+"text", value)
	} else if value := translatedReq.Get("text"); value.Exists() {
		dst = setRawJSONResult(dst, prefix+"text", value)
	} else if value := translatedReq.Get("response_format"); value.Exists() {
		text := []byte(`{}`)
		text, _ = sjson.SetRawBytes(text, "format", []byte(value.Raw))
		dst = setRawJSONBytes(dst, prefix+"text", text)
	}
	dst = setFirstRawJSONField(dst, prefix+"tool_choice",
		requestFieldSource{root: originalReq, path: "tool_choice"},
		requestFieldSource{root: translatedReq, path: "tool_choice"},
	)
	dst = setFirstRawJSONField(dst, prefix+"tools",
		requestFieldSource{root: originalReq, path: "tools"},
		requestFieldSource{root: translatedReq, path: "tools"},
	)
	dst = setFirstRawJSONField(dst, prefix+"top_logprobs",
		requestFieldSource{root: originalReq, path: "top_logprobs"},
		requestFieldSource{root: translatedReq, path: "top_logprobs"},
	)
	dst = setFirstRawJSONField(dst, prefix+"top_p",
		requestFieldSource{root: originalReq, path: "top_p"},
		requestFieldSource{root: translatedReq, path: "top_p"},
	)
	dst = setFirstRawJSONField(dst, prefix+"truncation",
		requestFieldSource{root: originalReq, path: "truncation"},
		requestFieldSource{root: translatedReq, path: "truncation"},
	)
	dst = setFirstRawJSONField(dst, prefix+"user",
		requestFieldSource{root: originalReq, path: "user"},
		requestFieldSource{root: translatedReq, path: "user"},
	)
	dst = setFirstRawJSONField(dst, prefix+"metadata",
		requestFieldSource{root: originalReq, path: "metadata"},
		requestFieldSource{root: translatedReq, path: "metadata"},
	)
	return dst
}

func mergeJSONArrayRaw(existing []byte, values gjson.Result) []byte {
	if !values.Exists() || !values.IsArray() {
		return existing
	}
	wrapper := []byte(`{"arr":[]}`)
	if len(existing) > 0 && gjson.Valid(string(existing)) {
		wrapper, _ = sjson.SetRawBytes(wrapper, "arr", existing)
	}
	values.ForEach(func(_, value gjson.Result) bool {
		wrapper, _ = sjson.SetRawBytes(wrapper, "arr.-1", []byte(value.Raw))
		return true
	})
	if arr := gjson.GetBytes(wrapper, "arr"); arr.Exists() {
		return []byte(arr.Raw)
	}
	return existing
}

func buildResponsesTextEventLogprobs(values gjson.Result) []byte {
	if !values.Exists() || !values.IsArray() {
		return nil
	}
	wrapper := []byte(`{"arr":[]}`)
	values.ForEach(func(_, value gjson.Result) bool {
		item := []byte(`{"token":"","logprob":0,"top_logprobs":[]}`)
		item = setRawJSONResult(item, "token", value.Get("token"))
		item = setRawJSONResult(item, "logprob", value.Get("logprob"))
		if topLogprobs := value.Get("top_logprobs"); topLogprobs.Exists() && topLogprobs.IsArray() {
			topWrapper := []byte(`{"arr":[]}`)
			topLogprobs.ForEach(func(_, top gjson.Result) bool {
				topItem := []byte(`{"token":"","logprob":0}`)
				topItem = setRawJSONResult(topItem, "token", top.Get("token"))
				topItem = setRawJSONResult(topItem, "logprob", top.Get("logprob"))
				topWrapper, _ = sjson.SetRawBytes(topWrapper, "arr.-1", topItem)
				return true
			})
			if arr := gjson.GetBytes(topWrapper, "arr"); arr.Exists() {
				item, _ = sjson.SetRawBytes(item, "top_logprobs", []byte(arr.Raw))
			}
		}
		wrapper, _ = sjson.SetRawBytes(wrapper, "arr.-1", item)
		return true
	})
	if arr := gjson.GetBytes(wrapper, "arr"); arr.Exists() {
		return []byte(arr.Raw)
	}
	return nil
}

func buildResponsesOutputTextPart(text string, annotationsRaw, logprobsRaw []byte) []byte {
	part := []byte(`{"type":"output_text","annotations":[],"logprobs":[],"text":""}`)
	part, _ = sjson.SetBytes(part, "text", text)
	if len(annotationsRaw) > 0 && gjson.Valid(string(annotationsRaw)) {
		part, _ = sjson.SetRawBytes(part, "annotations", annotationsRaw)
	}
	if len(logprobsRaw) > 0 && gjson.Valid(string(logprobsRaw)) {
		part, _ = sjson.SetRawBytes(part, "logprobs", logprobsRaw)
	}
	return part
}

func buildResponsesRefusalPart(refusal string) []byte {
	part := []byte(`{"type":"refusal","refusal":""}`)
	part, _ = sjson.SetBytes(part, "refusal", refusal)
	return part
}

func buildResponsesMessageContent(text, refusal string, annotationsRaw, logprobsRaw []byte) []byte {
	wrapper := []byte(`{"arr":[]}`)
	if text != "" || len(annotationsRaw) > 0 || len(logprobsRaw) > 0 {
		wrapper, _ = sjson.SetRawBytes(wrapper, "arr.-1", buildResponsesOutputTextPart(text, annotationsRaw, logprobsRaw))
	}
	if refusal != "" {
		wrapper, _ = sjson.SetRawBytes(wrapper, "arr.-1", buildResponsesRefusalPart(refusal))
	}
	if arr := gjson.GetBytes(wrapper, "arr"); arr.Exists() {
		return []byte(arr.Raw)
	}
	return []byte("[]")
}

func buildResponsesMessageItem(itemID, text, refusal string, annotationsRaw, logprobsRaw []byte) []byte {
	item := []byte(`{"id":"","type":"message","status":"completed","content":[],"role":"assistant"}`)
	item, _ = sjson.SetBytes(item, "id", itemID)
	item, _ = sjson.SetRawBytes(item, "content", buildResponsesMessageContent(text, refusal, annotationsRaw, logprobsRaw))
	return item
}

func emitCompletedMessageEvents(out *[][]byte, st *oaiToResponsesState, choiceIndex int, nextSeq func() int) {
	if !st.MsgItemAdded[choiceIndex] || st.MsgItemDone[choiceIndex] {
		return
	}
	msgOutputIndex := st.MsgOutputIx[choiceIndex]
	itemID := fmt.Sprintf("msg_%s_%d", st.ResponseID, choiceIndex)
	fullText := ""
	if b := st.MsgTextBuf[choiceIndex]; b != nil {
		fullText = b.String()
	}
	fullRefusal := ""
	if b := st.MsgRefusalBuf[choiceIndex]; b != nil {
		fullRefusal = b.String()
	}
	annotationsRaw := st.MsgAnnotations[choiceIndex]
	logprobsRaw := st.MsgTextLogprobs[choiceIndex]

	if st.MsgContentAdded[choiceIndex] {
		contentIndex := st.MsgTextContentIx[choiceIndex]
		done := []byte(`{"type":"response.output_text.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"text":"","logprobs":[]}`)
		done, _ = sjson.SetBytes(done, "sequence_number", nextSeq())
		done, _ = sjson.SetBytes(done, "item_id", itemID)
		done, _ = sjson.SetBytes(done, "output_index", msgOutputIndex)
		done, _ = sjson.SetBytes(done, "content_index", contentIndex)
		done, _ = sjson.SetBytes(done, "text", fullText)
		if len(logprobsRaw) > 0 {
			done, _ = sjson.SetRawBytes(done, "logprobs", logprobsRaw)
		}
		*out = append(*out, emitRespEvent("response.output_text.done", done))

		partDone := []byte(`{"type":"response.content_part.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
		partDone, _ = sjson.SetBytes(partDone, "sequence_number", nextSeq())
		partDone, _ = sjson.SetBytes(partDone, "item_id", itemID)
		partDone, _ = sjson.SetBytes(partDone, "output_index", msgOutputIndex)
		partDone, _ = sjson.SetBytes(partDone, "content_index", contentIndex)
		partDone, _ = sjson.SetRawBytes(partDone, "part", buildResponsesOutputTextPart(fullText, annotationsRaw, logprobsRaw))
		*out = append(*out, emitRespEvent("response.content_part.done", partDone))
	}

	if st.MsgRefusalAdded[choiceIndex] {
		contentIndex := st.MsgRefusalPartIx[choiceIndex]
		done := []byte(`{"type":"response.refusal.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"refusal":""}`)
		done, _ = sjson.SetBytes(done, "sequence_number", nextSeq())
		done, _ = sjson.SetBytes(done, "item_id", itemID)
		done, _ = sjson.SetBytes(done, "output_index", msgOutputIndex)
		done, _ = sjson.SetBytes(done, "content_index", contentIndex)
		done, _ = sjson.SetBytes(done, "refusal", fullRefusal)
		*out = append(*out, emitRespEvent("response.refusal.done", done))

		partDone := []byte(`{"type":"response.content_part.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"refusal","refusal":""}}`)
		partDone, _ = sjson.SetBytes(partDone, "sequence_number", nextSeq())
		partDone, _ = sjson.SetBytes(partDone, "item_id", itemID)
		partDone, _ = sjson.SetBytes(partDone, "output_index", msgOutputIndex)
		partDone, _ = sjson.SetBytes(partDone, "content_index", contentIndex)
		partDone, _ = sjson.SetRawBytes(partDone, "part", buildResponsesRefusalPart(fullRefusal))
		*out = append(*out, emitRespEvent("response.content_part.done", partDone))
	}

	itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"completed","content":[],"role":"assistant"}}`)
	itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", nextSeq())
	itemDone, _ = sjson.SetBytes(itemDone, "output_index", msgOutputIndex)
	itemDone, _ = sjson.SetBytes(itemDone, "item.id", itemID)
	itemDone, _ = sjson.SetRawBytes(itemDone, "item.content", buildResponsesMessageContent(fullText, fullRefusal, annotationsRaw, logprobsRaw))
	*out = append(*out, emitRespEvent("response.output_item.done", itemDone))
	st.MsgItemDone[choiceIndex] = true
}

func hasCompletedResponseOutput(st *oaiToResponsesState) bool {
	if st == nil {
		return false
	}
	if len(st.MsgItemAdded) > 0 || len(st.FuncCallIDs) > 0 || len(st.Reasonings) > 0 {
		return true
	}
	if st.ReasoningID != "" && st.ReasoningBuf.Len() > 0 {
		return true
	}
	return false
}

func buildResponsesCompletedEvent(st *oaiToResponsesState, originalRequestRawJSON, requestRawJSON []byte, nextSeq func() int) []byte {
	completed := []byte(`{"type":"response.completed","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null}}`)
	completed, _ = sjson.SetBytes(completed, "sequence_number", nextSeq())
	completed, _ = sjson.SetBytes(completed, "response.id", st.ResponseID)
	completed, _ = sjson.SetBytes(completed, "response.created_at", st.Created)
	completed = echoOpenAIResponsesRequestFields(completed, "response.", originalRequestRawJSON, requestRawJSON)

	outputsWrapper := []byte(`{"arr":[]}`)
	type completedOutputItem struct {
		index int
		raw   []byte
	}
	outputItems := make([]completedOutputItem, 0, len(st.Reasonings)+len(st.MsgItemAdded)+len(st.FuncArgsBuf))

	if len(st.Reasonings) > 0 {
		for _, r := range st.Reasonings {
			item := buildResponsesReasoningItem(r.ReasoningID, r.ReasoningData, r.ReasoningSummary)
			outputItems = append(outputItems, completedOutputItem{index: r.OutputIndex, raw: item})
		}
	}
	if st.ReasoningID != "" {
		item := buildResponsesReasoningItem(st.ReasoningID, st.ReasoningBuf.String(), "")
		outputItems = append(outputItems, completedOutputItem{index: st.ReasoningIndex, raw: item})
	}
	if len(st.MsgItemAdded) > 0 {
		for i, added := range st.MsgItemAdded {
			if !added {
				continue
			}
			txt := ""
			if b := st.MsgTextBuf[i]; b != nil {
				txt = b.String()
			}
			refusal := ""
			if b := st.MsgRefusalBuf[i]; b != nil {
				refusal = b.String()
			}
			item := buildResponsesMessageItem(
				fmt.Sprintf("msg_%s_%d", st.ResponseID, i),
				txt,
				refusal,
				st.MsgAnnotations[i],
				st.MsgTextLogprobs[i],
			)
			outputItems = append(outputItems, completedOutputItem{index: st.MsgOutputIx[i], raw: item})
		}
	}
	if len(st.FuncArgsBuf) > 0 {
		for key := range st.FuncArgsBuf {
			callID := st.FuncCallIDs[key]
			if callID == "" {
				continue
			}
			args := ""
			if b := st.FuncArgsBuf[key]; b != nil {
				args = b.String()
			}
			item := []byte(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
			item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("fc_%s", callID))
			item, _ = sjson.SetBytes(item, "arguments", args)
			item, _ = sjson.SetBytes(item, "call_id", callID)
			item, _ = sjson.SetBytes(item, "name", st.FuncNames[key])
			outputItems = append(outputItems, completedOutputItem{index: st.FuncOutputIx[key], raw: item})
		}
	}

	sort.Slice(outputItems, func(i, j int) bool { return outputItems[i].index < outputItems[j].index })
	for _, item := range outputItems {
		outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", item.raw)
	}
	if gjson.GetBytes(outputsWrapper, "arr.#").Int() > 0 {
		completed, _ = sjson.SetRawBytes(completed, "response.output", []byte(gjson.GetBytes(outputsWrapper, "arr").Raw))
	}
	if st.UsageSeen {
		completed, _ = sjson.SetBytes(completed, "response.usage.input_tokens", st.PromptTokens)
		completed, _ = sjson.SetBytes(completed, "response.usage.input_tokens_details.cached_tokens", st.CachedTokens)
		completed, _ = sjson.SetBytes(completed, "response.usage.output_tokens", st.CompletionTokens)
		if st.ReasoningTokens > 0 {
			completed, _ = sjson.SetBytes(completed, "response.usage.output_tokens_details.reasoning_tokens", st.ReasoningTokens)
		}
		total := st.TotalTokens
		if total == 0 {
			total = st.PromptTokens + st.CompletionTokens
		}
		completed, _ = sjson.SetBytes(completed, "response.usage.total_tokens", total)
	}
	return emitRespEvent("response.completed", completed)
}

// ConvertOpenAIChatCompletionsResponseToOpenAIResponses converts OpenAI Chat Completions streaming chunks
// to OpenAI Responses SSE events (response.*).
func ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &oaiToResponsesState{
			FuncArgsBuf:      make(map[string]*strings.Builder),
			FuncNames:        make(map[string]string),
			FuncCallIDs:      make(map[string]string),
			FuncOutputIx:     make(map[string]int),
			FuncCustom:       make(map[string]bool),
			FuncItemIDs:      make(map[string]string),
			MsgOutputIx:      make(map[int]int),
			MsgTextBuf:       make(map[int]*strings.Builder),
			MsgRefusalBuf:    make(map[int]*strings.Builder),
			MsgAnnotations:   make(map[int][]byte),
			MsgTextLogprobs:  make(map[int][]byte),
			MsgTextContentIx: make(map[int]int),
			MsgRefusalPartIx: make(map[int]int),
			MsgNextContentIx: make(map[int]int),
			MsgItemAdded:     make(map[int]bool),
			MsgContentAdded:  make(map[int]bool),
			MsgRefusalAdded:  make(map[int]bool),
			MsgItemDone:      make(map[int]bool),
			FuncArgsDone:     make(map[string]bool),
			FuncItemDone:     make(map[string]bool),
			Reasonings:       make([]oaiToResponsesStateReasoning, 0),
		}
	}
	st := (*param).(*oaiToResponsesState)

	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}
	rawJSON = bytes.TrimSpace(rawJSON)
	if len(rawJSON) == 0 {
		return [][]byte{}
	}
	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		if !st.CompletedEmitted && (st.CompletionPending || hasCompletedResponseOutput(st)) {
			st.CompletedEmitted = true
			return [][]byte{buildResponsesCompletedEvent(st, originalRequestRawJSON, requestRawJSON, func() int { st.Seq++; return st.Seq })}
		}
		return [][]byte{}
	}

	root := gjson.ParseBytes(rawJSON)
	obj := root.Get("object")
	if obj.Exists() && obj.String() != "" && obj.String() != "chat.completion.chunk" {
		return [][]byte{}
	}
	if !root.Get("choices").Exists() || !root.Get("choices").IsArray() {
		return [][]byte{}
	}

	if usage := root.Get("usage"); usage.Exists() {
		if v := usage.Get("prompt_tokens"); v.Exists() {
			st.PromptTokens = v.Int()
			st.UsageSeen = true
		}
		if v := usage.Get("prompt_tokens_details.cached_tokens"); v.Exists() {
			st.CachedTokens = v.Int()
			st.UsageSeen = true
		}
		if v := usage.Get("completion_tokens"); v.Exists() {
			st.CompletionTokens = v.Int()
			st.UsageSeen = true
		} else if v := usage.Get("output_tokens"); v.Exists() {
			st.CompletionTokens = v.Int()
			st.UsageSeen = true
		}
		if v := usage.Get("output_tokens_details.reasoning_tokens"); v.Exists() {
			st.ReasoningTokens = v.Int()
			st.UsageSeen = true
		} else if v := usage.Get("completion_tokens_details.reasoning_tokens"); v.Exists() {
			st.ReasoningTokens = v.Int()
			st.UsageSeen = true
		}
		if v := usage.Get("total_tokens"); v.Exists() {
			st.TotalTokens = v.Int()
			st.UsageSeen = true
		}
	}

	nextSeq := func() int { st.Seq++; return st.Seq }
	allocOutputIndex := func() int {
		ix := st.NextOutputIx
		st.NextOutputIx++
		return ix
	}
	toolStateKey := func(choiceIndex, toolIndex int) string { return fmt.Sprintf("%d:%d", choiceIndex, toolIndex) }
	var out [][]byte

	if !st.Started {
		st.ResponseID = root.Get("id").String()
		st.Created = root.Get("created").Int()
		st.MsgTextBuf = make(map[int]*strings.Builder)
		st.MsgRefusalBuf = make(map[int]*strings.Builder)
		st.MsgAnnotations = make(map[int][]byte)
		st.MsgTextLogprobs = make(map[int][]byte)
		st.MsgTextContentIx = make(map[int]int)
		st.MsgRefusalPartIx = make(map[int]int)
		st.MsgNextContentIx = make(map[int]int)
		st.ReasoningBuf.Reset()
		st.ReasoningID = ""
		st.ReasoningIndex = 0
		st.FuncArgsBuf = make(map[string]*strings.Builder)
		st.FuncNames = make(map[string]string)
		st.FuncCallIDs = make(map[string]string)
		st.FuncOutputIx = make(map[string]int)
		st.FuncCustom = make(map[string]bool)
		st.FuncItemIDs = make(map[string]string)
		st.MsgOutputIx = make(map[int]int)
		st.NextOutputIx = 0
		st.MsgItemAdded = make(map[int]bool)
		st.MsgContentAdded = make(map[int]bool)
		st.MsgRefusalAdded = make(map[int]bool)
		st.MsgItemDone = make(map[int]bool)
		st.FuncArgsDone = make(map[string]bool)
		st.FuncItemDone = make(map[string]bool)
		st.Reasonings = make([]oaiToResponsesStateReasoning, 0)
		st.PromptTokens = 0
		st.CachedTokens = 0
		st.CompletionTokens = 0
		st.TotalTokens = 0
		st.ReasoningTokens = 0
		st.UsageSeen = false
		st.CompletionPending = false
		st.CompletedEmitted = false

		created := []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`)
		created, _ = sjson.SetBytes(created, "sequence_number", nextSeq())
		created, _ = sjson.SetBytes(created, "response.id", st.ResponseID)
		created, _ = sjson.SetBytes(created, "response.created_at", st.Created)
		out = append(out, emitRespEvent("response.created", created))

		inprog := []byte(`{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress"}}`)
		inprog, _ = sjson.SetBytes(inprog, "sequence_number", nextSeq())
		inprog, _ = sjson.SetBytes(inprog, "response.id", st.ResponseID)
		inprog, _ = sjson.SetBytes(inprog, "response.created_at", st.Created)
		out = append(out, emitRespEvent("response.in_progress", inprog))
		st.Started = true
	}

	stopReasoning := func(text string) {
		outputItemDone := []byte(`{"type":"response.output_item.done","item":{"id":"","type":"reasoning","status":"completed","summary":[],"content":[]},"output_index":0,"sequence_number":0}`)
		outputItemDone, _ = sjson.SetBytes(outputItemDone, "sequence_number", nextSeq())
		outputItemDone, _ = sjson.SetBytes(outputItemDone, "output_index", st.ReasoningIndex)
		outputItemDone, _ = sjson.SetRawBytes(outputItemDone, "item", buildResponsesReasoningItem(st.ReasoningID, text, ""))
		out = append(out, emitRespEvent("response.output_item.done", outputItemDone))

		st.Reasonings = append(st.Reasonings, oaiToResponsesStateReasoning{ReasoningID: st.ReasoningID, ReasoningData: text, ReasoningSummary: "", OutputIndex: st.ReasoningIndex})
		st.ReasoningID = ""
	}

	emitFunctionDone := func(keys []string) {
		sort.Slice(keys, func(i, j int) bool {
			left := st.FuncOutputIx[keys[i]]
			right := st.FuncOutputIx[keys[j]]
			return left < right || (left == right && keys[i] < keys[j])
		})
		for _, key := range keys {
			callID := st.FuncCallIDs[key]
			if callID == "" || st.FuncItemDone[key] {
				continue
			}
			outputIndex := st.FuncOutputIx[key]
			args := "{}"
			if b := st.FuncArgsBuf[key]; b != nil && b.Len() > 0 {
				args = b.String()
			}

			fcDone := []byte(`{"type":"response.function_call_arguments.done","sequence_number":0,"item_id":"","output_index":0,"arguments":""}`)
			fcDone, _ = sjson.SetBytes(fcDone, "sequence_number", nextSeq())
			fcDone, _ = sjson.SetBytes(fcDone, "item_id", fmt.Sprintf("fc_%s", callID))
			fcDone, _ = sjson.SetBytes(fcDone, "output_index", outputIndex)
			fcDone, _ = sjson.SetBytes(fcDone, "arguments", args)
			out = append(out, emitRespEvent("response.function_call_arguments.done", fcDone))

			itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}}`)
			itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", nextSeq())
			itemDone, _ = sjson.SetBytes(itemDone, "output_index", outputIndex)
			itemDone, _ = sjson.SetBytes(itemDone, "item.id", fmt.Sprintf("fc_%s", callID))
			itemDone, _ = sjson.SetBytes(itemDone, "item.arguments", args)
			itemDone, _ = sjson.SetBytes(itemDone, "item.call_id", callID)
			itemDone, _ = sjson.SetBytes(itemDone, "item.name", st.FuncNames[key])
			out = append(out, emitRespEvent("response.output_item.done", itemDone))
			st.FuncItemDone[key] = true
			st.FuncArgsDone[key] = true
		}
	}

	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
		choices.ForEach(func(_, choice gjson.Result) bool {
			idx := int(choice.Get("index").Int())
			delta := choice.Get("delta")
			if delta.Exists() {
				if c := delta.Get("content"); c.Exists() && c.String() != "" {
					if st.ReasoningID != "" {
						stopReasoning(st.ReasoningBuf.String())
						st.ReasoningBuf.Reset()
					}
					if _, exists := st.MsgOutputIx[idx]; !exists {
						st.MsgOutputIx[idx] = allocOutputIndex()
					}
					msgOutputIndex := st.MsgOutputIx[idx]
					itemID := fmt.Sprintf("msg_%s_%d", st.ResponseID, idx)
					if !st.MsgItemAdded[idx] {
						item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"in_progress","content":[],"role":"assistant"}}`)
						item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
						item, _ = sjson.SetBytes(item, "output_index", msgOutputIndex)
						item, _ = sjson.SetBytes(item, "item.id", itemID)
						out = append(out, emitRespEvent("response.output_item.added", item))
						st.MsgItemAdded[idx] = true
					}
					if annotations := delta.Get("annotations"); annotations.Exists() && annotations.IsArray() {
						st.MsgAnnotations[idx] = mergeJSONArrayRaw(st.MsgAnnotations[idx], annotations)
					}
					if logprobs := choice.Get("logprobs.content"); logprobs.Exists() && logprobs.IsArray() {
						if logprobsRaw := buildResponsesTextEventLogprobs(logprobs); len(logprobsRaw) > 0 {
							st.MsgTextLogprobs[idx] = mergeJSONArrayRaw(st.MsgTextLogprobs[idx], gjson.ParseBytes(logprobsRaw))
						}
					}
					if !st.MsgContentAdded[idx] {
						contentIndex := st.MsgNextContentIx[idx]
						part := []byte(`{"type":"response.content_part.added","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
						part, _ = sjson.SetBytes(part, "sequence_number", nextSeq())
						part, _ = sjson.SetBytes(part, "item_id", itemID)
						part, _ = sjson.SetBytes(part, "output_index", msgOutputIndex)
						part, _ = sjson.SetBytes(part, "content_index", contentIndex)
						part, _ = sjson.SetRawBytes(part, "part", buildResponsesOutputTextPart("", st.MsgAnnotations[idx], st.MsgTextLogprobs[idx]))
						out = append(out, emitRespEvent("response.content_part.added", part))
						st.MsgTextContentIx[idx] = contentIndex
						st.MsgNextContentIx[idx] = contentIndex + 1
						st.MsgContentAdded[idx] = true
					}

					msg := []byte(`{"type":"response.output_text.delta","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"delta":"","logprobs":[]}`)
					msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
					msg, _ = sjson.SetBytes(msg, "item_id", itemID)
					msg, _ = sjson.SetBytes(msg, "output_index", msgOutputIndex)
					msg, _ = sjson.SetBytes(msg, "content_index", st.MsgTextContentIx[idx])
					msg, _ = sjson.SetBytes(msg, "delta", c.String())
					if len(st.MsgTextLogprobs[idx]) > 0 {
						msg, _ = sjson.SetRawBytes(msg, "logprobs", st.MsgTextLogprobs[idx])
					}
					out = append(out, emitRespEvent("response.output_text.delta", msg))
					if st.MsgTextBuf[idx] == nil {
						st.MsgTextBuf[idx] = &strings.Builder{}
					}
					st.MsgTextBuf[idx].WriteString(c.String())
				}

				if refusal := delta.Get("refusal"); refusal.Exists() && refusal.String() != "" {
					if st.ReasoningID != "" {
						stopReasoning(st.ReasoningBuf.String())
						st.ReasoningBuf.Reset()
					}
					if _, exists := st.MsgOutputIx[idx]; !exists {
						st.MsgOutputIx[idx] = allocOutputIndex()
					}
					msgOutputIndex := st.MsgOutputIx[idx]
					itemID := fmt.Sprintf("msg_%s_%d", st.ResponseID, idx)
					if !st.MsgItemAdded[idx] {
						item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"in_progress","content":[],"role":"assistant"}}`)
						item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
						item, _ = sjson.SetBytes(item, "output_index", msgOutputIndex)
						item, _ = sjson.SetBytes(item, "item.id", itemID)
						out = append(out, emitRespEvent("response.output_item.added", item))
						st.MsgItemAdded[idx] = true
					}
					if !st.MsgRefusalAdded[idx] {
						contentIndex := st.MsgNextContentIx[idx]
						part := []byte(`{"type":"response.content_part.added","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"refusal","refusal":""}}`)
						part, _ = sjson.SetBytes(part, "sequence_number", nextSeq())
						part, _ = sjson.SetBytes(part, "item_id", itemID)
						part, _ = sjson.SetBytes(part, "output_index", msgOutputIndex)
						part, _ = sjson.SetBytes(part, "content_index", contentIndex)
						part, _ = sjson.SetRawBytes(part, "part", buildResponsesRefusalPart(""))
						out = append(out, emitRespEvent("response.content_part.added", part))
						st.MsgRefusalPartIx[idx] = contentIndex
						st.MsgNextContentIx[idx] = contentIndex + 1
						st.MsgRefusalAdded[idx] = true
					}

					msg := []byte(`{"type":"response.refusal.delta","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"delta":""}`)
					msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
					msg, _ = sjson.SetBytes(msg, "item_id", itemID)
					msg, _ = sjson.SetBytes(msg, "output_index", msgOutputIndex)
					msg, _ = sjson.SetBytes(msg, "content_index", st.MsgRefusalPartIx[idx])
					msg, _ = sjson.SetBytes(msg, "delta", refusal.String())
					out = append(out, emitRespEvent("response.refusal.delta", msg))
					if st.MsgRefusalBuf[idx] == nil {
						st.MsgRefusalBuf[idx] = &strings.Builder{}
					}
					st.MsgRefusalBuf[idx].WriteString(refusal.String())
				}

				if rc := delta.Get("reasoning_content"); rc.Exists() && rc.String() != "" {
					if st.ReasoningID == "" {
						st.ReasoningID = fmt.Sprintf("rs_%s_%d", st.ResponseID, idx)
						st.ReasoningIndex = allocOutputIndex()
						item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"in_progress","summary":[],"content":[]}}`)
						item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
						item, _ = sjson.SetBytes(item, "output_index", st.ReasoningIndex)
						item, _ = sjson.SetBytes(item, "item.id", st.ReasoningID)
						out = append(out, emitRespEvent("response.output_item.added", item))
					}
					st.ReasoningBuf.WriteString(rc.String())
					out = append(out, buildResponsesReasoningDeltaEvent(st.ReasoningID, st.ReasoningIndex, rc.String(), nextSeq))
				}

				if tcs := delta.Get("tool_calls"); tcs.Exists() && tcs.IsArray() {
					if st.ReasoningID != "" {
						stopReasoning(st.ReasoningBuf.String())
						st.ReasoningBuf.Reset()
					}
					emitCompletedMessageEvents(&out, st, idx, nextSeq)

					tcs.ForEach(func(_, tc gjson.Result) bool {
						toolIndex := int(tc.Get("index").Int())
						key := toolStateKey(idx, toolIndex)
						newCallID := tc.Get("id").String()
						nameChunk := tc.Get("function.name").String()
						if nameChunk != "" {
							st.FuncNames[key] = nameChunk
						}

						existingCallID := st.FuncCallIDs[key]
						effectiveCallID := existingCallID
						shouldEmitItem := false
						if existingCallID == "" && newCallID != "" {
							effectiveCallID = newCallID
							st.FuncCallIDs[key] = newCallID
							tool := responsesToolStateFromRequest(originalRequestRawJSON, requestRawJSON, effectiveCallID, st.FuncNames[key], allocOutputIndex())
							st.FuncOutputIx[key] = tool.OutputIndex
							st.FuncCustom[key] = tool.Custom
							st.FuncItemIDs[key] = tool.ItemID
							shouldEmitItem = true
						}

						if shouldEmitItem && effectiveCallID != "" {
							tool := oaiToResponsesStateTool{
								ItemID:      st.FuncItemIDs[key],
								CallID:      effectiveCallID,
								Name:        st.FuncNames[key],
								OutputIndex: st.FuncOutputIx[key],
								Custom:      st.FuncCustom[key],
							}
							out = append(out, buildResponsesToolAddedEvent(tool, nextSeq))
						}

						if st.FuncArgsBuf[key] == nil {
							st.FuncArgsBuf[key] = &strings.Builder{}
						}

						if args := tc.Get("function.arguments"); args.Exists() && args.String() != "" {
							refCallID := st.FuncCallIDs[key]
							if refCallID == "" {
								refCallID = newCallID
							}
							if refCallID != "" {
								tool := oaiToResponsesStateTool{
									ItemID:      st.FuncItemIDs[key],
									CallID:      refCallID,
									Name:        st.FuncNames[key],
									OutputIndex: st.FuncOutputIx[key],
									Custom:      st.FuncCustom[key],
									Input:       args.String(),
								}
								out = append(out, buildResponsesToolDeltaEvent(tool, args.String(), nextSeq))
							}
							st.FuncArgsBuf[key].WriteString(args.String())
						}
						return true
					})
				}
			}

			if finishReason := choice.Get("finish_reason"); finishReason.Exists() && finishReason.String() != "" {
				if st.ReasoningID != "" {
					stopReasoning(st.ReasoningBuf.String())
					st.ReasoningBuf.Reset()
				}

				emitCompletedMessageEvents(&out, st, idx, nextSeq)

				funcKeys := make([]string, 0)
				for key := range st.FuncCallIDs {
					if strings.HasPrefix(key, fmt.Sprintf("%d:", idx)) {
						funcKeys = append(funcKeys, key)
					}
				}
				emitFunctionDone(funcKeys)
				st.CompletionPending = true
			}

			return true
		})
	}

	return out
}

// ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream builds a single Responses JSON
// from a non-streaming OpenAI Chat Completions response.
func ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	root := gjson.ParseBytes(rawJSON)

	resp := []byte(`{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"incomplete_details":null}`)

	id := root.Get("id").String()
	if id == "" {
		id = fmt.Sprintf("resp_%x_%d", time.Now().UnixNano(), atomic.AddUint64(&responseIDCounter, 1))
	}
	resp, _ = sjson.SetBytes(resp, "id", id)

	created := root.Get("created").Int()
	if created == 0 {
		created = time.Now().Unix()
	}
	resp, _ = sjson.SetBytes(resp, "created_at", created)

	resp = echoOpenAIResponsesRequestFields(resp, "", originalRequestRawJSON, requestRawJSON)
	if !gjson.GetBytes(resp, "model").Exists() {
		if v := root.Get("model"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "model", v.String())
		}
	}

	outputsWrapper := []byte(`{"arr":[]}`)
	rcText := gjson.GetBytes(rawJSON, "choices.0.message.reasoning_content").String()
	includeReasoning := rcText != ""
	if !includeReasoning && len(requestRawJSON) > 0 {
		includeReasoning = gjson.GetBytes(requestRawJSON, "reasoning").Exists()
	}
	if includeReasoning {
		rid := id
		if strings.HasPrefix(rid, "resp_") {
			rid = strings.TrimPrefix(rid, "resp_")
		}
		reasoningItem := buildResponsesReasoningItem(fmt.Sprintf("rs_%s", rid), rcText, "")
		outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", reasoningItem)
	}

	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
		choices.ForEach(func(_, choice gjson.Result) bool {
			msg := choice.Get("message")
			if !msg.Exists() {
				return true
			}

			text := msg.Get("content").String()
			refusal := msg.Get("refusal").String()
			annotationsRaw := []byte(nil)
			if annotations := msg.Get("annotations"); annotations.Exists() && annotations.IsArray() {
				annotationsRaw = []byte(annotations.Raw)
			}
			logprobsRaw := []byte(nil)
			if logprobs := choice.Get("logprobs.content"); logprobs.Exists() && logprobs.IsArray() {
				logprobsRaw = buildResponsesTextEventLogprobs(logprobs)
			}
			if text != "" || refusal != "" || len(annotationsRaw) > 0 || len(logprobsRaw) > 0 {
				item := buildResponsesMessageItem(
					fmt.Sprintf("msg_%s_%d", id, int(choice.Get("index").Int())),
					text,
					refusal,
					annotationsRaw,
					logprobsRaw,
				)
				outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", item)
			}

			if tcs := msg.Get("tool_calls"); tcs.Exists() && tcs.IsArray() {
				tcs.ForEach(func(_, tc gjson.Result) bool {
					callID := tc.Get("id").String()
					name := tc.Get("function.name").String()
					args := tc.Get("function.arguments").String()
					tool := responsesToolStateFromRequest(originalRequestRawJSON, requestRawJSON, callID, name, 0)
					tool.Input = args
					outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", tool.withSnapshot())
					return true
				})
			}

			return true
		})
	}

	if gjson.GetBytes(outputsWrapper, "arr.#").Int() > 0 {
		resp, _ = sjson.SetRawBytes(resp, "output", []byte(gjson.GetBytes(outputsWrapper, "arr").Raw))
	}

	if usage := root.Get("usage"); usage.Exists() {
		if usage.Get("prompt_tokens").Exists() || usage.Get("completion_tokens").Exists() || usage.Get("total_tokens").Exists() {
			resp, _ = sjson.SetBytes(resp, "usage.input_tokens", usage.Get("prompt_tokens").Int())
			if d := usage.Get("prompt_tokens_details.cached_tokens"); d.Exists() {
				resp, _ = sjson.SetBytes(resp, "usage.input_tokens_details.cached_tokens", d.Int())
			}
			resp, _ = sjson.SetBytes(resp, "usage.output_tokens", usage.Get("completion_tokens").Int())
			if d := usage.Get("output_tokens_details.reasoning_tokens"); d.Exists() {
				resp, _ = sjson.SetBytes(resp, "usage.output_tokens_details.reasoning_tokens", d.Int())
			}
			resp, _ = sjson.SetBytes(resp, "usage.total_tokens", usage.Get("total_tokens").Int())
		} else {
			resp, _ = sjson.SetBytes(resp, "usage", usage.Value())
		}
	}

	return resp
}
