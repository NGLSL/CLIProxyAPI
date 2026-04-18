package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	proxyconfig "github.com/NGLSL/CLIProxyAPI/v6/internal/config"
	internallogging "github.com/NGLSL/CLIProxyAPI/v6/internal/logging"
	"github.com/NGLSL/CLIProxyAPI/v6/internal/usage"
	sdkaccess "github.com/NGLSL/CLIProxyAPI/v6/sdk/access"
	"github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/NGLSL/CLIProxyAPI/v6/sdk/config"
	gin "github.com/gin-gonic/gin"
)

type stubAccessProvider struct {
	result *sdkaccess.Result
	err    *sdkaccess.AuthError
}

func (p *stubAccessProvider) Identifier() string {
	return "stub"
}

func (p *stubAccessProvider) Authenticate(_ context.Context, _ *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	return p.result, p.err
}

func newTestAccessManager(result *sdkaccess.Result, err *sdkaccess.AuthError) *sdkaccess.Manager {
	manager := sdkaccess.NewManager()
	manager.SetProviders([]sdkaccess.Provider{&stubAccessProvider{result: result, err: err}})
	return manager
}

func TestAuthMiddleware_SetsAccessIndex(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accessManager := newTestAccessManager(&sdkaccess.Result{Provider: "config-inline", Principal: "account-a"}, nil)
	engine := gin.New()
	engine.Use(AuthMiddleware(accessManager))
	engine.GET("/", func(c *gin.Context) {
		got, ok := c.Get("accessIndex")
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "missing accessIndex"})
			return
		}
		value, _ := got.(string)
		c.JSON(http.StatusOK, gin.H{"accessIndex": value})
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	want := sdkaccess.StableIndex("config-inline", "account-a")
	if !strings.Contains(rr.Body.String(), want) {
		t.Fatalf("response body missing access index %q: %s", want, rr.Body.String())
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"test-key"},
		},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()

	configPath := filepath.Join(tmpDir, "config.yaml")
	return NewServer(cfg, authManager, accessManager, configPath)
}

func TestHealthz(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp struct {
		Status                  string `json:"status"`
		ManagementRoutesEnabled bool   `json:"management_routes_enabled"`
		UsageStatisticsEnabled  bool   `json:"usage_statistics_enabled"`
		Version                 string `json:"version"`
		BuildDate               string `json:"build_date"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
	}
	if resp.Status != "ok" {
		t.Fatalf("unexpected response status: got %q want %q", resp.Status, "ok")
	}
	if resp.ManagementRoutesEnabled {
		t.Fatalf("unexpected management routes state: got %t want %t", resp.ManagementRoutesEnabled, false)
	}
	if resp.UsageStatisticsEnabled != usage.StatisticsEnabled() {
		t.Fatalf("unexpected usage statistics state: got %t want %t", resp.UsageStatisticsEnabled, usage.StatisticsEnabled())
	}
	if resp.Version == "" {
		t.Fatal("expected version to be present in healthz response")
	}
	if resp.BuildDate == "" {
		t.Fatal("expected build_date to be present in healthz response")
	}
}

func TestAmpProviderModelRoutes(t *testing.T) {
	testCases := []struct {
		name         string
		path         string
		wantStatus   int
		wantContains string
	}{
		{
			name:         "openai root models",
			path:         "/api/provider/openai/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "groq root models",
			path:         "/api/provider/groq/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "openai models",
			path:         "/api/provider/openai/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "anthropic models",
			path:         "/api/provider/anthropic/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"data"`,
		},
		{
			name:         "google models v1",
			path:         "/api/provider/google/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
		{
			name:         "google models v1beta",
			path:         "/api/provider/google/v1beta/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer test-key")

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("unexpected status code for %s: got %d want %d; body=%s", tc.path, rr.Code, tc.wantStatus, rr.Body.String())
			}
			if body := rr.Body.String(); !strings.Contains(body, tc.wantContains) {
				t.Fatalf("response body for %s missing %q: %s", tc.path, tc.wantContains, body)
			}
		})
	}
}

func TestServerRegistersProtectedOpenAIRoutes(t *testing.T) {
	server := newTestServer(t)

	protectedPaths := []struct {
		name   string
		method string
		path   string
	}{
		{name: "chat completions", method: http.MethodPost, path: "/v1/chat/completions"},
		{name: "responses", method: http.MethodPost, path: "/v1/responses"},
		{name: "responses compact", method: http.MethodPost, path: "/v1/responses/compact"},
	}

	for _, tc := range protectedPaths {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("unexpected status code for %s: got %d want %d; body=%s", tc.path, rr.Code, http.StatusUnauthorized, rr.Body.String())
			}
		})
	}
}

func TestDefaultRequestLoggerFactory_UsesResolvedLogDirectory(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	t.Setenv("writable_path", "")

	originalWD, errGetwd := os.Getwd()
	if errGetwd != nil {
		t.Fatalf("failed to get current working directory: %v", errGetwd)
	}

	tmpDir := t.TempDir()
	if errChdir := os.Chdir(tmpDir); errChdir != nil {
		t.Fatalf("failed to switch working directory: %v", errChdir)
	}
	defer func() {
		if errChdirBack := os.Chdir(originalWD); errChdirBack != nil {
			t.Fatalf("failed to restore working directory: %v", errChdirBack)
		}
	}()

	// Force ResolveLogDirectory to fallback to auth-dir/logs by making ./logs not a writable directory.
	if errWriteFile := os.WriteFile(filepath.Join(tmpDir, "logs"), []byte("not-a-directory"), 0o644); errWriteFile != nil {
		t.Fatalf("failed to create blocking logs file: %v", errWriteFile)
	}

	configDir := filepath.Join(tmpDir, "config")
	if errMkdirConfig := os.MkdirAll(configDir, 0o755); errMkdirConfig != nil {
		t.Fatalf("failed to create config dir: %v", errMkdirConfig)
	}
	configPath := filepath.Join(configDir, "config.yaml")

	authDir := filepath.Join(tmpDir, "auth")
	if errMkdirAuth := os.MkdirAll(authDir, 0o700); errMkdirAuth != nil {
		t.Fatalf("failed to create auth dir: %v", errMkdirAuth)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: proxyconfig.SDKConfig{
			RequestLog: false,
		},
		AuthDir:           authDir,
		ErrorLogsMaxFiles: 10,
	}

	logger := defaultRequestLoggerFactory(cfg, configPath)
	fileLogger, ok := logger.(*internallogging.FileRequestLogger)
	if !ok {
		t.Fatalf("expected *FileRequestLogger, got %T", logger)
	}

	errLog := fileLogger.LogRequestWithOptions(
		"/v1/chat/completions",
		http.MethodPost,
		map[string][]string{"Content-Type": []string{"application/json"}},
		[]byte(`{"input":"hello"}`),
		http.StatusBadGateway,
		map[string][]string{"Content-Type": []string{"application/json"}},
		[]byte(`{"error":"upstream failure"}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
		"issue-1711",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("failed to write forced error request log: %v", errLog)
	}

	authLogsDir := filepath.Join(authDir, "logs")
	authEntries, errReadAuthDir := os.ReadDir(authLogsDir)
	if errReadAuthDir != nil {
		t.Fatalf("failed to read auth logs dir %s: %v", authLogsDir, errReadAuthDir)
	}
	foundErrorLogInAuthDir := false
	for _, entry := range authEntries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			foundErrorLogInAuthDir = true
			break
		}
	}
	if !foundErrorLogInAuthDir {
		t.Fatalf("expected forced error log in auth fallback dir %s, got entries: %+v", authLogsDir, authEntries)
	}

	configLogsDir := filepath.Join(configDir, "logs")
	configEntries, errReadConfigDir := os.ReadDir(configLogsDir)
	if errReadConfigDir != nil && !os.IsNotExist(errReadConfigDir) {
		t.Fatalf("failed to inspect config logs dir %s: %v", configLogsDir, errReadConfigDir)
	}
	for _, entry := range configEntries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			t.Fatalf("unexpected forced error log in config dir %s", configLogsDir)
		}
	}
}
