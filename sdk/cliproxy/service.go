// Package cliproxy provides the core service implementation for the CLI Proxy API.
// It includes service lifecycle management, authentication handling, file watching,
// and integration with various AI service providers through a unified interface.
package cliproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/NGLSL/CLIProxyAPI/v7/internal/api"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/home"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/registry"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/runtime/executor"
	internalusage "github.com/NGLSL/CLIProxyAPI/v7/internal/usage"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/watcher"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/wsrelay"
	sdkaccess "github.com/NGLSL/CLIProxyAPI/v7/sdk/access"
	sdkAuth "github.com/NGLSL/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/NGLSL/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreusage "github.com/NGLSL/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/NGLSL/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/NGLSL/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// Service wraps the proxy server lifecycle so external programs can embed the CLI proxy.
// It manages the complete lifecycle including authentication, file watching, HTTP server,
// and integration with various AI service providers.
type Service struct {
	// cfg holds the current application configuration.
	cfg *config.Config

	// cfgMu protects concurrent access to the configuration.
	cfgMu sync.RWMutex

	// configUpdateMu serializes config updates across file watcher and Home subscriber.
	configUpdateMu sync.Mutex

	// configPath is the path to the configuration file.
	configPath string

	// tokenProvider handles loading token-based clients.
	tokenProvider TokenClientProvider

	// apiKeyProvider handles loading API key-based clients.
	apiKeyProvider APIKeyClientProvider

	// watcherFactory creates file watcher instances.
	watcherFactory WatcherFactory

	// hooks provides lifecycle callbacks.
	hooks Hooks

	// serverOptions contains additional server configuration options.
	serverOptions []api.ServerOption

	// server is the HTTP API server instance.
	server *api.Server

	// pprofServer manages the optional pprof HTTP debug server.
	pprofServer *pprofServer

	// serverErr channel for server startup/shutdown errors.
	serverErr chan error

	// watcher handles file system monitoring.
	watcher *WatcherWrapper

	// watcherCancel cancels the watcher context.
	watcherCancel context.CancelFunc

	// authUpdates channel for authentication updates.
	authUpdates chan watcher.AuthUpdate

	// authQueueStop cancels the auth update queue processing.
	authQueueStop context.CancelFunc

	// authManager handles legacy authentication operations.
	authManager *sdkAuth.Manager

	// accessManager handles request authentication providers.
	accessManager *sdkaccess.Manager

	// coreManager handles core authentication and execution.
	coreManager *coreauth.Manager

	// pluginHost owns dynamic plugin lifecycle and runtime capability adapters.
	pluginHost *pluginhost.Host

	// shutdownOnce ensures shutdown is called only once.
	shutdownOnce sync.Once

	// wsGateway manages websocket Gemini providers.
	wsGateway *wsrelay.Manager

	homeClient *home.Client
	homeCancel context.CancelFunc

	usagePersistenceCancel       context.CancelFunc
	usagePersistenceDone         chan struct{}
	authRuntimePersistenceCancel context.CancelFunc
	authRuntimePersistenceDone   chan struct{}
	authRuntimeSnapshotMu        sync.RWMutex
	authRuntimeSnapshot          coreauth.RuntimeSnapshot
}

const usagePersistenceInterval = time.Minute
const authRuntimePersistenceInterval = time.Minute

// RegisterUsagePlugin registers a usage plugin on the global usage manager.
// This allows external code to monitor API usage and token consumption.
//
// Parameters:
//   - plugin: The usage plugin to register
func (s *Service) RegisterUsagePlugin(plugin coreusage.Plugin) {
	coreusage.RegisterPlugin(plugin)
}

func (s *Service) registerPluginAuthParser() {
	var parser PluginAuthParser
	if s != nil && s.pluginHost != nil {
		parser = s.pluginHost
	}
	sdkAuth.RegisterPluginAuthParser(parser)
	if s != nil && s.watcher != nil {
		s.watcher.SetPluginAuthParser(parser)
	}
}

func (s *Service) syncPluginRuntime(ctx context.Context) {
	if !s.syncPluginRuntimeConfig(ctx) {
		return
	}
	s.syncPluginModelRuntime(ctx)
}

func (s *Service) syncPluginRuntimeConfig(ctx context.Context) bool {
	if s == nil {
		sdkAuth.RegisterPluginAuthParser(nil)
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()

	if s.pluginHost != nil {
		s.pluginHost.ApplyConfig(ctx, cfg)
	}
	if s.coreManager != nil {
		s.coreManager.SetPluginScheduler(s.pluginHost)
	}
	s.registerPluginAuthParser()
	if s.pluginHost == nil {
		return false
	}
	s.pluginHost.RegisterFrontendAuthProviders()
	if s.accessManager != nil {
		s.accessManager.SetProviders(sdkaccess.RegisteredProviders())
	}
	s.pluginHost.RegisterUsagePlugins()
	sdktranslator.SetPluginHooks(s.pluginHost)
	if s.server != nil {
		s.server.RefreshPluginManagementRoutes()
	}
	return true
}

func (s *Service) syncPluginModelRuntime(ctx context.Context) {
	if s == nil || s.pluginHost == nil || s.coreManager == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.pluginHost.RegisterModels(ctx, registry.GetGlobalRegistry())
	s.pluginHost.RegisterExecutors(s.coreManager, registry.GetGlobalRegistry())
	s.refreshPluginModelRegistrations(ctx)
	for _, auth := range s.coreManager.List() {
		if auth != nil && auth.ID != "" {
			s.coreManager.RefreshSchedulerEntry(auth.ID)
		}
	}
}

func (s *Service) refreshPluginModelRegistrations(ctx context.Context) {
	if s == nil || s.pluginHost == nil || s.coreManager == nil {
		return
	}
	for _, auth := range s.coreManager.List() {
		if auth == nil || auth.ID == "" {
			continue
		}
		s.registerModelsForAuth(ctx, auth)
	}
}

// newDefaultAuthManager creates a default authentication manager with all supported providers.
func newDefaultAuthManager() *sdkAuth.Manager {
	return sdkAuth.NewManager(
		sdkAuth.GetTokenStore(),
		sdkAuth.NewGeminiAuthenticator(),
		sdkAuth.NewCodexAuthenticator(),
		sdkAuth.NewClaudeAuthenticator(),
		sdkAuth.NewXAIAuthenticator(),
	)
}

func (s *Service) ensureAuthUpdateQueue(ctx context.Context) {
	if s == nil {
		return
	}
	if s.authUpdates == nil {
		s.authUpdates = make(chan watcher.AuthUpdate, 256)
	}
	if s.authQueueStop != nil {
		return
	}
	queueCtx, cancel := context.WithCancel(ctx)
	s.authQueueStop = cancel
	go s.consumeAuthUpdates(queueCtx)
}

func (s *Service) consumeAuthUpdates(ctx context.Context) {
	ctx = coreauth.WithSkipPersist(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-s.authUpdates:
			if !ok {
				return
			}
			s.handleAuthUpdate(ctx, update)
		labelDrain:
			for {
				select {
				case nextUpdate := <-s.authUpdates:
					s.handleAuthUpdate(ctx, nextUpdate)
				default:
					break labelDrain
				}
			}
		}
	}
}

func (s *Service) emitAuthUpdate(ctx context.Context, update watcher.AuthUpdate) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.watcher != nil && s.watcher.DispatchRuntimeAuthUpdate(update) {
		return
	}
	if s.authUpdates != nil {
		select {
		case s.authUpdates <- update:
			return
		default:
			log.Debugf("auth update queue saturated, applying inline action=%v id=%s", update.Action, update.ID)
		}
	}
	s.handleAuthUpdate(ctx, update)
}

func (s *Service) handleAuthUpdate(ctx context.Context, update watcher.AuthUpdate) {
	if s == nil {
		return
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if cfg == nil || s.coreManager == nil {
		return
	}
	switch update.Action {
	case watcher.AuthUpdateActionAdd, watcher.AuthUpdateActionModify:
		if update.Auth == nil || update.Auth.ID == "" {
			return
		}
		s.applyCoreAuthAddOrUpdate(ctx, update.Auth)
	case watcher.AuthUpdateActionDelete:
		id := update.ID
		if id == "" && update.Auth != nil {
			id = update.Auth.ID
		}
		if id == "" {
			return
		}
		s.applyCoreAuthRemoval(ctx, id)
	default:
		log.Debugf("received unknown auth update action: %v", update.Action)
	}
}

func (s *Service) usageSnapshotPath() string {
	if s == nil {
		return ""
	}
	return internalusage.DefaultSnapshotPath(s.configPath)
}

func (s *Service) authRuntimeSnapshotPath() string {
	if s == nil {
		return ""
	}
	return coreauth.DefaultRuntimeSnapshotPath(s.configPath)
}

func (s *Service) configureUsageStatisticsEnabled() bool {
	enabled := s != nil && s.cfg != nil && s.cfg.UsageStatisticsEnabled
	internalusage.SetStatisticsEnabled(enabled)
	return enabled
}

func (s *Service) restoreUsageSnapshot() {
	if s == nil || !internalusage.StatisticsEnabled() {
		return
	}

	path := s.usageSnapshotPath()
	if path == "" {
		return
	}

	snapshot, err := internalusage.LoadSnapshotFromFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		log.Warnf("failed to restore usage snapshot from %s: %v", path, err)
		return
	}

	stats := internalusage.GetRequestStatistics()
	current := stats.Snapshot()
	if current.TotalRequests == 0 && len(current.APIs) == 0 {
		result := stats.RestoreSnapshot(snapshot)
		if result.Requests > 0 || result.Details > 0 {
			log.Infof("restored usage snapshot from %s (requests=%d details=%d)", path, result.Requests, result.Details)
		}
		return
	}

	if usageSnapshotHasHigherAggregate(snapshot, current) {
		merged := mergeUsageSnapshots(snapshot, current)
		result := stats.RestoreSnapshot(merged)
		log.Infof(
			"restored larger usage snapshot from %s before merging current usage (requests=%d details=%d)",
			path,
			result.Requests,
			result.Details,
		)
		return
	}

	result := stats.MergeSnapshot(snapshot)
	if result.Added > 0 || result.Skipped > 0 {
		log.Infof("merged usage snapshot from %s (added=%d skipped=%d)", path, result.Added, result.Skipped)
	}
}

func (s *Service) persistUsageSnapshot() error {
	if s == nil || !internalusage.StatisticsEnabled() {
		return nil
	}

	path := s.usageSnapshotPath()
	if path == "" {
		return nil
	}

	stats := internalusage.GetRequestStatistics()
	snapshot := stats.Snapshot()
	protectedSnapshot, err := s.protectUsageSnapshotBeforePersist(path, stats, snapshot)
	if err != nil {
		return fmt.Errorf("protect usage snapshot before persist to %s: %w", path, err)
	}

	if err := internalusage.SaveSnapshotToFile(path, protectedSnapshot); err != nil {
		return fmt.Errorf("persist usage snapshot to %s: %w", path, err)
	}
	return nil
}

func (s *Service) protectUsageSnapshotBeforePersist(path string, stats *internalusage.RequestStatistics, current internalusage.StatisticsSnapshot) (internalusage.StatisticsSnapshot, error) {
	existing, err := internalusage.LoadSnapshotFromFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return current, nil
		}
		return current, err
	}

	if !usageSnapshotHasHigherAggregate(existing, current) {
		return current, nil
	}

	merged := mergeUsageSnapshots(existing, current)
	if stats != nil {
		result := stats.RestoreSnapshot(merged)
		log.Warnf(
			"preserved larger usage snapshot from %s before persist (requests=%d details=%d)",
			path,
			result.Requests,
			result.Details,
		)
		return stats.Snapshot(), nil
	}

	return merged, nil
}

func usageSnapshotHasHigherAggregate(candidate, current internalusage.StatisticsSnapshot) bool {
	candidateRequests, candidateTokens := usageSnapshotAggregate(candidate)
	currentRequests, currentTokens := usageSnapshotAggregate(current)
	return candidateRequests > currentRequests || candidateTokens > currentTokens
}

func usageSnapshotAggregate(snapshot internalusage.StatisticsSnapshot) (int64, int64) {
	requests := nonNegativeUsageValue(snapshot.TotalRequests)
	tokens := nonNegativeUsageValue(snapshot.TotalTokens)

	var apiRequests int64
	var apiTokens int64
	for _, apiSnapshot := range snapshot.APIs {
		modelRequests, modelTokens := usageModelAggregate(apiSnapshot.Models)
		apiRequests += maxUsageValue(apiSnapshot.TotalRequests, modelRequests)
		apiTokens += maxUsageValue(apiSnapshot.TotalTokens, modelTokens)
	}
	if apiRequests > requests {
		requests = apiRequests
	}
	if apiTokens > tokens {
		tokens = apiTokens
	}

	return requests, tokens
}

func usageModelAggregate(models map[string]internalusage.ModelSnapshot) (int64, int64) {
	var requests int64
	var tokens int64
	for _, modelSnapshot := range models {
		requests += nonNegativeUsageValue(modelSnapshot.TotalRequests)
		tokens += nonNegativeUsageValue(modelSnapshot.TotalTokens)
	}
	return requests, tokens
}

func mergeUsageSnapshots(base, delta internalusage.StatisticsSnapshot) internalusage.StatisticsSnapshot {
	base = normaliseUsageSnapshot(base)
	delta = normaliseUsageSnapshot(delta)

	merged := internalusage.StatisticsSnapshot{
		TotalRequests:  base.TotalRequests + delta.TotalRequests,
		SuccessCount:   base.SuccessCount + delta.SuccessCount,
		FailureCount:   base.FailureCount + delta.FailureCount,
		TotalTokens:    base.TotalTokens + delta.TotalTokens,
		APIs:           make(map[string]internalusage.APISnapshot, len(base.APIs)+len(delta.APIs)),
		RequestsByDay:  addUsageMaps(base.RequestsByDay, delta.RequestsByDay),
		RequestsByHour: addUsageMaps(base.RequestsByHour, delta.RequestsByHour),
		TokensByDay:    addUsageMaps(base.TokensByDay, delta.TokensByDay),
		TokensByHour:   addUsageMaps(base.TokensByHour, delta.TokensByHour),
	}

	for apiName, apiSnapshot := range base.APIs {
		merged.APIs[apiName] = cloneUsageAPISnapshot(apiSnapshot)
	}
	for apiName, apiSnapshot := range delta.APIs {
		merged.APIs[apiName] = mergeUsageAPISnapshots(merged.APIs[apiName], apiSnapshot)
	}

	return normaliseUsageSnapshot(merged)
}

func normaliseUsageSnapshot(snapshot internalusage.StatisticsSnapshot) internalusage.StatisticsSnapshot {
	stats := internalusage.NewRequestStatistics()
	stats.RestoreSnapshot(snapshot)
	return stats.Snapshot()
}

func cloneUsageAPISnapshot(snapshot internalusage.APISnapshot) internalusage.APISnapshot {
	cloned := internalusage.APISnapshot{
		TotalRequests: snapshot.TotalRequests,
		TotalTokens:   snapshot.TotalTokens,
		Models:        make(map[string]internalusage.ModelSnapshot, len(snapshot.Models)),
	}
	for modelName, modelSnapshot := range snapshot.Models {
		cloned.Models[modelName] = cloneUsageModelSnapshot(modelSnapshot)
	}
	return cloned
}

func mergeUsageAPISnapshots(base, delta internalusage.APISnapshot) internalusage.APISnapshot {
	merged := cloneUsageAPISnapshot(base)
	merged.TotalRequests += delta.TotalRequests
	merged.TotalTokens += delta.TotalTokens
	if merged.Models == nil {
		merged.Models = make(map[string]internalusage.ModelSnapshot, len(delta.Models))
	}
	for modelName, modelSnapshot := range delta.Models {
		merged.Models[modelName] = mergeUsageModelSnapshots(merged.Models[modelName], modelSnapshot)
	}
	return merged
}

func cloneUsageModelSnapshot(snapshot internalusage.ModelSnapshot) internalusage.ModelSnapshot {
	return internalusage.ModelSnapshot{
		TotalRequests: snapshot.TotalRequests,
		TotalTokens:   snapshot.TotalTokens,
		Details:       copyUsageDetails(snapshot.Details),
	}
}

func mergeUsageModelSnapshots(base, delta internalusage.ModelSnapshot) internalusage.ModelSnapshot {
	return internalusage.ModelSnapshot{
		TotalRequests: base.TotalRequests + delta.TotalRequests,
		TotalTokens:   base.TotalTokens + delta.TotalTokens,
		Details:       mergeUsageDetails(base.Details, delta.Details),
	}
}

func mergeUsageDetails(base, delta []internalusage.RequestDetail) []internalusage.RequestDetail {
	if len(base) == 0 {
		return copyUsageDetails(delta)
	}
	if len(delta) == 0 {
		return copyUsageDetails(base)
	}

	seen := make(map[string]struct{}, len(base)+len(delta))
	merged := make([]internalusage.RequestDetail, 0, len(base)+len(delta))
	for _, details := range [][]internalusage.RequestDetail{base, delta} {
		for _, detail := range details {
			key := usageDetailKey(detail)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, detail)
		}
	}
	return merged
}

func usageDetailKey(detail internalusage.RequestDetail) string {
	return fmt.Sprintf(
		"%s|%s|%s|%t|%d|%d|%d|%d|%d",
		detail.Timestamp.UTC().Format(time.RFC3339Nano),
		detail.Source,
		detail.AuthIndex,
		detail.Failed,
		detail.Tokens.InputTokens,
		detail.Tokens.OutputTokens,
		detail.Tokens.ReasoningTokens,
		detail.Tokens.CachedTokens,
		detail.Tokens.TotalTokens,
	)
}

func copyUsageDetails(source []internalusage.RequestDetail) []internalusage.RequestDetail {
	if len(source) == 0 {
		return nil
	}
	copied := make([]internalusage.RequestDetail, len(source))
	copy(copied, source)
	return copied
}

func addUsageMaps(base, delta map[string]int64) map[string]int64 {
	merged := make(map[string]int64, len(base)+len(delta))
	for key, value := range base {
		merged[key] = nonNegativeUsageValue(value)
	}
	for key, value := range delta {
		merged[key] += nonNegativeUsageValue(value)
	}
	return merged
}

func maxUsageValue(value, minimum int64) int64 {
	value = nonNegativeUsageValue(value)
	minimum = nonNegativeUsageValue(minimum)
	if value < minimum {
		return minimum
	}
	return value
}

func nonNegativeUsageValue(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func (s *Service) setAuthRuntimeSnapshot(snapshot coreauth.RuntimeSnapshot) {
	if s == nil {
		return
	}
	s.authRuntimeSnapshotMu.Lock()
	defer s.authRuntimeSnapshotMu.Unlock()
	s.authRuntimeSnapshot = snapshot.Clone()
}

func (s *Service) authRuntimeSnapshotState() coreauth.RuntimeSnapshot {
	if s == nil {
		return coreauth.RuntimeSnapshot{}
	}
	s.authRuntimeSnapshotMu.RLock()
	defer s.authRuntimeSnapshotMu.RUnlock()
	return s.authRuntimeSnapshot.Clone()
}

func (s *Service) restoreAuthRuntimeSnapshot() coreauth.RuntimeSnapshot {
	if s == nil {
		return coreauth.RuntimeSnapshot{}
	}

	path := s.authRuntimeSnapshotPath()
	if path == "" {
		return coreauth.RuntimeSnapshot{}
	}

	snapshot, err := coreauth.LoadRuntimeSnapshotFromFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return coreauth.RuntimeSnapshot{}
		}
		log.Warnf("failed to restore auth runtime snapshot from %s: %v", path, err)
		return coreauth.RuntimeSnapshot{}
	}

	s.setAuthRuntimeSnapshot(snapshot)
	if snapshot.Len() > 0 {
		log.Infof("restored auth runtime snapshot from %s (auths=%d)", path, snapshot.Len())
	}
	return snapshot
}

func (s *Service) applyAuthRuntimeSnapshot(snapshot coreauth.RuntimeSnapshot) []string {
	if s == nil || s.coreManager == nil || snapshot.Len() == 0 {
		return nil
	}
	applied := s.coreManager.ApplyRuntimeSnapshot(snapshot, time.Now())
	if len(applied) == 0 {
		return nil
	}
	for _, authID := range applied {
		s.coreManager.RefreshSchedulerEntry(authID)
	}
	return applied
}

func (s *Service) persistAuthRuntimeSnapshot() error {
	if s == nil || s.coreManager == nil {
		return nil
	}

	path := s.authRuntimeSnapshotPath()
	if path == "" {
		return nil
	}

	snapshot := s.coreManager.ExportRuntimeSnapshot(time.Now())
	s.setAuthRuntimeSnapshot(snapshot)
	if err := coreauth.SaveRuntimeSnapshotToFile(path, snapshot); err != nil {
		return fmt.Errorf("persist auth runtime snapshot to %s: %w", path, err)
	}
	return nil
}

func (s *Service) startAuthRuntimePersistence(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	if s.authRuntimeSnapshotPath() == "" || s.authRuntimePersistenceCancel != nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.authRuntimePersistenceCancel = cancel
	s.authRuntimePersistenceDone = done
	go func() {
		defer close(done)
		ticker := time.NewTicker(authRuntimePersistenceInterval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := s.persistAuthRuntimeSnapshot(); err != nil {
					log.Warnf("failed to persist auth runtime snapshot: %v", err)
				}
			}
		}
	}()
}

func (s *Service) stopAuthRuntimePersistence() {
	if s == nil {
		return
	}
	if s.authRuntimePersistenceCancel != nil {
		s.authRuntimePersistenceCancel()
		s.authRuntimePersistenceCancel = nil
	}
	if s.authRuntimePersistenceDone != nil {
		<-s.authRuntimePersistenceDone
		s.authRuntimePersistenceDone = nil
	}
}

func (s *Service) flushAuthRuntimePersist() {
	if s == nil {
		return
	}
	if err := s.persistAuthRuntimeSnapshot(); err != nil {
		log.Warnf("failed to persist auth runtime snapshot during shutdown: %v", err)
	}
}

func (s *Service) restoreAuthRuntimeSnapshotForAuth(authID string) bool {
	if s == nil || s.coreManager == nil || authID == "" {
		return false
	}
	snapshot := s.authRuntimeSnapshotState()
	state, ok := snapshot.Auths[authID]
	if !ok || state == nil {
		return false
	}
	applied := s.applyAuthRuntimeSnapshot(coreauth.RuntimeSnapshot{Auths: map[string]*coreauth.AuthRuntimeState{authID: state}})
	return len(applied) > 0
}

func (s *Service) restoreAuthRuntimeSnapshotForWatcherAuths(auths []*coreauth.Auth) {
	if s == nil || len(auths) == 0 {
		return
	}
	for _, auth := range auths {
		if auth == nil || auth.ID == "" {
			continue
		}
		s.restoreAuthRuntimeSnapshotForAuth(auth.ID)
	}
}

func (s *Service) reapplyAllModelRegistrations() {
	if s == nil || s.coreManager == nil {
		return
	}
	for _, auth := range s.coreManager.List() {
		if auth == nil || auth.ID == "" {
			continue
		}
		s.registerModelsForAuth(context.Background(), auth)
		s.coreManager.RefreshSchedulerEntry(auth.ID)
	}
}

func (s *Service) restoreWatcherSnapshotAuths() {
	if s == nil || s.watcher == nil {
		return
	}
	auths := s.watcher.SnapshotAuths()
	if len(auths) == 0 {
		return
	}
	for _, auth := range auths {
		if auth == nil || auth.ID == "" {
			continue
		}
		s.applyCoreAuthAddOrUpdate(context.Background(), auth)
	}
	s.restoreAuthRuntimeSnapshotForWatcherAuths(auths)
}

func (s *Service) startUsagePersistence(ctx context.Context) {
	if s == nil || !internalusage.StatisticsEnabled() {
		return
	}
	if s.usageSnapshotPath() == "" || s.usagePersistenceCancel != nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.usagePersistenceCancel = cancel
	s.usagePersistenceDone = done
	go func() {
		defer close(done)
		ticker := time.NewTicker(usagePersistenceInterval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := s.persistUsageSnapshot(); err != nil {
					log.Warnf("failed to persist usage snapshot: %v", err)
				}
			}
		}
	}()
}

func (s *Service) stopUsagePersistence() {
	if s == nil {
		return
	}
	if s.usagePersistenceCancel != nil {
		s.usagePersistenceCancel()
		s.usagePersistenceCancel = nil
	}
	if s.usagePersistenceDone != nil {
		<-s.usagePersistenceDone
		s.usagePersistenceDone = nil
	}
}

func (s *Service) flushUsageAndPersist() {
	if s == nil {
		return
	}
	manager := coreusage.DefaultManager()
	if manager != nil {
		manager.Stop()
	}
	if err := s.persistUsageSnapshot(); err != nil {
		log.Warnf("failed to persist usage snapshot during shutdown: %v", err)
	}
}

func (s *Service) ensureWebsocketGateway() {
	if s == nil {
		return
	}
	if s.wsGateway != nil {
		return
	}
	opts := wsrelay.Options{
		Path:           "/v1/ws",
		OnConnected:    s.wsOnConnected,
		OnDisconnected: s.wsOnDisconnected,
		LogDebugf:      log.Debugf,
		LogInfof:       log.Infof,
		LogWarnf:       log.Warnf,
	}
	s.wsGateway = wsrelay.NewManager(opts)
}

func (s *Service) wsOnConnected(channelID string) {
	if s == nil || channelID == "" {
		return
	}
	if !strings.HasPrefix(strings.ToLower(channelID), "aistudio-") {
		return
	}
	if s.coreManager != nil {
		if existing, ok := s.coreManager.GetByID(channelID); ok && existing != nil {
			if !existing.Disabled && existing.Status == coreauth.StatusActive {
				return
			}
		}
	}
	now := time.Now().UTC()
	auth := &coreauth.Auth{
		ID:         channelID,  // keep channel identifier as ID
		Provider:   "aistudio", // logical provider for switch routing
		Label:      channelID,  // display original channel id
		Status:     coreauth.StatusActive,
		CreatedAt:  now,
		UpdatedAt:  now,
		Attributes: map[string]string{"runtime_only": "true"},
		Metadata:   map[string]any{"email": channelID}, // metadata drives logging and usage tracking
	}
	log.Infof("websocket provider connected: %s", channelID)
	s.emitAuthUpdate(context.Background(), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionAdd,
		ID:     auth.ID,
		Auth:   auth,
	})
}

func (s *Service) wsOnDisconnected(channelID string, reason error) {
	if s == nil || channelID == "" {
		return
	}
	if reason != nil {
		if strings.Contains(reason.Error(), "replaced by new connection") {
			log.Infof("websocket provider replaced: %s", channelID)
			return
		}
		log.Warnf("websocket provider disconnected: %s (%v)", channelID, reason)
	} else {
		log.Infof("websocket provider disconnected: %s", channelID)
	}
	ctx := context.Background()
	s.emitAuthUpdate(ctx, watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionDelete,
		ID:     channelID,
	})
}

func (s *Service) applyCoreAuthAddOrUpdate(ctx context.Context, auth *coreauth.Auth) {
	if s == nil || s.coreManager == nil || auth == nil || auth.ID == "" {
		return
	}
	auth = auth.Clone()
	s.ensureExecutorsForAuth(auth)

	// IMPORTANT: Update coreManager FIRST, before model registration.
	// This ensures that configuration changes (proxy_url, prefix, etc.) take effect
	// immediately for API calls, rather than waiting for model registration to complete.
	op := "register"
	var err error
	if existing, ok := s.coreManager.GetByID(auth.ID); ok {
		auth.CreatedAt = existing.CreatedAt
		if !existing.Disabled && existing.Status != coreauth.StatusDisabled && !auth.Disabled && auth.Status != coreauth.StatusDisabled {
			auth.LastRefreshedAt = existing.LastRefreshedAt
			auth.NextRefreshAfter = existing.NextRefreshAfter
			if len(auth.ModelStates) == 0 && len(existing.ModelStates) > 0 {
				auth.ModelStates = existing.ModelStates
			}
		}
		op = "update"
		_, err = s.coreManager.Update(ctx, auth)
	} else {
		_, err = s.coreManager.Register(ctx, auth)
	}
	if err != nil {
		log.Errorf("failed to %s auth %s: %v", op, auth.ID, err)
		current, ok := s.coreManager.GetByID(auth.ID)
		if !ok || current.Disabled {
			GlobalModelRegistry().UnregisterClient(auth.ID)
			return
		}
		auth = current
	}

	// Register models after auth is updated in coreManager.
	// This operation may block on network calls, but the auth configuration
	// is already effective at this point.
	s.registerModelsForAuth(ctx, auth)
	s.coreManager.ReconcileRegistryModelStates(ctx, auth.ID)

	// Refresh the scheduler entry so that the auth's supportedModelSet is rebuilt
	// from the now-populated global model registry. Without this, newly added auths
	// have an empty supportedModelSet (because Register/Update upserts into the
	// scheduler before registerModelsForAuth runs) and are invisible to the scheduler.
	s.coreManager.RefreshSchedulerEntry(auth.ID)
	s.syncPluginRuntime(ctx)
}

func (s *Service) applyCoreAuthRemoval(ctx context.Context, id string) {
	if s == nil || id == "" {
		return
	}
	if s.coreManager == nil {
		return
	}
	GlobalModelRegistry().UnregisterClient(id)
	if existing, ok := s.coreManager.GetByID(id); ok && existing != nil {
		existing.Disabled = true
		existing.Status = coreauth.StatusDisabled
		if _, err := s.coreManager.Update(ctx, existing); err != nil {
			log.Errorf("failed to disable auth %s: %v", id, err)
		}
		if strings.EqualFold(strings.TrimSpace(existing.Provider), "codex") {
			executor.CloseCodexWebsocketSessionsForAuthID(existing.ID, "auth_removed")
			s.ensureExecutorsForAuth(existing)
		}
	}
	s.syncPluginRuntime(ctx)
}

func (s *Service) applyRetryConfig(cfg *config.Config) {
	if s == nil || s.coreManager == nil || cfg == nil {
		return
	}
	maxInterval := time.Duration(cfg.MaxRetryInterval) * time.Second
	s.coreManager.SetRetryConfig(cfg.RequestRetry, maxInterval, cfg.MaxRetryCredentials)
}

func openAICompatInfoFromAuth(a *coreauth.Auth) (providerKey string, compatName string, ok bool) {
	if a == nil {
		return "", "", false
	}
	if len(a.Attributes) > 0 {
		providerKey = strings.TrimSpace(a.Attributes["provider_key"])
		compatName = strings.TrimSpace(a.Attributes["compat_name"])
		if compatName != "" {
			if providerKey == "" {
				providerKey = compatName
			}
			return strings.ToLower(providerKey), compatName, true
		}
	}
	if strings.EqualFold(strings.TrimSpace(a.Provider), "openai-compatibility") {
		return "openai-compatibility", strings.TrimSpace(a.Label), true
	}
	return "", "", false
}

func (s *Service) hasNativeOpenAICompatExecutorConfig(a *coreauth.Auth, providerKey string) bool {
	if a == nil {
		return false
	}
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	if a.Attributes != nil {
		if strings.TrimSpace(a.Attributes["base_url"]) != "" {
			return true
		}
		if strings.TrimSpace(a.Attributes["compat_name"]) != "" {
			return true
		}
	}
	if strings.EqualFold(strings.TrimSpace(a.Provider), "openai-compatibility") {
		return true
	}
	if s == nil || s.cfg == nil {
		return false
	}

	candidates := make([]string, 0, 3)
	if providerKey != "" {
		candidates = append(candidates, providerKey)
	}
	if a.Attributes != nil {
		if v := strings.TrimSpace(a.Attributes["provider_key"]); v != "" {
			candidates = append(candidates, strings.ToLower(v))
		}
	}
	if provider := strings.TrimSpace(a.Provider); provider != "" {
		candidates = append(candidates, strings.ToLower(provider))
	}

	for i := range s.cfg.OpenAICompatibility {
		compat := &s.cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(compat.Name))
		if name == "" {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && candidate == name {
				return true
			}
		}
	}
	return false
}

func (s *Service) ensureExecutorsForAuth(a *coreauth.Auth) {
	s.ensureExecutorsForAuthWithMode(a, false)
}

func (s *Service) ensureExecutorsForAuthWithMode(a *coreauth.Auth, forceReplace bool) {
	if s == nil || s.coreManager == nil || a == nil {
		return
	}
	if strings.EqualFold(strings.TrimSpace(a.Provider), "codex") {
		if !forceReplace {
			existingExecutor, hasExecutor := s.coreManager.Executor("codex")
			if hasExecutor {
				_, isCodexAutoExecutor := existingExecutor.(*executor.CodexAutoExecutor)
				if isCodexAutoExecutor {
					return
				}
			}
		}
		s.coreManager.RegisterExecutor(executor.NewCodexAutoExecutor(s.cfg))
		return
	}
	// Skip disabled auth entries when (re)binding executors.
	// Disabled auths can linger during config reloads (e.g., removed OpenAI-compat entries)
	// and must not override active provider executors.
	if a.Disabled {
		return
	}
	if compatProviderKey, _, isCompat := openAICompatInfoFromAuth(a); isCompat {
		if compatProviderKey == "" {
			compatProviderKey = strings.ToLower(strings.TrimSpace(a.Provider))
		}
		if compatProviderKey == "" {
			compatProviderKey = "openai-compatibility"
		}
		s.coreManager.RegisterExecutor(executor.NewOpenAICompatExecutor(compatProviderKey, s.cfg))
		return
	}
	switch strings.ToLower(a.Provider) {
	case "gemini":
		s.coreManager.RegisterExecutor(executor.NewGeminiExecutor(s.cfg))
	case "vertex":
		s.coreManager.RegisterExecutor(executor.NewGeminiVertexExecutor(s.cfg))
	case "gemini-cli":
		s.coreManager.RegisterExecutor(executor.NewGeminiCLIExecutor(s.cfg))
	case "aistudio":
		if s.wsGateway != nil {
			s.coreManager.RegisterExecutor(executor.NewAIStudioExecutor(s.cfg, a.ID, s.wsGateway))
		}
		return
	case "antigravity":
		s.coreManager.RegisterExecutor(executor.NewAntigravityExecutor(s.cfg))
	case "claude":
		s.coreManager.RegisterExecutor(executor.NewClaudeExecutor(s.cfg))
	case "kimi":
		s.coreManager.RegisterExecutor(executor.NewKimiExecutor(s.cfg))
	case "xai":
		s.coreManager.RegisterExecutor(executor.NewXAIExecutor(s.cfg))
	default:
		providerKey := strings.ToLower(strings.TrimSpace(a.Provider))
		if providerKey == "" {
			providerKey = "openai-compatibility"
		}
		if s.pluginHost != nil && s.pluginHost.HasExecutorCandidateProvider(providerKey) && !s.hasNativeOpenAICompatExecutorConfig(a, providerKey) {
			return
		}
		s.coreManager.RegisterExecutor(executor.NewOpenAICompatExecutor(providerKey, s.cfg))
	}
}

func (s *Service) registerResolvedModelsForAuth(a *coreauth.Auth, providerKey string, models []*ModelInfo) {
	if a == nil || a.ID == "" {
		return
	}
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	if providerKey == "" {
		GlobalModelRegistry().UnregisterClient(a.ID)
		return
	}
	normalizedModels := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		clone := *model
		clone.ID = modelID
		normalizedModels = append(normalizedModels, &clone)
	}
	if len(normalizedModels) == 0 {
		GlobalModelRegistry().UnregisterClient(a.ID)
		return
	}
	GlobalModelRegistry().RegisterClient(a.ID, providerKey, normalizedModels)
}

func (s *Service) pluginModelsForProvider(providerKey string) []*ModelInfo {
	if s == nil || s.pluginHost == nil {
		return nil
	}
	return s.pluginHost.ModelsForProvider(providerKey)
}

func (s *Service) appendPluginModels(providerKey string, models []*ModelInfo) []*ModelInfo {
	pluginModels := s.pluginModelsForProvider(providerKey)
	if len(pluginModels) == 0 {
		return models
	}
	out := make([]*ModelInfo, 0, len(models)+len(pluginModels))
	seen := make(map[string]struct{}, len(models)+len(pluginModels))
	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if modelID != "" {
			seen[modelID] = struct{}{}
		}
		out = append(out, model)
	}
	for _, model := range pluginModels {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		if _, exists := seen[modelID]; exists {
			continue
		}
		seen[modelID] = struct{}{}
		out = append(out, model)
	}
	return out
}

func (s *Service) tryRegisterPluginModelsForAuth(ctx context.Context, a *coreauth.Auth, provider, authKind string, excluded []string) bool {
	if s == nil || s.pluginHost == nil || a == nil {
		return false
	}
	result := s.pluginHost.ModelsForAuth(ctx, a)
	if !result.Handled {
		return false
	}
	if result.Err != nil {
		return true
	}
	activeAuth := a
	providerKey := strings.ToLower(strings.TrimSpace(result.Provider))
	if providerKey == "" {
		providerKey = strings.ToLower(strings.TrimSpace(provider))
	}
	if result.Auth != nil && s.coreManager != nil {
		result.Auth.ID = a.ID
		if result.Auth.Provider == "" {
			result.Auth.Provider = a.Provider
		}
		if result.Auth.FileName == "" {
			result.Auth.FileName = a.FileName
		}
		if result.Auth.Attributes == nil {
			result.Auth.Attributes = make(map[string]string)
		}
		for key, value := range a.Attributes {
			if _, exists := result.Auth.Attributes[key]; !exists {
				result.Auth.Attributes[key] = value
			}
		}
		if updated, errUpdate := s.coreManager.Update(context.Background(), result.Auth); errUpdate == nil && updated != nil {
			activeAuth = updated.Clone()
		}
	}
	if activeAuth == nil {
		activeAuth = a
	}
	if activeProvider := strings.ToLower(strings.TrimSpace(activeAuth.Provider)); activeProvider != "" {
		providerKey = activeProvider
	}
	if providerKey == "" {
		providerKey = strings.ToLower(strings.TrimSpace(provider))
	}
	activeAuthKind := strings.ToLower(strings.TrimSpace(activeAuth.Attributes["auth_kind"]))
	if activeAuthKind == "" {
		if kind, _ := activeAuth.AccountInfo(); strings.EqualFold(kind, "api_key") {
			activeAuthKind = "apikey"
		}
	}
	activeExcluded := s.oauthExcludedModels(providerKey, activeAuthKind)
	if a == activeAuth && len(activeExcluded) == 0 {
		activeExcluded = excluded
	}
	if activeAuth.Attributes != nil {
		if val, ok := activeAuth.Attributes["excluded_models"]; ok && strings.TrimSpace(val) != "" {
			activeExcluded = strings.Split(val, ",")
		}
	}
	models := applyExcludedModels(result.Models, activeExcluded)
	models = applyOAuthModelAlias(s.cfg, providerKey, activeAuthKind, models)
	models = applyAllowedModelsForAuth(activeAuth, providerKey, models)
	if len(models) > 0 {
		s.registerResolvedModelsForAuth(activeAuth, providerKey, applyModelPrefixes(models, activeAuth.Prefix, s.cfg != nil && s.cfg.ForceModelPrefix))
		return true
	}
	GlobalModelRegistry().UnregisterClient(activeAuth.ID)
	return true
}

// rebindExecutors refreshes provider executors so they observe the latest configuration.
func (s *Service) rebindExecutors() {
	if s == nil || s.coreManager == nil {
		return
	}
	auths := s.coreManager.List()
	reboundCodex := false
	for _, auth := range auths {
		if auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			if reboundCodex {
				continue
			}
			reboundCodex = true
		}
		s.ensureExecutorsForAuthWithMode(auth, true)
	}
}

func (s *Service) applyConfigUpdate(newCfg *config.Config) {
	if s == nil {
		return
	}

	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()

	previousStrategy := ""
	s.cfgMu.RLock()
	if s.cfg != nil {
		previousStrategy = strings.ToLower(strings.TrimSpace(s.cfg.Routing.Strategy))
	}
	s.cfgMu.RUnlock()

	if newCfg == nil {
		s.cfgMu.RLock()
		newCfg = s.cfg
		s.cfgMu.RUnlock()
	}
	if newCfg == nil {
		return
	}
	if newCfg.Home.Enabled {
		forceHomeRuntimeConfig(newCfg)
	}

	nextStrategy := strings.ToLower(strings.TrimSpace(newCfg.Routing.Strategy))
	normalizeStrategy := func(strategy string) string {
		switch strategy {
		case "fill-first", "fillfirst", "ff":
			return "fill-first"
		case "sticky-round-robin", "sticky-roundrobin", "stickyroundrobin", "srr":
			return "sticky-round-robin"
		default:
			return "round-robin"
		}
	}
	previousStrategy = normalizeStrategy(previousStrategy)
	nextStrategy = normalizeStrategy(nextStrategy)
	if s.coreManager != nil && previousStrategy != nextStrategy {
		var selector coreauth.Selector
		switch nextStrategy {
		case "fill-first":
			selector = &coreauth.FillFirstSelector{}
		case "sticky-round-robin":
			selector = &coreauth.StickyRoundRobinSelector{}
		default:
			selector = &coreauth.RoundRobinSelector{}
		}
		s.coreManager.SetSelector(selector)
	}

	wasUsageEnabled := internalusage.StatisticsEnabled()
	s.applyRetryConfig(newCfg)
	s.applyPprofConfig(newCfg)
	if s.server != nil {
		s.server.UpdateClients(newCfg)
	}
	s.cfgMu.Lock()
	s.cfg = newCfg
	s.cfgMu.Unlock()
	if s.configureUsageStatisticsEnabled() {
		if !wasUsageEnabled {
			s.restoreUsageSnapshot()
		}
		s.startUsagePersistence(context.Background())
	} else {
		s.stopUsagePersistence()
	}
	if s.coreManager != nil {
		s.coreManager.SetConfig(newCfg)
		s.coreManager.SetOAuthModelAlias(newCfg.OAuthModelAlias)
		if !newCfg.Home.Enabled {
			runtimeSnapshot := s.restoreAuthRuntimeSnapshot()
			s.restoreWatcherSnapshotAuths()
			if len(s.applyAuthRuntimeSnapshot(runtimeSnapshot)) == 0 {
				s.reapplyAllModelRegistrations()
			}
			s.startAuthRuntimePersistence(context.Background())
		} else {
			s.registerHomeExecutors()
		}
	}
	s.rebindExecutors()
	s.syncPluginRuntime(coreauth.WithSkipPersist(context.Background()))
}

func forceHomeRuntimeConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	cfg.APIKeys = nil
	cfg.UsageStatisticsEnabled = true
	cfg.DisableCooling = true
	cfg.WebsocketAuth = false
	cfg.EnableGeminiCLIEndpoint = false
	cfg.RemoteManagement.AllowRemote = false
	cfg.RemoteManagement.DisableControlPanel = true
}

func (s *Service) registerHomeExecutors() {
	if s == nil || s.coreManager == nil || s.cfg == nil {
		return
	}

	// Home-dispatched auth records are not loaded from the local auth directory, so
	// baseline executors must be present before the first dispatch response arrives.
	s.coreManager.RegisterExecutor(executor.NewCodexAutoExecutor(s.cfg))
	s.coreManager.RegisterExecutor(executor.NewClaudeExecutor(s.cfg))
	s.coreManager.RegisterExecutor(executor.NewGeminiExecutor(s.cfg))
	s.coreManager.RegisterExecutor(executor.NewGeminiVertexExecutor(s.cfg))
	s.coreManager.RegisterExecutor(executor.NewGeminiCLIExecutor(s.cfg))
	s.coreManager.RegisterExecutor(executor.NewAIStudioExecutor(s.cfg, "", s.wsGateway))
	s.coreManager.RegisterExecutor(executor.NewAntigravityExecutor(s.cfg))
	s.coreManager.RegisterExecutor(executor.NewKimiExecutor(s.cfg))
	s.coreManager.RegisterExecutor(executor.NewXAIExecutor(s.cfg))
	s.coreManager.RegisterExecutor(executor.NewOpenAICompatExecutor("openai-compatibility", s.cfg))
}

func (s *Service) applyHomeOverlay(remoteCfg *config.Config) {
	if s == nil || remoteCfg == nil {
		return
	}

	s.cfgMu.RLock()
	baseCfg := s.cfg
	s.cfgMu.RUnlock()
	if baseCfg == nil {
		return
	}

	merged := *remoteCfg
	merged.Host = baseCfg.Host
	merged.Port = baseCfg.Port
	merged.TLS = baseCfg.TLS
	merged.Home = baseCfg.Home
	forceHomeRuntimeConfig(&merged)
	s.applyConfigUpdate(&merged)
}

type homeUsageForwarder struct {
	ctx    context.Context
	client *home.Client
	queue  chan []byte
}

func (f *homeUsageForwarder) HandleUsage(_ context.Context, record coreusage.Record) {
	if f == nil || f.client == nil || f.ctx == nil {
		return
	}
	select {
	case <-f.ctx.Done():
		return
	default:
	}
	raw, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		log.Warnf("failed to marshal home usage record: %v", errMarshal)
		return
	}
	select {
	case f.queue <- raw:
	default:
		log.Debug("home usage forwarding queue is full; dropping usage record")
	}
}

func (f *homeUsageForwarder) run() {
	if f == nil || f.ctx == nil || f.client == nil {
		return
	}
	for {
		select {
		case <-f.ctx.Done():
			return
		case payload := <-f.queue:
			f.forward(payload)
		}
	}
}

func (f *homeUsageForwarder) forward(payload []byte) {
	for {
		select {
		case <-f.ctx.Done():
			return
		default:
		}
		if f.client.HeartbeatOK() {
			if errPush := f.client.LPushUsage(f.ctx, payload); errPush == nil {
				return
			} else {
				log.Debugf("failed to forward usage record to home: %v", errPush)
			}
		}
		if !sleepWithContext(f.ctx, time.Second) {
			return
		}
	}
}

func (s *Service) startHomeUsageForwarder(ctx context.Context, client *home.Client) {
	if s == nil || client == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	forwarder := &homeUsageForwarder{
		ctx:    ctx,
		client: client,
		queue:  make(chan []byte, 256),
	}
	coreusage.RegisterPlugin(forwarder)
	go forwarder.run()
}

func sleepWithContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Service) startHomeSubscriber(ctx context.Context) {
	if s == nil {
		return
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if cfg == nil || !cfg.Home.Enabled {
		return
	}

	if s.homeCancel != nil {
		s.homeCancel()
		s.homeCancel = nil
	}
	if s.homeClient != nil {
		s.homeClient.Close()
		s.homeClient = nil
	}

	homeCtx := ctx
	if homeCtx == nil {
		homeCtx = context.Background()
	}
	homeCtx, cancel := context.WithCancel(homeCtx)
	s.homeCancel = cancel

	client := home.New(cfg.Home)
	s.homeClient = client
	home.SetCurrent(client)

	go client.StartConfigSubscriber(homeCtx, func(raw []byte) error {
		parsed, errParse := config.ParseConfigBytes(raw)
		if errParse != nil {
			log.Warnf("failed to parse home config payload: %v", errParse)
			return errParse
		}
		s.applyHomeOverlay(parsed)
		return nil
	})
	s.startHomeUsageForwarder(homeCtx, client)
}

// Run starts the service and blocks until the context is cancelled or the server stops.
// It initializes all components including authentication, file watching, HTTP server,
// and starts processing requests. The method blocks until the context is cancelled.
//
// Parameters:
//   - ctx: The context for controlling the service lifecycle
//
// Returns:
//   - error: An error if the service fails to start or run
func (s *Service) Run(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("cliproxy: service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	homeEnabled := s.cfg != nil && s.cfg.Home.Enabled
	if homeEnabled {
		forceHomeRuntimeConfig(s.cfg)
	}
	enabled := s.configureUsageStatisticsEnabled()
	coreusage.StartDefault(ctx)
	internalusage.ResetDefaultRequestStatistics()
	if enabled {
		s.restoreUsageSnapshot()
		s.startUsagePersistence(ctx)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	defer func() {
		if err := s.Shutdown(shutdownCtx); err != nil {
			log.Errorf("service shutdown returned error: %v", err)
		}
	}()

	if !homeEnabled {
		if errEnsureAuthDir := s.ensureAuthDir(); errEnsureAuthDir != nil {
			return errEnsureAuthDir
		}
	}

	s.applyRetryConfig(s.cfg)
	s.registerPluginAuthParser()

	if s.coreManager != nil && !homeEnabled {
		runtimeSnapshot := s.restoreAuthRuntimeSnapshot()
		if errLoad := s.coreManager.Load(ctx); errLoad != nil {
			log.Warnf("failed to load auth store: %v", errLoad)
		} else {
			s.reapplyAllModelRegistrations()
			applied := s.applyAuthRuntimeSnapshot(runtimeSnapshot)
			if len(applied) > 0 {
				log.Infof("applied auth runtime snapshot to %d stored auth(s)", len(applied))
			}
		}
		s.startAuthRuntimePersistence(ctx)
	}

	if !homeEnabled {
		tokenResult, errLoadTokens := s.tokenProvider.Load(ctx, s.cfg)
		if errLoadTokens != nil && !errors.Is(errLoadTokens, context.Canceled) {
			return errLoadTokens
		}
		if tokenResult == nil {
			tokenResult = &TokenClientResult{}
		}

		apiKeyResult, errLoadAPIKeys := s.apiKeyProvider.Load(ctx, s.cfg)
		if errLoadAPIKeys != nil && !errors.Is(errLoadAPIKeys, context.Canceled) {
			return errLoadAPIKeys
		}
		if apiKeyResult == nil {
			apiKeyResult = &APIKeyClientResult{}
		}
	}

	// legacy clients removed; no caches to refresh

	// handlers no longer depend on legacy clients; pass nil slice initially
	s.server = api.NewServer(s.cfg, s.coreManager, s.accessManager, s.configPath, s.serverOptions...)
	s.syncPluginRuntimeConfig(ctx)
	if homeEnabled {
		s.syncPluginModelRuntime(ctx)
	}

	if s.authManager == nil {
		s.authManager = newDefaultAuthManager()
	}

	if homeEnabled {
		s.startHomeSubscriber(ctx)
	}

	s.ensureWebsocketGateway()
	if s.server != nil && s.wsGateway != nil {
		s.server.AttachWebsocketRoute(s.wsGateway.Path(), s.wsGateway.Handler())
		s.server.SetWebsocketAuthChangeHandler(func(oldEnabled, newEnabled bool) {
			if oldEnabled == newEnabled {
				return
			}
			if !oldEnabled && newEnabled {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if errStop := s.wsGateway.Stop(ctx); errStop != nil {
					log.Warnf("failed to reset websocket connections after ws-auth change %t -> %t: %v", oldEnabled, newEnabled, errStop)
					return
				}
				log.Debugf("ws-auth enabled; existing websocket sessions terminated to enforce authentication")
				return
			}
			log.Debugf("ws-auth disabled; existing websocket sessions remain connected")
		})
	}

	if homeEnabled {
		s.registerHomeExecutors()
	}

	if s.hooks.OnBeforeStart != nil {
		s.hooks.OnBeforeStart(s.cfg)
	}

	// Register callback for startup and periodic model catalog refresh.
	// When remote model definitions change, re-register models for affected providers.
	// This intentionally rebuilds per-auth model availability from the latest catalog
	// snapshot instead of preserving prior registry suppression state.
	registry.SetModelRefreshCallback(func(changedProviders []string) {
		if s == nil || s.coreManager == nil || len(changedProviders) == 0 {
			return
		}

		providerSet := make(map[string]bool, len(changedProviders))
		for _, p := range changedProviders {
			providerSet[strings.ToLower(strings.TrimSpace(p))] = true
		}

		auths := s.coreManager.List()
		refreshed := 0
		for _, item := range auths {
			if item == nil || item.ID == "" {
				continue
			}
			auth, ok := s.coreManager.GetByID(item.ID)
			if !ok || auth == nil || auth.Disabled {
				continue
			}
			provider := strings.ToLower(strings.TrimSpace(auth.Provider))
			if !providerSet[provider] {
				continue
			}
			if s.refreshModelRegistrationForAuth(auth) {
				refreshed++
			}
		}

		if refreshed > 0 {
			log.Infof("re-registered models for %d auth(s) due to model catalog changes: %v", refreshed, changedProviders)
		}
	})

	s.serverErr = make(chan error, 1)
	go func() {
		if errStart := s.server.Start(); errStart != nil {
			s.serverErr <- errStart
		} else {
			s.serverErr <- nil
		}
	}()

	time.Sleep(100 * time.Millisecond)
	fmt.Printf("API server started successfully on: %s:%d\n", s.cfg.Host, s.cfg.Port)

	s.applyPprofConfig(s.cfg)

	if s.hooks.OnAfterStart != nil {
		s.hooks.OnAfterStart(s)
	}

	if !homeEnabled {
		reloadCallback := func(newCfg *config.Config) { s.applyConfigUpdate(newCfg) }

		watcherWrapper, errCreateWatcher := s.watcherFactory(s.configPath, s.cfg.AuthDir, reloadCallback)
		if errCreateWatcher != nil {
			return fmt.Errorf("cliproxy: failed to create watcher: %w", errCreateWatcher)
		}
		s.watcher = watcherWrapper
		s.ensureAuthUpdateQueue(ctx)
		if s.authUpdates != nil {
			watcherWrapper.SetAuthUpdateQueue(s.authUpdates)
		}
		watcherWrapper.SetConfig(s.cfg)
		s.registerPluginAuthParser()

		watcherCtx, watcherCancel := context.WithCancel(context.Background())
		s.watcherCancel = watcherCancel
		if errStartWatcher := watcherWrapper.Start(watcherCtx); errStartWatcher != nil {
			return fmt.Errorf("cliproxy: failed to start watcher: %w", errStartWatcher)
		}
		log.Info("file watcher started for config and auth directory changes")
		s.restoreWatcherSnapshotAuths()
		s.syncPluginModelRuntime(ctx)
	}

	// Prefer core auth manager auto refresh if available.
	if s.coreManager != nil && !homeEnabled {
		interval := 15 * time.Minute
		s.coreManager.StartAutoRefresh(context.Background(), interval)
		log.Infof("core auth auto-refresh started (interval=%s)", interval)
	}

	select {
	case <-ctx.Done():
		log.Debug("service context cancelled, shutting down...")
		return ctx.Err()
	case errServer := <-s.serverErr:
		return errServer
	}
}

// Shutdown gracefully stops background workers and the HTTP server.
// It ensures all resources are properly cleaned up and connections are closed.
// The shutdown is idempotent and can be called multiple times safely.
//
// Parameters:
//   - ctx: The context for controlling the shutdown timeout
//
// Returns:
//   - error: An error if shutdown fails
func (s *Service) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	var shutdownErr error
	s.shutdownOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}

		if s.homeCancel != nil {
			s.homeCancel()
			s.homeCancel = nil
		}
		if s.homeClient != nil {
			s.homeClient.Close()
			s.homeClient = nil
		}
		home.ClearCurrent()

		// legacy refresh loop removed; only stopping core auth manager below

		if s.watcherCancel != nil {
			s.watcherCancel()
		}
		if s.coreManager != nil {
			s.coreManager.StopAutoRefresh()
		}
		if s.watcher != nil {
			if err := s.watcher.Stop(); err != nil {
				log.Errorf("failed to stop file watcher: %v", err)
				shutdownErr = err
			}
		}
		if s.wsGateway != nil {
			if err := s.wsGateway.Stop(ctx); err != nil {
				log.Errorf("failed to stop websocket gateway: %v", err)
				if shutdownErr == nil {
					shutdownErr = err
				}
			}
		}
		if s.authQueueStop != nil {
			s.authQueueStop()
			s.authQueueStop = nil
		}

		if errShutdownPprof := s.shutdownPprof(ctx); errShutdownPprof != nil {
			log.Errorf("failed to stop pprof server: %v", errShutdownPprof)
			if shutdownErr == nil {
				shutdownErr = errShutdownPprof
			}
		}

		// no legacy clients to persist

		if s.server != nil {
			shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := s.server.Stop(shutdownCtx); err != nil {
				log.Errorf("error stopping API server: %v", err)
				if shutdownErr == nil {
					shutdownErr = err
				}
			}
		}

		s.stopUsagePersistence()
		s.stopAuthRuntimePersistence()
		s.flushUsageAndPersist()
		s.flushAuthRuntimePersist()
	})
	return shutdownErr
}

func (s *Service) ensureAuthDir() error {
	info, err := os.Stat(s.cfg.AuthDir)
	if err != nil {
		if os.IsNotExist(err) {
			if mkErr := os.MkdirAll(s.cfg.AuthDir, 0o755); mkErr != nil {
				return fmt.Errorf("cliproxy: failed to create auth directory %s: %w", s.cfg.AuthDir, mkErr)
			}
			log.Infof("created missing auth directory: %s", s.cfg.AuthDir)
			return nil
		}
		return fmt.Errorf("cliproxy: error checking auth directory %s: %w", s.cfg.AuthDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("cliproxy: auth path exists but is not a directory: %s", s.cfg.AuthDir)
	}
	return nil
}

// registerModelsForAuth (re)binds provider models in the global registry using the core auth ID as client identifier.
func (s *Service) registerModelsForAuth(ctx context.Context, a *coreauth.Auth) {
	if a == nil || a.ID == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if a.Disabled {
		GlobalModelRegistry().UnregisterClient(a.ID)
		return
	}
	authKind := strings.ToLower(strings.TrimSpace(a.Attributes["auth_kind"]))
	if authKind == "" {
		if kind, _ := a.AccountInfo(); strings.EqualFold(kind, "api_key") {
			authKind = "apikey"
		}
	}
	if a.Attributes != nil {
		if v := strings.TrimSpace(a.Attributes["gemini_virtual_primary"]); strings.EqualFold(v, "true") {
			GlobalModelRegistry().UnregisterClient(a.ID)
			return
		}
	}
	// Unregister legacy client ID (if present) to avoid double counting
	if a.Runtime != nil {
		if idGetter, ok := a.Runtime.(interface{ GetClientID() string }); ok {
			if rid := idGetter.GetClientID(); rid != "" && rid != a.ID {
				GlobalModelRegistry().UnregisterClient(rid)
			}
		}
	}
	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	compatProviderKey, compatDisplayName, compatDetected := openAICompatInfoFromAuth(a)
	if compatDetected {
		provider = "openai-compatibility"
	}
	excluded := s.oauthExcludedModels(provider, authKind)
	// The synthesizer pre-merges per-account and global exclusions into the "excluded_models" attribute.
	// If this attribute is present, it represents the complete list of exclusions and overrides the global config.
	if a.Attributes != nil {
		if val, ok := a.Attributes["excluded_models"]; ok && strings.TrimSpace(val) != "" {
			excluded = strings.Split(val, ",")
		}
	}
	if s.tryRegisterPluginModelsForAuth(ctx, a, provider, authKind, excluded) {
		return
	}
	var models []*ModelInfo
	switch provider {
	case "gemini":
		models = registry.GetGeminiModels()
		if entry := s.resolveConfigGeminiKey(a); entry != nil {
			if len(entry.Models) > 0 {
				models = buildGeminiConfigModels(entry)
			}
			if authKind == "apikey" {
				excluded = entry.ExcludedModels
			}
		}
		models = applyExcludedModels(models, excluded)
	case "vertex":
		// Vertex AI Gemini supports the same model identifiers as Gemini.
		models = registry.GetGeminiVertexModels()
		if entry := s.resolveConfigVertexCompatKey(a); entry != nil {
			if len(entry.Models) > 0 {
				models = buildVertexCompatConfigModels(entry)
			}
			if authKind == "apikey" {
				excluded = entry.ExcludedModels
			}
		}
		models = applyExcludedModels(models, excluded)
	case "gemini-cli":
		models = registry.GetGeminiCLIModels()
		models = applyExcludedModels(models, excluded)
	case "aistudio":
		models = registry.GetAIStudioModels()
		models = applyExcludedModels(models, excluded)
	case "antigravity":
		models = registry.GetAntigravityModels()
		models = applyExcludedModels(models, excluded)
	case "claude":
		models = registry.GetClaudeModels()
		if entry := s.resolveConfigClaudeKey(a); entry != nil {
			if len(entry.Models) > 0 {
				models = buildClaudeConfigModels(entry)
			}
			if authKind == "apikey" {
				excluded = entry.ExcludedModels
			}
		}
		models = applyExcludedModels(models, excluded)
	case "codex":
		codexPlanType := ""
		if a.Attributes != nil {
			codexPlanType = strings.TrimSpace(a.Attributes["plan_type"])
		}
		switch strings.ToLower(codexPlanType) {
		case "pro":
			models = registry.GetCodexProModels()
		case "plus":
			models = registry.GetCodexPlusModels()
		case "team", "business", "go":
			models = registry.GetCodexTeamModels()
		case "free":
			models = registry.GetCodexFreeModels()
		default:
			models = registry.GetCodexProModels()
		}
		if entry := s.resolveConfigCodexKey(a); entry != nil {
			if len(entry.Models) > 0 {
				models = buildCodexConfigModels(entry)
			}
			if authKind == "apikey" {
				excluded = entry.ExcludedModels
			}
		}
		models = applyExcludedModels(models, excluded)
	case "kimi":
		models = registry.GetKimiModels()
		models = applyExcludedModels(models, excluded)
	case "xai":
		models = registry.GetXAIModels()
		models = applyExcludedModels(models, excluded)
	default:
		// Handle OpenAI-compatibility providers by name using config
		if s.cfg != nil {
			providerKey := provider
			compatName := strings.TrimSpace(a.Provider)
			isCompatAuth := false
			if compatDetected {
				if compatProviderKey != "" {
					providerKey = compatProviderKey
				}
				if compatDisplayName != "" {
					compatName = compatDisplayName
				}
				isCompatAuth = true
			}
			if strings.EqualFold(providerKey, "openai-compatibility") {
				isCompatAuth = true
				if a.Attributes != nil {
					if v := strings.TrimSpace(a.Attributes["compat_name"]); v != "" {
						compatName = v
					}
					if v := strings.TrimSpace(a.Attributes["provider_key"]); v != "" {
						providerKey = strings.ToLower(v)
						isCompatAuth = true
					}
				}
				if providerKey == "openai-compatibility" && compatName != "" {
					providerKey = strings.ToLower(compatName)
				}
			} else if a.Attributes != nil {
				if v := strings.TrimSpace(a.Attributes["compat_name"]); v != "" {
					compatName = v
					isCompatAuth = true
				}
				if v := strings.TrimSpace(a.Attributes["provider_key"]); v != "" {
					providerKey = strings.ToLower(v)
					isCompatAuth = true
				}
			}
			for i := range s.cfg.OpenAICompatibility {
				compat := &s.cfg.OpenAICompatibility[i]
				if compat.Disabled {
					continue
				}
				if strings.EqualFold(compat.Name, compatName) {
					isCompatAuth = true
					ms := buildOpenAICompatibilityConfigModels(compat)
					if providerKey == "" {
						providerKey = "openai-compatibility"
					}
					ms = s.appendPluginModels(providerKey, ms)
					ms = applyAllowedModelsForAuth(a, providerKey, ms)
					if len(ms) > 0 {
						s.registerResolvedModelsForAuth(a, providerKey, applyModelPrefixes(ms, a.Prefix, s.cfg.ForceModelPrefix))
					} else {
						// Ensure stale registrations are cleared when model list becomes empty.
						GlobalModelRegistry().UnregisterClient(a.ID)
					}
					return
				}
			}
			if isCompatAuth {
				models = s.appendPluginModels(providerKey, nil)
				models = applyAllowedModelsForAuth(a, providerKey, models)
				if len(models) > 0 {
					s.registerResolvedModelsForAuth(a, providerKey, applyModelPrefixes(models, a.Prefix, s.cfg != nil && s.cfg.ForceModelPrefix))
				} else {
					// No matching provider found or models removed entirely; drop any prior registration.
					GlobalModelRegistry().UnregisterClient(a.ID)
				}
				return
			}
		}
	}
	models = applyOAuthModelAlias(s.cfg, provider, authKind, models)
	key := provider
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(a.Provider))
	}
	models = s.appendPluginModels(key, models)
	models = applyAllowedModelsForAuth(a, key, models)
	if len(models) > 0 {
		s.registerResolvedModelsForAuth(a, key, applyModelPrefixes(models, a.Prefix, s.cfg != nil && s.cfg.ForceModelPrefix))
		return
	}

	GlobalModelRegistry().UnregisterClient(a.ID)
}

// refreshModelRegistrationForAuth re-applies the latest model registration for
// one auth and reconciles any concurrent auth changes that race with the
// refresh. Callers are expected to pre-filter provider membership.
//
// Re-registration is deliberate: registry cooldown/suspension state is treated
// as part of the previous registration snapshot and is cleared when the auth is
// rebound to the refreshed model catalog.
func (s *Service) refreshModelRegistrationForAuth(current *coreauth.Auth) bool {
	if s == nil || s.coreManager == nil || current == nil || current.ID == "" {
		return false
	}

	if !current.Disabled {
		s.ensureExecutorsForAuth(current)
	}
	s.registerModelsForAuth(context.Background(), current)

	latest, ok := s.latestAuthForModelRegistration(current.ID)
	if !ok || latest.Disabled {
		GlobalModelRegistry().UnregisterClient(current.ID)
		s.coreManager.RefreshSchedulerEntry(current.ID)
		return false
	}

	// Re-apply the latest auth snapshot so concurrent auth updates cannot leave
	// stale model registrations behind. This may duplicate registration work when
	// no auth fields changed, but keeps the refresh path simple and correct.
	s.ensureExecutorsForAuth(latest)
	s.registerModelsForAuth(context.Background(), latest)
	s.coreManager.RefreshSchedulerEntry(current.ID)
	return true
}

// latestAuthForModelRegistration returns the latest auth snapshot regardless of
// provider membership. Callers use this after a registration attempt to restore
// whichever state currently owns the client ID in the global registry.
func (s *Service) latestAuthForModelRegistration(authID string) (*coreauth.Auth, bool) {
	if s == nil || s.coreManager == nil || authID == "" {
		return nil, false
	}
	auth, ok := s.coreManager.GetByID(authID)
	if !ok || auth == nil || auth.ID == "" {
		return nil, false
	}
	return auth, true
}

func (s *Service) resolveConfigClaudeKey(auth *coreauth.Auth) *config.ClaudeKey {
	if auth == nil || s.cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range s.cfg.ClaudeKey {
		entry := &s.cfg.ClaudeKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range s.cfg.ClaudeKey {
			entry := &s.cfg.ClaudeKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.APIKey), attrKey) {
				return entry
			}
		}
	}
	return nil
}

func (s *Service) resolveConfigGeminiKey(auth *coreauth.Auth) *config.GeminiKey {
	if auth == nil || s.cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range s.cfg.GeminiKey {
		entry := &s.cfg.GeminiKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	return nil
}

func (s *Service) resolveConfigVertexCompatKey(auth *coreauth.Auth) *config.VertexCompatKey {
	if auth == nil || s.cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range s.cfg.VertexCompatAPIKey {
		entry := &s.cfg.VertexCompatAPIKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range s.cfg.VertexCompatAPIKey {
			entry := &s.cfg.VertexCompatAPIKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.APIKey), attrKey) {
				return entry
			}
		}
	}
	return nil
}

func (s *Service) resolveConfigCodexKey(auth *coreauth.Auth) *config.CodexKey {
	if auth == nil || s.cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range s.cfg.CodexKey {
		entry := &s.cfg.CodexKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	return nil
}

func (s *Service) oauthExcludedModels(provider, authKind string) []string {
	cfg := s.cfg
	if cfg == nil {
		return nil
	}
	authKindKey := strings.ToLower(strings.TrimSpace(authKind))
	providerKey := strings.ToLower(strings.TrimSpace(provider))
	if authKindKey == "apikey" {
		return nil
	}
	return cfg.OAuthExcludedModels[providerKey]
}

func allowedModelsFromAuth(a *coreauth.Auth) []string {
	if a == nil || a.Attributes == nil {
		return nil
	}
	raw := strings.TrimSpace(a.Attributes["models"])
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		modelID := strings.TrimSpace(part)
		if modelID == "" {
			continue
		}
		key := strings.ToLower(modelID)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, modelID)
	}
	return out
}

func applyAllowedModelsForAuth(a *coreauth.Auth, provider string, models []*ModelInfo) []*ModelInfo {
	allowed := allowedModelsFromAuth(a)
	if len(allowed) == 0 {
		return models
	}

	byID := make(map[string]*ModelInfo, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		byID[strings.ToLower(modelID)] = model
	}

	now := time.Now().Unix()
	providerKey := strings.ToLower(strings.TrimSpace(provider))
	out := make([]*ModelInfo, 0, len(allowed))
	seen := make(map[string]struct{}, len(allowed))
	for _, modelID := range allowed {
		key := strings.ToLower(strings.TrimSpace(modelID))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if existing := byID[key]; existing != nil {
			out = append(out, existing)
			continue
		}
		info := &ModelInfo{
			ID:          modelID,
			Object:      "model",
			Created:     now,
			OwnedBy:     providerKey,
			Type:        providerKey,
			DisplayName: modelID,
			UserDefined: true,
		}
		if upstream := registry.LookupStaticModelInfo(modelID); upstream != nil {
			if strings.TrimSpace(upstream.OwnedBy) != "" {
				info.OwnedBy = upstream.OwnedBy
			}
			if strings.TrimSpace(upstream.Type) != "" {
				info.Type = upstream.Type
			}
			if upstream.Thinking != nil {
				info.Thinking = upstream.Thinking
			}
		}
		out = append(out, info)
	}
	return out
}

func applyExcludedModels(models []*ModelInfo, excluded []string) []*ModelInfo {
	if len(models) == 0 || len(excluded) == 0 {
		return models
	}

	patterns := make([]string, 0, len(excluded))
	for _, item := range excluded {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			patterns = append(patterns, strings.ToLower(trimmed))
		}
	}
	if len(patterns) == 0 {
		return models
	}

	filtered := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.ToLower(strings.TrimSpace(model.ID))
		blocked := false
		for _, pattern := range patterns {
			if matchWildcard(pattern, modelID) {
				blocked = true
				break
			}
		}
		if !blocked {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

func applyModelPrefixes(models []*ModelInfo, prefix string, forceModelPrefix bool) []*ModelInfo {
	trimmedPrefix := strings.TrimSpace(prefix)
	if trimmedPrefix == "" || len(models) == 0 {
		return models
	}

	out := make([]*ModelInfo, 0, len(models)*2)
	seen := make(map[string]struct{}, len(models)*2)

	addModel := func(model *ModelInfo) {
		if model == nil {
			return
		}
		id := strings.TrimSpace(model.ID)
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		out = append(out, model)
	}

	for _, model := range models {
		if model == nil {
			continue
		}
		baseID := strings.TrimSpace(model.ID)
		if baseID == "" {
			continue
		}
		if !forceModelPrefix || trimmedPrefix == baseID {
			addModel(model)
		}
		clone := *model
		clone.ID = trimmedPrefix + "/" + baseID
		addModel(&clone)
	}
	return out
}

// matchWildcard performs case-insensitive wildcard matching where '*' matches any substring.
func matchWildcard(pattern, value string) bool {
	if pattern == "" {
		return false
	}

	// Fast path for exact match (no wildcard present).
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}

	parts := strings.Split(pattern, "*")
	// Handle prefix.
	if prefix := parts[0]; prefix != "" {
		if !strings.HasPrefix(value, prefix) {
			return false
		}
		value = value[len(prefix):]
	}

	// Handle suffix.
	if suffix := parts[len(parts)-1]; suffix != "" {
		if !strings.HasSuffix(value, suffix) {
			return false
		}
		value = value[:len(value)-len(suffix)]
	}

	// Handle middle segments in order.
	for i := 1; i < len(parts)-1; i++ {
		segment := parts[i]
		if segment == "" {
			continue
		}
		idx := strings.Index(value, segment)
		if idx < 0 {
			return false
		}
		value = value[idx+len(segment):]
	}

	return true
}

type modelEntry interface {
	GetName() string
	GetAlias() string
}

func buildOpenAICompatibilityConfigModels(compat *config.OpenAICompatibility) []*ModelInfo {
	if compat == nil || len(compat.Models) == 0 {
		return nil
	}
	now := time.Now().Unix()
	models := make([]*ModelInfo, 0, len(compat.Models))
	for i := range compat.Models {
		model := compat.Models[i]
		modelID := strings.TrimSpace(model.Alias)
		if modelID == "" {
			modelID = strings.TrimSpace(model.Name)
		}
		if modelID == "" {
			continue
		}
		modelType := "openai-compatibility"
		if model.Image {
			modelType = registry.OpenAIImageModelType
		}
		thinking := model.Thinking
		if thinking == nil && !model.Image {
			thinking = &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}}
		}
		models = append(models, &ModelInfo{
			ID:          modelID,
			Object:      "model",
			Created:     now,
			OwnedBy:     compat.Name,
			Type:        modelType,
			DisplayName: modelID,
			UserDefined: false,
			Thinking:    thinking,
		})
	}
	return models
}

func buildConfigModels[T modelEntry](models []T, ownedBy, modelType string) []*ModelInfo {
	if len(models) == 0 {
		return nil
	}
	now := time.Now().Unix()
	out := make([]*ModelInfo, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for i := range models {
		model := models[i]
		name := strings.TrimSpace(model.GetName())
		alias := strings.TrimSpace(model.GetAlias())
		if alias == "" {
			alias = name
		}
		if alias == "" {
			continue
		}
		key := strings.ToLower(alias)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		display := name
		if display == "" {
			display = alias
		}
		info := &ModelInfo{
			ID:          alias,
			Object:      "model",
			Created:     now,
			OwnedBy:     ownedBy,
			Type:        modelType,
			DisplayName: display,
			UserDefined: true,
		}
		if name != "" {
			if upstream := registry.LookupStaticModelInfo(name); upstream != nil && upstream.Thinking != nil {
				info.Thinking = upstream.Thinking
			}
		}
		out = append(out, info)
	}
	return out
}

func buildVertexCompatConfigModels(entry *config.VertexCompatKey) []*ModelInfo {
	if entry == nil {
		return nil
	}
	return buildConfigModels(entry.Models, "google", "vertex")
}

func buildGeminiConfigModels(entry *config.GeminiKey) []*ModelInfo {
	if entry == nil {
		return nil
	}
	return buildConfigModels(entry.Models, "google", "gemini")
}

func buildClaudeConfigModels(entry *config.ClaudeKey) []*ModelInfo {
	if entry == nil {
		return nil
	}
	return buildConfigModels(entry.Models, "anthropic", "claude")
}

func buildCodexConfigModels(entry *config.CodexKey) []*ModelInfo {
	if entry == nil {
		return nil
	}
	return buildConfigModels(entry.Models, "openai", "openai")
}

func rewriteModelInfoName(name, oldID, newID string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return name
	}
	oldID = strings.TrimSpace(oldID)
	newID = strings.TrimSpace(newID)
	if oldID == "" || newID == "" {
		return name
	}
	if strings.EqualFold(oldID, newID) {
		return name
	}
	if strings.EqualFold(trimmed, oldID) {
		return newID
	}
	if strings.HasSuffix(trimmed, "/"+oldID) {
		prefix := strings.TrimSuffix(trimmed, oldID)
		return prefix + newID
	}
	if trimmed == "models/"+oldID {
		return "models/" + newID
	}
	return name
}

func applyOAuthModelAlias(cfg *config.Config, provider, authKind string, models []*ModelInfo) []*ModelInfo {
	if cfg == nil || len(models) == 0 {
		return models
	}
	channel := coreauth.OAuthModelAliasChannel(provider, authKind)
	if channel == "" || len(cfg.OAuthModelAlias) == 0 {
		return models
	}
	aliases := cfg.OAuthModelAlias[channel]
	if len(aliases) == 0 {
		return models
	}

	type aliasEntry struct {
		alias string
		fork  bool
	}

	forward := make(map[string][]aliasEntry, len(aliases))
	for i := range aliases {
		name := strings.TrimSpace(aliases[i].Name)
		alias := strings.TrimSpace(aliases[i].Alias)
		if name == "" || alias == "" {
			continue
		}
		if strings.EqualFold(name, alias) {
			continue
		}
		key := strings.ToLower(name)
		forward[key] = append(forward[key], aliasEntry{alias: alias, fork: aliases[i].Fork})
	}
	if len(forward) == 0 {
		return models
	}

	out := make([]*ModelInfo, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		entries := forward[key]
		if len(entries) == 0 {
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, model)
			continue
		}

		keepOriginal := false
		for _, entry := range entries {
			if entry.fork {
				keepOriginal = true
				break
			}
		}
		if keepOriginal {
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				out = append(out, model)
			}
		}

		addedAlias := false
		for _, entry := range entries {
			mappedID := strings.TrimSpace(entry.alias)
			if mappedID == "" {
				continue
			}
			if strings.EqualFold(mappedID, id) {
				continue
			}
			aliasKey := strings.ToLower(mappedID)
			if _, exists := seen[aliasKey]; exists {
				continue
			}
			seen[aliasKey] = struct{}{}
			clone := *model
			clone.ID = mappedID
			if clone.Name != "" {
				clone.Name = rewriteModelInfoName(clone.Name, id, mappedID)
			}
			out = append(out, &clone)
			addedAlias = true
		}

		if !keepOriginal && !addedAlias {
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, model)
		}
	}
	return out
}
