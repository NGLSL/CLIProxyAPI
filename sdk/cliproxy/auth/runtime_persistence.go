package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/util"
)

const runtimeSnapshotFileVersion = 1

var defaultRuntimeSnapshotPathParts = []string{"runtime", "auth-state.json"}

type RuntimeSnapshot struct {
	Auths map[string]*AuthRuntimeState `json:"auths,omitempty"`
}

type AuthRuntimeState struct {
	Status         Status                 `json:"status"`
	StatusMessage  string                 `json:"status_message,omitempty"`
	Unavailable    bool                   `json:"unavailable"`
	Quota          QuotaState             `json:"quota"`
	LastError      *Error                 `json:"last_error,omitempty"`
	UpdatedAt      time.Time              `json:"updated_at"`
	NextRetryAfter time.Time              `json:"next_retry_after"`
	ModelStates    map[string]*ModelState `json:"model_states,omitempty"`
}

type runtimeSnapshotPayload struct {
	Version    int             `json:"version"`
	ExportedAt time.Time       `json:"exported_at"`
	Runtime    RuntimeSnapshot `json:"runtime"`
}

func DefaultRuntimeSnapshotPath(configFilePath string) string {
	if writable := util.WritablePath(); writable != "" {
		parts := append([]string{writable}, defaultRuntimeSnapshotPathParts...)
		return filepath.Join(parts...)
	}

	configFilePath = strings.TrimSpace(configFilePath)
	if configFilePath == "" {
		return ""
	}

	base := filepath.Dir(configFilePath)
	if info, err := os.Stat(configFilePath); err == nil && info.IsDir() {
		base = configFilePath
	}

	parts := append([]string{base}, defaultRuntimeSnapshotPathParts...)
	return filepath.Join(parts...)
}

func LoadRuntimeSnapshotFromFile(path string) (RuntimeSnapshot, error) {
	var snapshot RuntimeSnapshot

	path = strings.TrimSpace(path)
	if path == "" {
		return snapshot, fmt.Errorf("auth runtime snapshot path is empty")
	}

	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return snapshot, err
	}

	var payload runtimeSnapshotPayload
	if err = json.Unmarshal(data, &payload); err != nil {
		return snapshot, err
	}
	if payload.Version != 0 && payload.Version != runtimeSnapshotFileVersion {
		return snapshot, fmt.Errorf("auth runtime snapshot: unsupported version %d", payload.Version)
	}

	return payload.Runtime.Clone(), nil
}

func SaveRuntimeSnapshotToFile(path string, snapshot RuntimeSnapshot) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("auth runtime snapshot path is empty")
	}
	path = filepath.Clean(path)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := json.Marshal(runtimeSnapshotPayload{
		Version:    runtimeSnapshotFileVersion,
		ExportedAt: time.Now().UTC(),
		Runtime:    snapshot.Clone(),
	})
	if err != nil {
		return err
	}

	return atomicWriteRuntimeSnapshot(path, data)
}

func (s RuntimeSnapshot) Clone() RuntimeSnapshot {
	if len(s.Auths) == 0 {
		return RuntimeSnapshot{}
	}
	copied := RuntimeSnapshot{Auths: make(map[string]*AuthRuntimeState, len(s.Auths))}
	for id, state := range s.Auths {
		if strings.TrimSpace(id) == "" || state == nil {
			continue
		}
		copied.Auths[id] = state.Clone()
	}
	if len(copied.Auths) == 0 {
		return RuntimeSnapshot{}
	}
	return copied
}

func (s RuntimeSnapshot) Len() int {
	return len(s.Auths)
}

func (r *AuthRuntimeState) Clone() *AuthRuntimeState {
	if r == nil {
		return nil
	}
	copied := *r
	copied.LastError = cloneError(r.LastError)
	if len(r.ModelStates) > 0 {
		copied.ModelStates = make(map[string]*ModelState, len(r.ModelStates))
		for model, state := range r.ModelStates {
			copied.ModelStates[model] = state.Clone()
		}
	}
	return &copied
}

func (m *Manager) ExportRuntimeSnapshot(now time.Time) RuntimeSnapshot {
	if m == nil {
		return RuntimeSnapshot{}
	}
	if now.IsZero() {
		now = time.Now()
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshot := RuntimeSnapshot{Auths: make(map[string]*AuthRuntimeState)}
	for id, auth := range m.auths {
		if strings.TrimSpace(id) == "" || auth == nil {
			continue
		}
		state := buildRuntimeSnapshotState(auth, now)
		if state == nil {
			continue
		}
		snapshot.Auths[id] = state
	}
	if len(snapshot.Auths) == 0 {
		return RuntimeSnapshot{}
	}
	return snapshot
}

func (m *Manager) ApplyRuntimeSnapshot(snapshot RuntimeSnapshot, now time.Time) []string {
	if m == nil || len(snapshot.Auths) == 0 {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}

	applied := make([]string, 0, len(snapshot.Auths))
	updatedAuths := make([]*Auth, 0, len(snapshot.Auths))

	m.mu.Lock()
	for id, state := range snapshot.Auths {
		if strings.TrimSpace(id) == "" || state == nil {
			continue
		}
		auth, ok := m.auths[id]
		if !ok || !runtimeSnapshotApplicable(auth, now) {
			continue
		}
		if !applyRuntimeSnapshotState(auth, state, now) {
			continue
		}
		updatedAuths = append(updatedAuths, auth.Clone())
		applied = append(applied, id)
	}
	m.mu.Unlock()

	if len(applied) == 0 {
		return nil
	}
	sort.Strings(applied)
	if m.scheduler != nil {
		for i := range updatedAuths {
			m.scheduler.upsertAuth(updatedAuths[i])
		}
	}
	return applied
}

func buildRuntimeSnapshotState(auth *Auth, now time.Time) *AuthRuntimeState {
	if auth == nil {
		return nil
	}
	candidate := auth.Clone()
	sanitizeRuntimeSnapshotAuth(candidate, now)
	if !hasRuntimeSnapshotState(candidate) {
		return nil
	}

	state := &AuthRuntimeState{
		Status:         candidate.Status,
		StatusMessage:  candidate.StatusMessage,
		Unavailable:    candidate.Unavailable,
		Quota:          candidate.Quota,
		LastError:      cloneError(candidate.LastError),
		UpdatedAt:      candidate.UpdatedAt,
		NextRetryAfter: candidate.NextRetryAfter,
	}
	if len(candidate.ModelStates) > 0 {
		state.ModelStates = make(map[string]*ModelState, len(candidate.ModelStates))
		for model, item := range candidate.ModelStates {
			state.ModelStates[model] = item.Clone()
		}
	}
	return state
}

func runtimeSnapshotApplicable(auth *Auth, now time.Time) bool {
	if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
		return false
	}
	candidate := auth.Clone()
	sanitizeRuntimeSnapshotAuth(candidate, now)
	return !hasRuntimeSnapshotState(candidate)
}

func applyRuntimeSnapshotState(auth *Auth, state *AuthRuntimeState, now time.Time) bool {
	if auth == nil || state == nil {
		return false
	}
	auth.Status = state.Status
	auth.StatusMessage = state.StatusMessage
	auth.Unavailable = state.Unavailable
	auth.Quota = state.Quota
	auth.LastError = cloneError(state.LastError)
	auth.UpdatedAt = state.UpdatedAt
	auth.NextRetryAfter = state.NextRetryAfter
	if len(state.ModelStates) > 0 {
		auth.ModelStates = make(map[string]*ModelState, len(state.ModelStates))
		for model, item := range state.ModelStates {
			auth.ModelStates[model] = item.Clone()
		}
	} else {
		auth.ModelStates = nil
	}
	sanitizeRuntimeSnapshotAuth(auth, now)
	return true
}

func preserveRuntimeSnapshotState(auth *Auth, existing *Auth, now time.Time) {
	if auth == nil || existing == nil {
		return
	}
	if auth.Disabled || auth.Status == StatusDisabled || existing.Disabled || existing.Status == StatusDisabled {
		return
	}
	candidate := auth.Clone()
	sanitizeRuntimeSnapshotAuth(candidate, now)
	if hasRuntimeSnapshotState(candidate) {
		return
	}
	state := buildRuntimeSnapshotState(existing, now)
	if state == nil {
		return
	}
	_ = applyRuntimeSnapshotState(auth, state, now)
}

func sanitizeRuntimeSnapshotAuth(auth *Auth, now time.Time) {
	if auth == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	if auth.Disabled || auth.Status == StatusDisabled {
		auth.ModelStates = nil
		clearAggregatedAvailability(auth)
		auth.LastError = nil
		auth.StatusMessage = ""
		return
	}

	if len(auth.ModelStates) > 0 {
		cleaned := make(map[string]*ModelState, len(auth.ModelStates))
		for model, state := range auth.ModelStates {
			clean := sanitizeRuntimeSnapshotModelState(state, now)
			if clean == nil {
				continue
			}
			cleaned[model] = clean
		}
		if len(cleaned) > 0 {
			auth.ModelStates = cleaned
		} else {
			auth.ModelStates = nil
		}
	}

	if len(auth.ModelStates) > 0 {
		updateAggregatedAvailability(auth, now)
		if !hasModelError(auth, now) {
			auth.LastError = nil
			auth.StatusMessage = ""
			auth.Status = StatusActive
		}
		return
	}

	if sanitizeRuntimeSnapshotAuthLevel(auth, now) {
		return
	}
}

func sanitizeRuntimeSnapshotAuthLevel(auth *Auth, now time.Time) bool {
	if auth == nil {
		return false
	}
	if !auth.Unavailable || auth.NextRetryAfter.IsZero() || !auth.NextRetryAfter.After(now) {
		clearAggregatedAvailability(auth)
		clearAuthStateOnSuccess(auth, now)
		return false
	}
	if auth.Quota.Exceeded && (auth.Quota.NextRecoverAt.IsZero() || !auth.Quota.NextRecoverAt.After(now)) {
		clearAggregatedAvailability(auth)
		clearAuthStateOnSuccess(auth, now)
		return false
	}
	if auth.Status == "" {
		auth.Status = StatusError
	}
	return true
}

func sanitizeRuntimeSnapshotModelState(state *ModelState, now time.Time) *ModelState {
	if state == nil {
		return nil
	}
	candidate := state.Clone()
	if candidate.Status == StatusDisabled {
		return candidate
	}
	if !candidate.Unavailable || candidate.NextRetryAfter.IsZero() || !candidate.NextRetryAfter.After(now) {
		resetModelState(candidate, now)
		return nil
	}
	if candidate.Quota.Exceeded && (candidate.Quota.NextRecoverAt.IsZero() || !candidate.Quota.NextRecoverAt.After(now)) {
		resetModelState(candidate, now)
		return nil
	}
	if modelStateIsClean(candidate) {
		return nil
	}
	return candidate
}

func hasRuntimeSnapshotState(auth *Auth) bool {
	if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
		return false
	}
	if len(auth.ModelStates) > 0 {
		return true
	}
	if auth.Status != "" && auth.Status != StatusActive {
		return true
	}
	if auth.StatusMessage != "" || auth.Unavailable || !auth.NextRetryAfter.IsZero() || auth.LastError != nil {
		return true
	}
	return !quotaStateIsClean(auth.Quota)
}

func quotaStateIsClean(state QuotaState) bool {
	return !state.Exceeded && state.Reason == "" && state.NextRecoverAt.IsZero() && state.BackoffLevel == 0
}

func atomicWriteRuntimeSnapshot(path string, data []byte) error {
	tmpFile, err := os.CreateTemp(filepath.Dir(path), "auth-runtime-*.json")
	if err != nil {
		return err
	}

	tmpName := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
	}()

	if _, err = tmpFile.Write(data); err != nil {
		return err
	}
	if err = tmpFile.Chmod(0o644); err != nil {
		return err
	}
	if err = tmpFile.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
	}
	if err = os.Rename(tmpName, path); err != nil {
		return err
	}

	return nil
}
