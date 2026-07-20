// Command oauth-spike proves the full ike auth login flow end to end against
// a real Datadog org: dynamic client registration, browser authorization with
// PKCE, the local callback, the token exchange, an authenticated API call,
// and a token refresh. Throwaway: it stores nothing.
//
// Run:
//
//	go run ./hack/oauth-spike --site datadoghq.eu
//	go run ./hack/oauth-spike --site datadoghq.eu --app-host acme-dev.datadoghq.eu
//
// --app-host is the browser-facing host for orgs with a custom subdomain
// (defaults to app.<site>).
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	callbackAddr = "127.0.0.1:53682"
	callbackPath = "/oauth/callback"
)

func main() {
	site := flag.String("site", "datadoghq.eu", "Datadog site (API host suffix)")
	appHost := flag.String("app-host", "", "browser-facing host (default app.<site>; set your org's subdomain host if it has one)")
	flag.Parse()
	if *appHost == "" {
		*appHost = "app." + *site
	}
	api := "https://api." + *site

	step := func(n int, what string) { fmt.Printf("\n[%d] %s\n", n, what) }

	// 1. Dynamic client registration (no credentials).
	step(1, "register a client at "+api+"/api/v2/oauth2/register")
	regBody, _ := json.Marshal(map[string]any{
		"client_name":   "ike-oauth-spike",
		"redirect_uris": []string{"http://" + callbackAddr + callbackPath},
	})
	resp, err := http.Post(api+"/api/v2/oauth2/register", "application/json", strings.NewReader(string(regBody)))
	must(err)
	reg := struct {
		ClientID string `json:"client_id"`
	}{}
	decodeInto(resp, &reg)
	if reg.ClientID == "" {
		fail("registration returned no client_id")
	}
	fmt.Println("    PASS: client_id =", reg.ClientID)

	// 2. PKCE material.
	step(2, "generate PKCE verifier/challenge")
	verifier := randomURLSafe(64)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := randomURLSafe(24)
	fmt.Println("    PASS")

	// 3. Local callback server + browser.
	step(3, "open the browser and wait for the callback on "+callbackAddr)
	codeCh := make(chan string, 1)
	srv := &http.Server{Addr: callbackAddr}
	http.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		fmt.Fprintln(w, "ike oauth spike: login received, you can close this tab.")
		codeCh <- r.URL.Query().Get("code")
	})
	go func() { _ = srv.ListenAndServe() }()

	authorize := "https://" + *appHost + "/oauth2/v1/authorize?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {reg.ClientID},
		"redirect_uri":          {"http://" + callbackAddr + callbackPath},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}.Encode()
	fmt.Println("    opening:", authorize)
	_ = exec.Command("open", authorize).Start() // macOS; print the URL anyway for manual open

	var code string
	select {
	case code = <-codeCh:
	case <-time.After(3 * time.Minute):
		fail("timed out waiting for the browser callback (3m)")
	}
	if code == "" {
		fail("callback carried no code (authorization denied?)")
	}
	fmt.Println("    PASS: authorization code received")

	// 4. Token exchange.
	step(4, "exchange the code for tokens at "+api+"/oauth2/v1/token")
	tok := exchange(api, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {reg.ClientID},
		"code":          {code},
		"redirect_uri":  {"http://" + callbackAddr + callbackPath},
		"code_verifier": {verifier},
	})
	fmt.Printf("    PASS: access token (%ds lifetime), refresh token present: %v\n", tok.ExpiresIn, tok.RefreshToken != "")

	// 5. Prove the token works.
	step(5, "call GET /api/v2/current_user with the access token")
	req, _ := http.NewRequest("GET", api+"/api/v2/current_user", nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	uresp, err := http.DefaultClient.Do(req)
	must(err)
	ubody, _ := io.ReadAll(uresp.Body)
	if uresp.StatusCode != 200 {
		fmt.Println("    body:", truncate(string(ubody), 400))
		fail(fmt.Sprintf("current_user returned HTTP %d — token not usable for the API (scopes?)", uresp.StatusCode))
	}
	fmt.Println("    PASS: authenticated as", extractHandle(ubody))

	// 6. Prove refresh works.
	step(6, "refresh the access token")
	if tok.RefreshToken == "" {
		fail("no refresh token issued — hourly re-login would remain")
	}
	tok2 := exchange(api, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {reg.ClientID},
		"refresh_token": {tok.RefreshToken},
	})
	fmt.Printf("    PASS: refreshed (new access token, %ds lifetime)\n", tok2.ExpiresIn)

	fmt.Println("\nALL STEPS PASSED — ike auth login is fully buildable.")
}

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

func exchange(api string, form url.Values) tokenResp {
	resp, err := http.Post(api+"/oauth2/v1/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	must(err)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		fmt.Println("    body:", truncate(string(body), 400))
		fail(fmt.Sprintf("token endpoint returned HTTP %d", resp.StatusCode))
	}
	var t tokenResp
	must(json.Unmarshal(body, &t))
	if t.AccessToken == "" {
		fail("token response carried no access_token")
	}
	return t
}

func decodeInto(resp *http.Response, v any) {
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		fmt.Println("    body:", truncate(string(body), 400))
		fail(fmt.Sprintf("HTTP %d", resp.StatusCode))
	}
	must(json.Unmarshal(body, v))
}

func extractHandle(body []byte) string {
	var u struct {
		Data struct {
			Attributes struct {
				Handle string `json:"handle"`
				Email  string `json:"email"`
			} `json:"attributes"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &u)
	if u.Data.Attributes.Handle != "" {
		return u.Data.Attributes.Handle
	}
	return u.Data.Attributes.Email
}

func randomURLSafe(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func must(err error) {
	if err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	fmt.Println("    FAIL:", msg)
	os.Exit(1)
}
