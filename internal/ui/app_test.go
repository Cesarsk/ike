package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/Cesarsk/ike/internal/data"
)

// TestAppSmoke boots the full TUI on a headless simulation screen and walks
// the k9s-style interactions: command mode, filtering, quick filters, help,
// detail view and quit.
func TestAppSmoke(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	sim.SetSize(140, 35)

	app := newDemoApp(t)
	app.SetScreen(sim)

	done := make(chan error, 1)
	go func() { done <- app.Run() }()

	waitFor(t, sim, "Monitors(all)")
	waitFor(t, sim, "Kong data plane 5xx")

	// Command mode: switch to every registered resource.
	typeCmd(sim, ":incidents")
	waitFor(t, sim, "Incidents(all)")
	waitFor(t, sim, "IR-142")

	// Incident action: r opens a confirm modal (the only write path); pick
	// a target state and the change is applied + reflected on reload.
	typeRunes(sim, "r")
	waitFor(t, sim, "currently active")
	press(sim, tcell.KeyRight) // Cancel → "→ stable"
	press(sim, tcell.KeyRight) // → "→ resolved"
	press(sim, tcell.KeyEnter)
	// IR-142 starts "active"; this row only appears once the change applied
	// and the incidents view reloaded.
	waitFor(t, sim, "IR-142 SEV-1 resolved")

	// Incident quick filter: digit 3 = resolved only (STATE column).
	typeRunes(sim, "3")
	waitFor(t, sim, "Incidents(state:resolved)")
	typeRunes(sim, "0") // back to all
	waitFor(t, sim, "Incidents(all)")

	typeCmd(sim, ":slos")
	waitFor(t, sim, "SLOs(all)")
	// SLO type filter (t cycles metric → monitor → time_slice → all) and
	// sorting (s cycles column, S reverses); title reflects both.
	typeRunes(sim, "t")
	waitFor(t, sim, "SLOs(type:metric)")
	typeRunes(sim, "s")
	waitFor(t, sim, "↕NAME▲")
	typeRunes(sim, "S")
	waitFor(t, sim, "↕NAME▼")
	press(sim, tcell.KeyEscape) // clears filter+sort side-effects for a clean state
	waitFor(t, sim, "SLOs(all)")

	typeCmd(sim, ":dashboards")
	waitFor(t, sim, "Dashboards(all)")

	// Dashboard render: enter draws widgets with sparklines, not raw JSON;
	// esc pops back to the dashboards table.
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "widgets · sparklines")
	waitFor(t, sim, "Request rate")
	press(sim, tcell.KeyEscape)
	waitFor(t, sim, "Dashboards(all)")

	// Logs: '/' is a server-side query.
	typeCmd(sim, ":logs")
	waitFor(t, sim, "Logs(")
	typeRunes(sim, "/")
	typeRunes(sim, "status:error")
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "Logs(status:error · 15m)")

	// Time-range control: digit keys switch the Logs window (title reflects it).
	typeRunes(sim, "2")
	waitFor(t, sim, "Logs(status:error · 1h)")
	typeRunes(sim, "1")
	waitFor(t, sim, "Logs(status:error · 15m)")

	// Back to monitors, then esc must pop the navigation stack back to the
	// previous page (logs, with its query intact) — k9s-style history.
	typeCmd(sim, ":monitors")
	waitFor(t, sim, "Monitors(all)")
	press(sim, tcell.KeyEscape)
	waitFor(t, sim, "Logs(status:error · 15m)")

	// Quick filter + client-side filter on monitors.
	typeCmd(sim, ":monitors")
	waitFor(t, sim, "Monitors(all)")

	// Auto-refresh toggle: 'p' pauses (header shows auto:off), 'p' resumes.
	typeRunes(sim, "p")
	waitFor(t, sim, "auto:off")
	typeRunes(sim, "p")
	waitFor(t, sim, "auto:on")

	// Mute: 'm' opens a confirm modal; confirming mutes (demo flips state to
	// Ignored) and the view reloads.
	typeRunes(sim, "m")
	waitFor(t, sim, "Mute monitor")
	press(sim, tcell.KeyRight) // Cancel → Mute
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "Ignored")

	typeRunes(sim, "1")
	waitFor(t, sim, "Monitors(state:Alert)")
	typeRunes(sim, "0")
	typeRunes(sim, "/kong")
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "Monitors(/kong)")

	// Drill-down: 'l' on the Kong monitor jumps to Logs pre-filtered with
	// its service tag; esc pops back to the filtered monitors view.
	typeRunes(sim, "l")
	waitFor(t, sim, "Logs(service:kong-proxy · 15m)")
	waitFor(t, sim, "kong-proxy") // rows for that service are on screen
	press(sim, tcell.KeyEscape)
	waitFor(t, sim, "Monitors(/kong)")

	// Help page.
	typeRunes(sim, "?")
	waitFor(t, sim, "NAVIGATION")
	typeRunes(sim, "q") // closes help, must not quit the app
	waitFor(t, sim, "Monitors(/kong)")

	// Detail view on the selected row: header hints must stay visible,
	// '?' must open help, and esc must go back step by step.
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "Monitor/")
	waitFor(t, sim, "<esc>back")   // context hints visible in detail view
	waitFor(t, sim, "full_object") // detail upgraded to the on-demand fetch
	typeRunes(sim, "?")
	waitFor(t, sim, "NAVIGATION")
	press(sim, tcell.KeyEscape) // help → back to detail, not to table
	waitFor(t, sim, "Monitor/")
	press(sim, tcell.KeyEscape) // detail → table
	waitFor(t, sim, "Monitors(/kong)")

	// Context switching: :ctx lists orgs, enter switches — cache, budget
	// and navigation history are dropped at the boundary.
	typeCmd(sim, ":ctx")
	waitFor(t, sim, "Contexts(all)")
	waitFor(t, sim, "demo-prod")
	press(sim, tcell.KeyDown) // demo-dev (active) → demo-prod
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "demo [demo-prod]") // header shows the new org
	waitFor(t, sim, "datadoghq.com")    // and its site
	waitFor(t, sim, "Monitors(all)")    // lands on a fresh monitors view
	press(sim, tcell.KeyEscape)         // history was cleared: esc is a no-op
	waitFor(t, sim, "Monitors(all)")

	// Add a context via the TUI form: name + site + pasted keys.
	typeCmd(sim, ":ctx")
	waitFor(t, sim, "Contexts(all)")
	typeRunes(sim, "a")
	waitFor(t, sim, "Add context")
	waitFor(t, sim, "How to fill this in") // guidance panel is on screen

	// Save with no input: the validation error must be visible on the form
	// page itself, not just in the bottom status bar.
	for i := 0; i < 6; i++ {
		press(sim, tcell.KeyTab) // skip Name, Site, both keys, token, subdomain
	}
	press(sim, tcell.KeyEnter) // Save
	waitFor(t, sim, "✗ Name is required")
	for i := 0; i < 6; i++ {
		press(sim, tcell.KeyBacktab) // back to Name
	}

	typeRunes(sim, "staging") // Name — spaces would be legal too
	press(sim, tcell.KeyTab)  // → Site dropdown (keep default US1)
	press(sim, tcell.KeyTab)  // → API key
	typeRunes(sim, "pasted-api-key")
	press(sim, tcell.KeyTab) // → APP key
	typeRunes(sim, "pasted-app-key")
	press(sim, tcell.KeyTab) // → Access token (left empty: key-pair auth)
	press(sim, tcell.KeyTab) // → Subdomain (optional, left empty)
	press(sim, tcell.KeyTab) // → Save button
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "staging") // new row in the contexts table
	waitFor(t, sim, "added")

	// Switch to it, then away again (can't delete the active context).
	// Filter to the single "staging" row so selection is unambiguous (row
	// counting races with async re-renders from background load()s).
	typeRunes(sim, "/staging")
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "Contexts(/staging)")
	press(sim, tcell.KeyEnter) // only row = staging → switch to it
	waitFor(t, sim, "demo [staging]")
	typeCmd(sim, ":ctx")
	waitFor(t, sim, "Contexts(all)")
	typeRunes(sim, "/demo-dev")
	press(sim, tcell.KeyEnter)
	press(sim, tcell.KeyEnter) // only row = demo-dev → switch back
	waitFor(t, sim, "demo [demo-dev]")

	// Delete the added context: filter to staging, ctrl-d → confirm → Delete.
	typeCmd(sim, ":ctx")
	waitFor(t, sim, "Contexts(all)")
	typeRunes(sim, "/staging")
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "Contexts(/staging)")
	press(sim, tcell.KeyCtrlD)
	waitFor(t, sim, "Delete context")
	press(sim, tcell.KeyRight) // Cancel → Delete button
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "staging deleted")
	press(sim, tcell.KeyEscape) // back out of :ctx before quitting

	// Quit.
	typeRunes(sim, "q")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("app exited with error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("app did not exit after 'q'")
	}
}

// TestStartupWithBrokenContext: when the initial context's credentials
// can't be resolved (first run, no env vars), the app must still start —
// on the :ctx view, showing the error — instead of exiting.
func TestStartupWithBrokenContext(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	sim.SetSize(140, 35)

	app, err := New(Options{
		Contexts: []ContextInfo{{Name: "dev", Site: "datadoghq.eu", Keys: "$MISSING_VAR"}},
		Current:  "dev",
		Factory: func(string) (data.Provider, error) {
			return nil, fmt.Errorf("environment variables MISSING_VAR must be set")
		},
		Refresh: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	app.SetScreen(sim)
	go func() { _ = app.Run() }()

	waitFor(t, sim, "Contexts(all)")
	waitFor(t, sim, "MISSING_VAR") // the resolution error is on screen
	app.Stop()
}

// TestEditConfigReload: 'e' in :ctx suspends into $EDITOR and reloads the
// config afterwards. EDITOR=true makes the editor a no-op; the injected
// ReloadContexts simulates the file having gained a context.
func TestEditConfigReload(t *testing.T) {
	t.Setenv("EDITOR", "true")
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	sim.SetSize(140, 35)

	app := newDemoApp(t)
	app.opts.ConfigPath = "/dev/null"
	app.opts.ReloadContexts = func() ([]ContextInfo, error) {
		return []ContextInfo{
			{Name: "demo-dev", Site: "datadoghq.eu", Keys: "built-in"},
			{Name: "demo-prod", Site: "datadoghq.com", Keys: "built-in"},
			{Name: "edited-in-vi", Site: "us5.datadoghq.com", Keys: "$NEW_VAR"},
		}, nil
	}
	app.SetScreen(sim)
	go func() { _ = app.Run() }()

	waitFor(t, sim, "Monitors(all)")
	typeCmd(sim, ":ctx")
	waitFor(t, sim, "Contexts(all)")
	typeRunes(sim, "e")
	waitFor(t, sim, "config reloaded")
	waitFor(t, sim, "edited-in-vi") // reloaded context appears in the table
	app.Stop()
}

// newDemoApp builds an App with two offline demo contexts, mirroring what
// `ike --demo` wires up in main.go — including in-memory add/delete.
func newDemoApp(t *testing.T) *App {
	t.Helper()
	sites := map[string]string{"demo-dev": "datadoghq.eu", "demo-prod": "datadoghq.com"}
	app, err := New(Options{
		Contexts: []ContextInfo{
			{Name: "demo-dev", Site: sites["demo-dev"], Keys: "built-in"},
			{Name: "demo-prod", Site: sites["demo-prod"], Keys: "built-in"},
		},
		Current: "demo-dev",
		Factory: func(name string) (data.Provider, error) {
			return data.NewDemo(sites[name]), nil
		},
		AddContext: func(name, site, _, _, _, _ string) (ContextInfo, error) {
			sites[name] = site
			return ContextInfo{Name: name, Site: site, Keys: "in-memory"}, nil
		},
		DeleteContext: func(name string) error {
			delete(sites, name)
			return nil
		},
		Refresh: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func typeCmd(sim tcell.SimulationScreen, cmd string) {
	typeRunes(sim, cmd)
	press(sim, tcell.KeyEnter)
}

func typeRunes(sim tcell.SimulationScreen, s string) {
	for _, r := range s {
		sim.InjectKey(tcell.KeyRune, r, tcell.ModNone)
		time.Sleep(10 * time.Millisecond)
	}
}

func press(sim tcell.SimulationScreen, k tcell.Key) {
	sim.InjectKey(k, 0, tcell.ModNone)
	time.Sleep(10 * time.Millisecond)
}

func waitFor(t *testing.T, sim tcell.SimulationScreen, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(screenText(sim), want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("screen never showed %q; screen was:\n%s", want, screenText(sim))
}

func screenText(sim tcell.SimulationScreen) string {
	cells, w, _ := sim.GetContents()
	var b strings.Builder
	for i, c := range cells {
		if len(c.Runes) > 0 {
			b.WriteRune(c.Runes[0])
		} else {
			b.WriteRune(' ')
		}
		if (i+1)%w == 0 {
			b.WriteRune('\n')
		}
	}
	return b.String()
}
