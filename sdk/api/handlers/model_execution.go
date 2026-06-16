package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/NGLSL/CLIProxyAPI/v7/internal/interfaces"
)

// ModelExecutionRequest describes an internal model execution request issued by
// plugin-host callbacks. EntryProtocol and ExitProtocol carry SDK translator
// names such as "openai" or "gemini". When both protocols match (the common
// case) the call behaves like a same-protocol request. When they differ, the
// entry protocol is used as the source format because the local executor path
// is single-protocol for now; protocol translation across the public API is
// handled by the regular HTTP entrypoints.
//
// Headers and Query allow plugin callers to supply explicit forward values
// which take precedence over anything derived from the caller's context.
//
// SkipInterceptorPluginID is retained for forwards-compatibility with plugins
// built against the upstream ABI; it is currently a no-op on this fork because
// per-request plugin interceptors are not yet wired into the executor pipeline.
type ModelExecutionRequest struct {
	EntryProtocol           string
	ExitProtocol            string
	Model                   string
	Stream                  bool
	Body                    []byte
	Headers                 http.Header
	Query                   url.Values
	Alt                     string
	SkipInterceptorPluginID string
}

// ModelExecutionResponse describes a non-streaming internal model execution
// response returned to plugin callers.
type ModelExecutionResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// ModelExecutionStream describes a streaming internal model execution response.
// Chunks must be drained by the caller; the channel is closed when the stream
// ends or aborts with a terminal error.
type ModelExecutionStream struct {
	StatusCode int
	Headers    http.Header
	Chunks     <-chan ModelExecutionChunk
}

// ModelExecutionChunk carries either a streaming payload or a terminal stream
// error. When Err is non-nil the chunk is the final one for the stream.
type ModelExecutionChunk struct {
	Payload []byte
	Err     *ModelExecutionStreamError
}

// ModelExecutionStreamError carries a terminal streaming error produced while
// serving a ModelExecutionStream. It mirrors the most relevant fields of
// interfaces.ErrorMessage so plugin callers can preserve status codes and
// response headers when relaying errors.
type ModelExecutionStreamError struct {
	StatusCode int
	Headers    http.Header
	Err        error
}

// Error implements the error interface.
func (e *ModelExecutionStreamError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return "model execution stream error"
}

// modelExecutionForwardKey carries caller-supplied forward headers and query
// parameters through a context.Context. The values are consulted by the regular
// requestForwardOptions path so that plugin-issued executions can override the
// headers/query that would otherwise be derived from a gin context.
type modelExecutionForwardKey struct{}

type modelExecutionForwardValues struct {
	headers http.Header
	query   url.Values
}

// WithModelExecutionForward returns a copy of ctx that carries explicit forward
// headers and query parameters for the duration of a plugin-issued model
// execution. Passing nil values is supported and clears the override.
func WithModelExecutionForward(ctx context.Context, headers http.Header, query url.Values) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, modelExecutionForwardKey{}, modelExecutionForwardValues{
		headers: headers,
		query:   query,
	})
}

// modelExecutionForwardFromContext returns the override values attached via
// WithModelExecutionForward. The boolean is false when no override is set.
func modelExecutionForwardFromContext(ctx context.Context) (http.Header, url.Values, bool) {
	if ctx == nil {
		return nil, nil, false
	}
	values, ok := ctx.Value(modelExecutionForwardKey{}).(modelExecutionForwardValues)
	if !ok {
		return nil, nil, false
	}
	return values.headers, values.query, true
}

// ExecuteModel performs a non-streaming internal model execution. It is the
// entry point used by the plugin host model-execution callback. Plugin-supplied
// headers/query take precedence over values derived from the caller's context.
// The method is safe to call concurrently with regular API traffic.
func (h *BaseAPIHandler) ExecuteModel(ctx context.Context, req ModelExecutionRequest) (ModelExecutionResponse, *interfaces.ErrorMessage) {
	if req.Stream {
		return ModelExecutionResponse{}, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      errors.New("ExecuteModel called with Stream=true; use ExecuteModelStream instead"),
		}
	}
	handlerType := req.EntryProtocol
	if handlerType == "" {
		handlerType = req.ExitProtocol
	}
	execCtx := WithModelExecutionForward(ctx, req.Headers, req.Query)
	body, headers, errMsg := h.executeWithAuthManager(execCtx, handlerType, req.Model, cloneBytes(req.Body), req.Alt, false)
	if errMsg != nil {
		return ModelExecutionResponse{}, errMsg
	}
	return ModelExecutionResponse{
		StatusCode: http.StatusOK,
		Headers:    cloneHeader(headers),
		Body:       cloneBytes(body),
	}, nil
}

// ExecuteModelStream performs a streaming internal model execution. The
// returned stream channel must be drained by the caller; the channel is closed
// after the terminal chunk (payload or error) has been emitted.
func (h *BaseAPIHandler) ExecuteModelStream(ctx context.Context, req ModelExecutionRequest) (ModelExecutionStream, *interfaces.ErrorMessage) {
	if !req.Stream {
		return ModelExecutionStream{}, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      errors.New("ExecuteModelStream called with Stream=false; use ExecuteModel instead"),
		}
	}
	handlerType := req.EntryProtocol
	if handlerType == "" {
		handlerType = req.ExitProtocol
	}
	execCtx := WithModelExecutionForward(ctx, req.Headers, req.Query)
	rawChunks, headers, errChan := h.executeStreamWithAuthManager(execCtx, handlerType, req.Model, cloneBytes(req.Body), req.Alt, false)

	wrapped := make(chan ModelExecutionChunk, 1)
	stream := ModelExecutionStream{
		StatusCode: http.StatusOK,
		Headers:    cloneHeader(headers),
		Chunks:     wrapped,
	}

	// Bridge the raw []byte chunk stream into ModelExecutionChunk values and
	// surface any terminal error from errChan as the final chunk. The goroutine
	// keeps the lifetime of the underlying channels decoupled from the caller.
	go func() {
		defer close(wrapped)
		for chunk := range rawChunks {
			select {
			case <-ctx.Done():
				return
			case wrapped <- ModelExecutionChunk{Payload: cloneBytes(chunk)}:
			}
		}
		// Drain the error channel non-blocking first; if nothing is ready the
		// stream ended cleanly. We still wait once on the channel because the
		// producer closes rawChunks before sending the terminal error.
		if err, ok := <-errChan; ok && err != nil {
			streamErr := &ModelExecutionStreamError{
				StatusCode: err.StatusCode,
				Headers:    cloneHeader(err.Addon),
				Err:        err.Error,
			}
			select {
			case <-ctx.Done():
				return
			case wrapped <- ModelExecutionChunk{Err: streamErr}:
			}
		}
	}()

	return stream, nil
}
