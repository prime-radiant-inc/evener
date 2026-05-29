# Device-Code OAuth Login for OpenAI in the Hub Webui — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a no-redirect device-code OpenAI sign-in to the hub Credentials page (remote-friendly), with automatic fallback to the existing paste-back flow when the client doesn't support device-code.

**Architecture:** Reuse the existing `internal/auth/openai` device-code primitives. Add a non-blocking, UI-driven poll: two RPCs (`device/start`, `device/poll`) on `hubAuthController`, a single-attempt `PollDeviceAuthOnce` helper, and a "device" editor in `credentials.html` that polls until authorized.

**Tech Stack:** Go (`cmd/serf-hub`, `internal/auth/openai`, `internal/appwire`), vanilla JS templates (`assets/launchconfig.js`, `templates/partials/credentials.html`), JSDOM tests (`jstest`).

**Spec:** `docs/superpowers/specs/2026-05-29-openai-device-code-webui-design.md`
**Ticket:** PRI-1878

Reference (verified current code):
- `internal/auth/openai/device.go`: `RequestDeviceCode` returns `DeviceCode{VerificationURL, UserCode, DeviceAuthID, Interval}`; on 404 returns a plain "not enabled" error (line ~126). `pollDeviceAuth(ctx, client, cfg, dc, opts)` is the blocking loop (lines ~176-265); per-attempt it POSTs to `<issuer>/api/accounts/deviceauth/token`, treats 403/404 as pending, 2xx decodes `devicePollResponse{authorization_code, code_challenge, code_verifier}`. `ExchangeDeviceCode(ctx, client, cfg, authCode, codeVerifier)` exists.
- `internal/auth/openai/device_test.go`: `newDeviceMockServer(t)` with hooks `m.usercode`/`m.token`, `m.cfg()` (Config → mock URL), `writeJSON(t, w, status, map[string]any{...})`.
- `cmd/serf-hub/app_auth.go`: `hubAuthController` has `stateDir, client, now, exchangeCode, mu, flows`. `config()`, `authRecordFromTokens(tokens)` (Source=oauth), `openAIStatus()`, `firstNonEmpty`, `normalizeAuthProvider`. `LoginComplete` shows the exchange→`ParseIDTokenClaims`→`SaveAuth` pattern. `authopenai.GenerateState() (string, error)`.
- `cmd/serf-hub/app_rpc.go`: auth handlers registered via `appserver.HandleTyped(server.Router(), appwire.MethodSerfAuth…, fn)`; `notifyAuthUpdated(server, provider, activeSource)` after state changes.
- `internal/appwire/types.go`: method consts ~lines 31-36; `AuthStatusResponse`, `AuthLogoutParams` etc. ~lines 520-700; `appwire.InvalidParams(msg)`.
- `cmd/serf-hub/assets/launchconfig.js`: `request(method, params)` helper; auth wrappers end after `authLogout` (~line 30).
- `cmd/serf-hub/templates/partials/credentials.html`: IIFE with `renderEditor(p,e)` (kinds `set`, `oauth-redirect`), the list `click` handler (actions `set`/`oauth`/`clear`/`cancel-edit`), `refresh()`, `openEditor` state.

---

### Task 1: `device.go` — sentinel error + single-attempt poll + refactor

**Files:**
- Modify: `internal/auth/openai/device.go`
- Test: `internal/auth/openai/device_test.go`

- [ ] **Step 1: Write the failing tests** — append to `internal/auth/openai/device_test.go`:

```go
func TestPollDeviceAuthOncePendingOnForbidden(t *testing.T) {
	m := newDeviceMockServer(t)
	m.token = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }
	_, pending, err := PollDeviceAuthOnce(context.Background(), m.server.Client(), m.cfg(), DeviceCode{DeviceAuthID: "d", UserCode: "C"})
	if err != nil || !pending {
		t.Fatalf("PollDeviceAuthOnce() pending=%v err=%v, want pending,no-error", pending, err)
	}
}

func TestPollDeviceAuthOnceSuccess(t *testing.T) {
	m := newDeviceMockServer(t)
	m.token = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"authorization_code": "auth-1", "code_challenge": "chal-1", "code_verifier": "ver-1",
		})
	}
	got, pending, err := PollDeviceAuthOnce(context.Background(), m.server.Client(), m.cfg(), DeviceCode{DeviceAuthID: "d", UserCode: "C"})
	if err != nil || pending {
		t.Fatalf("pending=%v err=%v, want not-pending,no-error", pending, err)
	}
	if got.AuthorizationCode != "auth-1" || got.CodeVerifier != "ver-1" {
		t.Fatalf("DeviceCodeSuccess = %+v", got)
	}
}

func TestPollDeviceAuthOnceSurfacesUnexpectedStatus(t *testing.T) {
	m := newDeviceMockServer(t)
	m.token = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }
	_, pending, err := PollDeviceAuthOnce(context.Background(), m.server.Client(), m.cfg(), DeviceCode{DeviceAuthID: "d", UserCode: "C"})
	if err == nil || pending {
		t.Fatalf("pending=%v err=%v, want error,not-pending", pending, err)
	}
}

func TestRequestDeviceCodeNotEnabledIsSentinel(t *testing.T) {
	m := newDeviceMockServer(t)
	m.usercode = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }
	_, err := RequestDeviceCode(context.Background(), m.server.Client(), m.cfg())
	if !errors.Is(err, ErrDeviceCodeNotEnabled) {
		t.Fatalf("RequestDeviceCode() err = %v, want errors.Is ErrDeviceCodeNotEnabled", err)
	}
}
```

(If `device_test.go` doesn't already import `"errors"`, add it.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/auth/openai/ -run 'PollDeviceAuthOnce|NotEnabledIsSentinel' -v`
Expected: FAIL — `PollDeviceAuthOnce` and `ErrDeviceCodeNotEnabled` are undefined (compile error).

- [ ] **Step 3: Add the sentinel error.** In `internal/auth/openai/device.go`, add to the `var (...)` block near `ErrAuthNotFound` (or create a var block in this file) :

```go
// ErrDeviceCodeNotEnabled means the OpenAI client does not support the
// device-code flow (the usercode endpoint returned 404). Callers can branch
// on this to fall back to the browser/redirect flow.
var ErrDeviceCodeNotEnabled = errors.New("device-code login is not enabled for this OpenAI client")
```

Then change the 404 branch in `RequestDeviceCode` from:

```go
	if resp.StatusCode == http.StatusNotFound {
		return DeviceCode{}, errors.New("device-code login is not enabled for this OpenAI client; use the browser flow (`serf openai login`) instead")
	}
```

to:

```go
	if resp.StatusCode == http.StatusNotFound {
		return DeviceCode{}, fmt.Errorf("%w; use the browser flow (`serf openai login`) instead", ErrDeviceCodeNotEnabled)
	}
```

- [ ] **Step 4: Add `PollDeviceAuthOnce` and refactor the loop.** In `internal/auth/openai/device.go`, add:

```go
// PollDeviceAuthOnce makes a single poll attempt against the device token
// endpoint. pending is true when the server reports the user has not finished
// authorizing yet (HTTP 403/404). A non-2xx status that is not pending is
// returned as err.
func PollDeviceAuthOnce(ctx context.Context, client *http.Client, cfg Config, dc DeviceCode) (DeviceCodeSuccess, bool, error) {
	cfg = mergeConfigDefaults(cfg)
	if client == nil {
		client = &http.Client{Timeout: cfg.HTTPTimeout}
	}
	body, err := json.Marshal(devicePollRequest{DeviceAuthID: dc.DeviceAuthID, UserCode: dc.UserCode})
	if err != nil {
		return DeviceCodeSuccess{}, false, fmt.Errorf("marshal device poll request: %w", err)
	}
	endpoint := strings.TrimRight(cfg.issuerBaseURL(), "/") + deviceAPIPath + "/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return DeviceCodeSuccess{}, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return DeviceCodeSuccess{}, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	status := resp.StatusCode
	if status >= 200 && status < 300 {
		var payload devicePollResponse
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			return DeviceCodeSuccess{}, false, fmt.Errorf("decode device poll response: %w", err)
		}
		if payload.AuthorizationCode == "" || payload.CodeVerifier == "" {
			return DeviceCodeSuccess{}, false, errors.New("device poll response missing authorization_code or code_verifier")
		}
		return DeviceCodeSuccess{
			AuthorizationCode: payload.AuthorizationCode,
			CodeChallenge:     payload.CodeChallenge,
			CodeVerifier:      payload.CodeVerifier,
		}, false, nil
	}
	if status == http.StatusForbidden || status == http.StatusNotFound {
		return DeviceCodeSuccess{}, true, nil
	}
	return DeviceCodeSuccess{}, false, fmt.Errorf("device auth failed with status %d", status)
}
```

Then replace the `for { ... }` body inside `pollDeviceAuth` (everything from `if err := ctx.Err(); err != nil {` through the end of the loop) with:

```go
	for {
		if err := ctx.Err(); err != nil {
			return DeviceCodeSuccess{}, err
		}

		success, pending, err := PollDeviceAuthOnce(ctx, client, cfg, dc)
		if err != nil {
			return DeviceCodeSuccess{}, err
		}
		if !pending {
			return success, nil
		}

		elapsed := now().Sub(start)
		if elapsed >= maxWait {
			return DeviceCodeSuccess{}, errors.New("device auth timed out after 15 minutes")
		}
		wait := interval
		if remaining := maxWait - elapsed; wait > remaining {
			wait = remaining
		}
		if err := sleep(ctx, wait); err != nil {
			return DeviceCodeSuccess{}, err
		}
	}
```

(After this refactor the `base := ...` and `endpoint := ...` lines above the loop in `pollDeviceAuth` are both unused — remove both, or Go won't compile. Keep `start := now()`, `interval`, `maxWait`, `now`, and `sleep`.)

- [ ] **Step 5: Run the package tests**

Run: `go test ./internal/auth/openai/ -v 2>&1 | tail -20`
Expected: PASS — the new tests pass and all existing `TestPollDeviceAuth*` / `TestRequestDeviceCode*` tests still pass (the refactor is behavior-preserving).

- [ ] **Step 6: Commit**

```bash
git add internal/auth/openai/device.go internal/auth/openai/device_test.go
git commit -m "feat(auth/openai): PollDeviceAuthOnce + ErrDeviceCodeNotEnabled sentinel (PRI-1878)"
```

---

### Task 2: appwire method constants + device types

**Files:**
- Modify: `internal/appwire/types.go`

- [ ] **Step 1: Add method constants.** In `internal/appwire/types.go`, alongside the other `MethodSerfAuth*` constants (after `MethodSerfAuthApiKeySet`):

```go
	MethodSerfAuthDeviceStart      = "serf/auth/device/start"
	MethodSerfAuthDevicePoll       = "serf/auth/device/poll"
```

- [ ] **Step 2: Add the params/response types.** Add near the other `Auth*` types (after `AuthApiKeySetParams`):

```go
// AuthDeviceStartParams is the params for serf/auth/device/start.
type AuthDeviceStartParams struct {
	Provider string `json:"provider"`
}

// AuthDeviceStartResponse carries the device code to display, or Fallback=true
// when the client doesn't support device-code and the caller should use the
// redirect/paste-back flow instead.
type AuthDeviceStartResponse struct {
	Provider        string `json:"provider"`
	FlowID          string `json:"flowId"`
	UserCode        string `json:"userCode"`
	VerificationURL string `json:"verificationUrl"`
	IntervalSeconds int    `json:"intervalSeconds"`
	Fallback        bool   `json:"fallback,omitempty"`
}

// AuthDevicePollParams is the params for serf/auth/device/poll.
type AuthDevicePollParams struct {
	Provider string `json:"provider"`
	FlowID   string `json:"flowId"`
}

// AuthDevicePollResponse reports one poll attempt. State is "pending",
// "authorized", or "expired". Status is populated only when authorized.
type AuthDevicePollResponse struct {
	State  string             `json:"state"`
	Status AuthStatusResponse `json:"status,omitempty"`
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./internal/appwire/`
Expected: no output (success).

- [ ] **Step 4: Commit**

```bash
git add internal/appwire/types.go
git commit -m "feat(appwire): device-code auth RPC method names and types (PRI-1878)"
```

---

### Task 3: controller — `DeviceStart` / `DevicePoll`

**Files:**
- Modify: `cmd/serf-hub/app_auth.go`
- Test: `cmd/serf-hub/app_auth_test.go`

- [ ] **Step 1: Write the failing tests** — append to `cmd/serf-hub/app_auth_test.go`:

```go
func TestAuth_DeviceStart_ReturnsCodeAndStoresFlow(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		return authopenai.DeviceCode{UserCode: "USER-1", VerificationURL: "https://auth.openai.com/codex/device", DeviceAuthID: "dev-1", Interval: 5 * time.Second}, nil
	}
	got, err := c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	if got.Fallback || got.UserCode != "USER-1" || got.VerificationURL == "" || got.IntervalSeconds != 5 || got.FlowID == "" {
		t.Fatalf("resp=%+v, want code fields + interval 5 + flowId", got)
	}
}

func TestAuth_DeviceStart_FallbackWhenNotEnabled(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		return authopenai.DeviceCode{}, authopenai.ErrDeviceCodeNotEnabled
	}
	got, err := c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	if !got.Fallback {
		t.Fatalf("resp=%+v, want Fallback=true", got)
	}
}

func TestAuth_DevicePoll_PendingThenAuthorized(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	c.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		return authopenai.DeviceCode{UserCode: "U", VerificationURL: "https://x", DeviceAuthID: "d", Interval: time.Second}, nil
	}
	pending := true
	c.pollDeviceOnce = func(context.Context, *http.Client, authopenai.Config, authopenai.DeviceCode) (authopenai.DeviceCodeSuccess, bool, error) {
		if pending {
			pending = false
			return authopenai.DeviceCodeSuccess{}, true, nil
		}
		return authopenai.DeviceCodeSuccess{AuthorizationCode: "ac", CodeVerifier: "cv"}, false, nil
	}
	c.exchangeDevice = func(context.Context, *http.Client, authopenai.Config, string, string) (authopenai.TokenSet, error) {
		return authopenai.TokenSet{AccessToken: "at", RefreshToken: "rt", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}, nil
	}
	start, err := c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	p1, err := c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "openai", FlowID: start.FlowID})
	if err != nil || p1.State != "pending" {
		t.Fatalf("first poll = %+v err=%v, want pending", p1, err)
	}
	p2, err := c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "openai", FlowID: start.FlowID})
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if p2.State != "authorized" || p2.Status.ActiveSource != authopenai.AuthSourceOAuth {
		t.Fatalf("second poll = %+v, want authorized + oauth", p2)
	}
}

func TestAuth_DevicePoll_UnknownFlowExpired(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	got, err := c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "openai", FlowID: "nope"})
	if err != nil || got.State != "expired" {
		t.Fatalf("got=%+v err=%v, want expired", got, err)
	}
}
```

(Ensure `net/http` is imported in `app_auth_test.go` — it already is.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/serf-hub/ -run 'TestAuth_Device' -v`
Expected: FAIL — `DeviceStart`/`DevicePoll` and the `requestDeviceCode`/`pollDeviceOnce`/`exchangeDevice` fields are undefined (compile error).

- [ ] **Step 3: Add controller fields + flow type + defaults.** In `cmd/serf-hub/app_auth.go`, add to the `hubAuthController` struct (after `exchangeCode`):

```go
	requestDeviceCode func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error)
	pollDeviceOnce    func(context.Context, *http.Client, authopenai.Config, authopenai.DeviceCode) (authopenai.DeviceCodeSuccess, bool, error)
	exchangeDevice    func(context.Context, *http.Client, authopenai.Config, string, string) (authopenai.TokenSet, error)

	deviceFlows map[string]deviceFlow
```

Add the flow type near `hubAuthFlow`:

```go
type deviceFlow struct {
	Provider  string
	Code      authopenai.DeviceCode
	StartedAt time.Time
}
```

In BOTH constructors (`newHubAuthController` and `newHubAuthControllerWithStore`), set these fields in the returned `&hubAuthController{...}` literal (alongside `exchangeCode: authopenai.ExchangeCode`):

```go
		requestDeviceCode: authopenai.RequestDeviceCode,
		pollDeviceOnce:    authopenai.PollDeviceAuthOnce,
		exchangeDevice:    authopenai.ExchangeDeviceCode,
		deviceFlows:       map[string]deviceFlow{},
```

- [ ] **Step 4: Add `DeviceStart` and `DevicePoll`.** In `cmd/serf-hub/app_auth.go`, add:

```go
func (c *hubAuthController) DeviceStart(ctx context.Context, params appwire.AuthDeviceStartParams) (appwire.AuthDeviceStartResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if provider != "openai" {
		return appwire.AuthDeviceStartResponse{}, appwire.InvalidParams(fmt.Sprintf("auth is not supported for provider %q", provider))
	}
	dc, err := c.requestDeviceCode(ctx, c.client, c.config())
	if err != nil {
		if errors.Is(err, authopenai.ErrDeviceCodeNotEnabled) {
			return appwire.AuthDeviceStartResponse{Provider: provider, Fallback: true}, nil
		}
		return appwire.AuthDeviceStartResponse{}, err
	}
	flowID, err := authopenai.GenerateState()
	if err != nil {
		return appwire.AuthDeviceStartResponse{}, fmt.Errorf("generate device flow id: %w", err)
	}
	c.mu.Lock()
	if c.deviceFlows == nil {
		c.deviceFlows = map[string]deviceFlow{}
	}
	c.deviceFlows[flowID] = deviceFlow{Provider: provider, Code: dc, StartedAt: c.now()}
	c.mu.Unlock()
	return appwire.AuthDeviceStartResponse{
		Provider:        provider,
		FlowID:          flowID,
		UserCode:        dc.UserCode,
		VerificationURL: dc.VerificationURL,
		IntervalSeconds: int(dc.Interval / time.Second),
	}, nil
}

func (c *hubAuthController) DevicePoll(ctx context.Context, params appwire.AuthDevicePollParams) (appwire.AuthDevicePollResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if provider != "openai" {
		return appwire.AuthDevicePollResponse{}, appwire.InvalidParams(fmt.Sprintf("auth is not supported for provider %q", provider))
	}
	flowID := strings.TrimSpace(params.FlowID)
	c.mu.Lock()
	flow, ok := c.deviceFlows[flowID]
	c.mu.Unlock()
	if !ok {
		return appwire.AuthDevicePollResponse{State: "expired"}, nil
	}
	if c.now().Sub(flow.StartedAt) >= 15*time.Minute {
		c.mu.Lock()
		delete(c.deviceFlows, flowID)
		c.mu.Unlock()
		return appwire.AuthDevicePollResponse{State: "expired"}, nil
	}

	success, pending, err := c.pollDeviceOnce(ctx, c.client, c.config(), flow.Code)
	if err != nil {
		return appwire.AuthDevicePollResponse{}, err
	}
	if pending {
		return appwire.AuthDevicePollResponse{State: "pending"}, nil
	}

	tokens, err := c.exchangeDevice(ctx, c.client, c.config(), success.AuthorizationCode, success.CodeVerifier)
	if err != nil {
		return appwire.AuthDevicePollResponse{}, err
	}
	record := c.authRecordFromTokens(tokens)
	if claims, err := authopenai.ParseIDTokenClaims(tokens.IDToken); err == nil {
		record.Email = firstNonEmpty(claims.Email, record.Email)
		record.AccountID = firstNonEmpty(claims.AccountID, record.AccountID)
		record.WorkspaceID = firstNonEmpty(claims.WorkspaceID, record.WorkspaceID)
	}
	if err := authopenai.SaveAuth(c.stateDir, record); err != nil {
		return appwire.AuthDevicePollResponse{}, err
	}
	c.mu.Lock()
	delete(c.deviceFlows, flowID)
	c.mu.Unlock()

	status, err := c.openAIStatus()
	if err != nil {
		return appwire.AuthDevicePollResponse{}, err
	}
	return appwire.AuthDevicePollResponse{State: "authorized", Status: status}, nil
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./cmd/serf-hub/ -run 'TestAuth_Device|TestHubRPCAuth|TestAuth_OpenAI' -v 2>&1 | tail -25`
Expected: PASS — new device tests pass; existing auth tests still pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/app_auth.go cmd/serf-hub/app_auth_test.go
git commit -m "feat(serf-hub): DeviceStart/DevicePoll controller methods (PRI-1878)"
```

---

### Task 4: register the device RPCs

**Files:**
- Modify: `cmd/serf-hub/app_rpc.go`

- [ ] **Step 1: Register the handlers.** In `cmd/serf-hub/app_rpc.go`, immediately after the `MethodSerfAuthApiKeySet` handler block, add:

```go
	appserver.HandleTyped(server.Router(), appwire.MethodSerfAuthDeviceStart, func(ctx context.Context, params appwire.AuthDeviceStartParams) (appwire.AuthDeviceStartResponse, error) {
		return authController.DeviceStart(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfAuthDevicePoll, func(ctx context.Context, params appwire.AuthDevicePollParams) (appwire.AuthDevicePollResponse, error) {
		resp, err := authController.DevicePoll(ctx, params)
		if err == nil && resp.State == "authorized" {
			notifyAuthUpdated(server, resp.Status.Provider, resp.Status.ActiveSource)
		}
		return resp, err
	})
```

- [ ] **Step 2: Build + run the hub package tests**

Run: `go build ./... && go test ./cmd/serf-hub/ 2>&1 | tail -3`
Expected: BUILD OK; package tests PASS (the new methods are wired; existing RPC tests unaffected).

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/app_rpc.go
git commit -m "feat(serf-hub): wire device-code auth RPCs (PRI-1878)"
```

---

### Task 5: launchconfig client wrappers

**Files:**
- Modify: `cmd/serf-hub/assets/launchconfig.js`

- [ ] **Step 1: Add the wrappers.** In `cmd/serf-hub/assets/launchconfig.js`, replace:

```js
    authLogout: (provider) => request("serf/auth/logout", { provider }),
  };
```

with:

```js
    authLogout: (provider) => request("serf/auth/logout", { provider }),
    authDeviceStart: (provider) => request("serf/auth/device/start", { provider }),
    authDevicePoll: (provider, flowId) => request("serf/auth/device/poll", { provider, flowId }),
  };
```

- [ ] **Step 2: Syntax-check**

Run: `node --check cmd/serf-hub/assets/launchconfig.js && echo OK`
Expected: `OK`.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/assets/launchconfig.js
git commit -m "feat(serf-hub): launchconfig authDeviceStart/authDevicePoll wrappers (PRI-1878)"
```

---

### Task 6: credentials page — device editor, polling, fallback + JSDOM test

**Files:**
- Modify: `cmd/serf-hub/templates/partials/credentials.html`
- Test: `cmd/serf-hub/jstest/test-credentials-device.js`

- [ ] **Step 1: Write the failing JSDOM test** — create `cmd/serf-hub/jstest/test-credentials-device.js`:

```js
// Loads credentials.html's inline script into JSDOM, mocks the device-code RPC
// wrappers, and asserts: the device editor renders the user code + verification
// link and polls to "authorized"; and that a fallback response switches to the
// paste-back (oauth-redirect) editor.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

const html = fs.readFileSync(path.resolve(__dirname, "../templates/partials/credentials.html"), "utf8");
const src = html.match(/<script>([\s\S]*?)<\/script>/)[1];

function makeDom() {
  return new JSDOM(`<!DOCTYPE html><html><body>
    <section id="credentials-rows" class="settings-collection" data-loaded="false">
      <header class="settings-collection-head"><h3>Providers</h3><span class="settings-collection-count" data-count></span></header>
      <ul class="settings-collection-list" role="list"><li class="settings-collection-empty">Loading…</li></ul>
    </section></body></html>`,
    { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/credentials" });
}
const wait = (dom, ms) => new Promise((r) => dom.window.setTimeout(r, ms));
const openaiRow = (dom) => dom.window.document.querySelector('li[data-provider="openai"]');

(async function main() {
  // Case 1: device flow renders code + verification link, polls to authorized.
  {
    const dom = makeDom();
    let listed = [{ provider: "openai", supported: true, signedIn: false, activeSource: "absent", authModes: ["apiKey", "oauth"] }];
    let pollCalls = 0;
    dom.window.launchconfig = {
      authList: async () => ({ providers: listed }),
      authDeviceStart: async () => ({ provider: "openai", flowId: "f1", userCode: "WXYZ-1234", verificationUrl: "https://auth.openai.com/codex/device", intervalSeconds: 0 }),
      authDevicePoll: async () => { pollCalls++; if (pollCalls >= 2) { listed = [{ provider: "openai", supported: true, signedIn: true, activeSource: "oauth", authModes: ["apiKey", "oauth"], hasStoredOAuth: true }]; return { state: "authorized", status: listed[0] }; } return { state: "pending" }; },
      authLoginStart: async () => ({ flowId: "x", url: "https://x" }),
    };
    dom.window.open = () => null;
    dom.window.eval(src);
    const section = dom.window.document.getElementById("credentials-rows");
    for (let i = 0; i < 100 && section.dataset.loaded !== "true"; i++) await wait(dom, 0);
    openaiRow(dom).querySelector('button[data-action="oauth"]').click();
    for (let i = 0; i < 100 && !openaiRow(dom).querySelector('[data-editor="device"]'); i++) await wait(dom, 0);
    const editor = openaiRow(dom).querySelector('[data-editor="device"]');
    assert(editor, "device editor should render after clicking Sign in");
    assert(/WXYZ-1234/.test(editor.textContent), "device editor should show the user code");
    assert(editor.querySelector('a[href="https://auth.openai.com/codex/device"]'), "device editor should link to the verification URL");
    for (let i = 0; i < 200 && openaiRow(dom).querySelector('[data-editor="device"]'); i++) await wait(dom, 5);
    assert(!openaiRow(dom).querySelector('[data-editor="device"]'), "device editor should close after authorized");
    assert(/oauth/.test(openaiRow(dom).textContent), "openai row should show oauth after authorized");
  }

  // Case 2: fallback switches to the paste-back (oauth-redirect) editor.
  {
    const dom = makeDom();
    dom.window.launchconfig = {
      authList: async () => ({ providers: [{ provider: "openai", supported: true, signedIn: false, activeSource: "absent", authModes: ["apiKey", "oauth"] }] }),
      authDeviceStart: async () => ({ provider: "openai", fallback: true }),
      authLoginStart: async () => ({ flowId: "f2", url: "https://auth.openai.com/oauth/authorize?x=1" }),
      authDevicePoll: async () => ({ state: "pending" }),
    };
    dom.window.open = () => null;
    dom.window.eval(src);
    const section = dom.window.document.getElementById("credentials-rows");
    for (let i = 0; i < 100 && section.dataset.loaded !== "true"; i++) await wait(dom, 0);
    openaiRow(dom).querySelector('button[data-action="oauth"]').click();
    for (let i = 0; i < 100 && !openaiRow(dom).querySelector('[data-editor="oauth-redirect"]'); i++) await wait(dom, 0);
    assert(openaiRow(dom).querySelector('[data-editor="oauth-redirect"]'), "fallback should open the paste-back editor");
  }

  console.log("test-credentials-device.js: OK");
})().catch((err) => { console.error(err && err.stack ? err.stack : err); process.exit(1); });
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-credentials-device.js; echo exit=$?`
Expected: FAIL (`exit=1`) — the `oauth` action still calls `authLoginStart` directly, so no `device` editor appears.

- [ ] **Step 3: Add the `device` editor branch.** In `cmd/serf-hub/templates/partials/credentials.html`, inside `renderEditor(p, e)`, after the `if (e.kind === "oauth-redirect") { ... }` block and before the final `return "";`, add:

```js
      if (e.kind === "device") {
        return `
          <div class="credentials-editor surface-inset" data-editor="device">
            <div class="credentials-editor-label">
              Enter this code at
              <a href="${escapeHtml(e.verificationUrl)}" target="_blank" rel="noopener">${escapeHtml(e.verificationUrl)}</a>
              to sign in:
            </div>
            <div class="credentials-device-code">${escapeHtml(e.userCode)}</div>
            <p class="credentials-device-status">${e.error ? escapeHtml(e.error) : "Waiting for you to authorize…"}</p>
            <div class="credentials-editor-actions">
              ${e.expired || e.error ? `<button type="button" class="btn btn-primary" data-action="device-retry">Start again</button>` : ""}
              <button type="button" class="btn btn-ghost" data-action="cancel-edit">Cancel</button>
            </div>
          </div>`;
      }
```

- [ ] **Step 4: Add the device-login driver + polling.** In `credentials.html`, just after the line `let openEditor = null;` near the top of the IIFE, add:

```js
    let devicePollTimer = null;
    function stopDevicePolling() {
      if (devicePollTimer) { clearTimeout(devicePollTimer); devicePollTimer = null; }
    }
    async function startDeviceLogin(provider) {
      stopDevicePolling();
      try {
        const r = await launchconfig.authDeviceStart(provider);
        if (r && r.fallback) {
          const lr = await launchconfig.authLoginStart(provider);
          window.open(lr.url, "_blank", "noopener");
          openEditor = { provider, kind: "oauth-redirect", flowId: lr.flowId, authUrl: lr.url };
          await refresh();
          return;
        }
        if (r.verificationUrl) window.open(r.verificationUrl, "_blank", "noopener");
        openEditor = { provider, kind: "device", flowId: r.flowId, userCode: r.userCode, verificationUrl: r.verificationUrl };
        await refresh();
        startDevicePolling(provider, r.flowId, Math.max(1, r.intervalSeconds || 5) * 1000);
      } catch (err) {
        if (window.SerfToast) window.SerfToast.show("Sign-in failed: " + (err && err.message ? err.message : err), "error");
      }
    }
    function startDevicePolling(provider, flowId, intervalMs) {
      stopDevicePolling();
      const tick = async () => {
        let resp;
        try {
          resp = await launchconfig.authDevicePoll(provider, flowId);
        } catch (err) {
          if (openEditor && openEditor.kind === "device" && openEditor.flowId === flowId) {
            openEditor.error = (err && err.message) ? err.message : String(err);
            await refresh();
          }
          return;
        }
        if (!openEditor || openEditor.kind !== "device" || openEditor.flowId !== flowId) return;
        if (resp.state === "authorized") {
          stopDevicePolling();
          openEditor = null;
          await refresh();
          if (window.SerfToast) window.SerfToast.show("Signed in to " + provider, "success");
          return;
        }
        if (resp.state === "expired") {
          stopDevicePolling();
          openEditor.expired = true;
          openEditor.error = "Code expired — start again.";
          await refresh();
          return;
        }
        devicePollTimer = setTimeout(tick, intervalMs);
      };
      devicePollTimer = setTimeout(tick, intervalMs);
    }
```

- [ ] **Step 5: Route the `oauth` action through device-login and stop polling on cancel.** In `credentials.html`, in the list `click` handler, replace the entire `} else if (action === "oauth") { ... }` block with:

```js
      } else if (action === "oauth") {
        await startDeviceLogin(provider);
      } else if (action === "device-retry") {
        await startDeviceLogin(provider);
```

And change the `cancel-edit` branch from:

```js
      } else if (action === "cancel-edit") {
        openEditor = null;
        await refresh();
```

to:

```js
      } else if (action === "cancel-edit") {
        stopDevicePolling();
        openEditor = null;
        await refresh();
```

- [ ] **Step 6: Run the new test + the existing credentials test**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-credentials-device.js && node test-credentials.js`
Expected: both print `OK`.

- [ ] **Step 7: Run the full jstest suite + Go build (template still parses)**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh >/dev/null && echo JS_OK; cd /Users/jesse/prime-radiant/toil-suite/serf && go build ./... && echo BUILD_OK`
Expected: `JS_OK` then `BUILD_OK`.

- [ ] **Step 8: Commit**

```bash
git add cmd/serf-hub/templates/partials/credentials.html cmd/serf-hub/jstest/test-credentials-device.js
git commit -m "feat(serf-hub): device-code sign-in UI on the credentials page (PRI-1878)"
```

---

### Task 7: full verification

**Files:** none (verification only)

- [ ] **Step 1: Build + vet**

Run: `go build ./... && go vet ./cmd/serf-hub/ ./internal/auth/openai/ ./internal/appwire/ && echo OK`
Expected: `OK`.

- [ ] **Step 2: Run affected Go suites**

Run: `go test ./cmd/serf-hub/... ./internal/auth/openai/... ./internal/appwire/...`
Expected: all PASS, pristine output.

- [ ] **Step 3: Full jstest suite**

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh 2>&1 | tail -3`
Expected: ends cleanly, no FAIL.

- [ ] **Step 4: Manual webui smoke (recommended).** Launch the hub in an isolated temp HOME (see the PRI-1877 smoke approach), open `/credentials`, click OpenAI "Sign in…", and confirm the device editor shows a user code + verification link and a "Waiting…" state. (Full authorization needs a real OpenAI account; verifying the editor renders and polls is sufficient for the smoke.)

- [ ] **Step 5: Move ticket to In Review + reflective comment.** Per `primeradiant-ops:linear-ticket-lifecycle`: move PRI-1878 to **In Review** and add a genuine reflective implementation comment.

---

## Self-Review

**Spec coverage:**
- §4.1 device-first with paste-back fallback → Task 6 (`startDeviceLogin` fallback branch) + Task 3 (`DeviceStart` Fallback). ✓
- §4.2 UI-driven single-poll → Task 1 (`PollDeviceAuthOnce`), Task 3 (`DevicePoll`), Task 6 (`startDevicePolling`). ✓
- §4.3 `PollDeviceAuthOnce` + refactor + `ErrDeviceCodeNotEnabled` → Task 1. ✓
- §4.4 RPC methods/types + controller (deviceFlows, injected funcs, 15-min expiry, GenerateState flowId, IntervalSeconds) → Tasks 2, 3, 4. ✓
- §4.5 launchconfig wrappers + device editor + fallback → Tasks 5, 6. ✓
- §5 error handling: not-enabled→fallback (Task 3/6), expiry (Task 3 `DevicePoll`, Task 6 `expired`), poll/exchange error (Task 3 returns err; Task 6 shows + stops), network blip (Task 6 keeps editor, stops on hard error). ✓
- §6 testing: device.go (Task 1), app_auth (Task 3), frontend JSDOM (Task 6). ✓

**Placeholder scan:** none — every code step has complete code; every run step has a command + expected output.

**Type consistency:** `DeviceStart(ctx, AuthDeviceStartParams) AuthDeviceStartResponse`, `DevicePoll(ctx, AuthDevicePollParams) AuthDevicePollResponse`; field names (`FlowID`, `UserCode`, `VerificationURL`, `IntervalSeconds`, `Fallback`, `State`, `Status`) and JSON tags (`flowId`, `userCode`, `verificationUrl`, `intervalSeconds`, `fallback`, `state`, `status`) match across Go types, the JS wrappers (`authDeviceStart`/`authDevicePoll`), and the JS UI (`r.fallback`, `r.userCode`, `r.verificationUrl`, `r.intervalSeconds`, `resp.state`). Injected func signatures match `authopenai.RequestDeviceCode`/`PollDeviceAuthOnce`/`ExchangeDeviceCode`.
