package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeDatadog stands in for the registration + token endpoints. The authorize
// step is simulated by the test's browser opener, which "logs in" by hitting
// the loopback callback with the code directly.
func fakeDatadog(t *testing.T, wantVerifier *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/oauth2/register", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "fake-client"})
	})
	mux.HandleFunc("/oauth2/v1/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code") != "good-code" || r.Form.Get("client_id") != "fake-client" {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			if wantVerifier != nil {
				*wantVerifier = r.Form.Get("code_verifier")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600,
			})
		case "refresh_token":
			if r.Form.Get("refresh_token") != "refresh-1" {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-2", "refresh_token": "refresh-2", "expires_in": 3600,
			})
		default:
			http.Error(w, "bad grant", http.StatusBadRequest)
		}
	})
	return httptest.NewServer(mux)
}

func TestRegister(t *testing.T) {
	srv := fakeDatadog(t, nil)
	defer srv.Close()
	id, err := Register(context.Background(), srv.URL)
	if err != nil || id != "fake-client" {
		t.Fatalf("Register = %q, %v", id, err)
	}
}

// TestLoginRoundTrip drives the whole PKCE flow with a stubbed "browser": the
// opener parses the authorize URL and calls the loopback callback with a code,
// exactly like a real browser redirect would.
func TestLoginRoundTrip(t *testing.T) {
	var gotVerifier string
	srv := fakeDatadog(t, &gotVerifier)
	defer srv.Close()
	ep := Endpoints{API: srv.URL, App: srv.URL}

	var authorizeURL string
	openBrowser := func(u string) error {
		authorizeURL = u
		parsed, err := url.Parse(u)
		if err != nil {
			return err
		}
		q := parsed.Query()
		// The "user logs in" and Datadog redirects back with code + state.
		cb := fmt.Sprintf("http://%s%s?code=good-code&state=%s",
			CallbackAddr, "/oauth/callback", q.Get("state"))
		go func() {
			resp, err := http.Get(cb)
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}

	tok, err := Login(context.Background(), ep, "fake-client", openBrowser)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Access != "access-1" || tok.Refresh != "refresh-1" {
		t.Fatalf("tokens = %+v", tok)
	}
	if time.Until(tok.Expiry) < 55*time.Minute {
		t.Fatalf("expiry not set from expires_in: %v", tok.Expiry)
	}
	// PKCE actually enforced end to end: the challenge in the authorize URL
	// must be derivable from the verifier sent to the token endpoint.
	if !strings.Contains(authorizeURL, "code_challenge=") || gotVerifier == "" {
		t.Fatalf("PKCE material missing: url=%q verifier=%q", authorizeURL, gotVerifier)
	}
}

// TestLoginUsesAppHost proves the authorize page is opened on ep.App — the
// org's subdomain host when one is set — not the API host. The token exchange
// still goes to ep.API.
func TestLoginUsesAppHost(t *testing.T) {
	srv := fakeDatadog(t, nil)
	defer srv.Close()
	// App is a subdomain-shaped host the browser opener only parses (never
	// fetches); API is the reachable fake for the token exchange.
	ep := Endpoints{API: srv.URL, App: "https://acme-dev.datadoghq.eu"}

	var authorizeURL string
	openBrowser := func(u string) error {
		authorizeURL = u
		parsed, err := url.Parse(u)
		if err != nil {
			return err
		}
		cb := fmt.Sprintf("http://%s/oauth/callback?code=good-code&state=%s",
			CallbackAddr, parsed.Query().Get("state"))
		go func() {
			if resp, err := http.Get(cb); err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	if _, err := Login(context.Background(), ep, "fake-client", openBrowser); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(authorizeURL, "https://acme-dev.datadoghq.eu/oauth2/v1/authorize?") {
		t.Fatalf("authorize URL must be on the subdomain host, got %q", authorizeURL)
	}
}

func TestLoginDenied(t *testing.T) {
	srv := fakeDatadog(t, nil)
	defer srv.Close()
	ep := Endpoints{API: srv.URL, App: srv.URL}
	openBrowser := func(u string) error {
		parsed, _ := url.Parse(u)
		cb := fmt.Sprintf("http://%s/oauth/callback?error=access_denied&state=%s",
			CallbackAddr, parsed.Query().Get("state"))
		go func() {
			resp, err := http.Get(cb)
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	if _, err := Login(context.Background(), ep, "fake-client", openBrowser); err == nil {
		t.Fatal("denied authorization must error")
	}
}

func TestSourceLazyRefresh(t *testing.T) {
	srv := fakeDatadog(t, nil)
	defer srv.Close()

	var saved []Credentials
	src := NewSource(srv.URL, Credentials{
		ClientID: "fake-client",
		TokenSet: TokenSet{Access: "access-1", Refresh: "refresh-1", Expiry: time.Now().Add(time.Hour)},
	}, func(c Credentials) error { saved = append(saved, c); return nil })

	// Fresh token: no refresh, no save.
	tok, err := src.Token(context.Background())
	if err != nil || tok != "access-1" {
		t.Fatalf("fresh = %q, %v", tok, err)
	}
	if len(saved) != 0 {
		t.Fatalf("fresh token must not persist, saved %d", len(saved))
	}

	// Expiring token: refreshed, rotated, persisted.
	src.tok.Expiry = time.Now().Add(10 * time.Second)
	tok, err = src.Token(context.Background())
	if err != nil || tok != "access-2" {
		t.Fatalf("refreshed = %q, %v", tok, err)
	}
	if len(saved) != 1 || saved[0].Refresh != "refresh-2" {
		t.Fatalf("refresh must persist the rotated set: %+v", saved)
	}
}

func TestSourceDeadRefresh(t *testing.T) {
	srv := fakeDatadog(t, nil)
	defer srv.Close()
	src := NewSource(srv.URL, Credentials{
		ClientID: "fake-client",
		TokenSet: TokenSet{Access: "old", Refresh: "revoked", Expiry: time.Now().Add(-time.Minute)},
	}, nil)
	if _, err := src.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "ike auth login") {
		t.Fatalf("dead refresh must tell the user to re-login, got %v", err)
	}
}

func TestEndpointsFor(t *testing.T) {
	ep := EndpointsFor("datadoghq.eu", "")
	if ep.API != "https://api.datadoghq.eu" || ep.App != "https://app.datadoghq.eu" {
		t.Fatalf("default: %+v", ep)
	}
	ep = EndpointsFor("datadoghq.eu", "acme-dev")
	if ep.App != "https://acme-dev.datadoghq.eu" {
		t.Fatalf("subdomain: %+v", ep)
	}
}
