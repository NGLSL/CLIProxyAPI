package management

import (
	"testing"
	"time"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestShouldRefreshQuotaEntry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	older := now.Add(-2 * time.Hour)
	younger := now.Add(-30 * time.Minute)
	futureRecover := now.Add(2 * time.Hour)
	pastRecover := now.Add(-10 * time.Minute)
	interval := time.Hour
	customInterval := 20 * time.Minute

	tests := []struct {
		name     string
		entry    quotaCacheEntry
		force    bool
		interval time.Duration
		want     bool
	}{
		{
			name:     "unauthorized skipped automatically",
			entry:    quotaCacheEntry{Status: quotaCacheStatusUnauthorized, LastRefreshAt: &older},
			interval: interval,
			want:     false,
		},
		{
			name:     "unauthorized included when forced",
			entry:    quotaCacheEntry{Status: quotaCacheStatusUnauthorized, LastRefreshAt: &younger},
			force:    true,
			interval: interval,
			want:     true,
		},
		{
			name:     "fresh younger than interval skipped",
			entry:    quotaCacheEntry{Status: quotaCacheStatusFresh, LastRefreshAt: &younger},
			interval: interval,
			want:     false,
		},
		{
			name:     "fresh older than interval included",
			entry:    quotaCacheEntry{Status: quotaCacheStatusFresh, LastRefreshAt: &older},
			interval: interval,
			want:     true,
		},
		{
			name:     "custom interval refreshes sooner",
			entry:    quotaCacheEntry{Status: quotaCacheStatusFresh, LastRefreshAt: &younger},
			interval: customInterval,
			want:     true,
		},
		{
			name:     "rate limited with future recovery skipped",
			entry:    quotaCacheEntry{Status: quotaCacheStatusRateLimited, LastRefreshAt: &older, QuotaRecoverAt: &futureRecover},
			interval: interval,
			want:     false,
		},
		{
			name:     "rate limited with past recovery included",
			entry:    quotaCacheEntry{Status: quotaCacheStatusRateLimited, LastRefreshAt: &younger, QuotaRecoverAt: &pastRecover},
			interval: interval,
			want:     true,
		},
		{
			name:     "rate limited without recovery skipped automatically",
			entry:    quotaCacheEntry{Status: quotaCacheStatusRateLimited, LastRefreshAt: &older},
			interval: interval,
			want:     false,
		},
		{
			name:     "rate limited without recovery included when forced",
			entry:    quotaCacheEntry{Status: quotaCacheStatusRateLimited, LastRefreshAt: &younger},
			force:    true,
			interval: interval,
			want:     true,
		},
		{
			name:     "pending without last refresh included",
			entry:    quotaCacheEntry{Status: quotaCacheStatusPending},
			interval: interval,
			want:     true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shouldRefreshQuotaEntry(tt.entry, now, tt.force, tt.interval)
			if got != tt.want {
				t.Fatalf("shouldRefreshQuotaEntry() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQuotaCacheServiceRefreshInterval(t *testing.T) {
	t.Parallel()

	service := newQuotaCacheService(nil, "", nil)
	if got := service.quotaCacheRefreshInterval(); got != time.Duration(config.DefaultQuotaCacheRefreshInterval)*time.Second {
		t.Fatalf("quotaCacheRefreshInterval() = %v, want %v", got, time.Duration(config.DefaultQuotaCacheRefreshInterval)*time.Second)
	}

	service.SetConfig(&config.Config{QuotaCacheRefreshInterval: 90})
	if got := service.quotaCacheRefreshInterval(); got != 90*time.Second {
		t.Fatalf("quotaCacheRefreshInterval() = %v, want %v", got, 90*time.Second)
	}
}

func TestQuotaCacheSelection(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	freshRecent := now.Add(-10 * time.Minute)
	freshOld := now.Add(-2 * time.Hour)
	futureRecover := now.Add(2 * time.Hour)
	pastRecover := now.Add(-15 * time.Minute)

	disabledAuth := newQuotaSelectionAuth("disabled-id", "codex", "disabled.json", true)
	unauthorizedAuth := newQuotaSelectionAuth("unauthorized-id", "claude", "unauthorized.json", false)
	freshRecentAuth := newQuotaSelectionAuth("fresh-recent-id", "codex", "fresh-recent.json", false)
	freshOldAuth := newQuotaSelectionAuth("fresh-old-id", "codex", "fresh-old.json", false)
	rateFutureAuth := newQuotaSelectionAuth("rate-future-id", "claude", "rate-future.json", false)
	ratePastAuth := newQuotaSelectionAuth("rate-past-id", "claude", "rate-past.json", false)
	rateMissingAuth := newQuotaSelectionAuth("rate-missing-id", "claude", "rate-missing.json", false)
	missingEntryAuth := newQuotaSelectionAuth("missing-entry-id", "gemini", "missing-entry.json", false)
	unsupportedAuth := newQuotaSelectionAuth("unsupported-id", "openai", "unsupported.json", false)

	auths := []*coreauth.Auth{
		disabledAuth,
		unauthorizedAuth,
		freshRecentAuth,
		freshOldAuth,
		rateFutureAuth,
		ratePastAuth,
		rateMissingAuth,
		missingEntryAuth,
		unsupportedAuth,
	}

	entries := quotaCacheEntryMap([]quotaCacheEntry{
		{Name: quotaAuthName(unauthorizedAuth), Provider: supportedQuotaProvider(unauthorizedAuth), Status: quotaCacheStatusUnauthorized, LastRefreshAt: &freshOld},
		{Name: quotaAuthName(freshRecentAuth), Provider: supportedQuotaProvider(freshRecentAuth), Status: quotaCacheStatusFresh, LastRefreshAt: &freshRecent},
		{Name: quotaAuthName(freshOldAuth), Provider: supportedQuotaProvider(freshOldAuth), Status: quotaCacheStatusFresh, LastRefreshAt: &freshOld},
		{Name: quotaAuthName(rateFutureAuth), Provider: supportedQuotaProvider(rateFutureAuth), Status: quotaCacheStatusRateLimited, LastRefreshAt: &freshOld, QuotaRecoverAt: &futureRecover},
		{Name: quotaAuthName(ratePastAuth), Provider: supportedQuotaProvider(ratePastAuth), Status: quotaCacheStatusRateLimited, LastRefreshAt: &freshRecent, QuotaRecoverAt: &pastRecover},
		{Name: quotaAuthName(rateMissingAuth), Provider: supportedQuotaProvider(rateMissingAuth), Status: quotaCacheStatusRateLimited, LastRefreshAt: &freshOld},
	})

	automaticTargets := selectQuotaRefreshTargets(auths, entries, nil, false, now, time.Hour)
	assertQuotaTargetAuthIndexes(t, automaticTargets, []string{freshOldAuth.Index, missingEntryAuth.Index, ratePastAuth.Index})

	customIntervalTargets := selectQuotaRefreshTargets(auths, entries, nil, false, now, 5*time.Minute)
	assertQuotaTargetAuthIndexes(t, customIntervalTargets, []string{freshOldAuth.Index, freshRecentAuth.Index, missingEntryAuth.Index, ratePastAuth.Index})

	forcedIndexes := map[string]struct{}{
		disabledAuth.Index:     {},
		unauthorizedAuth.Index: {},
		rateFutureAuth.Index:   {},
		rateMissingAuth.Index:  {},
		missingEntryAuth.Index: {},
		unsupportedAuth.Index:  {},
	}
	forcedTargets := selectQuotaRefreshTargets(auths, entries, forcedIndexes, true, now, time.Hour)
	assertQuotaTargetAuthIndexes(t, forcedTargets, []string{missingEntryAuth.Index, rateFutureAuth.Index, rateMissingAuth.Index, unauthorizedAuth.Index})
}

func TestSupportedQuotaProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		provider string
		want     string
	}{
		{provider: "claude", want: "claude"},
		{provider: "codex", want: "codex"},
		{provider: "gemini", want: "gemini-cli"},
		{provider: "gemini-cli", want: "gemini-cli"},
		{provider: "kimi", want: "kimi"},
		{provider: "antigravity", want: "antigravity"},
		{provider: "openai", want: ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.provider, func(t *testing.T) {
			t.Parallel()
			auth := &coreauth.Auth{Provider: tt.provider}
			if got := supportedQuotaProvider(auth); got != tt.want {
				t.Fatalf("supportedQuotaProvider(%q) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}

func newQuotaSelectionAuth(id, provider, fileName string, disabled bool) *coreauth.Auth {
	auth := &coreauth.Auth{
		ID:       id,
		Provider: provider,
		FileName: fileName,
		Disabled: disabled,
		Status:   coreauth.StatusActive,
	}
	if disabled {
		auth.Status = coreauth.StatusDisabled
	}
	auth.EnsureIndex()
	return auth
}

func assertQuotaTargetAuthIndexes(t *testing.T, targets []quotaRefreshTarget, want []string) {
	t.Helper()
	if len(targets) != len(want) {
		t.Fatalf("target length = %d, want %d", len(targets), len(want))
	}
	for i, target := range targets {
		if target.Auth == nil {
			t.Fatalf("target %d has nil auth", i)
		}
		if target.AuthIndex != want[i] {
			t.Fatalf("target %d auth_index = %q, want %q", i, target.AuthIndex, want[i])
		}
	}
}
