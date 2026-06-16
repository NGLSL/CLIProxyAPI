package chat_completions

import (
	. "github.com/NGLSL/CLIProxyAPI/v7/internal/constant"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/interfaces"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenAI,
		Codex,
		ConvertOpenAIRequestToCodex,
		interfaces.TranslateResponse{
			Stream:    ConvertCodexResponseToOpenAI,
			NonStream: ConvertCodexResponseToOpenAINonStream,
		},
	)
}
