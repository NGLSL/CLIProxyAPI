// Claude thinking signature validation wrappers for Antigravity bypass mode.
package claude

import (
	"fmt"
	"strings"

	"github.com/NGLSL/CLIProxyAPI/v7/internal/cache"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const maxBypassSignatureLen = signature.MaxClaudeThinkingSignatureLen

type claudeSignatureTree = signature.ClaudeSignatureTree

// StripEmptySignatureThinkingBlocks removes thinking blocks whose signatures
// are empty after trimming whitespace and any optional cache prefix.
func StripEmptySignatureThinkingBlocks(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload
	}
	modified := false
	for i, msg := range messages.Array() {
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		var kept []string
		stripped := false
		for _, part := range content.Array() {
			if part.Get("type").String() == "thinking" && !hasNonEmptyClaudeSignature(part.Get("signature").String()) {
				stripped = true
				continue
			}
			kept = append(kept, part.Raw)
		}
		if stripped {
			modified = true
			if len(kept) == 0 {
				payload, _ = sjson.SetRawBytes(payload, fmt.Sprintf("messages.%d.content", i), []byte("[]"))
			} else {
				payload, _ = sjson.SetRawBytes(payload, fmt.Sprintf("messages.%d.content", i), []byte("["+strings.Join(kept, ",")+"]"))
			}
		}
	}
	if !modified {
		return payload
	}
	return payload
}

// StripInvalidBypassSignatureThinkingBlocks 在严格 bypass 模式下移除无法通过
// Antigravity Claude 签名校验的 thinking 块，避免把伪造或跨供应商签名继续发给上游。
func StripInvalidBypassSignatureThinkingBlocks(payload []byte) []byte {
	return signature.StripInvalidClaudeThinkingBlocks(payload, claudeBypassSignatureValidationOptions())
}

func hasNonEmptyClaudeSignature(sig string) bool {
	sig = strings.TrimSpace(sig)
	if sig == "" {
		return false
	}
	if idx := strings.IndexByte(sig, '#'); idx >= 0 {
		sig = strings.TrimSpace(sig[idx+1:])
	}
	return sig != ""
}

func ValidateClaudeBypassSignatures(inputRawJSON []byte) error {
	return signature.ValidateClaudeThinkingSignatures(inputRawJSON, claudeBypassSignatureValidationOptions())
}

func normalizeClaudeBypassSignature(rawSignature string) (string, error) {
	return signature.NormalizeClaudeThinkingSignature(rawSignature, claudeBypassSignatureValidationOptions())
}

func inspectDoubleLayerSignature(sig string) (*claudeSignatureTree, error) {
	return signature.InspectClaudeDoubleLayerSignature(sig)
}

func inspectSingleLayerSignature(sig string) (*claudeSignatureTree, error) {
	return signature.InspectClaudeSingleLayerSignature(sig)
}

func inspectClaudeSignaturePayload(payload []byte, encodingLayers int) (*claudeSignatureTree, error) {
	return signature.InspectClaudeSignaturePayload(payload, encodingLayers)
}

func claudeBypassSignatureValidationOptions() signature.ClaudeSignatureValidationOptions {
	return signature.ClaudeSignatureValidationOptions{Strict: cache.SignatureBypassStrictMode()}
}
