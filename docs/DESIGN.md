# ike — Design

## Vision

k9s for Datadog: a keyboard-driven terminal cockpit for humans doing
operations and incident response. Not a scripting CLI (that's
[pup](https://github.com/DataDog/pup)), not a dashboard replacement — a fast
navigator with drill-down, deep links, and k9s muscle-memory.

## Why the k9s analogy breaks — and what we do about it

k9s can poll the Kubernetes API every 2 seconds because it is local, free and
has watch semantics. Datadog's API has neither:

| Endpoint family | Limit (per org, per hour) |
|---|---|
| Timeseries query | 1,600 |
| Log search | 300 |
| Metric retrieval | 100 |

Consequences baked into the architecture:

1. **Snapshot + cache, not watch.** Every fetch lands in a TTL cache keyed by
   `resource|query` (`internal/data/cache.go`). Switching views re-renders
   from cache; only TTL expiry, an explicit `ctrl-r`, or a new server query
   hits the API.
2. **Auto-refresh is opt-in per resource.** Monitors/incidents (cheap listing
   endpoints) refresh on a timer; logs, SLOs and dashboards are on-demand.
3. **Budget visibility.** The live provider records `X-RateLimit-*` headers
   from every response and the header widget displays remaining headroom.
   A TUI on five laptops shares one org budget with Terraform providers and
   Grafana — the user must be able to see what the tool is spending.
4. **Stale-tolerant.** On fetch error with a cached copy, ike serves the stale
   rows and surfaces the error, instead of blanking the screen mid-incident.

## Architecture

```
main.go                 flags, provider wiring
internal/data/
  types.go              Row, Resource registry (columns, TTLs, aliases), Provider iface
  cache.go              TTL cache + stale-on-error, the rate-limit defence
  live.go               datadog-api-client-go v2; rate-limit header tracking
  demo.go               offline provider with plausible SRE data (--demo)
internal/ui/
  app.go                tview shell: header, table, prompt, status, keys
  app_test.go           headless end-to-end smoke test (tcell SimulationScreen)
  screendump_test.go    README screenshot generator (IKE_DUMP=1)
```

Decisions:

- **Go + tview/tcell** — the exact widget stack k9s uses, so k9s UX parity
  (command mode, bordered tables, header hints) is the default, not an
  imitation. Official `datadog-api-client-go` covers everything we need.
- **`Provider` interface with a demo implementation** — the TUI is fully
  exercisable and testable without credentials, and the smoke test drives the
  real app end-to-end on a simulation screen in CI.
- **Read-mostly, every write confirm-gated.** The write surface is
  deliberately small: incident state (`r`) and severity (`v`), monitor
  mute/unmute (`m`), cancel downtime (`x`). Each is behind a confirmation
  modal built fresh per invocation; everything else is a read or a browser
  deep-link. Any future write follows the same confirm-gated rule.
- **Auth = named contexts with env-indirected secrets** (see
  [ARCHITECTURE.md](ARCHITECTURE.md)). The config file names *which env
  vars* hold each org's keys; plaintext keys in the file are rejected at
  parse time — dotfiles get committed. With no config file, the classic
  `DD_API_KEY`/`DD_APP_KEY`/`DD_SITE` vars form an implicit `default`
  context. OAuth2+PKCE and OS-keychain storage (what pup does) are the
  right end state but wrong first battle.
- **Contexts, not auto-detection, for multi-org.** Org layouts are too
  heterogeneous across companies (org-per-env, org-per-BU, single org) to
  infer; kubeconfig-style named contexts is the only shape that fits all.

## UX parity map (k9s → ike)

| k9s | ike |
|---|---|
| `:pods`, `:deploy` | `:monitors`, `:incidents`, `:slos`, `:logs`, `:dashboards` |
| `/` filter | `/` — client-side filter; in Logs, a server-side Datadog query |
| `enter` describe/drill | `enter` detail view (full JSON of the object) |
| `esc` back (page stack pop) | `esc` pops the navigation stack (view, filter, selection restored) |
| `:ctx` cluster contexts | `:ctx` Datadog org contexts (hard boundary: cache/budget/history dropped) |
| header: context, version, CPU/MEM | header: mode, site, view, cache age, **API budget** |
| resource-specific hotkeys | monitors: `0`–`4` state quick filters |
| live watch | TTL cache + explicit `ctrl-r` (deliberate — see above) |

## Query autocomplete (Logs `/`)

Zero-API by design. The completion offers common facet **keys**
(`service:`, `host:`, `status:`, `env:`, `@http.status_code`, …), search
**operators** (`AND`/`OR`/`NOT`), and facet **values harvested from the log
rows already loaded** (e.g. after `service:` it suggests the services in the
current result set). It never calls the facet API — so it costs nothing
against the tight logs budget, at the price of only knowing values already
seen in the current window. It completes the last whitespace-delimited token
and preserves the rest; a completion identical to what's typed is suppressed
so `enter` submits rather than re-accepting. Richer org-wide facet
completion (facet API, rate-limited) is a possible later opt-in mode.

## Roadmap

### Current focus — Datadog-native depth + UX (v0.2.x)

The strategy: keep the k9s interaction model (that muscle memory is the point)
and differentiate by going deeper into Datadog than a generic list/detail tool
can. Decided 2026-07-18, in rough build order:

1. **Cross-org rollup.** Activate *multiple* contexts from the `:ctx` page
   (opt-in per context; exactly one active remains the default) and get a
   rollup view that merges incidents/alerting monitors across every active org,
   each row tagged with its org. The one real architecture change in the
   package: several providers/caches alive at once, fetched in parallel, each
   respecting its own org's rate-limit budget. k9s is structurally
   single-context; this is ike's clearest differentiator.
2. **Incident war room.** One incident-focused screen: commander, responders,
   to-dos, impacts (`ListIncidentImpacts`), key fields — instead of a JSON
   dump. The timeline stays out (no official create/read op — see below).
3. **Richer structured detail views.** Replace the remaining raw-JSON detail
   dumps with sectioned, Datadog-native layouts, extending the incident People
   header + monitor metric-sparkline pattern to every view.
4. **SLO error-budget burndown.** A burndown sparkline + burn rate on the SLO
   detail — the `GetSLOHistory` series is already fetched for attainment.
5. **RUM view.** Verified feasible in v2.62.0: `RUMApi` `ListRUMEvents` /
   `AggregateRUMEvents` + `GetRUMApplications` — a `:rum` view with
   server-side query, like logs.
6. **Synthetics view.** Verified feasible: v1 `SyntheticsApi` `ListTests` +
   latest API/browser results — test pass/fail at a glance.
7. **Distinct visual identity.** A signature default palette + glyph set so
   ike reads as ike in screenshots (the theme system already exists).
8. **Fuzzy finder** overlay for resources and rows (client-side, zero API).

Tentative (roadmapped, not committed): **watch mode** — run ike passively and
raise a desktop notification on a new SEV-1 / monitor flip. Rate-limit
sensitive; needs a deliberately cheap poll.

Verified dead end: **service map / dependency graph** — the official client
(checked v2.62.0) exposes no service-dependencies or service-map endpoint;
same category as per-service stats. Revisit only if Datadog publishes one.
First-run onboarding was initially skipped, then shipped in v0.3.0 as the
one-time getting-started page (`:manual`).

### Auth — SHIPPED as v0.3.0 (`ike auth login`)

1. **OAuth2 login, pup-style**: `ike auth login --site <site> --subdomain <sub> --org <org>`.
   - **The flags map onto a named context.** `auth login` creates-or-updates a
     context (target it with `--context <name>`, defaulting to the `--org`
     value) and persists `{site, subdomain, org, keychain: true}` to the config
     — the *same* context model as the `:ctx` add form, so CLI login and
     manual contexts converge. `subdomain` already exists (web deep-links);
     `org` becomes a **new `Context` field** (label + used in the authorize
     flow). You pick which context you're authenticating *to*.
   - **Mechanics**: native OAuth2 + PKCE + dynamic client registration, a local
     callback server, token refresh, OS-keychain storage per context —
     mirroring pup (`pup auth login --site datadoghq.eu --subdomain <sub>
     --org <env>`). Checked: pup has no non-interactive "print token" command,
     so ike can't borrow pup's tokens; native is the honest path.
   - After login, `:ctx` lists the new context and switching uses the keychain
     token; refresh removes the ~1h rotation pain of the interim paste-a-token
     path (`token-env`/keychain token auth keeps working meanwhile).
   - **Spike: PASSED (2026-07-20).** Dynamic client registration works
     unauthenticated (`POST /api/v2/oauth2/register` → 201 with a `client_id`,
     `authorization_code` + `refresh_token` grants, public client). The full
     round-trip — browser authorize (`/oauth2/v1/authorize`, PKCE S256), local
     callback, token exchange (`/oauth2/v1/token`), an authenticated
     `current_user` call, and a token refresh — was validated against a real
     org with `hack/oauth-spike`. Resolved for the build: **no scopes are
     requested** — the token inherits the user's own permissions (pup-token
     evidence), so every read view and confirm-gated write works.
   - **In-app (shipped).** `:ctx` → `a` picks the auth type; choosing *Browser
     sign-in* creates a pending OAuth context. `O` on a row runs the login
     (shared `loginContext` core): sign-in/refresh on an OAuth row, or a
     confirm-gated conversion on a key/token row (which clears the old
     secrets). `enter` on an unsigned OAuth context prompts "press O".

### Near-term (rest of Tier 2)

2. **Live log tail** (bounded polling + backoff — must not blow the 300/h logs
   budget) **+ log → surrounding-context** (±N min, same host; needs
   absolute-time-range plumbing through the fetch path).

### Longer-term

3. **Token rotation** on an existing context — folds into `ike auth login
   --context <existing>` (re-auth updates the keychain token in place), so no
   separate key/flow is needed once auth login lands.
4. Bulk select + act (mute N monitors / resolve N incidents) behind one confirm.
5. **Incident timeline note** — a free-text timeline comment is **not** in the
   official client (only to-dos and attachments are; the timeline is an internal
   API). Shipped the `T` to-do panel (list/add/complete/delete) as the feasible
   annotate-the-incident verb; a true note waits on an official endpoint.
   Likewise **setting responders** has no write path (the incident update
   relationships expose only commander/integrations/postmortem) — responders are
   shown read-only in the detail.
6. **`:services` stats** — requests/error-rate/p95 per service would need either
   the internal service-stats endpoint (not in the official client) or a
   trace-metrics query (per-operation, no clean per-service rollup); revisit if
   the metrics path proves viable. For now `:services` is names + enter→traces.

### Deferred deliberately

Write-heavy verbs (bulk actions, the remaining incident writes) can't be tested
from the authoring sandbox — they wait on live dev-org validation of the writes
already shipped (incident state/severity, commander assign, to-do
add/complete/delete, monitor mute, cancel downtime).

Done: ~~multi-org contexts + config file~~ (`:ctx`, env-indirected secrets),
~~esc navigation stack~~, ~~in-app context add/delete with OS-keychain
storage~~ (`:ctx` → `a` / `ctrl-d`), ~~monitor → logs drill-down~~ (`l`),
~~pagination~~ (bounded, truncation logged), ~~on-demand full-object detail
fetch~~, ~~org web subdomains~~, ~~in-terminal dashboard rendering~~
(grid of widget sparklines matching the DD layout), ~~column sorting~~
(`s`/`S`), ~~SLO type filter~~ (`t`) + ~~incident state quick-filters~~,
~~incident state change~~ (`r`), ~~monitor mute/unmute~~ (`m`,
read-modify-write on `silenced`), ~~SLO attainment + error budget~~ (on
`enter`), ~~clipboard copy~~ (`c`), ~~configurable auto-refresh~~
(`refresh-interval` / `--refresh` / `p` toggle), ~~log-query autocomplete~~,
~~log time-range~~ (`1`–`5`), ~~glanceable budget header~~, ~~monitor MUTED
column~~, ~~traces view + APM span search~~ (`:traces`), ~~log⇄trace
correlation~~ (`t` → trace waterfall reconstructed from spans; `l` → the
trace's logs), ~~events feed~~ (`:events`), ~~metric-behind-a-monitor~~ (detail
sparkline), ~~log patterns~~ (`P`, zero-API clustering), ~~query history~~ (↑
in the prompt), ~~429 rate-limit backoff~~ (auto-pauses auto-refresh),
~~double-ctrl-c quit~~, ~~unified trace timeline~~ (waterfall + all-services
logs chronological), ~~downtimes list~~ (`:downtimes`), ~~downtimes cancel~~
(`x`, confirm-gated), ~~incident severity change~~ (`v`, SEV-1…SEV-5,
confirm-gated), ~~first Homebrew release~~ (`v0.1.0`+`v0.1.1`, `brew install
cesarsk/tap/ike`; goreleaser builds serialized to avoid runner OOM, formula in
`Formula/`), ~~trace view via `APMTraceApi.GetTraceByID`~~ (one call + native
`is_truncated`, replacing the span-search reconstruction), ~~Tier 3 config
polish~~: ~~per-resource TTL overrides~~ (`ttl-overrides`), ~~column
customization~~ (`columns`, display-only projection — edited via the `C`
column picker: `space` show/hide + `J`/`K` reorder, live + saved), ~~themes/
skins~~ (`theme`: default/mono/nord/solarized), ~~saved queries per context~~
(`Q` picker — save/apply/delete, persisted per context), ~~`:settings` editor~~
(theme + per-view TTL edited live and saved to config; theme re-applied at
runtime via `applyTheme`), ~~APM services view~~ (`:services` — service list via
`APMApi.GetServiceList` scoped by env (`/` = env, default `prod`), `enter` →
traces `service:<name>`; names only — the official API exposes no per-service
stats, and it's retention-independent unlike the span-aggregate it replaced,
which showed empty on orgs with tight span retention), ~~incident commander~~
(`I` — searchable user picker backed by `ListUsers`, assign to anyone with
yourself pinned on top; confirm-gated), ~~incident to-do panel~~ (`T` — list /
add / toggle-complete / delete action items, assignable to anyone via the same
picker), ~~incident responders + People detail~~ (commander / responders /
declared-by / created-by resolved to handles above the raw object via
`include=users`; responders are read-only — the API has no write path),
~~hardened incident field mapping~~ (`incidentField` handles both the single-
and multi-value arms of the field union + missing fields, so custom fields
don't blank SEV/STATE), ~~session restore~~ (`current-view` in the config
alongside `current-context`; reopens on the last org + view, persisted on
`:ctx`/`:<resource>` switches, drill-downs stay transient), ~~startup splash~~
(full-screen `IKE` logo + version + `github.com/Cesarsk`, transparent
background, ~1.2s or any key to dismiss, first view loading underneath),
~~multi-context activation + org-spanning views~~ (space in `:ctx`, per-org
providers/caches/budgets, CTX column, Row-level routing of every
detail/drill/write), ~~:overview~~ (cross-org triage: open incidents +
alerting monitors, worst first), ~~incident war room~~ (people, impacts via
`ListIncidentImpacts`, to-dos, fields in one detail), ~~SLO error-budget
burndown~~ (burn rate + burndown sparkline from `GetSLOHistory`), ~~RUM view~~
(`:rum` via `ListRUMEvents`, server-side query), ~~fuzzy row finder~~ (`F`),
~~structured monitor detail~~, ~~ike signature palette~~ (default theme; the
original look remains as `theme: default`), ~~synthetics view~~ (`:synthetics`
— inventory via `ListTests`, per-test latest results + pass rate on enter).

## Traces & correlation

`:traces` searches APM spans (v2 spans API), same server-query + time-window
shape as Logs. The **waterfall** (`t` from a log or span) fetches the trace by
id via `APMTraceApi.GetTraceByID` (`GET /api/v2/trace/{id}`) and links its
spans by `parentID` into a DFS tree with proportional duration bars. That is
the canonical trace-fetch — one call, strongly-typed spans (parent id, service,
resource, ns timings, error flag), and the API's own `is_truncated` flag —
and it replaced an earlier reconstruction from a `trace_id:` span search built
on the wrong belief that no get-trace endpoint existed. Two caveats: the
endpoint is an **unstable operation** (enabled explicitly at client init, so
its contract may change across Datadog API versions), and the whole feature
still hinges on `trace_id` being injected into logs (APM log-correlation) — a
log without one degrades to a clear message rather than a broken jump.

## Project policy (decided 2026-07-14)

- **Visibility**: public on GitHub (`Cesarsk/ike`) from the first push, no
  announcement until live mode is validated.
- **License**: Apache-2.0 (matches k9s and pup; explicit patent grant).
- **CI**: GitHub Actions on every push/PR — gofmt, vet, test, build on
  ubuntu + macos. No golangci-lint until contributors arrive.
- **Releases**: goreleaser on `v*` tags → GitHub Releases + Homebrew
  formula in `Cesarsk/homebrew-tap` (`brew install cesarsk/tap/ike`).
  Targets: darwin/linux, amd64/arm64 only — no Windows. Prereqs: tap repo
  + `TAP_GITHUB_TOKEN` secret.
- **Versioning**: no tag until live mode is verified against a real org.
  `v0.1.0` = all five views verified live + keychain add works. `v1.0` =
  config schema frozen (breaking YAML changes need migration), pagination,
  hardened incidents mapping, weeks of real incident use. Feature breadth
  is not a 1.0 gate; stability is.
- **Contribution model**: solo direct-push to main while single-maintainer;
  CI on every push keeps main honest. Contributor scaffolding exists from
  day one (CONTRIBUTING.md → AGENTS.md, issue/PR templates, SECURITY.md).
  Branch protection (PRs + green CI required) turns on when a second
  regular contributor appears.
- **Vocabulary**: [CONTEXT.md](../CONTEXT.md) is the canonical glossary.

## Dashboard rendering

`enter` on a dashboard renders its widgets in the terminal: each widget's
title, type, primary metric query, and a block-character sparkline + latest
value over the last hour. Widget queries are extracted by walking the
dashboard JSON generically (the widget-definition union has ~25 nesting
variants; JSON traversal is far more robust than the typed union). Only
single-metric widgets get a sparkline — formula/log/note widgets show a note.
Metric fetches are **bounded** (`data.MaxDashWidgets`, currently 12) and
uncached: the timeseries query API is the tightest budget we spend, so a
40-widget dashboard can't fan out to 40 requests on one open. No
auto-refresh; `ctrl-r` is the explicit re-fetch. This is a trend-at-a-glance
substitute for the graphical dashboard, not fidelity — `o` still opens the
real thing.

## Non-goals

- Pixel-faithful graph rendering — sparklines convey trend; deep-link (`o`)
  to the browser for the real dashboard.
- Wrapping all 33 Datadog products. Incident-response surface only; breadth
  is what pup is for.
- Config mutation (monitor definitions, dashboards JSON) in early versions.
