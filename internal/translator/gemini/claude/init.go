package claude

import (
	. "github.com/NGLSL/CLIProxyAPI/v7/internal/constant"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/interfaces"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		Claude,
		Gemini,
		ConvertClaudeRequestToGemini,
		interfaces.TranslateResponse{
			Stream:     ConvertGeminiResponseToClaude,
			NonStream:  ConvertGeminiResponseToClaudeNonStream,
			TokenCount: ClaudeTokenCount,
		},
	)
}
