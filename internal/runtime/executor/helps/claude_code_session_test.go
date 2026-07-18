package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestExtractClaudeCodeSessionIDFromPayloadJSON(t *testing.T) {
	payload := []byte("{\"metadata\":{\"user_id\":\"{\\\"device_id\\\":\\\"d\\\",\\\"session_id\\\":\\\"cache-session-1\\\"}\"}}")
	got := ExtractClaudeCodeSessionID(context.Background(), payload, nil)
	if got != "cache-session-1" {
		t.Fatalf("ExtractClaudeCodeSessionID() = %q, want cache-session-1", got)
	}
}

func TestExtractClaudeCodeSessionIDFromHeader(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	ginCtx.Request.Header.Set(ClaudeCodeSessionHeader, "header-session-1")
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	got := ExtractClaudeCodeSessionID(ctx, []byte(`{"model":"gpt-5.4"}`), nil)
	if got != "header-session-1" {
		t.Fatalf("ExtractClaudeCodeSessionID() = %q, want header-session-1", got)
	}
}

func TestExtractClaudeCodeSessionIDPrefersHeaderOverPayload(t *testing.T) {
	payload := []byte("{\"metadata\":{\"user_id\":\"{\\\"session_id\\\":\\\"payload-session\\\"}\"}}")
	headers := http.Header{}
	headers.Set(ClaudeCodeSessionHeader, "header-session")

	got := ExtractClaudeCodeSessionID(context.Background(), payload, headers)
	if got != "header-session" {
		t.Fatalf("ExtractClaudeCodeSessionID() = %q, want header-session", got)
	}
}

func TestClaudeCodeExecutionScopeIsolatesAgents(t *testing.T) {
	rootHeaders := http.Header{}
	rootHeaders.Set(ClaudeCodeSessionHeader, "session-agents")

	childAHeaders := http.Header{}
	childAHeaders.Set(ClaudeCodeSessionHeader, "session-agents")
	childAHeaders.Set(ClaudeCodeAgentHeader, "agent-a")

	childBHeaders := http.Header{}
	childBHeaders.Set(ClaudeCodeSessionHeader, "session-agents")
	childBHeaders.Set(ClaudeCodeAgentHeader, "agent-b")

	rootScope, ok := ClaudeCodeExecutionScope(context.Background(), nil, rootHeaders)
	if !ok || rootScope != "claude:session-agents:agent:main" {
		t.Fatalf("root scope = %q ok=%v", rootScope, ok)
	}
	childAScope, ok := ClaudeCodeExecutionScope(context.Background(), nil, childAHeaders)
	if !ok || childAScope != "claude:session-agents:agent:agent-a" {
		t.Fatalf("childA scope = %q ok=%v", childAScope, ok)
	}
	childBScope, ok := ClaudeCodeExecutionScope(context.Background(), nil, childBHeaders)
	if !ok || childBScope != "claude:session-agents:agent:agent-b" {
		t.Fatalf("childB scope = %q ok=%v", childBScope, ok)
	}
	if rootScope == childAScope || childAScope == childBScope {
		t.Fatalf("agent scopes collided: root=%q a=%q b=%q", rootScope, childAScope, childBScope)
	}
}

func TestClaudeCodeExecutionScopeAcceptsLowercaseHeaderMapKeys(t *testing.T) {
	headers := http.Header{
		"x-claude-code-session-id": []string{"lower-session"},
		"x-claude-code-agent-id":   []string{"lower-agent"},
	}
	scope, ok := ClaudeCodeExecutionScope(context.Background(), nil, headers)
	if !ok || scope != "claude:lower-session:agent:lower-agent" {
		t.Fatalf("scope = %q ok=%v", scope, ok)
	}
}

func TestClaudeCodePromptCacheDeterministicAndAgentScoped(t *testing.T) {
	ctx := context.Background()
	payload := []byte("{\"metadata\":{\"user_id\":\"{\\\"session_id\\\":\\\"cache-session-2\\\"}\"}}")

	rootHeaders := http.Header{}
	rootHeaders.Set(ClaudeCodeSessionHeader, "cache-session-2")

	childHeaders := http.Header{}
	childHeaders.Set(ClaudeCodeSessionHeader, "cache-session-2")
	childHeaders.Set(ClaudeCodeAgentHeader, "agent-a")

	first, ok, err := ClaudeCodePromptCache(ctx, "grok-composer-2.5-fast", payload, rootHeaders)
	if err != nil || !ok || first.ID == "" {
		t.Fatalf("root first = %#v ok=%v err=%v", first, ok, err)
	}
	second, ok, err := ClaudeCodePromptCache(ctx, "grok-composer-2.5-fast", payload, rootHeaders)
	if err != nil || !ok || second.ID != first.ID {
		t.Fatalf("root second id = %q want %q err=%v", second.ID, first.ID, err)
	}

	child, ok, err := ClaudeCodePromptCache(ctx, "grok-composer-2.5-fast", payload, childHeaders)
	if err != nil || !ok || child.ID == "" {
		t.Fatalf("child = %#v ok=%v err=%v", child, ok, err)
	}
	if child.ID == first.ID {
		t.Fatalf("child agent reused root prompt cache id %q", child.ID)
	}
}
