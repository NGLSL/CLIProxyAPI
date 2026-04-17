package access

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
)

// Provider validates credentials for incoming requests.
type Provider interface {
	Identifier() string
	Authenticate(ctx context.Context, r *http.Request) (*Result, *AuthError)
}

// Result conveys authentication outcome.
type Result struct {
	Provider  string
	Principal string
	Index     string
	Metadata  map[string]string
}

func stableAccessIndex(seed string) string {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return ""
	}
	checksum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(checksum[:8])
}

// EnsureIndex returns a stable access identity index.
func (r *Result) EnsureIndex() string {
	if r == nil {
		return ""
	}
	if strings.TrimSpace(r.Index) != "" {
		return strings.TrimSpace(r.Index)
	}
	provider := strings.TrimSpace(r.Provider)
	principal := strings.TrimSpace(r.Principal)
	if provider == "" || principal == "" {
		return ""
	}
	r.Index = stableAccessIndex(provider + "\x00" + principal)
	return r.Index
}

// StableIndex returns the stable access index for provider and principal.
func StableIndex(provider, principal string) string {
	provider = strings.TrimSpace(provider)
	principal = strings.TrimSpace(principal)
	if provider == "" || principal == "" {
		return ""
	}
	return stableAccessIndex(provider + "\x00" + principal)
}

var (
	registryMu sync.RWMutex
	registry   = make(map[string]Provider)
	order      []string
)

// RegisterProvider registers a pre-built provider instance for a given type identifier.
func RegisterProvider(typ string, provider Provider) {
	normalizedType := strings.TrimSpace(typ)
	if normalizedType == "" || provider == nil {
		return
	}

	registryMu.Lock()
	if _, exists := registry[normalizedType]; !exists {
		order = append(order, normalizedType)
	}
	registry[normalizedType] = provider
	registryMu.Unlock()
}

// UnregisterProvider removes a provider by type identifier.
func UnregisterProvider(typ string) {
	normalizedType := strings.TrimSpace(typ)
	if normalizedType == "" {
		return
	}
	registryMu.Lock()
	if _, exists := registry[normalizedType]; !exists {
		registryMu.Unlock()
		return
	}
	delete(registry, normalizedType)
	for index := range order {
		if order[index] != normalizedType {
			continue
		}
		order = append(order[:index], order[index+1:]...)
		break
	}
	registryMu.Unlock()
}

// RegisteredProviders returns the global provider instances in registration order.
func RegisteredProviders() []Provider {
	registryMu.RLock()
	if len(order) == 0 {
		registryMu.RUnlock()
		return nil
	}
	providers := make([]Provider, 0, len(order))
	for _, providerType := range order {
		provider, exists := registry[providerType]
		if !exists || provider == nil {
			continue
		}
		providers = append(providers, provider)
	}
	registryMu.RUnlock()
	return providers
}
