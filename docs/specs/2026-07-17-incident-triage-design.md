# Incident triage: commander picker, to-do panel, responders

Status: approved design (2026-07-17)

## Goal

Extend incident write verbs beyond "assign commander to me". Specifically:

1. Assign an incident commander to **any** org user, not only yourself.
2. Manage an incident's **to-dos** in-app: list, add (assigned to anyone),
   complete, delete.
3. **Display responders** in the incident detail (read-only — the API has no
   write path for them).

All user selection is driven by a **live API lookup** of assignable users, not
free-text entry.

## Non-goals / not possible

Verified against the pinned client (`datadog-api-client-go/v2 v2.62.0`):

- **Setting responders** — `IncidentUpdateRelationships` exposes only
  `commander_user`, `integrations`, `postmortem`. No `responders` field, no
  add/remove-responder operation. Responders are read-only.
- **Timeline notes** — the timeline-cell *models* exist but there is no
  `CreateIncidentTimelineCell` operation.
- **Impacts / integrations** — API exists but out of scope (niche for triage).
- **Status change** — already shipped as `r` (state) / `v` (severity) via
  `SetIncidentField`. Not re-implemented here.

## Building blocks

### A. User picker (shared)

A searchable overlay page (`userpick`), same pattern as the column/saved-query
pickers: a search `InputField` on top, a `tview.List` of results below. Typing
re-queries the API (debounced); Up/Down selects; Enter confirms; Esc cancels.
`:` command mode works on the page (consistent with the other overlays).

Backed by a new provider method:

```
ListUsers(ctx, query string) ([]User, error)
```

- `UsersApi.ListUsers` with `Filter` = query (server-side match on
  name/email/handle), `FilterStatus` = "Active", `PageSize` ≈ 50 (bounded — one
  page, no unbounded pagination).
- Short cache (users change rarely); counts against a new `users` rate-limit
  family in `Budget()`.
- Empty query returns the first page (so the picker is useful before typing).

### B. Incident to-do panel

An overlay page (`todos`) scoped to the selected incident. Lists the incident's
to-dos with a completion marker and assignees. In-panel keys:

- `a` — add: content prompt → user picker for the assignee.
- `c` / `space` — toggle complete on the highlighted to-do.
- `d` — delete the highlighted to-do (behind the existing confirm modal).
- `esc` — back.

New provider methods:

```
IncidentTodos(ctx, incidentID string) ([]Todo, error)
SetIncidentTodoCompleted(ctx, incidentID, todoID string, done bool) error
DeleteIncidentTodo(ctx, incidentID, todoID string) error
```

New type:

```
type Todo struct {
    ID        string
    Content   string
    Assignees []string // handles
    Completed bool
}
```

- `IncidentTodos` → `ListIncidentTodos`.
- `SetIncidentTodoCompleted` → `UpdateIncidentTodo` (set/clear the `completed`
  timestamp; done = now in RFC3339, undone = null).
- `DeleteIncidentTodo` → `DeleteIncidentTodo`.
- `AddIncidentTodo(id, content, handle)` already exists and takes any handle —
  reused from the panel's add flow.

## Features

### 1. Commander → anyone (`I`)

`I` opens the user picker with the **current user pinned at the top** as the
default selection:

- Enter immediately → take command yourself (fast path; one extra keystroke vs.
  today).
- Type to search → select any user → assign.

Selection routes through the existing confirmation modal, then
`SetIncidentCommander(incidentID, userID)` (already generic — only the old UI
pinned it to `CurrentUser`). `confirmAssignCommander` is reworked to open the
picker instead of going straight to confirm.

### 2. To-do panel (`T`)

**Behavior change:** `T` today quick-adds a to-do assigned to you. It now opens
the to-do panel (building block B). Add is still available inside the panel
(`a`), and now lets you pick the assignee. This makes `T` the single home for
to-do triage.

### 3. To-do assign-to-other

Falls out of (1) + (2): the panel's add flow routes through the user picker, and
`AddIncidentTodo` already accepts any handle.

### 4. Responders + People (read-only)

The incident detail view is currently a raw `json.MarshalIndent(Raw)` dump with
no commander/responders surfaced. Two changes:

- **Enrich the fetch:** add `Include: [users]` to the `GetIncident` call (it is
  currently bare). This carries the incident's user objects in the response
  `included` array.
- **Render a People header** above the JSON dump:
  - Commander (resolved handle)
  - Responders (resolved handles where possible)
  - Declared-by / Created-by

**Caveat — responder name resolution is best-effort.** Responders are a distinct
object type (`incident_responders`); there is no `include=responders`, so the
responder relationship gives ids that may not map cleanly to the `included`
users. The exact id↔user mapping will be **confirmed against the dev org during
implementation**; if an id does not resolve, its raw id is shown rather than
faking a name. Commander and created/declared-by resolve cleanly regardless — so
this change also fixes the "detail is opaque JSON" problem as a side effect.

## Keybindings (incidents view)

| Key | Before | After |
|-----|--------|-------|
| `r` | state change | unchanged |
| `v` | severity change | unchanged |
| `I` | take command (me) | **open user picker** (you pinned on top) |
| `T` | quick-add to-do (me) | **open to-do panel** (add/complete/delete inside) |

New page-local keys inside the to-do panel: `a` add, `c`/`space` complete, `d`
delete, `esc` back. User picker: type to search, Up/Down select, Enter confirm,
Esc cancel.

## Provider surface (all implementations)

New methods on the `Provider` interface, implemented in `live`, `demo`,
`errored`, `cached`:

- `ListUsers(ctx, query) ([]User, error)`
- `IncidentTodos(ctx, incidentID) ([]Todo, error)`
- `SetIncidentTodoCompleted(ctx, incidentID, todoID, done) error`
- `DeleteIncidentTodo(ctx, incidentID, todoID) error`

Demo: `ListUsers` returns a small fixed roster (incl. `demo.user`); `IncidentTodos`
returns a couple of sample items; complete/delete are no-ops returning nil.
Cached: `ListUsers` cached with a short TTL; the write methods drop the incident
cache; `IncidentTodos` cached briefly per incident.
Errored: all return the standard errored-provider failure.

## Testing

- **e2e (SimulationScreen, demo mode):**
  - `I` → picker opens → Enter (self) → confirm → success flash.
  - `I` → picker opens → type → select other → confirm → success flash.
  - `T` → panel opens → `a` add (content + assignee) → appears; `c` toggles
    complete marker; `d` deletes behind confirm.
  - Responders/People header visible in a demo incident's detail.
- **Unit (wire shape, for the untestable writes):** `UpdateIncidentTodo`
  completed/uncompleted body; delete path. Same rationale as the existing
  `commanderUpdateBody` test.

## Rate-limit notes

- `ListUsers` is a new call family — one bounded page (≤50), short cache, and it
  appears in `Budget()`.
- The picker debounces search input so keystrokes don't each fire a request.
- To-do list is fetched once when the panel opens; writes drop the cache so a
  re-open reflects changes.

## Validation before release

The commander-to-other and to-do writes must be exercised against the **dev**
org (not prod) and reverted after, since they cannot be run from the authoring
sandbox. This also confirms the responder id↔user mapping for (4).
