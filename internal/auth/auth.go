// Package auth implements ike's native Datadog OAuth2 login: dynamic client
// registration, the PKCE authorization-code flow through the user's browser,
// token refresh, and a lazy-refreshing token source for the live provider.
// Endpoints are injectable so the whole flow is testable against fakes; the
// real endpoints were proven by hack/oauth-spike.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// CallbackAddr is the loopback address the browser is redirected back to.
	CallbackAddr = "127.0.0.1:53682"
	callbackPath = "/oauth/callback"
	// refreshMargin refreshes an access token this close to its expiry, so a
	// request never goes out with a token about to die mid-flight.
	refreshMargin = 60 * time.Second
	// loginTimeout bounds how long we wait for the user to finish the browser
	// login before giving up.
	loginTimeout = 3 * time.Minute
)

// Endpoints are the OAuth endpoints for one org. API is the api host base
// (token + registration); App is the browser-facing base (authorize), which
// differs for orgs with a custom subdomain.
type Endpoints struct {
	API string // e.g. https://api.datadoghq.eu
	App string // e.g. https://app.datadoghq.eu or the org's subdomain host
}

// EndpointsFor derives the endpoints from a site and an optional browser
// subdomain ("" = the default app host).
func EndpointsFor(site, subdomain string) Endpoints {
	app := "https://app." + site
	if subdomain != "" {
		app = "https://" + subdomain + "." + site
	}
	return Endpoints{API: "https://api." + site, App: app}
}

// TokenSet is one org's OAuth tokens. Expiry is absolute.
type TokenSet struct {
	Access  string    `json:"access"`
	Refresh string    `json:"refresh"`
	Expiry  time.Time `json:"expiry"`
}

// Credentials is everything persisted per context in the OS keychain.
type Credentials struct {
	ClientID string `json:"client_id"`
	TokenSet
}

// Register performs dynamic client registration and returns the client id.
// Unauthenticated by design (public client).
func Register(ctx context.Context, api string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"client_name":   "ike",
		"redirect_uris": []string{"http://" + CallbackAddr + callbackPath},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		api+"/api/v2/oauth2/register", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("client registration: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("client registration: HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	var reg struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(raw, &reg); err != nil || reg.ClientID == "" {
		return "", fmt.Errorf("client registration: no client_id in response")
	}
	return reg.ClientID, nil
}

// Login runs the PKCE authorization-code flow: it starts the loopback
// callback server, sends the user's browser to the authorize page via
// openBrowser (injectable for tests), and exchanges the returned code for
// tokens. It never stores anything.
func Login(ctx context.Context, ep Endpoints, clientID string, openBrowser func(url string) error) (TokenSet, error) {
	verifier := randomURLSafe(64)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := randomURLSafe(24)
	redirect := "http://" + CallbackAddr + callbackPath

	ln, err := net.Listen("tcp", CallbackAddr)
	if err != nil {
		return TokenSet{}, fmt.Errorf("callback listener: %w (is another login running?)", err)
	}
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			errCh <- errors.New("authorize callback: state mismatch")
			return
		}
		if e := q.Get("error"); e != "" {
			fmt.Fprintln(w, "ike: authorization was denied, you can close this tab.")
			errCh <- fmt.Errorf("authorization denied: %s", e)
			return
		}
		fmt.Fprintln(w, "ike: signed in, you can close this tab.")
		codeCh <- q.Get("code")
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	authorize := ep.App + "/oauth2/v1/authorize?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirect},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}.Encode()
	if err := openBrowser(authorize); err != nil {
		return TokenSet{}, fmt.Errorf("open browser: %w", err)
	}

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return TokenSet{}, err
	case <-time.After(loginTimeout):
		return TokenSet{}, errors.New("timed out waiting for the browser login")
	case <-ctx.Done():
		return TokenSet{}, ctx.Err()
	}
	if code == "" {
		return TokenSet{}, errors.New("authorize callback carried no code")
	}
	return exchange(ctx, ep.API, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {redirect},
		"code_verifier": {verifier},
	})
}

// Refresh trades a refresh token for a fresh token set.
func Refresh(ctx context.Context, api, clientID, refreshToken string) (TokenSet, error) {
	return exchange(ctx, api, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
	})
}

func exchange(ctx context.Context, api string, form url.Values) (TokenSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		api+"/oauth2/v1/token", strings.NewReader(form.Encode()))
	if err != nil {
		return TokenSet{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return TokenSet{}, fmt.Errorf("token endpoint: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return TokenSet{}, fmt.Errorf("token endpoint: HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	var t struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return TokenSet{}, fmt.Errorf("token endpoint: %w", err)
	}
	if t.AccessToken == "" {
		return TokenSet{}, errors.New("token endpoint: no access_token")
	}
	return TokenSet{
		Access:  t.AccessToken,
		Refresh: t.RefreshToken,
		Expiry:  time.Now().Add(time.Duration(t.ExpiresIn) * time.Second),
	}, nil
}

// Source supplies the current access token, refreshing lazily when it is
// about to expire and persisting the refreshed set through save. Safe for
// concurrent use; refreshes are single-flight.
type Source struct {
	api      string
	clientID string
	save     func(Credentials) error

	mu  sync.Mutex
	tok TokenSet
}

// NewSource builds a token source from stored credentials. save persists a
// refreshed set (nil = don't persist).
func NewSource(api string, creds Credentials, save func(Credentials) error) *Source {
	return &Source{api: api, clientID: creds.ClientID, save: save, tok: creds.TokenSet}
}

// Token returns a currently-valid access token, refreshing first if needed.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Until(s.tok.Expiry) > refreshMargin {
		return s.tok.Access, nil
	}
	if s.tok.Refresh == "" {
		return "", errors.New("access token expired and no refresh token stored — run: ike auth login")
	}
	fresh, err := Refresh(ctx, s.api, s.clientID, s.tok.Refresh)
	if err != nil {
		return "", fmt.Errorf("token refresh failed (run: ike auth login): %w", err)
	}
	if fresh.Refresh == "" {
		fresh.Refresh = s.tok.Refresh // server didn't rotate it; keep the old one
	}
	s.tok = fresh
	if s.save != nil {
		if err := s.save(Credentials{ClientID: s.clientID, TokenSet: fresh}); err != nil {
			// Persisting is best-effort: the session keeps working either way.
			return fresh.Access, nil
		}
	}
	return fresh.Access, nil
}

func randomURLSafe(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:197] + "…"
	}
	return s
}
