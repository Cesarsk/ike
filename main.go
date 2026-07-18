// ike — a k9s-style terminal UI for Datadog.
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Cesarsk/ike/internal/config"
	"github.com/Cesarsk/ike/internal/data"
	"github.com/Cesarsk/ike/internal/ui"
)

// version is injected by goreleaser via -ldflags at release time.
var version = "dev"

func main() {
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
		// PersistSession remembers the org + view across sessions, written as the
		// user navigates (see ui.App.persistSession).
		opts.PersistSession = func(context, view string) error {
			cfg.CurrentContext = context
			cfg.CurrentView = view
			return cfg.Save(config.Path())
		}
		for _, n := range cfg.Names() {
			opts.Contexts = append(opts.Contexts, ui.ContextInfo{Name: n, Site: cfg.Contexts[n].Site, Keys: keysLabel(cfg.Contexts[n])})
		}

		store := config.KeyringStore{}
		opts.Factory = func(name string) (data.Provider, error) {
			c, ok := cfg.Contexts[name]
			if !ok {
				return nil, fmt.Errorf("unknown context %q", name)
			}
			switch {
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
			return ui.ContextInfo{Name: name, Site: site, Keys: keysLabel(entry)}, nil
		}
		opts.ConfigPath = config.Path()
		opts.ReloadContexts = func() ([]ui.ContextInfo, error) {
			cfg2, err := config.Load(config.Path())
			if err != nil {
				return nil, err
			}
			cfg = cfg2 // factory/add/delete closures see the fresh config
			var infos []ui.ContextInfo
			for _, n := range cfg.Names() {
				infos = append(infos, ui.ContextInfo{Name: n, Site: cfg.Contexts[n].Site, Keys: keysLabel(cfg.Contexts[n])})
			}
			return infos, nil
		}
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
