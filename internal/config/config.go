// Package config implements kubeconfig-style named contexts: one Datadog
// org (site + credentials) per context, switchable at runtime via :ctx.
//
// Secrets NEVER live in the config file. Each context names the environment
// variables that hold its keys (api-key-env / app-key-env); plaintext
// api-key/app-key fields are rejected at parse time.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

const DefaultSite = "datadoghq.com"

// Sites is the fixed list of Datadog regional endpoints. It is a security
// control, not a convenience: credentials are sent as headers to
// api.<site>, so an unrecognized site in a (possibly tampered or
// socially-engineered) config file would exfiltrate keys. Load refuses
// anything not on this list.
var Sites = []string{
	"datadoghq.com",
	"datadoghq.eu",
	"us3.datadoghq.com",
	"us5.datadoghq.com",
	"ap1.datadoghq.com",
	"ap2.datadoghq.com",
	"ddog-gov.com",
}

// ValidSite reports whether s is a known Datadog endpoint.
func ValidSite(s string) bool {
	for _, v := range Sites {
		if s == v {
			return true
		}
	}
	return false
}

// Context is one Datadog organization. Credentials come from either the
// named environment variables or, for contexts added in the TUI, the OS
// keychain (macOS Keychain / Linux Secret Service) — never from this file.
//
// Two auth shapes are supported: an API key + application key pair, or a
// single bearer/access token (OAuth2 access token or PAT — what `pup`
// issues). For env contexts the shape is implied by which env names are
// set; for keychain contexts it is recorded in `auth: token`.
type Context struct {
	Site      string `yaml:"site,omitempty"`
	APIKeyEnv string `yaml:"api-key-env,omitempty"`
	AppKeyEnv string `yaml:"app-key-env,omitempty"`
	TokenEnv  string `yaml:"token-env,omitempty"`
	Keychain  bool   `yaml:"keychain,omitempty"`
	Auth      string `yaml:"auth,omitempty"` // "" (key pair) or "token"
}

type Config struct {
	CurrentContext string             `yaml:"current-context"`
	Contexts       map[string]Context `yaml:"contexts"`
}

// Path returns the config file location: $DDEZ_CONFIG if set, otherwise
// ~/.config/ddez/config.yaml.
func Path() string {
	if p := os.Getenv("DDEZ_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "ddez", "config.yaml")
}

// Load reads and validates the config file. A missing file is not an error:
// it returns an implicit single-context config built from the classic
// DD_API_KEY / DD_APP_KEY / DD_SITE environment variables, so ddez keeps
// working with no config at all.
func Load(path string) (*Config, error) {
	if path == "" {
		return implicit(), nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return implicit(), nil
	}
	if err != nil {
		return nil, err
	}

	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	// Strict decoding: rejects plaintext `api-key:`/`app-key:` fields and
	// typos. Secrets must be referenced via env vars, never inlined.
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w\n(secrets never go in the config file — point api-key-env/app-key-env at environment variables)", path, err)
	}

	if len(c.Contexts) == 0 {
		return nil, fmt.Errorf("%s: no contexts defined", path)
	}
	for name, ctx := range c.Contexts {
		if ctx.Site == "" {
			ctx.Site = DefaultSite
			c.Contexts[name] = ctx
		}
		if !ValidSite(ctx.Site) {
			return nil, fmt.Errorf("%s: context %q has unknown site %q — refusing to send credentials to an unrecognized host (valid sites: %v)", path, name, ctx.Site, Sites)
		}
		keyPair := ctx.APIKeyEnv != "" && ctx.AppKeyEnv != ""
		if !ctx.Keychain && !keyPair && ctx.TokenEnv == "" {
			return nil, fmt.Errorf("%s: context %q needs credentials: api-key-env + app-key-env, token-env, or keychain: true", path, name)
		}
	}
	if _, ok := c.Contexts[c.CurrentContext]; !ok {
		return nil, fmt.Errorf("%s: current-context %q is not a defined context", path, c.CurrentContext)
	}
	return &c, nil
}

func implicit() *Config {
	site := os.Getenv("DD_SITE")
	// The same allowlist applies to DD_SITE — an attacker-influenced env
	// var must not redirect credentials either.
	if site == "" || !ValidSite(site) {
		site = DefaultSite
	}
	return &Config{
		CurrentContext: "default",
		Contexts: map[string]Context{
			"default": {Site: site, APIKeyEnv: "DD_API_KEY", AppKeyEnv: "DD_APP_KEY"},
		},
	}
}

// Resolve reads the context's credentials from its environment variables.
func (c Context) Resolve() (apiKey, appKey string, err error) {
	apiKey, appKey = os.Getenv(c.APIKeyEnv), os.Getenv(c.AppKeyEnv)
	if apiKey == "" || appKey == "" {
		return "", "", fmt.Errorf("environment variables %s and %s must both be set", c.APIKeyEnv, c.AppKeyEnv)
	}
	return apiKey, appKey, nil
}

// ResolveToken reads the context's bearer token from its environment variable.
func (c Context) ResolveToken() (string, error) {
	tok := os.Getenv(c.TokenEnv)
	if tok == "" {
		return "", fmt.Errorf("environment variable %s must be set", c.TokenEnv)
	}
	return tok, nil
}

// Save writes the config back to disk (0600 — it holds no secrets, but org
// names and sites are nobody else's business either). The write is atomic
// (temp file + rename) so a crash can never leave a corrupt config.
// Note: comments in a hand-written file are not preserved.
func (c *Config) Save(path string) error {
	if path == "" {
		return fmt.Errorf("no config path")
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Names returns the context names, sorted.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Contexts))
	for n := range c.Contexts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
