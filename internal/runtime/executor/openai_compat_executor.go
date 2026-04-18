package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/config"
	"github.com/NGLSL/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/NGLSL/CLIProxyAPI/v6/internal/thinking"
	"github.com/NGLSL/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/NGLSL/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

// OpenAICompatExecutor implements a stateless executor for OpenAI-compatible providers.
// It performs request/response translation and executes against the provider base URL
// using per-auth credentials (API key) and per-auth HTTP transport (proxy) from context.
type OpenAICompatExecutor struct {
	provider string
	cfg      *config.Config
}

// NewOpenAICompatExecutor creates an executor bound to a provider key (e.g., "openrouter").
func NewOpenAICompatExecutor(provider string, cfg *config.Config) *OpenAICompatExecutor {
	return &OpenAICompatExecutor{provider: provider, cfg: cfg}
}

// Identifier implements cliproxyauth.ProviderExecutor.
func (e *OpenAICompatExecutor) Identifier() string { return e.provider }

// PrepareRequest injects OpenAI-compatible credentials into the outgoing HTTP request.
func (e *OpenAICompatExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, apiKey := e.resolveCredentials(auth)
	e.prepareUpstreamRequest(req, auth, apiKey)
	return nil
}

func (e *OpenAICompatExecutor) applyConfigHeaders(req *http.Request, auth *cliproxyauth.Auth) {
	if req == nil {
		return
	}
	compat := e.resolveCompatConfig(auth)
	if compat == nil || len(compat.Headers) == 0 {
		return
	}
	util.ApplyHeaderMapExcept(req.Header, compat.Headers, "Authorization")
}

func (e *OpenAICompatExecutor) applyGlobalForwardHeaders(req *http.Request) {
	if req == nil || e == nil || e.cfg == nil {
		return
	}
	util.ApplyHeaderMapExcept(req.Header, e.cfg.ForwardRequestHeaders, "Authorization")
}

func (e *OpenAICompatExecutor) applyAuthHeaders(req *http.Request, auth *cliproxyauth.Auth) {
	if req == nil {
		return
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrsExcept(req, attrs, "Authorization")
}

func (e *OpenAICompatExecutor) applyUpstreamHeaders(req *http.Request, auth *cliproxyauth.Auth) {
	e.applyAuthHeaders(req, auth)
	e.applyGlobalForwardHeaders(req)
	e.applyConfigHeaders(req, auth)
}

func (e *OpenAICompatExecutor) prepareUpstreamRequest(req *http.Request, auth *cliproxyauth.Auth, apiKey string) {
	if req == nil {
		return
	}
	e.applyUpstreamHeaders(req, auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

func cloneValues(values url.Values) url.Values {
	if len(values) == 0 {
		return nil
	}
	cloned := make(url.Values, len(values))
	for key, items := range values {
		cloned[key] = append([]string(nil), items...)
	}
	return cloned
}

func mergeQueryValues(dst, src url.Values) url.Values {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(url.Values, len(src))
	}
	for key, items := range src {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		dst.Del(trimmedKey)
		for _, item := range items {
			dst.Add(trimmedKey, item)
		}
	}
	return dst
}

func mergeForwardHeaders(dst, src http.Header, protectedKeys ...string) http.Header {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(http.Header, len(src))
	}
	protected := make(map[string]struct{}, len(protectedKeys))
	for _, key := range protectedKeys {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(key))
		if canonical != "" {
			protected[canonical] = struct{}{}
		}
	}
	for key, items := range src {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(key))
		if canonical == "" {
			continue
		}
		if _, blocked := protected[canonical]; blocked {
			continue
		}
		dst.Del(canonical)
		for _, item := range items {
			trimmed := strings.TrimSpace(item)
			if trimmed == "" {
				continue
			}
			dst.Add(canonical, trimmed)
		}
	}
	return dst
}

func mergeJSONObjects(base []byte, extra gjson.Result) ([]byte, error) {
	if !extra.Exists() || !extra.IsObject() {
		return base, nil
	}
	if len(base) == 0 {
		base = []byte(`{}`)
	}
	result := base
	var mergeErr error
	extra.ForEach(func(key, value gjson.Result) bool {
		result, mergeErr = sjson.SetRawBytes(result, key.String(), []byte(value.Raw))
		return mergeErr == nil
	})
	if mergeErr != nil {
		return base, mergeErr
	}
	return result, nil
}

func extraHeadersFromPayload(payload []byte) http.Header {
	extraHeaders := gjson.GetBytes(payload, "extra_headers")
	if !extraHeaders.Exists() || !extraHeaders.IsObject() {
		return nil
	}
	headers := make(http.Header)
	extraHeaders.ForEach(func(key, value gjson.Result) bool {
		name := http.CanonicalHeaderKey(strings.TrimSpace(key.String()))
		if name == "" {
			return true
		}
		switch {
		case value.IsArray():
			for _, item := range value.Array() {
				text := strings.TrimSpace(item.String())
				if text != "" {
					headers.Add(name, text)
				}
			}
		default:
			text := strings.TrimSpace(value.String())
			if text != "" {
				headers.Add(name, text)
			}
		}
		return true
	})
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func extraQueryFromPayload(payload []byte) url.Values {
	extraQuery := gjson.GetBytes(payload, "extra_query")
	if !extraQuery.Exists() || !extraQuery.IsObject() {
		return nil
	}
	query := make(url.Values)
	extraQuery.ForEach(func(key, value gjson.Result) bool {
		name := strings.TrimSpace(key.String())
		if name == "" {
			return true
		}
		switch {
		case value.IsArray():
			for _, item := range value.Array() {
				query.Add(name, item.String())
			}
		default:
			query.Add(name, value.String())
		}
		return true
	})
	if len(query) == 0 {
		return nil
	}
	return query
}

func stripOpenAICompatExtras(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}
	for _, path := range []string{"extra_headers", "extra_query", "extra_body", "metadata"} {
		updated, errDelete := sjson.DeleteBytes(payload, path)
		if errDelete != nil {
			continue
		}
		payload = updated
	}
	return payload
}

func prepareOpenAICompatPayload(payload []byte, originalPayload []byte) ([]byte, http.Header, url.Values, error) {
	prepared := payload
	if len(prepared) == 0 {
		prepared = []byte(`{}`)
	}
	extraHeaders := extraHeadersFromPayload(originalPayload)
	extraQuery := extraQueryFromPayload(originalPayload)
	prepared = stripOpenAICompatExtras(prepared)
	prepared, err := mergeJSONObjects(prepared, gjson.GetBytes(originalPayload, "extra_body"))
	if err != nil {
		return nil, nil, nil, err
	}
	if serviceTier := gjson.GetBytes(originalPayload, "service_tier"); serviceTier.Exists() {
		prepared, err = sjson.SetRawBytes(prepared, "service_tier", []byte(serviceTier.Raw))
		if err != nil {
			return nil, nil, nil, err
		}
	}
	return prepared, extraHeaders, extraQuery, nil
}

// HttpRequest injects OpenAI-compatible credentials into the request and executes it.
func (e *OpenAICompatExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("openai compat executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	_, apiKey := e.resolveCredentials(auth)
	e.prepareUpstreamRequest(httpReq, auth, apiKey)
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *OpenAICompatExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	endpoint := "/chat/completions"
	if opts.Alt == "responses/compact" {
		to = sdktranslator.FromString("openai-response")
		endpoint = "/responses/compact"
	}
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, opts.Stream)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, opts.Stream)
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	translated = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel)
	if opts.Alt == "responses/compact" {
		if updated, errDelete := sjson.DeleteBytes(translated, "stream"); errDelete == nil {
			translated = updated
		}
	}

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}
	translated, extraHeaders, extraQuery, err := prepareOpenAICompatPayload(translated, originalPayload)
	if err != nil {
		return resp, fmt.Errorf("openai compat executor: prepare payload: %w", err)
	}

	upstreamURL := strings.TrimSuffix(baseURL, "/") + endpoint
	parsedURL, err := url.Parse(upstreamURL)
	if err != nil {
		return resp, fmt.Errorf("openai compat executor: parse upstream url: %w", err)
	}
	query := cloneValues(opts.Query)
	query = mergeQueryValues(query, extraQuery)
	if len(query) > 0 {
		parsedURL.RawQuery = query.Encode()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, parsedURL.String(), bytes.NewReader(translated))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	httpReq.Header = mergeForwardHeaders(httpReq.Header, opts.Headers, "Authorization", "Content-Type", "User-Agent")
	httpReq.Header = mergeForwardHeaders(httpReq.Header, extraHeaders, "Authorization", "Content-Type", "User-Agent")
	e.prepareUpstreamRequest(httpReq, auth, apiKey)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       parsedURL.String(),
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	// Ensure we at least record the request even if upstream doesn't return usage
	reporter.EnsurePublished(ctx)
	// Translate response back to source format when needed
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *OpenAICompatExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	translated = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	// Request usage data in the final streaming chunk so that token statistics
	// are captured even when the upstream is an OpenAI-compatible provider.
	translated, _ = sjson.SetBytes(translated, "stream_options.include_usage", true)
	translated, extraHeaders, extraQuery, err := prepareOpenAICompatPayload(translated, originalPayload)
	if err != nil {
		return nil, fmt.Errorf("openai compat executor: prepare payload: %w", err)
	}

	upstreamURL := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	parsedURL, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, fmt.Errorf("openai compat executor: parse upstream url: %w", err)
	}
	query := cloneValues(opts.Query)
	query = mergeQueryValues(query, extraQuery)
	if len(query) > 0 {
		parsedURL.RawQuery = query.Encode()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, parsedURL.String(), bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	httpReq.Header = mergeForwardHeaders(httpReq.Header, opts.Headers, "Authorization", "Content-Type", "User-Agent", "Accept", "Cache-Control")
	httpReq.Header = mergeForwardHeaders(httpReq.Header, extraHeaders, "Authorization", "Content-Type", "User-Agent", "Accept", "Cache-Control")
	e.prepareUpstreamRequest(httpReq, auth, apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       parsedURL.String(),
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseOpenAIStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			if len(line) == 0 {
				continue
			}

			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}

			// OpenAI-compatible streams are SSE: lines typically prefixed with "data: ".
			// Pass through translator; it yields one or more chunks for the target schema.
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(line), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		} else {
			// In case the upstream close the stream without a terminal [DONE] marker.
			// Feed a synthetic done marker through the translator so pending
			// response.completed events are still emitted exactly once.
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}
			}
		}
		// Ensure we record the request if no usage chunk was ever seen
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenAICompatExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	modelForCounting := baseModel

	translated, err := thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	enc, err := helps.TokenizerForModel(modelForCounting)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: tokenizer init failed: %w", err)
	}

	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: token counting failed: %w", err)
	}

	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

// Refresh is a no-op for API-key based compatibility providers.
func (e *OpenAICompatExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("openai compat executor: refresh called")
	_ = ctx
	return auth, nil
}

func (e *OpenAICompatExecutor) resolveCredentials(auth *cliproxyauth.Auth) (baseURL, apiKey string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
	}
	return
}

func (e *OpenAICompatExecutor) resolveCompatConfig(auth *cliproxyauth.Auth) *config.OpenAICompatibility {
	if auth == nil || e.cfg == nil {
		return nil
	}
	candidates := make([]string, 0, 3)
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["compat_name"]); v != "" {
			candidates = append(candidates, v)
		}
		if v := strings.TrimSpace(auth.Attributes["provider_key"]); v != "" {
			candidates = append(candidates, v)
		}
	}
	if v := strings.TrimSpace(auth.Provider); v != "" {
		candidates = append(candidates, v)
	}
	for i := range e.cfg.OpenAICompatibility {
		compat := &e.cfg.OpenAICompatibility[i]
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), compat.Name) {
				return compat
			}
		}
	}
	return nil
}

func (e *OpenAICompatExecutor) overrideModel(payload []byte, model string) []byte {
	if len(payload) == 0 || model == "" {
		return payload
	}
	payload, _ = sjson.SetBytes(payload, "model", model)
	return payload
}

type statusErr struct {
	code       int
	msg        string
	retryAfter *time.Duration
}

func (e statusErr) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("status %d", e.code)
}
func (e statusErr) StatusCode() int            { return e.code }
func (e statusErr) RetryAfter() *time.Duration { return e.retryAfter }
