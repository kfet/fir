// Ported from: packages/ai/src/utils/oauth/openai-codex.ts
// Upstream hash: 7b3c9f1a
package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

const (
	openAICodexClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	openAICodexAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	openAICodexDefaultTokenURL = "https://auth.openai.com/oauth/token"
	openAICodexRedirectURI = "http://localhost:1455/auth/callback"
	openAICodexScope       = "openid profile email offline_access"
	// JWTClaimPath is the JWT claim key for OpenAI auth data.
	JWTClaimPath           = "https://api.openai.com/auth"
)

var openAICodexTokenURL = openAICodexDefaultTokenURL

// setOpenAICodexTokenURL overrides the token URL (for testing).
func setOpenAICodexTokenURL(u string) { openAICodexTokenURL = u }

// OpenAICodexProvider implements OpenAI Codex (ChatGPT) OAuth.
type OpenAICodexProvider struct{}

func (p *OpenAICodexProvider) ID() string              { return "openai-codex" }
func (p *OpenAICodexProvider) Name() string             { return "ChatGPT Plus/Pro (Codex Subscription)" }
func (p *OpenAICodexProvider) UsesCallbackServer() bool { return true }

func (p *OpenAICodexProvider) Login(callbacks LoginCallbacks) (*Credentials, error) {
	return loginOpenAICodex(callbacks)
}

func (p *OpenAICodexProvider) RefreshToken(creds *Credentials) (*Credentials, error) {
	return refreshOpenAICodexToken(creds.Refresh)
}

func (p *OpenAICodexProvider) GetAPIKey(creds *Credentials) string {
	return creds.Access
}

func (p *OpenAICodexProvider) ModifyModels(models []*ai.Model, _ *Credentials) []*ai.Model {
	return models
}

// createOAuthState generates a random hex state string.
func createOAuthState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// parseAuthorizationInput extracts code and state from various input formats:
// full redirect URL, "code#state", "code=x&state=y" query, or bare code.
func parseAuthorizationInput(input string) (code, state string) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", ""
	}

	// Strip shell-escape backslashes (common when pasting from terminal output).
	value = strings.ReplaceAll(value, "\\", "")

	// Try URL
	if u, err := url.Parse(value); err == nil && u.Scheme != "" {
		return u.Query().Get("code"), u.Query().Get("state")
	}

	// Try code#state
	if strings.Contains(value, "#") {
		parts := strings.SplitN(value, "#", 2)
		return parts[0], parts[1]
	}

	// Try query-string format
	if strings.Contains(value, "code=") {
		params, _ := url.ParseQuery(value)
		return params.Get("code"), params.Get("state")
	}

	// Bare code
	return value, ""
}

// decodeJWTPayload decodes the payload portion of a JWT (without verification).
func decodeJWTPayload(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT: expected 3 parts, got %d", len(parts))
	}

	// JWTs use the base64url alphabet with no padding (RFC 4648 §5).
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decoding JWT payload: %w", err)
	}

	var result map[string]any
	if err := json.Unmarshal(decoded, &result); err != nil {
		return nil, fmt.Errorf("parsing JWT payload: %w", err)
	}
	return result, nil
}

// getAccountID extracts the chatgpt_account_id from a JWT access token.
func getAccountID(accessToken string) string {
	payload, err := decodeJWTPayload(accessToken)
	if err != nil {
		return ""
	}

	authClaim, ok := payload[JWTClaimPath].(map[string]any)
	if !ok {
		return ""
	}

	accountID, ok := authClaim["chatgpt_account_id"].(string)
	if !ok || accountID == "" {
		return ""
	}
	return accountID
}

// startOpenAICallbackServer starts a local HTTP server on port 1455 for the OAuth callback.
func startOpenAICallbackServer(ctx context.Context, expectedState string) (result <-chan string, closeServer func(), err error) {
	ch := make(chan string, 1)
	var once sync.Once

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != expectedState {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "State mismatch")
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "Missing authorization code")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `<!doctype html><html><body><p>Authentication successful. Return to your terminal to continue.</p></body></html>`)
		once.Do(func() { ch <- code })
	})

	listener, err := net.Listen("tcp", "127.0.0.1:1455")
	if err != nil {
		return nil, nil, fmt.Errorf("starting callback server: %w", err)
	}

	srv := &http.Server{Handler: mux}
	go srv.Serve(listener)

	go func() {
		<-ctx.Done()
		once.Do(func() { close(ch) })
		srv.Close()
	}()

	return ch, func() { srv.Close() }, nil
}

// loginOpenAICodex runs the full OpenAI Codex OAuth flow.
func loginOpenAICodex(callbacks LoginCallbacks) (*Credentials, error) {
	ctx := callbacks.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	if callbacks.OnPrompt == nil {
		return nil, fmt.Errorf("OpenAI Codex login requires OnPrompt callback")
	}

	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("generating PKCE: %w", err)
	}

	state, err := createOAuthState()
	if err != nil {
		return nil, fmt.Errorf("creating state: %w", err)
	}

	progress := func(msg string) {
		if callbacks.OnProgress != nil {
			callbacks.OnProgress(msg)
		}
	}

	// Start local callback server
	callbackCtx, cancelCallback := context.WithCancel(ctx)
	defer cancelCallback()

	codeCh, closeServer, err := startOpenAICallbackServer(callbackCtx, state)
	if err != nil {
		// Server failed — fall back to manual paste only
		codeCh = nil
		closeServer = func() {}
	}
	defer closeServer()

	// Build authorization URL
	params := url.Values{
		"response_type":        {"code"},
		"client_id":            {openAICodexClientID},
		"redirect_uri":         {openAICodexRedirectURI},
		"scope":                {openAICodexScope},
		"code_challenge":       {pkce.Challenge},
		"code_challenge_method": {"S256"},
		"state":                {state},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"originator":           {"fir"},
	}
	authURL := openAICodexAuthorizeURL + "?" + params.Encode()

	if callbacks.OnAuth != nil {
		callbacks.OnAuth(AuthInfo{
			URL:          authURL,
			Instructions: "A browser window should open. Complete login to finish.",
		})
	}

	var code string

	if callbacks.OnManualCodeInput != nil && codeCh != nil {
		// Race: browser callback vs manual input
		type manualResult struct {
			input string
			err   error
		}
		manualCh := make(chan manualResult, 1)
		go func() {
			input, err := callbacks.OnManualCodeInput()
			manualCh <- manualResult{input, err}
			cancelCallback()
		}()

		select {
		case c := <-codeCh:
			if c != "" {
				code = c
			}
		case mr := <-manualCh:
			if mr.err != nil {
				return nil, mr.err
			}
			c, s := parseAuthorizationInput(mr.input)
			if s != "" && s != state {
				return nil, fmt.Errorf("state mismatch")
			}
			code = c
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	} else if codeCh != nil {
		// Wait for callback with timeout
		select {
		case c := <-codeCh:
			if c != "" {
				code = c
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// Fallback: prompt user for manual code paste
	if code == "" {
		input, err := callbacks.OnPrompt(Prompt{
			Message: "Paste the authorization code (or full redirect URL):",
		})
		if err != nil {
			return nil, err
		}
		c, s := parseAuthorizationInput(input)
		if s != "" && s != state {
			return nil, fmt.Errorf("state mismatch")
		}
		code = c
	}

	if code == "" {
		return nil, fmt.Errorf("missing authorization code")
	}

	// Exchange code for tokens
	progress("Exchanging authorization code for tokens...")
	creds, err := exchangeOpenAICodexCode(code, pkce.Verifier)
	if err != nil {
		return nil, err
	}

	// Extract account ID from JWT
	accountID := getAccountID(creds.Access)
	if accountID == "" {
		return nil, fmt.Errorf("failed to extract accountId from token")
	}

	if creds.Extra == nil {
		creds.Extra = make(map[string]any)
	}
	creds.Extra["accountId"] = accountID

	return creds, nil
}

// exchangeOpenAICodexCode exchanges an authorization code for tokens.
func exchangeOpenAICodexCode(code, verifier string) (*Credentials, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {openAICodexClientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {openAICodexRedirectURI},
	}

	resp, err := oauthHTTPClient.PostForm(openAICodexTokenURL, data)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, string(body))
	}

	var tokenData struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    *int64 `json:"expires_in"` // Pointer to distinguish missing from zero
	}
	if err := json.Unmarshal(body, &tokenData); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	if tokenData.AccessToken == "" || tokenData.RefreshToken == "" || tokenData.ExpiresIn == nil {
		return nil, fmt.Errorf("token response missing required fields")
	}

	expiresAt := time.Now().UnixMilli() + *tokenData.ExpiresIn*1000

	return &Credentials{
		Access:  tokenData.AccessToken,
		Refresh: tokenData.RefreshToken,
		Expires: expiresAt,
	}, nil
}

// refreshOpenAICodexToken refreshes an expired OpenAI Codex token.
func refreshOpenAICodexToken(refreshToken string) (*Credentials, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {openAICodexClientID},
	}

	resp, err := oauthHTTPClient.PostForm(openAICodexTokenURL, data)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenAI Codex token refresh failed (%d): %s", resp.StatusCode, string(body))
	}

	var tokenData struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    *int64 `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenData); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	if tokenData.AccessToken == "" || tokenData.RefreshToken == "" || tokenData.ExpiresIn == nil {
		return nil, fmt.Errorf("token refresh response missing required fields")
	}

	expiresAt := time.Now().UnixMilli() + *tokenData.ExpiresIn*1000

	accountID := getAccountID(tokenData.AccessToken)
	if accountID == "" {
		return nil, fmt.Errorf("failed to extract accountId from refreshed token")
	}

	return &Credentials{
		Access:  tokenData.AccessToken,
		Refresh: tokenData.RefreshToken,
		Expires: expiresAt,
		Extra:   map[string]any{"accountId": accountID},
	}, nil
}
