# AGENTS.md — working on ike

ike is a k9s-style terminal UI for Datadog (the name is a dog's name — "keep an
eye on your Datadog"): Go + tview/tcell, read-mostly against the Datadog API,
organized around per-org "contexts".
Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) (diagrams, component map)
and [docs/DESIGN.md](docs/DESIGN.md) (decisions, roadmap) before changing
anything structural. Use the vocabulary defined in [CONTEXT.md](CONTEXT.md).

## Build, test, verify

```sh
go build -o ike .           # binary
go vet ./... && gofmt -l .  # must both be clean (gofmt prints nothing)
go test ./...               # unit + headless end-to-end TUI tests
./ike --demo                # full app on fake data, no credentials
IKE_DUMP=1 go test -run TestScreenDump ./internal/ui -v   # README screenshots
```

Every change must leave `vet`, `gofmt -l` (no output) and `go test ./...`
green — CI (`.github/workflows/ci.yaml`) enforces exactly these on
ubuntu + macos. Releases: push a `v*` tag → goreleaser builds darwin/linux
binaries and updates the Homebrew tap (see `.goreleaser.yaml`); do not tag
without the maintainer's go-ahead.

## Hard rules

1. **No secrets in the config file, ever.** Credentials are env-indirected
   (`api-key-env`, `token-env`), in the OS keychain (API/APP keys, access
   tokens), or OAuth tokens in the keychain (`<ctx>:oauth` blob). Strict YAML
   parsing (`KnownFields`) must keep rejecting inline `api-key:` fields. Never
   log a credential, token, or OAuth blob; log the auth *kind* only.
2. **The API surface is read-mostly, and every write is confirm-gated.** The
   writes today are: incident state / severity / commander, incident to-dos
   (add/complete/delete), monitor mute, downtime cancel. Each goes behind a
   confirmation modal — a write must never happen on a single unconfirmed
   keypress. New writes follow the same rule.
3. **Layering.** `ui` never imports YAML, keyring, or the Datadog client — it
   sees `data.Provider` and the injected `Options` callbacks. `data` knows
   nothing about tview. `main.go` is the only place that wires them together.
4. **Everything works in `--demo`.** New features must be exercisable offline
   via the `Provider` interface / injected callbacks — that is what makes them
   testable and demoable. `--demo` uses only generic fake data (rule 12).
5. **Every interactive feature gets a SimulationScreen e2e test.** Pattern:
   `internal/ui/app_test.go` — inject keys with `typeRunes`/`press`/`typeCmd`,
   assert with `waitFor`/`waitForMatch`/`waitForBg` on the rendered screen. No
   ptys (sandboxes lack them), no sleeps beyond the helpers. Screen-style
   asserts read ONE `GetContents` snapshot and convert regex byte offsets to
   rune offsets (box-drawing borders are 3-byte runes).
6. **`tview.Escape` every user-provided string** rendered in a dynamic-colors
   TextView or a Box title. `[staging]` is a valid tview color tag and silently
   disappears (this bit us; see git history).
7. **Errors must be visible where the user is looking.** Form errors go to the
   form's error line, not only the bottom status bar. Fetch errors keep stale
   cached rows on screen rather than blanking mid-incident.
8. **Rate limits are a design constraint.** New fetch paths go through
   `data.Cached` with a TTL; nothing polls the API in a loop. On-demand fetches
   (detail, trace, log-context) are single bounded calls. Surface budget from
   `X-RateLimit-*` headers where relevant.
9. **k9s parity for UX decisions.** When adding interaction, check how k9s does
   it first (navigation-stack semantics, command aliases, `:`/`/` conventions).
   Divergence needs a reason written into DESIGN.md.
10. **Context switches are hard boundaries.** Anything org-scoped (cache,
    budget, nav history, queries) is torn down in `switchContext` — never let
    one org's data render under another org's header. Row-scoped actions route
    to the row's origin org via `providerFor`.
11. **`config.Sites` is a security allowlist, not a convenience list.**
    Credentials are sent as headers to `api.<site>`, so every site a context
    can use must come from that one list (Load validates; the `:ctx` dropdown
    renders it). Never add a free-text site path. Browser opens are https-only
    (`App.openURL`), which also blocks argument injection into `open`.
12. **No employer- or person-identifying content anywhere in the repo.** Demo
    data, examples, screenshots and docs use only generic names (example.com,
    acme-dev, alice, prod-1, payments-api). This is a personal OSS project with
    no affiliation to anyone's employer — keep it that way in every commit,
    including README screen captures and the demo GIF regenerated from demo
    data.

## Go style

House style, grafted from a review of a well-run Go project's conventions and
adapted to ike. New code follows these; existing code is brought into line
opportunistically when touched (no blanket retrofits).

**File organization.** Group files around real concerns, not a monolith. A
package's behavior splits across `concern.go` files (`contexts.go`,
`correlation.go`, `render.go`, `nav.go`, `help.go`, …); features land in the
concern they belong to. Don't grow one giant file, and don't add wrapper files
that only forward calls.

**Naming.**
- Receivers: a single lowercase letter, the type's first letter (`a *App`,
  `l *Live`). Never `self`/`this`/the spelled-out type.
- Locals: short, and shorter the tighter the scope; don't restate the type
  (`row`, not `rowValue`; `r`, `i`, `ok`, `err`, `ctx`). Map/assert results use
  `ok`. Longer names only for wide scope (exported APIs, struct fields, tests).
- Functions: verb + noun (`drillToLogs`, `switchContext`). `Get`/`Set` only for
  field access. Constructors are `New…`.
- Interfaces: capability, `-er` where it's one method (`Provider`, not
  `IProvider`/`ProviderInterface`).
- Constants: `Default…` for defaults, `Max…`/`Min…` for limits — no bare magic
  numbers.
- Acronyms all-caps: `URL`, `ID`, `HTTP`, `OAuth`. Bool struct fields skip the
  `is`/`has` prefix (`Active`, `Muted`), functions may use it (`hasData`).

**Struct literals: always use named fields.** Positional literals compile
silently after a field reorder and corrupt data. The only exception is a
single-field wrapper where the name adds nothing.

**Control flow.** Guard clauses and early returns; keep to about one level of
conditional nesting. No `else` after an early return.

**Declarations.** No function-scoped `type`/`const` — declare them at package
level. Within a file: types, then consts, then vars, then exported funcs
(incl. constructors), then exported methods, then unexported methods, then
helpers. Keep related methods together.

**Interface compliance.** Assert it at compile time where a type must satisfy
an interface: `var _ data.Provider = (*Live)(nil)`.

**No mutable package-level state.** State lives on the owning struct (the `App`,
a `Live`). Package `var`s are only for sentinel errors, compile-time interface
assertions, and truly-immutable lookup tables (`themes`, `siteRegions`,
`spanningResources`) that are never reassigned.

**Args/Res structs.** A function taking 5+ arguments bundles them into an
`Args` struct; one returning 3+ *data* results bundles them into a `Res` struct
(a trailing `bool`/`error` success indicator doesn't count). Also bundle when
two adjacent params/results share a type (`(int, int)` — which is width, which
is height?). Named fields put the meaning in the code, not in argument order.
Declare the struct immediately before its function; pass/return it by value; it
must not outlive its single call site (if it does, it's a plain type, not
Args/Res).

**Comments.** Godoc on exported identifiers, ≤3 lines, says *what* not *how*, no
trailing period; skip it when the name is self-documenting. Unexported gets a
comment only when the behavior isn't obvious from the signature. Inline
comments explain *why*, never restate the code.

**Tests.** `Test<Subject>` names the unit; scenario detail goes in `t.Run(…)`
labels kept under ~40 chars. Table-driven where there are several cases;
`t.Helper()` in shared assert helpers.

## Conventions

- Logging: `log/slog` to the file configured in `main.go` (`--log-file`,
  default `~/.local/state/ike/ike.log`; `--debug` for fetch-level lines).
  Never write to stdout/stderr while the TUI runs — tview owns the terminal.
- Keep `README.md` (user-facing), `docs/DESIGN.md` (decisions/roadmap),
  `docs/ARCHITECTURE.md` (structure/diagrams, Mermaid) and `docs/MANUAL.md`
  (full walkthrough) in sync with feature work — update them in the same
  change. README/docs prose: no em dashes, plain language over jargon.
- Commit style: imperative subject, body explains *why*. Reference the roadmap
  item in DESIGN.md when closing one. Commit author is the maintainer's public
  identity, never a work email.
- tview gotchas already learned (don't relearn them): `trackEnd` latches on
  empty-table draws (see `render()`); autocomplete dropdowns swallow the first
  Enter (see `SetAutocompletedFunc`); a closed DropDown opens on Up/Down unless
  captured; dynamic-color tag swallowing (rule 6).

## Deliberately not adopted

Recorded so these are decisions, not oversights:

- **Never-commit / do-exactly-what-is-asked stances** — ike's workflow is
  commit → PR → CI → release; the maintainer drives it.
- **Black-box tests (`package_test`) / testify** — ike's e2e tests are
  necessarily in-package (they drive the App and assert on internal state) and
  use hand-rolled `waitFor` helpers, no testify dependency.
- **Never panic** — ike returns errors everywhere except one sanctioned
  fail-closed panic: `internal/auth.randomURLSafe` panics if the CSPRNG fails
  rather than emit predictable OAuth material.
- **Typed `Err`-prefixed errors + `errors.Is` everywhere** — ike's errors are
  user-facing display strings shown in the status bar, not matched
  programmatically; inline `fmt.Errorf` stays.
- **80-char line cap / markdown soft-wrap** — gofmt is the code formatter (no
  hard column cap); docs stay hard-wrapped as they are.

## Current state / known gaps

- Live-validated against a real Datadog org through v0.4.0: browser OAuth,
  keychain reads/writes, multi-org spanning, and the confirm-gated writes all
  exercised on a real org.
- Incidents field mapping (union-typed severity/state) is still the most
  intricate live code. Monitors are capped at one page of 200; logs at 100 with
  no pagination; log surrounding-context is a single bounded ±window (no
  streaming — the Datadog client has no tail endpoint).
- Repo policy — visibility, versioning, release gates — is recorded in
  docs/DESIGN.md § Project policy.
