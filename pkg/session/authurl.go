package session

import (
	"context"
	"sync/atomic"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/pinoauth"
)

// WithLocalAuthURL arms ctx to capture the loopback authorize URL a
// provider reports before OnAuth (see ai.WithLocalAuthURLSink). The
// returned func picks the URL to open in this host's browser: the
// loopback URL when one was reported, else the shown (short) URL. The
// shown URL is still displayed for a browser on another device.
func WithLocalAuthURL(ctx context.Context) (context.Context, func(pinoauth.AuthInfo) string) {
	var local atomic.Pointer[string]
	ctx = ai.WithLocalAuthURLSink(ctx, func(u string) { local.Store(&u) })
	return ctx, func(info pinoauth.AuthInfo) string {
		if u := local.Load(); u != nil {
			return *u
		}
		return PreferredAuthURL(info.URL, info.ShortURL)
	}
}
