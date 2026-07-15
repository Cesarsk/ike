# AGENTS.md — working on ike

ike is a k9s-style terminal UI for Datadog (the name is a dog's name — "keep an
eye on your Datadog"): Go + tview/tcell, read-only against the Datadog API,
organized around per-org "contexts".
Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) (diagrams, component map)
and [docs/DESIGN.md](docs/DESIGN.md) (decisions, roadmap) before changing
anything structural.

## Build, test, verify

```sh
go build -o ike .        # binary
go vet ./... && gofmt -l .  # must both be clean
go test ./...             # unit + headless end-to-end TUI tests
./ike --demo             # full app on fake data, no credentials
IKE_DUMP=1 go test -run TestScreenDump ./internal/ui -v   # README screenshots
```

Every change must leave `vet`, `gofmt -l` (no output) and `go test ./...`
green — CI (`.github/workflows/ci.yaml`) enforces exactly these on
ubuntu + macos. Releases: push a `v*` tag → goreleaser builds darwin/linux
binaries and updates the Homebrew tap (see `.goreleaser.yaml`); do not tag
without the maintainer's go-ahead. Use the vocabulary defined in
[CONTEXT.md](CONTEXT.md).

## Hard rules

1. **No secrets in the config file, ever.** Credentials are env-indirected
   (`api-key-env`, `token-env`) or in the OS keychain. Strict YAML parsing
   (`KnownFields`) must keep rejecting inline `api-key:` fields. Never log a
   credential value; log auth *kind* only.
2. **The API surface is read-only.** Any future write operation (mute,
   downtime) goes behind an explicit confirm modal and is a deliberate,
   discussed exception.
3. **Layering:** `ui` never imports YAML, keyring, or the Datadog client —
   it sees `data.Provider` and the injected `Options` callbacks. `data`
   knows nothing about tview. `main.go` is the only place that wires them.
4. **Everything works in `--demo`.** New features must be exercisable
   offline via the `Provider` interface / injected callbacks — that's what
   makes them testable and demoable.
5. **Every interactive feature gets a SimulationScreen e2e test.**
   Pattern: `internal/ui/app_test.go` — inject keys with
   `typeRunes`/`press`/`typeCmd`, assert with `waitFor` on rendered screen
   text. No ptys (sandboxes don't have them), no sleeps beyond the helpers.
6. **`tview.Escape` every user-provided string** rendered in a
   dynamic-colors TextView or a Box title. `[staging]` is a valid tview
   color tag and silently disappears (this bit us; see git history).
7. **Errors must be visible where the user is looking.** Form errors go to
   the form's error line, not only the bottom status bar. Fetch errors keep
   stale cached rows on screen rather than blanking mid-incident.
8. **Rate limits are a design constraint, not an afterthought.** New fetch
   paths go through `data.Cached` with a TTL; nothing polls the API in a
   loop. Surface budget from `X-RateLimit-*` headers where relevant.
9. **k9s parity for UX decisions.** When adding interaction, check how k9s
   does it first (navigation stack semantics, `e` = edit in $EDITOR,
   command aliases). Divergence needs a reason written into DESIGN.md.
10. **Context switches are hard boundaries.** Anything org-scoped (cache,
    budget, nav history, queries) must be torn down in `switchContext` —
    never let one org's data render under another org's header.
11. **`config.Sites` is a security allowlist, not a convenience list.**
    Credentials are sent as headers to `api.<site>`, so every site a
    context can use must come from that one list (Load validates, the :ctx
    dropdown renders it). Never add a free-text site path. Browser opens
    are https-only (`App.openURL`).
12. **No employer- or person-identifying content anywhere in the repo.**
    Demo data, examples, screenshots and docs use only generic names
    (example.com, alice, prod-1, payments-api). This is a personal OSS
    project with no affiliation to anyone's employer — keep it that way in
    every commit, including README screen captures regenerated from demo
    data.

## Conventions

- Logging: `log/slog` to the file configured in `main.go` (`--log-file`,
  default `~/.local/state/ike/ike.log`; `--debug` for fetch-level lines).
  Never write to stdout/stderr while the TUI runs — tview owns the terminal.
- Keep `README.md` (user-facing), `docs/DESIGN.md` (decisions/roadmap) and
  `docs/ARCHITECTURE.md` (structure/diagrams, Mermaid) in sync with feature
  work — update them in the same change.
- Commit style: imperative subject, body explains *why*. Reference the
  roadmap item in DESIGN.md when closing one.
- tview gotchas already learned (don't relearn them): `trackEnd` latches on
  empty-table draws (see `render()`); autocomplete dropdowns swallow the
  first Enter (see `SetAutocompletedFunc`); dynamic-color tag swallowing
  (rule 6).

## Current state / known gaps

- Live mode has never been run against a real Datadog org; keychain writes
  (`go-keyring` → macOS `security`) are untested on a real keychain.
- Incidents field mapping (union-typed severity/state) is the most
  speculative live code; monitors are capped at one page of 200; logs at
  100 with no pagination.
- Git history starts at the maintainer's first manual `git init` (the
  authoring sandbox cannot write `.git/config`); repo policy — visibility,
  versioning, release gates — is recorded in docs/DESIGN.md § Project
  policy.
