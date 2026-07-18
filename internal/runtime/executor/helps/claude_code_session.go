package helps

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	// ClaudeCodeSessionHeader 是 Claude Code 根会话 ID 请求头。
	// 同一个对话窗口内的主 agent / 子 agent 通常共享该值。
	ClaudeCodeSessionHeader = "X-Claude-Code-Session-Id"

	// ClaudeCodeAgentHeader 是 Claude Code 当前 agent 身份请求头。
	// 子代理会带独立 agent-id；缺失时按主 agent（main）处理。
	ClaudeCodeAgentHeader = "X-Claude-Code-Agent-Id"

	// ClaudeCodeMainAgentID 是未声明 agent 头时的稳定哨兵值。
	// 保证“根会话主代理”始终落在固定分桶，而不是空字符串导致 key 漂移。
	ClaudeCodeMainAgentID = "main"
)

var claudeCodeSessionSuffixPattern = regexp.MustCompile(`_session_([a-f0-9-]+)$`)

// ExtractClaudeCodeSessionID 解析 Claude Code 会话 ID。
// 优先顺序：显式请求头 X-Claude-Code-Session-Id -> gin 请求头 -> payload metadata.user_id。
func ExtractClaudeCodeSessionID(ctx context.Context, payload []byte, headers http.Header) string {
	if sessionID := claudeCodeHeader(ctx, headers, ClaudeCodeSessionHeader); sessionID != "" {
		return sessionID
	}
	return extractClaudeCodeSessionIDFromPayload(payload)
}

// ExtractClaudeCodeAgentID 解析 Claude Code agent ID。
// 有头用头；无头回落 main，确保同会话不同 agent 能被稳定区分。
func ExtractClaudeCodeAgentID(ctx context.Context, headers http.Header) string {
	if agentID := claudeCodeHeader(ctx, headers, ClaudeCodeAgentHeader); agentID != "" {
		return agentID
	}
	return ClaudeCodeMainAgentID
}

// ClaudeCodeExecutionScope 生成“根会话 + agent”执行作用域。
// 返回形如：claude:{sessionID}:agent:{agentID}
//
// 用途链路：
//  1. ClaudeCodePromptCache 用它生成上游 prompt_cache_key，避免子 agent 复用主 agent 缓存；
//  2. codex reasoning replay 用它作为 sessionKey，避免多 agent 回放串台；
//  3. 同 session 不同 agent 并行时，两边状态分桶隔离。
func ClaudeCodeExecutionScope(ctx context.Context, payload []byte, headers http.Header) (string, bool) {
	sessionID := ExtractClaudeCodeSessionID(ctx, payload, headers)
	if sessionID == "" {
		return "", false
	}
	return "claude:" + sessionID + ":agent:" + ExtractClaudeCodeAgentID(ctx, headers), true
}

// claudeCodeHeader 读取 Claude Code 相关请求头。
// 同时兼容：调用方直接传入的 headers、以及 gin.Context 中的原始 HTTP 请求头。
func claudeCodeHeader(ctx context.Context, headers http.Header, name string) string {
	if value := headerValueCaseInsensitive(headers, name); value != "" {
		return value
	}
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			return headerValueCaseInsensitive(ginCtx.Request.Header, name)
		}
	}
	return ""
}

// headerValueCaseInsensitive 做大小写不敏感的 header 读取。
// 某些中间层会把 header key 归一成小写 map key，不能只依赖 http.Header.Get。
func headerValueCaseInsensitive(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	if value := strings.TrimSpace(headers.Get(name)); value != "" {
		return value
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}

func extractClaudeCodeSessionIDFromPayload(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	userID := gjson.GetBytes(payload, "metadata.user_id").String()
	if userID == "" {
		return ""
	}
	if matches := claudeCodeSessionSuffixPattern.FindStringSubmatch(userID); len(matches) >= 2 {
		return matches[1]
	}
	if len(userID) > 0 && userID[0] == '{' {
		return strings.TrimSpace(gjson.Get(userID, "session_id").String())
	}
	return ""
}

// ClaudeCodePromptCache 为单个 Claude Code agent 生成确定性的上游 prompt_cache_key。
//
// 旧实现只按 session 分桶，同会话子 agent 会撞同一 cache；
// 现在改为 session+agent 作用域 + SHA1 确定性 ID：
//   - 同 agent 多次请求稳定复用；
//   - 不同 agent 天然隔离；
//   - 不再依赖进程内随机 UUID 缓存表。
func ClaudeCodePromptCache(ctx context.Context, modelName string, payload []byte, headers http.Header) (CodexCache, bool, error) {
	modelName = strings.TrimSpace(modelName)
	executionScope, ok := ClaudeCodeExecutionScope(ctx, payload, headers)
	if modelName == "" || !ok {
		return CodexCache{}, false, nil
	}
	identity := strings.Join([]string{"cli-proxy-api:codex:claude-code", modelName, executionScope}, "\x00")
	return CodexCache{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity)).String()}, true, nil
}
