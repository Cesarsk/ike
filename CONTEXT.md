# CONTEXT.md — ike glossary

Canonical vocabulary for this project. Use these words with exactly these
meanings in code, docs, issues, and commit messages.

- **Context** — one Datadog organization a user can point ike at: a name,
  a site, and credentials. Named after kubeconfig/k9s contexts. Switching
  context is a hard boundary: nothing carries across (data, budget,
  history). *Not* to be confused with a Go `context.Context`.
- **Site** — a Datadog regional endpoint (`datadoghq.com`, `datadoghq.eu`,
  `us3./us5./ap1./ap2.datadoghq.com`, `ddog-gov.com`). Fixed list; part of
  a Context.
- **Credentials** — either a **key pair** (API key + application key) or an
  **access token** (OAuth2 bearer token / PAT). Exactly one shape per
  Context. Never stored in the config file.
- **Provider** — a source of rows for resources. Live (real Datadog API)
  and Demo (built-in fake data) are providers; the UI cannot tell them
  apart.
- **Resource** — a navigable Datadog object type: monitors, incidents,
  SLOs, logs, dashboards. Each is one table view with columns, a TTL and
  command aliases.
- **View** — what's on screen for one resource (table + filter + selection).
- **Detail** — the full-object page opened with enter on a row.
- **Filter** — client-side row narrowing with `/`. In Logs, `/` is instead
  a **query**: a Datadog search expression evaluated server-side.
- **Budget** — remaining Datadog API rate-limit headroom for the active
  Context, as reported by the API itself. Shown in the header.
- **Cache** — per-resource, per-query TTL store; navigating between views
  is free, only expiry or an explicit refresh spends Budget.
- **Navigation stack** — the esc-pops-back history of views (k9s page-stack
  semantics). "Back" always means popping this stack.
- **Drill-down** — jumping from one resource to a related view of another
  (monitor → its logs via `l`), carrying derived context (the log query)
  and pushing the origin onto the navigation stack.
- **Demo mode** — the offline run mode (`--demo`): demo providers, in-memory
  contexts, no network, no keychain.
