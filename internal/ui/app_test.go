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

	// Incident severity: v opens a confirm modal; pick a target severity and
	// the change is applied + reflected on reload (SEV column, independent of
	// the state just changed above).
	typeRunes(sim, "v")
	waitFor(t, sim, "currently SEV-1")
	press(sim, tcell.KeyRight) // Cancel → "→ SEV-2"
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "IR-142 SEV-2 resolved")

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

	// Query history: submit a second query, then ↑ in the prompt recalls the
	// previous one and re-submitting it restores that view. ctrl-u clears the
	// prefilled current query before typing a fresh one.
	typeRunes(sim, "/")
	press(sim, tcell.KeyCtrlU) // clear prefilled "status:error"
	typeRunes(sim, "service:vault")
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "Logs(service:vault · 15m)")
	typeRunes(sim, "/")     // reopen prompt (prefilled with current "service:vault")
	press(sim, tcell.KeyUp) // ↑ → most-recent history entry ("service:vault")
	press(sim, tcell.KeyUp) // ↑ → older entry ("status:error")
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "Logs(status:error · 15m)")

	// Correlation: 't' on an error log (which carries a trace_id in demo)
	// opens the trace waterfall; 'l' from the trace jumps to that trace's
	// logs; esc pops back to the logs view. This is the debugging loop.
	press(sim, tcell.KeyEnter)  // ensure a row is selected (top error log)
	press(sim, tcell.KeyEscape) // close detail, stay on logs
	waitFor(t, sim, "Logs(status:error · 15m)")
	typeRunes(sim, "t")
	waitFor(t, sim, "spans · total")                     // trace waterfall header
	waitFor(t, sim, "kong-proxy")                        // first hop of the demo trace chain
	waitFor(t, sim, "logs in this trace (chronological") // unified timeline below the waterfall
	typeRunes(sim, "l")                                  // trace → its logs (full view)
	waitFor(t, sim, "Logs(trace_id:")                    // logs filtered to the trace
	press(sim, tcell.KeyEscape)                          // back to the trace
	waitFor(t, sim, "spans · total")
	press(sim, tcell.KeyEscape) // back to logs
	waitFor(t, sim, "Logs(status:error · 15m)")

	// Log patterns: P clusters the loaded lines; esc pops back to logs.
	typeRunes(sim, "P")
	waitFor(t, sim, "patterns")
	waitFor(t, sim, "loaded log lines")
	press(sim, tcell.KeyEscape)
	waitFor(t, sim, "Logs(status:error · 15m)")

	// Traces view: server query + t opens the waterfall for a span's trace.
	typeCmd(sim, ":traces")
	waitFor(t, sim, "Traces(")
	waitFor(t, sim, "kong-proxy")
	typeRunes(sim, "t")
	waitFor(t, sim, "spans · total")
	press(sim, tcell.KeyEscape)
	waitFor(t, sim, "Traces(")

	// Events feed: the "what changed" stream (deploys, alerts).
	typeCmd(sim, ":events")
	waitFor(t, sim, "Events(")
	waitFor(t, sim, "Deployed payments-api")

	// Downtimes: org-wide mute visibility + cancel (x) behind a confirm.
	typeCmd(sim, ":downtimes")
	waitFor(t, sim, "Downtimes(")
	waitFor(t, sim, "Maintenance window")
	typeRunes(sim, "x") // cancel the selected (top) downtime
	waitFor(t, sim, "Cancel downtime dt-0")
	press(sim, tcell.KeyRight) // Cancel → "Cancel downtime"
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "canceled") // status flips once the write applied + reloaded
	press(sim, tcell.KeyEscape) // back to events
	waitFor(t, sim, "Events(")
	press(sim, tcell.KeyEscape) // back to traces (nav stack)
	waitFor(t, sim, "Traces(")

	// Back to monitors, then esc must pop the navigation stack back to the
	// previous page (traces) — k9s-style history.
	typeCmd(sim, ":monitors")
	waitFor(t, sim, "Monitors(all)")
	press(sim, tcell.KeyEscape)
	waitFor(t, sim, "Traces(")
	press(sim, tcell.KeyEscape) // traces → logs (earlier in the stack)
	waitFor(t, sim, "Logs(status:error · 15m)")

	// Quick filter + client-side filter on monitors.
	typeCmd(sim, ":monitors")
	waitFor(t, sim, "Monitors(all)")

	// Auto-refresh toggle: 'p' pauses (header shows auto:off), 'p' resumes.
	typeRunes(sim, "p")
	waitFor(t, sim, "auto:off")
	typeRunes(sim, "p")
	waitFor(t, sim, "auto:on")

	// Mute: 'm' opens a confirm modal; confirming mutes and the view reloads
	// with the MUTED column showing "muted" (state is unaffected).
	typeRunes(sim, "m")
	waitFor(t, sim, "Mute monitor")
	press(sim, tcell.KeyRight) // Cancel → Mute
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "muted")

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
	waitFor(t, sim, "<esc>back")        // context hints visible in detail view
	waitFor(t, sim, "metric (last 1h)") // monitor detail shows the metric sparkline
	waitFor(t, sim, "full_object")      // and the on-demand full object below it
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
