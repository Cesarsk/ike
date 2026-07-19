package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadValid(t *testing.T) {
	p := write(t, `
current-context: dev
contexts:
  dev:
    site: datadoghq.eu
    api-key-env: IKE_DEV_API_KEY
    app-key-env: IKE_DEV_APP_KEY
  prod:
    api-key-env: IKE_PROD_API_KEY
    app-key-env: IKE_PROD_APP_KEY
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.CurrentContext != "dev" {
		t.Errorf("current = %q", c.CurrentContext)
	}
	if got := c.Contexts["prod"].Site; got != DefaultSite {
		t.Errorf("prod site should default to %s, got %q", DefaultSite, got)
	}
	if got := c.Names(); len(got) != 2 || got[0] != "dev" || got[1] != "prod" {
		t.Errorf("names = %v", got)
	}
}

func TestTTLOverrides(t *testing.T) {
	p := write(t, `
current-context: dev
contexts:
  dev:
    site: datadoghq.eu
    api-key-env: IKE_DEV_API_KEY
    app-key-env: IKE_DEV_APP_KEY
ttl-overrides:
  logs: 120s
  monitors: 1m
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	got := c.ResolvedTTLOverrides()
	if got["logs"] != 120*time.Second {
		t.Errorf("logs ttl = %v, want 120s", got["logs"])
	}
	if got["monitors"] != time.Minute {
		t.Errorf("monitors ttl = %v, want 1m", got["monitors"])
	}
}

func TestSaveAndDeleteQuery(t *testing.T) {
	p := write(t, `
current-context: dev
contexts:
  dev:
    site: datadoghq.eu
    api-key-env: IKE_DEV_API_KEY
    app-key-env: IKE_DEV_APP_KEY
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.SaveQuery("dev", "errors", "logs", "status:error") {
		t.Fatal("SaveQuery on an existing context should succeed")
	}
	// Same name+view replaces in place.
	c.SaveQuery("dev", "errors", "logs", "status:error env:prod")
	if got := c.Contexts["dev"].SavedQueries; len(got) != 1 || got[0].Query != "status:error env:prod" {
		t.Fatalf("replace failed: %v", got)
	}
	// Same name, different view = a distinct entry.
	c.SaveQuery("dev", "errors", "traces", "service:api")
	if len(c.Contexts["dev"].SavedQueries) != 2 {
		t.Fatalf("want 2 queries, got %d", len(c.Contexts["dev"].SavedQueries))
	}
	// Delete is name+view scoped.
	c.DeleteQuery("dev", "errors", "logs")
	if rem := c.Contexts["dev"].SavedQueries; len(rem) != 1 || rem[0].View != "traces" {
		t.Fatalf("want only the traces query left, got %v", rem)
	}
	if c.SaveQuery("nope", "x", "logs", "*") {
		t.Fatal("SaveQuery on an unknown context should return false")
	}
}

func TestTTLOverrideInvalidDurationRejected(t *testing.T) {
	p := write(t, `
current-context: dev
contexts:
  dev:
    site: datadoghq.eu
    api-key-env: IKE_DEV_API_KEY
    app-key-env: IKE_DEV_APP_KEY
ttl-overrides:
  logs: not-a-duration
`)
	if _, err := Load(p); err == nil {
		t.Fatal("expected error for invalid ttl-override duration")
	}
}

func TestRejectPlaintextSecrets(t *testing.T) {
	p := write(t, `
current-context: dev
contexts:
  dev:
    site: datadoghq.eu
    api-key: deadbeef
    app-key-env: X
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("plaintext api-key must be rejected")
	}
	if !strings.Contains(err.Error(), "secrets never go in the config file") {
		t.Errorf("error should explain the env indirection rule, got: %v", err)
	}
}

func TestDanglingCurrentContextFallsBack(t *testing.T) {
	// Deleting the context current-context pointed at must not brick the
	// app: Load falls back to the first remaining context.
	p := write(t, `
current-context: default
contexts:
  dev: {site: datadoghq.eu, api-key-env: A, app-key-env: B}
  prod: {site: datadoghq.com, api-key-env: C, app-key-env: D}
`)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("dangling current-context must not error, got %v", err)
	}
	if c.CurrentContext != "dev" { // sorted-first of {dev, prod}
		t.Errorf("fallback current-context = %q, want dev", c.CurrentContext)
	}
}

func TestNoContextsIsError(t *testing.T) {
	p := write(t, "current-context: x\ncontexts: {}\n")
	if _, err := Load(p); err == nil {
		t.Fatal("a config with zero contexts must still error")
	}
}

func TestMissingKeyEnvNames(t *testing.T) {
	p := write(t, `
current-context: dev
contexts:
  dev: {site: datadoghq.eu}
`)
	if _, err := Load(p); err == nil {
		t.Fatal("context without api-key-env/app-key-env must be rejected")
	}
}

func TestMissingFileFallsBackToImplicit(t *testing.T) {
	t.Setenv("DD_SITE", "us5.datadoghq.com")
	c, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	def, ok := c.Contexts["default"]
	if !ok || c.CurrentContext != "default" {
		t.Fatalf("implicit config expected, got %+v", c)
	}
	if def.Site != "us5.datadoghq.com" || def.APIKeyEnv != "DD_API_KEY" {
		t.Errorf("implicit context wrong: %+v", def)
	}
}

func TestTokenEnvContext(t *testing.T) {
	p := write(t, `
current-context: dev
contexts:
  dev:
    site: datadoghq.eu
    token-env: IKE_DEV_TOKEN
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("IKE_DEV_TOKEN", "tok-123")
	tok, err := c.Contexts["dev"].ResolveToken()
	if err != nil || tok != "tok-123" {
		t.Fatalf("token resolve: %v %q", err, tok)
	}
}

func TestSubdomainValidation(t *testing.T) {
	good := write(t, `
current-context: stage
contexts:
  stage:
    site: datadoghq.eu
    subdomain: acme-stage
    api-key-env: A
    app-key-env: B
`)
	c, err := Load(good)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Contexts["stage"].WebBase(); got != "https://acme-stage.datadoghq.eu" {
		t.Errorf("WebBase = %q", got)
	}
	if got := (Context{Site: "datadoghq.eu"}).WebBase(); got != "https://app.datadoghq.eu" {
		t.Errorf("default WebBase = %q", got)
	}

	bad := write(t, `
current-context: stage
contexts:
  stage:
    site: datadoghq.eu
    subdomain: "evil.com/x"
    api-key-env: A
    app-key-env: B
`)
	if _, err := Load(bad); err == nil {
		t.Fatal("subdomain with dots/slashes must be rejected — it would escape the site domain")
	}
}

func TestUnknownSiteRejected(t *testing.T) {
	p := write(t, `
current-context: dev
contexts:
  dev:
    site: evil.example.com
    api-key-env: A
    app-key-env: B
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("unknown site must be rejected — credentials would be sent to it")
	}
	if !strings.Contains(err.Error(), "refusing to send credentials") {
		t.Errorf("error should explain the exfiltration risk, got: %v", err)
	}
}

func TestImplicitIgnoresInvalidDDSite(t *testing.T) {
	t.Setenv("DD_SITE", "evil.example.com")
	c, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Contexts["default"].Site; got != DefaultSite {
		t.Errorf("invalid DD_SITE must fall back to %s, got %q", DefaultSite, got)
	}
}

func TestKeychainContextNeedsNoEnvNames(t *testing.T) {
	p := write(t, `
current-context: prod
contexts:
  prod:
    site: datadoghq.com
    keychain: true
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Contexts["prod"].Keychain {
		t.Error("keychain flag lost")
	}
}

func TestSaveRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.yaml")
	c := &Config{
		CurrentContext: "dev",
		CurrentView:    "incidents",
		Contexts: map[string]Context{
			"dev":  {Site: "datadoghq.eu", APIKeyEnv: "A", AppKeyEnv: "B"},
			"prod": {Site: "datadoghq.com", Keychain: true, Active: true},
		},
	}
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentContext != "dev" || len(got.Contexts) != 2 || !got.Contexts["prod"].Keychain {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.CurrentView != "incidents" {
		t.Errorf("current-view not preserved: %q", got.CurrentView)
	}
	if !got.Contexts["prod"].Active || got.Contexts["dev"].Active {
		t.Errorf("active flags not preserved: %+v", got.Contexts)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestResolve(t *testing.T) {
	t.Setenv("T_API", "aaa")
	t.Setenv("T_APP", "bbb")
	api, app, err := (Context{APIKeyEnv: "T_API", AppKeyEnv: "T_APP"}).Resolve()
	if err != nil || api != "aaa" || app != "bbb" {
		t.Fatalf("resolve: %v %q %q", err, api, app)
	}
	if _, _, err := (Context{APIKeyEnv: "T_API", AppKeyEnv: "T_UNSET"}).Resolve(); err == nil {
		t.Fatal("missing env var must error")
	}
}

func TestRefreshInterval(t *testing.T) {
	c := &Config{RefreshInterval: "45s"}
	if got := c.Refresh(30 * time.Second); got != 45*time.Second {
		t.Errorf("Refresh = %v, want 45s", got)
	}
	if got := (&Config{}).Refresh(30 * time.Second); got != 30*time.Second {
		t.Errorf("unset Refresh = %v, want default 30s", got)
	}
	if got := (&Config{RefreshInterval: "garbage"}).Refresh(30 * time.Second); got != 30*time.Second {
		t.Errorf("bad Refresh = %v, want default 30s", got)
	}
	if got := (&Config{RefreshInterval: "0"}).Refresh(30 * time.Second); got != 0 {
		t.Errorf("zero Refresh = %v, want 0 (disabled)", got)
	}
}
