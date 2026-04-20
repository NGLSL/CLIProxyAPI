package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type quotaProbeResult struct {
	Payload    json.RawMessage
	Status     quotaCacheStatus
	HTTPStatus int
	Err        error
}

type quotaCacheService struct {
	cfg         *config.Config
	authManager *coreauth.Manager
	repo        *quotaCacheRepository

	mu       sync.RWMutex
	snapshot quotaCacheSnapshot

	probe func(*quotaCacheService, any, quotaRefreshTarget) quotaProbeResult
}

type quotaCacheError struct {
	statusCode int
	message    string
}

func (e quotaCacheError) Error() string {
	return e.message
}

func quotaCacheRequestError(message string) error {
	return quotaCacheError{statusCode: http.StatusBadRequest, message: message}
}

func quotaCacheHTTPError(err error) (int, string) {
	var cacheErr quotaCacheError
	if errorsAsQuotaCacheError(err, &cacheErr) {
		return cacheErr.statusCode, cacheErr.message
	}
	return http.StatusInternalServerError, "failed to refresh quota cache"
}

func errorsAsQuotaCacheError(err error, target *quotaCacheError) bool {
	if err == nil || target == nil {
		return false
	}
	typed, ok := err.(quotaCacheError)
	if !ok {
		return false
	}
	*target = typed
	return true
}

func newQuotaCacheService(cfg *config.Config, configFilePath string, authManager *coreauth.Manager) *quotaCacheService {
	service := &quotaCacheService{
		cfg:         cfg,
		authManager: authManager,
		repo:        newQuotaCacheRepository(configFilePath),
		probe:       defaultQuotaProbe,
	}
	service.snapshot = service.reconcileSnapshot(quotaCacheSnapshot{Version: quotaCacheVersion})
	return service
}

func (s *quotaCacheService) SetConfig(cfg *config.Config) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
}

func (s *quotaCacheService) SetAuthManager(manager *coreauth.Manager) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.authManager = manager
	s.snapshot = s.reconcileSnapshotLocked(s.snapshot)
	s.mu.Unlock()
}

func (s *quotaCacheService) Load() (quotaCacheSnapshot, bool, error) {
	if s == nil {
		return quotaCacheSnapshot{Version: quotaCacheVersion}, false, nil
	}
	snapshot, existed, err := s.repo.Load()
	if err != nil {
		return quotaCacheSnapshot{Version: quotaCacheVersion}, false, err
	}
	s.mu.Lock()
	s.snapshot = s.reconcileSnapshotLocked(snapshot)
	s.mu.Unlock()
	return snapshot, existed, nil
}

func (s *quotaCacheService) Snapshot() quotaCacheSnapshot {
	if s == nil {
		return quotaCacheSnapshot{Version: quotaCacheVersion}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneQuotaCacheSnapshot(s.snapshot)
}

func (s *quotaCacheService) Refresh(ctx any, req quotaCacheRefreshRequest) (quotaCacheRefreshResponse, error) {
	if s == nil {
		return quotaCacheRefreshResponse{}, fmt.Errorf("quota cache service is nil")
	}

	now := time.Now().UTC()
	auths := s.listAuths()
	requestedIndexes, err := s.validateRequestedAuthIndexes(auths, req.AuthIndexes)
	if err != nil {
		return quotaCacheRefreshResponse{}, err
	}

	currentSnapshot := s.Snapshot()
	entryMap := quotaCacheEntryMap(currentSnapshot.Entries)
	targets := selectQuotaRefreshTargets(auths, entryMap, requestedIndexes, req.Force, now)
	if len(requestedIndexes) > 0 && len(targets) == 0 {
		return quotaCacheRefreshResponse{}, quotaCacheRequestError("no eligible auth entries matched auth_indexes")
	}

	updatedEntries := make([]quotaCacheEntry, 0, len(targets))
	for _, target := range targets {
		key := quotaCacheKey(target.Provider, target.Name)
		previousEntry := entryMap[key]
		result := s.probe(s, ctx, target)
		entry := s.entryFromProbeResult(target, previousEntry, result, now)
		entryMap[key] = entry
		updatedEntries = append(updatedEntries, cloneQuotaCacheEntry(entry))
	}

	snapshot := s.reconcileSnapshotWithEntryMap(entryMap, currentSnapshot.UpdatedAt)
	if len(targets) > 0 {
		snapshot.UpdatedAt = now
	}

	s.mu.Lock()
	s.snapshot = cloneQuotaCacheSnapshot(snapshot)
	s.mu.Unlock()

	if err = s.repo.Save(snapshot); err != nil {
		return quotaCacheRefreshResponse{}, err
	}

	return quotaCacheRefreshResponse{
		UpdatedAt: snapshot.UpdatedAt,
		Entries:   updatedEntries,
	}, nil
}

func (s *quotaCacheService) helperHandler() *Handler {
	if s == nil {
		return &Handler{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return &Handler{cfg: s.cfg, authManager: s.authManager}
}

func (s *quotaCacheService) listAuths() []*coreauth.Auth {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	manager := s.authManager
	s.mu.RUnlock()
	if manager == nil {
		return nil
	}
	return manager.List()
}

func (s *quotaCacheService) validateRequestedAuthIndexes(auths []*coreauth.Auth, requested []string) (map[string]struct{}, error) {
	if len(requested) == 0 {
		return nil, nil
	}
	byIndex := make(map[string]*coreauth.Auth, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		index := auth.EnsureIndex()
		if index == "" {
			continue
		}
		byIndex[index] = auth
	}

	selected := make(map[string]struct{}, len(requested))
	for _, raw := range requested {
		index := strings.TrimSpace(raw)
		if index == "" {
			continue
		}
		auth, ok := byIndex[index]
		if !ok {
			return nil, quotaCacheRequestError(fmt.Sprintf("auth index not found: %s", index))
		}
		if auth.Disabled || auth.Status == coreauth.StatusDisabled {
			return nil, quotaCacheRequestError(fmt.Sprintf("auth is disabled: %s", index))
		}
		if supportedQuotaProvider(auth) == "" {
			return nil, quotaCacheRequestError(fmt.Sprintf("auth provider does not support quota cache: %s", index))
		}
		selected[index] = struct{}{}
	}
	if len(selected) == 0 {
		return nil, quotaCacheRequestError("auth_indexes is empty")
	}
	return selected, nil
}

func (s *quotaCacheService) entryFromProbeResult(target quotaRefreshTarget, previous quotaCacheEntry, result quotaProbeResult, refreshedAt time.Time) quotaCacheEntry {
	entry := quotaCacheEntry{
		Name:      target.Name,
		Provider:  target.Provider,
		AuthIndex: target.AuthIndex,
		Disabled:  target.Auth != nil && (target.Auth.Disabled || target.Auth.Status == coreauth.StatusDisabled),
		Status:    quotaCacheStatusPending,
		Payload:   cloneRawMessage(previous.Payload),
		LastError: previous.LastError,
	}
	entry.LastRefreshAt = timePointer(refreshedAt)

	status := result.Status
	if status == "" {
		status = s.statusFromProbe(target.Auth, result)
	}
	entry.Status = status
	entry.LastErrorStatus = result.HTTPStatus
	if previous.QuotaRecoverAt != nil {
		entry.QuotaRecoverAt = timePointer(*previous.QuotaRecoverAt)
	}

	if status == quotaCacheStatusFresh {
		entry.Payload = cloneRawMessage(result.Payload)
		entry.LastError = ""
		entry.LastErrorStatus = 0
		entry.QuotaRecoverAt = nil
		return entry
	}

	if status == quotaCacheStatusUnauthorized || status == quotaCacheStatusRateLimited {
		entry.Payload = nil
	}
	if result.Err != nil {
		entry.LastError = strings.TrimSpace(result.Err.Error())
	}
	if entry.LastError == "" {
		switch status {
		case quotaCacheStatusUnauthorized:
			entry.LastError = "unauthorized"
		case quotaCacheStatusRateLimited:
			entry.LastError = "rate limited"
		case quotaCacheStatusError:
			entry.LastError = "quota refresh failed"
		}
	}
	if status == quotaCacheStatusRateLimited && target.Auth != nil && !target.Auth.Quota.NextRecoverAt.IsZero() {
		entry.QuotaRecoverAt = timePointer(target.Auth.Quota.NextRecoverAt.UTC())
		if entry.LastError == "" {
			entry.LastError = strings.TrimSpace(target.Auth.Quota.Reason)
		}
	}
	return entry
}

func (s *quotaCacheService) statusFromProbe(auth *coreauth.Auth, result quotaProbeResult) quotaCacheStatus {
	if result.Err == nil {
		return quotaCacheStatusFresh
	}
	if result.HTTPStatus == http.StatusUnauthorized {
		return quotaCacheStatusUnauthorized
	}
	if result.HTTPStatus == http.StatusTooManyRequests {
		return quotaCacheStatusRateLimited
	}
	if auth != nil && auth.Quota.Exceeded {
		if !auth.Quota.NextRecoverAt.IsZero() || strings.TrimSpace(auth.Quota.Reason) != "" {
			return quotaCacheStatusRateLimited
		}
	}
	message := strings.ToLower(strings.TrimSpace(result.Err.Error()))
	if strings.Contains(message, "quota") || strings.Contains(message, "rate limit") || strings.Contains(message, "too many requests") {
		return quotaCacheStatusRateLimited
	}
	return quotaCacheStatusError
}

func (s *quotaCacheService) reconcileSnapshot(snapshot quotaCacheSnapshot) quotaCacheSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reconcileSnapshotLocked(snapshot)
}

func (s *quotaCacheService) reconcileSnapshotLocked(snapshot quotaCacheSnapshot) quotaCacheSnapshot {
	entryMap := quotaCacheEntryMap(snapshot.Entries)
	return s.reconcileSnapshotWithEntryMapLocked(entryMap, snapshot.UpdatedAt)
}

func (s *quotaCacheService) reconcileSnapshotWithEntryMap(entryMap map[string]quotaCacheEntry, updatedAt time.Time) quotaCacheSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reconcileSnapshotWithEntryMapLocked(entryMap, updatedAt)
}

func (s *quotaCacheService) reconcileSnapshotWithEntryMapLocked(entryMap map[string]quotaCacheEntry, updatedAt time.Time) quotaCacheSnapshot {
	auths := []*coreauth.Auth(nil)
	if s.authManager != nil {
		auths = s.authManager.List()
	}
	entries := make([]quotaCacheEntry, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		provider := supportedQuotaProvider(auth)
		if provider == "" {
			continue
		}
		name := quotaAuthName(auth)
		if name == "" {
			continue
		}
		key := quotaCacheKey(provider, name)
		entry, ok := entryMap[key]
		if !ok {
			entry = quotaCacheEntry{Status: quotaCacheStatusPending}
		}
		entry.Name = name
		entry.Provider = provider
		entry.AuthIndex = auth.EnsureIndex()
		entry.Disabled = auth.Disabled || auth.Status == coreauth.StatusDisabled
		if entry.Status == "" {
			entry.Status = quotaCacheStatusPending
		}
		entries = append(entries, cloneQuotaCacheEntry(entry))
	}
	sort.Slice(entries, func(i, j int) bool {
		leftName := strings.ToLower(entries[i].Name)
		rightName := strings.ToLower(entries[j].Name)
		if leftName != rightName {
			return leftName < rightName
		}
		leftProvider := strings.ToLower(entries[i].Provider)
		rightProvider := strings.ToLower(entries[j].Provider)
		if leftProvider != rightProvider {
			return leftProvider < rightProvider
		}
		return entries[i].AuthIndex < entries[j].AuthIndex
	})
	return quotaCacheSnapshotWithEntries(entries, updatedAt)
}

func cloneQuotaCacheSnapshot(snapshot quotaCacheSnapshot) quotaCacheSnapshot {
	clone := quotaCacheSnapshot{
		Version:   snapshot.Version,
		UpdatedAt: snapshot.UpdatedAt,
		Entries:   make([]quotaCacheEntry, 0, len(snapshot.Entries)),
	}
	for _, entry := range snapshot.Entries {
		clone.Entries = append(clone.Entries, cloneQuotaCacheEntry(entry))
	}
	return clone
}

func cloneQuotaCacheEntry(entry quotaCacheEntry) quotaCacheEntry {
	clone := entry
	clone.Payload = cloneRawMessage(entry.Payload)
	if entry.LastRefreshAt != nil {
		ts := entry.LastRefreshAt.UTC()
		clone.LastRefreshAt = &ts
	}
	if entry.QuotaRecoverAt != nil {
		ts := entry.QuotaRecoverAt.UTC()
		clone.QuotaRecoverAt = &ts
	}
	return clone
}

func cloneRawMessage(in json.RawMessage) json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(json.RawMessage, len(in))
	copy(out, in)
	return out
}

func timePointer(value time.Time) *time.Time {
	copied := value.UTC()
	return &copied
}

func contextFromAny(value any) context.Context {
	if ctx, ok := value.(context.Context); ok && ctx != nil {
		return ctx
	}
	return context.Background()
}
