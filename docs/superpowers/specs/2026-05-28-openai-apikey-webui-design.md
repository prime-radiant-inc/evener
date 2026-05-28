# OpenAI API Key from the Webui Credentials Page

Date: 2026-05-28
Status: design approved; ready for implementation plan
Ticket: PRI-1877

## 1. Goal

Let a user set an OpenAI **API key** from the hub webui Credentials page, so
OpenAI supports "API key **or** OAuth" — which the data model already claims
(`providerAuthModes["openai"] = {"apiKey","oauth"}`) but the backend blocks.

Today the Credentials page renders a "Set API key" button for OpenAI, but
saving it fails: `hubAuthController.ApiKeySet` rejects `openai` with
*"openai api keys must be configured via env or hub.env; use
serf/auth/login/start for OAuth"* (`cmd/serf-hub/app_auth.go`). The button
lies. This closes that gap.

## 2. Non-Goals

- **Standalone `serf` CLI reading the hub's `credentials.toml`.** The webui
  stores a key in the hub's credential store; that key applies to
  hub-spawned sessions. The standalone CLI keeps its existing env / OAuth
  resolution. Reconciling the two credential homes is out of scope.
- **Encryption at rest.** The store is verbatim, `chmod 600`, by existing
  design (threat model is filesystem permissions; see the launch-config spec
  §2).
- **Key validation.** No `sk-` format check, no live probe — keys are stored
  verbatim, matching every other provider.
- **A `serf openai login --api-key` CLI path.** The storage change is
  provider-generic, so this is cheap to add later, but YAGNI for now.

## 3. Background: how OpenAI credentials work today

Three potential credential sources for OpenAI:

1. **OAuth record** — written by `serf openai login` (or the webui OAuth flow)
   to `<stateDir>/auth/openai.json` as an `AuthRecord` with `Source: "oauth"`.
   Routes to the **ChatGPT/Codex backend** (`OPENAI_CHATGPT_BASE_URL`).
2. **`OPENAI_API_KEY` env** — routes to the **standard OpenAI API** backend
   (`OPENAI_BASE_URL`). An `sk-` key cannot auth against the ChatGPT backend,
   so API-key and OAuth are not interchangeable creds for one endpoint.
3. **`credentials.toml` file key** — what every *other* provider uses.

The credential is resolved in several places, all following "stored OAuth
record → else `OPENAI_API_KEY` env":

- `internal/auth/openai/service.go` — `Status`, `ResolveRuntimeCredentials`.
- `llm/providers/openai/adapter.go` — `NewFromEnv` (OAuth → ChatGPT backend;
  else `OPENAI_API_KEY` → API backend).
- `cmd/serf-hub/app_auth.go` — `openAIStatus`.
- `cmd/serf-hub/spawn.go` — `openAIStoredOAuthUsable` (launch gating).

**The plumbing for a stored key already exists.** The blocker is narrow:

- `credentials.Store` already supports `openai`: `Get` does file→env,
  plus `Layers`/`List` (`internal/credentials/store.go`).
- `launchconfig.ToEnv` already maps `openai → OPENAI_API_KEY` and injects
  `Creds.APIKeyFor("openai")` into every spawned session
  (`internal/launchconfig/env.go`).
- The adapter's env branch already routes `OPENAI_API_KEY` to the standard
  API backend (`llm/providers/openai/adapter.go`).
- `cmd/serf-hub/main.go` loads **one** `credsStore` and hands the same
  `*credentials.Store` instance to both the spawner (`Creds`) and the auth
  controller (`CredsStore`), so a write is instantly visible to spawns.

OpenAI is blocked at exactly one layer — the hub auth controller:
`ApiKeySet` rejects `openai`, and the OpenAI status path reports only the
OAuth record + live `OPENAI_API_KEY` env (it ignores the store's file layer).

## 4. Design

Unify OpenAI into the shared `credentials.toml` store for its **API-key
layer**, while keeping OAuth as a separate, higher-precedence sign-in. This
matches how the other providers already behave ("independent layers, file
shadows env") and deletes the special-casing.

### 4.1 Storage model and precedence

- A webui-set OpenAI API key is written to `credentials.toml` via the shared
  store (`creds.Set("openai", key)`), exactly like other providers.
- OAuth stays in `<stateDir>/auth/openai.json`.
- **Effective precedence:** `OAuth (openai.json)` > `file key
  (credentials.toml)` > `OPENAI_API_KEY env`. File shadows env, like other
  providers; OAuth sign-in still wins.

This precedence is already what the runtime produces: `ToEnv` injects
`Store.Get("openai")` (file→env) as `OPENAI_API_KEY` into the child, and the
child adapter prefers a usable OAuth record over `OPENAI_API_KEY`.

### 4.2 Changes

Almost everything lives in `cmd/serf-hub/app_auth.go`:

1. **`ApiKeySet`** — drop the `if provider == "openai"` rejection so it falls
   through to `c.creds.Set("openai", value)`.

2. **OpenAI status** (`openAIStatus` / the dedicated openai branch of
   `Status`) — keep the OAuth-record detection, but also read
   `c.creds.Layers("openai")` and populate the layered fields on
   `appwire.AuthStatusResponse`:
   - `ActiveSource`: `oauth` if an OAuth record is present (matching today's
     status, including the expired-but-present case flagged via
     `NeedsRefresh`/`NeedsLogin`), else `file` if the store has a file key,
     else `env` if `OPENAI_API_KEY` is set, else `absent`.
   - `HasStoredFile`, `EnvVar`: from `Layers`.
   - `SignedIn`: true if any source is present.
   - OAuth-specific fields (`Email`, `Expiry`, `NeedsRefresh`, `NeedsLogin`)
     retained when the OAuth record is present.

   OpenAI keeps its dedicated status path rather than being folded into the
   generic `creds.List()` loop, because only OpenAI carries the OAuth
   dimension. `List` continues to handle openai via this enriched status.

3. **`Logout` / Clear** — clear the **effective** layer:
   - effective `oauth` → `DeleteAuth(openai.json)` (existing behavior);
   - effective `file` → `c.creds.Clear("openai")`;
   - `env` cannot be cleared.

   Clearing the effective layer reveals the one beneath (e.g. clear OAuth and a
   stored file key becomes effective).

**Expected zero-change, proven by tests:** `launchconfig.ToEnv`,
`credentials.Store`, the adapter env branch. If any test exposes a gap (e.g.
an openai exclusion hidden in a path not yet read), it is fixed there.

**Frontend (`cmd/serf-hub/templates/partials/credentials.html`):** already
renders "Set API key"/"Replace key"/"Sign in…"/"Clear" for OpenAI and a
layered `sourceLayers` display. No change expected beyond verifying the layers
render once the status response carries `HasStoredFile`/`EnvVar`.

### 4.3 Data flow

- **Set key (webui):** `authApiKeySet("openai", key)` → RPC
  `auth.apiKeySet` → `ApiKeySet` → `creds.Set` → `credentials.toml`. Status
  refresh now shows `activeSource: file` (or `oauth` if OAuth also present,
  with file shown as a shadowed layer).
- **Spawn a session:** `ToEnv` reads `creds.APIKeyFor("openai")` (file→env),
  injects `OPENAI_API_KEY`; child adapter routes it to the API backend —
  unless a usable OAuth record exists, which the child prefers.
- **Clear:** as in §4.2.

## 5. Error handling

- `ApiKeySet` with an empty value: existing generic behavior (rejected with
  "value is required").
- `creds.Set` / `creds.Clear` I/O errors: surfaced to the RPC caller; the
  webui shows the inline editor error + toast (existing `credentials.html`
  error handling).
- A malformed or unreadable `openai.json` during status: the OAuth layer is
  treated as absent and the file/env layers still resolve (status must not
  hard-fail because OAuth state is corrupt).
- **Expired OAuth record + stored file key:** existing runtime behavior is
  preserved. An OAuth record that exists but cannot refresh remains "effective"
  and `ResolveRuntimeCredentials` surfaces a re-login error rather than
  silently falling back to the file key — the user explicitly signed in. This
  change does not make a file key a fallback for an expired OAuth record;
  doing so is a deliberate, separate decision and is out of scope.

## 6. Testing (TDD)

- **`credentials.Store`** (`internal/credentials`): `openai` `Set`/`Get`/
  `Clear`/`Layers` and file→env precedence. Likely already covered
  generically; add explicit openai cases if missing.
- **`app_auth`** (`cmd/serf-hub`):
  - `ApiKeySet("openai", key)` persists to the store (no longer rejected).
  - Status reflects each source and the `oauth > file > env > absent`
    precedence, including shadowed layers (`HasStoredFile`/`EnvVar`).
  - Clear removes the effective layer and reveals the next.
- **`launchconfig.ToEnv`**: a stored openai file key is injected as
  `OPENAI_API_KEY`; with a usable OAuth record present, child preference for
  OAuth is preserved.
- **adapter** (`llm/providers/openai`): `OPENAI_API_KEY` (from the file layer)
  → standard API backend (existing behavior, asserted).
- **web** (`cmd/serf-hub/web_test.go` / app_rpc): the credentials RPC round
  trip for openai set/clear.

## 7. Documentation

This change makes `cmd/serf-hub/README.md` § "Provider Credentials" stale — it
currently frames OpenAI as OAuth-only (no `api_key`) and does not state the
resolution model. Update that section (the evergreen home) as part of the
implementation to capture the durable, cross-cutting knowledge:

- OpenAI supports a stored `api_key` in `credentials.toml` in addition to
  OAuth.
- **Precedence:** `OAuth (openai.json)` > `credentials.toml` file >
  provider env var.
- OpenAI's two backends: OAuth routes to the ChatGPT/Codex backend; an API
  key routes to the standard OpenAI API backend. They are not interchangeable
  for one endpoint.

This dated spec remains the historical design record; it is not maintained as
the code evolves. Only the README section is kept ever-green.

## 8. Decisions (resolved)

- **Credential model:** independent layers like other providers — file key in
  `credentials.toml`, env separate, OAuth on top; shown with shadowing.
- **Validation:** none; store verbatim.
- **Clear semantics:** clear the effective layer only.
- **Scope:** webui-only.
