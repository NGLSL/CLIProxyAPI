package util

import (
	"net/http"
	"strings"
)

// ApplyHeaderMap applies the provided header map to the target header collection.
func ApplyHeaderMap(target http.Header, headers map[string]string) {
	applyHeaderMap(target, headers, nil)
}

// ApplyHeaderMapExcept applies the provided header map while preserving protected headers.
func ApplyHeaderMapExcept(target http.Header, headers map[string]string, protectedKeys ...string) {
	applyHeaderMap(target, headers, buildProtectedHeaderSet(protectedKeys...))
}

// ApplyHeaderMapToRequest applies the provided header map to an HTTP request.
func ApplyHeaderMapToRequest(r *http.Request, headers map[string]string) {
	applyHeaderMapToRequest(r, headers, nil)
}

// ApplyHeaderMapToRequestExcept applies the provided header map to an HTTP request while preserving protected headers.
func ApplyHeaderMapToRequestExcept(r *http.Request, headers map[string]string, protectedKeys ...string) {
	applyHeaderMapToRequest(r, headers, buildProtectedHeaderSet(protectedKeys...))
}

// ApplyCustomHeadersFromAttrs applies user-defined headers stored in the provided attributes map.
// Custom headers override existing request headers when conflicts occur.
func ApplyCustomHeadersFromAttrs(r *http.Request, attrs map[string]string) {
	if r == nil {
		return
	}
	ApplyHeaderMapToRequest(r, extractCustomHeaders(attrs))
}

// ApplyCustomHeadersFromAttrsExcept applies user-defined headers while preserving protected headers.
func ApplyCustomHeadersFromAttrsExcept(r *http.Request, attrs map[string]string, protectedKeys ...string) {
	if r == nil {
		return
	}
	ApplyHeaderMapToRequestExcept(r, extractCustomHeaders(attrs), protectedKeys...)
}

func extractCustomHeaders(attrs map[string]string) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	headers := make(map[string]string)
	for k, v := range attrs {
		if !strings.HasPrefix(k, "header:") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(k, "header:"))
		if name == "" {
			continue
		}
		val := strings.TrimSpace(v)
		if val == "" {
			continue
		}
		headers[name] = val
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func buildProtectedHeaderSet(keys ...string) map[string]struct{} {
	if len(keys) == 0 {
		return nil
	}
	protected := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(key))
		if canonical == "" {
			continue
		}
		protected[canonical] = struct{}{}
	}
	if len(protected) == 0 {
		return nil
	}
	return protected
}

func applyHeaderMap(target http.Header, headers map[string]string, protected map[string]struct{}) {
	if target == nil || len(headers) == 0 {
		return
	}
	for k, v := range headers {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(k))
		if canonical == "" {
			continue
		}
		val := strings.TrimSpace(v)
		if val == "" {
			continue
		}
		if _, blocked := protected[canonical]; blocked {
			continue
		}
		target.Set(canonical, val)
	}
}

func applyHeaderMapToRequest(r *http.Request, headers map[string]string, protected map[string]struct{}) {
	if r == nil || len(headers) == 0 {
		return
	}
	for k, v := range headers {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(k))
		if canonical == "" {
			continue
		}
		val := strings.TrimSpace(v)
		if val == "" {
			continue
		}
		if _, blocked := protected[canonical]; blocked {
			continue
		}
		if canonical == "Host" {
			r.Host = val
			continue
		}
		r.Header.Set(canonical, val)
	}
}
