package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestShouldSkipMethodForRequestLogging(t *testing.T) {
	tests := []struct {
		name string
		req  *http.Request
		skip bool
	}{
		{
			name: "nil request",
			req:  nil,
			skip: true,
		},
		{
			name: "post request should not skip",
			req: &http.Request{
				Method: http.MethodPost,
				URL:    &url.URL{Path: "/v1/responses"},
			},
			skip: false,
		},
		{
			name: "plain get should skip",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/v1/models"},
				Header: http.Header{},
			},
			skip: true,
		},
		{
			name: "responses websocket upgrade should not skip",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/v1/responses"},
				Header: http.Header{"Upgrade": []string{"websocket"}},
			},
			skip: false,
		},
		{
			name: "responses get without upgrade should skip",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/v1/responses"},
				Header: http.Header{},
			},
			skip: true,
		},
	}

	for i := range tests {
		got := shouldSkipMethodForRequestLogging(tests[i].req)
		if got != tests[i].skip {
			t.Fatalf("%s: got skip=%t, want %t", tests[i].name, got, tests[i].skip)
		}
	}
}

func TestShouldCaptureRequestBody(t *testing.T) {
	tests := []struct {
		name          string
		loggerEnabled bool
		req           *http.Request
		want          bool
	}{
		{
			name:          "logger enabled always captures",
			loggerEnabled: true,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("{}")),
				ContentLength: -1,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: true,
		},
		{
			name:          "nil request",
			loggerEnabled: false,
			req:           nil,
			want:          false,
		},
		{
			name:          "small known size json in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("{}")),
				ContentLength: 2,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: true,
		},
		{
			name:          "large known size skipped in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("x")),
				ContentLength: maxErrorOnlyCapturedRequestBodyBytes + 1,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: false,
		},
		{
			name:          "unknown size skipped in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("x")),
				ContentLength: -1,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: false,
		},
		{
			name:          "multipart skipped in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("x")),
				ContentLength: 1,
				Header:        http.Header{"Content-Type": []string{"multipart/form-data; boundary=abc"}},
			},
			want: false,
		},
	}

	for i := range tests {
		got := shouldCaptureRequestBody(tests[i].loggerEnabled, tests[i].req)
		if got != tests[i].want {
			t.Fatalf("%s: got %t, want %t", tests[i].name, got, tests[i].want)
		}
	}
}

func TestCaptureRequestInfoPreservesSmallBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	body := []byte("small-request-body")
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.ContentLength = int64(len(body))

	info, err := captureRequestInfo(c, true)
	if err != nil {
		t.Fatalf("captureRequestInfo error: %v", err)
	}
	if !bytes.Equal(info.Body, body) {
		t.Fatalf("request info body = %q, want %q", string(info.Body), string(body))
	}
	if info.BodyTruncated {
		t.Fatal("expected small body to not be truncated")
	}
	if got := readAllRequestBody(t, c.Request.Body); !bytes.Equal(got, body) {
		t.Fatalf("replayed request body = %q, want %q", string(got), string(body))
	}
}

func TestCaptureRequestInfoTruncatesLargeBodyAndReplaysFullBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	body := bytes.Repeat([]byte("a"), requestLogBodyPrefixLimit+128)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.ContentLength = int64(len(body))

	info, err := captureRequestInfo(c, true)
	if err != nil {
		t.Fatalf("captureRequestInfo error: %v", err)
	}
	if len(info.Body) != requestLogBodyPrefixLimit {
		t.Fatalf("captured request body length = %d, want %d", len(info.Body), requestLogBodyPrefixLimit)
	}
	if !bytes.Equal(info.Body, body[:requestLogBodyPrefixLimit]) {
		t.Fatal("captured request body prefix mismatch")
	}
	if !info.BodyTruncated {
		t.Fatal("expected large body to be marked truncated")
	}
	if got := readAllRequestBody(t, c.Request.Body); !bytes.Equal(got, body) {
		t.Fatalf("replayed request body length = %d, want %d", len(got), len(body))
	}
}

func TestCaptureRequestInfoReplayedBodyClosePropagates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	originalBody := &trackingReadCloser{reader: bytes.NewReader([]byte("close-propagation"))}
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Body = originalBody
	c.Request.ContentLength = int64(len("close-propagation"))

	_, err := captureRequestInfo(c, true)
	if err != nil {
		t.Fatalf("captureRequestInfo error: %v", err)
	}
	if err = c.Request.Body.Close(); err != nil {
		t.Fatalf("wrapped body close error: %v", err)
	}
	if !originalBody.closed {
		t.Fatal("expected wrapped body close to propagate to original body")
	}
}

func readAllRequestBody(t *testing.T, body io.ReadCloser) []byte {
	t.Helper()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read request body error: %v", err)
	}
	return data
}

type trackingReadCloser struct {
	reader io.Reader
	closed bool
}

func (r *trackingReadCloser) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}
