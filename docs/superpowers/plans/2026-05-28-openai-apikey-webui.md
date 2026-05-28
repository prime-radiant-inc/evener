# OpenAI API Key from the Webui Credentials Page — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the hub webui Credentials page set an OpenAI API key alongside OAuth, by removing the OpenAI special-casing in the hub auth controller.

**Architecture:** OpenAI's API-key layer is stored in the shared `credentials.toml` (like every other provider); OAuth stays in `openai.json` as a separate, higher-precedence sign-in. Effective precedence is `OAuth > file > env`. The store→`ToEnv`→adapter plumbing that carries a stored key to spawned sessions already exists; this plan unblocks `ApiKeySet`, makes OpenAI status layer-aware, makes `Logout` clear the effective layer, and updates the evergreen README.

**Tech Stack:** Go; `cmd/serf-hub` (hub auth controller, RPC), `internal/credentials` (store), `internal/auth/openai` (OAuth state), `internal/launchconfig` (spawn env).

**Spec:** `docs/superpowers/specs/2026-05-28-openai-apikey-webui-design.md`
**Ticket:** PRI-1877

Reference (current code, do not re-derive):
- `cmd/serf-hub/app_auth.go`: `openAIStatus` (lines ~280-326), `ApiKeySet` (~266-278), `Logout` (~222-240), `Status` openai branch (~81-90), helper `openAIStatusFromRecord` (~340).
- `internal/credentials/store.go`: `Get` (file→env), `Layers(provider) (hasFile bool, envVar string)`, `Set`, `Clear`. `SourceFile == "file"`, `SourceEnv == "env"`.
- `internal/auth/openai`: `LoadAuth`/`SaveAuth`/`DeleteAuth`, `AuthFilePath(stateDir)` = `<stateDir>/auth/openai.json`, `ErrAuthNotFound`, `ErrAuthCorrupt` (wrapped, use `errors.Is`), `AuthSourceOAuth=="oauth"`, `AuthSourceEnv=="env"`, `AuthSourceSignedOut=="signed-out"`.
- Test helpers: `oaitest.IsolateOpenAIAuth(t)` clears `OPENAI_API_KEY` etc. and isolates the state dir; `newHubAuthControllerWithStore(stateDir, store)` builds a controller (note: it derives its OAuth `stateDir` from env, ignoring the first arg — override `c.stateDir` directly when a test needs to control it).

---

### Task 1: Make OpenAI status layer-aware (file/env/oauth precedence, `HasStoredFile`, corrupt-tolerant)

**Files:**
- Modify: `cmd/serf-hub/app_auth.go` (`openAIStatus`)
- Test: `cmd/serf-hub/app_auth_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `cmd/serf-hub/app_auth_test.go`:

```go
func TestAuth_OpenAI_Status_ReflectsStoredFileKey(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir() // empty: no OAuth record
	if err := store.Set("openai", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	got, err := c.Status(appwire.AuthStatusParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !got.SignedIn || got.ActiveSource != string(credentials.SourceFile) || !got.HasStoredFile {
		t.Fatalf("status=%+v, want signed-in file with HasStoredFile", got)
	}
	if got.HasStoredOAuth {
		t.Fatalf("status=%+v, want no stored OAuth", got)
	}
}

func TestAuth_OpenAI_Status_OAuthShadowsStoredFileKey(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	if err := store.Set("openai", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	if err := authopenai.SaveAuth(c.stateDir, authopenai.AuthRecord{
		Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth,
		ObtainedAt: time.Now().Add(-time.Hour), TokenType: "Bearer",
		AccessToken: "acc", RefreshToken: "ref",
		Expiry: time.Now().Add(time.Hour), Email: "o@example.com",
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	got, err := c.Status(appwire.AuthStatusParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.ActiveSource != authopenai.AuthSourceOAuth || !got.HasStoredFile || !got.HasStoredOAuth {
		t.Fatalf("status=%+v, want oauth active with file shadowed", got)
	}
}

func TestAuth_OpenAI_Status_CorruptOAuthFallsBackToFile(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	if err := store.Set("openai", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	authPath := authopenai.AuthFilePath(c.stateDir)
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := c.Status(appwire.AuthStatusParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("Status returned error on corrupt record: %v", err)
	}
	if got.ActiveSource != string(credentials.SourceFile) {
		t.Fatalf("status=%+v, want file (corrupt oauth treated as absent)", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/serf-hub/ -run 'TestAuth_OpenAI_Status_' -v`
Expected: FAIL — `ReflectsStoredFileKey` gets `ActiveSource "signed-out"`, `OAuthShadowsStoredFileKey` gets `HasStoredFile false`, `CorruptOAuthFallsBackToFile` returns a non-nil error.

- [ ] **Step 3: Rewrite `openAIStatus`**

Replace the entire `openAIStatus` function in `cmd/serf-hub/app_auth.go` with:

```go
func (c *hubAuthController) openAIStatus() (appwire.AuthStatusResponse, error) {
	// Precedence: stored OAuth record > credentials.toml file key >
	// OPENAI_API_KEY env. OAuth wins so an explicit sign-in beats a stray
	// file/env key; the file layer shadows env, like other providers.
	record, err := authopenai.LoadAuth(c.stateDir)
	hasRecord := false
	switch {
	case err == nil:
		hasRecord = true
	case errors.Is(err, authopenai.ErrAuthNotFound):
		// no OAuth layer
	case errors.Is(err, authopenai.ErrAuthCorrupt):
		// treat a corrupt record as absent; file/env layers still resolve
	default:
		return appwire.AuthStatusResponse{}, err
	}

	hasFile, _ := c.creds.Layers("openai")
	envSet := strings.TrimSpace(c.authEnv["OPENAI_API_KEY"]) != ""

	var active authopenai.AuthStatus
	switch {
	case hasRecord:
		active = openAIStatusFromRecord(c.now(), record)
	case hasFile:
		active = authopenai.AuthStatus{SignedIn: true, Source: string(credentials.SourceFile)}
	case envSet:
		active = authopenai.AuthStatus{SignedIn: true, Source: authopenai.AuthSourceEnv}
	default:
		active = authopenai.AuthStatus{Source: authopenai.AuthSourceSignedOut}
	}

	status := appwire.AuthStatusResponse{
		Provider:      "openai",
		Supported:     true,
		SignedIn:      active.SignedIn,
		ActiveSource:  active.Source,
		Email:         active.Email,
		AccountID:     active.AccountID,
		WorkspaceID:   active.WorkspaceID,
		NeedsRefresh:  active.NeedsRefresh,
		NeedsLogin:    active.NeedsLogin,
		HasStoredFile: hasFile,
	}
	if envSet {
		status.EnvVar = "OPENAI_API_KEY"
	}
	if hasRecord {
		status.HasStoredOAuth = true
		status.StoredEmail = record.Email
		if status.ActiveSource == authopenai.AuthSourceOAuth {
			status.Email = firstNonEmpty(status.Email, record.Email)
			status.AccountID = firstNonEmpty(status.AccountID, record.AccountID)
			status.WorkspaceID = firstNonEmpty(status.WorkspaceID, record.WorkspaceID)
		}
	}

	return status, nil
}
```

- [ ] **Step 4: Run the new tests and the existing auth suite**

Run: `go test ./cmd/serf-hub/ -run 'TestAuth_OpenAI_Status_|TestHubRPCAuthStatus|TestHubRPCAuthLogout' -v`
Expected: PASS (new tests pass; existing OAuth/env status + logout tests still pass — precedence preserved).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/app_auth.go cmd/serf-hub/app_auth_test.go
git commit -m "feat(serf-hub): openai status reflects credentials.toml file layer (PRI-1877)"
```

---

### Task 2: Allow setting an OpenAI API key (`ApiKeySet`)

**Files:**
- Modify: `cmd/serf-hub/app_auth.go` (`ApiKeySet`)
- Test: `cmd/serf-hub/app_auth_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/serf-hub/app_auth_test.go`:

```go
func TestAuth_OpenAI_ApiKeySet_PersistsAndReportsFile(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	credsPath := filepath.Join(dir, "credentials.toml")
	store, _ := credentials.LoadStore(credsPath)
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir() // no OAuth record

	got, err := c.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "openai", Value: "sk-openai-XXX"})
	if err != nil {
		t.Fatalf("ApiKeySet(openai): %v", err)
	}
	if got.ActiveSource != string(credentials.SourceFile) || !got.HasStoredFile {
		t.Fatalf("status=%+v, want file active with HasStoredFile", got)
	}
	store2, _ := credentials.LoadStore(credsPath)
	v, src := store2.Get("openai")
	if v != "sk-openai-XXX" || src != credentials.SourceFile {
		t.Errorf("after ApiKeySet: v=%q src=%q, want sk-openai-XXX/file", v, src)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/serf-hub/ -run TestAuth_OpenAI_ApiKeySet_PersistsAndReportsFile -v`
Expected: FAIL — `ApiKeySet` returns InvalidParams "openai api keys must be configured via env or hub.env…".

- [ ] **Step 3: Remove the OpenAI rejection**

In `cmd/serf-hub/app_auth.go`, replace the `ApiKeySet` function with:

```go
func (c *hubAuthController) ApiKeySet(params appwire.AuthApiKeySetParams) (appwire.AuthStatusResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if strings.TrimSpace(params.Value) == "" {
		return appwire.AuthStatusResponse{}, appwire.InvalidParams("value is required")
	}
	if err := c.creds.Set(provider, params.Value); err != nil {
		return appwire.AuthStatusResponse{}, err
	}
	return c.Status(appwire.AuthStatusParams{Provider: provider})
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/serf-hub/ -run 'TestAuth_OpenAI_ApiKeySet_PersistsAndReportsFile|TestAuth_ApiKeySet_WritesAndReports' -v`
Expected: PASS (openai now persists; the existing anthropic ApiKeySet test still passes).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/app_auth.go cmd/serf-hub/app_auth_test.go
git commit -m "feat(serf-hub): allow setting an OpenAI API key via credentials (PRI-1877)"
```

---

### Task 3: Layer-aware `Logout` for OpenAI (clear the effective layer)

**Files:**
- Modify: `cmd/serf-hub/app_auth.go` (`Logout`)
- Test: `cmd/serf-hub/app_auth_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `cmd/serf-hub/app_auth_test.go`:

```go
func TestAuth_OpenAI_Logout_ClearsStoredFileKey(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir() // no OAuth record
	if err := store.Set("openai", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	resp, err := c.Logout(appwire.AuthLogoutParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !resp.Removed || resp.Status.ActiveSource != authopenai.AuthSourceSignedOut {
		t.Fatalf("resp=%+v, want removed + signed-out", resp)
	}
	if v, _ := store.Get("openai"); v != "" {
		t.Errorf("file key still present: %q", v)
	}
}

func TestAuth_OpenAI_Logout_OAuthRevealsStoredFileKey(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	if err := store.Set("openai", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	if err := authopenai.SaveAuth(c.stateDir, authopenai.AuthRecord{
		Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth,
		ObtainedAt: time.Now().Add(-time.Hour), TokenType: "Bearer",
		AccessToken: "acc", RefreshToken: "ref",
		Expiry: time.Now().Add(time.Hour), Email: "o@example.com",
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	resp, err := c.Logout(appwire.AuthLogoutParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !resp.Removed || resp.Status.ActiveSource != string(credentials.SourceFile) {
		t.Fatalf("resp=%+v, want removed OAuth revealing file", resp)
	}
	if resp.Status.HasStoredOAuth {
		t.Errorf("OAuth record still present after logout")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/serf-hub/ -run 'TestAuth_OpenAI_Logout_' -v`
Expected: FAIL — current `Logout` calls `DeleteAuth` only; `ClearsStoredFileKey` gets `Removed false` (no OAuth file to delete) and the key is not cleared.

- [ ] **Step 3: Make the OpenAI branch clear the effective layer**

In `cmd/serf-hub/app_auth.go`, replace the `Logout` function with:

```go
func (c *hubAuthController) Logout(params appwire.AuthLogoutParams) (appwire.AuthLogoutResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if provider != "openai" {
		if err := c.creds.Clear(provider); err != nil {
			return appwire.AuthLogoutResponse{}, err
		}
		status, _ := c.Status(appwire.AuthStatusParams{Provider: provider})
		return appwire.AuthLogoutResponse{Removed: true, Status: status}, nil
	}

	// OpenAI: clear the effective layer only. An OAuth record (present or
	// corrupt) shadows the stored file key, so remove it first; otherwise clear
	// the file key. The env layer cannot be cleared.
	_, loadErr := authopenai.LoadAuth(c.stateDir)
	hasRecord := loadErr == nil || errors.Is(loadErr, authopenai.ErrAuthCorrupt)
	removed := false
	if hasRecord {
		r, delErr := authopenai.DeleteAuth(c.stateDir)
		if delErr != nil {
			return appwire.AuthLogoutResponse{}, delErr
		}
		removed = r
	} else {
		hasFile, _ := c.creds.Layers("openai")
		if hasFile {
			if clrErr := c.creds.Clear("openai"); clrErr != nil {
				return appwire.AuthLogoutResponse{}, clrErr
			}
			removed = true
		}
	}
	status, statusErr := c.openAIStatus()
	if statusErr != nil {
		return appwire.AuthLogoutResponse{}, statusErr
	}
	return appwire.AuthLogoutResponse{Removed: removed, Status: status}, nil
}
```

- [ ] **Step 4: Run the new tests and the existing logout test**

Run: `go test ./cmd/serf-hub/ -run 'TestAuth_OpenAI_Logout_|TestHubRPCAuthLogoutRemovesUserScopedOpenAIAuth' -v`
Expected: PASS (new tests pass; existing OAuth-only logout still reports removed + signed-out).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/app_auth.go cmd/serf-hub/app_auth_test.go
git commit -m "feat(serf-hub): openai logout clears the effective credential layer (PRI-1877)"
```

---

### Task 4: Regression test — a stored OpenAI key flows to spawned sessions via `ToEnv`

This guards the spec's "expected zero-change" plumbing claim. No production code changes.

**Files:**
- Test: `internal/launchconfig/env_test.go`

- [ ] **Step 1: Write the test**

Append to `internal/launchconfig/env_test.go`:

```go
func TestToEnv_OpenAIStoredKeyInjectsOpenAIAPIKey(t *testing.T) {
	creds := stubCreds{keys: map[string]string{"openai": "sk-FROM-FILE"}}
	got := envSliceToMap(ToEnv(EnvInputs{
		Provider:  "openai",
		Creds:     creds,
		ParentEnv: []string{"PATH=/usr/bin"},
	}))
	if got["OPENAI_API_KEY"] != "sk-FROM-FILE" {
		t.Errorf("OPENAI_API_KEY = %q, want sk-FROM-FILE", got["OPENAI_API_KEY"])
	}
}
```

- [ ] **Step 2: Run the test (expect PASS without code changes)**

Run: `go test ./internal/launchconfig/ -run TestToEnv_OpenAIStoredKeyInjectsOpenAIAPIKey -v`
Expected: PASS — `ToEnv` already maps `openai → OPENAI_API_KEY` and injects `Creds.APIKeyFor("openai")`. If this FAILS, stop: the spec's plumbing assumption is wrong and the design needs revisiting before proceeding.

- [ ] **Step 3: Commit**

```bash
git add internal/launchconfig/env_test.go
git commit -m "test(launchconfig): guard openai stored key injection to spawns (PRI-1877)"
```

---

### Task 5: Update the evergreen doc — `cmd/serf-hub/README.md` Provider Credentials

**Files:**
- Modify: `cmd/serf-hub/README.md`

- [ ] **Step 1: Update the credentials section**

In `cmd/serf-hub/README.md`, add `openai` to the TOML example by replacing:

```toml
[providers.anthropic]
api_key = "sk-ant-..."

[providers.openrouter]
api_key = "..."
```

with:

```toml
[providers.anthropic]
api_key = "sk-ant-..."

[providers.openai]
api_key = "sk-..."

[providers.openrouter]
api_key = "..."
```

Then replace this paragraph:

```
The Hub UI (`/credentials`) or TUI (`:credentials`) writes this file via
the `serf/auth/apiKey/set` RPC. OpenAI OAuth state remains in the existing
`~/.serf/auth/openai.json` file; OAuth flows are triggered from the same
UIs via `serf/auth/login/start`.

Process-env credentials (e.g., `ANTHROPIC_API_KEY` exported in the shell)
still work as a fallback when no file entry exists for the provider —
matches the existing `hub.env` style for users who prefer external secret
management.
```

with:

```
The Hub UI (`/credentials`) or TUI (`:credentials`) writes this file via
the `serf/auth/apiKey/set` RPC. Process-env credentials (e.g.,
`ANTHROPIC_API_KEY` exported in the shell) still work as a fallback when no
file entry exists for the provider — matching the `hub.env` style for users
who prefer external secret management.

### OpenAI credential resolution

OpenAI supports both an API key (stored in `credentials.toml` like any other
provider, or via `OPENAI_API_KEY`) and OAuth (sign in via
`serf/auth/login/start`; state stored in `~/.serf/auth/openai.json`).

The effective credential is resolved by precedence:

1. **OAuth** record (`openai.json`), if signed in;
2. **file** key (`credentials.toml`);
3. **`OPENAI_API_KEY`** env var.

The file layer shadows env, like other providers; an explicit OAuth sign-in
wins over both. The two routes hit **different backends**: OAuth routes to the
ChatGPT/Codex backend (`OPENAI_CHATGPT_BASE_URL`), while an API key routes to
the standard OpenAI API backend (`OPENAI_BASE_URL`). They are not
interchangeable credentials for one endpoint.
```

- [ ] **Step 2: Commit**

```bash
git add cmd/serf-hub/README.md
git commit -m "docs(serf-hub): document OpenAI API-key support and cred precedence (PRI-1877)"
```

---

### Task 6: Full verification

**Files:** none (verification only)

- [ ] **Step 1: Build**

Run: `go build ./...`
Expected: no output (success).

- [ ] **Step 2: Run the affected packages' suites**

Run: `go test ./cmd/serf-hub/... ./internal/launchconfig/... ./internal/credentials/... ./internal/auth/openai/... ./llm/providers/openai/...`
Expected: all PASS. Output must be pristine — investigate any new warning or log line, don't ignore it.

- [ ] **Step 3: Manual webui smoke check (optional but recommended)**

Start the hub, open `/credentials`, and confirm for OpenAI: "Set API key" saves without error and the row shows `file`; "Clear" removes it; if signed in via OAuth, the row shows `oauth`. (Use the `run` skill or the project's local-dev flow to launch the hub.)

- [ ] **Step 4: Move ticket to In Review + reflective comment**

Per `primeradiant-ops:linear-ticket-lifecycle`: move PRI-1877 to **In Review** and add a genuine reflective implementation comment (what was smooth, what was tricky, risk flags).

---

## Self-Review

**Spec coverage:**
- §4.2 change 1 (`ApiKeySet` unblock) → Task 2. ✓
- §4.2 change 2 (layer-aware status: ActiveSource precedence, `HasStoredFile`, `EnvVar`) → Task 1. ✓
- §4.2 change 3 (`Logout` clears effective layer) → Task 3. ✓
- §4.2 "expected zero-change" plumbing (`ToEnv`) → Task 4 (regression guard). ✓
- §5 error handling: corrupt `openai.json` tolerated → Task 1 (`CorruptOAuthFallsBackToFile`). ✓
- §5 expired-OAuth behavior unchanged → no task touches `ResolveRuntimeCredentials`; status path keeps reporting `oauth` for a present record via `openAIStatusFromRecord` (incl. `NeedsLogin`). ✓
- §6 testing (store, app_auth, ToEnv, adapter) → Tasks 1-4 + Task 6 runs adapter/credentials suites. ✓
- §7 Documentation (README evergreen) → Task 5. ✓
- Frontend: spec says verify-only; covered by Task 6 Step 3 manual smoke. ✓

**Placeholder scan:** none — every code/test step has complete code and exact run commands.

**Type/name consistency:** `openAIStatus`, `ApiKeySet`, `Logout` signatures match `cmd/serf-hub/app_auth.go`; `credentials.SourceFile`/`SourceEnv`, `authopenai.AuthSource*`, `c.creds.Layers`, `authopenai.AuthFilePath`, `appwire.AuthStatusResponse.{HasStoredFile,EnvVar,HasStoredOAuth,StoredEmail}`, and `stubCreds{keys:...}` all verified against current source.
