package cliproxy

import (
	"context"
	"errors"
	"path/filepath"
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
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "restore-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 4, 17, 10, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})
	if err := internalusage.SaveRequestStatisticsToFile(snapshotPath, stats); err != nil {
		t.Fatalf("SaveRequestStatisticsToFile() error = %v", err)
	}

	service := &Service{configPath: configPath}
	service.restoreUsageSnapshot()

	snapshot := internalusage.GetRequestStatistics().Snapshot()
	if snapshot.TotalRequests != 1 {
		t.Fatalf("total requests = %d, want 1", snapshot.TotalRequests)
	}
	if snapshot.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want 30", snapshot.TotalTokens)
	}
	if got := len(snapshot.APIs["restore-key"].Models["gpt-5.4"].Details); got != 1 {
		t.Fatalf("details len = %d, want 1", got)
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
