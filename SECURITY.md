# Security Policy

ike handles Datadog credentials (API/application keys, access tokens). By
design they live only in the OS keychain or in environment variables — never
in the config file, never in logs. If you find a way to make ike leak a
credential, that is a security bug.

## Reporting

Use GitHub's **private vulnerability reporting** on this repository
(Security → Report a vulnerability). Please do not open public issues for
suspected credential leaks, and never paste real keys or tokens into an
issue or PR.

Supported version: the latest release only.

## Threat model (what ike defends against)

- **Credential exfiltration via config tampering**: the `site` field is
  validated against the fixed allowlist of Datadog endpoints at load time —
  a config pointing at an unrecognized host is rejected outright, and an
  invalid `DD_SITE` env var falls back to the default site. Credentials can
  only ever be sent to `api.<known Datadog site>`.
- **Secrets at rest**: OS keychain or env vars only; strict YAML parsing
  rejects inline keys; config and log files are 0600 in 0700 directories;
  config writes are atomic.
- **Secrets in transit within the app**: never in URLs, argv, or log lines
  (logs record auth *kind* and context *name* only).
- **Injection**: no shell interpolation anywhere (`exec.Command` argument
  arrays); `o` refuses to open non-https URLs, so hostile API payloads
  can't smuggle `file://`/custom schemes to the OS opener.
- **Dependencies**: `govulncheck` runs in CI on every push and weekly;
  Dependabot watches Go modules and GitHub Actions.

## Accepted risks (documented, not defended)

- `$EDITOR` is trusted — `:ctx` → `e` executes it on the config file. It is
  the user's own environment on their own machine.
- Log-search queries are written to the debug log (0600). Query *text* may
  be sensitive in some orgs; the log never contains credentials.
- OS keychain semantics apply: any process the OS lets read the "ike"
  keychain service can read the keys (on macOS this prompts the user).
