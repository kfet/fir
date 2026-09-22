package extension

import (
	"regexp"
	"sync"

	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/models"
)

// Client version placeholders let an extension declare a HEADER TEMPLATE
// whose version scalar is supplied by the catalog overlay rather than by a
// literal compiled into the extension. The extension writes:
//
//	token_headers={"User-Agent": "claude-cli/{clientVersion.claudeCode} (external, cli)"}
//
// and Go substitutes the effective pin (see models.ModelRegistry.ClientVersion)
// at call time. That is what allows a vendor-gated client version to ship as
// DATA on the catalog channel instead of as a one-line binary release.
//
// The trust line, deliberately narrow (see validateCatalogProviders in
// pkg/models/catalog.go): expansion happens in header VALUES ONLY — never in
// a header NAME, never in a URL — the substituted value can only ever match
// models.ClientVersionRE, and the surrounding text is compiled into the
// extension. The overlay changes what a request LOOKS LIKE, never where it
// GOES or what CREDENTIAL it carries.
var clientVersionPlaceholderRE = regexp.MustCompile(`\{clientVersion\.([A-Za-z0-9]+)\}`)

// unknownClientVersionKeys deduplicates the warning below to once per key per
// process, so a mistyped template is loud but not a log flood.
var unknownClientVersionKeys sync.Map

// expandClientVersions substitutes {clientVersion.<key>} in v using lookup.
//
// An unknown or empty key is left LITERAL and warned about once: a header
// reading "claude-cli/{clientVersion.foo}" is a greppable, self-describing
// failure, whereas silently dropping it would send a plausible-looking but
// malformed value and surface only as an opaque vendor 400.
func expandClientVersions(v string, lookup func(key string) string) string {
	if lookup == nil || !clientVersionPlaceholderRE.MatchString(v) {
		return v
	}
	return clientVersionPlaceholderRE.ReplaceAllStringFunc(v, func(match string) string {
		key := clientVersionPlaceholderRE.FindStringSubmatch(match)[1]
		value := lookup(key)
		if value == "" {
			if _, seen := unknownClientVersionKeys.LoadOrStore(key, struct{}{}); !seen {
				firlog.Warn("extension: unknown client version key %q — leaving %q literal", key, match)
			}
			return match
		}
		return value
	})
}

// expandClientVersionHeaders returns a copy of headers with every VALUE
// expanded. Keys are passed through untouched, on purpose.
func expandClientVersionHeaders(headers map[string]string, lookup func(key string) string) map[string]string {
	if len(headers) == 0 {
		return headers
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[k] = expandClientVersions(v, lookup)
	}
	return out
}

// clientVersion resolves a pin for this bridge: through the session's model
// registry when there is one (so a hot-applied overlay takes effect with no
// restart), otherwise through the compiled-in floor — which is exactly what a
// registry-less host such as `fir login` should advertise.
func (b *Bridge) clientVersion(key string) string {
	if api := b.api.Load(); api != nil {
		if v := (*api).GetClientVersion(key); v != "" {
			return v
		}
	}
	return models.DefaultClientVersions().Get(key)
}

// clientVersionLookup is b.clientVersion as a plain function, for the
// expanders (and for tests to substitute).
func (b *Bridge) clientVersionLookup() func(key string) string {
	if b == nil {
		return models.DefaultClientVersions().Get
	}
	return b.clientVersion
}

// clientVersionsParam is the {"claudeCode": "..."} map handed to extension
// hooks that make their OWN HTTP calls (auth/list_models), where Go has no
// response to post-process. It is the same validated scalar from the same
// source — a convenience on top of the one mechanism, not a second source of
// truth.
func (b *Bridge) clientVersionsParam() map[string]string {
	lookup := b.clientVersionLookup()
	out := map[string]string{}
	for _, key := range []string{models.ClientVersionKeyClaudeCode} {
		if v := lookup(key); v != "" {
			out[key] = v
		}
	}
	return out
}
