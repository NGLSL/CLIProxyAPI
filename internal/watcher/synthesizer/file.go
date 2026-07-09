package synthesizer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/NGLSL/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/config"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/watcher/diff"
	coreauth "github.com/NGLSL/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/NGLSL/CLIProxyAPI/v7/sdk/pluginapi"
)

// FileSynthesizer generates Auth entries from OAuth JSON files.
// It handles file-based authentication.
type FileSynthesizer struct{}

// NewFileSynthesizer creates a new FileSynthesizer instance.
func NewFileSynthesizer() *FileSynthesizer {
	return &FileSynthesizer{}
}

// Synthesize generates Auth entries from auth files in the auth directory.
func (s *FileSynthesizer) Synthesize(ctx *SynthesisContext) ([]*coreauth.Auth, error) {
	out := make([]*coreauth.Auth, 0, 16)
	if ctx == nil || ctx.AuthDir == "" {
		return out, nil
	}

	entries, err := os.ReadDir(ctx.AuthDir)
	if err != nil {
		// Not an error if directory doesn't exist
		return out, nil
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		full := filepath.Join(ctx.AuthDir, name)
		data, errRead := os.ReadFile(full)
		if errRead != nil || len(data) == 0 {
			continue
		}
		auths := synthesizeFileAuths(ctx, full, data)
		if len(auths) == 0 {
			continue
		}
		out = append(out, auths...)
	}
	return out, nil
}

// SynthesizeAuthFile generates Auth entries for one auth JSON file payload.
// It shares exactly the same mapping behavior as FileSynthesizer.Synthesize.
func SynthesizeAuthFile(ctx *SynthesisContext, fullPath string, data []byte) []*coreauth.Auth {
	return synthesizeFileAuths(ctx, fullPath, data)
}

func synthesizeFileAuths(ctx *SynthesisContext, fullPath string, data []byte) []*coreauth.Auth {
	if ctx == nil || len(data) == 0 {
		return nil
	}
	now := ctx.Now
	cfg := ctx.Config
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal != nil {
		return nil
	}
	t, _ := metadata["type"].(string)
	provider := strings.ToLower(strings.TrimSpace(t))
	if ctx.PluginAuthParser != nil {
		auth, handled, errParse := ctx.PluginAuthParser.ParseAuth(context.Background(), pluginapi.AuthParseRequest{
			Provider: provider,
			Path:     fullPath,
			FileName: filepath.Base(fullPath),
			RawJSON:  data,
		})
		if errParse == nil && handled && auth != nil {
			auth.CreatedAt = now
			auth.UpdatedAt = now
			if auth.Attributes == nil {
				auth.Attributes = make(map[string]string)
			}
			auth.Attributes["path"] = fullPath
			auth.Attributes["source"] = fullPath
			auth.Attributes["source_type"] = "file"
			perAccountExcluded := extractExcludedModelsFromMetadata(metadata)
			applyAuthAllowedModelsMeta(auth, extractAllowedModelsFromMetadata(metadata))
			coreauth.ApplyCustomHeadersFromMetadata(auth)
			ApplyAuthExcludedModelsMeta(auth, cfg, perAccountExcluded, "oauth")
			return []*coreauth.Auth{auth}
		}
	}
	if provider == "" {
		return nil
	}
	if provider == "gemini" {
		provider = "gemini-cli"
	}
	if ctx.PluginAuthParser != nil {
		auths, handled, errParse := parsePluginFileAuths(ctx.PluginAuthParser, pluginapi.AuthParseRequest{
			Provider: provider,
			Path:     fullPath,
			FileName: filepath.Base(fullPath),
			RawJSON:  data,
		})
		if errParse == nil && handled {
			auths = compactPluginAuths(auths)
			if len(auths) == 0 {
				return nil
			}
			perAccountExcluded := extractExcludedModelsFromMetadata(metadata)
			perAccountModelAliases := extractOAuthModelAliasesFromMetadata(metadata)
			disabled, _ := metadata["disabled"].(bool)
			for index, auth := range auths {
				if auth == nil {
					continue
				}
				if len(auths) > 1 {
					coreauth.MarkPluginVirtualAuth(auth, fullPath, index)
				}
				auth.CreatedAt = now
				auth.UpdatedAt = now
				if auth.Attributes == nil {
					auth.Attributes = make(map[string]string)
				}
				auth.Attributes[coreauth.AttributePath] = fullPath
				auth.Attributes[coreauth.AttributeSource] = fullPath
				auth.Attributes[coreauth.AttributeSourceBackend] = coreauth.AuthSourceFile
				if disabled {
					auth.Disabled = true
					auth.Status = coreauth.StatusDisabled
					if auth.Metadata == nil {
						auth.Metadata = make(map[string]any)
					}
					auth.Metadata["disabled"] = true
				}
				coreauth.SetOAuthModelAliasesAttribute(auth, perAccountModelAliases)
				ApplyAuthExcludedModelsMeta(auth, cfg, perAccountExcluded, "oauth")
				coreauth.ApplyCustomHeadersFromMetadata(auth)
			}
			return auths
		}
	}
	if provider == "" || provider == "gemini-cli" {
		return nil
	}
	label := provider
	if email, _ := metadata["email"].(string); email != "" {
		label = email
	}
	// Use relative path under authDir as ID to stay consistent with the file-based token store.
	id := fullPath
	if strings.TrimSpace(ctx.AuthDir) != "" {
		if rel, errRel := filepath.Rel(ctx.AuthDir, fullPath); errRel == nil && rel != "" {
			id = rel
		}
	}
	if runtime.GOOS == "windows" {
		id = strings.ToLower(id)
	}

	proxyURL := ""
	if p, ok := metadata["proxy_url"].(string); ok {
		proxyURL = p
	}

	prefix := ""
	if rawPrefix, ok := metadata["prefix"].(string); ok {
		trimmed := strings.TrimSpace(rawPrefix)
		trimmed = strings.Trim(trimmed, "/")
		if trimmed != "" && !strings.Contains(trimmed, "/") {
			prefix = trimmed
		}
	}

	disabled, _ := metadata["disabled"].(bool)
	status := coreauth.StatusActive
	if disabled {
		status = coreauth.StatusDisabled
	}

	// Read per-account excluded models from the OAuth JSON file.
	perAccountExcluded := extractExcludedModelsFromMetadata(metadata)
	perAccountModelAliases := extractOAuthModelAliasesFromMetadata(metadata)

	a := &coreauth.Auth{
		ID:       id,
		Provider: provider,
		Label:    label,
		Prefix:   prefix,
		Status:   status,
		Disabled: disabled,
		Attributes: map[string]string{
			"source":      fullPath,
			"source_type": "file",
			"path":        fullPath,
		},
		ProxyURL:  proxyURL,
		Metadata:  metadata,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// Read priority from auth file.
	if rawPriority, ok := metadata["priority"]; ok {
		switch v := rawPriority.(type) {
		case float64:
			a.Attributes["priority"] = strconv.Itoa(int(v))
		case string:
			priority := strings.TrimSpace(v)
			if _, errAtoi := strconv.Atoi(priority); errAtoi == nil {
				a.Attributes["priority"] = priority
			}
		}
	}
	// Read note from auth file.
	if rawNote, ok := metadata["note"]; ok {
		if note, isStr := rawNote.(string); isStr {
			if trimmed := strings.TrimSpace(note); trimmed != "" {
				a.Attributes["note"] = trimmed
			}
		}
	}
	applyAuthAllowedModelsMeta(a, extractAllowedModelsFromMetadata(metadata))
	coreauth.ApplyCustomHeadersFromMetadata(a)
	coreauth.SetOAuthModelAliasesAttribute(a, perAccountModelAliases)
	ApplyAuthExcludedModelsMeta(a, cfg, perAccountExcluded, "oauth")
	// For codex auth files, extract plan_type from the JWT id_token.
	if provider == "codex" {
		if idTokenRaw, ok := metadata["id_token"].(string); ok && strings.TrimSpace(idTokenRaw) != "" {
			if claims, errParse := codex.ParseJWTToken(idTokenRaw); errParse == nil && claims != nil {
				if pt := strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType); pt != "" {
					a.Attributes["plan_type"] = pt
				}
			}
		}
	}
	return []*coreauth.Auth{a}
}

func parsePluginFileAuths(parser PluginAuthParser, req pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
	if parser == nil {
		return nil, false, nil
	}
	if multiParser, ok := parser.(PluginMultiAuthParser); ok {
		return multiParser.ParseAuths(context.Background(), req)
	}
	auth, handled, errParse := parser.ParseAuth(context.Background(), req)
	if errParse != nil || !handled || auth == nil {
		return nil, handled, errParse
	}
	return []*coreauth.Auth{auth}, true, nil
}

func compactPluginAuths(auths []*coreauth.Auth) []*coreauth.Auth {
	if len(auths) == 0 {
		return nil
	}
	out := auths[:0]
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		out = append(out, auth)
	}
	return out
}

// extractOAuthModelAliasesFromMetadata reads per-account model aliases from OAuth JSON metadata.
// Supports both "model_aliases" and "model-aliases" keys.
func extractOAuthModelAliasesFromMetadata(metadata map[string]any) []config.OAuthModelAlias {
	if metadata == nil {
		return nil
	}
	raw, ok := metadata["model_aliases"]
	if !ok {
		raw, ok = metadata["model-aliases"]
	}
	if !ok || raw == nil {
		return nil
	}
	data, errMarshal := json.Marshal(raw)
	if errMarshal != nil {
		return nil
	}
	var aliases []config.OAuthModelAlias
	if errUnmarshal := json.Unmarshal(data, &aliases); errUnmarshal != nil {
		return nil
	}
	cfg := config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"auth": aliases,
		},
	}
	cfg.SanitizeOAuthModelAlias()
	return cfg.OAuthModelAlias["auth"]
}

// extractExcludedModelsFromMetadata reads per-account excluded models from the OAuth JSON metadata.
// Supports both "excluded_models" and "excluded-models" keys, and accepts string or object arrays.
func extractExcludedModelsFromMetadata(metadata map[string]any) []string {
	if metadata == nil {
		return nil
	}
	for _, key := range []string{"excluded_models", "excluded-models"} {
		if raw, ok := metadata[key]; ok {
			if models := normalizeMetadataStringList(raw); len(models) > 0 {
				return models
			}
		}
	}
	return nil
}

func normalizeMetadataStringList(raw any) []string {
	if raw == nil {
		return nil
	}
	var candidates []string
	switch v := raw.(type) {
	case []string:
		candidates = append(candidates, v...)
	case []any:
		candidates = make([]string, 0, len(v))
		for _, item := range v {
			switch typed := item.(type) {
			case string:
				candidates = append(candidates, typed)
			case map[string]any:
				alias, _ := typed["alias"].(string)
				name, _ := typed["name"].(string)
				if strings.TrimSpace(alias) != "" {
					candidates = append(candidates, alias)
				} else {
					candidates = append(candidates, name)
				}
			}
		}
	default:
		return nil
	}

	result := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func extractAllowedModelsFromMetadata(metadata map[string]any) []string {
	if metadata == nil {
		return nil
	}
	for _, key := range []string{"models", "allowed_models", "allowed-models"} {
		if raw, ok := metadata[key]; ok {
			if models := normalizeMetadataStringList(raw); len(models) > 0 {
				return models
			}
		}
	}
	return nil
}

func applyAuthAllowedModelsMeta(auth *coreauth.Auth, models []string) {
	if auth == nil || len(models) == 0 {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["models"] = strings.Join(models, ",")
	if hash := diff.ComputeAllowedModelsHash(models); hash != "" {
		auth.Attributes["models_hash"] = hash
	}
}
