package helps

import (
	"testing"
	"time"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/NGLSL/CLIProxyAPI/v6/sdk/config"
)

func TestNewUtlsHTTPClientReusesTransportForInheritedProxySetting(t *testing.T) {
	t.Parallel()

	clientA := NewUtlsHTTPClient(nil, nil, time.Second)
	clientB := NewUtlsHTTPClient(nil, nil, 2*time.Second)

	if clientA.Transport == nil || clientB.Transport == nil {
		t.Fatal("expected uTLS transport to be configured")
	}
	if clientA.Transport != clientB.Transport {
		t.Fatal("expected inherited proxy setting to reuse the same uTLS transport")
	}
	if clientA.Timeout != time.Second {
		t.Fatalf("clientA timeout = %v, want %v", clientA.Timeout, time.Second)
	}
	if clientB.Timeout != 2*time.Second {
		t.Fatalf("clientB timeout = %v, want %v", clientB.Timeout, 2*time.Second)
	}
}

func TestNewUtlsHTTPClientReusesTransportForSameExplicitProxy(t *testing.T) {
	t.Parallel()

	auth := &cliproxyauth.Auth{ProxyURL: "http://shared-proxy.example.com:8080"}
	clientA := NewUtlsHTTPClient(nil, auth, 0)
	clientB := NewUtlsHTTPClient(nil, auth, 0)

	if clientA.Transport == nil || clientB.Transport == nil {
		t.Fatal("expected uTLS transport to be configured")
	}
	if clientA.Transport != clientB.Transport {
		t.Fatal("expected matching explicit proxy settings to reuse the same uTLS transport")
	}
}

func TestNewUtlsHTTPClientSeparatesDifferentProxySettings(t *testing.T) {
	t.Parallel()

	clientA := NewUtlsHTTPClient(nil, &cliproxyauth.Auth{ProxyURL: "http://proxy-a.example.com:8080"}, 0)
	clientB := NewUtlsHTTPClient(
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://proxy-b.example.com:8080"}},
		nil,
		0,
	)

	if clientA.Transport == nil || clientB.Transport == nil {
		t.Fatal("expected uTLS transport to be configured")
	}
	if clientA.Transport == clientB.Transport {
		t.Fatal("expected different proxy settings to use different uTLS transports")
	}
}
