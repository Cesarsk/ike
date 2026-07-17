# ike — User Manual

A task-oriented guide to driving ike, a k9s-style terminal UI for Datadog. If
you already know k9s, you already know most of this: `:` switches views, `/`
filters, `enter` drills in, `esc` goes back. The rest is below.

- New here? Start with [First run](#first-run).
- Want the one-page cheat sheet? Jump to [Keybinding reference](#keybinding-reference).
- Curious *why* it works the way it does? See [docs/DESIGN.md](DESIGN.md).

---

## Contents

1. [Install](#install)
2. [First run](#first-run)
3. [Reading the screen](#reading-the-screen)
4. [Navigation basics](#navigation-basics)
5. [The views](#the-views)
6. [The debugging loop](#the-debugging-loop-monitor--logs--trace--logs)
7. [Working with logs](#working-with-logs)
8. [Actions (writes)](#actions-writes)
9. [Multiple orgs: contexts & auth](#multiple-orgs-contexts--auth)
10. [Rate limits](#rate-limits)
11. [Keybinding reference](#keybinding-reference)
12. [Config file reference](#config-file-reference)
13. [Command-line flags](#command-line-flags)
14. [Troubleshooting](#troubleshooting)

---

## Install

```sh
brew install cesarsk/tap/ike
```

Or grab a prebuilt binary from the [latest release](https://github.com/Cesarsk/ike/releases/latest),
or build from source with `go install github.com/Cesarsk/ike@latest`. See the
[README](../README.md#install) for all options.

---

## First run

**No credentials? Explore with demo data.** This ships two fake orgs and never
touches the network — the fastest way to learn the keys:

```sh
ike --demo
```

**Live mode, single org.** ike reads the same environment variables as
dogshell and the Datadog Terraform provider:

```sh
export DD_API_KEY=...  DD_APP_KEY=...  DD_SITE=datadoghq.eu
ike
```

Those three vars form an implicit `default` context. To manage **several**
orgs, see [contexts](#multiple-orgs-contexts--auth).

> ike needs a read-scoped API + application key pair (or a bearer token). The
> app never writes anything without a confirmation prompt (see
> [Actions](#actions-writes)).

---

## Reading the screen

```
 Mode:   live [prod]              <:>cmd  </>filter  <enter>details  <o>open in
 Site:   datadoghq.eu                                                  Datadog
 View:   Monitors                   <ctrl-r>refresh  <esc>back  <?>help  <q>quit
 Age:    12s (ttl 30s)
 Budget: monitors 973/1000       :monitors  :incidents  :slos  :logs  :dashboards
╔══════════════════════════════ Monitors(all)[18] ═════════════════════════════╗
║STATE   MUTED  NAME                        TYPE          PRIO TAGS             ║
║Alert          Node not ready in prod      service check P1   team:sre …      ║
╚═══════════════════════════════════════════════════════════════════════════════╝
```

The header is your situational awareness:

| Field | Meaning |
|-------|---------|
| **Mode** | `demo`, or `live [context-name]` — which org you're pointed at. |
| **Site** | The Datadog site (`datadoghq.eu`, `datadoghq.com`, …). |
| **View** | The current resource, plus any active filter/sort in the table title (`Monitors(state:Alert)`, `↕NAME▲`). |
| **Age** | How stale the shown data is, and the cache TTL for this view. |
| **Budget** | Live API rate-limit headroom from Datadog's own `X-RateLimit-*` headers, colour-coded (green healthy → red nearly spent). See [Rate limits](#rate-limits). |

The bottom line of the header shows context-sensitive hints for the current
view; the row count is in the table title (`[18]`).

---

## Navigation basics

| Key | Action |
|-----|--------|
| `:` | **command mode** — type a resource name/alias (`:mon`, `:logs`) and `enter`. |
| `/` | **filter** the rows. In Logs/Traces/Events this is a real Datadog query sent to the API; elsewhere it filters the loaded rows. |
| `enter` | **drill in** — the detail view, or a view-specific action (an SLO's error budget, a dashboard's widget grid). |
| `esc` | **back** — first press clears an active filter; next press pops the navigation history (k9s-style: view, filter and selection are restored). |
| `j`/`k` or `↑`/`↓` | move the selection / scroll the detail view. |
| `o` | open the selected item in the Datadog **web UI** (works in the detail view too). |
| `?` | help overlay, from any view. |
| `q` | back out of detail/help; from a table view, quit. |
| `ctrl-c` | quit — press **twice** within ~2s to confirm (`:q`, `:quit`, `:exit` also quit). |

---

## The views

Switch to any view with `:` + its name or a shorter alias.

| View | Command / aliases | Shows |
|------|-------------------|-------|
| **Monitors** | `:monitors` `:mon` `:m` | Alert state, mute status, name, type, priority, tags. Auto-refreshes. |
| **Incidents** | `:incidents` `:inc` `:i` | ID, severity, state, title, customer impact, created. Auto-refreshes. |
| **SLOs** | `:slos` `:slo` `:s` | Name, type, target, timeframe, tags. |
| **Logs** | `:logs` `:log` `:l` | Time, status, service, host, message — server-side search. |
| **Traces** | `:traces` `:tr` `:apm` `:spans` | APM spans: time, service, resource, duration, error, trace id. |
| **Services** | `:services` `:svc` | Your APM services for an env (`/` sets the env, default `prod`); `enter` → that service's traces. |
| **Events** | `:events` `:ev` | The change stream: deploys, alerts, config changes. |
| **Downtimes** | `:downtimes` `:dt` `:mutes` | Scheduled/active monitor mutes: status, scope, message, created. |
| **Dashboards** | `:dashboards` `:dash` `:d` | Title, layout, author, modified. |
| **Contexts** | `:ctx` | Your Datadog orgs — switch, add, edit, delete (see [contexts](#multiple-orgs-contexts--auth)). |
| **Settings** | `:settings` | Theme, per-view cache TTLs and columns — edited live (see [settings](#settings-view)). |

### Per-view keys

- **Monitors** — `0`–`4` quick-filter by state (alert / warn / nodata / ok / all);
  `l` → its logs; `m` → mute/unmute.
- **Incidents** — `0`–`3` quick-filter by state (active / stable / resolved / all);
  `r` → change state; `v` → change severity; `I` → assign commander (searchable
  user picker, yourself pinned on top so `enter` = take command); `T` → to-do
  panel (list / add / complete / delete, assign to anyone). `enter` opens the
  detail, which shows a **People** header (commander + responders — responders
  are read-only) above the object.
- **SLOs** — `enter` shows live **attainment + error budget**; `t` cycles the
  type filter (metric / monitor / time_slice / all).
- **Logs / Traces / Events** — `/` is a Datadog query; `1`–`5` set the time
  window (15m / 1h / 4h / 1d / 7d). Logs also: `P` patterns, `t` → trace.
- **Services** — lists your APM services for an environment; `/` sets the env
  (default `prod`), `enter` → that service's traces (`service:<name>`). Names
  only: Datadog's official API doesn't expose per-service request/error/latency
  stats to third-party clients. The list comes from the service catalog (trace
  stats), so it's populated even when span retention is tight — unlike a raw
  span search.
- **Dashboards** — `enter` renders the widgets as a **grid of sparklines**
  matching the Datadog layout; `ctrl-r` re-fetches.
- **Downtimes** — `x` cancels the selected downtime.
- Any table — `s` cycles the sort column, `S` reverses it; `C` opens the
  **column picker** (`space` show/hide, `J`/`K` reorder — live + saved).

---

## The debugging loop (monitor → logs → trace → logs)

This is what ike is *for* — the loop your on-call actually runs, without a
browser tab:

1. A **monitor** fires. Select it, press **`l`** → its logs (the monitor's own
   log query, or its `service:`/`env:` tags).
2. Find the failing request. Press **`t`** → the **trace waterfall**: every
   service hop as a proportional duration bar, with the erroring span marked.
3. From the trace, press **`l`** → all of that trace's **logs across every
   service**, in one chronological timeline below the waterfall.

The three views interconnect by `trace_id`. The jump needs APM log-injection
(the `trace_id` present on your logs); a log without one gives you a clear "no
trace_id" message rather than a broken jump.

You can also enter the loop from the top: **`:services`** → `enter` on a
service → its **traces** → a failing trace → its **logs**. Same loop, started
from the service list instead of a monitor.

---

## Working with logs

Logs is the richest view. `/` opens a query prompt that talks to the Datadog
logs search API directly:

- **Autocomplete** (zero extra API calls): as you type, ike suggests facet keys
  (`service:`, `status:`, `@http.status_code`, …), operators (`AND`/`OR`/`NOT`),
  and **values it has already seen in the loaded rows** (e.g. after `service:`
  it offers the services in the current results). `tab`/`enter` accepts a
  suggestion, then keep typing; a second `enter` submits.
- **Time window** — `1`–`5` = 15m / 1h / 4h / 1d / 7d (shown in the title).
- **Patterns** — `P` clusters the loaded lines into templates (numbers, IDs,
  UUIDs, hex, quoted strings normalised out) so a flood collapses into a handful
  of shapes. Zero extra API calls — it clusters what's already on screen.
- **Query history** — in the `/` prompt, `↑`/`↓` recall previous queries for
  this view. `ctrl-u` clears the line.
- **Saved queries** — `Q` opens a per-context picker of bookmarked queries for
  the current view: `enter` applies one, `a` saves the *active* query under a
  name you type, `d` deletes the highlighted one. Saved queries persist to the
  config file, scoped to the org (a query only makes sense against the org
  whose services/tags it references). Works in Logs, Traces and Events.

---

## Actions (writes)

ike is read-mostly. Every write is behind a **confirmation modal** — nothing
leaves your keyboard without a second keypress.

| Key | View | Action |
|-----|------|--------|
| `r` | Incidents | Change state (active / stable / resolved). |
| `v` | Incidents | Change severity (SEV-1 … SEV-5). |
| `I` | Incidents | Assign commander — searchable user picker (yourself pinned on top: `enter` = take command), then a confirm. |
| `T` | Incidents | To-do panel — `a` add (with assignee picker), `c`/`space` toggle complete, `d` delete (delete is confirm-gated). |
| `m` | Monitors | Mute / unmute (toggles from current state; edits only the `silenced` option, nothing else). |
| `x` | Downtimes | Cancel the selected downtime. |
| `c` | any | **Copy** the row's web URL (or log query / id) to the clipboard — not a write, no prompt. |
| `o` | any | Open the row in the Datadog web UI. |

> Testing writes? Use a **dev/non-prod org** and restore what you change. Mute
> shows in the Monitors **MUTED** column, independent of the alert state.

---

## Multiple orgs: contexts & auth

Most companies run several Datadog orgs (dev/stage/prod, org-per-team, …). ike
models them as **contexts**, kubeconfig-style, in `~/.config/ike/config.yaml`
(override with `$IKE_CONFIG`). Secrets **never** live in that file — it names
the *env vars* that hold them, or defers to the OS keychain.

Inside the app, `:ctx` lists your orgs:

| Key | Action |
|-----|--------|
| `enter` | **Switch** org. A hard boundary: cache, rate-limit budget and navigation history are all dropped — nothing leaks between orgs. |
| `a` | **Add** a context via a form (name, site, then API/APP keys *or* a bearer token — all masked). Secrets go to the **OS keychain**; only `{site, keychain: true}` is written to the config. |
| `e` | **Edit** the config file in `$EDITOR` (vi by default); on exit it's reloaded and re-validated. |
| `ctrl-d` | **Delete** the selected context (with confirmation; the active one is protected). |

**Auth options per context:**
- **Key pair** — point `api-key-env` / `app-key-env` at env vars (populated by
  direnv, 1Password CLI, etc.).
- **Token** — set `token-env` (or keychain token auth); ike sends it as
  `Authorization: Bearer`. Handy for pup-issued access tokens, but those expire
  ~1h, so rotation is manual for now.
- **Keychain** — the in-app `a` form stores secrets in the macOS Keychain /
  Linux Secret Service under the service name `ike`.

Plaintext `api-key:` values in the config file are **rejected at parse time** —
dotfiles get committed, so ike won't let you put a secret there.

**Which context at startup:** `--context` flag → `$IKE_CONTEXT` → the config's
`current-context`.

---

## Rate limits

Datadog's API is rate-limited **per organization** (log search is ~300
requests/hour, for example). Unlike Kubernetes, you can't poll it every two
seconds — so ike is built around the limit, not in spite of it:

- Every view is **cached** with a per-resource TTL; navigating between views is
  free.
- Only cheap views (monitors, incidents) **auto-refresh**. `p` pauses/resumes
  it at runtime (the header shows `auto:on`/`off`).
- **`ctrl-r`** is the explicit "spend budget now" refresh (bypasses the cache).
- The **Budget** header shows live headroom from Datadog's `X-RateLimit-*`
  response headers. A `429` auto-pauses auto-refresh so ike backs off.

If a fetch fails but ike has a cached copy, it serves the stale rows and shows
the error rather than blanking mid-incident.

---

## Keybinding reference

**Global**

| Key | Action |
|-----|--------|
| `:` | command mode (resource name/alias, or `:ctx`, `:q`) |
| `/` | filter / query |
| `enter` | detail / drill-in |
| `esc` | clear filter, then back through history |
| `j` `k` / `↑` `↓` | move selection / scroll |
| `o` | open in Datadog web UI |
| `c` | copy URL / query / id |
| `C` | column picker (show/hide + reorder, live + saved) |
| `s` / `S` | sort column / reverse |
| `ctrl-r` | force refresh (spends budget) |
| `p` | pause / resume auto-refresh |
| `?` | help |
| `q` | back; quit from a table |
| `ctrl-c` | quit (press twice) |

**View-specific**

| Key | Where | Action |
|-----|-------|--------|
| `0`–`4` | Monitors | state quick filter (alert/warn/nodata/ok/all) |
| `0`–`3` | Incidents | state quick filter (active/stable/resolved/all) |
| `1`–`5` | Logs/Traces/Events | time window (15m/1h/4h/1d/7d) |
| `l` | Monitors, Traces | drill to logs |
| `t` | Logs, Traces | drill to trace waterfall |
| `t` | SLOs | cycle type filter |
| `P` | Logs | cluster into patterns |
| `Q` | Logs/Traces/Events | saved-query picker (enter apply · `a` save · `d` delete) |
| `r` | Incidents | change state |
| `v` | Incidents | change severity |
| `I` | Incidents | assign commander (user picker; you pinned on top) |
| `T` | Incidents | to-do panel (list · `a` add · `c` done · `d` delete) |
| `m` | Monitors | mute / unmute |
| `x` | Downtimes | cancel downtime |
| `↑` `↓` | `/` prompt | query history |

**Contexts (`:ctx`)** — `enter` switch · `a` add · `e` edit config · `ctrl-d` delete.

---

## Settings view

`:settings` opens an in-app editor for global customizations. Every change
**applies live and is saved back to the config** — no restart:

- **Theme** — `enter` on the Theme row cycles `default → mono → nord →
  solarized`; the whole UI recolours immediately.
- **TTL · `<view>`** — `enter` prompts for a cache TTL (a Go duration like
  `120s`; blank clears back to the built-in default).

`esc` returns to where you were. These settings can still be hand-edited in the
config file (below).

**Columns** are customized where you use them, not here: press **`C`** on any
table to open the column picker for that view — `space` shows/hides the
highlighted column, `J`/`K` reorder it, `esc` applies and saves. It's live and
persisted to the config's `columns:`; at least one column always stays visible.
Saved queries likewise have their own per-view `Q` picker.

---

## Config file reference

`~/.config/ike/config.yaml` (or `$IKE_CONFIG`):

```yaml
current-context: dev
refresh-interval: 30s          # optional; overrides the 30s default
ttl-overrides:                 # optional; per-view cache TTL (Go durations)
  logs: 120s                   # trade freshness against the API rate limit
  monitors: 15s
columns:                       # optional; choose/reorder columns per view
  monitors: [STATE, NAME, TAGS]
  logs: [TIME, SERVICE, MESSAGE]
theme: default                 # optional; default | mono | nord | solarized
contexts:
  dev:
    site: datadoghq.eu
    subdomain: acme-dev          # optional: only if your web UI is at
                                 # https://acme-dev.datadoghq.eu — fixes the
                                 # 'open in Datadog' links; the API is unaffected
    api-key-env: IKE_DEV_API_KEY # name of the env var holding the key —
    app-key-env: IKE_DEV_APP_KEY # secrets NEVER go in this file
    saved-queries:               # bookmarked queries (managed in-app via 'Q')
      - {name: errors, view: logs, query: "status:error"}
      - {name: payments, view: traces, query: "service:payments-api"}
  prod:
    site: datadoghq.com
    keychain: true               # secrets stored in the OS keychain (via `:ctx` → a)
    auth: token                  # "" (key pair, default) or "token"
  staging:
    site: datadoghq.eu
    token-env: IKE_STAGING_TOKEN # bearer/access token instead of a key pair
```

| Field | Meaning |
|-------|---------|
| `current-context` | Which context to start on (unless overridden by flag/env). |
| `refresh-interval` | Auto-refresh cadence, e.g. `45s`, `0` to disable. |
| `ttl-overrides.<view>` | Custom cache TTL per view (`logs`, `monitors`, …), Go duration; overrides the built-in default. |
| `columns.<view>` | Column subset/order to display for a view, by name (below). Normally set via the in-app `C` picker; hand-editable too. Unknown names ignored; empty/all-unknown shows all. Display-only — sort/filter still see every column. |
| `theme` | TUI colour palette: `default`, `mono`, `nord`, or `solarized`. Recolours the chrome (borders, titles, selection, accents) — status colours (alert red, ok green) are never themed. |

**Available column names per view** (for `columns:`):

| View | Columns |
|------|---------|
| `monitors` | STATE, MUTED, NAME, TYPE, PRIO, TAGS |
| `incidents` | ID, SEV, STATE, TITLE, IMPACT, CREATED |
| `slos` | NAME, TYPE, TARGET, TIMEFRAME, TAGS |
| `logs` | TIME, STATUS, SERVICE, HOST, MESSAGE |
| `traces` | TIME, SERVICE, RESOURCE, DURATION, ERR, TRACE_ID |
| `events` | TIME, TYPE, SOURCE, TITLE, TAGS |
| `downtimes` | STATUS, SCOPE, MESSAGE, CREATED |
| `dashboards` | TITLE, LAYOUT, AUTHOR, MODIFIED |
| `contexts.<name>.site` | Datadog site (must be a known Datadog host — validated). |
| `contexts.<name>.subdomain` | Custom web-UI subdomain, for deep links only. |
| `contexts.<name>.api-key-env` / `app-key-env` | Env var **names** for the key pair. |
| `contexts.<name>.token-env` | Env var name for a bearer token. |
| `contexts.<name>.keychain` | `true` → secrets are in the OS keychain. |
| `contexts.<name>.auth` | `""` (key pair) or `token`. |
| `contexts.<name>.saved-queries` | Bookmarked queries (`{name, view, query}`), managed in-app with `Q`. Per-context. |

No config file? The `DD_API_KEY` / `DD_APP_KEY` / `DD_SITE` env vars act as an
implicit single `default` context.

---

## Command-line flags

| Flag | Default | Meaning |
|------|---------|---------|
| `--demo` | off | Run with built-in demo data (no credentials). |
| `--context <name>` | — | Start on a named context (overrides `$IKE_CONTEXT` and `current-context`). |
| `--site <site>` | — | Datadog site override when running without a config file. |
| `--refresh <dur>` | `30s` | Auto-refresh interval for live views; `0` disables. |
| `--debug` | off | Log at debug level (every fetch, with timing and cache state). |
| `--log-file <path>` | `~/.local/state/ike/ike.log` | Debug log file; empty string disables logging. |
| `--version` | — | Print version and exit. |

---

## Troubleshooting

**A view is empty.** No matching resources, or the filter/query is too narrow.
For Logs/Traces, widen the time window (`1`–`5`) — the default is 15m.

**`t` says "no trace_id".** The selected log has no injected `trace_id`, so
there's no trace to jump to. That needs APM log-correlation configured on the
service emitting the log.

**A trace comes back empty.** APM span retention/indexing may not cover it, or
the id isn't findable. The trace fetch uses an *unstable* Datadog endpoint whose
contract can change across API versions.

**"open in Datadog" opens the wrong URL.** Set `subdomain:` on the context to
match your org's web host (`https://<subdomain>.datadoghq.eu`). It affects deep
links only, never the API.

**Auth errors on startup.** ike opens on the `:ctx` view with the error instead
of exiting, so you can fix or add a context in place. Check that the env vars
named in your config are actually populated (`echo $IKE_DEV_API_KEY`), or that
the keychain entry exists.

**Everything feels stale.** Auto-refresh is off for all but monitors/incidents
by design (rate limits). `ctrl-r` forces a fresh fetch; `p` toggles auto-refresh
where it applies.

**Rate-limited (`429`).** ike auto-pauses auto-refresh and serves cached data.
Wait for the budget (header) to recover, or lean on the cache and navigate less
aggressively.

For the *why* behind these behaviours, see [docs/DESIGN.md](DESIGN.md).
