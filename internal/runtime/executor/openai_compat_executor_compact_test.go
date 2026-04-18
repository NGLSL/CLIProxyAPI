package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/NGLSL/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorPrepareRequestProtectsAuthorization(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":              "provider-token",
		"header:Authorization": "Bearer client-token",
		"header:X-Test":        "client-value",
	}}
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})

	if err := executor.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer provider-token" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer provider-token")
	}
	if got := req.Header.Get("X-Test"); got != "client-value" {
		t.Fatalf("X-Test = %q, want %q", got, "client-value")
	}
}

func TestOpenAICompatExecutorPrepareRequestAppliesGlobalHeadersWithConfigPrecedence(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("X-Test", "client-value")
	req.Header.Set("Authorization", "Bearer client-token")
	executor := NewOpenAICompatExecutor("openrouter", &config.Config{SDKConfig: config.SDKConfig{
		ForwardRequestHeaders: map[string]string{
			"Authorization": "Bearer global-token",
			"X-Test":        "global-value",
		},
	}, OpenAICompatibility: []config.OpenAICompatibility{{
		Name:    "openrouter",
		BaseURL: "https://example.com",
		Headers: map[string]string{"X-Test": "config-value"},
	}}})
	auth := &cliproxyauth.Auth{Provider: "openrouter", Attributes: map[string]string{
		"base_url":             "https://example.com",
		"api_key":              "provider-token",
		"compat_name":          "openrouter",
		"provider_key":         "openrouter",
		"header:Authorization": "Bearer attr-token",
		"header:X-Test":        "auth-value",
	}}

	if err := executor.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer provider-token" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer provider-token")
	}
	if got := req.Header.Get("X-Test"); got != "config-value" {
		t.Fatalf("X-Test = %q, want %q", got, "config-value")
	}
}

func TestOpenAICompatExecutorHttpRequestPrefersConfigHeaders(t *testing.T) {
	var gotAuthorization string
	var gotCustom string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Test")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openrouter", &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:    "openrouter",
		BaseURL: server.URL,
		Headers: map[string]string{"X-Test": "config-value"},
	}}})
	auth := &cliproxyauth.Auth{Provider: "openrouter", Attributes: map[string]string{
		"base_url":             server.URL,
		"api_key":              "provider-token",
		"compat_name":          "openrouter",
		"provider_key":         "openrouter",
		"header:Authorization": "Bearer client-token",
		"header:X-Test":        "client-value",
	}}
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	if _, err := executor.HttpRequest(context.Background(), auth, req); err != nil {
		t.Fatalf("HttpRequest() error = %v", err)
	}
	if gotAuthorization != "Bearer provider-token" {
		t.Fatalf("Authorization = %q, want %q", gotAuthorization, "Bearer provider-token")
	}
	if gotCustom != "config-value" {
		t.Fatalf("X-Test = %q, want %q", gotCustom, "config-value")
	}
}

func TestOpenAICompatExecutorHttpRequestAppliesGlobalHeadersWhenConfigDoesNotOverride(t *testing.T) {
	var gotAuthorization string
	var gotCustom string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Test")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openrouter", &config.Config{SDKConfig: config.SDKConfig{
		ForwardRequestHeaders: map[string]string{
			"Authorization": "Bearer global-token",
			"X-Test":        "global-value",
		},
	}, OpenAICompatibility: []config.OpenAICompatibility{{
		Name:    "openrouter",
		BaseURL: server.URL,
	}}})
	auth := &cliproxyauth.Auth{Provider: "openrouter", Attributes: map[string]string{
		"base_url":             server.URL,
		"api_key":              "provider-token",
		"compat_name":          "openrouter",
		"provider_key":         "openrouter",
		"header:Authorization": "Bearer auth-token",
		"header:X-Test":        "auth-value",
	}}
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("X-Test", "client-value")

	if _, err := executor.HttpRequest(context.Background(), auth, req); err != nil {
		t.Fatalf("HttpRequest() error = %v", err)
	}
	if gotAuthorization != "Bearer provider-token" {
		t.Fatalf("Authorization = %q, want %q", gotAuthorization, "Bearer provider-token")
	}
	if gotCustom != "global-value" {
		t.Fatalf("X-Test = %q, want %q", gotCustom, "global-value")
	}
}

func TestOpenAICompatExecutorExecutePrefersConfigHeaders(t *testing.T) {
	var gotAuthorization string
	var gotCustom string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Test")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openrouter", &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:    "openrouter",
		BaseURL: server.URL,
		Headers: map[string]string{"X-Test": "config-value"},
	}}})
	auth := &cliproxyauth.Auth{Provider: "openrouter", Attributes: map[string]string{
		"base_url":             server.URL,
		"api_key":              "provider-token",
		"compat_name":          "openrouter",
		"provider_key":         "openrouter",
		"header:Authorization": "Bearer client-token",
		"header:X-Test":        "client-value",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-4.1",
		Payload: []byte(`{"model":"gpt-4.1","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotAuthorization != "Bearer provider-token" {
		t.Fatalf("Authorization = %q, want %q", gotAuthorization, "Bearer provider-token")
	}
	if gotCustom != "config-value" {
		t.Fatalf("X-Test = %q, want %q", gotCustom, "config-value")
	}
}

func TestOpenAICompatExecutorExecuteStreamPrefersConfigHeaders(t *testing.T) {
	var gotAuthorization string
	var gotCustom string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Test")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openrouter", &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:    "openrouter",
		BaseURL: server.URL,
		Headers: map[string]string{"X-Test": "config-value"},
	}}})
	auth := &cliproxyauth.Auth{Provider: "openrouter", Attributes: map[string]string{
		"base_url":             server.URL,
		"api_key":              "provider-token",
		"compat_name":          "openrouter",
		"provider_key":         "openrouter",
		"header:Authorization": "Bearer client-token",
		"header:X-Test":        "client-value",
	}}

	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-4.1",
		Payload: []byte(`{"model":"gpt-4.1","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for range stream.Chunks {
	}
	if gotAuthorization != "Bearer provider-token" {
		t.Fatalf("Authorization = %q, want %q", gotAuthorization, "Bearer provider-token")
	}
	if gotCustom != "config-value" {
		t.Fatalf("X-Test = %q, want %q", gotCustom, "config-value")
	}
}

func TestOpenAICompatExecutorCompactPassthrough(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"gpt-5.1-codex-max","input":[{"role":"user","content":"hi"}]}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.1-codex-max",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/responses/compact" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/responses/compact")
	}
	if !gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("expected input in body")
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected messages in body")
	}
	if string(resp.Payload) != `{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}` {
		t.Fatalf("payload = %s", string(resp.Payload))
	}
}

func TestOpenAICompatExecutorExecutePassesThroughExtraFieldsAndRequestMetadata(t *testing.T) {
	var gotPath string
	var gotQuery string
	var gotClientHeader string
	var gotExtraHeader string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotClientHeader = r.Header.Get("X-Client")
		gotExtraHeader = r.Header.Get("X-Extra")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{
		"model":"gpt-4.1",
		"messages":[{"role":"user","content":"hi"}],
		"metadata":{"trace_id":"trace-123","tenant":"acme"},
		"service_tier":"priority",
		"extra_headers":{"X-Extra":"extra-value"},
		"extra_query":{"provider":"openrouter","tags":["a","b"]},
		"extra_body":{"seed":7,"user":"abc"}
	}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-4.1",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: payload,
		Headers:         http.Header{"X-Client": []string{"client-value"}},
		Query:           url.Values{"client": []string{"1"}, "provider": []string{"client-provider"}},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("Execute() returned empty payload")
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/chat/completions")
	}
	if gotQuery != "client=1&provider=openrouter&tags=a&tags=b" {
		t.Fatalf("query = %q, want %q", gotQuery, "client=1&provider=openrouter&tags=a&tags=b")
	}
	if gotClientHeader != "client-value" {
		t.Fatalf("X-Client = %q, want %q", gotClientHeader, "client-value")
	}
	if gotExtraHeader != "extra-value" {
		t.Fatalf("X-Extra = %q, want %q", gotExtraHeader, "extra-value")
	}
	if got := gjson.GetBytes(gotBody, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want %q", got, "priority")
	}
	if got := gjson.GetBytes(gotBody, "seed").Int(); got != 7 {
		t.Fatalf("seed = %d, want %d", got, 7)
	}
	if got := gjson.GetBytes(gotBody, "user").String(); got != "abc" {
		t.Fatalf("user = %q, want %q", got, "abc")
	}
	if gjson.GetBytes(gotBody, "extra_headers").Exists() {
		t.Fatalf("extra_headers unexpectedly present in upstream body")
	}
	if gjson.GetBytes(gotBody, "extra_query").Exists() {
		t.Fatalf("extra_query unexpectedly present in upstream body")
	}
	if gjson.GetBytes(gotBody, "extra_body").Exists() {
		t.Fatalf("extra_body unexpectedly present in upstream body")
	}
	if gjson.GetBytes(gotBody, "metadata").Exists() {
		t.Fatalf("metadata unexpectedly present in upstream body")
	}
}
