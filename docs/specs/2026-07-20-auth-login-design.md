# ike auth login — native OAuth2 (v0.3.0)

Status: approved design (2026-07-20). Spike: PASSED (`hack/oauth-spike`).

## Goal

`ike auth login` signs a user into a Datadog org through the browser and keeps
them signed in: no API keys, no hourly token pasting. The flags map onto a
named context, so CLI login and the `:ctx` model converge.

```sh
ike auth login --site datadoghq.eu --org dev            # context "dev"
ike auth login --site datadoghq.eu --subdomain acme-dev --org dev
ike auth login --context dev                            # re-login / rotate
```

## Flow (all proven by the spike)

1. **Dynamic client registration** — `POST /api/v2/oauth2/register`
   (unauthenticated) with `client_name: "ike"` and the local redirect URI →
   `client_id`. Reused on later logins for the same context.
2. **PKCE authorize** — browser opens
   `https://<app-host>/oauth2/v1/authorize` (`app.<site>` or the org's
   subdomain host) with S256 challenge + state; a local server on
   `127.0.0.1:53682/oauth/callback` receives the code.
3. **Token exchange** — `POST /oauth2/v1/token` (public client + verifier) →
   access token (short-lived) + refresh token.
4. **Storage** — one OS-keychain entry per context (`<context>:oauth`): JSON
   `{client_id, access, refresh, expiry}`. The config file records only
   `{site, subdomain?, org?, keychain: true, auth: oauth}`.
5. **Refresh** — lazy, on use: a token source hands the current access token
   to the live provider per request, refreshing when expiry is within 60s
   (single-flight, persisted back to the keychain). A refresh failure surfaces
   as an auth error flash; `ike auth login --context <name>` repairs it.

## Decisions

- **Scopes: none requested.** The token inherits the user's permissions —
  empirically sufficient (pup tokens with the same flow power every ike view
  and write today). Missing permissions degrade to the existing 403 flash.
- **Refresh: lazy on use**, no background goroutine. Simple, testable, and the
  TUI's fetch cadence makes proactive refresh pointless.
- **Client reuse**: the registered `client_id` is stored and reused; a full
  re-registration only happens when none is stored.

## Components

- `internal/auth` — the engine, UI-free and endpoint-injectable for tests:
  `Register`, `Login` (callback server + browser opener), `Refresh`,
  `TokenSet`, and `Source` (lazy-refreshing token supplier with a persistence
  callback).
- `internal/config` — `Context.Org` field; `auth: oauth` shape (valid with
  `keychain: true`, no env vars); keyring gains `SetOAuth`/`GetOAuth`
  (account `<context>:oauth`).
- `internal/data` — `Live` accepts a token *source* (per-request callback)
  besides the static token; everything else unchanged.
- `main.go` — the `auth login` subcommand: run the flow, write keychain +
  config, set `current-context`, print what happened.

## Testing

`internal/auth` is covered end-to-end against `httptest` fakes: registration,
the full authorize/callback/exchange round-trip (browser opener stubbed to hit
the callback directly), refresh, source lazy-refresh + persistence, and error
paths (denied authorize, dead refresh token). Config round-trips the new
fields. The real-browser path was proven by the spike and is re-validated
manually before release.

## Out of scope

- Token revocation on context delete (keychain entry is deleted; server-side
  revocation endpoint unverified).
- The `:ctx` add-form gaining an oauth path (CLI-first; form later).
- Scope selection UI.
