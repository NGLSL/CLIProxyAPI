// Package xai provides OAuth2 authentication helpers for xAI Grok.
package xai

import "time"

const (
	// DefaultAPIBaseURL is the default xAI Grok CLI chat proxy base URL.
	// Grok CLI OAuth 授权后，实际的 Responses API 请求需要发往 cli-chat-proxy.grok.com 而非 api.x.ai。
	DefaultAPIBaseURL = "https://cli-chat-proxy.grok.com/v1"
	// XAITokenAuthHeader 是 Grok CLI 代理网关要求的令牌认证标识头，固定值 xai-grok-cli。
	XAITokenAuthHeaderKey = "X-XAI-Token-Auth"
	XAITokenAuthHeaderValue = "xai-grok-cli"
	// GrokClientVersionHeader 用于声明 Grok CLI 客户端版本，上游网关依赖此值做协议兼容。
	GrokClientVersionHeaderKey = "x-grok-client-version"
	GrokClientVersionHeaderValue = "0.2.93"
	// Issuer is xAI's OAuth issuer.
	Issuer = "https://auth.x.ai"
	// DiscoveryURL is the OIDC discovery endpoint used to resolve OAuth endpoints.
	DiscoveryURL = Issuer + "/.well-known/openid-configuration"
	// ClientID is the public xAI Grok CLI OAuth client ID.
	ClientID = "b1a00492-073a-47ea-816f-4c329264a828"
	// Scope is the OAuth scope set required for xAI API access.
	Scope = "openid profile email offline_access grok-cli:access api:access"
	// RedirectHost is the loopback host used by xAI OAuth.
	RedirectHost = "127.0.0.1"
	// CallbackPort is the preferred loopback callback port.
	CallbackPort = 56121
	// RedirectPath is the loopback callback path registered by the xAI client.
	RedirectPath = "/callback"
)

var refreshLead = 5 * time.Minute

// RefreshLead returns the refresh lead time for xAI OAuth credentials.
func RefreshLead() time.Duration {
	return refreshLead
}

// PKCECodes holds the PKCE verifier/challenge pair.
type PKCECodes struct {
	CodeVerifier  string
	CodeChallenge string
}

// AuthorizeURLParams contains the values used to build the xAI OAuth URL.
type AuthorizeURLParams struct {
	AuthorizationEndpoint string
	RedirectURI           string
	CodeChallenge         string
	State                 string
	Nonce                 string
}

// Discovery contains OAuth endpoints resolved from xAI OIDC discovery.
type Discovery struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

// TokenData holds xAI OAuth token data.
type TokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	Expire       string `json:"expired,omitempty"`
	Email        string `json:"email,omitempty"`
	Subject      string `json:"sub,omitempty"`
}

// AuthBundle aggregates token data and OAuth metadata for persistence.
type AuthBundle struct {
	TokenData     TokenData
	LastRefresh   string
	BaseURL       string
	RedirectURI   string
	TokenEndpoint string
}
