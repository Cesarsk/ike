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

	// Settings editor: :settings lists settings; the Theme row is selected on
	// open and enter cycles the theme live (default → mono → nord); esc returns.
	typeCmd(sim, ":settings")
	waitFor(t, sim, "enter cycles") // settings-only text: the page is open
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "theme: mono")
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "theme: nord")
	// ':' must open command mode from the settings page too (not only esc).
	typeCmd(sim, ":monitors")
	waitFor(t, sim, "Monitors(all)")

	// Column picker: C opens it for the current view; ↓ moves to a column and
	// space toggles its visibility; esc applies and returns to the table.
	typeRunes(sim, "C")
	waitFor(t, sim, "[x] STATE") // picker open, STATE shown
	press(sim, tcell.KeyDown)    // → MUTED
	typeRunes(sim, " ")          // hide it
	waitFor(t, sim, "[ ] MUTED")
	typeRunes(sim, " ") // show it again — leave the table as we found it
	waitFor(t, sim, "[x] MUTED")
	press(sim, tcell.KeyEscape)
	waitFor(t, sim, "Monitors(all)")

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

	// Incident commander: I opens the searchable user picker with the acting
	// user pinned on top; enter on the pin = take command, behind a confirm.
	typeRunes(sim, "I")
	waitFor(t, sim, "Commander · IR-142") // picker open
	waitFor(t, sim, "(you)")              // acting user pinned on top
	press(sim, tcell.KeyEnter)            // choose the pinned self
	waitFor(t, sim, "to demo.user?")      // confirm modal (question text wraps across lines)
	press(sim, tcell.KeyRight)            // Cancel → Assign
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "commander → demo.user")

	// Commander to someone else: I → type to search the org (live ListUsers) →
	// choose a different user → confirm → assigned. This is the core new verb.
	typeRunes(sim, "I")
	waitFor(t, sim, "Commander · IR-142")
	waitFor(t, sim, "(you)") // full list rendered (pin present)
	typeRunes(sim, "carol")
	waitForGone(t, sim, "(you)") // filtered on "carol": pin gone, carol is the row
	waitFor(t, sim, "Carol Diaz")
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "to carol?") // confirm modal
	press(sim, tcell.KeyRight)   // Cancel → Assign
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "commander → carol")

	// Incident to-do panel: T opens the panel (listing seeded to-dos); 'a' adds
	// one (content prompt → assignee picker), 'c' toggles complete, 'd' deletes.
	typeRunes(sim, "T")
	waitFor(t, sim, "To-dos · IR-142")
	waitFor(t, sim, "Page the on-call DBA") // a seeded to-do
	typeRunes(sim, "a")
	waitFor(t, sim, "to-do for IR-142") // content prompt
	typeRunes(sim, "failover the primary")
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "Assign to-do · IR-142") // assignee picker
	waitFor(t, sim, "(you)")                 // pinned self rendered before choosing
	press(sim, tcell.KeyEnter)               // assign to self (pinned)
	waitFor(t, sim, "to-do added → @demo.user")
	waitFor(t, sim, "failover the primary") // now listed in the panel
	typeRunes(sim, "c")                     // toggle complete on the highlighted to-do
	waitFor(t, sim, "to-do completed")
	typeRunes(sim, "d") // delete the highlighted to-do
	waitFor(t, sim, "Delete this to-do")
	press(sim, tcell.KeyRight) // Cancel → Delete
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "to-do deleted")
	press(sim, tcell.KeyEscape) // back to incidents
	waitFor(t, sim, "Incidents(all)")

	// Incident detail: the People header resolves commander + responders
	// (read-only) above the raw object — no longer an opaque JSON dump.
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "── people ──")
	waitFor(t, sim, "responders:")
	waitFor(t, sim, "bob") // a demo responder handle
	press(sim, tcell.KeyEscape)
	waitFor(t, sim, "Incidents(all)")

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

	// Services view: APM service list (names; the / query is the env, default
	// prod); enter on a service drills to its traces (services ▸ traces ▸ logs).
	typeCmd(sim, ":services")
	waitFor(t, sim, "Services(prod)") // '/' query = env filter, default prod
	waitFor(t, sim, "kong-proxy")     // sorted; top row
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "Traces(service:kong-proxy")
	press(sim, tcell.KeyEscape)
	waitFor(t, sim, "Services(prod)")

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

	// Saved queries: Q opens the picker; 'a' saves the current query under a
	// typed name; it then appears in the list, and enter applies it.
	typeRunes(sim, "Q")
	waitFor(t, sim, "Saved queries")
	typeRunes(sim, "a")
	waitFor(t, sim, "save query as")
	typeRunes(sim, "kong-errs")
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "saved kong-errs")
	waitFor(t, sim, "kong-errs") // now listed in the picker
	press(sim, tcell.KeyEnter)   // apply the (only) saved query
	waitFor(t, sim, "Logs(service:kong-proxy · 15m)")

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
func TestProjectColumns(t *testing.T) {
	full := []string{"STATE", "MUTED", "NAME", "TYPE", "PRIO", "TAGS"}

	// Subset + reorder, case-insensitive, with indices into the full row.
	names, idx := projectColumns(full, []string{"name", "STATE", "tags"})
	if strings.Join(names, ",") != "NAME,STATE,TAGS" {
		t.Errorf("names = %v", names)
	}
	if len(idx) != 3 || idx[0] != 2 || idx[1] != 0 || idx[2] != 5 {
		t.Errorf("idx = %v, want [2 0 5]", idx)
	}

	// Unknown names are skipped, valid ones kept.
	if names, _ := projectColumns(full, []string{"NOPE", "NAME"}); strings.Join(names, ",") != "NAME" {
		t.Errorf("names = %v, want NAME", names)
	}

	// Empty want → identity (all columns, registry order).
	if names, idx := projectColumns(full, nil); len(names) != len(full) || idx[0] != 0 {
		t.Errorf("identity failed: names=%v idx=%v", names, idx)
	}

	// All-unknown → identity fallback, never a blank table.
	if names, _ := projectColumns(full, []string{"XXX", "YYY"}); len(names) != len(full) {
		t.Errorf("all-unknown should fall back to all columns: %v", names)
	}
}

func TestCommandCompletions(t *testing.T) {
	has := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}
	// Resource keys complete (these already worked).
	if !has(commandCompletions("mon"), "monitors") {
		t.Errorf("mon should offer monitors: %v", commandCompletions("mon"))
	}
	// Pseudo-commands complete (the reported gap).
	if !has(commandCompletions("c"), "ctx") {
		t.Errorf("c should offer ctx: %v", commandCompletions("c"))
	}
	if !has(commandCompletions("se"), "settings") {
		t.Errorf("se should offer settings: %v", commandCompletions("se"))
	}
	// A prefix matching both a resource and a pseudo-command offers both.
	if got := commandCompletions("s"); !has(got, "slos") || !has(got, "settings") {
		t.Errorf("s should offer slos and settings: %v", got)
	}
	if got := commandCompletions("zzz"); len(got) != 0 {
		t.Errorf("no match should be empty: %v", got)
	}
}

func TestInitialResource(t *testing.T) {
	first := data.Resources()[0].Key
	cases := map[string]string{
		"incidents": "incidents", // exact key
		"inc":       "incidents", // alias
		"":          first,       // empty → default
		"bogus":     first,       // unknown → default
	}
	for view, want := range cases {
		if got := initialResource(view).Key; got != want {
			t.Errorf("initialResource(%q) = %q, want %q", view, got, want)
		}
	}
}

// TestSplash: the startup logo shows, then auto-dismisses to the table.
func TestSplash(t *testing.T) {
	sim := newSim(t)
	app := newDemoApp(t)
	app.SetScreen(sim)
	go func() { _ = app.Run() }()

	waitFor(t, sim, "github.com/Cesarsk") // splash creator line
	waitFor(t, sim, "Monitors(all)")      // auto-dismissed → the table
	app.Stop()
}

// TestSessionRestore: switching org + view persists, and a fresh session
// launched from the persisted values reopens on that org + view (not the
// default context + monitors).
func TestSessionRestore(t *testing.T) {
	sites := map[string]string{"demo-dev": "datadoghq.eu", "demo-prod": "datadoghq.com"}
	var savedCtx, savedView string
	mkOpts := func(current, view string) Options {
		return Options{
			Contexts: []ContextInfo{
				{Name: "demo-dev", Site: sites["demo-dev"], Keys: "built-in"},
				{Name: "demo-prod", Site: sites["demo-prod"], Keys: "built-in"},
			},
			Current:     current,
			CurrentView: view,
			Factory:     func(name string) (data.Provider, error) { return data.NewDemo(sites[name]), nil },
			PersistSession: func(c, v string) error {
				savedCtx, savedView = c, v
				return nil
			},
			Refresh: time.Minute,
		}
	}

	// Session 1: start on demo-dev, switch org to demo-prod, then view :incidents.
	sim1 := newSim(t)
	app1, err := New(mkOpts("demo-dev", ""))
	if err != nil {
		t.Fatal(err)
	}
	app1.SetScreen(sim1)
	go func() { _ = app1.Run() }()
	waitFor(t, sim1, "Monitors(all)") // splash cleared → default view
	typeCmd(sim1, ":ctx")
	waitFor(t, sim1, "demo-prod")
	press(sim1, tcell.KeyDown) // demo-dev (active) → demo-prod
	press(sim1, tcell.KeyEnter)
	waitFor(t, sim1, "demo [demo-prod]")
	typeCmd(sim1, ":incidents")
	waitFor(t, sim1, "Incidents(all)")
	app1.Stop()

	if savedCtx != "demo-prod" || savedView != "incidents" {
		t.Fatalf("persisted (%q,%q), want (demo-prod,incidents)", savedCtx, savedView)
	}

	// Session 2: relaunch from the persisted values → reopens there.
	sim2 := newSim(t)
	app2, err := New(mkOpts(savedCtx, savedView))
	if err != nil {
		t.Fatal(err)
	}
	app2.SetScreen(sim2)
	go func() { _ = app2.Run() }()
	waitFor(t, sim2, "demo [demo-prod]") // restored org
	waitFor(t, sim2, "Incidents(all)")   // restored view (not the default monitors)
	app2.Stop()
}

// TestMultiContextSpanning: space in :ctx activates a second org; spanning
// views then show a CTX column with rows from both orgs and per-org budget
// lines; deactivation restores the single-org UI; the activation is persisted
// through the PersistActive callback; row-scoped calls route by Row.Ctx.
func TestMultiContextSpanning(t *testing.T) {
	sites := map[string]string{"demo-dev": "datadoghq.eu", "demo-prod": "datadoghq.com"}
	var persisted []string
	app, err := New(Options{
		Contexts: []ContextInfo{
			{Name: "demo-dev", Site: sites["demo-dev"], Keys: "built-in"},
			{Name: "demo-prod", Site: sites["demo-prod"], Keys: "built-in"},
		},
		Current: "demo-dev",
		Factory: func(name string) (data.Provider, error) { return data.NewDemo(sites[name]), nil },
		PersistActive: func(name string, active bool) error {
			persisted = append(persisted, fmt.Sprintf("%s=%v", name, active))
			return nil
		},
		Refresh: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	sim := newSim(t)
	app.SetScreen(sim)
	go func() { _ = app.Run() }()

	waitFor(t, sim, "Monitors(all)")

	// Activate demo-prod for spanning (space on its row in :ctx).
	typeCmd(sim, ":ctx")
	waitFor(t, sim, "demo-prod")
	press(sim, tcell.KeyDown) // demo-dev → demo-prod
	typeRunes(sim, " ")
	waitFor(t, sim, "demo-prod activated")
	waitFor(t, sim, "●") // activation marker in the :ctx table

	// Monitors now span both orgs: CTX column + rows tagged demo-prod, and
	// the header shows one budget line per org.
	typeCmd(sim, ":monitors")
	waitFor(t, sim, "CTX")
	waitFor(t, sim, "demo-prod")
	waitFor(t, sim, "demo-dev:") // per-org budget line

	// Incidents span too (both orgs ship IR-142 in demo data).
	typeCmd(sim, ":incidents")
	waitFor(t, sim, "CTX")
	waitFor(t, sim, "IR-142")

	// :overview merges open incidents + alerting monitors across both orgs,
	// worst first (a SEV-1 incident above monitor alerts), CTX column shown.
	typeCmd(sim, ":overview")
	waitFor(t, sim, "Overview(")
	waitFor(t, sim, "incident")
	waitFor(t, sim, "monitor")
	waitFor(t, sim, "CTX")
	// enter on the top row (an incident) opens the real incident detail with
	// the People header — the overview row resolved to its underlying kind.
	press(sim, tcell.KeyEnter)
	waitFor(t, sim, "── people ──")
	press(sim, tcell.KeyEscape)
	waitFor(t, sim, "Overview(")

	// Deactivate → single-org UI back (no CTX column).
	typeCmd(sim, ":ctx")
	waitFor(t, sim, "demo-prod")
	press(sim, tcell.KeyDown)
	typeRunes(sim, " ")
	waitFor(t, sim, "demo-prod deactivated")
	typeCmd(sim, ":monitors")
	waitFor(t, sim, "Monitors(all)")
	waitForGone(t, sim, "CTX")

	if len(persisted) != 2 || persisted[0] != "demo-prod=true" || persisted[1] != "demo-prod=false" {
		t.Errorf("persisted activations = %v, want [demo-prod=true demo-prod=false]", persisted)
	}
	app.Stop()
}

// TestProviderRouting: rows carry their origin context and providerFor routes
// to that org's provider, falling back to the current one.
func TestProviderRouting(t *testing.T) {
	dev := data.NewCached(data.NewDemo("datadoghq.eu"))
	prod := data.NewCached(data.NewDemo("datadoghq.com"))
	a := &App{provider: dev, current: "dev", providers: map[string]*data.Cached{"dev": dev, "prod": prod}}
	if got := a.providerFor(data.Row{Ctx: "prod"}); got != prod {
		t.Errorf("prod row routed to %v", got)
	}
	if got := a.providerFor(data.Row{Ctx: "dev"}); got != dev {
		t.Errorf("dev row routed to %v", got)
	}
	if got := a.providerFor(data.Row{}); got != dev {
		t.Errorf("untagged row should route to current")
	}
	if got := a.providerFor(data.Row{Ctx: "gone"}); got != dev {
		t.Errorf("unknown ctx should fall back to current")
	}
}

func newSim(t *testing.T) tcell.SimulationScreen {
	t.Helper()
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	sim.SetSize(140, 35)
	return sim
}

func newDemoApp(t *testing.T) *App {
	t.Helper()
	sites := map[string]string{"demo-dev": "datadoghq.eu", "demo-prod": "datadoghq.com"}
	savedQ := map[string][]SavedQuery{}
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
		SavedQueries: func(ctxName string) []SavedQuery { return savedQ[ctxName] },
		SaveQuery: func(ctxName, name, view, query string) error {
			savedQ[ctxName] = append(savedQ[ctxName], SavedQuery{Name: name, View: view, Query: query})
			return nil
		},
		DeleteQuery: func(ctxName, name, view string) error {
			out := savedQ[ctxName][:0]
			for _, q := range savedQ[ctxName] {
				if q.Name != name || q.View != view {
					out = append(out, q)
				}
			}
			savedQ[ctxName] = out
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

// waitForGone waits until a substring is absent — used to confirm an async
// state change landed (e.g. the picker's pinned "(you)" row disappearing once
// a search filter applies) without racing an early positive match.
func waitForGone(t *testing.T, sim tcell.SimulationScreen, gone string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !strings.Contains(screenText(sim), gone) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("screen still showed %q; screen was:\n%s", gone, screenText(sim))
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
