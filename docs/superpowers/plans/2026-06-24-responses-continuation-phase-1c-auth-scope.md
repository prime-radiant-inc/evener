# Responses Continuation Phase 1C Auth Scope Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add sanitized OpenAI auth-scope identity construction so later continuation planners can compare provider storage scope without seeing raw API keys or OAuth tokens.

**Architecture:** Keep raw credentials on the existing OpenAI transport fields and add sanitized identity fields beside them. The continuation hasher remains in `llm`; the OpenAI adapter construction layer derives hashes from API key, OAuth account/workspace identifiers, and org/project identifiers before any planner code exists.

**Tech Stack:** Go, `llm.ContinuationHasher`, OpenAI adapter construction tests, deterministic unit tests only.

---

## File Structure

- `llm/responses_continuation.go`: owns cross-provider continuation metadata types, including `AuthScopeIdentity`.
- `llm/continuation_secret.go`: owns versioned continuation HMAC helpers and allowed scope kinds.
- `llm/providers/openai/adapter.go`: stores sanitized auth scope on `Adapter`, derives it in `NewForInstance`, and keeps raw credentials in transport-only fields.
- `llm/providers/openai/adapter_test.go`: proves API-key, org/project, and OAuth auth-scope derivation without live provider calls.

## Non-Goals

- Do not enable `responses_delta` runtime selection.
- Do not compute request fingerprints or storage-scope fingerprints.
- Do not persist continuation metadata from auth scope.
- Do not pass raw credentials to planner helpers; this phase only creates sanitized fields for future helpers.

### Task 1: Add AuthScopeIdentity Type

**Files:**
- Modify: `llm/responses_continuation.go`

- [ ] **Step 1: Write the failing compile contract through OpenAI tests**

Add tests in Task 2 first that reference `llm.AuthScopeIdentity`; this task should fail to compile until the type exists.

- [ ] **Step 2: Add the type**

Add this type after `ContinuationMetadata`:

```go
type AuthScopeIdentity struct {
	Version        string
	AuthSource     string
	CredentialHash string
	AccountHash    string
	WorkspaceHash  string
}
```

- [ ] **Step 3: Run focused compile**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestNewForInstance_ContinuationAuthScope' -count=1
```

Expected: fails until Task 2 implementation is complete.

### Task 2: Derive API-Key Auth Scope

**Files:**
- Modify: `llm/providers/openai/adapter.go`
- Modify: `llm/providers/openai/adapter_test.go`

- [ ] **Step 1: Write the failing test**

Add this test near the existing `NewForInstance` tests:

```go
func TestNewForInstance_ContinuationAuthScope_APIKey(t *testing.T) {
	hasher := llm.NewContinuationHasher([]byte("01234567890123456789012345678901"))
	wantCredentialHash, err := hasher.HashContinuationScopeValue("credential", "sk-test")
	if err != nil {
		t.Fatalf("HashContinuationScopeValue credential: %v", err)
	}
	wantOrgHash, err := hasher.HashContinuationScopeValue("org_id", "org-123")
	if err != nil {
		t.Fatalf("HashContinuationScopeValue org: %v", err)
	}
	wantProjectHash, err := hasher.HashContinuationScopeValue("project_id", "proj-456")
	if err != nil {
		t.Fatalf("HashContinuationScopeValue project: %v", err)
	}

	a, err := NewForInstance(OpenAIInstanceParams{
		Name:               "work",
		APIKey:             " sk-test ",
		OrgID:              " org-123 ",
		ProjectID:          " proj-456 ",
		ContinuationHasher: hasher,
	})
	if err != nil {
		t.Fatalf("NewForInstance: %v", err)
	}
	if a.AuthScopeIdentity.Version != "cont-scope-v1" {
		t.Fatalf("Version = %q", a.AuthScopeIdentity.Version)
	}
	if a.AuthScopeIdentity.AuthSource != "api_key" {
		t.Fatalf("AuthSource = %q", a.AuthScopeIdentity.AuthSource)
	}
	if a.AuthScopeIdentity.CredentialHash != wantCredentialHash {
		t.Fatalf("CredentialHash = %q, want %q", a.AuthScopeIdentity.CredentialHash, wantCredentialHash)
	}
	if a.OrgIDHash != wantOrgHash {
		t.Fatalf("OrgIDHash = %q, want %q", a.OrgIDHash, wantOrgHash)
	}
	if a.ProjectIDHash != wantProjectHash {
		t.Fatalf("ProjectIDHash = %q, want %q", a.ProjectIDHash, wantProjectHash)
	}
	if strings.Contains(a.AuthScopeIdentity.CredentialHash, "sk-test") {
		t.Fatalf("CredentialHash leaks raw key: %q", a.AuthScopeIdentity.CredentialHash)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestNewForInstance_ContinuationAuthScope_APIKey' -count=1
```

Expected: FAIL because `ContinuationHasher`, `AuthScopeIdentity`, `OrgIDHash`, and `ProjectIDHash` are not wired.

- [ ] **Step 3: Add adapter fields and params**

Add these fields to `Adapter`:

```go
	AuthScopeIdentity llm.AuthScopeIdentity
	OrgIDHash         string
	ProjectIDHash     string
```

Add this field to `OpenAIInstanceParams`:

```go
	ContinuationHasher *llm.ContinuationHasher
```

- [ ] **Step 4: Add helper functions**

Add helpers in `adapter.go`:

```go
func authScopeForAPIKey(hasher *llm.ContinuationHasher, apiKey string) (llm.AuthScopeIdentity, error) {
	if hasher == nil {
		return llm.AuthScopeIdentity{}, nil
	}
	hash, err := hasher.HashContinuationScopeValue("credential", apiKey)
	if err != nil {
		return llm.AuthScopeIdentity{}, err
	}
	return llm.AuthScopeIdentity{
		Version:        "cont-scope-v1",
		AuthSource:     "api_key",
		CredentialHash: hash,
	}, nil
}

func hashOpenAIScopeIdentifier(hasher *llm.ContinuationHasher, kind, value string) (string, error) {
	if hasher == nil || strings.TrimSpace(value) == "" {
		return "", nil
	}
	return hasher.HashContinuationScopeValue(kind, value)
}
```

- [ ] **Step 5: Populate API-key adapter fields**

In the API-key branch of `NewForInstance`, compute `authScope`, `orgHash`, and `projectHash` from the trimmed key/org/project values and store them on the returned adapter.

- [ ] **Step 6: Run the focused test**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestNewForInstance_ContinuationAuthScope_APIKey' -count=1 -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

Run:

```sh
git status --short
git add llm/responses_continuation.go llm/providers/openai/adapter.go llm/providers/openai/adapter_test.go
git commit -m "feat(openai): derive api key continuation auth scope"
```

### Task 3: Derive OAuth Auth Scope

**Files:**
- Modify: `llm/providers/openai/adapter.go`
- Modify: `llm/providers/openai/adapter_test.go`

- [ ] **Step 1: Write the failing test**

Add this test near the OAuth construction tests:

```go
func TestNewFromEnv_ContinuationAuthScope_OAuth(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	hasher := llm.NewContinuationHasher([]byte("01234567890123456789012345678901"))
	userStateDir := authopenai.DefaultStateDir()
	if err := authopenai.SaveAuth(userStateDir, "openai", authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   time.Now().Add(-time.Minute).UTC(),
		TokenType:    "Bearer",
		Scope:        "openid profile email offline_access",
		AccessToken:  "oauth-token",
		RefreshToken: "refresh-token",
		Expiry:       time.Now().Add(time.Hour).UTC(),
		AccountID:    "acct_123",
		WorkspaceID:  "ws_456",
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	wantAccountHash, err := hasher.HashContinuationScopeValue("account", "acct_123")
	if err != nil {
		t.Fatalf("HashContinuationScopeValue account: %v", err)
	}
	wantWorkspaceHash, err := hasher.HashContinuationScopeValue("workspace", "ws_456")
	if err != nil {
		t.Fatalf("HashContinuationScopeValue workspace: %v", err)
	}
	wantCredentialHash, err := hasher.HashContinuationScopeValue("credential", "oauth:acct_123:ws_456")
	if err != nil {
		t.Fatalf("HashContinuationScopeValue credential: %v", err)
	}

	a, err := NewForInstance(OpenAIInstanceParams{
		Name:               "openai",
		StateHome:          os.Getenv("XDG_STATE_HOME"),
		ContinuationHasher: hasher,
	})
	if err != nil {
		t.Fatalf("NewForInstance: %v", err)
	}
	if a.AuthScopeIdentity.AuthSource != "oauth" {
		t.Fatalf("AuthSource = %q", a.AuthScopeIdentity.AuthSource)
	}
	if a.AuthScopeIdentity.AccountHash != wantAccountHash {
		t.Fatalf("AccountHash = %q, want %q", a.AuthScopeIdentity.AccountHash, wantAccountHash)
	}
	if a.AuthScopeIdentity.WorkspaceHash != wantWorkspaceHash {
		t.Fatalf("WorkspaceHash = %q, want %q", a.AuthScopeIdentity.WorkspaceHash, wantWorkspaceHash)
	}
	if a.AuthScopeIdentity.CredentialHash != wantCredentialHash {
		t.Fatalf("CredentialHash = %q, want %q", a.AuthScopeIdentity.CredentialHash, wantCredentialHash)
	}
	for _, raw := range []string{"oauth-token", "refresh-token", "acct_123", "ws_456"} {
		if strings.Contains(a.AuthScopeIdentity.CredentialHash, raw) {
			t.Fatalf("CredentialHash leaks %q: %q", raw, a.AuthScopeIdentity.CredentialHash)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestNewFromEnv_ContinuationAuthScope_OAuth' -count=1
```

Expected: FAIL because the OAuth branch does not populate `AuthScopeIdentity`.

- [ ] **Step 3: Add OAuth helper**

Add this helper in `adapter.go`:

```go
func authScopeForOAuth(hasher *llm.ContinuationHasher, accountID, workspaceID string) (llm.AuthScopeIdentity, error) {
	if hasher == nil {
		return llm.AuthScopeIdentity{}, nil
	}
	accountHash, err := hashOpenAIScopeIdentifier(hasher, "account", accountID)
	if err != nil {
		return llm.AuthScopeIdentity{}, err
	}
	workspaceHash, err := hashOpenAIScopeIdentifier(hasher, "workspace", workspaceID)
	if err != nil {
		return llm.AuthScopeIdentity{}, err
	}
	credentialHash, err := hasher.HashContinuationScopeValue("credential", "oauth:"+strings.TrimSpace(accountID)+":"+strings.TrimSpace(workspaceID))
	if err != nil {
		return llm.AuthScopeIdentity{}, err
	}
	return llm.AuthScopeIdentity{
		Version:        "cont-scope-v1",
		AuthSource:     "oauth",
		CredentialHash: credentialHash,
		AccountHash:    accountHash,
		WorkspaceHash:  workspaceHash,
	}, nil
}
```

- [ ] **Step 4: Populate OAuth adapter field**

In the OAuth branch of `NewForInstance`, preserve the existing raw bearer token path, compute auth scope from stable account/workspace identifiers, and assign it to `Adapter.AuthScopeIdentity`.

- [ ] **Step 5: Run focused OAuth tests**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestNewFromEnv_ContinuationAuthScope_OAuth|TestNewFromEnv_PrefersStoredOAuthOverAPIKey|TestNewFromEnv_UsesStoredOAuthTransportWhenAPIKeyAbsent' -count=1 -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

Run:

```sh
git status --short
git add llm/providers/openai/adapter.go llm/providers/openai/adapter_test.go
git commit -m "feat(openai): derive oauth continuation auth scope"
```

### Task 4: Verify Scope and Document Proof

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-1c.md`

- [ ] **Step 1: Run deterministic tests**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm ./llm/providers/openai -run 'TestContinuation|TestNewForInstance_ContinuationAuthScope|TestNewFromEnv_ContinuationAuthScope|TestNewFromEnv_ReadsOrgAndProjectID|TestInstanceFactory_EnvTunables_APIKeyPath' -count=1 -v
git diff --check
```

Expected: all tests pass and `git diff --check` prints nothing.

- [ ] **Step 2: Write proof**

Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-1c.md` with:

```markdown
# Responses Continuation Phase 1C Proof

## Scope

Phase 1C adds sanitized OpenAI auth-scope identity construction for future continuation storage-scope planning.

Runtime continuation remains disabled. This phase does not send `previous_response_id`, does not send continuation-owned `store:true`, and does not persist auth scope to transcripts or API logs.

## Evidence

- `GOCACHE=/tmp/serf-gocache go test ./llm ./llm/providers/openai -run 'TestContinuation|TestNewForInstance_ContinuationAuthScope|TestNewFromEnv_ContinuationAuthScope|TestNewFromEnv_ReadsOrgAndProjectID|TestInstanceFactory_EnvTunables_APIKeyPath' -count=1 -v`
- `git diff --check`

## Contracts Proven

- API-key adapters keep the raw API key only on the existing transport field and expose a versioned credential HMAC for continuation planning.
- OpenAI org/project identifiers keep their raw header fields and expose separate versioned HMACs for continuation planning.
- OAuth/Codex adapters keep the raw bearer token only on the existing transport field and expose versioned credential/account/workspace HMACs from stable auth-record identifiers.
- Missing `ContinuationHasher` leaves auth-scope fields empty, so default construction behavior remains unchanged until a later runtime phase supplies the hasher.
```

- [ ] **Step 3: Commit proof**

Run:

```sh
git status --short
git add docs/superpowers/plans/2026-06-24-responses-continuation-phase-1c-auth-scope.md docs/superpowers/proofs/2026-06-24-responses-continuation-phase-1c.md
git commit -m "docs: record responses continuation phase 1c proof"
```

## Self-Review

- Spec coverage: covers sanitized `AuthScopeIdentity`, API-key credential hashes, OAuth account/workspace hashes, and org/project hashes. Request fingerprinting, storage-scope fingerprinting, runtime selection, and transcript persistence are intentionally deferred to later phases.
- Placeholder scan: no TBD/TODO/fill-in steps.
- Type consistency: `AuthScopeIdentity`, `ContinuationHasher`, `OrgIDHash`, and `ProjectIDHash` names are consistent across plan steps.
