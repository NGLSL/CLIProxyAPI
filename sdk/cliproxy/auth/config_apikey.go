package auth

import "strings"

// IsConfigAPIKeyAuth reports whether the auth entry is synthesized from config *-api-key lists.
// 仅识别“配置里显式配置的 API Key 账号”，排除 oauth/runtime 注入出来却碰巧带 api_key 字段的条目。
func IsConfigAPIKeyAuth(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if auth.AuthKind() != AuthKindAPIKey {
		return false
	}
	if auth.AuthSourceKind() != AuthSourceConfig {
		// 兼容旧数据：部分历史条目只写了 source=config:...，没有 source_kind。
		if auth.Attributes == nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(auth.Attributes["source"])), "config:") {
			return false
		}
	}
	return strings.TrimSpace(authAttribute(auth, AttributeAPIKey)) != ""
}
