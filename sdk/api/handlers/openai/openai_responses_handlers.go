// Package openai provides HTTP handlers for OpenAIResponses API endpoints.
// This package implements the OpenAIResponses-compatible API interface, including model listing
// and chat completion functionality. It supports both streaming and non-streaming responses,
// and manages a pool of clients to interact with backend services.
// The handlers translate OpenAIResponses API requests to the appropriate backend format and
// convert responses back to OpenAIResponses-compatible format.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	. "github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func writeResponsesSSEChunk(w io.Writer, chunk []byte) {
	if w == nil || len(chunk) == 0 {
		return
	}
	if _, err := w.Write(chunk); err != nil {
		return
	}
	if bytes.HasSuffix(chunk, []byte("\n\n")) || bytes.HasSuffix(chunk, []byte("\r\n\r\n")) {
		return
	}
	suffix := []byte("\n\n")
	if bytes.HasSuffix(chunk, []byte("\r\n")) {
		suffix = []byte("\r\n")
	} else if bytes.HasSuffix(chunk, []byte("\n")) {
		suffix = []byte("\n")
	}
	if _, err := w.Write(suffix); err != nil {
		return
	}
}

type responsesSSEFramer struct {
	pending            []byte
	sawTerminalEvent   bool
	terminalResponseID string
}

func (f *responsesSSEFramer) observeFrame(frame []byte) {
	if f == nil || len(frame) == 0 {
		return
	}
	if responsesSSEHasTerminalEvent(frame) {
		f.sawTerminalEvent = true
		if f.terminalResponseID == "" {
			f.terminalResponseID = responsesResponseIDFromChunk(frame)
		}
	}
}

func (f *responsesSSEFramer) WriteChunk(w io.Writer, chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	if responsesSSENeedsLineBreak(f.pending, chunk) {
		f.pending = append(f.pending, '\n')
	}
	f.pending = append(f.pending, chunk...)
	for {
		frameLen := responsesSSEFrameLen(f.pending)
		if frameLen == 0 {
			break
		}
		frame := f.pending[:frameLen]
		f.observeFrame(frame)
		writeResponsesSSEChunk(w, frame)
		copy(f.pending, f.pending[frameLen:])
		f.pending = f.pending[:len(f.pending)-frameLen]
	}
	if len(bytes.TrimSpace(f.pending)) == 0 {
		f.pending = f.pending[:0]
		return
	}
	if len(f.pending) == 0 || !responsesSSECanEmitWithoutDelimiter(f.pending) {
		return
	}
	f.observeFrame(f.pending)
	writeResponsesSSEChunk(w, f.pending)
	f.pending = f.pending[:0]
}

func (f *responsesSSEFramer) Flush(w io.Writer) {
	if len(f.pending) == 0 {
		return
	}
	if len(bytes.TrimSpace(f.pending)) == 0 {
		f.pending = f.pending[:0]
		return
	}
	if !responsesSSECanEmitWithoutDelimiter(f.pending) {
		f.pending = f.pending[:0]
		return
	}
	f.observeFrame(f.pending)
	writeResponsesSSEChunk(w, f.pending)
	f.pending = f.pending[:0]
}

func responsesSSEFrameLen(chunk []byte) int {
	if len(chunk) == 0 {
		return 0
	}
	lf := bytes.Index(chunk, []byte("\n\n"))
	crlf := bytes.Index(chunk, []byte("\r\n\r\n"))
	switch {
	case lf < 0:
		if crlf < 0 {
			return 0
		}
		return crlf + 4
	case crlf < 0:
		return lf + 2
	case lf < crlf:
		return lf + 2
	default:
		return crlf + 4
	}
}

func responsesSSENeedsMoreData(chunk []byte) bool {
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 {
		return false
	}
	return responsesSSEHasField(trimmed, []byte("event:")) && !responsesSSEHasField(trimmed, []byte("data:"))
}

func responsesSSEHasField(chunk []byte, prefix []byte) bool {
	s := chunk
	for len(s) > 0 {
		line := s
		if i := bytes.IndexByte(s, '\n'); i >= 0 {
			line = s[:i]
			s = s[i+1:]
		} else {
			s = nil
		}
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func responsesSSECanEmitWithoutDelimiter(chunk []byte) bool {
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 || responsesSSENeedsMoreData(trimmed) || !responsesSSEHasField(trimmed, []byte("data:")) {
		return false
	}
	return responsesSSEDataLinesValid(trimmed)
}

func responsesSSEDataLinesValid(chunk []byte) bool {
	s := chunk
	for len(s) > 0 {
		line := s
		if i := bytes.IndexByte(s, '\n'); i >= 0 {
			line = s[:i]
			s = s[i+1:]
		} else {
			s = nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		if !json.Valid(data) {
			return false
		}
	}
	return true
}

func responsesSSENeedsLineBreak(pending, chunk []byte) bool {
	if len(pending) == 0 || len(chunk) == 0 {
		return false
	}
	if bytes.HasSuffix(pending, []byte("\n")) || bytes.HasSuffix(pending, []byte("\r")) {
		return false
	}
	if chunk[0] == '\n' || chunk[0] == '\r' {
		return false
	}
	trimmed := bytes.TrimLeft(chunk, " \t")
	if len(trimmed) == 0 {
		return false
	}
	for _, prefix := range [][]byte{[]byte("data:"), []byte("event:"), []byte("id:"), []byte("retry:"), []byte(":")} {
		if bytes.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

func responsesSSEHasTerminalEvent(chunk []byte) bool {
	eventType := ""
	s := chunk
	for len(s) > 0 {
		line := s
		if i := bytes.IndexByte(s, '\n'); i >= 0 {
			line = s[:i]
			s = s[i+1:]
		} else {
			s = nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if bytes.HasPrefix(line, []byte("event:")) {
			eventType = string(bytes.TrimSpace(line[len("event:"):]))
			if eventType == "response.completed" || eventType == "response.incomplete" {
				return true
			}
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 || !json.Valid(data) {
			continue
		}
		typeField := gjson.GetBytes(data, "type").String()
		if typeField == "response.completed" || typeField == "response.incomplete" {
			return true
		}
	}
	return eventType == "response.completed" || eventType == "response.incomplete"
}

func responsesSSETerminalEOFError() *interfaces.ErrorMessage {
	return &interfaces.ErrorMessage{
		StatusCode: http.StatusRequestTimeout,
		Error:      fmt.Errorf("stream closed before response.completed or response.incomplete"),
	}
}

func responsesSSEWriteTerminalError(w io.Writer, errMsg *interfaces.ErrorMessage) {
	if errMsg == nil {
		return
	}
	status := http.StatusInternalServerError
	if errMsg.StatusCode > 0 {
		status = errMsg.StatusCode
	}
	errText := http.StatusText(status)
	if errMsg.Error != nil && errMsg.Error.Error() != "" {
		errText = errMsg.Error.Error()
	}
	chunk := handlers.BuildOpenAIResponsesStreamErrorChunk(status, errText, 0)
	_, _ = fmt.Fprintf(w, "\nevent: error\ndata: %s\n\n", string(chunk))
}

func responsesSSEFinalError(framer *responsesSSEFramer) *interfaces.ErrorMessage {
	if framer != nil && framer.sawTerminalEvent {
		return nil
	}
	return responsesSSETerminalEOFError()
}

func responsesSSEWriteDone(w io.Writer, framer *responsesSSEFramer) *interfaces.ErrorMessage {
	if errMsg := responsesSSEFinalError(framer); errMsg != nil {
		responsesSSEWriteTerminalError(w, errMsg)
		return errMsg
	}
	_, _ = w.Write([]byte("\n"))
	return nil
}

const responsesAffinityCacheTTL = 30 * time.Minute

type responsesAffinityCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]responsesAffinityEntry
}

type responsesAffinityEntry struct {
	authID   string
	lastSeen time.Time
}

func newResponsesAffinityCache(ttl time.Duration) *responsesAffinityCache {
	if ttl <= 0 {
		ttl = responsesAffinityCacheTTL
	}
	return &responsesAffinityCache{
		ttl:     ttl,
		entries: make(map[string]responsesAffinityEntry),
	}
}

func (c *responsesAffinityCache) get(key string) string {
	key = strings.TrimSpace(key)
	if c == nil || key == "" {
		return ""
	}

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(now)

	entry, ok := c.entries[key]
	if !ok || strings.TrimSpace(entry.authID) == "" {
		return ""
	}
	entry.lastSeen = now
	c.entries[key] = entry
	return entry.authID
}

func (c *responsesAffinityCache) record(key string, authID string) {
	key = strings.TrimSpace(key)
	authID = strings.TrimSpace(authID)
	if c == nil || key == "" || authID == "" {
		return
	}

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(now)
	c.entries[key] = responsesAffinityEntry{authID: authID, lastSeen: now}
}

func (c *responsesAffinityCache) delete(key string) {
	key = strings.TrimSpace(key)
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

func (c *responsesAffinityCache) cleanupLocked(now time.Time) {
	if c == nil || c.ttl <= 0 {
		return
	}
	for key, entry := range c.entries {
		if strings.TrimSpace(entry.authID) == "" || now.Sub(entry.lastSeen) > c.ttl {
			delete(c.entries, key)
		}
	}
}

type responsesRequestAffinity struct {
	continuityKey string
	sessionKey    string
	pinnedAuthID  string
	selectedAuth  string
}

type responsesSSECapture struct {
	responseID string
}

type responsesStreamAffinityState struct {
	affinity *responsesRequestAffinity
	capture  *responsesSSECapture
}

func (c *responsesSSECapture) Observe(chunk []byte) {
	if c == nil || len(chunk) == 0 || c.responseID != "" {
		return
	}
	if responseID := responsesResponseIDFromChunk(chunk); responseID != "" {
		c.responseID = responseID
	}
}

var defaultResponsesAffinityCache = newResponsesAffinityCache(0)

// OpenAIResponsesAPIHandler contains the handlers for OpenAIResponses API endpoints.
// It holds a pool of clients to interact with the backend service.
type OpenAIResponsesAPIHandler struct {
	*handlers.BaseAPIHandler
	affinityCache *responsesAffinityCache
}

// NewOpenAIResponsesAPIHandler creates a new OpenAIResponses API handlers instance.
// It takes an BaseAPIHandler instance as input and returns an OpenAIResponsesAPIHandler.
//
// Parameters:
//   - apiHandlers: The base API handlers instance
//
// Returns:
//   - *OpenAIResponsesAPIHandler: A new OpenAIResponses API handlers instance
func NewOpenAIResponsesAPIHandler(apiHandlers *handlers.BaseAPIHandler) *OpenAIResponsesAPIHandler {
	return &OpenAIResponsesAPIHandler{
		BaseAPIHandler: apiHandlers,
		affinityCache:  defaultResponsesAffinityCache,
	}
}

// HandlerType returns the identifier for this handler implementation.
func (h *OpenAIResponsesAPIHandler) HandlerType() string {
	return OpenaiResponse
}

// Models returns the OpenAIResponses-compatible model metadata supported by this handler.
func (h *OpenAIResponsesAPIHandler) Models() []map[string]any {
	// Get dynamic models from the global registry
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("openai")
}

const (
	responsesAffinityKeyPrefixResponse = "response:"
	responsesAffinityKeyPrefixSession  = "session:"
)

func responsesAffinityKey(prefix string, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return prefix + raw
}

func responsesResponseAffinityKey(responseID string) string {
	return responsesAffinityKey(responsesAffinityKeyPrefixResponse, responseID)
}

func responsesSessionAffinityKey(sessionID string) string {
	return responsesAffinityKey(responsesAffinityKeyPrefixSession, sessionID)
}

func responsesContinuityKey(rawJSON []byte) string {
	if key := strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()); key != "" {
		return responsesResponseAffinityKey(key)
	}
	if key := strings.TrimSpace(gjson.GetBytes(rawJSON, "session_id").String()); key != "" {
		return responsesSessionAffinityKey(key)
	}
	return ""
}

func responsesSessionKey(rawJSON []byte) string {
	return strings.TrimSpace(gjson.GetBytes(rawJSON, "session_id").String())
}

func responsesResponseIDFromJSON(rawJSON []byte) string {
	if responseID := strings.TrimSpace(gjson.GetBytes(rawJSON, "response.id").String()); responseID != "" {
		return responseID
	}
	return strings.TrimSpace(gjson.GetBytes(rawJSON, "id").String())
}

func responsesResponseIDFromChunk(chunk []byte) string {
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) || !json.Valid(data) {
			continue
		}
		typ := strings.TrimSpace(gjson.GetBytes(data, "type").String())
		if typ != "response.completed" && typ != "response.incomplete" {
			continue
		}
		if responseID := strings.TrimSpace(gjson.GetBytes(data, "response.id").String()); responseID != "" {
			return responseID
		}
	}
	return ""
}

func (h *OpenAIResponsesAPIHandler) ensureAffinityCache() {
	if h != nil && h.affinityCache == nil {
		h.affinityCache = defaultResponsesAffinityCache
	}
}

func (h *OpenAIResponsesAPIHandler) responsesPinnedAuthID(key string) string {
	h.ensureAffinityCache()
	key = strings.TrimSpace(key)
	if h == nil || h.affinityCache == nil || key == "" {
		return ""
	}
	authID := strings.TrimSpace(h.affinityCache.get(key))
	if authID == "" {
		return ""
	}
	if h.AuthManager == nil {
		return authID
	}
	if auth, ok := h.AuthManager.GetByID(authID); ok && auth != nil {
		return authID
	}
	h.affinityCache.delete(key)
	return ""
}

func (h *OpenAIResponsesAPIHandler) responsesPrepareRequestContext(ctx context.Context, rawJSON []byte) (context.Context, *responsesRequestAffinity) {
	h.ensureAffinityCache()
	affinity := &responsesRequestAffinity{
		continuityKey: responsesContinuityKey(rawJSON),
		sessionKey:    responsesSessionKey(rawJSON),
	}
	if affinity.continuityKey != "" {
		affinity.pinnedAuthID = h.responsesPinnedAuthID(affinity.continuityKey)
	}
	if affinity.pinnedAuthID == "" && affinity.sessionKey != "" {
		affinity.pinnedAuthID = h.responsesPinnedAuthID(responsesSessionAffinityKey(affinity.sessionKey))
	}
	if affinity.pinnedAuthID != "" {
		ctx = handlers.WithPinnedAuthID(ctx, affinity.pinnedAuthID)
	}
	ctx = handlers.WithSelectedAuthIDCallback(ctx, func(authID string) {
		affinity.selectedAuth = strings.TrimSpace(authID)
	})
	if affinity.sessionKey != "" {
		ctx = handlers.WithExecutionSessionID(ctx, affinity.sessionKey)
	}
	return ctx, affinity
}

func (h *OpenAIResponsesAPIHandler) responsesRecordAffinity(affinity *responsesRequestAffinity, responseID string) {
	h.ensureAffinityCache()
	if h == nil || h.affinityCache == nil || affinity == nil {
		return
	}
	authID := strings.TrimSpace(affinity.selectedAuth)
	if authID == "" {
		authID = strings.TrimSpace(affinity.pinnedAuthID)
	}
	if authID == "" {
		return
	}
	if key := strings.TrimSpace(responseID); key != "" {
		h.affinityCache.record(responsesResponseAffinityKey(key), authID)
	}
	if key := strings.TrimSpace(affinity.sessionKey); key != "" {
		h.affinityCache.record(responsesSessionAffinityKey(key), authID)
	}
}

func (h *OpenAIResponsesAPIHandler) responsesRecordAffinityFromJSON(affinity *responsesRequestAffinity, payload []byte) {
	h.responsesRecordAffinity(affinity, responsesResponseIDFromJSON(payload))
}

func (h *OpenAIResponsesAPIHandler) responsesRecordAffinityFromSSE(affinity *responsesRequestAffinity, capture *responsesSSECapture, framer *responsesSSEFramer) {
	if framer != nil && strings.TrimSpace(framer.terminalResponseID) != "" {
		h.responsesRecordAffinity(affinity, framer.terminalResponseID)
		return
	}
	if capture == nil {
		return
	}
	h.responsesRecordAffinity(affinity, capture.responseID)
}

// OpenAIResponsesModels handles the /v1/models endpoint.
// It returns a list of available AI models with their capabilities
// and specifications in OpenAIResponses-compatible format.
func (h *OpenAIResponsesAPIHandler) OpenAIResponsesModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   h.Models(),
	})
}

// Responses handles the /v1/responses endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler based on the model provider.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIResponsesAPIHandler) Responses(c *gin.Context) {
	rawJSON, err := c.GetRawData()
	// If data retrieval fails, return a 400 Bad Request error.
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Check if the client requested a streaming response.
	streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.True {
		h.handleStreamingResponse(c, rawJSON)
	} else {
		h.handleNonStreamingResponse(c, rawJSON)
	}

}

func (h *OpenAIResponsesAPIHandler) Compact(c *gin.Context) {
	rawJSON, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.True {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported for compact responses",
				Type:    "invalid_request_error",
			},
		})
		return
	}
	if streamResult.Exists() {
		if updated, err := sjson.DeleteBytes(rawJSON, "stream"); err == nil {
			rawJSON = updated
		}
	}

	c.Header("Content-Type", "application/json")
	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "responses/compact")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

// handleNonStreamingResponse handles non-streaming chat completion responses
// for Gemini models. It selects a client from the pool, sends the request, and
// aggregates the response before sending it back to the client in OpenAIResponses format.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	cliCtx, affinity := h.responsesPrepareRequestContext(cliCtx, rawJSON)
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)

	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	h.responsesRecordAffinityFromJSON(affinity, resp)
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

// handleStreamingResponse handles streaming responses for Gemini models.
// It establishes a streaming connection with the backend service and forwards
// the response chunks to the client in real-time using Server-Sent Events.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleStreamingResponse(c *gin.Context, rawJSON []byte) {
	// Get the http.Flusher interface to manually flush the response.
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	cliCtx, affinity := h.responsesPrepareRequestContext(cliCtx, rawJSON)
	capture := &responsesSSECapture{}
	dataChan, upstreamHeaders, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")

	setSSEHeaders := func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
	}
	framer := &responsesSSEFramer{}

	// Peek at the first chunk
	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case errMsg, ok := <-errChan:
			if !ok {
				// Err channel closed cleanly; wait for data channel.
				errChan = nil
				continue
			}
			// Upstream failed immediately. Return proper error status and JSON.
			h.WriteErrorResponse(c, errMsg)
			if errMsg != nil {
				cliCancel(errMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				// A clean close without a terminal Responses event is still a terminal stream error.
				setSSEHeaders()
				handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
				errMsg := responsesSSEWriteDone(c.Writer, framer)
				flusher.Flush()
				if errMsg != nil {
					cliCancel(errMsg.Error)
					return
				}
				cliCancel(nil)
				return
			}

			// Success! Set headers.
			setSSEHeaders()
			handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)

			capture.Observe(chunk)
			framer.WriteChunk(c.Writer, chunk)
			flusher.Flush()

			state := &responsesStreamAffinityState{affinity: affinity, capture: capture}
			h.forwardResponsesStream(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan, framer, state)
			return
		}
	}
}

func (h *OpenAIResponsesAPIHandler) forwardResponsesStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage, framer *responsesSSEFramer, state ...*responsesStreamAffinityState) {
	if framer == nil {
		framer = &responsesSSEFramer{}
	}
	var affinity *responsesRequestAffinity
	var capture *responsesSSECapture
	if len(state) > 0 && state[0] != nil {
		affinity = state[0].affinity
		capture = state[0].capture
	}
	h.ForwardStream(c, flusher, cancel, data, errs, handlers.StreamForwardOptions{
		WriteChunk: func(chunk []byte) {
			if capture != nil {
				capture.Observe(chunk)
			}
			framer.WriteChunk(c.Writer, chunk)
		},
		WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {
			framer.Flush(c.Writer)
			responsesSSEWriteTerminalError(c.Writer, errMsg)
		},
		WriteDone: func() {
			framer.Flush(c.Writer)
			if errMsg := responsesSSEWriteDone(c.Writer, framer); errMsg == nil {
				h.responsesRecordAffinityFromSSE(affinity, capture, framer)
			}
		},
	})
}
