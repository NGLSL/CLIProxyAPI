package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	coreexecutor "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/gin-gonic/gin"
	"golang.org/x/net/context"
)

func TestRequestExecutionMetadata_IncludesStickyRouteKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Idempotency-Key", "idem-1")
	ginCtx.Request = req
	ginCtx.Set("accessIndex", "access-idx-1")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	ctx = WithPinnedAuthID(ctx, "auth-1")
	ctx = WithSelectedAuthIDCallback(ctx, func(string) {})
	ctx = WithExecutionSessionID(ctx, "session-1")

	meta := requestExecutionMetadata(ctx)
	if got, _ := meta[idempotencyKeyMetadataKey].(string); got != "idem-1" {
		t.Fatalf("idempotency key = %q, want %q", got, "idem-1")
	}
	if got, _ := meta[coreexecutor.StickyRouteMetadataKey].(string); got != "access-idx-1" {
		t.Fatalf("sticky route key = %q, want %q", got, "access-idx-1")
	}
	if got, _ := meta[coreexecutor.PinnedAuthMetadataKey].(string); got != "auth-1" {
		t.Fatalf("pinned auth id = %q, want %q", got, "auth-1")
	}
	if got, _ := meta[coreexecutor.ExecutionSessionMetadataKey].(string); got != "session-1" {
		t.Fatalf("execution session id = %q, want %q", got, "session-1")
	}
	if _, ok := meta[coreexecutor.SelectedAuthCallbackMetadataKey].(func(string)); !ok {
		t.Fatalf("selected auth callback missing or wrong type")
	}
}

func TestRequestExecutionMetadata_OmitsStickyRouteKeyWithoutAccessIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	ginCtx.Request = req

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	meta := requestExecutionMetadata(ctx)
	if _, ok := meta[coreexecutor.StickyRouteMetadataKey]; ok {
		t.Fatalf("sticky route key unexpectedly present: %#v", meta[coreexecutor.StickyRouteMetadataKey])
	}
	if got, _ := meta[idempotencyKeyMetadataKey].(string); got == "" {
		t.Fatalf("idempotency key = empty")
	}
}
