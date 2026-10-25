package providers

import (
	"context"
	"net/http"
	"strings"
)

// Response-header capture.
//
// Every built-in provider sends its HTTP requests through httpTransport, so
// that single RoundTripper is the one place that sees every provider
// response. A caller that wants the headers puts a sink on the request
// context with WithResponseHeaderSink; providerTransport hands the sink the
// redacted header map of each response it receives (success or error status
// alike). Without a sink the cost is one context lookup per request.

type respHeaderSinkKey struct{}

// ResponseHeaderSink receives the redacted, lowercase-keyed headers of a
// provider HTTP response.
type ResponseHeaderSink func(map[string]string)

// WithResponseHeaderSink returns ctx carrying sink. Provider responses made
// with the returned context (or one derived from it) are reported to sink.
func WithResponseHeaderSink(ctx context.Context, sink ResponseHeaderSink) context.Context {
	return context.WithValue(ctx, respHeaderSinkKey{}, sink)
}

// ResponseHeaderSinkFrom returns the sink carried by ctx, or nil.
func ResponseHeaderSinkFrom(ctx context.Context) ResponseHeaderSink {
	sink, _ := ctx.Value(respHeaderSinkKey{}).(ResponseHeaderSink)
	return sink
}

func reportResponseHeaders(req *http.Request, resp *http.Response) {
	if resp == nil || req == nil {
		return
	}
	sink := ResponseHeaderSinkFrom(req.Context())
	if sink == nil {
		return
	}
	sink(RedactHeaders(resp.Header))
}

// RedactHeaders flattens h into a lowercase name -> value map (multiple
// values joined with ", ") and drops headers that may carry credentials or
// session state: cookies, auth headers, and any name containing token,
// secret, api-key, apikey, session or auth.
func RedactHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		name := strings.ToLower(k)
		if sensitiveHeader(name) {
			continue
		}
		out[name] = strings.Join(v, ", ")
	}
	return out
}

func sensitiveHeader(name string) bool {
	switch name {
	case "set-cookie", "set-cookie2", "cookie", "authorization",
		"proxy-authorization", "www-authenticate", "proxy-authenticate":
		return true
	}
	for _, frag := range []string{"token", "secret", "api-key", "apikey", "session", "auth"} {
		if strings.Contains(name, frag) {
			return true
		}
	}
	return false
}

// RedactHeaderMap is RedactHeaders for an already-flattened map (e.g. one
// reported by an extension provider): keys are lowercased and sensitive
// headers dropped.
func RedactHeaderMap(h map[string]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		name := strings.ToLower(k)
		if !sensitiveHeader(name) {
			out[name] = v
		}
	}
	return out
}
