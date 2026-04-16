package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestPatchAuthFileFields_MergeHeadersAndDeleteEmptyValues(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       "test.json",
		FileName: "test.json",
		Provider: "claude",
		Attributes: map[string]string{
			"path":            "/tmp/test.json",
			"header:X-Old":    "old",
			"header:X-Remove": "gone",
		},
		Metadata: map[string]any{
			"type": "claude",
			"headers": map[string]any{
				"X-Old":    "old",
				"X-Remove": "gone",
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	body := `{"name":"test.json","prefix":"p1","proxy_url":"http://proxy.local","headers":{"X-Old":"new","X-New":"v","X-Remove":"  ","X-Nope":""}}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	updated, ok := manager.GetByID("test.json")
	if !ok || updated == nil {
		t.Fatalf("expected auth record to exist after patch")
	}

	if updated.Prefix != "p1" {
		t.Fatalf("prefix = %q, want %q", updated.Prefix, "p1")
	}
	if updated.ProxyURL != "http://proxy.local" {
		t.Fatalf("proxy_url = %q, want %q", updated.ProxyURL, "http://proxy.local")
	}

	if updated.Metadata == nil {
		t.Fatalf("expected metadata to be non-nil")
	}
	if got, _ := updated.Metadata["prefix"].(string); got != "p1" {
		t.Fatalf("metadata.prefix = %q, want %q", got, "p1")
	}
	if got, _ := updated.Metadata["proxy_url"].(string); got != "http://proxy.local" {
		t.Fatalf("metadata.proxy_url = %q, want %q", got, "http://proxy.local")
	}

	headersMeta, ok := updated.Metadata["headers"].(map[string]any)
	if !ok {
		raw, _ := json.Marshal(updated.Metadata["headers"])
		t.Fatalf("metadata.headers = %T (%s), want map[string]any", updated.Metadata["headers"], string(raw))
	}
	if got := headersMeta["X-Old"]; got != "new" {
		t.Fatalf("metadata.headers.X-Old = %#v, want %q", got, "new")
	}
	if got := headersMeta["X-New"]; got != "v" {
		t.Fatalf("metadata.headers.X-New = %#v, want %q", got, "v")
	}
	if _, ok := headersMeta["X-Remove"]; ok {
		t.Fatalf("expected metadata.headers.X-Remove to be deleted")
	}
	if _, ok := headersMeta["X-Nope"]; ok {
		t.Fatalf("expected metadata.headers.X-Nope to be absent")
	}

	if got := updated.Attributes["header:X-Old"]; got != "new" {
		t.Fatalf("attrs header:X-Old = %q, want %q", got, "new")
	}
	if got := updated.Attributes["header:X-New"]; got != "v" {
		t.Fatalf("attrs header:X-New = %q, want %q", got, "v")
	}
	if _, ok := updated.Attributes["header:X-Remove"]; ok {
		t.Fatalf("expected attrs header:X-Remove to be deleted")
	}
	if _, ok := updated.Attributes["header:X-Nope"]; ok {
		t.Fatalf("expected attrs header:X-Nope to be absent")
	}
}

func TestPatchAuthFileFields_HeadersEmptyMapIsNoop(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       "noop.json",
		FileName: "noop.json",
		Provider: "claude",
		Attributes: map[string]string{
			"path":         "/tmp/noop.json",
			"header:X-Kee": "1",
		},
		Metadata: map[string]any{
			"type": "claude",
			"headers": map[string]any{
				"X-Kee": "1",
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	body := `{"name":"noop.json","note":"hello","headers":{}}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	updated, ok := manager.GetByID("noop.json")
	if !ok || updated == nil {
		t.Fatalf("expected auth record to exist after patch")
	}
	if got := updated.Attributes["header:X-Kee"]; got != "1" {
		t.Fatalf("attrs header:X-Kee = %q, want %q", got, "1")
	}
	headersMeta, ok := updated.Metadata["headers"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata.headers to remain a map, got %T", updated.Metadata["headers"])
	}
	if got := headersMeta["X-Kee"]; got != "1" {
		t.Fatalf("metadata.headers.X-Kee = %#v, want %q", got, "1")
	}
}

func TestPatchAuthFileFields_NamesSupportsBatchDisabledAndReturnsResults(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	for _, record := range []*coreauth.Auth{
		{
			ID:       "first.json",
			FileName: "first.json",
			Provider: "claude",
			Attributes: map[string]string{
				"path": "/tmp/first.json",
			},
			Metadata: map[string]any{"type": "claude"},
		},
		{
			ID:       "second.json",
			FileName: "second.json",
			Provider: "claude",
			Attributes: map[string]string{
				"path": "/tmp/second.json",
			},
			Metadata: map[string]any{"type": "claude"},
		},
	} {
		if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
			t.Fatalf("failed to register auth record: %v", errRegister)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	body := `{"names":["first.json","second.json","missing.json"],"disabled":true}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusMultiStatus, rec.Code, rec.Body.String())
	}
	if !gjson.ValidBytes(rec.Body.Bytes()) {
		t.Fatalf("expected JSON body, got %s", rec.Body.String())
	}
	if got := gjson.GetBytes(rec.Body.Bytes(), "status").String(); got != "partial" {
		t.Fatalf("status = %q, want %q", got, "partial")
	}
	if got := gjson.GetBytes(rec.Body.Bytes(), "updated.#").Int(); got != 2 {
		t.Fatalf("updated count = %d, want 2", got)
	}
	if got := gjson.GetBytes(rec.Body.Bytes(), "failed.#").Int(); got != 1 {
		t.Fatalf("failed count = %d, want 1", got)
	}
	if got := gjson.GetBytes(rec.Body.Bytes(), `failed.#(name=="missing.json").error`).String(); got == "" {
		t.Fatalf("expected failed entry for missing.json, body = %s", rec.Body.String())
	}

	for _, id := range []string{"first.json", "second.json"} {
		updated, ok := manager.GetByID(id)
		if !ok || updated == nil {
			t.Fatalf("expected auth record %s to exist after patch", id)
		}
		if !updated.Disabled {
			t.Fatalf("expected %s to be disabled", id)
		}
		if updated.Status != coreauth.StatusDisabled {
			t.Fatalf("status for %s = %q, want %q", id, updated.Status, coreauth.StatusDisabled)
		}
	}
}

func TestPatchAuthFileFields_AllSupportsWebsocketsSkipAndListExposure(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	for _, record := range []*coreauth.Auth{
		{
			ID:       "codex.json",
			FileName: "codex.json",
			Provider: "codex",
			Attributes: map[string]string{
				"path":       "/tmp/codex.json",
				"websockets": "false",
			},
			Metadata: map[string]any{"type": "codex", "websockets": false},
		},
		{
			ID:       "claude.json",
			FileName: "claude.json",
			Provider: "claude",
			Attributes: map[string]string{
				"path": "/tmp/claude.json",
			},
			Metadata: map[string]any{"type": "claude"},
		},
	} {
		if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
			t.Fatalf("failed to register auth record: %v", errRegister)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	patchBody := `{"all":true,"websockets":true}`
	patchRec := httptest.NewRecorder()
	patchCtx, _ := gin.CreateTestContext(patchRec)
	patchReq := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(patchBody))
	patchReq.Header.Set("Content-Type", "application/json")
	patchCtx.Request = patchReq
	h.PatchAuthFileFields(patchCtx)

	if patchRec.Code != http.StatusMultiStatus {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusMultiStatus, patchRec.Code, patchRec.Body.String())
	}
	if got := gjson.GetBytes(patchRec.Body.Bytes(), "status").String(); got != "partial" {
		t.Fatalf("status = %q, want %q", got, "partial")
	}
	if got := gjson.GetBytes(patchRec.Body.Bytes(), "updated.#").Int(); got != 1 {
		t.Fatalf("updated count = %d, want 1", got)
	}
	if got := gjson.GetBytes(patchRec.Body.Bytes(), "skipped.#").Int(); got != 1 {
		t.Fatalf("skipped count = %d, want 1", got)
	}
	if got := gjson.GetBytes(patchRec.Body.Bytes(), `skipped.#(name=="claude.json").reason`).String(); got != "websockets not supported" {
		t.Fatalf("skip reason = %q, want %q; body = %s", got, "websockets not supported", patchRec.Body.String())
	}

	codexAuth, ok := manager.GetByID("codex.json")
	if !ok || codexAuth == nil {
		t.Fatalf("expected codex auth record to exist after patch")
	}
	if got := codexAuth.Attributes["websockets"]; got != "true" {
		t.Fatalf("attrs websockets = %q, want %q", got, "true")
	}
	if got, _ := codexAuth.Metadata["websockets"].(bool); !got {
		t.Fatalf("metadata.websockets = %#v, want true", codexAuth.Metadata["websockets"])
	}

	listRec := httptest.NewRecorder()
	listCtx, _ := gin.CreateTestContext(listRec)
	listReq := httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	listCtx.Request = listReq
	h.ListAuthFiles(listCtx)

	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}
	if got := gjson.GetBytes(listRec.Body.Bytes(), `files.#(name=="codex.json").websockets`).String(); got != "true" {
		t.Fatalf("list codex websockets = %q, want %q; body = %s", got, "true", listRec.Body.String())
	}
	if gjson.GetBytes(listRec.Body.Bytes(), `files.#(name=="claude.json").websockets`).Exists() {
		t.Fatalf("expected claude auth to omit websockets field, body = %s", listRec.Body.String())
	}
}

func TestPatchAuthFileFields_RejectsRequestWithoutTargets(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(&memoryAuthStore{}, nil, nil))

	body := `{"disabled":true}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	if got := gjson.GetBytes(rec.Body.Bytes(), "error").String(); got != "name, names, or all is required" {
		t.Fatalf("error = %q, want %q", got, "name, names, or all is required")
	}
}

func TestPatchAuthFileFields_RejectsRequestWithoutFields(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(&memoryAuthStore{}, nil, nil))

	body := `{"name":"noop.json"}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	if got := gjson.GetBytes(rec.Body.Bytes(), "error").String(); got != "no fields to update" {
		t.Fatalf("error = %q, want %q", got, "no fields to update")
	}
}

func TestPatchAuthFileFields_NameDeduplicatesAgainstNames(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       "dup.json",
		FileName: "dup.json",
		Provider: "claude",
		Attributes: map[string]string{
			"path": "/tmp/dup.json",
		},
		Metadata: map[string]any{"type": "claude"},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	body := `{"name":"dup.json","names":["dup.json"," dup.json "],"disabled":true}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if got := gjson.GetBytes(rec.Body.Bytes(), "updated.#").Int(); got != 1 {
		t.Fatalf("updated count = %d, want 1; body = %s", got, rec.Body.String())
	}
}

func TestPatchAuthFileFields_NoopStillReturnsUpdated(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       "noop-target.json",
		FileName: "noop-target.json",
		Provider: "claude",
		Disabled: true,
		Status:   coreauth.StatusDisabled,
		Attributes: map[string]string{
			"path": "/tmp/noop-target.json",
		},
		Metadata: map[string]any{"type": "claude"},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	body := `{"name":"noop-target.json","disabled":true}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if got := gjson.GetBytes(rec.Body.Bytes(), "updated.#").Int(); got != 1 {
		t.Fatalf("updated count = %d, want 1; body = %s", got, rec.Body.String())
	}
	if got := gjson.GetBytes(rec.Body.Bytes(), "updated.0").String(); got != "noop-target.json" {
		t.Fatalf("updated[0] = %q, want %q; body = %s", got, "noop-target.json", rec.Body.String())
	}
}
