// Side-query request marking.
//
// The advisor path (AgentSession.SideQueryStream) needs the provider to treat
// its request differently from an executor turn: a separate prompt-cache
// namespace, long cache retention, and anchored breakpoint placement. None of
// that can be expressed through github.com/kfet/agent's SimplePromptOptions,
// which carries only Model and Reasoning — and that module is external and
// must not be modified.
//
// It does not need to be. Agent.SimplePromptStream resolves its stream
// function through agent.DefaultStreamFn(ctx) using the CALL's context, and
// fir owns that factory (defaultstream.go). So a value on the context fir
// itself passes into SimplePromptStream comes straight back to fir's own
// closure, where the per-call stream options are specialised. The marker
// never touches the external module's types, and never reaches any wire.
//
// The alternative — cloning the *ai.Model and hiding a marker in
// Model.Headers — was rejected: headers are provider-visible, so every
// provider would need to filter the marker out or leak it upstream.
//
// INVARIANT: fir must not set a per-agent stream function
// (AgentOptions.StreamFn / Agent.SetStreamFn). resolveStreamFn returns that
// override in preference to the DefaultStreamFn factory, so a side query
// routed through it would silently lose the marker — going out un-namespaced,
// on short retention, with the tail breakpoint back on the one-off question.
// The failure is invisible: correct answers, quietly full-price. If a per-
// agent stream fn is ever needed, it must apply
// providers.ApplySideQueryOptions itself.

package session

import "context"

// sideQueryCtxKey is the private context key marking a side-query call.
type sideQueryCtxKey struct{}

// withSideQuery marks ctx as belonging to a side query.
func withSideQuery(ctx context.Context) context.Context {
	return context.WithValue(ctx, sideQueryCtxKey{}, true)
}

// isSideQueryContext reports whether ctx was marked by withSideQuery.
func isSideQueryContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(sideQueryCtxKey{}).(bool)
	return v
}
