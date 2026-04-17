package responses

import (
	. "github.com/NGLSL/CLIProxyAPI/v6/internal/constant"
	"github.com/NGLSL/CLIProxyAPI/v6/internal/interfaces"
	"github.com/NGLSL/CLIProxyAPI/v6/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenaiResponse,
		Antigravity,
		ConvertOpenAIResponsesRequestToAntigravity,
		interfaces.TranslateResponse{
			Stream:    ConvertAntigravityResponseToOpenAIResponses,
			NonStream: ConvertAntigravityResponseToOpenAIResponsesNonStream,
		},
	)
}
