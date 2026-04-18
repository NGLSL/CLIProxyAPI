package handlers

import (
	"net/http"
	"testing"
)

func TestFilterUpstreamHeaders_RemovesConnectionScopedHeaders(t *testing.T) {
	src := http.Header{}
	src.Add("Connection", "keep-alive, x-hop-a, x-hop-b")
	src.Add("Connection", "x-hop-c")
	src.Set("Keep-Alive", "timeout=5")
	src.Set("X-Hop-A", "a")
	src.Set("X-Hop-B", "b")
	src.Set("X-Hop-C", "c")
	src.Set("X-Request-Id", "req-1")
	src.Set("Set-Cookie", "session=secret")

	filtered := FilterUpstreamHeaders(src)
	if filtered == nil {
		t.Fatalf("expected filtered headers, got nil")
	}

	requestID := filtered.Get("X-Request-Id")
	if requestID != "req-1" {
		t.Fatalf("expected X-Request-Id to be preserved, got %q", requestID)
	}

	blockedHeaderKeys := []string{
		"Connection",
		"Keep-Alive",
		"X-Hop-A",
		"X-Hop-B",
		"X-Hop-C",
		"Set-Cookie",
	}
	for _, key := range blockedHeaderKeys {
		value := filtered.Get(key)
		if value != "" {
			t.Fatalf("expected %s to be removed, got %q", key, value)
		}
	}
}

func TestFilterUpstreamHeaders_ReturnsNilWhenAllHeadersBlocked(t *testing.T) {
	src := http.Header{}
	src.Add("Connection", "x-hop-a")
	src.Set("X-Hop-A", "a")
	src.Set("Set-Cookie", "session=secret")

	filtered := FilterUpstreamHeaders(src)
	if filtered != nil {
		t.Fatalf("expected nil when all headers are filtered, got %#v", filtered)
	}
}

func TestFilterUpstreamHeaders_PreservesGatewayAndBusinessHeaders(t *testing.T) {
	src := http.Header{}
	src.Set("X-LiteLLM-Model-Id", "litellm-1")
	src.Set("Helicone-RateLimit-Remaining", "9")
	src.Set("X-Request-Id", "req-1")
	src.Set("Content-Length", "123")
	src.Set("Content-Encoding", "gzip")

	filtered := FilterUpstreamHeaders(src)
	if filtered == nil {
		t.Fatalf("expected filtered headers, got nil")
	}
	if got := filtered.Get("X-LiteLLM-Model-Id"); got != "litellm-1" {
		t.Fatalf("X-LiteLLM-Model-Id = %q, want %q", got, "litellm-1")
	}
	if got := filtered.Get("Helicone-RateLimit-Remaining"); got != "9" {
		t.Fatalf("Helicone-RateLimit-Remaining = %q, want %q", got, "9")
	}
	if got := filtered.Get("X-Request-Id"); got != "req-1" {
		t.Fatalf("X-Request-Id = %q, want %q", got, "req-1")
	}
	if got := filtered.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want empty", got)
	}
	if got := filtered.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty", got)
	}
}
