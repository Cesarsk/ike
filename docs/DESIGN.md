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
- **Read-mostly.** The one write operation is changing an incident's state
  (`r`), always behind a confirmation modal. Everything else is read or a
  browser deep-link. Any future write (monitor mute/downtime) follows the
  same confirm-gated rule.
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

1. `c` = set/replace credentials on an existing context — needed for token
   rotation (access tokens expire ~1h) and keychain-service-rename recovery;
   today the only path is delete + re-add.
2. Incident → linked monitors/logs drill-down (monitor → logs shipped as `l`).
3. Monitor mute/unmute via Downtimes API behind a confirm modal.
4. Hardened incidents field mapping (union types; verify against live org).
5. Sparkline metric previews in the detail view (braille rendering).
6. Live tail emulation for logs: bounded polling loop with visible budget
   spend (there is no public streaming API).
7. OAuth2 + PKCE device flow (the pup approach) as a keys-free alternative.
8. Per-resource TTL overrides and skins in the config file.

Done: ~~multi-org contexts + config file~~ (`:ctx`, env-indirected secrets),
~~esc navigation stack~~, ~~in-app context add/delete with OS-keychain
storage~~ (`:ctx` → `a` / `ctrl-d`), ~~monitor → logs drill-down~~ (`l`),
~~pagination~~ (bounded, truncation logged), ~~on-demand full-object detail
fetch~~, ~~org web subdomains~~, ~~in-terminal dashboard rendering~~
(widget sparklines).

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
