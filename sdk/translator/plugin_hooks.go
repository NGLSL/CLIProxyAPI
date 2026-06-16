package translator

import "context"

// PluginHooks defines optional translator extension hooks provided by plugins.
// The plugin host implements this interface and installs it via
// Registry.SetPluginHooks so that plugin-supplied translators participate in
// the regular translation pipeline.
//
// Normalize* methods always run and may rewrite the body in place. The
// Translate* methods are fallbacks consulted only when no native translator is
// registered for the (from, to) format pair; the bool result reports whether
// the plugin supplied a translation. This split keeps plugin translators from
// overriding native behaviour while still allowing plugins to introduce new
// format pairs.
type PluginHooks interface {
	NormalizeRequest(ctx context.Context, from, to Format, model string, body []byte, stream bool) []byte
	TranslateRequest(ctx context.Context, from, to Format, model string, body []byte, stream bool) ([]byte, bool)
	NormalizeResponseBefore(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) []byte
	TranslateResponse(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) ([]byte, bool)
	NormalizeResponseAfter(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) []byte
}
