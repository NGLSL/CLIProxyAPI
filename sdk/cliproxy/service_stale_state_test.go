package cliproxy

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/registry"
	internalusage "github.com/NGLSL/CLIProxyAPI/v6/internal/usage"
	sdkaccess "github.com/NGLSL/CLIProxyAPI/v6/sdk/access"
	coreauth "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreusage "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/usage"
	"github.com/NGLSL/CLIProxyAPI/v6/sdk/config"
)

func TestServiceApplyCoreAuthAddOrUpdate_DeleteReAddDoesNotInheritStaleRuntimeState(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	authID := "service-stale-state-auth"
	modelID := "stale-model"
	lastRefreshedAt := time.Date(2026, time.March, 1, 8, 0, 0, 0, time.UTC)
	nextRefreshAfter := lastRefreshedAt.Add(30 * time.Minute)

	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(authID)
	})

	service.applyCoreAuthAddOrUpdate(context.Background(), &coreauth.Auth{
		ID:               authID,
		Provider:         "claude",
		Status:           coreauth.StatusActive,
		LastRefreshedAt:  lastRefreshedAt,
		NextRefreshAfter: nextRefreshAfter,
		ModelStates: map[string]*coreauth.ModelState{
			modelID: &coreauth.ModelState{
				Quota: coreauth.QuotaState{BackoffLevel: 7},
			},
		},
	})

	service.applyCoreAuthRemoval(context.Background(), authID)

	disabled, ok := service.coreManager.GetByID(authID)
	if !ok || disabled == nil {
		t.Fatalf("expected disabled auth after removal")
	}
	if !disabled.Disabled || disabled.Status != coreauth.StatusDisabled {
		t.Fatalf("expected disabled auth after removal, got disabled=%v status=%v", disabled.Disabled, disabled.Status)
	}
	if disabled.LastRefreshedAt.IsZero() {
		t.Fatalf("expected disabled auth to still carry prior LastRefreshedAt for regression setup")
	}
	if disabled.NextRefreshAfter.IsZero() {
		t.Fatalf("expected disabled auth to still carry prior NextRefreshAfter for regression setup")
	}

	// Reconcile prunes unsupported model state during registration, so seed the
	// disabled snapshot explicitly before exercising delete -> re-add behavior.
	disabled.ModelStates = map[string]*coreauth.ModelState{
		modelID: &coreauth.ModelState{
			Quota: coreauth.QuotaState{BackoffLevel: 7},
		},
	}
	if _, err := service.coreManager.Update(context.Background(), disabled); err != nil {
		t.Fatalf("seed disabled auth stale ModelStates: %v", err)
	}

	disabled, ok = service.coreManager.GetByID(authID)
	if !ok || disabled == nil {
		t.Fatalf("expected disabled auth after stale state seeding")
	}
	if len(disabled.ModelStates) == 0 {
		t.Fatalf("expected disabled auth to carry seeded ModelStates for regression setup")
	}

	service.applyCoreAuthAddOrUpdate(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "claude",
		Status:   coreauth.StatusActive,
	})

	updated, ok := service.coreManager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatalf("expected re-added auth to be present")
	}
	if updated.Disabled {
		t.Fatalf("expected re-added auth to be active")
	}
	if !updated.LastRefreshedAt.IsZero() {
		t.Fatalf("expected LastRefreshedAt to reset on delete -> re-add, got %v", updated.LastRefreshedAt)
	}
	if !updated.NextRefreshAfter.IsZero() {
		t.Fatalf("expected NextRefreshAfter to reset on delete -> re-add, got %v", updated.NextRefreshAfter)
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected ModelStates to reset on delete -> re-add, got %d entries", len(updated.ModelStates))
	}
	if models := registry.GetGlobalRegistry().GetModelsForClient(authID); len(models) == 0 {
		t.Fatalf("expected re-added auth to re-register models in global registry")
	}
}

func TestServiceConfigureUsageStatisticsEnabledFollowsConfig(t *testing.T) {
	prevEnabled := internalusage.StatisticsEnabled()
	t.Cleanup(func() {
		internalusage.SetStatisticsEnabled(prevEnabled)
	})

	service := &Service{cfg: &config.Config{UsageStatisticsEnabled: false}}
	if enabled := service.configureUsageStatisticsEnabled(); enabled {
		t.Fatalf("configureUsageStatisticsEnabled() = true, want false")
	}
	if internalusage.StatisticsEnabled() {
		t.Fatalf("StatisticsEnabled() = true, want false")
	}

	service.cfg.UsageStatisticsEnabled = true
	if enabled := service.configureUsageStatisticsEnabled(); !enabled {
		t.Fatalf("configureUsageStatisticsEnabled() = false, want true")
	}
	if !internalusage.StatisticsEnabled() {
		t.Fatalf("StatisticsEnabled() = false, want true")
	}
}

func TestServiceRestoreUsageSnapshotMergesPersistedData(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	coreusage.ResetDefaultManagerForTest(t)
	coreusage.RegisterPlugin(internalusage.NewLoggerPlugin())
	prevEnabled := internalusage.StatisticsEnabled()
	internalusage.SetStatisticsEnabled(true)
	internalusage.ResetDefaultRequestStatistics()
	t.Cleanup(func() {
		internalusage.ResetDefaultRequestStatistics()
		internalusage.SetStatisticsEnabled(prevEnabled)
	})

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	snapshotPath := internalusage.DefaultSnapshotPath(configPath)
	stats := internalusage.NewRequestStatistics()
	baseTime := time.Date(2026, 4, 17, 10, 0, 0, 0, time.UTC)

	var expectedTotalTokens int64
	for i := 0; i < 225; i++ {
		tokens := int64(i + 1)
		expectedTotalTokens += tokens
		stats.Record(context.Background(), coreusage.Record{
			APIKey:      "restore-key",
			Model:       "gpt-5.4",
			RequestedAt: baseTime.Add(time.Duration(i) * time.Minute),
			Source:      "restore-source",
			Detail: coreusage.Detail{
				InputTokens: tokens,
				TotalTokens: tokens,
			},
		})
	}
	if err := internalusage.SaveRequestStatisticsToFile(snapshotPath, stats); err != nil {
		t.Fatalf("SaveRequestStatisticsToFile() error = %v", err)
	}

	service := &Service{configPath: configPath}
	service.restoreUsageSnapshot()

	snapshot := internalusage.GetRequestStatistics().Snapshot()
	if snapshot.TotalRequests != 225 {
		t.Fatalf("total requests = %d, want 225", snapshot.TotalRequests)
	}
	if snapshot.TotalTokens != expectedTotalTokens {
		t.Fatalf("total tokens = %d, want %d", snapshot.TotalTokens, expectedTotalTokens)
	}
	apiSnapshot := snapshot.APIs["restore-key"]
	if apiSnapshot.TotalRequests != 225 {
		t.Fatalf("api total requests = %d, want 225", apiSnapshot.TotalRequests)
	}
	if apiSnapshot.TotalTokens != expectedTotalTokens {
		t.Fatalf("api total tokens = %d, want %d", apiSnapshot.TotalTokens, expectedTotalTokens)
	}
	modelSnapshot := apiSnapshot.Models["gpt-5.4"]
	if modelSnapshot.TotalRequests != 225 {
		t.Fatalf("model total requests = %d, want 225", modelSnapshot.TotalRequests)
	}
	if modelSnapshot.TotalTokens != expectedTotalTokens {
		t.Fatalf("model total tokens = %d, want %d", modelSnapshot.TotalTokens, expectedTotalTokens)
	}
	if got := len(modelSnapshot.Details); got != 200 {
		t.Fatalf("details len = %d, want 200", got)
	}
}

func TestServiceReloadEnablingUsageRestoresSnapshot(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	coreusage.ResetDefaultManagerForTest(t)
	coreusage.RegisterPlugin(internalusage.NewLoggerPlugin())
	prevEnabled := internalusage.StatisticsEnabled()
	internalusage.SetStatisticsEnabled(false)
	internalusage.ResetDefaultRequestStatistics()
	t.Cleanup(func() {
		internalusage.ResetDefaultRequestStatistics()
		internalusage.SetStatisticsEnabled(prevEnabled)
	})

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	authDir := filepath.Join(tmpDir, "auth")
	if err := internalusage.SaveSnapshotToFile(internalusage.DefaultSnapshotPath(configPath), internalusage.StatisticsSnapshot{
		TotalRequests: 1,
		SuccessCount:  1,
		TotalTokens:   12,
		APIs: map[string]internalusage.APISnapshot{
			"reload-key": {
				TotalRequests: 1,
				TotalTokens:   12,
				Models: map[string]internalusage.ModelSnapshot{
					"gpt-5.4": {
						TotalRequests: 1,
						TotalTokens:   12,
						Details: []internalusage.RequestDetail{{
							Timestamp: time.Date(2026, 4, 17, 10, 30, 0, 0, time.UTC),
							Source:    "reload-source",
							Tokens: internalusage.TokenStats{
								InputTokens:  4,
								OutputTokens: 8,
								TotalTokens:  12,
							},
						}},
					},
				},
			},
		},
		RequestsByDay:  map[string]int64{"2026-04-17": 1},
		RequestsByHour: map[string]int64{"10": 1},
		TokensByDay:    map[string]int64{"2026-04-17": 12},
		TokensByHour:   map[string]int64{"10": 12},
	}); err != nil {
		t.Fatalf("SaveSnapshotToFile() error = %v", err)
	}

	service := &Service{
		cfg: &config.Config{
			Port:                   0,
			AuthDir:                authDir,
			UsageStatisticsEnabled: false,
		},
		configPath:     configPath,
		tokenProvider:  stubTokenClientProvider{},
		apiKeyProvider: stubAPIKeyClientProvider{},
		accessManager:  sdkaccess.NewManager(),
		coreManager:    coreauth.NewManager(nil, nil, nil),
		watcherFactory: func(cfgPath, watchAuthDir string, reload func(*config.Config)) (*WatcherWrapper, error) {
			return &WatcherWrapper{
				start: func(context.Context) error {
					reload(&config.Config{
						Port:                   0,
						AuthDir:                watchAuthDir,
						UsageStatisticsEnabled: true,
					})
					return nil
				},
			}, nil
		},
		hooks: Hooks{
			OnAfterStart: func(s *Service) {
				time.AfterFunc(200*time.Millisecond, func() {
					_ = s.Shutdown(context.Background())
				})
			},
		},
	}

	if err := service.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	snapshot := internalusage.GetRequestStatistics().Snapshot()
	if snapshot.TotalRequests != 1 {
		t.Fatalf("total requests = %d, want 1", snapshot.TotalRequests)
	}
	if snapshot.TotalTokens != 12 {
		t.Fatalf("total tokens = %d, want 12", snapshot.TotalTokens)
	}
}

func TestServiceShutdownFlushUsageAndPersistSavesDrainedRecords(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	coreusage.ResetDefaultManagerForTest(t)
	coreusage.RegisterPlugin(internalusage.NewLoggerPlugin())
	prevEnabled := internalusage.StatisticsEnabled()
	internalusage.SetStatisticsEnabled(true)
	internalusage.ResetDefaultRequestStatistics()
	t.Cleanup(func() {
		internalusage.ResetDefaultRequestStatistics()
		internalusage.SetStatisticsEnabled(prevEnabled)
	})

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	service := &Service{
		cfg: &config.Config{
			SDKConfig:              config.SDKConfig{},
			Port:                   0,
			AuthDir:                filepath.Join(tmpDir, "auth"),
			UsageStatisticsEnabled: true,
		},
		configPath:     configPath,
		tokenProvider:  stubTokenClientProvider{},
		apiKeyProvider: stubAPIKeyClientProvider{},
		watcherFactory: stubWatcherFactory,
		authManager:    nil,
		accessManager:  sdkaccess.NewManager(),
		coreManager:    coreauth.NewManager(nil, nil, nil),
		hooks: Hooks{
			OnAfterStart: func(s *Service) {
				coreusage.PublishRecord(context.Background(), coreusage.Record{
					APIKey:      "shutdown-key",
					Model:       "gpt-5.4",
					RequestedAt: time.Date(2026, 4, 17, 11, 0, 0, 0, time.UTC),
					Detail: coreusage.Detail{
						InputTokens:  7,
						OutputTokens: 5,
						TotalTokens:  12,
					},
				})
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	err := service.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}

	loaded, err := internalusage.LoadSnapshotFromFile(internalusage.DefaultSnapshotPath(configPath))
	if err != nil {
		t.Fatalf("LoadSnapshotFromFile() error = %v", err)
	}
	if loaded.TotalRequests != 1 {
		t.Fatalf("total requests = %d, want 1", loaded.TotalRequests)
	}
	if loaded.TotalTokens != 12 {
		t.Fatalf("total tokens = %d, want 12", loaded.TotalTokens)
	}
	if got := len(loaded.APIs["shutdown-key"].Models["gpt-5.4"].Details); got != 1 {
		t.Fatalf("details len = %d, want 1", got)
	}
}

func TestServicePersistUsageSnapshotPreservesLargerStoredAggregate(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	prevEnabled := internalusage.StatisticsEnabled()
	internalusage.SetStatisticsEnabled(true)
	internalusage.ResetDefaultRequestStatistics()
	t.Cleanup(func() {
		internalusage.ResetDefaultRequestStatistics()
		internalusage.SetStatisticsEnabled(prevEnabled)
	})

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	snapshotPath := internalusage.DefaultSnapshotPath(configPath)
	storedTime := time.Date(2026, 4, 17, 10, 0, 0, 0, time.UTC)
	if err := internalusage.SaveSnapshotToFile(snapshotPath, internalusage.StatisticsSnapshot{
		TotalRequests: 100,
		SuccessCount:  95,
		FailureCount:  5,
		TotalTokens:   1000,
		APIs: map[string]internalusage.APISnapshot{
			"stored-key": {
				TotalRequests: 100,
				TotalTokens:   1000,
				Models: map[string]internalusage.ModelSnapshot{
					"gpt-5.4": {
						TotalRequests: 100,
						TotalTokens:   1000,
						Details: []internalusage.RequestDetail{{
							Timestamp: storedTime,
							Source:    "stored-source",
							Tokens: internalusage.TokenStats{
								InputTokens:  400,
								OutputTokens: 600,
								TotalTokens:  1000,
							},
						}},
					},
				},
			},
		},
		RequestsByDay:  map[string]int64{"2026-04-17": 100},
		RequestsByHour: map[string]int64{"10": 100},
		TokensByDay:    map[string]int64{"2026-04-17": 1000},
		TokensByHour:   map[string]int64{"10": 1000},
	}); err != nil {
		t.Fatalf("SaveSnapshotToFile() error = %v", err)
	}

	stats := internalusage.GetRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "live-key",
		Model:       "gpt-5.4",
		RequestedAt: storedTime.Add(time.Hour),
		Detail: coreusage.Detail{
			InputTokens:  5,
			OutputTokens: 7,
			TotalTokens:  12,
		},
	})

	service := &Service{configPath: configPath}
	if err := service.persistUsageSnapshot(); err != nil {
		t.Fatalf("persistUsageSnapshot() error = %v", err)
	}

	loaded, err := internalusage.LoadSnapshotFromFile(snapshotPath)
	if err != nil {
		t.Fatalf("LoadSnapshotFromFile() error = %v", err)
	}
	if loaded.TotalRequests != 101 {
		t.Fatalf("total requests = %d, want 101", loaded.TotalRequests)
	}
	if loaded.SuccessCount != 96 {
		t.Fatalf("success count = %d, want 96", loaded.SuccessCount)
	}
	if loaded.FailureCount != 5 {
		t.Fatalf("failure count = %d, want 5", loaded.FailureCount)
	}
	if loaded.TotalTokens != 1012 {
		t.Fatalf("total tokens = %d, want 1012", loaded.TotalTokens)
	}
	if loaded.APIs["stored-key"].TotalTokens != 1000 {
		t.Fatalf("stored api tokens = %d, want 1000", loaded.APIs["stored-key"].TotalTokens)
	}
	if loaded.APIs["live-key"].TotalTokens != 12 {
		t.Fatalf("live api tokens = %d, want 12", loaded.APIs["live-key"].TotalTokens)
	}
}

func TestServicePersistUsageSnapshotPreservesTrimmedLiveAggregate(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	prevEnabled := internalusage.StatisticsEnabled()
	internalusage.SetStatisticsEnabled(true)
	internalusage.ResetDefaultRequestStatistics()
	t.Cleanup(func() {
		internalusage.ResetDefaultRequestStatistics()
		internalusage.SetStatisticsEnabled(prevEnabled)
	})

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	snapshotPath := internalusage.DefaultSnapshotPath(configPath)
	storedTime := time.Date(2026, 4, 17, 10, 0, 0, 0, time.UTC)
	if err := internalusage.SaveSnapshotToFile(snapshotPath, internalusage.StatisticsSnapshot{
		TotalRequests: 1000,
		SuccessCount:  1000,
		TotalTokens:   1000,
		APIs: map[string]internalusage.APISnapshot{
			"stored-key": {
				TotalRequests: 1000,
				TotalTokens:   1000,
				Models: map[string]internalusage.ModelSnapshot{
					"gpt-5.4": {
						TotalRequests: 1000,
						TotalTokens:   1000,
						Details: []internalusage.RequestDetail{{
							Timestamp: storedTime,
							Source:    "stored-source",
							Tokens:    internalusage.TokenStats{InputTokens: 1, TotalTokens: 1},
						}},
					},
				},
			},
		},
		RequestsByDay:  map[string]int64{"2026-04-17": 1000},
		RequestsByHour: map[string]int64{"10": 1000},
		TokensByDay:    map[string]int64{"2026-04-17": 1000},
		TokensByHour:   map[string]int64{"10": 1000},
	}); err != nil {
		t.Fatalf("SaveSnapshotToFile() error = %v", err)
	}

	stats := internalusage.GetRequestStatistics()
	for i := range 225 {
		stats.Record(context.Background(), coreusage.Record{
			APIKey:      "live-key",
			Model:       "gpt-5.4",
			RequestedAt: storedTime.Add(time.Hour + time.Duration(i)*time.Minute),
			Source:      "live-source",
			AuthIndex:   "live-auth",
			Detail:      coreusage.Detail{InputTokens: 1, TotalTokens: 1},
		})
	}

	service := &Service{configPath: configPath}
	if err := service.persistUsageSnapshot(); err != nil {
		t.Fatalf("persistUsageSnapshot() error = %v", err)
	}

	loaded, err := internalusage.LoadSnapshotFromFile(snapshotPath)
	if err != nil {
		t.Fatalf("LoadSnapshotFromFile() error = %v", err)
	}
	if loaded.TotalRequests != 1225 {
		t.Fatalf("total requests = %d, want 1225", loaded.TotalRequests)
	}
	if loaded.TotalTokens != 1225 {
		t.Fatalf("total tokens = %d, want 1225", loaded.TotalTokens)
	}
	if loaded.APIs["live-key"].TotalRequests != 225 {
		t.Fatalf("live api requests = %d, want 225", loaded.APIs["live-key"].TotalRequests)
	}
	if loaded.APIs["live-key"].TotalTokens != 225 {
		t.Fatalf("live api tokens = %d, want 225", loaded.APIs["live-key"].TotalTokens)
	}
	if got := len(loaded.APIs["live-key"].Models["gpt-5.4"].Details); got != 200 {
		t.Fatalf("live details len = %d, want 200", got)
	}
}

func TestServiceRunRestoresStoredAuthRuntimeSnapshotAndRegistersModels(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	authDir := filepath.Join(tmpDir, "auth")
	authID := "stored-auth"
	now := time.Now().UTC()
	nextRetryAfter := now.Add(10 * time.Minute)
	nextRecoverAt := now.Add(20 * time.Minute)

	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(authID)
	})

	service := &Service{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{},
			Port:      0,
			AuthDir:   authDir,
		},
		configPath:     configPath,
		tokenProvider:  stubTokenClientProvider{},
		apiKeyProvider: stubAPIKeyClientProvider{},
		watcherFactory: stubWatcherFactory,
		accessManager:  sdkaccess.NewManager(),
		coreManager: coreauth.NewManager(&testCoreAuthStore{items: []*coreauth.Auth{{
			ID:       authID,
			Provider: "claude",
			Status:   coreauth.StatusActive,
		}}}, nil, nil),
		hooks: Hooks{
			OnAfterStart: func(s *Service) {
				time.AfterFunc(200*time.Millisecond, func() {
					_ = s.Shutdown(context.Background())
				})
			},
		},
	}

	if err := coreauth.SaveRuntimeSnapshotToFile(service.authRuntimeSnapshotPath(), coreauth.RuntimeSnapshot{Auths: map[string]*coreauth.AuthRuntimeState{
		authID: {
			Status:         coreauth.StatusError,
			StatusMessage:  "cooldown",
			Unavailable:    true,
			NextRetryAfter: nextRetryAfter,
			LastError:      &coreauth.Error{Code: "rate_limit", Message: "retry later", Retryable: true, HTTPStatus: 429},
			UpdatedAt:      now,
			Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: nextRecoverAt, BackoffLevel: 2},
		},
	}}); err != nil {
		t.Fatalf("SaveRuntimeSnapshotToFile() error = %v", err)
	}

	if err := service.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	auth, ok := service.coreManager.GetByID(authID)
	if !ok || auth == nil {
		t.Fatalf("expected stored auth to be loaded")
	}
	if !auth.Unavailable {
		t.Fatalf("expected stored auth to restore unavailable state")
	}
	if !auth.NextRetryAfter.Equal(nextRetryAfter) {
		t.Fatalf("auth.NextRetryAfter = %v, want %v", auth.NextRetryAfter, nextRetryAfter)
	}
	if !auth.Quota.Exceeded {
		t.Fatalf("expected stored auth quota state to be restored")
	}
	if got := len(registry.GetGlobalRegistry().GetModelsForClient(authID)); got == 0 {
		t.Fatalf("expected stored auth models to be registered after load")
	}
}

func TestServiceRestoreWatcherSnapshotAuthsRestoresRuntimeSnapshotForWatcherAuth(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	authID := "watcher-auth"
	now := time.Now().UTC()
	nextRetryAfter := now.Add(10 * time.Minute)

	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(authID)
	})

	service.setAuthRuntimeSnapshot(coreauth.RuntimeSnapshot{Auths: map[string]*coreauth.AuthRuntimeState{
		authID: {
			Status:         coreauth.StatusError,
			StatusMessage:  "cooldown",
			Unavailable:    true,
			NextRetryAfter: nextRetryAfter,
			LastError:      &coreauth.Error{Code: "rate_limit", Message: "retry later", Retryable: true, HTTPStatus: 429},
			UpdatedAt:      now,
		},
	}})
	service.watcher = &WatcherWrapper{snapshotAuths: func() []*coreauth.Auth {
		return []*coreauth.Auth{{
			ID:       authID,
			Provider: "claude",
			Status:   coreauth.StatusActive,
		}}
	}}

	service.restoreWatcherSnapshotAuths()

	updated, ok := service.coreManager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatalf("expected watcher auth to be present")
	}
	if !updated.Unavailable {
		t.Fatalf("expected watcher auth to restore unavailable state")
	}
	if !updated.NextRetryAfter.Equal(nextRetryAfter) {
		t.Fatalf("updated.NextRetryAfter = %v, want %v", updated.NextRetryAfter, nextRetryAfter)
	}
}

func TestServiceReloadRestoreDoesNotOverrideFresherRuntimeState(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	authID := "reload-auth"
	now := time.Now().UTC()
	currentRetryAfter := now.Add(5 * time.Minute)
	snapshotRetryAfter := now.Add(30 * time.Minute)

	if _, err := service.coreManager.Register(context.Background(), &coreauth.Auth{
		ID:             authID,
		Provider:       "claude",
		Status:         coreauth.StatusError,
		StatusMessage:  "current state",
		Unavailable:    true,
		NextRetryAfter: currentRetryAfter,
		LastError:      &coreauth.Error{Code: "current", Message: "current state", Retryable: true, HTTPStatus: 429},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	service.setAuthRuntimeSnapshot(coreauth.RuntimeSnapshot{Auths: map[string]*coreauth.AuthRuntimeState{
		authID: {
			Status:         coreauth.StatusError,
			StatusMessage:  "stale snapshot",
			Unavailable:    true,
			NextRetryAfter: snapshotRetryAfter,
			LastError:      &coreauth.Error{Code: "snapshot", Message: "stale snapshot", Retryable: true, HTTPStatus: 429},
		},
	}})

	if service.restoreAuthRuntimeSnapshotForAuth(authID) {
		t.Fatalf("expected stale snapshot to be ignored")
	}

	updated, ok := service.coreManager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to remain present")
	}
	if updated.StatusMessage != "current state" {
		t.Fatalf("updated.StatusMessage = %q, want current state", updated.StatusMessage)
	}
	if !updated.NextRetryAfter.Equal(currentRetryAfter) {
		t.Fatalf("updated.NextRetryAfter = %v, want %v", updated.NextRetryAfter, currentRetryAfter)
	}
	if updated.LastError == nil || updated.LastError.Code != "current" {
		t.Fatalf("updated.LastError = %#v, want current", updated.LastError)
	}
}

func TestServiceShutdownPersistsAuthRuntimeSnapshot(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	authID := "shutdown-auth"
	now := time.Now().UTC()
	nextRetryAfter := now.Add(10 * time.Minute)

	service := &Service{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{},
			Port:      0,
			AuthDir:   filepath.Join(tmpDir, "auth"),
		},
		configPath:     configPath,
		tokenProvider:  stubTokenClientProvider{},
		apiKeyProvider: stubAPIKeyClientProvider{},
		watcherFactory: stubWatcherFactory,
		accessManager:  sdkaccess.NewManager(),
		coreManager:    coreauth.NewManager(nil, nil, nil),
	}

	if _, err := service.coreManager.Register(context.Background(), &coreauth.Auth{
		ID:             authID,
		Provider:       "claude",
		Status:         coreauth.StatusError,
		StatusMessage:  "cooldown",
		Unavailable:    true,
		NextRetryAfter: nextRetryAfter,
		LastError:      &coreauth.Error{Code: "rate_limit", Message: "retry later", Retryable: true, HTTPStatus: 429},
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	service.startAuthRuntimePersistence(context.Background())
	if err := service.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	loaded, err := coreauth.LoadRuntimeSnapshotFromFile(service.authRuntimeSnapshotPath())
	if err != nil {
		t.Fatalf("LoadRuntimeSnapshotFromFile() error = %v", err)
	}
	state := loaded.Auths[authID]
	if state == nil {
		t.Fatalf("expected persisted auth runtime snapshot")
	}
	if !state.Unavailable {
		t.Fatalf("expected persisted snapshot to keep unavailable state")
	}
	if !state.NextRetryAfter.Equal(nextRetryAfter) {
		t.Fatalf("state.NextRetryAfter = %v, want %v", state.NextRetryAfter, nextRetryAfter)
	}
}

type stubTokenClientProvider struct{}

func (stubTokenClientProvider) Load(context.Context, *config.Config) (*TokenClientResult, error) {
	return &TokenClientResult{}, nil
}

type stubAPIKeyClientProvider struct{}

func (stubAPIKeyClientProvider) Load(context.Context, *config.Config) (*APIKeyClientResult, error) {
	return &APIKeyClientResult{}, nil
}

func stubWatcherFactory(configPath, authDir string, reload func(*config.Config)) (*WatcherWrapper, error) {
	return &WatcherWrapper{}, nil
}

type testCoreAuthStore struct {
	mu    sync.Mutex
	items []*coreauth.Auth
}

func (s *testCoreAuthStore) List(context.Context) ([]*coreauth.Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*coreauth.Auth, 0, len(s.items))
	for _, item := range s.items {
		out = append(out, item.Clone())
	}
	return out, nil
}

func (s *testCoreAuthStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	if auth == nil {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.items {
		if item != nil && item.ID == auth.ID {
			s.items[i] = auth.Clone()
			return auth.ID, nil
		}
	}
	s.items = append(s.items, auth.Clone())
	return auth.ID, nil
}

func (s *testCoreAuthStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := s.items[:0]
	for _, item := range s.items {
		if item == nil || item.ID == id {
			continue
		}
		filtered = append(filtered, item)
	}
	s.items = filtered
	return nil
}
