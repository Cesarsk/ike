package ui

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/rivo/tview"
)

// setHints shows the shortcuts valid in the current context, k9s-style.
func (a *App) setHints() {
	var lines []string
	switch a.page {
	case "detail":
		lines = []string{
			"[aqua]<esc>[white]back  [aqua]<o>[white]open in Datadog  [aqua]<c>[white]copy  [aqua]<?>[white]help",
			"[aqua]<↑/↓ j/k>[white]scroll  [aqua]<q>[white]back",
		}
	case "dashboard":
		lines = []string{
			"[aqua]<esc>[white]back  [aqua]<ctrl-r>[white]refresh sparklines  [aqua]<o>[white]open in Datadog",
			"[aqua]<↑/↓ j/k>[white]scroll  [aqua]<?>[white]help  [aqua]<q>[white]back",
		}
	case "trace":
		lines = []string{
			"[aqua]<esc>[white]back  [aqua]<l>[white]logs for this trace  [aqua]<o>[white]open in Datadog",
			"[aqua]<↑/↓ j/k>[white]scroll  [aqua]<?>[white]help  [aqua]<q>[white]back",
		}
	case "patterns":
		lines = []string{
			"[aqua]<esc>[white]back to logs  [aqua]<↑/↓ j/k>[white]scroll  [aqua]<?>[white]help",
		}
	case "cost":
		lines = []string{
			"[aqua]<1/3/6/y>[white]range  [aqua]<[/]>[white]month  [aqua]</>[white]filter  [aqua]<s>[white]sub-orgs  [aqua]<f>[white]focus org",
			"[aqua]<enter>[white]product history  [aqua]<o>[white]open  [aqua]<ctrl-r>[white]refresh  [aqua]<esc>[white]back  [aqua]<?>[white]help",
		}
	case "costprod":
		lines = []string{
			"[aqua]<↑/↓ j/k>[white]scroll  [aqua]<esc>[white]back to the breakdown  [aqua]<?>[white]help",
		}
	case "oncall":
		lines = []string{
			"[aqua]<p>[white]page  [aqua]<a>[white]ack  [aqua]<e>[white]escalate  [aqua]<r>[white]resolve  [aqua]<o>[white]open",
			"[aqua]<ctrl-r>[white]refresh  [aqua]<↑/↓ j/k>[white]scroll  [aqua]<esc>[white]back  [aqua]<?>[white]help",
		}
	case "teammembers":
		lines = []string{
			"[aqua]<o>[white]open  [aqua]<ctrl-r>[white]refresh  [aqua]<↑/↓ j/k>[white]scroll  [aqua]<esc>[white]back  [aqua]<?>[white]help",
		}
	case "notebook":
		lines = []string{
			"[aqua]<o>[white]open  [aqua]<ctrl-r>[white]refresh  [aqua]<↑/↓ j/k>[white]scroll  [aqua]<esc>[white]back  [aqua]<?>[white]help",
		}
	case "logcontext":
		lines = []string{
			"[aqua]<↑/↓ j/k>[white]move  [aqua]<enter>[white]expand  [aqua]<t>[white]trace  [aqua]<esc>[white]back to logs  [aqua]<?>[white]help",
		}
	case "help":
		lines = []string{
			"[aqua]<esc>[white]back  [aqua]<q>[white]back",
		}
	case "ctxform":
		lines = []string{
			"[aqua]<tab>[white]next field  [aqua]<shift-tab>[white]previous",
			"[aqua]<esc>[white]cancel  [aqua]<enter>[white]on Save to store",
		}
	case "userpick":
		lines = []string{
			"[aqua]<type>[white]search users (from Datadog)  [aqua]<↑/↓>[white]select",
			"[aqua]<enter>[white]choose  [aqua]<esc>[white]cancel",
		}
	case "todos":
		lines = []string{
			"[aqua]<a>[white]add  [aqua]<c/space/enter>[white]toggle done  [aqua]<d>[white]delete",
			"[aqua]<↑/↓ j/k>[white]move  [aqua]<esc>[white]back  [aqua]<?>[white]help",
		}
	default:
		refresh := "on"
		if a.paused {
			refresh = "off"
		}
		lines = []string{
			"[aqua]<:>[white]cmd  [aqua]</>[white]filter  [aqua]<enter>[white]details  [aqua]<o>[white]open  [aqua]<c>[white]copy  [aqua]<C>[white]cols",
			fmt.Sprintf("[aqua]<ctrl-r>[white]refresh  [aqua]<p>[white]auto:%s  [aqua]<esc>[white]back  [aqua]<?>[white]help  [aqua]<q>[white]quit", refresh),
			"",
			"[orange]:monitors :incidents :slos :logs :traces :cost :oncall :teams …[-]  [aqua]:menu[-] all commands",
		}
		switch a.res.Key {
		case "monitors":
			lines = append(lines, "[gray]<l>logs  <m>mute (<space>mark → mute many)  <s>sort <S>rev   quick: <1>alert <2>warn <3>nodata <4>ok <0>all")
		case "slos":
			lines = append(lines, "[gray]<enter>error budget  <t>cycle type filter  <s>sort <S>reverse")
		case "incidents":
			lines = append(lines, "[gray]<r>state (<space>mark → resolve many)  <v>severity  <I>commander (pick)  <T>to-dos  quick: <1>active <2>stable <3>resolved <0>all")
		case "downtimes":
			lines = append(lines, "[gray]<x>cancel (<space>mark → cancel many)  <s>sort <S>reverse")
		case "logs":
			lines = append(lines, "[gray]</>query (tab=complete, ↑ history)  <t>trace  <x>context  <P>patterns  <Q>saved  window: <1>15m..<5>7d")
		case "traces":
			lines = append(lines, "[gray]</>query  <t>trace waterfall  <l>logs for trace  <Q>saved  window: <1>15m..<5>7d")
		case "services":
			lines = append(lines, "[gray]<enter>traces for service  </>env (default prod)  <s>sort <S>reverse")
		case "events":
			lines = append(lines, "[gray]</>query  <Q>saved  window: <1>15m..<5>7d  <s>sort   (deploys, alerts, changes)")
		case "rum":
			lines = append(lines, "[gray]</>RUM query (e.g. @type:error)  window: <1>15m..<5>7d  <s>sort")
		case "synthetics":
			lines = append(lines, "[gray]<enter>latest results + pass rate  <s>sort <S>reverse")
		case "security":
			lines = append(lines, "[gray]</>signals query  <enter>detail  <s>sort   (Cloud SIEM / CSM · last 24h)")
		case "notebooks":
			lines = append(lines, "[gray]<enter>read the notebook  <s>sort <S>reverse   (runbooks, postmortems)")
		case "hosts":
			lines = append(lines, "[gray]<m>mute/unmute host  <o>open  <s>sort <S>reverse   (down/muted first)")
		case "containers":
			lines = append(lines, "[gray]</>tag filter (kube_namespace:… cluster:…)  <l>logs  <C>columns (+ns/cluster)  <enter>detail  <o>open")
		case overviewResource.Key:
			lines = append(lines, "[gray]<enter>detail  open incidents + alerting monitors across every active org")
		case ctxResource.Key:
			lines = append(lines, "[gray]<enter>switch org  <space>toggle active (all active orgs merge in views)  <O>browser sign-in  <a>add  <e>edit  <d>delete")
		default:
			lines = append(lines, "[gray]<s>sort <S>reverse")
		}
	}
	a.hintTV.SetText(a.theme.recolor(strings.Join(lines, "\n")))
}

func (a *App) buildHelp() tview.Primitive {
	tv := tview.NewTextView().SetDynamicColors(true)
	tv.SetBorder(true).SetTitle(" Help ").SetTitleColor(a.theme.Title)
	fmt.Fprint(tv, a.theme.recolor(`
 [orange]NAVIGATION
   [aqua]:menu[white]         the full command list (aliases too) — enter runs a command
   [aqua]:<resource>[white]   switch view: monitors incidents slos logs traces services
                 events rum synthetics downtimes dashboards hosts containers
                 teams oncall
                 (aliases: mon inc s l tr svc ev dt d syn)
                 :overview (cross-org triage), :cost (Datadog spend),
                 :oncall (who's on call + paging), :teams (members), :ctx, :settings
   [aqua]enter[white]         detail — full object on demand; SLO error budget; monitor metric
                 sparkline; on a dashboard its widget grid; on logs/traces a row
   [aqua]esc[white]           go back (navigation history, k9s-style); clears the active filter
   [aqua]↑/↓ j/k[white]       move selection / scroll (↑/↓ in the / prompt = query history)
   [aqua]o[white]             open the selected item in the Datadog web UI (works in detail too)

 [orange]SEARCH, SORT & FILTER
   [aqua]/<text>[white]       filter rows; in Logs/Traces/Events it is a Datadog query (server-side,
                 tab-completes facets/values; ↑ recalls previous queries)
   [aqua]s / S[white]         cycle the sort column / reverse the direction (any table)
   [aqua]0-4[white]           quick filter by status — monitors: alert/warn/nodata/ok/all;
                 incidents: active/stable/resolved/all
   [aqua]1-5[white]           (logs/traces/events) time window: 15m / 1h / 4h / 1d / 7d
   [aqua]t[white]             (SLOs) cycle the Type filter: metric / monitor / time_slice / all
   [aqua]P[white]             (logs) cluster the loaded lines into patterns — flood triage
   [aqua]Q[white]             (logs/traces/events) saved-query picker — [aqua]enter[white] apply, [aqua]a[white] save, [aqua]d[white] delete
   [aqua]F[white]             (any table) fuzzy row finder — type a subsequence, [aqua]enter[white] jumps to the row

 [orange]CORRELATION (the debugging loop)
   [aqua]enter[white]         (service) → its traces (service:<name>) — services ▸ traces ▸ logs
   [aqua]l[white]             drill to logs — (monitor) its log query; (trace) that trace's logs
   [aqua]t[white]             drill to the trace waterfall — (logs/traces) the row's trace_id;
                 needs APM log-injection, else a clear "no trace_id"
   [aqua]x[white]             (logs) surrounding context — a ±5m window around the line, same
                 service/host, oldest first (one query, not a live stream)

 [orange]ACTIONS
   [aqua]space[white]         mark rows for a bulk action; then <m>/<r>/<x> act on all
                 marked at once behind one confirm (mute monitors, resolve
                 incidents, cancel downtimes). esc clears the selection
   [aqua]m[white]             (monitor) mute / unmute — behind a confirmation
                 (host) mute / unmute the selected host — behind a confirmation
   [aqua]r[white]             (incident) change state (active/stable/resolved) — behind a confirm
   [aqua]v[white]             (incident) change severity (SEV-1…SEV-5) — behind a confirm
   [aqua]I[white]             (incident) assign commander — searchable user picker (you pinned
                 on top: enter = take command), behind a confirm
   [aqua]T[white]             (incident) to-do panel — list / add (assign to anyone) / toggle
                 done / delete action items
   [gray]              (incident) commander & responders show in the detail (enter) — responders
                 are read-only; the API has no write path for them
   [aqua]x[white]             (downtime) cancel the selected downtime — behind a confirm
   [aqua]c[white]             copy the row's URL / query / id to the clipboard
   [aqua]ctrl-r[white]        force refresh (bypasses cache — spends API budget)
   [aqua]p[white]             pause / resume auto-refresh (header shows auto:on/off)

 [orange]CONTEXTS (:ctx)
   [aqua]enter[white]         switch to an org (cache, budget and history reset). Orgs marked
                 active with space stay in; anything else drops out
   [aqua]space[white]         toggle an org active — every "active" row merges into the views
                 (CTX column names each row's org); actions on a row always hit
                 that row's org. On the org you're driving, space hands the
                 driver role to another active org and drops it; dropping your
                 last active org leaves no context selected and gates the other
                 views until you pick one. The org you're driving shows in the header
   [aqua]O[white]             browser sign-in (OAuth) for the selected context — tokens go to the OS
                 keychain and refresh automatically. On an OAuth row it signs in or
                 refreshes; on a key/token row it offers to convert it (asks first)
   [aqua]a[white]             add a context — pick its auth (browser sign-in, API/APP keys, or
                 access token); the form's fields follow the choice
   [aqua]e[white]             edit the selected context in a form (auth type, site, subdomain,
                 credentials) — leave a secret field empty to keep the stored one
   [aqua]d[white] (or ctrl-d) delete the selected context (asks first)

 [orange]OTHER
   [aqua]C[white]             (any table) column picker — [aqua]space[white] show/hide, [aqua]J/K[white] reorder; live + saved
   [aqua]:watch[white]        hands-off refresh mode — keeps the current view updating on its
                 cadence for a wall display (respects pause); header shows ● WATCH.
                 Query views (logs/traces/rum) aren't auto-refetched (budget)
   [aqua]:settings[white]     theme and per-view cache TTLs — applies live + saved to config
   [aqua]?[white]             this help (from any view)
   [aqua]:manual[white]       the getting-started page (shown once on first run)
   [aqua]q[white]             back in detail/help; quit from a table view
   [aqua]ctrl-c[white]        quit — press twice to confirm (also :q :quit :exit)

 [gray]Views auto-refresh only where cheap (monitors, incidents), else cached per TTL.
 [gray]The Budget header shows Datadog X-RateLimit headroom; a 429 auto-pauses refresh.
`))
	return tv
}

// buildIntro is the getting-started page: shown once on first run and
// reopenable any time with :manual (or :instructions). Deliberately shorter
// and more task-oriented than the full help (?).
func (a *App) buildIntro() tview.Primitive {
	tv := tview.NewTextView().SetDynamicColors(true)
	tv.SetBorder(true).SetTitle(" Getting started ").SetTitleColor(a.theme.Title)
	fmt.Fprint(tv, a.theme.recolor(`
 [orange]ike — keep an eye on your Datadog, k9s-style.

 [orange]1 · Connect an org[white]
   [aqua]:ctx[white] then [aqua]a[white] adds a context. Pick the auth type — [aqua]Browser sign-in
   (OAuth)[white] is the easy path: no keys to paste, tokens refresh themselves.
   [aqua]O[white] on a row signs in again whenever needed.

 [orange]2 · Look around[white]
   [aqua]:monitors :incidents :slos :logs :traces :services :events :rum
   :synthetics :downtimes :dashboards[white] switch views; [aqua]:overview[white] is
   cross-org triage. [aqua]enter[white] drills into a row, [aqua]esc[white] goes back,
   [aqua]o[white] opens the row in the Datadog web UI.

 [orange]3 · Filter and find[white]
   [aqua]/[white] filters the table — in logs, traces and events it is a full
   Datadog query (server-side). [aqua]F[white] fuzzy-finds in the current view.

 [orange]4 · Watch several orgs at once[white]
   In [aqua]:ctx[white], [aqua]space[white] marks orgs active — active rows highlight and every
   view merges them (the CTX column names each row's org). [aqua]enter[white]
   switches org: marked orgs stay in, everything else drops out.

 [orange]5 · Act — every write asks first[white]
   [aqua]m[white] mute monitor · [aqua]r[white]/[aqua]v[white] incident state/severity · [aqua]I[white] commander ·
   [aqua]T[white] to-dos · [aqua]x[white] cancel downtime

 [gray]? opens the full key reference · :manual reopens this page any time.
 [gray]No credentials yet? Quit and run ike --demo to explore with fake data.
 [gray]<esc> starts you off.`))
	return tv
}

// showIntro opens the getting-started page and, on a first run, persists that
// it was shown so the next session starts normally.
func (a *App) showIntro() {
	if a.page == "intro" {
		return
	}
	a.pushNav()
	a.showPage("intro")
	if a.opts.FirstRun && !a.introMarked {
		a.introMarked = true
		if a.opts.MarkIntroSeen != nil {
			if err := a.opts.MarkIntroSeen(); err != nil {
				slog.Warn("persist intro-seen failed", "err", err)
			}
		}
	}
}

// ---- input ----------------------------------------------------------------

// showHelp opens the help page; esc pops back to wherever the user came from.
func (a *App) showHelp() {
	if a.page == "help" {
		return
	}
	a.pushNav()
	a.showPage("help")
}
