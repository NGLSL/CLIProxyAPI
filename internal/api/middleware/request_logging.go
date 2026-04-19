// Package middleware provides HTTP middleware components for the CLI Proxy API server.
// This file contains the request logging middleware that captures comprehensive
// request and response data when enabled through configuration.
package middleware

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/logging"
	internalusage "github.com/NGLSL/CLIProxyAPI/v6/internal/usage"
	"github.com/NGLSL/CLIProxyAPI/v6/internal/util"
	"github.com/gin-gonic/gin"
)

const maxErrorOnlyCapturedRequestBodyBytes int64 = 1 << 20 // 1 MiB
const requestLogBodyPrefixLimit = nonStreamingSuccessResponseLogBodyLimit

// UsageMetricsMiddleware installs a lightweight response wrapper used only for
// request-scoped usage aggregation. It keeps downstream byte and chunk metrics
// available even when request logging middleware is not mounted.
func UsageMetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		internalusage.EnsureRequestMetrics(c)
		if _, ok := c.Writer.(*ResponseWriterWrapper); !ok {
			wrapper := NewResponseWriterWrapper(c.Writer, nil, nil)
			wrapper.ginContext = c
			c.Writer = wrapper
		}
		c.Next()
	}
}

// RequestLoggingMiddleware creates a Gin middleware that logs HTTP requests and responses.
// It captures detailed information about the request and response, including headers and body,
// and uses the provided RequestLogger to record this data. When full request logging is disabled,
// body capture is limited to small known-size payloads to avoid large per-request memory spikes.
func RequestLoggingMiddleware(logger logging.RequestLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if logger == nil {
			c.Next()
			return
		}

		if shouldSkipMethodForRequestLogging(c.Request) {
			c.Next()
			return
		}

		path := c.Request.URL.Path
		if !shouldLogRequest(path) {
			c.Next()
			return
		}

		loggerEnabled := logger.IsEnabled()

		// Capture request information
		requestInfo, err := captureRequestInfo(c, shouldCaptureRequestBody(loggerEnabled, c.Request))
		if err != nil {
			// Log error but continue processing
			// In a real implementation, you might want to use a proper logger here
			c.Next()
			return
		}

		// Create response writer wrapper
		internalusage.EnsureRequestMetrics(c)
		wrapper := NewResponseWriterWrapper(c.Writer, logger, requestInfo)
		wrapper.ginContext = c
		if !loggerEnabled {
			wrapper.logOnErrorOnly = true
		}
		c.Writer = wrapper

		// Process the request
		c.Next()

		// Finalize logging after request processing
		if err = wrapper.Finalize(c); err != nil {
			// Log error but don't interrupt the response
			// In a real implementation, you might want to use a proper logger here
		}
	}
}

func shouldSkipMethodForRequestLogging(req *http.Request) bool {
	if req == nil {
		return true
	}
	if req.Method != http.MethodGet {
		return false
	}
	return !isResponsesWebsocketUpgrade(req)
}

func isResponsesWebsocketUpgrade(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	if req.URL.Path != "/v1/responses" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(req.Header.Get("Upgrade")), "websocket")
}

func shouldCaptureRequestBody(loggerEnabled bool, req *http.Request) bool {
	if loggerEnabled {
		return true
	}
	if req == nil || req.Body == nil {
		return false
	}
	contentType := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		return false
	}
	if req.ContentLength <= 0 {
		return false
	}
	return req.ContentLength <= maxErrorOnlyCapturedRequestBodyBytes
}

// captureRequestInfo extracts relevant information from the incoming HTTP request.
// It captures the URL, method, headers, and a bounded request body prefix. The consumed
// prefix is then stitched back in front of the original body so downstream handlers can
// still read the complete request payload.
func captureRequestInfo(c *gin.Context, captureBody bool) (*RequestInfo, error) {
	// Capture URL with sensitive query parameters masked
	maskedQuery := util.MaskSensitiveQuery(c.Request.URL.RawQuery)
	url := c.Request.URL.Path
	if maskedQuery != "" {
		url += "?" + maskedQuery
	}

	// Capture method
	method := c.Request.Method

	// Capture headers
	headers := make(map[string][]string)
	for key, values := range c.Request.Header {
		headers[key] = values
	}

	// Capture request body
	var body []byte
	bodyTruncated := false
	if captureBody && c.Request.Body != nil {
		consumedPrefix, bodyPrefix, truncated, err := captureRequestBodyPrefix(c.Request.Body, requestLogBodyPrefixLimit)
		if err != nil {
			return nil, err
		}
		c.Request.Body = newReplayedRequestBody(consumedPrefix, c.Request.Body)
		body = bodyPrefix
		bodyTruncated = truncated
	}

	return &RequestInfo{
		URL:           url,
		Method:        method,
		Headers:       headers,
		Body:          body,
		BodyTruncated: bodyTruncated,
		RequestID:     logging.GetGinRequestID(c),
		Timestamp:     time.Now(),
	}, nil
}

func captureRequestBodyPrefix(body io.ReadCloser, limit int) ([]byte, []byte, bool, error) {
	if body == nil || limit < 0 {
		return nil, nil, false, nil
	}

	readLimit := int64(limit) + 1
	consumedPrefix, err := io.ReadAll(io.LimitReader(body, readLimit))
	if err != nil {
		return nil, nil, false, fmt.Errorf("read request body prefix: %w", err)
	}
	if len(consumedPrefix) <= limit {
		return consumedPrefix, consumedPrefix, false, nil
	}
	return consumedPrefix, consumedPrefix[:limit], true, nil
}

func newReplayedRequestBody(consumedPrefix []byte, originalBody io.ReadCloser) io.ReadCloser {
	return &replayedRequestBody{
		reader: io.MultiReader(bytes.NewReader(consumedPrefix), originalBody),
		body:   originalBody,
	}
}

type replayedRequestBody struct {
	reader io.Reader
	body   io.ReadCloser
}

func (r *replayedRequestBody) Read(p []byte) (int, error) {
	if r == nil || r.reader == nil {
		return 0, io.EOF
	}
	return r.reader.Read(p)
}

func (r *replayedRequestBody) Close() error {
	if r == nil || r.body == nil {
		return nil
	}
	return r.body.Close()
}

// shouldLogRequest determines whether the request should be logged.
// It skips management endpoints to avoid leaking secrets but allows
// all other routes, including module-provided ones, to honor request-log.
func shouldLogRequest(path string) bool {
	if strings.HasPrefix(path, "/v0/management") || strings.HasPrefix(path, "/management") {
		return false
	}

	if strings.HasPrefix(path, "/api") {
		return strings.HasPrefix(path, "/api/provider")
	}

	return true
}
