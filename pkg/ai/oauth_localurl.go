package ai

import "context"

type localAuthURLKey struct{}

// WithLocalAuthURLSink returns a ctx that carries sink. A login caller that
// auto-opens a browser on this machine sets it; a provider that shows the
// user a manual (copy-the-code) authorize URL reports the loopback
// authorize URL here before calling OnAuth, so the caller opens that one
// locally and the callback server catches the redirect.
func WithLocalAuthURLSink(ctx context.Context, sink func(url string)) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, localAuthURLKey{}, sink)
}

// ReportLocalAuthURL hands url to the sink on ctx, if any.
func ReportLocalAuthURL(ctx context.Context, url string) {
	if sink, ok := ctx.Value(localAuthURLKey{}).(func(string)); ok {
		sink(url)
	}
}
