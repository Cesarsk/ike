# ike 🐶

A k9s-style terminal UI for Datadog. Browse monitors, incidents, SLOs, logs,
traces and dashboards from your keyboard, with the same muscle memory you
already have from [k9s](https://k9scli.io): `:` to switch views, `/` to filter,
`enter` to drill in, `esc` to go back.

[![CI](https://github.com/Cesarsk/ike/actions/workflows/ci.yaml/badge.svg)](https://github.com/Cesarsk/ike/actions/workflows/ci.yaml)
[![Release](https://img.shields.io/github/v/release/Cesarsk/ike?sort=semver)](https://github.com/Cesarsk/ike/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/Cesarsk/ike)](https://goreportcard.com/report/github.com/Cesarsk/ike)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

```
 Mode:   demo [demo-dev]            <:>cmd  </>filter  <enter>details  <o>open
 Site:   datadoghq.eu                                           <c>copy  <C>cols
 View:   Monitors                   <ctrl-r>refresh  <p>auto:on  <esc>back  <?>help
 Budget: monitors 973/1000   :monitors :incidents :slos :logs :traces :services …
╔══════════════════════════════ Monitors(all)[18] ═════════════════════════════╗
║STATE   MUTED NAME                             TYPE             PRIO TAGS      ║
║Alert         Payments API p99 latency > 800ms metric alert     P1   team:pay…║
║Alert         Node not ready in prod           service check    P1   team:sre…║
║Warn          Vault sealed                     service check    P1   team:sre…║
║No Data       Datadog agent not reporting      service check    P2   team:sre…║
║OK            Kong data plane 5xx rate         metric alert     P1   team:sre…║
╚═══════════════════════════════════════════════════════════════════════════════╝
```

<sub>Named after a dog named Ike. The command is `ike`; the job is keeping an eye on things.</sub>

> **Status: real-org validated.** Eleven views (monitors, incidents, SLOs, logs,
> traces, services, events, RUM, synthetics, downtimes, dashboards) plus `:overview`, a
> cross-org triage screen. Views can span several Datadog orgs at once
> (activate contexts with space in `:ctx`), log and trace correlation with a
> unified request timeline, an incident war room (people, impacts, to-dos),
> SLO error-budget burndowns, confirm-gated writes (mute a monitor, change
> incident state / severity / commander, incident to-dos, cancel a downtime),
> a fuzzy row finder, session restore, and an offline demo mode. New here?
> The **[User Manual](docs/MANUAL.md)** is a full walkthrough.

## Demo

![ike demo](docs/demo.gif)

Or run it yourself with no credentials:

```sh
ike --demo
```

## Install

**Homebrew** (macOS and Linux):

```sh
brew install cesarsk/tap/ike
```

**Prebuilt binaries**: grab a tarball for your OS and architecture from the
[latest release](https://github.com/Cesarsk/ike/releases/latest) (darwin and
linux, amd64 and arm64), extract it, and put `ike` on your `PATH`.

**From source** (Go 1.25+):

```sh
go install github.com/Cesarsk/ike@latest
```

## Quick start

```sh
# No credentials? Explore with demo data (ships two fake orgs, try :ctx):
ike --demo

# Sign in through your browser (OAuth2; no API keys, tokens auto-refresh):
ike auth login --site datadoghq.eu --org myorg
ike

# Or classic env vars, same as dogshell and Terraform:
export DD_API_KEY=...  DD_APP_KEY=...  DD_SITE=datadoghq.eu
ike
```

`ike auth login` opens Datadog's own login page (SSO and 2FA included), stores
the tokens in your OS keychain tied to a named context, and refreshes them
automatically. Re-run it any time to rotate. Orgs with a custom web subdomain
pass `--subdomain acme-dev`. The same flow is available inside the app: in
`:ctx`, add a context with the "Browser sign-in (OAuth)" auth type, then press
`O` on its row to sign in. `O` also re-signs-in an OAuth context, or converts a
key/token context to OAuth (it asks first).

The first launch opens a short getting-started page inside the app; reopen it
any time with `:manual`.

## The debugging loop

ike is built around the loop your on-call actually runs. A monitor fires, so
you jump to its **logs** (`l`), then to the failing request's **trace** (`t`,
the span waterfall showing every service hop and where the error is), then back
to the **logs** for that whole trace (`l`). Monitors, logs and traces all
connect by `trace_id`, in the terminal, without opening a browser tab.

## Why ike

Datadog's official CLI, [pup](https://github.com/DataDog/pup), is a scripting
tool: 200+ commands with JSON output, built for automation and AI agents. It is
the `kubectl` of Datadog. ike covers the other side: an interactive,
keyboard-driven cockpit you sit in front of during an incident, the way you use
`k9s` for Kubernetes. You browse, filter, drill down and take action, all from
the keyboard.

## Key bindings

The essentials (see the [full reference in the Manual](docs/MANUAL.md#keybinding-reference)):

| Key | Action |
|-----|--------|
| `:` | switch view: `:monitors` `:incidents` `:slos` `:logs` `:traces` `:services` `:events` `:rum` `:synthetics` `:downtimes` `:dashboards` `:overview` `:ctx` `:settings` |
| `/` | filter rows; in Logs/Traces/Events it is a Datadog search query (with autocomplete) |
| `enter` | detail view (SLO error budget, dashboard widget grid, incident People header, …) |
| `l` / `t` | drill to logs / to the trace waterfall (the debugging loop) |
| `esc` | back; also clears an active filter first |
| `r` / `v` | incident: change state / severity (confirm-gated) |
| `I` / `T` | incident: assign commander (searchable picker) / open the to-do panel |
| `m` / `x` | mute a monitor / cancel a downtime (confirm-gated) |
| `Q` / `C` | saved-query picker / column picker |
| `F` | fuzzy row finder on any table |
| `space` | in `:ctx`: activate a context so views span that org too |
| `?` | help, from any view |

## Multiple orgs (contexts)

Most companies run several Datadog organizations (dev, stage, prod, one per
business unit, and so on). ike models each as a **context**, kubeconfig-style,
in `~/.config/ike/config.yaml`:

```yaml
current-context: dev
contexts:
  dev:
    site: datadoghq.eu
    api-key-env: IKE_DEV_API_KEY     # the name of the env var holding the key.
    app-key-env: IKE_DEV_APP_KEY     # secrets never go in this file.
  prod:
    site: datadoghq.com
    api-key-env: IKE_PROD_API_KEY
    app-key-env: IKE_PROD_APP_KEY
```

- `:ctx` lists contexts; `enter` switches org. A switch drops the cache,
  rate-limit budget and navigation history, so nothing leaks between orgs.
- Add a context from inside the app with `:ctx` then `a`: a form takes the name,
  site, and either an API/APP key pair or a bearer token (all masked). Secrets
  go to the OS keychain; only `{site, keychain: true}` is written to the file.
- Secrets are always env-indirected or keychain-stored. Plaintext `api-key:`
  fields are rejected at parse time. With no config file at all, the classic
  `DD_API_KEY` / `DD_APP_KEY` / `DD_SITE` vars act as an implicit `default`
  context.

Full context and auth details are in the
[Manual](docs/MANUAL.md#multiple-orgs-contexts--auth).

## Built around Datadog's rate limits

Datadog's API is rate-limited per organization (log search is 300 requests per
hour, for example), so you cannot poll it every few seconds the way you can a
Kubernetes cluster. ike is built for that:

- every view is cached with a per-resource TTL, so navigating around spends no
  API budget;
- only the cheap views (monitors, incidents) auto-refresh;
- `ctrl-r` is the explicit "refresh from the API" action;
- the header shows your live rate-limit headroom, read from Datadog's own
  `X-RateLimit-*` response headers.

## Customization

`:settings` edits the theme (`ike`, `default`, `mono`, `nord`, `solarized`) and
per-view cache TTLs. `C` on any table opens a column picker (`space` to
show/hide, `J`/`K` to reorder). Everything applies live and is saved to the
config file, which is hand-editable too. See the
[Manual](docs/MANUAL.md#settings-view).

## Development

```sh
go test ./...                                             # includes a headless TUI smoke test
IKE_DUMP=1 go test -run TestScreenDump ./internal/ui -v   # regenerate the README screens
```

The TUI is tested end-to-end on a tcell `SimulationScreen`, so no pty is needed.
See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the design and
[docs/DESIGN.md](docs/DESIGN.md) for decisions and roadmap.

## License

Apache 2.0
