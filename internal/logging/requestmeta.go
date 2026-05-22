package logging

import (
	"context"
	"net/http"
	"sync"
)

type responseHeadersKey struct{}

type responseHeadersHolder struct {
	mu      sync.RWMutex
	headers http.Header
}

// WithResponseHeadersHolder attaches a mutable upstream response header holder to ctx.
func WithResponseHeadersHolder(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if holder, ok := ctx.Value(responseHeadersKey{}).(*responseHeadersHolder); ok && holder != nil {
		return ctx
	}
	return context.WithValue(ctx, responseHeadersKey{}, &responseHeadersHolder{})
}

// SetResponseHeaders stores a cloned snapshot of the latest upstream response headers.
func SetResponseHeaders(ctx context.Context, headers http.Header) {
	if ctx == nil {
		return
	}
	holder, ok := ctx.Value(responseHeadersKey{}).(*responseHeadersHolder)
	if !ok || holder == nil {
		return
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()
	holder.headers = CloneHTTPHeader(headers)
}

// GetResponseHeaders returns a cloned snapshot of the latest upstream response headers.
func GetResponseHeaders(ctx context.Context) http.Header {
	if ctx == nil {
		return nil
	}
	holder, ok := ctx.Value(responseHeadersKey{}).(*responseHeadersHolder)
	if !ok || holder == nil {
		return nil
	}
	holder.mu.RLock()
	defer holder.mu.RUnlock()
	return CloneHTTPHeader(holder.headers)
}

// CloneHTTPHeader returns a deep copy of headers for safe storage outside request code.
func CloneHTTPHeader(src http.Header) http.Header {
	if len(src) == 0 {
		return nil
	}
	dst := make(http.Header, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}
