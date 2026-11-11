package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/netip"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// target is a validated UploadTarget or DownloadTarget.
type target struct {
	FileID      string            `json:"fileId"`
	URL         string            `json:"url"`
	Path        string            `json:"path"`
	Method      string            `json:"method"`
	Form        *uploadForm       `json:"form"`
	Headers     map[string]string `json:"headers"`
	Credentials string            `json:"credentials"`
	ExpiresAt   string            `json:"expiresAt"`
	// download only
	Name     string  `json:"name"`
	MimeType string  `json:"mimeType"`
	Size     int64   `json:"size"`
	Digest   *digest `json:"digest"`

	resolved   *url.URL
	include    bool
	privateOK  bool // may connect to non-global addresses
	allowProxy bool
}

type uploadForm struct {
	Fields []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"fields"`
	FileField string `json:"fileField"`
}

type digest struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

var (
	uploadKeys   = keySet("fileId", "url", "path", "method", "form", "headers", "credentials", "expiresAt", "_meta")
	downloadKeys = keySet("name", "mimeType", "size", "digest", "url", "path", "headers", "credentials", "expiresAt", "_meta")
	formKeys     = keySet("fields", "fileField")
	fieldKeys    = keySet("name", "value")
	digestKeys   = keySet("algorithm", "value")

	targetPathRE = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)+$`)

	// Fetch's forbidden request headers, plus the credential headers the
	// extension forbids outright.
	forbiddenHeaders = keySet("authorization", "cookie", "accept-charset", "accept-encoding",
		"access-control-request-headers", "access-control-request-method", "connection",
		"content-length", "cookie2", "date", "dnt", "expect", "host", "keep-alive", "origin",
		"referer", "set-cookie", "te", "trailer", "transfer-encoding", "upgrade", "via",
		"x-http-method", "x-http-method-override", "x-method-override")
	routingHeaders = keySet("forwarded", "x-original-url", "x-rewrite-url",
		"x-http-method-override", "x-http-method", "x-method-override")
	formHeaders = keySet("content-type", "content-encoding", "content-range", "content-digest", "repr-digest")
)

func keySet(ks ...string) map[string]bool {
	m := make(map[string]bool, len(ks))
	for _, k := range ks {
		m[k] = true
	}
	return m
}

// errTarget is the generic rejection. Messages never carry the target's URL,
// headers or form values: those are secrets.
func errTarget(why string) error { return fmt.Errorf("rejected transfer target: %s", why) }

// checkKeys drops null members of m and rejects unknown ones.
func checkKeys(m map[string]any, allowed map[string]bool) error {
	for k, v := range m {
		if v == nil {
			delete(m, k)
			continue
		}
		if !allowed[k] {
			return errTarget("unknown member")
		}
	}
	return nil
}

// parseTarget validates raw against the extension's target rules.
func (e *filesEnv) parseTarget(raw json.RawMessage, upload bool) (*target, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, errTarget("not an object")
	}
	allowed := downloadKeys
	if upload {
		allowed = uploadKeys
	}
	if err := checkKeys(m, allowed); err != nil {
		return nil, err
	}
	if f, ok := m["form"].(map[string]any); ok {
		if err := checkKeys(f, formKeys); err != nil {
			return nil, err
		}
		fields, _ := f["fields"].([]any)
		for _, fl := range fields {
			fm, ok := fl.(map[string]any)
			if !ok {
				return nil, errTarget("bad form field")
			}
			if err := checkKeys(fm, fieldKeys); err != nil {
				return nil, err
			}
		}
	}
	if d, ok := m["digest"].(map[string]any); ok {
		if err := checkKeys(d, digestKeys); err != nil {
			return nil, err
		}
	}
	clean, _ := json.Marshal(m)
	var t target
	if err := json.Unmarshal(clean, &t); err != nil {
		return nil, errTarget("member of the wrong type")
	}
	return &t, e.validate(&t, upload, m)
}

func (e *filesEnv) validate(t *target, upload bool, m map[string]any) error {
	if (t.URL == "") == (t.Path == "") {
		return errTarget("needs exactly one of url and path")
	}
	switch t.Credentials {
	case "omit":
	case "include":
		t.include = true
	default:
		return errTarget("credentials must be omit or include")
	}
	if upload {
		if t.FileID == "" {
			return errTarget("missing fileId")
		}
		switch t.Method {
		case "":
			t.Method = http.MethodPut
		case http.MethodPut, http.MethodPost:
		default:
			return errTarget("method must be PUT or POST")
		}
		if t.Form != nil && t.Method != http.MethodPost {
			return errTarget("form requires POST")
		}
		if _, ok := m["form"]; ok && t.Form == nil {
			return errTarget("bad form")
		}
	} else {
		if _, ok := m["mimeType"]; !ok {
			return errTarget("missing mimeType")
		}
		if _, ok := m["size"]; !ok || t.Size < 0 {
			return errTarget("missing or negative size")
		}
		if t.Digest != nil && (t.Digest.Algorithm != "sha-256" || t.Digest.Value == "") {
			return errTarget("unsupported digest")
		}
	}
	if t.ExpiresAt != "" {
		exp, err := time.Parse(time.RFC3339, t.ExpiresAt)
		if err != nil {
			return errTarget("bad expiresAt")
		}
		if time.Now().After(exp) {
			return errTarget("expired")
		}
	}
	if e.mcpURL == nil && (t.Path != "" || t.include) {
		return errTarget("a stdio server may only issue url targets with credentials omit")
	}
	if err := e.resolve(t); err != nil {
		return err
	}
	return checkHeaders(t)
}

// resolve sets t.resolved and the network permissions for t.
func (e *filesEnv) resolve(t *target) error {
	if t.Path != "" {
		if !targetPathRE.MatchString(t.Path) {
			return errTarget("bad path")
		}
		for _, seg := range strings.Split(t.Path[1:], "/") {
			if seg == "." || seg == ".." {
				return errTarget("bad path")
			}
		}
		u := *e.mcpURL
		u.Path = strings.TrimRight(u.Path, "/") + t.Path
		u.RawPath = ""
		u.Fragment = ""
		if u.Scheme != "https" && !isLoopbackHost(u.Hostname()) && !e.originAllowed(&u) {
			return errTarget("path over plain http")
		}
		t.resolved = &u
		t.privateOK = true
		t.allowProxy = true
		return nil
	}
	u, err := url.Parse(t.URL)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return errTarget("url is not absolute")
	}
	if u.User != nil {
		return errTarget("url carries userinfo")
	}
	allowed := e.originAllowed(u)
	switch u.Scheme {
	case "https":
	case "http":
		if !allowed || !isLoopbackHost(u.Hostname()) {
			return errTarget("url is not https")
		}
	default:
		return errTarget("url is not https")
	}
	if len(e.cfg.AllowedOrigins) > 0 && !allowed {
		return errTarget("origin not in files.allowedOrigins")
	}
	if t.include && (e.mcpURL == nil || originOf(u) != originOf(e.mcpURL)) {
		return errTarget("credentials include on another origin")
	}
	t.resolved = u
	t.privateOK = allowed
	t.allowProxy = allowed || t.include
	return nil
}

func (e *filesEnv) originAllowed(u *url.URL) bool {
	o := originOf(u)
	for _, a := range e.cfg.AllowedOrigins {
		if au, err := url.Parse(a); err == nil && au.Host != "" && originOf(au) == o {
			return true
		}
	}
	return false
}

func checkHeaders(t *target) error {
	for k, v := range t.Headers {
		lk := strings.ToLower(k)
		if !validHeaderName(k) || !validHeaderValue(v) {
			return errTarget("invalid header")
		}
		if forbiddenHeaders[lk] || strings.HasPrefix(lk, "proxy-") || strings.HasPrefix(lk, "sec-") {
			return errTarget("forbidden header")
		}
		if (t.Path != "" || t.include) && (routingHeaders[lk] || strings.HasPrefix(lk, "x-forwarded-")) {
			return errTarget("forbidden header")
		}
		if t.Form != nil && formHeaders[lk] {
			return errTarget("forbidden header on a form target")
		}
	}
	if f := t.Form; f != nil {
		if badFormName(f.FileField) {
			return errTarget("bad form fileField")
		}
		for _, fl := range f.Fields {
			if badFormName(fl.Name) || fl.Name == f.FileField || strings.ContainsAny(fl.Value, "\r\n\x00") {
				return errTarget("bad form field")
			}
		}
	}
	return nil
}

func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c >= 0x7f || strings.IndexByte(`"(),/:;<=>?@[\]{}`, c) >= 0 {
			return false
		}
	}
	return true
}

func validHeaderValue(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < ' ' && c != '\t') || c == 0x7f {
			return false
		}
	}
	return strings.TrimSpace(s) == s
}

func badFormName(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			return true
		}
	}
	return false
}

// ---- network --------------------------------------------------------------

// nonGlobal lists special-purpose ranges that are not globally reachable
// (RFC 6890 and successors) beyond what netip's predicates cover.
var nonGlobal = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func globallyReachable(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range nonGlobal {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// defaultTransferClient builds the HTTP client for one transfer: no
// redirects, no cookies, no ambient credentials, and no connections to
// non-global addresses unless the operator allowed the target.
func defaultTransferClient(t *target) (*http.Client, error) {
	var proxy func(*http.Request) (*url.URL, error)
	if pu, _ := http.ProxyFromEnvironment(&http.Request{URL: t.resolved}); pu != nil {
		if !t.allowProxy {
			return nil, errTarget("behind a proxy, url targets must be listed in files.allowedOrigins")
		}
		proxy = http.ProxyFromEnvironment
	}
	privateOK := t.privateOK
	dialer := &net.Dialer{
		Timeout: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			if privateOK || proxy != nil {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			a, err := netip.ParseAddr(host)
			if err != nil || !globallyReachable(a) {
				return errors.New("target address is not globally reachable")
			}
			return nil
		},
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 proxy,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   30 * time.Second,
			ResponseHeaderTimeout: 5 * time.Minute,
			ForceAttemptHTTP2:     true,
			DisableKeepAlives:     true, // one client per transfer: no idle conns left behind
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

// scrubErr strips the URL a *url.Error carries.
func scrubErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func (e *filesEnv) newRequest(ctx context.Context, t *target, method string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, t.resolved.String(), body)
	if err != nil {
		return nil, errTarget("bad url")
	}
	for k, v := range t.Headers {
		req.Header.Set(k, v)
	}
	if t.include && e.token != nil {
		tok, err := e.token(ctx)
		if err != nil {
			return nil, fmt.Errorf("MCP credential: %w", err)
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	return req, nil
}

func (e *filesEnv) do(t *target, req *http.Request) (*http.Response, error) {
	client, err := e.newHTTP(t)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, scrubErr(err)
	}
	return resp, nil
}

const maxPutAttempts = 3

// transferUp sends f to t.
func (e *filesEnv) transferUp(ctx context.Context, t *target, f *pendingFile) error {
	for attempt := 1; ; attempt++ {
		retryable, err := e.sendOnce(ctx, t, f)
		if err == nil {
			return nil
		}
		if !retryable || t.Method != http.MethodPut || attempt >= maxPutAttempts || ctx.Err() != nil {
			return fmt.Errorf("upload of %s failed: %w", f.name, err)
		}
	}
}

func (e *filesEnv) sendOnce(ctx context.Context, t *target, f *pendingFile) (bool, error) {
	fh, err := e.readFile(f.path)
	if err != nil {
		return false, err
	}
	defer fh.Close()
	if st, err := fh.Stat(); err != nil || st.Size() != f.size {
		return false, errors.New("file changed since it was approved")
	}
	body := io.Reader(io.LimitReader(fh, f.size))
	length := f.size
	var ctype string
	if t.Form != nil {
		pre, post, ct, err := multipartFrame(t.Form, f)
		if err != nil {
			return false, err
		}
		body = io.MultiReader(bytes.NewReader(pre), body, bytes.NewReader(post))
		length += int64(len(pre) + len(post))
		ctype = ct
	}
	req, err := e.newRequest(ctx, t, t.Method, body)
	if err != nil {
		return false, err
	}
	req.ContentLength = length
	if length == 0 {
		req.Body = http.NoBody
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := e.do(t, req)
	if err != nil {
		return true, err
	}
	drainAndClose(resp)
	if resp.StatusCode/100 == 2 {
		return false, nil
	}
	retry := resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests
	return retry, fmt.Errorf("target answered HTTP %d", resp.StatusCode)
}

// whatwgEscape encodes a multipart name the way the WHATWG form encoder does.
func whatwgEscape(s string) string {
	return strings.NewReplacer(`"`, "%22", "\r", "%0D", "\n", "%0A").Replace(s)
}

// multipartFrame returns the bytes before and after the file's content in a
// multipart/form-data body, and its Content-Type.
func multipartFrame(form *uploadForm, f *pendingFile) ([]byte, []byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf) // 30 random bytes: 240 bits of boundary randomness
	for _, fl := range form.Fields {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="`+whatwgEscape(fl.Name)+`"`)
		pw, err := w.CreatePart(h)
		if err != nil {
			return nil, nil, "", err
		}
		if _, err := io.WriteString(pw, fl.Value); err != nil {
			return nil, nil, "", err
		}
	}
	fname := f.name
	if i := strings.LastIndexAny(fname, `/\`); i >= 0 {
		fname = fname[i+1:]
	}
	if fname == "" || fname == "." || fname == ".." {
		fname = "file"
	}
	ct := "application/octet-stream"
	if _, _, err := mime.ParseMediaType(f.mimeType); err == nil {
		ct = f.mimeType
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="`+whatwgEscape(form.FileField)+`"; filename="`+whatwgEscape(fname)+`"`)
	h.Set("Content-Type", ct)
	if _, err := w.CreatePart(h); err != nil {
		return nil, nil, "", err
	}
	pre := append([]byte(nil), buf.Bytes()...)
	buf.Reset()
	if err := w.Close(); err != nil {
		return nil, nil, "", err
	}
	return pre, buf.Bytes(), w.FormDataContentType(), nil
}

// transferDown fetches t into the download directory and returns its path.
func (e *filesEnv) transferDown(ctx context.Context, t *target) (string, error) {
	dir, err := e.dir()
	if err != nil {
		return "", err
	}
	req, err := e.newRequest(ctx, t, http.MethodGet, nil)
	if err != nil {
		return "", err
	}
	resp, err := e.do(t, req)
	if err != nil {
		return "", err
	}
	defer drainAndClose(resp)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("target answered HTTP %d", resp.StatusCode)
	}
	name := "download"
	if t.Name != "" {
		name = safeBaseName(t.Name)
	}
	out, err := createUnique(dir, name)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, cerr := io.Copy(io.MultiWriter(out, h), io.LimitReader(resp.Body, t.Size+1))
	if err := out.Close(); cerr == nil {
		cerr = err
	}
	fail := func(err error) (string, error) {
		_ = os.Remove(out.Name())
		return "", err
	}
	if cerr != nil {
		return fail(scrubErr(cerr))
	}
	if n != t.Size {
		return fail(fmt.Errorf("received %d bytes, expected %d; discarded", n, t.Size))
	}
	if t.Digest != nil && base64.RawURLEncoding.EncodeToString(h.Sum(nil)) != strings.TrimRight(t.Digest.Value, "=") {
		return fail(errors.New("digest mismatch; discarded"))
	}
	return filepath.Abs(out.Name())
}
