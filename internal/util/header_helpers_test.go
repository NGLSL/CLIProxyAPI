package util

import (
	"net/http"
	"testing"
)

func TestApplyHeaderMapExcept_ProtectsExistingHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer existing")
	headers.Set("User-Agent", "existing-ua")

	ApplyHeaderMapExcept(headers, map[string]string{
		" Authorization ": "Bearer override",
		"User-Agent":      "override-ua",
		"X-Test":          " value ",
	}, "authorization", " user-agent ")

	if got := headers.Get("Authorization"); got != "Bearer existing" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer existing")
	}
	if got := headers.Get("User-Agent"); got != "existing-ua" {
		t.Fatalf("User-Agent = %q, want %q", got, "existing-ua")
	}
	if got := headers.Get("X-Test"); got != "value" {
		t.Fatalf("X-Test = %q, want %q", got, "value")
	}
}

func TestApplyCustomHeadersFromAttrsExcept_ProtectsExistingHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer existing")
	req.Header.Set("User-Agent", "existing-ua")

	ApplyCustomHeadersFromAttrsExcept(req, map[string]string{
		"header:Authorization": "Bearer override",
		"header:User-Agent":    "override-ua",
		"header:X-Test":        "value",
	}, "authorization", " user-agent ")

	if got := req.Header.Get("Authorization"); got != "Bearer existing" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer existing")
	}
	if got := req.Header.Get("User-Agent"); got != "existing-ua" {
		t.Fatalf("User-Agent = %q, want %q", got, "existing-ua")
	}
	if got := req.Header.Get("X-Test"); got != "value" {
		t.Fatalf("X-Test = %q, want %q", got, "value")
	}
}

func TestApplyCustomHeadersFromAttrsExcept_AllowsOverridesWhenNotProtected(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("User-Agent", "existing-ua")

	ApplyCustomHeadersFromAttrsExcept(req, map[string]string{
		"header:User-Agent": "override-ua",
	}, "Authorization")

	if got := req.Header.Get("User-Agent"); got != "override-ua" {
		t.Fatalf("User-Agent = %q, want %q", got, "override-ua")
	}
}

func TestApplyCustomHeadersFromAttrs_SetsRequestHost(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	ApplyCustomHeadersFromAttrs(req, map[string]string{
		"header:Host": "upstream.example.com",
	})

	if got := req.Host; got != "upstream.example.com" {
		t.Fatalf("Host = %q, want %q", got, "upstream.example.com")
	}
}

func TestApplyHeaderMapToRequestExcept_ProtectsHost(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Host = "existing.example.com"

	ApplyHeaderMapToRequestExcept(req, map[string]string{
		"Host": "override.example.com",
	}, "host")

	if got := req.Host; got != "existing.example.com" {
		t.Fatalf("Host = %q, want %q", got, "existing.example.com")
	}
}
