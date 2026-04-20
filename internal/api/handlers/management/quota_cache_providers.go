package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	codexauth "github.com/NGLSL/CLIProxyAPI/v6/internal/auth/codex"
	coreauth "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const defaultAntigravityProjectID = "bamboo-precept-lgxtn"

var claudeRequestHeaders = map[string]string{
	"Authorization":  "Bearer $TOKEN$",
	"Content-Type":   "application/json",
	"anthropic-beta": "oauth-2025-04-20",
}

var codexRequestHeaders = map[string]string{
	"Authorization": "Bearer $TOKEN$",
	"Content-Type":  "application/json",
	"User-Agent":    "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal",
}

var geminiCLIRequestHeaders = map[string]string{
	"Authorization": "Bearer $TOKEN$",
	"Content-Type":  "application/json",
}

var kimiRequestHeaders = map[string]string{
	"Authorization": "Bearer $TOKEN$",
}

var antigravityRequestHeaders = map[string]string{
	"Authorization": "Bearer $TOKEN$",
	"Content-Type":  "application/json",
	"User-Agent":    "antigravity/1.11.5 windows/amd64",
}

var claudeUsageWindowDefs = []struct {
	Key      string
	ID       string
	LabelKey string
}{
	{Key: "five_hour", ID: "five-hour", LabelKey: "claude_quota.five_hour"},
	{Key: "seven_day", ID: "seven-day", LabelKey: "claude_quota.seven_day"},
	{Key: "seven_day_oauth_apps", ID: "seven-day-oauth-apps", LabelKey: "claude_quota.seven_day_oauth_apps"},
	{Key: "seven_day_opus", ID: "seven-day-opus", LabelKey: "claude_quota.seven_day_opus"},
	{Key: "seven_day_sonnet", ID: "seven-day-sonnet", LabelKey: "claude_quota.seven_day_sonnet"},
	{Key: "seven_day_cowork", ID: "seven-day-cowork", LabelKey: "claude_quota.seven_day_cowork"},
	{Key: "iguana_necktie", ID: "iguana-necktie", LabelKey: "claude_quota.iguana_necktie"},
}

var geminiQuotaGroupDefs = []struct {
	ID               string
	Label            string
	PreferredModelID string
	ModelIDs         []string
}{
	{ID: "gemini-flash-lite-series", Label: "Gemini Flash Lite Series", PreferredModelID: "gemini-2.5-flash-lite", ModelIDs: []string{"gemini-2.5-flash-lite"}},
	{ID: "gemini-flash-series", Label: "Gemini Flash Series", PreferredModelID: "gemini-3-flash-preview", ModelIDs: []string{"gemini-3-flash-preview", "gemini-2.5-flash"}},
	{ID: "gemini-pro-series", Label: "Gemini Pro Series", PreferredModelID: "gemini-3.1-pro-preview", ModelIDs: []string{"gemini-3.1-pro-preview", "gemini-3-pro-preview", "gemini-2.5-pro"}},
}

var antigravityGroupDefs = []struct {
	ID             string
	Label          string
	Identifiers    []string
	LabelFromModel bool
}{
	{ID: "claude-gpt", Label: "Claude/GPT", Identifiers: []string{"claude-sonnet-4-6", "claude-opus-4-6-thinking", "gpt-oss-120b-medium"}},
	{ID: "gemini-3-pro", Label: "Gemini 3 Pro", Identifiers: []string{"gemini-3-pro-high", "gemini-3-pro-low"}},
	{ID: "gemini-3-1-pro-series", Label: "Gemini 3.1 Pro Series", Identifiers: []string{"gemini-3.1-pro-high", "gemini-3.1-pro-low"}},
	{ID: "gemini-2-5-flash", Label: "Gemini 2.5 Flash", Identifiers: []string{"gemini-2.5-flash", "gemini-2.5-flash-thinking"}},
	{ID: "gemini-2-5-flash-lite", Label: "Gemini 2.5 Flash Lite", Identifiers: []string{"gemini-2.5-flash-lite"}},
	{ID: "gemini-2-5-cu", Label: "Gemini 2.5 CU", Identifiers: []string{"rev19-uic3-1p"}},
	{ID: "gemini-3-flash", Label: "Gemini 3 Flash", Identifiers: []string{"gemini-3-flash"}},
	{ID: "gemini-image", Label: "gemini-3.1-flash-image", Identifiers: []string{"gemini-3.1-flash-image"}, LabelFromModel: true},
}

func defaultQuotaProbe(service *quotaCacheService, ctx any, target quotaRefreshTarget) quotaProbeResult {
	switch target.Provider {
	case "claude":
		return probeClaudeQuota(service, ctx, target)
	case "codex":
		return probeCodexQuota(service, ctx, target)
	case "gemini-cli":
		return probeGeminiCLIQuota(service, ctx, target)
	case "antigravity":
		return probeAntigravityQuota(service, ctx, target)
	case "kimi":
		return probeKimiQuota(service, ctx, target)
	default:
		return quotaProbeResult{Err: fmt.Errorf("quota probe unsupported for provider %s", target.Provider)}
	}
}

func probeClaudeQuota(service *quotaCacheService, ctx any, target quotaRefreshTarget) quotaProbeResult {
	usageResp, err := service.executeQuotaAPICall(ctx, target, http.MethodGet, "https://api.anthropic.com/api/oauth/usage", claudeRequestHeaders, "")
	if err != nil {
		return quotaProbeResult{Err: err}
	}
	if usageResp.StatusCode < http.StatusOK || usageResp.StatusCode >= http.StatusMultipleChoices {
		return quotaProbeResult{HTTPStatus: usageResp.StatusCode, Err: fmt.Errorf("%s", strings.TrimSpace(usageResp.Body))}
	}

	usageMap, err := decodeQuotaJSONBody(usageResp.Body)
	if err != nil {
		return quotaProbeResult{Err: err}
	}

	profileMap := map[string]any{}
	profileResp, profileErr := service.executeQuotaAPICall(ctx, target, http.MethodGet, "https://api.anthropic.com/api/oauth/profile", claudeRequestHeaders, "")
	if profileErr == nil && profileResp.StatusCode >= http.StatusOK && profileResp.StatusCode < http.StatusMultipleChoices {
		profileMap, _ = decodeQuotaJSONBody(profileResp.Body)
	}

	payload := map[string]any{
		"windows":    buildClaudeQuotaWindows(usageMap),
		"extraUsage": quotaMap(usageMap["extra_usage"]),
		"planType":   resolveClaudePlanType(profileMap),
	}
	return quotaProbeResult{Payload: marshalQuotaPayload(payload)}
}

func probeCodexQuota(service *quotaCacheService, ctx any, target quotaRefreshTarget) quotaProbeResult {
	accountID, planType := resolveCodexAccountInfo(target.Auth)
	if accountID == "" {
		return quotaProbeResult{Err: fmt.Errorf("codex id_token missing chatgpt account id")}
	}

	headers := copyStringMap(codexRequestHeaders)
	headers["Chatgpt-Account-Id"] = accountID
	resp, err := service.executeQuotaAPICall(ctx, target, http.MethodGet, "https://chatgpt.com/backend-api/wham/usage", headers, "")
	if err != nil {
		return quotaProbeResult{Err: err}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return quotaProbeResult{HTTPStatus: resp.StatusCode, Err: fmt.Errorf("%s", strings.TrimSpace(resp.Body))}
	}

	body, err := decodeQuotaJSONBody(resp.Body)
	if err != nil {
		return quotaProbeResult{Err: err}
	}
	payload := map[string]any{
		"windows":  buildCodexQuotaWindows(body),
		"planType": planType,
	}
	return quotaProbeResult{Payload: marshalQuotaPayload(payload)}
}

func probeGeminiCLIQuota(service *quotaCacheService, ctx any, target quotaRefreshTarget) quotaProbeResult {
	projectID := resolveGeminiCLIProjectID(target.Auth)
	if projectID == "" {
		return quotaProbeResult{Err: fmt.Errorf("gemini-cli project id missing")}
	}

	quotaResp, err := service.executeQuotaAPICall(ctx, target, http.MethodPost, "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota", geminiCLIRequestHeaders, fmt.Sprintf(`{"project":%q}`, projectID))
	if err != nil {
		return quotaProbeResult{Err: err}
	}
	if quotaResp.StatusCode < http.StatusOK || quotaResp.StatusCode >= http.StatusMultipleChoices {
		return quotaProbeResult{HTTPStatus: quotaResp.StatusCode, Err: fmt.Errorf("%s", strings.TrimSpace(quotaResp.Body))}
	}

	quotaBody, err := decodeQuotaJSONBody(quotaResp.Body)
	if err != nil {
		return quotaProbeResult{Err: err}
	}

	supplementary := map[string]any{}
	codeAssistData := fmt.Sprintf(`{"cloudaicompanionProject":%q,"metadata":{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI","duetProject":%q}}`, projectID, projectID)
	codeAssistResp, codeAssistErr := service.executeQuotaAPICall(ctx, target, http.MethodPost, "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", geminiCLIRequestHeaders, codeAssistData)
	if codeAssistErr == nil && codeAssistResp.StatusCode >= http.StatusOK && codeAssistResp.StatusCode < http.StatusMultipleChoices {
		supplementary, _ = decodeQuotaJSONBody(codeAssistResp.Body)
	}

	payload := map[string]any{
		"buckets":       buildGeminiCLIQuotaBuckets(quotaArray(quotaBody["buckets"])),
		"tierLabel":     resolveGeminiCLITierLabel(supplementary),
		"tierId":        resolveGeminiCLITierID(supplementary),
		"creditBalance": resolveGeminiCLICreditBalance(supplementary),
	}
	return quotaProbeResult{Payload: marshalQuotaPayload(payload)}
}

func probeAntigravityQuota(service *quotaCacheService, ctx any, target quotaRefreshTarget) quotaProbeResult {
	projectID := resolveAntigravityProjectID(target.Auth)
	requestBody := fmt.Sprintf(`{"project":%q}`, projectID)
	urls := []string{
		"https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
		"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:fetchAvailableModels",
		"https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
	}

	var lastResult quotaProbeResult
	for _, url := range urls {
		resp, err := service.executeQuotaAPICall(ctx, target, http.MethodPost, url, antigravityRequestHeaders, requestBody)
		if err != nil {
			lastResult = quotaProbeResult{Err: err}
			continue
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			lastResult = quotaProbeResult{HTTPStatus: resp.StatusCode, Err: fmt.Errorf("%s", strings.TrimSpace(resp.Body))}
			continue
		}
		body, errDecode := decodeQuotaJSONBody(resp.Body)
		if errDecode != nil {
			lastResult = quotaProbeResult{Err: errDecode}
			continue
		}
		models := quotaMap(body["models"])
		payload := map[string]any{"groups": buildAntigravityQuotaGroups(models)}
		return quotaProbeResult{Payload: marshalQuotaPayload(payload)}
	}
	if lastResult.Err == nil {
		lastResult.Err = fmt.Errorf("antigravity quota request failed")
	}
	return lastResult
}

func probeKimiQuota(service *quotaCacheService, ctx any, target quotaRefreshTarget) quotaProbeResult {
	resp, err := service.executeQuotaAPICall(ctx, target, http.MethodGet, "https://api.kimi.com/coding/v1/usages", kimiRequestHeaders, "")
	if err != nil {
		return quotaProbeResult{Err: err}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return quotaProbeResult{HTTPStatus: resp.StatusCode, Err: fmt.Errorf("%s", strings.TrimSpace(resp.Body))}
	}
	body, err := decodeQuotaJSONBody(resp.Body)
	if err != nil {
		return quotaProbeResult{Err: err}
	}
	payload := map[string]any{"rows": buildKimiQuotaRows(body)}
	return quotaProbeResult{Payload: marshalQuotaPayload(payload)}
}

func (s *quotaCacheService) executeQuotaAPICall(ctx any, target quotaRefreshTarget, method, url string, headers map[string]string, data string) (apiCallResponse, error) {
	helper := s.helperHandler()
	index := target.AuthIndex
	request := apiCallRequest{
		AuthIndexSnake: &index,
		Method:         method,
		URL:            url,
		Header:         copyStringMap(headers),
		Data:           data,
	}
	return helper.executeAPICall(contextFromAny(ctx), request)
}

func buildClaudeQuotaWindows(payload map[string]any) []map[string]any {
	windows := make([]map[string]any, 0, len(claudeUsageWindowDefs))
	for _, def := range claudeUsageWindowDefs {
		window := quotaMap(payload[def.Key])
		if len(window) == 0 {
			continue
		}
		usedPercent := quotaPercent(window["utilization"])
		resetLabel := quotaString(window["resets_at"])
		if resetLabel == "" {
			resetLabel = "-"
		}
		windows = append(windows, map[string]any{
			"id":          def.ID,
			"label":       def.ID,
			"labelKey":    def.LabelKey,
			"usedPercent": usedPercent,
			"resetLabel":  resetLabel,
		})
	}
	return windows
}

func resolveClaudePlanType(profile map[string]any) string {
	account := quotaMap(profile["account"])
	organization := quotaMap(profile["organization"])
	if quotaBoolValue(account["has_claude_max"]) {
		return "max"
	}
	if quotaBoolValue(account["has_claude_pro"]) {
		return "pro"
	}
	for _, key := range []string{"subscription_status", "billing_type", "rate_limit_tier", "organization_type"} {
		if value := strings.ToLower(strings.TrimSpace(quotaString(organization[key]))); value != "" {
			return value
		}
	}
	return ""
}

func resolveCodexAccountInfo(auth *coreauth.Auth) (string, string) {
	if auth == nil {
		return "", ""
	}
	planType := ""
	if auth.Attributes != nil {
		planType = strings.ToLower(strings.TrimSpace(auth.Attributes["plan_type"]))
	}
	if auth.Metadata == nil {
		return "", planType
	}
	idToken := strings.TrimSpace(stringValue(auth.Metadata, "id_token"))
	if idToken == "" {
		return "", planType
	}
	claims, err := codexauth.ParseJWTToken(idToken)
	if err != nil || claims == nil {
		return "", planType
	}
	if planType == "" {
		planType = strings.ToLower(strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType))
	}
	return strings.TrimSpace(claims.CodexAuthInfo.ChatgptAccountID), planType
}

func buildCodexQuotaWindows(payload map[string]any) []map[string]any {
	windows := make([]map[string]any, 0)
	appendWindow := func(id, labelKey string, window map[string]any, labelParams map[string]any) {
		if len(window) == 0 {
			return
		}
		usedPercent := quotaOptionalNumber(window["used_percent"])
		if usedPercent == nil {
			usedPercent = quotaOptionalNumber(window["usedPercent"])
		}
		resetLabel := formatCodexResetLabel(window)
		entry := map[string]any{
			"id":         id,
			"label":      id,
			"labelKey":   labelKey,
			"resetLabel": resetLabel,
		}
		if usedPercent != nil {
			entry["usedPercent"] = *usedPercent
		} else {
			entry["usedPercent"] = nil
		}
		if len(labelParams) > 0 {
			entry["labelParams"] = labelParams
		}
		windows = append(windows, entry)
	}

	rateLimit := quotaMap(firstNonNil(payload["rate_limit"], payload["rateLimit"]))
	appendWindow("five-hour", "codex_quota.primary_window", quotaMap(firstNonNil(rateLimit["primary_window"], rateLimit["primaryWindow"])), nil)
	appendWindow("weekly", "codex_quota.secondary_window", quotaMap(firstNonNil(rateLimit["secondary_window"], rateLimit["secondaryWindow"])), nil)

	codeReview := quotaMap(firstNonNil(payload["code_review_rate_limit"], payload["codeReviewRateLimit"]))
	appendWindow("code-review-five-hour", "codex_quota.code_review_primary_window", quotaMap(firstNonNil(codeReview["primary_window"], codeReview["primaryWindow"])), nil)
	appendWindow("code-review-weekly", "codex_quota.code_review_secondary_window", quotaMap(firstNonNil(codeReview["secondary_window"], codeReview["secondaryWindow"])), nil)

	for index, raw := range quotaArray(firstNonNil(payload["additional_rate_limits"], payload["additionalRateLimits"])) {
		item := quotaMap(raw)
		rateInfo := quotaMap(firstNonNil(item["rate_limit"], item["rateLimit"]))
		name := quotaString(firstNonNil(item["limit_name"], item["limitName"], item["metered_feature"], item["meteredFeature"]))
		if name == "" {
			name = fmt.Sprintf("additional-%d", index+1)
		}
		labelParams := map[string]any{"name": name}
		appendWindow(fmt.Sprintf("%s-five-hour-%d", normalizeQuotaIdentifier(name), index), "codex_quota.additional_primary_window", quotaMap(firstNonNil(rateInfo["primary_window"], rateInfo["primaryWindow"])), labelParams)
		appendWindow(fmt.Sprintf("%s-weekly-%d", normalizeQuotaIdentifier(name), index), "codex_quota.additional_secondary_window", quotaMap(firstNonNil(rateInfo["secondary_window"], rateInfo["secondaryWindow"])), labelParams)
	}
	return windows
}

func formatCodexResetLabel(window map[string]any) string {
	if len(window) == 0 {
		return "-"
	}
	if seconds, ok := quotaInt64(firstNonNil(window["reset_after_seconds"], window["resetAfterSeconds"])); ok && seconds > 0 {
		return time.Now().UTC().Add(time.Duration(seconds) * time.Second).Format(time.RFC3339)
	}
	if resetAt, ok := quotaInt64(firstNonNil(window["reset_at"], window["resetAt"])); ok && resetAt > 0 {
		if resetAt > 1_000_000_000_000 {
			return time.UnixMilli(resetAt).UTC().Format(time.RFC3339)
		}
		return time.Unix(resetAt, 0).UTC().Format(time.RFC3339)
	}
	return "-"
}

func resolveGeminiCLIProjectID(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if value := strings.TrimSpace(quotaString(auth.Metadata["project_id"])); value != "" {
			return value
		}
	}
	candidates := []string{}
	_, accountInfo := auth.AccountInfo()
	if accountInfo != "" {
		candidates = append(candidates, accountInfo)
	}
	if auth.Metadata != nil {
		candidates = append(candidates, quotaString(auth.Metadata["account"]))
	}
	if auth.Attributes != nil {
		candidates = append(candidates, strings.TrimSpace(auth.Attributes["account"]))
	}
	for _, candidate := range candidates {
		start := strings.LastIndex(candidate, "(")
		end := strings.LastIndex(candidate, ")")
		if start >= 0 && end > start+1 {
			return strings.TrimSpace(candidate[start+1 : end])
		}
	}
	return ""
}

func buildGeminiCLIQuotaBuckets(rawBuckets []any) []map[string]any {
	type bucketState struct {
		id                string
		label             string
		tokenType         string
		modelIDs          []string
		preferredModelID  string
		preferredFraction *float64
		preferredAmount   *float64
		preferredReset    string
		fallbackFraction  *float64
		fallbackAmount    *float64
		fallbackReset     string
	}

	lookup := make(map[string]struct {
		id               string
		label            string
		preferredModelID string
	})
	for _, def := range geminiQuotaGroupDefs {
		for _, modelID := range def.ModelIDs {
			lookup[modelID] = struct {
				id               string
				label            string
				preferredModelID string
			}{id: def.ID, label: def.Label, preferredModelID: def.PreferredModelID}
		}
	}

	grouped := make(map[string]*bucketState)
	order := make([]string, 0)
	for _, raw := range rawBuckets {
		bucket := quotaMap(raw)
		modelID := normalizeGeminiCLIModelID(quotaString(firstNonNil(bucket["modelId"], bucket["model_id"])))
		if modelID == "" || strings.HasPrefix(modelID, "gemini-2.0-flash") {
			continue
		}
		tokenType := quotaString(firstNonNil(bucket["tokenType"], bucket["token_type"]))
		fraction := quotaOptionalFraction(firstNonNil(bucket["remainingFraction"], bucket["remaining_fraction"]))
		amount := quotaOptionalNumber(firstNonNil(bucket["remainingAmount"], bucket["remaining_amount"]))
		resetTime := quotaString(firstNonNil(bucket["resetTime"], bucket["reset_time"]))
		groupDef, ok := lookup[modelID]
		groupID := modelID
		label := modelID
		preferredModelID := ""
		if ok {
			groupID = groupDef.id
			label = groupDef.label
			preferredModelID = groupDef.preferredModelID
		}
		mapKey := groupID + "::" + tokenType
		group, exists := grouped[mapKey]
		if !exists {
			group = &bucketState{id: groupID, label: label, tokenType: tokenType, preferredModelID: preferredModelID}
			grouped[mapKey] = group
			order = append(order, mapKey)
		}
		group.modelIDs = append(group.modelIDs, modelID)
		group.fallbackFraction = minQuotaNumberPointer(group.fallbackFraction, fraction)
		group.fallbackAmount = minQuotaNumberPointer(group.fallbackAmount, amount)
		group.fallbackReset = earlierResetTime(group.fallbackReset, resetTime)
		if preferredModelID != "" && modelID == preferredModelID {
			group.preferredFraction = fraction
			group.preferredAmount = amount
			group.preferredReset = resetTime
		}
	}

	buckets := make([]map[string]any, 0, len(order))
	for _, key := range order {
		group := grouped[key]
		fraction := group.preferredFraction
		if fraction == nil {
			fraction = group.fallbackFraction
		}
		amount := group.preferredAmount
		if amount == nil {
			amount = group.fallbackAmount
		}
		resetTime := group.preferredReset
		if resetTime == "" {
			resetTime = group.fallbackReset
		}
		entry := map[string]any{
			"id":                strings.Trim(group.id+"-"+group.tokenType, "-"),
			"label":             group.label,
			"remainingFraction": nil,
			"remainingAmount":   nil,
			"resetTime":         resetTime,
			"tokenType":         emptyStringToNil(group.tokenType),
			"modelIds":          uniqueStrings(group.modelIDs),
		}
		if fraction != nil {
			entry["remainingFraction"] = *fraction
		}
		if amount != nil {
			entry["remainingAmount"] = *amount
		}
		buckets = append(buckets, entry)
	}
	return buckets
}

func resolveGeminiCLITierLabel(payload map[string]any) any {
	tierID := resolveGeminiCLITierID(payload)
	switch tierID {
	case "free-tier":
		return "tier_free"
	case "legacy-tier":
		return "tier_legacy"
	case "standard-tier":
		return "tier_standard"
	case "g1-pro-tier":
		return "tier_pro"
	case "g1-ultra-tier":
		return "tier_ultra"
	case "":
		return nil
	default:
		return tierID
	}
}

func resolveGeminiCLITierID(payload map[string]any) string {
	paidTier := quotaMap(firstNonNil(payload["paidTier"], payload["paid_tier"]))
	currentTier := quotaMap(firstNonNil(payload["currentTier"], payload["current_tier"]))
	for _, tier := range []map[string]any{paidTier, currentTier} {
		if id := strings.ToLower(strings.TrimSpace(quotaString(tier["id"]))); id != "" {
			return id
		}
	}
	return ""
}

func resolveGeminiCLICreditBalance(payload map[string]any) any {
	paidTier := quotaMap(firstNonNil(payload["paidTier"], payload["paid_tier"]))
	currentTier := quotaMap(firstNonNil(payload["currentTier"], payload["current_tier"]))
	tier := paidTier
	if len(tier) == 0 {
		tier = currentTier
	}
	credits := quotaArray(firstNonNil(tier["availableCredits"], tier["available_credits"]))
	total := 0.0
	found := false
	for _, raw := range credits {
		credit := quotaMap(raw)
		creditType := quotaString(firstNonNil(credit["creditType"], credit["credit_type"]))
		if creditType != "GOOGLE_ONE_AI" {
			continue
		}
		amount := quotaOptionalNumber(firstNonNil(credit["creditAmount"], credit["credit_amount"]))
		if amount != nil {
			total += *amount
			found = true
		}
	}
	if !found {
		return nil
	}
	return total
}

func resolveAntigravityProjectID(auth *coreauth.Auth) string {
	if auth != nil {
		if auth.Metadata != nil {
			if projectID := strings.TrimSpace(quotaString(auth.Metadata["project_id"])); projectID != "" {
				return projectID
			}
		}
		if auth.Attributes != nil {
			if projectID := strings.TrimSpace(auth.Attributes["project_id"]); projectID != "" {
				return projectID
			}
		}
	}
	return defaultAntigravityProjectID
}

func buildAntigravityQuotaGroups(models map[string]any) []map[string]any {
	groups := make([]map[string]any, 0)
	for _, def := range antigravityGroupDefs {
		quotaEntries := make([]map[string]any, 0)
		for _, identifier := range def.Identifiers {
			id, entry := findAntigravityModel(models, identifier)
			if id == "" || len(entry) == 0 {
				continue
			}
			remaining := quotaOptionalFraction(firstNonNil(quotaMap(firstNonNil(entry["quotaInfo"], entry["quota_info"]))["remainingFraction"], quotaMap(firstNonNil(entry["quotaInfo"], entry["quota_info"]))["remaining_fraction"], quotaMap(firstNonNil(entry["quotaInfo"], entry["quota_info"]))["remaining"]))
			resetTime := quotaString(firstNonNil(quotaMap(firstNonNil(entry["quotaInfo"], entry["quota_info"]))["resetTime"], quotaMap(firstNonNil(entry["quotaInfo"], entry["quota_info"]))["reset_time"]))
			if remaining == nil {
				if resetTime == "" {
					continue
				}
				zero := 0.0
				remaining = &zero
			}
			quotaEntries = append(quotaEntries, map[string]any{
				"id":                id,
				"remainingFraction": *remaining,
				"resetTime":         resetTime,
				"displayName":       quotaString(entry["displayName"]),
			})
		}
		if len(quotaEntries) == 0 {
			continue
		}
		remainingFraction := 1.0
		resetTime := ""
		label := def.Label
		modelsList := make([]string, 0, len(quotaEntries))
		for _, entry := range quotaEntries {
			modelsList = append(modelsList, quotaString(entry["id"]))
			if value, ok := quotaFloat64(entry["remainingFraction"]); ok && value < remainingFraction {
				remainingFraction = value
			}
			if resetTime == "" {
				resetTime = quotaString(entry["resetTime"])
			}
			if def.LabelFromModel {
				if displayName := strings.TrimSpace(quotaString(entry["displayName"])); displayName != "" {
					label = displayName
				}
			}
		}
		groups = append(groups, map[string]any{
			"id":                def.ID,
			"label":             label,
			"models":            modelsList,
			"remainingFraction": remainingFraction,
			"resetTime":         emptyStringToNil(resetTime),
		})
	}
	return groups
}

func findAntigravityModel(models map[string]any, identifier string) (string, map[string]any) {
	if direct := quotaMap(models[identifier]); len(direct) > 0 {
		return identifier, direct
	}
	for key, raw := range models {
		entry := quotaMap(raw)
		if strings.EqualFold(quotaString(entry["displayName"]), identifier) {
			return key, entry
		}
	}
	return "", nil
}

func buildKimiQuotaRows(payload map[string]any) []map[string]any {
	rows := make([]map[string]any, 0)
	if usage := quotaMap(payload["usage"]); len(usage) > 0 {
		if row := buildKimiRow("usage", map[string]any{"label": "Usage"}, usage); len(row) > 0 {
			rows = append(rows, row)
		}
	}
	for index, raw := range quotaArray(payload["limits"]) {
		item := quotaMap(raw)
		label := quotaString(firstNonNil(item["name"], item["title"], item["scope"]))
		if label == "" {
			label = fmt.Sprintf("Limit %d", index+1)
		}
		detail := quotaMap(item["detail"])
		source := item
		if len(detail) > 0 {
			source = detail
		}
		if row := buildKimiRow(fmt.Sprintf("limit-%d", index), map[string]any{"label": label}, source); len(row) > 0 {
			rows = append(rows, row)
		}
	}
	return rows
}

func buildKimiRow(id string, label map[string]any, payload map[string]any) map[string]any {
	limit, okLimit := quotaInt64(payload["limit"])
	if !okLimit || limit <= 0 {
		return nil
	}
	used, okUsed := quotaInt64(payload["used"])
	if !okUsed {
		if remaining, okRemaining := quotaInt64(payload["remaining"]); okRemaining {
			used = limit - remaining
		}
	}
	if used < 0 {
		used = 0
	}
	row := map[string]any{
		"id":    id,
		"label": label["label"],
		"used":  used,
		"limit": limit,
	}
	if resetHint := kimiResetHint(payload); resetHint != "" {
		row["resetHint"] = resetHint
	}
	return row
}

func kimiResetHint(payload map[string]any) string {
	for _, key := range []string{"reset_at", "resetAt", "reset_time", "resetTime"} {
		if value := quotaString(payload[key]); value != "" {
			return value
		}
	}
	if seconds, ok := quotaInt64(firstNonNil(payload["reset_in"], payload["resetIn"], payload["ttl"])); ok && seconds > 0 {
		return time.Now().UTC().Add(time.Duration(seconds) * time.Second).Format(time.RFC3339)
	}
	return ""
}

func decodeQuotaJSONBody(body string) (map[string]any, error) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func marshalQuotaPayload(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return json.RawMessage(data)
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func quotaMap(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return cloneMap(typed)
	}
	return nil
}

func quotaArray(value any) []any {
	if typed, ok := value.([]any); ok {
		return typed
	}
	return nil
}

func quotaString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return fmt.Sprintf("%v", typed)
	case int64:
		return fmt.Sprintf("%d", typed)
	case int:
		return fmt.Sprintf("%d", typed)
	default:
		return ""
	}
}

func quotaFloat64(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		if typed == "" {
			return 0, false
		}
		parsed, err := json.Number(strings.TrimSpace(typed)).Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func quotaInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case string:
		if typed == "" {
			return 0, false
		}
		parsed, err := json.Number(strings.TrimSpace(typed)).Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func quotaBoolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func quotaPercent(value any) float64 {
	if raw, ok := quotaFloat64(value); ok {
		if raw <= 1 {
			return raw * 100
		}
		return raw
	}
	return 0
}

func quotaOptionalNumber(value any) *float64 {
	if raw, ok := quotaFloat64(value); ok {
		return &raw
	}
	return nil
}

func quotaOptionalFraction(value any) *float64 {
	if raw, ok := quotaFloat64(value); ok {
		if raw > 1 {
			normalized := raw / 100
			return &normalized
		}
		return &raw
	}
	if text := quotaString(value); strings.HasSuffix(text, "%") {
		if raw, ok := quotaFloat64(strings.TrimSuffix(text, "%")); ok {
			normalized := raw / 100
			return &normalized
		}
	}
	return nil
}

func minQuotaNumberPointer(current, next *float64) *float64 {
	if current == nil {
		return next
	}
	if next == nil {
		return current
	}
	if *next < *current {
		return next
	}
	return current
}

func earlierResetTime(current, next string) string {
	if strings.TrimSpace(current) == "" {
		return strings.TrimSpace(next)
	}
	if strings.TrimSpace(next) == "" {
		return strings.TrimSpace(current)
	}
	currentTime, errCurrent := time.Parse(time.RFC3339, current)
	nextTime, errNext := time.Parse(time.RFC3339, next)
	if errCurrent != nil {
		return next
	}
	if errNext != nil {
		return current
	}
	if nextTime.Before(currentTime) {
		return next
	}
	return current
}

func normalizeGeminiCLIModelID(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasSuffix(value, "_vertex") {
		return strings.TrimSuffix(value, "_vertex")
	}
	return value
}

func normalizeQuotaIdentifier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func emptyStringToNil(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
