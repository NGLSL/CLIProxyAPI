package chat_completions

import (
	. "github.com/NGLSL/CLIProxyAPI/v6/internal/constant"
	"github.com/NGLSL/CLIProxyAPI/v6/internal/interfaces"
	"github.com/NGLSL/CLIProxyAPI/v6/internal/translator/translator"
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
