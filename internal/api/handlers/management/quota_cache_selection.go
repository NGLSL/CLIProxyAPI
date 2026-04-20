package management

import (
	"sort"
	"strings"
	"time"

	coreauth "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const quotaCacheRefreshInterval = time.Hour

type quotaRefreshTarget struct {
	Auth      *coreauth.Auth
	AuthIndex string
	Name      string
	Provider  string
}

func supportedQuotaProvider(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	case "gemini", "gemini-cli":
		return "gemini-cli"
	case "kimi":
		return "kimi"
	case "antigravity":
		return "antigravity"
	default:
		return ""
	}
}

func quotaAuthName(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if name := strings.TrimSpace(auth.FileName); name != "" {
		return name
	}
	return strings.TrimSpace(auth.ID)
}

func shouldRefreshQuotaEntry(entry quotaCacheEntry, now time.Time, force bool) bool {
	if force {
		return true
	}
	if entry.Status == quotaCacheStatusUnauthorized {
		return false
	}
	if entry.Status == quotaCacheStatusRateLimited {
		if entry.QuotaRecoverAt == nil || entry.QuotaRecoverAt.IsZero() {
			return false
		}
		return !entry.QuotaRecoverAt.After(now)
	}
	if entry.LastRefreshAt == nil || entry.LastRefreshAt.IsZero() {
		return true
	}
	return now.Sub(*entry.LastRefreshAt) >= quotaCacheRefreshInterval
}

func selectQuotaRefreshTargets(auths []*coreauth.Auth, entries map[string]quotaCacheEntry, authIndexes map[string]struct{}, force bool, now time.Time) []quotaRefreshTarget {
	targets := make([]quotaRefreshTarget, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if auth.Disabled || auth.Status == coreauth.StatusDisabled {
			continue
		}
		provider := supportedQuotaProvider(auth)
		if provider == "" {
			continue
		}
		authIndex := auth.EnsureIndex()
		if len(authIndexes) > 0 {
			if _, ok := authIndexes[authIndex]; !ok {
				continue
			}
		}
		name := quotaAuthName(auth)
		if name == "" {
			continue
		}
		if !force {
			entry, ok := entries[quotaCacheKey(provider, name)]
			if ok && !shouldRefreshQuotaEntry(entry, now, false) {
				continue
			}
		}
		targets = append(targets, quotaRefreshTarget{
			Auth:      auth,
			AuthIndex: authIndex,
			Name:      name,
			Provider:  provider,
		})
	}

	sort.Slice(targets, func(i, j int) bool {
		leftName := strings.ToLower(targets[i].Name)
		rightName := strings.ToLower(targets[j].Name)
		if leftName != rightName {
			return leftName < rightName
		}
		leftProvider := strings.ToLower(targets[i].Provider)
		rightProvider := strings.ToLower(targets[j].Provider)
		if leftProvider != rightProvider {
			return leftProvider < rightProvider
		}
		return targets[i].AuthIndex < targets[j].AuthIndex
	})

	return targets
}
