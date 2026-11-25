package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	firlog "github.com/kfet/fir/pkg/log"
)

// deviceUserCodeTimeout bounds the one usercode request made before the
// auth URL is shown, so a slow device endpoint cannot delay the browser link.
const deviceUserCodeTimeout = 10 * time.Second

// minDeviceInterval floors the server's poll interval (a var so tests can
// poll fast).
var minDeviceInterval = time.Second

// deviceSession is an issued device code awaiting user approval.
type deviceSession struct {
	authID   string
	userCode string
	interval time.Duration
}

// deviceGrant is the authorization code a device login yields.
type deviceGrant struct {
	code     string
	verifier string
}

// devicePost POSTs a JSON body with the spec's headers and returns the
// status code and decoded JSON object (empty on a non-JSON body).
func devicePost(ctx context.Context, dc *DeviceCodeSpec, url string, body any) (int, map[string]any, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range dc.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out, nil
}

// requestDeviceCode asks for a user code once. It returns nil (and logs)
// on any failure — 404 (device login not enabled for this server), 5xx,
// a CDN 530 — so the caller silently omits the device section.
func requestDeviceCode(ctx context.Context, dc *DeviceCodeSpec, clientID string) *deviceSession {
	ctx, cancel := context.WithTimeout(ctx, deviceUserCodeTimeout)
	defer cancel()
	status, data, err := devicePost(ctx, dc, dc.UserCodeURL, map[string]string{"client_id": clientID})
	if err != nil || status != http.StatusOK {
		firlog.Info("oauth device code unavailable", "status", status, "error", err)
		return nil
	}
	authID, _ := data["device_auth_id"].(string)
	userCode, _ := data["user_code"].(string)
	if authID == "" || userCode == "" {
		firlog.Info("oauth device code: malformed usercode response")
		return nil
	}
	return &deviceSession{authID: authID, userCode: userCode, interval: parseInterval(data["interval"])}
}

// parseInterval reads a poll interval given as a number or numeric string,
// defaulting to 5s and never below minDeviceInterval.
func parseInterval(v any) time.Duration {
	secs := 5.0
	switch x := v.(type) {
	case float64:
		secs = x
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			secs = f
		}
	}
	d := time.Duration(secs * float64(time.Second))
	if d < minDeviceInterval {
		d = minDeviceInterval
	}
	return d
}

// pollDevice polls until the user approves, ctx ends, or the wait cap
// passes. 403/404 mean "pending"; 429 and 5xx (incl. a CDN 530) are
// treated as transient; any other non-200 ends this source.
func pollDevice(ctx context.Context, dc *DeviceCodeSpec, s *deviceSession) (*deviceGrant, error) {
	maxWait := time.Duration(dc.MaxWaitSeconds) * time.Second
	if maxWait <= 0 {
		maxWait = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, maxWait)
	defer cancel()
	body := map[string]string{"device_auth_id": s.authID, "user_code": s.userCode}
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("device code login: %w", ctx.Err())
		case <-t.C:
		}
		status, data, err := devicePost(ctx, dc, dc.PollURL, body)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("device code login: %w", ctx.Err())
			}
			continue // transient network error: keep polling
		}
		switch status {
		case http.StatusOK:
			code, _ := data["authorization_code"].(string)
			verifier, _ := data["code_verifier"].(string)
			if code == "" {
				return nil, fmt.Errorf("device code login: response missing authorization_code")
			}
			return &deviceGrant{code: code, verifier: verifier}, nil
		case http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests:
			continue
		default:
			if status >= 500 {
				continue
			}
			return nil, fmt.Errorf("device code login: HTTP %d", status)
		}
	}
}

// deviceInstructions appends the device-code section after the browser
// instructions, so the browser link stays first.
func deviceInstructions(base string, dc *DeviceCodeSpec, s *deviceSession) string {
	var b strings.Builder
	b.WriteString(base)
	if base != "" {
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "Or, on any device, open %s and enter code: %s", dc.VerificationURL, s.userCode)
	if dc.Hint != "" {
		b.WriteString("\n")
		b.WriteString(dc.Hint)
	}
	return b.String()
}
