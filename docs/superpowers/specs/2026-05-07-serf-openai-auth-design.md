# Serf OpenAI Auth Design

Add a first-class Serf-owned OpenAI login flow so users can authenticate Serf directly with OpenAI/ChatGPT-backed OAuth instead of pasting API keys or reusing Codex state.

## Decisions

- **User-facing commands:** `serf openai login`, `serf openai logout`, `serf openai status`
- **Primary flow:** browser-based PKCE with localhost callback
- **Remote fallback:** always print the auth URL and accept a pasted final redirect URL when the callback cannot be reached
- **Storage:** Serf-owned auth state under the resolved Serf state directory
- **Future-proofing:** provider-agnostic internal seams, but only OpenAI is implemented in v1
- **Credential precedence:** `OPENAI_API_KEY` overrides stored Serf OpenAI OAuth state

## Goals

- Give Serf its own explicit OpenAI login UX
- Support both laptop and remote/server workflows
- Keep the OpenAI adapter focused on model requests, not OAuth orchestration
- Preserve current API-key-based usage
- Design storage so OS keychain support can be added later without breaking the high-level format

## Non-Goals

- Reusing Codex auth state or `~/.codex/auth.json`
- Building a generic `serf auth` command family in v1
- Adding non-OpenAI provider login in this iteration
- Building TUI-native account management before the CLI flow exists
- Depending on OS keychain in v1

## User Experience

### `serf openai login`

Default behavior:

1. Resolve Serf state dir and OpenAI OAuth client configuration
2. Generate PKCE verifier/challenge and anti-CSRF state
3. Start a short-lived localhost callback listener
4. Build the authorize URL
5. Try to open the browser locally
6. Always print the URL to stdout/stderr for manual use
7. Wait for either:
   - a successful localhost callback, or
   - a pasted redirect URL from the user
8. Exchange the authorization code for tokens
9. Persist auth state atomically
10. Print a signed-in summary including email/account metadata when available

Pasteback UX:

- If the callback does not arrive, Serf prompts for the final redirect URL
- The user can copy the redirect URL from a browser running elsewhere and paste it into the terminal
- Serf extracts `code` and `state`, validates them, and continues the exchange

### `serf openai status`

Prints:

- auth source: `env` or `state-dir`
- signed-in vs signed-out
- email if present
- account/workspace identifier if present
- token expiry/refresh status
- whether re-login is required

If `OPENAI_API_KEY` is set, status reports that env auth is active even if a stored OAuth session also exists.

### `serf openai logout`

- Deletes the local Serf-owned OpenAI auth record
- Prints whether a session was removed
- Remote revocation is optional and should only be added if the official OpenAI docs confirm a supported revoke flow for this client type

## Architecture

Keep the CLI explicit but isolate auth internals from the LLM provider adapter.

### CLI surface

Modify `cmd/serf/main.go` to dispatch a new top-level `openai` command family:

- `serf openai login`
- `serf openai logout`
- `serf openai status`

These commands should live in dedicated command files rather than bloating `main.go` with all OAuth logic.

### OpenAI auth package

Add a Serf-owned package responsible for the full auth lifecycle. A reasonable layout is:

- `internal/auth/openai/config.go`
- `internal/auth/openai/pkce.go`
- `internal/auth/openai/server.go`
- `internal/auth/openai/manual.go`
- `internal/auth/openai/tokens.go`
- `internal/auth/openai/storage.go`
- `internal/auth/openai/claims.go`
- `internal/auth/openai/service.go`

Responsibilities:

- `config.go`
  - issuer URLs
  - client ID/config
  - redirect URI construction
  - timeout settings

- `pkce.go`
  - code verifier generation
  - code challenge generation
  - state generation

- `server.go`
  - localhost callback listener lifecycle
  - callback request parsing
  - success/error response pages or plain-text responses

- `manual.go`
  - parse pasted redirect URLs
  - extract `code` and `state`
  - validate shape before exchange

- `tokens.go`
  - authorization code exchange
  - refresh token exchange
  - response decoding

- `claims.go`
  - parse `id_token` claims if OpenAI returns them
  - expose email/account/workspace metadata helpers

- `storage.go`
  - load/save/delete auth record
  - atomic rewrite
  - permission tightening

- `service.go`
  - orchestration for login, logout, status, and runtime token resolution

### OpenAI adapter integration

`llm/providers/openai/adapter.go` should not own OAuth flows. It should resolve credentials through a narrow bearer-token provider interface or helper that:

- checks `OPENAI_API_KEY` first
- otherwise loads Serf-owned OpenAI auth from the state dir
- refreshes if needed
- returns a current bearer token for request headers

The adapter’s request/response mapping to `/v1/responses` stays unchanged.

## Storage Model

Store OpenAI auth in the Serf state dir, not in `~/.codex`.

Recommended path:

- `<resolved-state-dir>/auth/openai.json`

Make the schema versioned and Serf-owned:

```json
{
  "version": 1,
  "provider": "openai",
  "source": "oauth",
  "obtained_at": "2026-05-07T23:00:00Z",
  "token_type": "Bearer",
  "scope": "openid profile email offline_access",
  "access_token": "...",
  "refresh_token": "...",
  "id_token": "...",
  "expiry": "2026-05-08T00:00:00Z",
  "email": "user@example.com",
  "account_id": "acct_123",
  "workspace_id": "ws_123"
}
```

Notes:

- The file format should be designed so secret-bearing fields can later move into keychain storage while preserving the same top-level metadata shape
- File writes should be atomic
- File permissions should be owner-only

## Token Lifecycle

### Login

- perform auth code exchange
- persist the token bundle
- decode useful claims and store denormalized metadata for fast `status`

### Request-time resolution

- if `OPENAI_API_KEY` is set, use it and do not mutate state-dir auth
- otherwise, if stored OAuth auth exists:
  - if the access token is fresh, use it
  - if near expiry and refresh token exists, refresh first
  - on refresh success, rewrite storage atomically
  - on permanent refresh failure, mark auth unusable and surface a clear re-login error

### Logout

- delete the local auth file
- leave env-based auth unaffected

## State Dir Integration

The auth subsystem must use the same state-dir resolution rules Serf already uses:

- `--state-dir`
- `SERF_STATE_DIR`
- XDG-derived default

This keeps auth portable across local machines, containers, and servers.

The CLI auth commands should accept the same state-dir override behavior so users can inspect or manage the exact runtime profile they intend to use.

## Error Handling

- Browser open failure is non-fatal; print the URL and continue
- Callback timeout should transition into the pasteback path, not fail immediately
- Pasted URL with missing or mismatched `state` should produce a precise validation error
- Token exchange errors should include compact HTTP/provider detail without dumping secrets
- Refresh failures caused by invalid or revoked refresh tokens should become a clear “login expired, run `serf openai login`” error
- Corrupt auth files should be reported as local state corruption, not as generic OpenAI auth failure
- Missing account metadata should not block a successful login if the access token is otherwise valid

## Touch Points

### CLI

- `cmd/serf/main.go`
  - add `openai` subcommand dispatch

- New command files, for example:
  - `cmd/serf/openai_login.go`
  - `cmd/serf/openai_logout.go`
  - `cmd/serf/openai_status.go`

### Shared helpers

- `cmdutil`
  - reuse or extend state-dir resolution helpers if needed
  - avoid duplicating state-path logic inside the new auth package

### Runtime

- `cmd/serf/run.go`
- `cmd/serf/serve.go`
- `cmd/serf-tui/embedded.go`

These must construct runtime sessions so the OpenAI adapter can find the same state-dir-backed auth that the CLI commands write.

### Provider

- `llm/providers/openai/adapter.go`
  - swap env-only lookup for env-plus-state credential resolution

## Testing

### Unit tests

- PKCE verifier/challenge/state generation
- authorize URL construction
- pasted redirect URL parsing
- callback state validation
- auth file load/save/delete behavior
- file permission behavior where testable
- expiry and refresh decision logic
- `id_token`/claim parsing if present

### CLI tests

- `serf openai status` when signed out
- `serf openai status` with env auth
- `serf openai status` with stored auth
- `serf openai logout`
- `serf openai login` happy path with mocked callback exchange
- `serf openai login` manual pasteback path with mocked exchange

### Adapter tests

- env API key wins over stored OAuth
- stored OAuth is used when env key is absent
- expired stored OAuth triggers refresh
- permanent refresh failure surfaces a re-login error

### Integration-style tests

- fake local auth server for authorization code exchange
- localhost callback listener success path
- manual pasteback flow end-to-end without callback

## Rollout

v1 should ship as:

- new explicit OpenAI CLI commands
- runtime support for stored Serf OpenAI OAuth credentials
- no TUI-specific account UI beyond benefitting from the shared runtime auth state

The TUI and server flows can later add richer account surfaces on top of the same stored-auth subsystem.

## Open Questions

These must be verified before implementation:

- Serf’s sanctioned OpenAI OAuth client ID and redirect URI allow-list
- exact token endpoint contract and supported scopes for third-party Serf login
- whether the provider returns useful `id_token` claims for account/workspace display
- whether remote token revocation is supported and desirable in v1
