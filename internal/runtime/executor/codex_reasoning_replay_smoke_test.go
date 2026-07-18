package executor

import (
	"context"
	"encoding/base64"
	"net/http"
	"testing"

	internalcache "github.com/NGLSL/CLIProxyAPI/v7/internal/cache"
	cliproxyexecutor "github.com/NGLSL/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/NGLSL/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func validCodexReasoningEncryptedContentForSmoke(seed byte) string {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	for i := 9; i < len(payload); i++ {
		payload[i] = seed + byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func TestSmokeCodexReasoningReplayCumulativeToolTurns(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	scope := codexReasoningReplayScope{
		modelName:  "gpt-5.4",
		sessionKey: "claude:session-cumulative-tools:agent:main",
	}
	firstEncrypted := validCodexReasoningEncryptedContentForSmoke(21)
	secondEncrypted := validCodexReasoningEncryptedContentForSmoke(22)
	cacheCodexReasoningReplayFromCompleted(scope, []byte(`{"response":{"output":[`+
		`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+firstEncrypted+`"},`+
		`{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"first\"}"}`+
		`]}}`))
	cacheCodexReasoningReplayFromCompleted(scope, []byte(`{"response":{"output":[`+
		`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+secondEncrypted+`"},`+
		`{"type":"function_call","call_id":"call_2","name":"lookup","arguments":"{\"q\":\"second\"}"}`+
		`]}}`))

	body := []byte(`{"model":"gpt-5.4","input":[` +
		`{"type":"message","role":"user","content":"first"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"one"},` +
		`{"type":"message","role":"user","content":"second"},` +
		`{"type":"function_call_output","call_id":"call_2","output":"two"},` +
		`{"type":"message","role":"user","content":"third"}` +
		`]}`)

	items, ok := internalcache.GetCodexReasoningReplayItems(scope.modelName, scope.sessionKey)
	if !ok {
		t.Fatal("expected cumulative cache items")
	}
	updated, ok := insertCodexReasoningReplayTurns(body, items)
	if !ok {
		t.Fatalf("insert failed, body=%s", body)
	}
	wantTypes := []string{"message", "reasoning", "function_call", "function_call_output", "message", "reasoning", "function_call", "function_call_output", "message"}
	gotItems := gjson.GetBytes(updated, "input").Array()
	if len(gotItems) != len(wantTypes) {
		t.Fatalf("input length = %d, want %d; body=%s", len(gotItems), len(wantTypes), updated)
	}
	for index, wantType := range wantTypes {
		if gotType := gotItems[index].Get("type").String(); gotType != wantType {
			t.Fatalf("input.%d.type = %q, want %q; body=%s", index, gotType, wantType, updated)
		}
	}
	if gotItems[1].Get("encrypted_content").String() != firstEncrypted || gotItems[5].Get("encrypted_content").String() != secondEncrypted {
		t.Fatalf("cumulative reasoning was not restored in turn order: %s", updated)
	}
}

func TestSmokeCodexReasoningReplayAgentIsolation(t *testing.T) {
	from := sdktranslator.FromString("claude")
	req := cliproxyexecutor.Request{
		Model:   "local-alias-high",
		Payload: []byte(`{"model":"local-alias","messages":[{"role":"user","content":"next"}]}`),
	}
	body := []byte(`{"model":"gpt-5.4","prompt_cache_key":"shared-client-key","input":[{"type":"message","role":"user","content":"next"}]}`)
	rootHeaders := http.Header{}
	rootHeaders.Set("X-Claude-Code-Session-Id", "session-agents")
	childAHeaders := rootHeaders.Clone()
	childAHeaders.Set("X-Claude-Code-Agent-Id", "agent-a")
	childBHeaders := rootHeaders.Clone()
	childBHeaders.Set("X-Claude-Code-Agent-Id", "agent-b")
	metadata := map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "shared-execution-session"}

	root := codexReasoningReplayScopeFromRequest(context.Background(), from, req, cliproxyexecutor.Options{SourceFormat: from, Headers: rootHeaders, Metadata: metadata}, body)
	childA := codexReasoningReplayScopeFromRequest(context.Background(), from, req, cliproxyexecutor.Options{SourceFormat: from, Headers: childAHeaders, Metadata: metadata}, body)
	childB := codexReasoningReplayScopeFromRequest(context.Background(), from, req, cliproxyexecutor.Options{SourceFormat: from, Headers: childBHeaders, Metadata: metadata}, body)
	if root.sessionKey == "" || childA.sessionKey == "" || childB.sessionKey == "" {
		t.Fatalf("empty session keys: root=%#v a=%#v b=%#v", root, childA, childB)
	}
	if root.sessionKey == childA.sessionKey || childA.sessionKey == childB.sessionKey || root.sessionKey == childB.sessionKey {
		t.Fatalf("agent replay scopes are not isolated: root=%#v a=%#v b=%#v", root, childA, childB)
	}
}
