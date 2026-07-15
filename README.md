# ike 🐶

**ike — keep an eye on your Datadog. A k9s-style terminal UI.**

Your Datadog's sitter: navigate monitors, incidents, SLOs, logs and dashboards
from your terminal with the muscle memory you already have from
[k9s](https://k9scli.io): `:` to switch resources, `/` to filter, `enter` to
drill down, `esc` to go back.

<sub>Named after a dog named Ike. The command is `ike`; the job is keeping an eye on things.</sub>

```
 Mode:   demo                     <:>cmd  </>filter  <enter>details  <o>open in
 Site:   datadoghq.eu                                                   Datadog
 View:   Monitors                   <ctrl-r>refresh  <esc>back  <?>help  <q>quit
 Age:    0s (ttl 30s)
 Budget: monitors 973/1000       :monitors  :incidents  :slos  :logs  :dashboards
╔══════════════════════════════ Monitors(all)[18] ═════════════════════════════╗
║STATE   NAME                             TYPE             PRIO TAGS           ║
║Alert   Node not ready in prod           service check    P1   team:sre,servi…║
║Alert   Payments API p99 latency > 800ms metric alert     P1   team:payments,…║
║Alert   Synthetic: login journey failing synthetics alert P1   team:frontend,…║
║Warn    Backup job missed schedule       event alert      P2   team:sre,servi…║
║Warn    S3 4xx on document bucket        metric alert     P4   team:backend,s…║
║OK      Kong data plane 5xx rate         metric alert     P1   team:sre,servi…║
╚═══════════════════════════════════════════════════════════════════════════════╝
```

> **Status: proof of concept.** Read-only, five resource views, multi-org
> contexts, demo mode. See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for
> diagrams of how it works and [docs/DESIGN.md](docs/DESIGN.md) for the
> design decisions and roadmap.

## Why

[Pup](https://github.com/DataDog/pup), Datadog's official CLI, is built for
AI agents and scripting — 200+ commands, JSON out. It is `kubectl`. Nothing in
the ecosystem is `k9s`: an interactive, keyboard-driven cockpit for a human
running incident response. ike is that tool.

## Quick start

```sh
go build -o ike .

# No credentials? Explore with demo data (ships two fake orgs — try :ctx):
./ike --demo

# Live mode, single org — same env vars as dogshell/terraform:
export DD_API_KEY=... DD_APP_KEY=... DD_SITE=datadoghq.eu
./ike
```

Flags: `--context` (start on a named context), `--refresh` (auto-refresh
interval, default `30s`), `--site` (site override when running without a
config file), `--demo`.

## Multiple orgs (contexts)

Most companies run several Datadog organizations (dev/stage/uat/prod,
org-per-BU, …). ike models them as **contexts**, kubeconfig-style, in
`~/.config/ike/config.yaml` (or `$IKE_CONFIG`):

```yaml
current-context: dev
contexts:
  dev:
    site: datadoghq.eu
    subdomain: acme-dev               # only if your org's web UI lives at
                                      # https://acme-dev.datadoghq.eu — fixes
                                      # 'open in Datadog' links, API unaffected
    api-key-env: IKE_DEV_API_KEY     # name of the env var holding the key —
    app-key-env: IKE_DEV_APP_KEY     # secrets NEVER go in this file
  prod:
    site: datadoghq.com
    api-key-env: IKE_PROD_API_KEY
    app-key-env: IKE_PROD_APP_KEY
```

- `:ctx` inside the app lists contexts; `enter` switches org. A switch drops
  the cache, rate-limit budget and navigation history — nothing leaks
  between orgs, and the header always shows which org you're on
  (`live [prod]`).
- **Add a context from inside the app**: `:ctx` → `a` opens a form — name,
  site, then either paste your API/APP keys **or a bearer/access token**
  (all masked), with a guidance panel explaining where each credential
  lives in Datadog. Secrets go to the **OS keychain** (macOS Keychain /
  Linux Secret Service); only `{site, keychain: true}` is written to the
  config file. `ctrl-d` deletes the selected context (with confirmation;
  the active one is protected).
- **Edit the config in your editor**: `:ctx` → `e` suspends the TUI and
  opens the config file in `$EDITOR` (vi by default), k9s-style; on exit
  the file is reloaded and re-validated.
- **Token auth in the config file** works too: set `token-env` instead of
  the two key env vars, and ike sends it as an `Authorization: Bearer`
  header (OAuth2 access tokens / PATs, e.g. from Datadog's pup CLI).
- Startup selection: `--context` flag → `$IKE_CONTEXT` → `current-context`.
- Plaintext `api-key:` fields are **rejected at parse time**; point the
  `*-env` fields at env vars populated by direnv, 1Password CLI, etc., or
  use the in-app form for keychain storage.
- No config file? The classic `DD_API_KEY`/`DD_APP_KEY`/`DD_SITE` vars act
  as an implicit single `default` context.

## Key bindings

| Key | Action |
|-----|--------|
| `:` | command mode — `:monitors` `:incidents` `:slos` `:logs` `:dashboards` (or `mon`, `inc`, `s`, `l`, `d`) |
| `:ctx` | list org contexts; `enter` switches, `a` adds (keys/token → OS keychain), `e` edits the config in `$EDITOR`, `ctrl-d` deletes |
| `/` | filter rows; in **Logs** this is a Datadog search query sent to the API, with **autocomplete** for facet keys, operators, and values seen in the current results (`tab`/`enter` accepts, then keep typing; a second `enter` submits) |
| `enter` | detail view — fetches the **full object** on demand where the list is only a summary (monitors, incidents). On a **dashboard**, renders its widgets with block-character sparklines + latest values (`ctrl-r` refreshes) |
| `esc` | go back to the previous view (k9s-style navigation history); clears the active filter |
| `l` | on a monitor: **drill down to its logs** — jumps to the Logs view pre-filtered with the monitor's log query (log monitors) or its `service:`/`env:` tags; `esc` returns |
| `r` | on an incident: **change its state** (active/stable/resolved) — the only write ike performs, always behind a confirmation |
| `s` / `S` | cycle the sort column / reverse the direction (any table view) |
| `t` | on SLOs: cycle the **Type** filter (metric / monitor / time_slice / all) |
| `o` | open selected item in the Datadog web UI (works in detail view too) |
| `ctrl-r` | force refresh (bypasses cache — spends API budget) |
| `1`–`4`, `0` | monitors quick filter: alert / warn / no data / ok / all |
| `1`–`5` | in **Logs**: time window — 15m / 1h / 4h / 1d / 7d (shown in the title) |
| `j`/`k`, `↑`/`↓` | move selection / scroll detail |
| `?` | help (from any view) |
| `q` | back in detail/help; quit from a table view (`ctrl-c`, `:q`, `:quit`, `:exit` always quit) |

## Rate limits are a feature, not a footnote

Datadog's API is rate-limited **per organization** (e.g. log search: 300
requests/hour). Unlike Kubernetes, you cannot poll it every two seconds. ike
is designed around that:

- every view is cached with a per-resource TTL — navigation is free;
- only cheap views (monitors, incidents) auto-refresh;
- `ctrl-r` is the explicit "spend budget" action;
- the header shows live rate-limit headroom from Datadog's own
  `X-RateLimit-*` response headers.

## Development

```sh
go test ./...                                  # includes a headless TUI smoke test
IKE_DUMP=1 go test -run TestScreenDump ./internal/ui -v   # regenerate README screens
```

The TUI is tested end-to-end on a tcell `SimulationScreen` — no pty needed.

## License

Apache 2.0
