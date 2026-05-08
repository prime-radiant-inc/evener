# Serf OpenAI Auth Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Serf-owned OpenAI login with explicit `serf openai login|logout|status` commands, browser PKCE, localhost callback, and manual pasted-URL fallback for remote/server use.

**Architecture:** A dedicated Serf OpenAI auth package owns OAuth orchestration, storage, refresh, and status reporting. The OpenAI provider adapter remains focused on Responses API calls and resolves a bearer token from env auth first, then Serf-owned state-dir auth.

**Tech Stack:** Go / net/http / OAuth PKCE / local callback server / JSON file storage in Serf state dir

**Spec:** `docs/superpowers/specs/2026-05-07-serf-openai-auth-design.md`

---

## File Map

### New files
| File | Responsibility |
|------|---------------|
| `internal/auth/openai/config.go` | OAuth endpoints, client config, redirect URI rules |
| `internal/auth/openai/pkce.go` | PKCE verifier/challenge/state generation |
| `internal/auth/openai/server.go` | Localhost callback listener |
| `internal/auth/openai/manual.go` | Pasted redirect URL parsing and validation |
| `internal/auth/openai/tokens.go` | Code exchange and refresh HTTP calls |
| `internal/auth/openai/storage.go` | Auth file load/save/delete |
| `internal/auth/openai/claims.go` | Optional token claim parsing |
| `internal/auth/openai/service.go` | Login/logout/status/runtime credential orchestration |
| `internal/auth/openai/*_test.go` | Unit tests for the new package |
| `cmd/serf/openai_login.go` | `serf openai login` command |
| `cmd/serf/openai_logout.go` | `serf openai logout` command |
| `cmd/serf/openai_status.go` | `serf openai status` command |

### Modified files
| File | Changes |
|------|---------|
| `cmd/serf/main.go` | Add `openai` subcommand dispatch |
| `cmdutil/*.go` | Reuse or expose state-dir resolution helpers if needed |
| `llm/providers/openai/adapter.go` | Replace env-only lookup with env-plus-state credential resolution |
| `llm/providers/openai/adapter_test.go` | Add stored-auth and refresh coverage |
| `cmd/serf/*_test.go` | CLI coverage for login/logout/status |
| `README.md` | Document OpenAI commands and auth behavior |

---

## Chunk 1: Auth Package Foundations

### Task 1: Add the persisted auth model and storage

**Files:**
- Create: `internal/auth/openai/storage.go`
- Create: `internal/auth/openai/storage_test.go`

- [ ] **Step 1: Write the failing storage tests**

Cover:
- save writes `auth/openai.json` under the resolved state dir
- save is atomic
- load returns `not found` cleanly
- delete removes the record cleanly
- malformed JSON returns a corruption error

- [ ] **Step 2: Run the storage tests to verify they fail**

Run: `go test ./internal/auth/openai -run 'Test(AuthStorage|Load|Save|Delete)'`
Expected: FAIL because the package/files do not exist yet.

- [ ] **Step 3: Implement the storage model**

Add:
- a versioned auth record struct
- helper to derive `<state-dir>/auth/openai.json`
- atomic write via temp file + rename
- owner-only file permissions

- [ ] **Step 4: Run the storage tests to verify they pass**

Run: `go test ./internal/auth/openai -run 'Test(AuthStorage|Load|Save|Delete)'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/auth/openai/storage.go internal/auth/openai/storage_test.go
git commit -m "feat: add OpenAI auth storage"
```

### Task 2: Add PKCE and manual redirect parsing helpers

**Files:**
- Create: `internal/auth/openai/pkce.go`
- Create: `internal/auth/openai/manual.go`
- Create: `internal/auth/openai/pkce_test.go`
- Create: `internal/auth/openai/manual_test.go`

- [ ] **Step 1: Write the failing PKCE and manual parsing tests**

Cover:
- verifier and challenge shapes
- random state generation
- pasted URL parsing extracts `code` and `state`
- missing `code`
- mismatched `state`
- invalid URL shape

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/auth/openai -run 'Test(PKCE|Manual)'`
Expected: FAIL because the helpers do not exist yet.

- [ ] **Step 3: Implement PKCE and manual parsing**

Keep the API minimal:
- `GeneratePKCE()`
- `GenerateState()`
- `ParseRedirectURL(raw string)`
- `ValidateState(expected, got string)`

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/auth/openai -run 'Test(PKCE|Manual)'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/auth/openai/pkce.go internal/auth/openai/manual.go internal/auth/openai/pkce_test.go internal/auth/openai/manual_test.go
git commit -m "feat: add OpenAI auth PKCE helpers"
```

### Task 3: Add config and authorize URL construction

**Files:**
- Create: `internal/auth/openai/config.go`
- Create: `internal/auth/openai/config_test.go`

- [ ] **Step 1: Write the failing config tests**

Cover:
- authorize URL contains required query params
- redirect URI is localhost callback
- state and PKCE challenge are propagated
- browser-open flag does not affect URL generation

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/auth/openai -run 'Test(AuthorizeURL|Config)'`
Expected: FAIL because the config helpers do not exist yet.

- [ ] **Step 3: Implement config helpers**

Define:
- issuer base URL
- client ID
- scope set
- redirect path
- timeout defaults
- authorize URL builder

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/auth/openai -run 'Test(AuthorizeURL|Config)'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/auth/openai/config.go internal/auth/openai/config_test.go
git commit -m "feat: add OpenAI auth config"
```

---

## Chunk 2: OAuth Network Flows

### Task 4: Add token exchange and refresh client

**Files:**
- Create: `internal/auth/openai/tokens.go`
- Create: `internal/auth/openai/tokens_test.go`

- [ ] **Step 1: Write the failing token client tests**

Use `httptest.Server` to cover:
- authorization code exchange success
- refresh success
- compact error propagation on non-2xx
- invalid response decoding

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/auth/openai -run 'Test(TokenExchange|TokenRefresh)'`
Expected: FAIL because the client does not exist yet.

- [ ] **Step 3: Implement the token client**

Implement:
- request structs
- response structs
- code exchange
- refresh exchange
- compact provider error extraction

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/auth/openai -run 'Test(TokenExchange|TokenRefresh)'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/auth/openai/tokens.go internal/auth/openai/tokens_test.go
git commit -m "feat: add OpenAI token exchange client"
```

### Task 5: Add localhost callback listener

**Files:**
- Create: `internal/auth/openai/server.go`
- Create: `internal/auth/openai/server_test.go`

- [ ] **Step 1: Write the failing callback listener tests**

Cover:
- successful callback capture
- state mismatch rejection
- timeout behavior
- clean shutdown

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/auth/openai -run 'Test(CallbackServer|LocalCallback)'`
Expected: FAIL because the listener does not exist yet.

- [ ] **Step 3: Implement the callback listener**

Requirements:
- bind localhost
- expose redirect URI
- capture `code` and `state`
- return a small human-readable success/failure response
- support timeout/cancel

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/auth/openai -run 'Test(CallbackServer|LocalCallback)'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/auth/openai/server.go internal/auth/openai/server_test.go
git commit -m "feat: add OpenAI callback listener"
```

### Task 6: Add optional claim parsing

**Files:**
- Create: `internal/auth/openai/claims.go`
- Create: `internal/auth/openai/claims_test.go`

- [ ] **Step 1: Write the failing claim parsing tests**

Cover:
- parse email from `id_token` when available
- parse account/workspace metadata if present
- handle absent or malformed JWTs without panicking

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/auth/openai -run 'Test(TokenClaims|IDToken)'`
Expected: FAIL because the parser does not exist yet.

- [ ] **Step 3: Implement minimal claim parsing**

Keep this best-effort:
- decode claims only for status display
- do not make model-call auth depend on claim parsing success

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/auth/openai -run 'Test(TokenClaims|IDToken)'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/auth/openai/claims.go internal/auth/openai/claims_test.go
git commit -m "feat: add OpenAI auth claim parsing"
```

---

## Chunk 3: Service Orchestration

### Task 7: Add login/status/logout service

**Files:**
- Create: `internal/auth/openai/service.go`
- Create: `internal/auth/openai/service_test.go`

- [ ] **Step 1: Write the failing service tests**

Cover:
- login succeeds via callback path
- login succeeds via manual pasteback path
- browser open failure is non-fatal
- status reflects signed-out state
- status reflects stored OAuth state
- logout deletes stored auth

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/auth/openai -run 'Test(Service|Login|Logout|Status)'`
Expected: FAIL because the service does not exist yet.

- [ ] **Step 3: Implement the service orchestration**

The service should:
- construct login sessions
- choose callback vs pasteback completion
- persist resulting tokens
- provide status summaries
- delete stored state on logout

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/auth/openai -run 'Test(Service|Login|Logout|Status)'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/auth/openai/service.go internal/auth/openai/service_test.go
git commit -m "feat: add OpenAI auth service"
```

### Task 8: Add refresh-aware runtime credential resolution

**Files:**
- Modify: `internal/auth/openai/service.go`
- Extend: `internal/auth/openai/service_test.go`

- [ ] **Step 1: Write the failing refresh tests**

Cover:
- `OPENAI_API_KEY` wins over stored auth
- valid stored token is returned unchanged
- near-expiry token refreshes before use
- permanent refresh failure produces a re-login error

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/auth/openai -run 'Test(RuntimeCredentials|Refresh)'`
Expected: FAIL because runtime resolution is not implemented yet.

- [ ] **Step 3: Implement runtime credential resolution**

Expose a narrow method such as:
- `ResolveBearerToken(ctx, stateDir string) (token string, source string, err error)`

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/auth/openai -run 'Test(RuntimeCredentials|Refresh)'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/auth/openai/service.go internal/auth/openai/service_test.go
git commit -m "feat: add OpenAI auth refresh resolution"
```

---

## Chunk 4: CLI Commands

### Task 9: Add `serf openai login`

**Files:**
- Create: `cmd/serf/openai_login.go`
- Create or Modify: `cmd/serf/main_test.go`

- [ ] **Step 1: Write the failing CLI login tests**

Cover:
- command dispatch reaches the OpenAI login handler
- URL is printed even if browser open fails
- manual paste mode is accepted

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/serf -run 'Test(OpenAILogin|OpenAISubcommand)'`
Expected: FAIL because the command does not exist yet.

- [ ] **Step 3: Implement the login command**

Requirements:
- explicit command help
- shared state-dir resolution
- browser-open attempt plus URL printing
- fallback prompt for pasted redirect URL

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/serf -run 'Test(OpenAILogin|OpenAISubcommand)'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf/openai_login.go cmd/serf/main_test.go
git commit -m "feat: add serf openai login"
```

### Task 10: Add `serf openai logout` and `serf openai status`

**Files:**
- Create: `cmd/serf/openai_logout.go`
- Create: `cmd/serf/openai_status.go`
- Modify: `cmd/serf/main_test.go`

- [ ] **Step 1: Write the failing logout/status tests**

Cover:
- status when signed out
- status when env auth is active
- status when stored auth is active
- logout removes stored auth

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/serf -run 'Test(OpenAIStatus|OpenAILogout)'`
Expected: FAIL because the commands do not exist yet.

- [ ] **Step 3: Implement the commands**

Keep status output compact and script-friendly:
- auth source
- signed in/out
- email/account metadata when present
- expiry summary

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/serf -run 'Test(OpenAIStatus|OpenAILogout)'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf/openai_logout.go cmd/serf/openai_status.go cmd/serf/main_test.go
git commit -m "feat: add serf openai logout and status"
```

### Task 11: Wire top-level `openai` dispatch

**Files:**
- Modify: `cmd/serf/main.go`
- Modify: `cmd/serf/main_test.go`

- [ ] **Step 1: Write the failing dispatch tests**

Cover:
- `serf openai` shows help
- `serf openai login` dispatches correctly
- unknown `serf openai <subcommand>` fails cleanly

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/serf -run 'Test(OpenAISubcommandDispatch|OpenAIHelp)'`
Expected: FAIL because dispatch is incomplete.

- [ ] **Step 3: Implement subcommand dispatch**

Add explicit parsing before the main agent flags are processed.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/serf -run 'Test(OpenAISubcommandDispatch|OpenAIHelp)'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf/main.go cmd/serf/main_test.go
git commit -m "feat: dispatch OpenAI auth subcommands"
```

---

## Chunk 5: Runtime Integration

### Task 12: Make the OpenAI adapter use the new credential resolver

**Files:**
- Modify: `llm/providers/openai/adapter.go`
- Modify: `llm/providers/openai/adapter_test.go`

- [ ] **Step 1: Write the failing adapter resolution tests**

Cover:
- env API key still works exactly as before
- stored OAuth token is used when env key is absent
- refresh is attempted for near-expiry stored auth
- refresh failure is surfaced clearly

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./llm/providers/openai -run 'Test(NewFromEnv|CredentialResolution|Refresh)'`
Expected: FAIL because the adapter is still env-only.

- [ ] **Step 3: Implement adapter integration**

Keep the adapter contract narrow:
- do not duplicate token refresh logic here
- call the OpenAI auth service/helper to resolve a bearer token

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./llm/providers/openai -run 'Test(NewFromEnv|CredentialResolution|Refresh)'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add llm/providers/openai/adapter.go llm/providers/openai/adapter_test.go
git commit -m "feat: resolve OpenAI auth from Serf state"
```

### Task 13: Ensure run/serve/tui use the same state-dir-backed auth

**Files:**
- Modify: `cmd/serf/run.go`
- Modify: `cmd/serf/serve.go`
- Modify: `cmd/serf-tui/embedded.go`
- Modify tests as needed

- [ ] **Step 1: Write the failing integration tests**

Cover:
- runtime session can find stored OpenAI auth in the resolved state dir
- explicit `--state-dir` overrides default
- serve and embedded TUI construction do not regress provider setup

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/serf ./cmd/serf-tui -run 'Test(StateDirAuth|Embedded|Serve)'`
Expected: FAIL because the runtime does not yet pass through the new auth expectations.

- [ ] **Step 3: Implement runtime plumbing**

Do the minimum needed so all OpenAI entry points share the same state-dir auth source.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/serf ./cmd/serf-tui -run 'Test(StateDirAuth|Embedded|Serve)'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf/run.go cmd/serf/serve.go cmd/serf-tui/embedded.go
git commit -m "feat: share OpenAI auth across runtime entry points"
```

---

## Chunk 6: Documentation and Final Verification

### Task 14: Update README and help text

**Files:**
- Modify: `README.md`
- Modify: `cmd/serf/main.go`

- [ ] **Step 1: Write the failing documentation checks**

Define the checks:
- README mentions `serf openai login|logout|status`
- README explains browser and pasted-URL fallback
- README explains precedence of `OPENAI_API_KEY`

- [ ] **Step 2: Run the checks to verify they fail**

Run: `rg -n "serf openai login|pasted redirect|OPENAI_API_KEY" README.md cmd/serf/main.go`
Expected: missing or incomplete matches.

- [ ] **Step 3: Update the docs**

Add:
- command usage
- state-dir behavior
- remote/server pasteback usage example

- [ ] **Step 4: Run the checks to verify they pass**

Run: `rg -n "serf openai login|pasted redirect|OPENAI_API_KEY" README.md cmd/serf/main.go`
Expected: all intended references present.

- [ ] **Step 5: Commit**

```bash
git add README.md cmd/serf/main.go
git commit -m "docs: document Serf OpenAI login"
```

### Task 15: Run the full verification suite

**Files:**
- No code changes expected

- [ ] **Step 1: Run focused auth and CLI suites**

Run:
```bash
go test ./internal/auth/openai ./llm/providers/openai ./cmd/serf ./cmd/serf-tui
```
Expected: PASS

- [ ] **Step 2: Run broader regression coverage**

Run:
```bash
go test ./llm/... ./cmdutil ./cmd/serf/... ./cmd/serf-tui/...
```
Expected: PASS

- [ ] **Step 3: Inspect git diff**

Run:
```bash
git status --short
git diff --stat
```
Expected: only intended OpenAI auth files and docs changed.

- [ ] **Step 4: Commit final fixups if needed**

```bash
git add <intended-files>
git commit -m "test: finalize OpenAI auth integration"
```

---

## Risks to Watch

- OpenAI’s sanctioned client configuration may constrain redirect URIs or scopes differently than expected
- Stored auth must not silently shadow `OPENAI_API_KEY`
- Manual pasteback validation must reject the wrong redirect without leaking secrets
- Refresh failures must degrade into a clear re-login path, not a confusing 401 loop

## Execution Notes

- Do not revive the earlier “reuse Codex auth.json” implementation; this plan assumes a Serf-owned auth subsystem only
- Keep the OpenAI adapter thin; avoid moving OAuth orchestration into provider request code
- Preserve current API-key usage and tests while adding OAuth behavior
