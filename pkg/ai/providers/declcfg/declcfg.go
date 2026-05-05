// Package declcfg implements the substitution grammar and JSON template
// engine shared by all declarative provider configs.
//
// See RECAP.md (top-level) for the design.  Briefly:
//
//   - String values support ${path.to.var} and ${fn.name(arg1,arg2)} substitutions.
//   - The literal JSON value "$inner" is replaced by an inner JSON value
//     supplied by the caller (used by request envelopes that wrap an inner
//     request body).  Outside JSON values it is plain text.
//   - $$ escapes a literal $.
//
// Variables are looked up in a Context whose Vars map carries dotted keys
// (e.g. "model.id", "creds.access_token") populated by the adapter per
// request.  Env vars are gated through Context.Env to avoid arbitrary
// process-environment exfiltration; only keys an adapter explicitly exposes
// are reachable.
//
// The function set is deliberately small.  Adding functions is forever-public
// extension API; do so only when a concrete provider proves the need.
package declcfg

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Context is the per-request substitution context.
type Context struct {
	// Vars carries variables looked up by ${path.to.value}.  Keys are dotted
	// strings; nested maps are not used.  Values are stringified by Sprint.
	Vars map[string]any

	// Env returns the value of an environment variable.  Adapters must gate
	// this through an allowlist (e.g. the provider's declared EnvKeys) — the
	// substitution engine does not read process env directly.  Return ok=false
	// to surface a missing-env error to the caller.
	Env func(name string) (value string, ok bool)

	// Funcs is the function registry.  If nil, DefaultFuncs() is used.
	Funcs map[string]Func
}

// Func is a substitution-grammar function.  Args are pre-parsed bare-word or
// 'single-quoted' tokens.
type Func func(args []string) (string, error)

// DefaultFuncs returns the standard function set: fn.rand_id, fn.unix_millis.
// Callers may extend the returned map but should not mutate the returned
// values.
func DefaultFuncs() map[string]Func {
	return map[string]Func{
		"fn.rand_id":     fnRandID,
		"fn.unix_millis": fnUnixMillis,
	}
}

func fnRandID(args []string) (string, error) {
	prefix := ""
	if len(args) > 0 {
		prefix = args[0]
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand_id: %w", err)
	}
	for i := range b {
		b[i] = alphabet[b[i]%byte(len(alphabet))]
	}
	suffix := string(b)
	if prefix == "" {
		return fmt.Sprintf("%d-%s", time.Now().UnixMilli(), suffix), nil
	}
	return fmt.Sprintf("%s-%d-%s", prefix, time.Now().UnixMilli(), suffix), nil
}

func fnUnixMillis(args []string) (string, error) {
	if len(args) != 0 {
		return "", fmt.Errorf("unix_millis: takes no args")
	}
	return strconv.FormatInt(time.Now().UnixMilli(), 10), nil
}

// Substitute replaces all ${...} expressions in s using ctx.  Use this for
// config string fields (URL templates, header values).  For JSON envelopes
// (which need the "$inner" sentinel and recursive walking), call SubstituteJSON.
//
// Errors include the failing expression for diagnosability.
func Substitute(s string, ctx *Context) (string, error) {
	if ctx == nil {
		return "", errors.New("declcfg: nil Context")
	}
	var out strings.Builder
	i := 0
	for i < len(s) {
		// $$ → literal $
		if s[i] == '$' && i+1 < len(s) && s[i+1] == '$' {
			out.WriteByte('$')
			i += 2
			continue
		}
		// ${...}
		if s[i] == '$' && i+1 < len(s) && s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				return "", fmt.Errorf("declcfg: unterminated ${ in %q", s)
			}
			expr := s[i+2 : i+2+end]
			val, err := evalExpr(expr, ctx)
			if err != nil {
				return "", fmt.Errorf("declcfg: %w (in %q)", err, s)
			}
			out.WriteString(val)
			i += 3 + end
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String(), nil
}

// evalExpr evaluates a single inner expression (without surrounding ${}).
// Either a dotted variable path or a function call name(arg1,arg2,...).
func evalExpr(expr string, ctx *Context) (string, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return "", errors.New("empty expression")
	}
	// Function call: contains '(' and ends with ')'.
	if i := strings.IndexByte(expr, '('); i >= 0 {
		if !strings.HasSuffix(expr, ")") {
			return "", fmt.Errorf("malformed function call %q", expr)
		}
		name := strings.TrimSpace(expr[:i])
		argstr := expr[i+1 : len(expr)-1]
		args, err := splitArgs(argstr)
		if err != nil {
			return "", err
		}
		funcs := ctx.Funcs
		if funcs == nil {
			funcs = DefaultFuncs()
		}
		fn, ok := funcs[name]
		if !ok {
			return "", fmt.Errorf("unknown function %q", name)
		}
		return fn(args)
	}
	// Variable lookup.
	return lookupVar(expr, ctx)
}

func lookupVar(path string, ctx *Context) (string, error) {
	if strings.HasPrefix(path, "env.") {
		name := path[len("env."):]
		if ctx.Env == nil {
			return "", fmt.Errorf("env access not permitted (env.%s)", name)
		}
		v, ok := ctx.Env(name)
		if !ok {
			return "", fmt.Errorf("env var %s not set or not allowed", name)
		}
		return v, nil
	}
	v, ok := ctx.Vars[path]
	if !ok {
		return "", fmt.Errorf("undefined variable %q", path)
	}
	return stringify(v), nil
}

func stringify(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		// JSON numbers decode to float64; format as int when whole.
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case json.Number:
		return string(x)
	default:
		return fmt.Sprint(v)
	}
}

// splitArgs parses a comma-separated arg list with optional single-quoted
// strings.  Bare tokens are trimmed.  No nested ${} or function calls are
// allowed inside args (kept simple on purpose).
func splitArgs(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var args []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' && !inQuote:
			inQuote = true
		case c == '\'' && inQuote:
			inQuote = false
		case c == ',' && !inQuote:
			args = append(args, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("unterminated single-quoted string in args")
	}
	args = append(args, strings.TrimSpace(cur.String()))
	return args, nil
}

// SubstituteJSON walks a JSON-decoded value (as produced by json.Unmarshal
// into any) and substitutes:
//
//   - every string element/value via Substitute(...)
//   - every value equal to the literal string "$inner" with `inner`
//
// inner may be nil (in which case "$inner" substitutes to a JSON null).
// Maps and slices are walked recursively; numbers/bools/null are returned
// unchanged.  The returned value is suitable for json.Marshal.
//
// "$inner" replacement happens only at JSON value positions, not inside other
// strings (a string like "prefix-$inner" passes through Substitute as-is —
// the $-sequence is plain text since it isn't `${...}`).
func SubstituteJSON(node any, ctx *Context, inner any) (any, error) {
	switch v := node.(type) {
	case nil:
		return nil, nil
	case string:
		if v == "$inner" {
			return inner, nil
		}
		return Substitute(v, ctx)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, child := range v {
			r, err := SubstituteJSON(child, ctx, inner)
			if err != nil {
				return nil, fmt.Errorf("at key %q: %w", k, err)
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			r, err := SubstituteJSON(child, ctx, inner)
			if err != nil {
				return nil, fmt.Errorf("at [%d]: %w", i, err)
			}
			out[i] = r
		}
		return out, nil
	default:
		// numbers, bools — unchanged
		return v, nil
	}
}
