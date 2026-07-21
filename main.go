// ike — a k9s-style terminal UI for Datadog.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Cesarsk/ike/internal/auth"
	"github.com/Cesarsk/ike/internal/config"
	"github.com/Cesarsk/ike/internal/data"
	"github.com/Cesarsk/ike/internal/ui"
)

// version is injected by goreleaser via -ldflags at release time.
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "auth" {
		runAuth(os.Args[2:])
		return
	}
	showVersion := flag.Bool("version", false, "print version and exit")
	demo := flag.Bool("demo", false, "run with built-in demo data (no credentials needed)")
	ctxFlag := flag.String("context", "", "context to start on (overrides $IKE_CONTEXT and current-context)")
	site := flag.String("site", "", "Datadog site override when running without a config file")
	refresh := flag.Duration("refresh", 30*time.Second, "auto-refresh interval for live views (monitors, incidents)")
	debug := flag.Bool("debug", false, "log at debug level (every fetch with timing and cache state)")
	logFile := flag.String("log-file", defaultLogPath(), "debug log file; empty string disables logging")
	flag.Parse()

	if *showVersion {
		fmt.Println("ike", version)
		return
	}

	setupLogging(*logFile, *debug)
	slog.Info("ike starting", "version", version, "demo", *demo, "config", config.Path(), "refresh", *refresh)

	var opts ui.Options
	opts.Refresh = *refresh

	if *demo {
		// Two fake orgs so the :ctx switcher is exercisable offline; add and
		// delete work in-memory so the whole flow is demoable too.
		sites := map[string]string{
			"demo-dev":  "datadoghq.eu",
			"demo-prod": "datadoghq.com",
		}
		for _, n := range []string{"demo-dev", "demo-prod"} {
			opts.Contexts = append(opts.Contexts, ui.ContextInfo{Name: n, Site: sites[n], Keys: "built-in"})
		}
		opts.Factory = func(name string) (data.Provider, error) {
			s, ok := sites[name]
			if !ok {
				return nil, fmt.Errorf("unknown demo context %q", name)
			}
			return data.NewDemo(s), nil
		}
		opts.AddContext = func(name, site, _, _, _, _ string) (ui.ContextInfo, error) {
			sites[name] = site
			return ui.ContextInfo{Name: name, Site: site, Keys: "in-memory"}, nil
		}
		// OAuth is faked in demo mode: no browser opens, the context is just
		// marked signed-in, so the add-OAuth + O flow is exercisable offline.
		opts.AddOAuthContext = func(name, site, _ string) (ui.ContextInfo, error) {
			sites[name] = site
			return ui.ContextInfo{Name: name, Site: site, Keys: "in-memory (oauth)", Auth: "oauth"}, nil
		}
		opts.OAuthLogin = func(name string) (ui.ContextInfo, error) {
			return ui.ContextInfo{Name: name, Site: sites[name], Keys: "in-memory (oauth)", Auth: "oauth"}, nil
		}
		opts.UpdateContext = func(name, authMode, site, _, _, _, _ string) (ui.ContextInfo, error) {
			sites[name] = site
			keys, auth := "in-memory", ""
			switch authMode {
			case "oauth":
				keys, auth = "in-memory (oauth)", "oauth"
			case "token":
				keys, auth = "in-memory (token)", "token"
			}
			return ui.ContextInfo{Name: name, Site: site, Keys: keys, Auth: auth}, nil
		}
		opts.DeleteContext = func(name string) error {
			delete(sites, name)
			return nil
		}
		savedQ := map[string][]ui.SavedQuery{}
		opts.SavedQueries = func(ctxName string) []ui.SavedQuery { return savedQ[ctxName] }
		opts.SaveQuery = func(ctxName, name, view, query string) error {
			qs := savedQ[ctxName]
			for i, q := range qs {
				if q.Name == name && q.View == view {
					qs[i] = ui.SavedQuery{Name: name, View: view, Query: query}
					return nil
				}
			}
			savedQ[ctxName] = append(qs, ui.SavedQuery{Name: name, View: view, Query: query})
			return nil
		}
		opts.DeleteQuery = func(ctxName, name, view string) error {
			qs := savedQ[ctxName]
			out := qs[:0]
			for _, q := range qs {
				if q.Name != name || q.View != view {
					out = append(out, q)
				}
			}
			savedQ[ctxName] = out
			return nil
		}
		opts.Current = "demo-dev"
	} else {
		cfg, err := config.Load(config.Path())
		if err != nil {
			fatal(err.Error())
		}
		// The config file can set refresh-interval; an explicit --refresh
		// flag still wins.
		refreshSet := false
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "refresh" {
				refreshSet = true
			}
		})
		if !refreshSet {
			opts.Refresh = cfg.Refresh(*refresh)
		}
		if *site != "" {
			if c, ok := cfg.Contexts["default"]; ok && len(cfg.Contexts) == 1 {
				c.Site = *site
				cfg.Contexts["default"] = c
			}
		}
		current := cfg.CurrentContext
		if v := os.Getenv("IKE_CONTEXT"); v != "" {
			current = v
		}
		if *ctxFlag != "" {
			current = *ctxFlag
		}
		if _, ok := cfg.Contexts[current]; !ok {
			fatal(fmt.Sprintf("context %q is not defined in %s (available: %v)", current, config.Path(), cfg.Names()))
		}
		opts.Current = current
		opts.TTLOverrides = cfg.ResolvedTTLOverrides()
		opts.Columns = cfg.Columns
		opts.Theme = cfg.Theme
		opts.SavedQueries = func(ctxName string) []ui.SavedQuery {
			c, ok := cfg.Contexts[ctxName]
			if !ok {
				return nil
			}
			out := make([]ui.SavedQuery, 0, len(c.SavedQueries))
			for _, q := range c.SavedQueries {
				out = append(out, ui.SavedQuery{Name: q.Name, View: q.View, Query: q.Query})
			}
			return out
		}
		opts.SaveQuery = func(ctxName, name, view, query string) error {
			cfg.SaveQuery(ctxName, name, view, query)
			return cfg.Save(config.Path())
		}
		opts.DeleteQuery = func(ctxName, name, view string) error {
			cfg.DeleteQuery(ctxName, name, view)
			return cfg.Save(config.Path())
		}
		opts.SaveSettings = func(theme string, ttl map[string]string, columns map[string][]string) error {
			cfg.Theme = theme
			cfg.TTLOverrides = ttl
			cfg.Columns = columns
			return cfg.Save(config.Path())
		}
		opts.CurrentView = cfg.CurrentView
		opts.Version = version
		// One-time getting-started page; :manual reopens it later.
		opts.FirstRun = !cfg.IntroSeen
		opts.MarkIntroSeen = func() error {
			cfg.IntroSeen = true
			return cfg.Save(config.Path())
		}
		// PersistSession remembers the org + view across sessions, written as the
		// user navigates (see ui.App.persistSession).
		opts.PersistSession = func(context, view string) error {
			cfg.CurrentContext = context
			cfg.CurrentView = view
			return cfg.Save(config.Path())
		}
		// OAuthLogin backs the :ctx row-scoped browser sign-in ('O'): it logs in
		// (or re-logs-in) the selected context, reading its site/subdomain from
		// the stored config. On a key/token context it converts it to OAuth.
		opts.OAuthLogin = func(name string) (ui.ContextInfo, error) {
			entry, err := loginContext(cfg, config.KeyringStore{}, loginTarget{name: name}, func(u string) error {
				slog.Info("oauth authorize", "url", u)
				return openBrowser(u)
			})
			if err != nil {
				return ui.ContextInfo{}, err
			}
			return ui.ContextInfo{Name: name, Site: entry.Site, Keys: keysLabel(entry), Auth: entry.Auth, Subdomain: entry.Subdomain, Active: entry.Active}, nil
		}
		// AddOAuthContext creates a pending OAuth context (:ctx → a, Auth =
		// browser sign-in): the entry is persisted now; tokens arrive when the
		// user presses O to sign in.
		opts.AddOAuthContext = func(name, site, subdomain string) (ui.ContextInfo, error) {
			if _, exists := cfg.Contexts[name]; exists {
				return ui.ContextInfo{}, fmt.Errorf("context %q already exists in %s", name, config.Path())
			}
			if !config.ValidSite(site) {
				return ui.ContextInfo{}, fmt.Errorf("unknown site %q", site)
			}
			if !config.ValidSubdomain(subdomain) {
				return ui.ContextInfo{}, fmt.Errorf("invalid subdomain %q — a single DNS label like acme-stage", subdomain)
			}
			entry := config.Context{Site: site, Subdomain: subdomain, Keychain: true, Auth: "oauth"}
			cfg.Contexts[name] = entry
			if err := cfg.Save(config.Path()); err != nil {
				delete(cfg.Contexts, name)
				return ui.ContextInfo{}, err
			}
			return ui.ContextInfo{Name: name, Site: site, Keys: keysLabel(entry), Auth: "oauth", Subdomain: subdomain}, nil
		}
		// PersistActive saves a context's spanning activation (space in :ctx).
		opts.PersistActive = func(context string, active bool) error {
			c, ok := cfg.Contexts[context]
			if !ok {
				return fmt.Errorf("unknown context %q", context)
			}
			c.Active = active
			cfg.Contexts[context] = c
			return cfg.Save(config.Path())
		}
		for _, n := range cfg.Names() {
			c := cfg.Contexts[n]
			opts.Contexts = append(opts.Contexts, ui.ContextInfo{Name: n, Site: c.Site, Keys: keysLabel(c), Auth: c.Auth, Subdomain: c.Subdomain, Active: c.Active})
		}

		store := config.KeyringStore{}
		opts.Factory = func(name string) (data.Provider, error) {
			c, ok := cfg.Contexts[name]
			if !ok {
				return nil, fmt.Errorf("unknown context %q", name)
			}
			switch {
			case c.Keychain && c.Auth == "oauth":
				src, err := oauthSource(store, name, c.Site)
				if err != nil {
					return nil, err
				}
				return data.NewLiveTokenSource(c.Site, c.WebBase(), src), nil
			case c.Keychain && c.Auth == "token":
				token, err := store.GetToken(name)
				if err != nil {
					return nil, err
				}
				return data.NewLiveToken(c.Site, c.WebBase(), token), nil
			case c.Keychain:
				apiKey, appKey, err := store.Get(name)
				if err != nil {
					return nil, err
				}
				return data.NewLive(c.Site, c.WebBase(), apiKey, appKey), nil
			case c.TokenEnv != "":
				token, err := c.ResolveToken()
				if err != nil {
					return nil, err
				}
				return data.NewLiveToken(c.Site, c.WebBase(), token), nil
			default:
				apiKey, appKey, err := c.Resolve()
				if err != nil {
					return nil, err
				}
				return data.NewLive(c.Site, c.WebBase(), apiKey, appKey), nil
			}
		}
		opts.AddContext = func(name, site, apiKey, appKey, token, subdomain string) (ui.ContextInfo, error) {
			if _, exists := cfg.Contexts[name]; exists {
				return ui.ContextInfo{}, fmt.Errorf("context %q already exists in %s", name, config.Path())
			}
			if !config.ValidSubdomain(subdomain) {
				return ui.ContextInfo{}, fmt.Errorf("invalid subdomain %q — a single DNS label like acme-stage", subdomain)
			}
			entry := config.Context{Site: site, Subdomain: subdomain, Keychain: true}
			var err error
			if token != "" {
				entry.Auth = "token"
				err = store.SetToken(name, token)
			} else {
				err = store.Set(name, apiKey, appKey)
			}
			if err != nil {
				return ui.ContextInfo{}, err
			}
			cfg.Contexts[name] = entry
			if err := cfg.Save(config.Path()); err != nil {
				delete(cfg.Contexts, name)
				_ = store.Delete(name) // roll back the keychain entry
				return ui.ContextInfo{}, err
			}
			return ui.ContextInfo{Name: name, Site: site, Keys: keysLabel(entry), Auth: entry.Auth, Subdomain: entry.Subdomain}, nil
		}
		// UpdateContext edits an existing context from the :ctx form ('e'):
		// change site/subdomain/auth type and (optionally) rotate credentials.
		// Empty credential args mean "keep the stored secret".
		opts.UpdateContext = func(name, authMode, site, apiKey, appKey, token, subdomain string) (ui.ContextInfo, error) {
			c, ok := cfg.Contexts[name]
			if !ok {
				return ui.ContextInfo{}, fmt.Errorf("unknown context %q", name)
			}
			if !config.ValidSite(site) {
				return ui.ContextInfo{}, fmt.Errorf("unknown site %q", site)
			}
			if !config.ValidSubdomain(subdomain) {
				return ui.ContextInfo{}, fmt.Errorf("invalid subdomain %q — a single DNS label like acme-stage", subdomain)
			}
			prev := c
			c.Site, c.Subdomain, c.Keychain = site, subdomain, true
			c.APIKeyEnv, c.AppKeyEnv, c.TokenEnv = "", "", ""
			switch authMode {
			case "oauth":
				c.Auth = "oauth"
			case "token":
				c.Auth = "token"
				if token != "" {
					if err := store.SetToken(name, token); err != nil {
						return ui.ContextInfo{}, err
					}
				}
			default: // keys
				c.Auth = ""
				if apiKey != "" && appKey != "" {
					if err := store.Set(name, apiKey, appKey); err != nil {
						return ui.ContextInfo{}, err
					}
				}
			}
			cfg.Contexts[name] = c
			if err := cfg.Save(config.Path()); err != nil {
				cfg.Contexts[name] = prev
				return ui.ContextInfo{}, err
			}
			// Drop the current provider so the next switch rebuilds it with the
			// new auth/site.
			return ui.ContextInfo{Name: name, Site: c.Site, Keys: keysLabel(c), Auth: c.Auth, Subdomain: c.Subdomain, Active: c.Active}, nil
		}
		opts.ConfigPath = config.Path()
		opts.DeleteContext = func(name string) error {
			c, ok := cfg.Contexts[name]
			if !ok {
				return fmt.Errorf("unknown context %q", name)
			}
			delete(cfg.Contexts, name)
			// Keep current-context consistent: if it pointed at the deleted
			// context, repoint it to a remaining one before saving.
			prevCurrent := cfg.CurrentContext
			if cfg.CurrentContext == name {
				if names := cfg.Names(); len(names) > 0 {
					cfg.CurrentContext = names[0]
				}
			}
			if err := cfg.Save(config.Path()); err != nil {
				cfg.Contexts[name] = c // roll back
				cfg.CurrentContext = prevCurrent
				return err
			}
			if c.Keychain {
				return config.KeyringStore{}.Delete(name)
			}
			return nil
		}
	}

	app, err := ui.New(opts)
	if err != nil {
		fatal(err.Error() + "\n      Try `ike --demo` to explore without credentials.")
	}
	if err := app.Run(); err != nil {
		fatal(err.Error())
	}
}

// defaultLogPath follows the k9s convention: an XDG state file, e.g.
// ~/.local/state/ike/ike.log.
func defaultLogPath() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "ike", "ike.log")
}

// setupLogging routes slog to a file — never to stderr, which the TUI owns.
// With no usable file, logging is discarded entirely.
func setupLogging(path string, debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	var w io.Writer = io.Discard
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
				w = f
			}
		}
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})))
}

func keysLabel(c config.Context) string {
	switch {
	case c.Keychain && c.Auth == "oauth":
		return "keychain (oauth)"
	case c.Keychain && c.Auth == "token":
		return "keychain (token)"
	case c.Keychain:
		return "keychain"
	case c.TokenEnv != "":
		return "$" + c.TokenEnv + " (token)"
	default:
		return "$" + c.APIKeyEnv
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "ike:", msg)
	os.Exit(1)
}

// oauthSource builds the lazy-refreshing token supplier for an OAuth context:
// credentials come from the keychain, refreshed sets are persisted back.
func oauthSource(store config.KeyringStore, name, site string) (func(context.Context) (string, error), error) {
	blob, err := store.GetOAuth(name)
	if err != nil {
		return nil, fmt.Errorf("%w — run: ike auth login --context %s", err, name)
	}
	var creds auth.Credentials
	if err := json.Unmarshal([]byte(blob), &creds); err != nil {
		return nil, fmt.Errorf("stored oauth credentials unreadable — run: ike auth login --context %s", name)
	}
	src := auth.NewSource("https://api."+site, creds, func(c auth.Credentials) error {
		b, err := json.Marshal(c)
		if err != nil {
			return err
		}
		return store.SetOAuth(name, string(b))
	})
	return src.Token, nil
}

// runAuth implements `ike auth login`: browser sign-in via OAuth2 + PKCE,
// tokens to the OS keychain, and a context created or updated in the config.
func runAuth(args []string) {
	if len(args) == 0 || args[0] != "login" {
		fmt.Fprintln(os.Stderr, "usage: ike auth login [--site <site>] [--subdomain <sub>] [--org <label>] [--context <name>]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("auth login", flag.ExitOnError)
	site := fs.String("site", "", "Datadog site (defaults to the context's site, else "+config.DefaultSite+")")
	subdomain := fs.String("subdomain", "", "org's custom web subdomain (single DNS label), if it has one")
	org := fs.String("org", "", "organization label; also the default context name")
	ctxName := fs.String("context", "", "context to create or update (defaults to --org)")
	_ = fs.Parse(args[1:])

	name := *ctxName
	if name == "" {
		name = *org
	}
	if name == "" {
		fatal("pass --org <label> or --context <name> so the login lands on a named context")
	}

	// Load the config if present; a missing file just means a fresh one.
	cfg, err := config.Load(config.Path())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			fatal(err.Error())
		}
		cfg = &config.Config{Contexts: map[string]config.Context{}}
	}
	fmt.Println("opening your browser to sign in — if it does not open, visit the printed URL")
	entry, err := loginContext(cfg, config.KeyringStore{}, loginTarget{name: name, site: *site, subdomain: *subdomain, org: *org}, func(u string) error {
		fmt.Println(u)
		return openBrowser(u)
	})
	if err != nil {
		fatal(err.Error())
	}
	cfg.CurrentContext = name
	if err := cfg.Save(config.Path()); err != nil {
		fatal(err.Error())
	}
	fmt.Printf("signed in: context %q (site %s) — tokens in the OS keychain, refreshed automatically.\nrun `ike` to start.\n", name, entry.Site)
}

// loginTarget names the context an OAuth login lands on. site/subdomain/org
// are merged onto the stored entry when non-empty (the TUI leaves them blank
// and relies on the stored values; the CLI fills them from flags).
type loginTarget struct {
	name, site, subdomain, org string
}

// loginContext is the shared core of `ike auth login` and the TUI's :ctx `O`
// form: merge the flags onto the named context entry, register (or reuse) the
// OAuth client, run the browser flow, persist tokens to the keychain and the
// entry to the config. openURL launches the authorize page (the CLI also
// prints it; the TUI must not write to stdout).
func loginContext(cfg *config.Config, store config.KeyringStore, t loginTarget, openURL func(string) error) (config.Context, error) {
	name := t.name
	entry := cfg.Contexts[name]
	// Remember the pre-login shape so we can clear stale credentials if this
	// login converts a key/token context to OAuth.
	converting := entry.Auth != "oauth" && (entry.Keychain || entry.APIKeyEnv != "" || entry.TokenEnv != "")
	if t.site != "" {
		entry.Site = t.site
	}
	if entry.Site == "" {
		entry.Site = config.DefaultSite
	}
	if t.subdomain != "" {
		entry.Subdomain = t.subdomain
	}
	if t.org != "" {
		entry.Org = t.org
	}
	if !config.ValidSite(entry.Site) {
		return entry, fmt.Errorf("unknown site %q — refusing to send a login to an unrecognized host (valid: %v)", entry.Site, config.Sites)
	}
	if !config.ValidSubdomain(entry.Subdomain) {
		return entry, fmt.Errorf("invalid subdomain %q — a single DNS label like acme-stage", entry.Subdomain)
	}
	ep := auth.EndpointsFor(entry.Site, entry.Subdomain)

	// Reuse the registered client when this context logged in before;
	// register a fresh one otherwise.
	clientID := ""
	if blob, err := store.GetOAuth(name); err == nil {
		var prev auth.Credentials
		if json.Unmarshal([]byte(blob), &prev) == nil {
			clientID = prev.ClientID
		}
	}
	if clientID == "" {
		id, err := auth.Register(context.Background(), ep.API)
		if err != nil {
			return entry, err
		}
		clientID = id
	}

	tok, err := auth.Login(context.Background(), ep, clientID, openURL)
	if err != nil {
		return entry, err
	}
	blob, _ := json.Marshal(auth.Credentials{ClientID: clientID, TokenSet: tok})
	if err := store.SetOAuth(name, string(blob)); err != nil {
		return entry, err
	}
	// Converting away from keys/token: drop the now-unused env references from
	// the config and the stale secrets from the keychain.
	if converting {
		entry.APIKeyEnv, entry.AppKeyEnv, entry.TokenEnv = "", "", ""
		if err := store.DeleteNonOAuth(name); err != nil {
			slog.Warn("could not clear old credentials after oauth conversion", "context", name, "err", err)
		}
	}
	entry.Keychain = true
	entry.Auth = "oauth"
	cfg.Contexts[name] = entry
	if err := cfg.Save(config.Path()); err != nil {
		return entry, err
	}
	return entry, nil
}

// openBrowser opens a URL with the platform opener; the URL is also printed
// so a headless/remote session can open it manually.
func openBrowser(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "linux":
		return exec.Command("xdg-open", u).Start()
	}
	return fmt.Errorf("unsupported platform %s — open the URL manually", runtime.GOOS)
}
