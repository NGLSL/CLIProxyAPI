package geminiCLI

import (
	. "github.com/NGLSL/CLIProxyAPI/v7/internal/constant"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/interfaces"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		GeminiCLI,
		Codex,
		ConvertGeminiCLIRequestToCodex,
		interfaces.TranslateResponse{
			Stream:     ConvertCodexResponseToGeminiCLI,
			NonStream:  ConvertCodexResponseToGeminiCLINonStream,
			TokenCount: GeminiCLITokenCount,
		},
	)
}
