package helps

import (
	"context"
	"net/http"
	"testing"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/NGLSL/CLIProxyAPI/v6/sdk/config"
)

func TestNewProxyAwareHTTPClientDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "direct"},
		0,
	)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestNewProxyAwareHTTPClientReusesExplicitProxyTransport(t *testing.T) {
	t.Parallel()

	auth := &cliproxyauth.Auth{ProxyURL: "http://shared-proxy.example.com:8080"}
	clientA := NewProxyAwareHTTPClient(context.Background(), nil, auth, 0)
	clientB := NewProxyAwareHTTPClient(context.Background(), nil, auth, 0)

	if clientA.Transport == nil || clientB.Transport == nil {
		t.Fatal("expected explicit proxy transport to be configured")
	}
	if clientA.Transport != clientB.Transport {
		t.Fatal("expected clients with the same explicit proxy to reuse transport")
	}
}

func TestNewProxyAwareHTTPClientDirectAndNoneReuseTransport(t *testing.T) {
	t.Parallel()

	clientDirect := NewProxyAwareHTTPClient(context.Background(), nil, &cliproxyauth.Auth{ProxyURL: "direct"}, 0)
	clientNone := NewProxyAwareHTTPClient(context.Background(), nil, &cliproxyauth.Auth{ProxyURL: "none"}, 0)

	if clientDirect.Transport == nil || clientNone.Transport == nil {
		t.Fatal("expected direct proxy transports to be configured")
	}
	if clientDirect.Transport != clientNone.Transport {
		t.Fatal("expected direct and none proxy settings to reuse the same transport")
	}
}

func TestNewProxyAwareHTTPClientUsesContextRoundTripperWithoutExplicitProxy(t *testing.T) {
	t.Parallel()

	ctxTransport := &http.Transport{}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(ctxTransport))
	client := NewProxyAwareHTTPClient(ctx, nil, nil, 0)

	if client.Transport != ctxTransport {
		t.Fatal("expected context round tripper to be used when no explicit proxy is configured")
	}
}
