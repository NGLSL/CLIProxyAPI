package claude

import (
	. "github.com/NGLSL/CLIProxyAPI/v6/internal/constant"
	"github.com/NGLSL/CLIProxyAPI/v6/internal/interfaces"
	"github.com/NGLSL/CLIProxyAPI/v6/internal/translator/translator"
)

func init() {
	translator.Register(
		Claude,
		GeminiCLI,
		ConvertClaudeRequestToCLI,
		interfaces.TranslateResponse{
			Stream:     ConvertGeminiCLIResponseToClaude,
			NonStream:  ConvertGeminiCLIResponseToClaudeNonStream,
			TokenCount: ClaudeTokenCount,
		},
	)
}
