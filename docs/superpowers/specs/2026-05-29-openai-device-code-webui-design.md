# Device-Code OAuth Login for OpenAI in the Hub Webui

Date: 2026-05-29
Status: design approved; ready for implementation plan
Ticket: PRI-1878

## 1. Goal

Let a user sign in to OpenAI from the hub webui with no browser redirect, so it
works when the hub is remote (e.g. `paradise-park:9180` reached from a laptop).
Expose the existing **device-code** flow (`internal/auth/openai/device.go`) in
the Credentials page. Device-code becomes the webui "Sign in…" for all hosts;
if OpenAI reports the client doesn't support device-code, fall back to today's
paste-back flow automatically.

## 2. Non-Goals

- **Changing the OAuth client or its redirect URIs.** Sign-in uses OpenAI's
  Codex public client (`ClientID app_EMoamEEZ73f0CkXaXp7hrann`), whose redirect
  is loopback-locked (`http://localhost:1455/auth/callback`). We don't own it,
  so we cannot point a redirect at the hub. Device-code sidesteps redirects
  entirely.
- **Touching the CLI device flow** (`serf openai login --device`) or the
  existing redirect/paste-back code, beyond wiring paste-back in as the
  fallback.
- **Background polling goroutines in the hub.** Polling is UI-driven (§4.2).

## 3. Background

The device-code building blocks already exist and are used by the CLI:

- `RequestDeviceCode(ctx, client, cfg) (DeviceCode, error)` — returns
  `{VerificationURL (https://auth.openai.com/codex/device), UserCode,
  DeviceAuthID, Interval}`. On a 404 it returns a "device-code login is not
  enabled for this OpenAI client" error.
- `PollDeviceAuth(ctx, client, cfg, dc) (DeviceCodeSuccess, error)` — **blocks**
  up to 15 minutes, polling at `dc.Interval`; 403/404 mean "pending".
- `ExchangeDeviceCode(ctx, client, cfg, authCode, codeVerifier) (TokenSet, error)`.

The hub auth RPCs follow one pattern: `appserver.HandleTyped(server.Router(),
appwire.MethodSerfAuth…, handler)` calling a `hubAuthController` method, with
`notifyAuthUpdated(server, provider, activeSource)` pushing `serf/auth/updated`
after a state change. `LoginComplete` shows the exchange→claims→`SaveAuth`
pattern: exchange tokens, `authRecordFromTokens`, enrich via
`ParseIDTokenClaims`, `SaveAuth(c.stateDir, record)`.

A blocking 15-minute poll cannot be an RPC, so the webui needs a non-blocking
polling model.

## 4. Design

### 4.1 Sign-in flow and fallback

The Credentials page "Sign in…" (OAuth) action runs the device-code flow.
If `DeviceStart` reports the client doesn't support device-code, the UI
transparently switches to the existing paste-back flow (`authLoginStart` →
paste redirect URL → `authLoginComplete`). The redirect/paste-back code is
unchanged; it just becomes the fallback.

### 4.2 Polling model — UI-driven single-poll

- `device/start` requests a code and returns it immediately.
- The browser polls `device/poll` every `intervalSeconds`; **each call makes
  one attempt** against OpenAI and returns `pending` / `authorized` / `expired`.
- On `authorized`, the hub exchanges + saves before responding.

No long-lived hub goroutines, no blocking RPCs, and a hub restart simply ends
the in-flight attempt (the user clicks "Start again"). The hub tracks a
per-flow start time and enforces the same ~15-minute window the CLI uses.

### 4.3 `internal/auth/openai/device.go`

Extract a single-attempt helper and refactor the blocking loop to use it, so
both paths share one request implementation and behavior is unchanged:

```go
// PollDeviceAuthOnce makes one poll attempt. pending is true when the server
// reports the user has not finished yet (403/404); a non-2xx that is not
// pending is returned as err.
func PollDeviceAuthOnce(ctx context.Context, client *http.Client, cfg Config, dc DeviceCode) (DeviceCodeSuccess, bool, error)
```

`pollDeviceAuth` (the blocking loop) calls `PollDeviceAuthOnce` per iteration;
its 403/404→sleep→retry and timeout semantics are preserved.

Add a sentinel for the "not enabled" case so callers can branch without string
matching:

```go
var ErrDeviceCodeNotEnabled = errors.New("device-code login is not enabled for this OpenAI client")
```

`RequestDeviceCode` returns `%w`-wrapped `ErrDeviceCodeNotEnabled` on the 404.

### 4.4 Hub RPC + controller

New appwire methods/types (`internal/appwire/types.go`):

- `MethodSerfAuthDeviceStart = "serf/auth/device/start"`
- `MethodSerfAuthDevicePoll  = "serf/auth/device/poll"`
- `AuthDeviceStartParams{ Provider string }`
- `AuthDeviceStartResponse{ Provider, FlowID, UserCode, VerificationURL string; IntervalSeconds int; Fallback bool }`
- `AuthDevicePollParams{ Provider, FlowID string }`
- `AuthDevicePollResponse{ State string; Status AuthStatusResponse }` — `State`
  ∈ `pending|authorized|expired`; `Status` populated on `authorized`.

`hubAuthController` (`cmd/serf-hub/app_auth.go`):

- Add a `deviceFlows map[string]deviceFlow` guarded by the existing `mu`, where
  `deviceFlow{ Provider string; Code authopenai.DeviceCode; StartedAt time.Time }`.
- Inject device funcs on the controller for testability, mirroring the existing
  `exchangeCode` seam: `requestDeviceCode`, `pollDeviceOnce`, `exchangeDevice`
  (defaulting to `authopenai.RequestDeviceCode` / `PollDeviceAuthOnce` /
  `ExchangeDeviceCode`).
- `DeviceStart(params)`: provider must be `openai`. Call `requestDeviceCode`.
  If `errors.Is(err, authopenai.ErrDeviceCodeNotEnabled)` → return
  `{Fallback: true}`. On other errors → return the error. On success → store a
  `deviceFlow` keyed by a generated `flowId` (reusing the existing
  `authopenai.GenerateState()` helper) with `StartedAt = c.now()` and return
  `{FlowID, UserCode, VerificationURL, IntervalSeconds}` (`IntervalSeconds` =
  `int(dc.Interval / time.Second)`).
- `DevicePoll(ctx, params)`: look up the flow (missing → `{State:"expired"}`).
  If `c.now().Sub(flow.StartedAt) >= 15*time.Minute` → drop flow,
  `{State:"expired"}`. Else `pollDeviceOnce`: `pending` → `{State:"pending"}`;
  success → `exchangeDevice` → `authRecordFromTokens` → enrich via
  `ParseIDTokenClaims` → `SaveAuth(c.stateDir, record)` → drop flow →
  `{State:"authorized", Status: openAIStatus()}`.

`app_rpc.go`: register both methods; on `authorized` from `DevicePoll`, call
`notifyAuthUpdated(server, "openai", resp.Status.ActiveSource)`.

### 4.5 Frontend (`credentials.html` + launchconfig client)

Add `authDeviceStart`/`authDevicePoll` to the launchconfig RPC client wrappers
(alongside the existing `authLoginStart`/`authLoginComplete`/`authApiKeySet`/
`authLogout`/`authList`).

In `credentials.html`, the `oauth` action calls `authDeviceStart(provider)`:
- `fallback === true` → run the existing paste-back path unchanged.
- otherwise → open a new `device` editor kind: show the **user code**
  prominently, a link "Open OpenAI and enter this code" → `verificationUrl`
  (also `window.open`ed), and a "Waiting for you to authorize…" state. Start an
  interval timer that calls `authDevicePoll(provider, flowId)` every
  `intervalSeconds`:
  - `pending` → keep waiting;
  - `authorized` → stop timer, success toast, `refresh()`;
  - `expired` → stop timer, show "Code expired — Start again" (re-runs
    `authDeviceStart`).
- Cancel (or closing the editor) stops the timer.

### 4.6 Data flow

1. Click "Sign in…" → `authDeviceStart("openai")`.
2. Hub `RequestDeviceCode` → store flow → return code + interval (or
   `fallback`).
3. UI shows the code + link; user opens `auth.openai.com/codex/device`, enters
   the code, approves.
4. UI polls `authDevicePoll` each interval → hub `PollDeviceAuthOnce`.
5. On success the hub exchanges + saves the OAuth record, returns `authorized`
   + status; UI refreshes and the row shows `oauth` (using PRI-1877's layered
   display). `serf/auth/updated` fires.

## 5. Error handling

- **Not enabled:** `DeviceStart` returns `{Fallback:true}` → UI uses paste-back.
- **Expiry:** flow older than 15 min (or unknown flowId) → `DevicePoll` returns
  `expired`; UI offers "Start again". The flow entry is dropped.
- **Poll/exchange failure:** non-pending poll error or exchange error surfaces
  as the RPC error; UI shows it inline and stops polling.
- **Network blips while polling:** a failed `authDevicePoll` is surfaced but the
  UI may keep the editor open for a manual retry; a persistent error stops
  polling. (UI keeps the last code visible so the user need not restart for a
  transient blip.)

## 6. Testing

- **`device.go`:** `PollDeviceAuthOnce` against an `httptest` server — `pending`
  on 403/404, success decodes the bundle, other non-2xx → err. Confirm the
  existing `PollDeviceAuth` tests stay green after the refactor.
  `RequestDeviceCode` 404 → `errors.Is(err, ErrDeviceCodeNotEnabled)`.
- **`app_auth` (`cmd/serf-hub`):** with injected device funcs —
  - `DeviceStart` returns code fields and stores a flow; the not-enabled error
    yields `{Fallback:true}`.
  - `DevicePoll` → `pending` passes through; `authorized` exchanges, saves, and
    returns signed-in `oauth` status; an expired/unknown flow returns
    `expired`.
- **Frontend:** a `test-credentials.js`-style JSDOM test mocking
  `authDeviceStart`/`authDevicePoll`: the device editor renders the code +
  verification link, the poll loop reaches `authorized` and refreshes, and a
  `fallback` response switches to the paste-back editor.

## 7. Decisions (resolved)

- **Sign-in flow:** device-code is the webui default; paste-back is the
  automatic fallback when device-code is unsupported.
- **Polling:** UI-driven single-poll; no hub goroutines; ~15-minute window.
- **Scope:** OpenAI only (the only OAuth provider); CLI and redirect code
  untouched.
