package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	anthropicAuthorizeURL = "https://claude.ai/oauth/authorize"
	anthropicScopes       = "user:profile user:inference"
	callbackHost          = "127.0.0.1"
	callbackPort          = 53692
	callbackPath          = "/callback"

	// manualRedirectURI is Anthropic's hosted code-display page: the browser
	// lands there after consent and shows a code#state string for the user to
	// paste back. It's how a login can finish when the daemon runs on a remote
	// host where a localhost callback can never be reached.
	manualRedirectURI = "https://platform.claude.com/oauth/code/callback"
)

func redirectURI() string {
	return fmt.Sprintf("http://localhost:%d%s", callbackPort, callbackPath)
}

// AnthropicAuthURL builds a fresh PKCE-backed authorize URL and returns it
// alongside the verifier needed to complete the exchange. Exposed so a CLI can
// offer a "print the URL" mode without running the full interactive flow.
func AnthropicAuthURL() (authURL, verifier string, err error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return "", "", err
	}
	q := url.Values{}
	q.Set("code", "true")
	q.Set("client_id", anthropicClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI())
	q.Set("scope", anthropicScopes)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", verifier)
	return anthropicAuthorizeURL + "?" + q.Encode(), verifier, nil
}

// AnthropicManualAuthURL builds a PKCE authorize URL whose redirect lands on
// Anthropic's hosted code page instead of a localhost callback. The user pastes
// the displayed code back; complete with ExchangeManualCode.
//
// Unlike the CLI's localhost flow, state here is NOT the verifier: the state
// travels through the browser (URL, history, the pasted code#state string)
// while the verifier must stay server-side for PKCE to mean anything.
func AnthropicManualAuthURL() (authURL, verifier, state string, err error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return "", "", "", err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", err
	}
	state = base64.RawURLEncoding.EncodeToString(buf)
	q := url.Values{}
	q.Set("code", "true")
	q.Set("client_id", anthropicClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", manualRedirectURI)
	q.Set("scope", anthropicScopes)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	return anthropicAuthorizeURL + "?" + q.Encode(), verifier, state, nil
}

// ExchangeManualCode finishes a manual (paste-code) login. The pasted string
// may be "code#state" — the fragment is stripped; the caller has already
// matched state to this verifier.
func ExchangeManualCode(ctx context.Context, code, verifier, state string) (*Credentials, error) {
	if i := strings.IndexByte(code, '#'); i >= 0 {
		code = code[:i]
	}
	return exchangeCodeManual(ctx, code, verifier, state)
}

// AnthropicLogin runs the full Claude OAuth login: PKCE, a local callback
// server on 127.0.0.1:53692, browser authorize, then code exchange. On success
// it saves credentials under providerKey and returns them. onURL receives the
// authorize URL (e.g. to print it in case the browser didn't open).
func AnthropicLogin(ctx context.Context, store *Store, providerKey string, onURL func(string)) (*Credentials, error) {
	authURL, verifier, err := AnthropicAuthURL()
	if err != nil {
		return nil, fmt.Errorf("build auth url: %w", err)
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	server, err := startCallbackServer(verifier, codeCh, errCh)
	if err != nil {
		return nil, fmt.Errorf("start callback server: %w", err)
	}
	defer server.Close()

	if onURL != nil {
		onURL(authURL)
	}
	openBrowser(authURL)

	var code string
	select {
	case code = <-codeCh:
	case e := <-errCh:
		return nil, e
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	creds, err := exchangeCode(ctx, code, verifier)
	if err != nil {
		return nil, err
	}
	if err := store.Save(providerKey, creds); err != nil {
		return nil, fmt.Errorf("save credentials: %w", err)
	}
	return creds, nil
}

func exchangeCode(ctx context.Context, code, verifier string) (*Credentials, error) {
	// The CLI's localhost flow uses the verifier as state, unchanged.
	return exchangeCodeVia(ctx, code, verifier, verifier, redirectURI())
}

func exchangeCodeManual(ctx context.Context, code, verifier, state string) (*Credentials, error) {
	return exchangeCodeVia(ctx, code, verifier, state, manualRedirectURI)
}

func exchangeCodeVia(ctx context.Context, code, verifier, state, redirect string) (*Credentials, error) {
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     anthropicClientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  redirect,
		"code_verifier": verifier,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicTokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("token exchange parse: %w", err)
	}
	return &Credentials{
		Access:  out.AccessToken,
		Refresh: out.RefreshToken,
		Expires: time.Now().UnixMilli() + out.ExpiresIn*1000,
	}, nil
}

func generatePKCE() (verifier, challenge string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func startCallbackServer(expectedState string, codeCh chan<- string, errCh chan<- error) (*http.Server, error) {
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if e := q.Get("error"); e != "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<h2>Login failed: %s</h2>", htmlEscape(e))
			return
		}
		code, state := q.Get("code"), q.Get("state")
		if code == "" || state != expectedState {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "<h2>Login failed: missing code or state mismatch.</h2>")
			return
		}
		fmt.Fprint(w, "<h2>Logged in — you can close this tab.</h2>")
		codeCh <- code
	})

	addr := fmt.Sprintf("%s:%d", callbackHost, callbackPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	server := &http.Server{Handler: mux}
	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	return server, nil
}

func openBrowser(target string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", target).Start()
	case "linux":
		_ = exec.Command("xdg-open", target).Start()
	case "windows":
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
	}
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;").Replace(s)
}
