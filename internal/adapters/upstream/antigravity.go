package upstream

import "context"

// executeAntigravity answers a non-streaming request through Cloud Code
// v1internal:generateContent. Placeholder until the translation lands.
func (a *HTTPAdapter) executeAntigravity(ctx context.Context, target Target, req responsesWireRequest, items []responsesItem, catalog toolCatalog) (Result, error) {
	return Result{}, unsupported("the Antigravity protocol is not implemented yet")
}

// streamAntigravity streams a request through Cloud Code
// v1internal:streamGenerateContent. Placeholder until the translation lands.
func (a *HTTPAdapter) streamAntigravity(ctx context.Context, target Target, req responsesWireRequest, items []responsesItem, catalog toolCatalog, emit func(Event) error) (Result, error) {
	return Result{}, unsupported("the Antigravity protocol is not implemented yet")
}
