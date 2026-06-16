package chat_completions

import (
	. "github.com/NGLSL/CLIProxyAPI/v7/internal/constant"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/interfaces"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenAI,
		GeminiCLI,
		ConvertOpenAIRequestToGeminiCLI,
		interfaces.TranslateResponse{
			Stream:    ConvertCliResponseToOpenAI,
			NonStream: ConvertCliResponseToOpenAINonStream,
		},
	)
}
