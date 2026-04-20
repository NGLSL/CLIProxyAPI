package management

import (
	"encoding/json"
	"strings"
	"time"
)

const quotaCacheVersion = 1

type quotaCacheStatus string

const (
	quotaCacheStatusFresh        quotaCacheStatus = "fresh"
	quotaCacheStatusRefreshing   quotaCacheStatus = "refreshing"
	quotaCacheStatusRateLimited  quotaCacheStatus = "rate_limited"
	quotaCacheStatusUnauthorized quotaCacheStatus = "unauthorized"
	quotaCacheStatusError        quotaCacheStatus = "error"
	quotaCacheStatusPending      quotaCacheStatus = "pending"
)

type quotaCacheSnapshot struct {
	Version   int               `json:"version"`
	UpdatedAt time.Time         `json:"updated_at"`
	Entries   []quotaCacheEntry `json:"entries"`
}

type quotaCacheEntry struct {
	Name            string           `json:"name"`
	Provider        string           `json:"provider"`
	AuthIndex       string           `json:"auth_index"`
	Disabled        bool             `json:"disabled"`
	Status          quotaCacheStatus `json:"status"`
	LastRefreshAt   *time.Time       `json:"last_refresh_at,omitempty"`
	LastError       string           `json:"last_error,omitempty"`
	LastErrorStatus int              `json:"last_error_status,omitempty"`
	QuotaRecoverAt  *time.Time       `json:"quota_recover_at,omitempty"`
	Payload         json.RawMessage  `json:"payload,omitempty"`
}

type quotaCacheRefreshRequest struct {
	AuthIndexes []string `json:"auth_indexes"`
	Force       bool     `json:"force"`
}

type quotaCacheRefreshResponse struct {
	UpdatedAt time.Time         `json:"updated_at"`
	Entries   []quotaCacheEntry `json:"entries"`
}

func quotaCacheKey(provider, name string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + "\x00" + strings.TrimSpace(name)
}
