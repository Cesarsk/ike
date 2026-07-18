# Session restore (org + last view) + startup splash

Status: approved design (2026-07-17)

## Goals

1. **Session restore** — reopening `ike` lands on the org (context) and the
   resource view (tab) the user was last on, not always the default context +
   `monitors`.
2. **Startup splash** — a brief k9s-style splash (a small shiba ASCII + `ike
   <version>` + tagline) shown at launch, auto-dismissed.

## 1. Session restore

Scope: **org + last view** (not the server query/filter — deliberately, to avoid
surprising reopens).

### Config

`Config` gains a top-level field beside `current-context`:

```
CurrentView string `yaml:"current-view"`
```

Validation is non-fatal (mirrors the dangling-`current-context` handling): an
empty or unknown view is left as-is and the UI falls back to the first resource
(`monitors`).

### Persistence

New callback on `ui.Options`:

```
PersistSession func(context, view string) error
```

`main` wires it to write both fields and `Save`; demo mode leaves it nil
(no-op). It is invoked at the **deliberate** navigation points only:

- `switchContext` (after the `:ctx` → enter org switch) → persists `(newOrg,
  monitors)` (the org switch resets to the first view).
- `execCommand` after a `:<resource>` switch → persists `(org, res.Key)`.

Drill-downs (`l`/`t`/`enter` into a trace/log/detail) and nav-stack restores are
**not** persisted — reopen lands on the tab the user chose, not a transient
drill target. Write-through (not on-quit) so it survives a hard terminal kill.

### Launch

`Options.CurrentView` carries the persisted view. `New()` opens on
`initialResource(o.CurrentView)` — resolved via `data.ResourceByAlias`, falling
back to `Resources()[0]` when empty/unknown — instead of always `Resources()[0]`.
`--context` / `$IKE_CONTEXT` still override the org at launch; the view has no
flag (YAGNI).

`persistSession()` helper reads `a.current` + `a.res.Key`, skips the `ctx`
pseudo-resource and the empty resource, and logs (not fatals) on write error.

## 2. Startup splash

- A centered stylised **IKE** logo (block/shade art) + the version (`v`-prefixed
  for numeric versions, e.g. `v0.1.5`; a `dev` build stays `dev`) +
  `github.com/Cesarsk`. (A shiba drawing was prototyped and dropped — a face
  reads ambiguously at small size and the good braille art was too tall for an
  80×24 terminal.)
- Shown **full-screen** for its duration via a root swap (`SetRoot(splashView)`
  → `SetRoot(rootView)` on dismiss), so it isn't boxed under the header/footer.
  The splash background is **transparent** (`tcell.ColorDefault`) so it inherits
  the terminal background instead of painting a differently-shaded rectangle.
- Raised from `New()` *after* the initial `switchResource` kicks off the first
  fetch — so data loads underneath the splash (non-blocking).
- Auto-dismissed after ~1.2s (a goroutine → `QueueUpdateDraw(dismissSplash)`,
  same pattern as `ticker`) **or** on any keypress (`keys()` `"splash"` case →
  `dismissSplash()`, key swallowed so it doesn't also act on the table).
- `dismissSplash()` is idempotent (guards on `a.page == "splash"`) and restores
  the normal layout + the `table` page (the initial view already rendered).
- `Options.Version` carries the `main` `version` string (goreleaser ldflag);
  defaults to `dev`.

## Testing

- **Unit:** `config` round-trip preserves `current-view`; unknown view falls
  back without error.
- **e2e (SimulationScreen):**
  - Splash art visible at startup, then clears to the table.
  - Switch org + `:incidents`, rebuild the app from the same persisted config
    (simulating relaunch), assert it opens on that org + incidents.

## Out of scope

- Persisting the server query/filter, sort, or selected row.
- A `--view` launch flag.
