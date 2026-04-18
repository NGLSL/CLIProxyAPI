package helps

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/NGLSL/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

type roundTripperCache struct {
	mu    sync.RWMutex
	items map[string]http.RoundTripper
}

func newRoundTripperCache() *roundTripperCache {
	return &roundTripperCache{items: make(map[string]http.RoundTripper)}
}

func (c *roundTripperCache) Load(key string) http.RoundTripper {
	if c == nil || key == "" {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.items[key]
}

func (c *roundTripperCache) LoadOrStore(key string, candidate http.RoundTripper) http.RoundTripper {
	if c == nil || key == "" || candidate == nil {
		return candidate
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing := c.items[key]; existing != nil {
		return existing
	}
	c.items[key] = candidate
	return candidate
}

var sharedProxyTransports = newRoundTripperCache()

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority)
// 2. Use cfg.ProxyURL if auth proxy is not configured
// 3. Use RoundTripper from context if neither are configured
//
// Parameters:
//   - ctx: The context containing optional RoundTripper
//   - cfg: The application configuration
//   - auth: The authentication information
//   - timeout: The client timeout (0 means no timeout)
//
// Returns:
//   - *http.Client: An HTTP client with configured proxy or transport
func NewProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	httpClient := &http.Client{}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	proxyURL := effectiveProxyURL(cfg, auth)
	if proxyURL != "" {
		transport := getCachedProxyTransport(proxyURL)
		if transport != nil {
			httpClient.Transport = transport
			return httpClient
		}
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyURL)
	}

	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		httpClient.Transport = rt
	}

	return httpClient
}

func effectiveProxyURL(cfg *config.Config, auth *cliproxyauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if cfg != nil {
		return strings.TrimSpace(cfg.ProxyURL)
	}
	return ""
}

func normalizedProxyCacheKey(proxyURL string) string {
	trimmed := strings.TrimSpace(proxyURL)
	if trimmed == "" {
		return ""
	}

	setting, errParse := proxyutil.Parse(trimmed)
	if errParse != nil {
		return trimmed
	}

	switch setting.Mode {
	case proxyutil.ModeInherit:
		return ""
	case proxyutil.ModeDirect:
		return "direct"
	case proxyutil.ModeProxy:
		if setting.URL != nil {
			return setting.URL.String()
		}
	}

	return trimmed
}

func getCachedProxyTransport(proxyURL string) http.RoundTripper {
	cacheKey := normalizedProxyCacheKey(proxyURL)
	if cacheKey == "" {
		return nil
	}
	if transport := sharedProxyTransports.Load(cacheKey); transport != nil {
		return transport
	}
	transport := buildProxyTransport(proxyURL)
	if transport == nil {
		return nil
	}
	return sharedProxyTransports.LoadOrStore(cacheKey, transport)
}

// buildProxyTransport creates an HTTP transport configured for the given proxy URL.
// It supports SOCKS5, HTTP, and HTTPS proxy protocols.
//
// Parameters:
//   - proxyURL: The proxy URL string (e.g., "socks5://user:pass@host:port", "http://host:port")
//
// Returns:
//   - *http.Transport: A configured transport, or nil if the proxy URL is invalid
func buildProxyTransport(proxyURL string) *http.Transport {
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	return transport
}
