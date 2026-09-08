# Provider Sheet Editor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The provider instance sheet becomes the instance's editor: name, base URL, protocol, surface, provider variables, API key env var and credential header edit in place with a dirty-gated Save, and the backend can rename an instance and set or clear every one of those fields.

**Architecture:** Additive `evener-appwire-v4` fields on `InstanceEditParams` (rename, api-key-env and credential-header set/clear, protocol/surface clear, var deletion) and two authored fields on `InstanceEntry` so the form can prefill. The hub's `Edit` applies the new pairs and, for a rename, re-keys providers.toml, repoints the default, and moves the stored key and OAuth record. On the frontend `InstanceDetailSheet` becomes `InstanceSheet` with a form above the existing credential actions; `EditInstanceDialog` is deleted; the Add form gains the same protocol and surface selects. The design-system rule is rewritten to say a detail sheet is the editor.

**Tech Stack:** Go (appwire, cmd/evener-hub, llm/registry), TypeScript + React + zustand + vitest, `make generate`.

**Spec:** `docs/superpowers/specs/2026-09-07-settings-sheet-editors-and-agents-doc-design.md` §2 and §4 (plus §5, §6). This plan is slice 2 of 3; it does not depend on slice 1.

## Global Constraints

- Repo root for every command: the git worktree you were started in. Frontend commands run from `cmd/evener-hub/frontend`.
- Protocol version stays `evener-appwire-v4`; every wire change is additive and keeps "empty means unchanged" for string fields.
- Protocol values: `openai-chat`, `openai-responses`, `anthropic`, `google`. Surface values: `openai`, `anthropic`, `google`, `generic`. The registry rejects anything else on write (`llm/registry/schema.go`).
- A credential header on the wire is `NAME=VALUE`; the value must reference a `$VAR` (`registry.CheckCredentialHeaderValue`); never a literal secret. The client refuses a value without `$` before any RPC, as the Add form already does.
- Rename is refused for implicit instances (`InvalidParams`), for an invalid name (`InvalidParams`, `registry.ValidInstanceName`), and for a name any instance already has (`Conflict`).
- No `git add -A`. Commit after every task with the exact message given.
- Frontend: `npx biome check --write <touched files under src/>` before every commit that touches frontend files; gate is `make test-web` from the repo root.
- Tests never read the developer's config root; every Go instances test uses `newInstancesFixture` (isolated temp dirs).
- Follow each file's comment style: explain why, never narrate what changed.

---

### Task 1: Wire types

**Files:**
- Modify: `appwire/types.go` (the `InstanceEditParams` doc comment and struct near line 2762; the `InstanceEntry` struct near line 2671)
- Regenerate: `cmd/evener-hub/frontend/src/protocol/types.gen.ts`, `docs/appwire-protocol.md`

**Interfaces:**
- Produces on `InstanceEditParams`: `NewName string json:"newName,omitempty"`, `ClearProtocol bool json:"clearProtocol,omitempty"`, `ClearSurface bool json:"clearSurface,omitempty"`, `APIKeyEnv string json:"apiKeyEnv,omitempty"`, `ClearAPIKeyEnv bool json:"clearApiKeyEnv,omitempty"`, `CredentialHeader string json:"credentialHeader,omitempty"`, `ClearCredentialHeader bool json:"clearCredentialHeader,omitempty"`.
- Produces on `InstanceEntry`: `APIKeyEnv string json:"apiKeyEnv,omitempty"`, `CredentialHeader string json:"credentialHeader,omitempty"`.

- [ ] **Step 1: Replace the InstanceEditParams doc comment and struct**

In `appwire/types.go`, replace the paragraph that begins `// Protocol and Surface have no clear operation yet` (three lines, through `// treatment as BaseURL is ledgered for whenever a form needs to clear one.`) with:

```go
// The 2026-09-07 sheet-editor additions keep the same rule. NewName renames
// the instance (empty means unchanged). APIKeyEnv/ClearAPIKeyEnv and
// CredentialHeader/ClearCredentialHeader set or drop the authored
// api_key_env and credential_headers; CredentialHeader is NAME=VALUE with a
// $VAR value, exactly as InstanceCreateParams takes it. ClearProtocol and
// ClearSurface are the clears the paragraph above ledgered. A Vars entry
// whose value is empty DELETES that variable: a blank var never meant
// anything, so the empty value is free to mean "remove". Each set/clear
// pair follows BaseURL/ClearBaseURL: never both meaningful in one request.
```

Then replace the struct with:

```go
type InstanceEditParams struct {
	Name                  string            `json:"name"`
	NewName               string            `json:"newName,omitempty"`
	BaseURL               string            `json:"baseUrl,omitempty"`
	ClearBaseURL          bool              `json:"clearBaseUrl,omitempty"`
	Protocol              string            `json:"protocol,omitempty"`
	ClearProtocol         bool              `json:"clearProtocol,omitempty"`
	Surface               string            `json:"surface,omitempty"`
	ClearSurface          bool              `json:"clearSurface,omitempty"`
	Vars                  map[string]string `json:"vars,omitempty"`
	APIKeyEnv             string            `json:"apiKeyEnv,omitempty"`
	ClearAPIKeyEnv        bool              `json:"clearApiKeyEnv,omitempty"`
	CredentialHeader      string            `json:"credentialHeader,omitempty"`
	ClearCredentialHeader bool              `json:"clearCredentialHeader,omitempty"`
}
```

- [ ] **Step 2: Add the authored fields to InstanceEntry**

In the `InstanceEntry` struct, after the `Vars` field, add:

```go
	// APIKeyEnv and CredentialHeader are the AUTHORED api_key_env (its first
	// entry) and credential header (as NAME=VALUE) from providers.toml, so
	// the sheet's form can prefill them. Never the registry's own defaults
	// for an implicit instance, and never a secret: a credential header
	// value is a $VAR template by construction
	// (registry.CheckCredentialHeaderValue refuses a literal).
	APIKeyEnv        string `json:"apiKeyEnv,omitempty"`
	CredentialHeader string `json:"credentialHeader,omitempty"`
```

- [ ] **Step 3: Build, regenerate, verify**

Run: `go build ./... && make generate && git status --short`
Expected: build OK; `types.gen.ts` and `docs/appwire-protocol.md` modified. Confirm: `grep -n "clearProtocol\|newName\|credentialHeader" cmd/evener-hub/frontend/src/protocol/types.gen.ts | head`.

Run: `go test ./appwire ./internal/appwirets 2>&1 | tail -4`
Expected: ok.

- [ ] **Step 4: Commit**

```bash
git add appwire/types.go cmd/evener-hub/frontend/src/protocol/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(appwire): rename, credential-field, and clear additions to instance/edit"
```

---

### Task 2: The list reports the authored API key env and credential header

**Files:**
- Modify: `cmd/evener-hub/app_instances.go` (`List` at line 43, `entryFor` at line 87)
- Test: `cmd/evener-hub/app_instances_test.go`

**Interfaces:**
- Produces: `entryFor(inst registry.Instance, authored *registry.Provider) appwire.InstanceEntry`; `credentialHeaderField(headers map[string]string) string`.

- [ ] **Step 1: Write the failing test**

Append to `cmd/evener-hub/app_instances_test.go`:

```go
// TestInstances_ListReportsAuthoredCredentialFields: the sheet's form
// prefills api_key_env and the credential header from what the user
// authored, and only that - an implicit instance inherits both from the
// registry and shows neither.
func TestInstances_ListReportsAuthoredCredentialFields(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk", "PORTKEY_KEY": "pk"})
	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name: "work", Base: "openai", APIKeyEnv: "PORTKEY_KEY", CredentialHeader: "Authorization=Bearer $PORTKEY_KEY",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	work := entry(t, f.ctl.List(), "work")
	if work.APIKeyEnv != "PORTKEY_KEY" {
		t.Fatalf("APIKeyEnv = %q, want PORTKEY_KEY", work.APIKeyEnv)
	}
	if work.CredentialHeader != "Authorization=Bearer $PORTKEY_KEY" {
		t.Fatalf("CredentialHeader = %q", work.CredentialHeader)
	}
	groq := entry(t, f.ctl.List(), "groq")
	if groq.APIKeyEnv != "" || groq.CredentialHeader != "" {
		t.Fatalf("an implicit instance has nothing authored, got apiKeyEnv=%q credentialHeader=%q", groq.APIKeyEnv, groq.CredentialHeader)
	}
}

func TestCredentialHeaderField_RendersTheFirstHeaderInSortedOrder(t *testing.T) {
	if got := credentialHeaderField(nil); got != "" {
		t.Fatalf("nil = %q", got)
	}
	got := credentialHeaderField(map[string]string{"X-Key": "$B", "Authorization": "Bearer $A"})
	if got != "Authorization=Bearer $A" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestInstances_ListReportsAuthoredCredentialFields|TestCredentialHeaderField' 2>&1 | head -8`
Expected: compile error `undefined: credentialHeaderField`.

- [ ] **Step 3: Implement**

In `cmd/evener-hub/app_instances.go`, change `List` so the instance loop reads the authored layer once:

```go
	r := c.reg.Get()
	if r != nil {
		// The authored layer is what the sheet's form edits, so the entry
		// carries the authored credential fields alongside the registry's
		// resolved view. A file that cannot be read right now simply
		// prefills nothing; the refusal itself is already in Diagnostics.
		var layer *registry.Layer
		if l, exists, err := c.read(); err == nil && exists {
			layer = l
		}
		for _, inst := range r.Instances() {
			var authored *registry.Provider
			if layer != nil {
				if p, ok := layer.Providers[inst.Name]; ok {
					authored = &p
				}
			}
			entries = append(entries, c.entryFor(inst, authored))
		}
```

Change `entryFor`'s signature to `func (c *hubInstancesController) entryFor(inst registry.Instance, authored *registry.Provider) appwire.InstanceEntry`, build the entry into a local `entry := appwire.InstanceEntry{...}` (same literal as today), and before returning it add:

```go
	if authored != nil {
		if len(authored.APIKeyEnv) > 0 {
			entry.APIKeyEnv = authored.APIKeyEnv[0]
		}
		entry.CredentialHeader = credentialHeaderField(authored.CredentialHeaders)
	}
	return entry
```

Add below `entryFor`:

```go
// credentialHeaderField renders the authored credential_headers map as the
// single NAME=VALUE field the forms use; credentialHeaderFrom is its
// inverse. Several headers are possible by hand-editing the file, never
// through the pane; the first in sorted order is the one the form edits.
func credentialHeaderField(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	names := slices.Sorted(maps.Keys(headers))
	return names[0] + "=" + headers[names[0]]
}
```

`slices` and `maps` are already imported by this file. Then run `grep -n "entryFor(" cmd/evener-hub/*.go` and pass `nil` as the second argument at every other call site (there may be none besides `List`).

- [ ] **Step 4: Run the instances tests and the wire-fixture corpus test**

Run: `go test ./cmd/evener-hub -run 'TestInstances_|TestCredentialHeaderField|TestAuthWireFixtures' 2>&1 | tail -8`
Expected: PASS. If `TestAuthWireFixturesMatchTheHubHandler` fails because a scenario now carries `apiKeyEnv` or `credentialHeader`, regenerate the corpus exactly as its failure message says (`go test ./cmd/evener-hub -run TestAuthWireFixtures -update-authwire`), then run `go test ./cmd/evener-tui/... 2>&1 | tail -3` and, from `cmd/evener-hub/frontend`, `npx vitest run src/panes/settings/sections/credentials 2>&1 | tail -5`, and include `cmd/evener-hub/testdata/authwire/responses.json` in the commit.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/app_instances.go cmd/evener-hub/app_instances_test.go
git commit -m "feat(hub): report an instance's authored api_key_env and credential header"
```

(Add `cmd/evener-hub/testdata/authwire/responses.json` to the `git add` if it was regenerated.)

---

### Task 3: Edit applies the new field pairs, the clears, and var deletion

**Files:**
- Modify: `cmd/evener-hub/app_instances.go` (`Edit`, lines 280-360)
- Test: `cmd/evener-hub/app_instances_test.go`

**Interfaces:**
- Consumes: Task 1's fields; the existing `credentialHeaderFrom(field string) (map[string]string, error)` and `validVarNames`.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/evener-hub/app_instances_test.go`:

```go
func TestInstances_EditSetsAndClearsAPIKeyEnvAndCredentialHeader(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk", "PORTKEY_KEY": "pk"})
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := f.ctl.Edit(appwire.InstanceEditParams{
		Name: "work", APIKeyEnv: "PORTKEY_KEY", CredentialHeader: "Authorization=Bearer $PORTKEY_KEY",
	}); err != nil {
		t.Fatalf("Edit(set): %v", err)
	}
	p := authoredEntry(t, f.tomlPath, "work")
	if !slices.Equal(p.APIKeyEnv, []string{"PORTKEY_KEY"}) {
		t.Fatalf("authored api_key_env = %v", p.APIKeyEnv)
	}
	if p.CredentialHeaders["Authorization"] != "Bearer $PORTKEY_KEY" {
		t.Fatalf("authored credential_headers = %v", p.CredentialHeaders)
	}
	if e := entry(t, f.ctl.List(), "work"); e.APIKeyEnv != "PORTKEY_KEY" || e.CredentialHeader != "Authorization=Bearer $PORTKEY_KEY" {
		t.Fatalf("List = apiKeyEnv %q credentialHeader %q", e.APIKeyEnv, e.CredentialHeader)
	}

	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", ClearAPIKeyEnv: true, ClearCredentialHeader: true}); err != nil {
		t.Fatalf("Edit(clear): %v", err)
	}
	p = authoredEntry(t, f.tomlPath, "work")
	if len(p.APIKeyEnv) != 0 || len(p.CredentialHeaders) != 0 {
		t.Fatalf("after clearing: api_key_env %v credential_headers %v", p.APIKeyEnv, p.CredentialHeaders)
	}
	if e := entry(t, f.ctl.List(), "work"); e.APIKeyEnv != "" || e.CredentialHeader != "" {
		t.Fatalf("List after clearing = apiKeyEnv %q credentialHeader %q", e.APIKeyEnv, e.CredentialHeader)
	}
}

func TestInstances_EditRefusesALiteralCredentialHeader(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", CredentialHeader: "Authorization=Bearer sk-literal"})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("Edit = %v, want an InvalidParams wire error", err)
	}
	if p := authoredEntry(t, f.tomlPath, "work"); len(p.CredentialHeaders) != 0 {
		t.Fatalf("a refused header was written: %v", p.CredentialHeaders)
	}
}

func TestInstances_EditClearsProtocolAndSurface(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name: "work", Base: "openai", Protocol: "openai-responses", Surface: "generic",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p := authoredEntry(t, f.tomlPath, "work"); p.Protocol != "openai-responses" || p.Surface != "generic" {
		t.Fatalf("authored = protocol %q surface %q", p.Protocol, p.Surface)
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", ClearProtocol: true, ClearSurface: true}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if p := authoredEntry(t, f.tomlPath, "work"); p.Protocol != "" || p.Surface != "" {
		t.Fatalf("after clearing: protocol %q surface %q", p.Protocol, p.Surface)
	}
	if e := entry(t, f.ctl.List(), "work"); e.Protocol == "" {
		t.Fatal("the resolved protocol must fall back to the base's, not vanish")
	}
}

func TestInstances_EditDeletesAVarGivenAnEmptyValue(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name: "work", Base: "openai", Vars: map[string]string{"REGION": "us-east-1", "ZONE": "a"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", Vars: map[string]string{"REGION": ""}}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	got := readConfigProviders(t, f.tomlPath)["work"].Transport.Vars
	if _, still := got["REGION"]; still {
		t.Fatalf("REGION survived an empty-value edit: %v", got)
	}
	if got["ZONE"] != "a" {
		t.Fatalf("an untouched var changed: %v", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestInstances_EditSetsAndClears|TestInstances_EditRefusesALiteral|TestInstances_EditClearsProtocol|TestInstances_EditDeletesAVar' 2>&1 | tail -12`
Expected: the four tests FAIL (fields silently ignored, no refusal, var kept).

- [ ] **Step 3: Implement**

In `Edit`, directly after the `validVarNames` check at the top, add the header validation before any lock is taken:

```go
	credentialHeaders, err := credentialHeaderFrom(params.CredentialHeader)
	if err != nil {
		return err
	}
```

(`credentialHeaderFrom` returns `nil, nil` for an empty field and an `InvalidParams` wire error for a malformed one.) Note that `err` is then reused by the existing `before, _, err := c.read()` line: change that line to `before, _, err := c.read()` → `before, _, readErr := c.read(); if readErr != nil { return readErr }` only if the compiler complains about redeclaration; otherwise leave it.

Replace the field-apply block (from `if params.ClearBaseURL {` through the `if len(params.Vars) > 0 { ... }` block) with:

```go
	if params.ClearBaseURL {
		// Drops the authored override and goes back to the registry
		// default, restoring spec §10's credential inheritance from the
		// base provider (#711). Additive over BaseURL's existing "empty
		// means unchanged" (v3): the two are never both meaningful in the
		// same request (appwire.InstanceEditParams doc comment).
		p.Transport.BaseURL = ""
	} else if v := strings.TrimSpace(params.BaseURL); v != "" {
		p.Transport.BaseURL = v
	}
	if params.ClearProtocol {
		p.Protocol = ""
	} else if v := strings.TrimSpace(params.Protocol); v != "" {
		p.Protocol = v
	}
	if params.ClearSurface {
		p.Surface = ""
	} else if v := strings.TrimSpace(params.Surface); v != "" {
		p.Surface = v
	}
	if params.ClearAPIKeyEnv {
		p.APIKeyEnv = nil
	} else if v := strings.TrimSpace(params.APIKeyEnv); v != "" {
		p.APIKeyEnv = []string{v}
	}
	if params.ClearCredentialHeader {
		p.CredentialHeaders = nil
	} else if credentialHeaders != nil {
		p.CredentialHeaders = credentialHeaders
	}
	// An empty value deletes the variable (appwire.InstanceEditParams);
	// anything else is set as sent, over whatever was authored before.
	for key, value := range params.Vars {
		if strings.TrimSpace(value) == "" {
			delete(p.Transport.Vars, key)
			continue
		}
		if p.Transport.Vars == nil {
			p.Transport.Vars = map[string]string{}
		}
		p.Transport.Vars[key] = value
	}
```

If `maps` is now unused in this file after removing `maps.Copy`, it is still used by `List` and `credentialHeaderField`, so the import stays.

- [ ] **Step 4: Run the instances tests**

Run: `go test ./cmd/evener-hub -run 'TestInstances_' 2>&1 | tail -6`
Expected: all PASS, including the pre-existing edit tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/app_instances.go cmd/evener-hub/app_instances_test.go
git commit -m "feat(hub): edit an instance's api_key_env, credential header, and clears"
```

---

### Task 4: Edit renames an instance

**Files:**
- Modify: `cmd/evener-hub/app_instances.go` (`Edit`; new `moveCredentials`)
- Test: `cmd/evener-hub/app_instances_test.go`

**Interfaces:**
- Consumes: `hubAuthController` fields `creds`, `stateDir`, `setCredential`, `clearCredential`, `loadAuth`, `saveAuth`, `deleteAuth` (all exist in `app_auth.go`); `authopenai.ErrAuthNotFound`; `registry.ValidInstanceName`; `appwire.Conflict`.
- Produces: `(*hubInstancesController).moveCredentials(oldName, newName string) error`.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/evener-hub/app_instances_test.go`:

```go
func TestInstances_EditRenamesEntryDefaultStoredKeyAndOAuthRecord(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := authopenai.SaveAuth(f.stateDir, "work", authopenai.AuthRecord{Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	if err := f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "work"}); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}

	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"}); err != nil {
		t.Fatalf("Edit(rename): %v", err)
	}

	l, _, err := registry.ReadConfigFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadConfigFile: %v", err)
	}
	if _, still := l.Providers["work"]; still {
		t.Fatal("[providers.work] survived the rename")
	}
	if _, ok := l.Providers["personal"]; !ok {
		t.Fatalf("[providers.personal] missing; got %v", l.Providers)
	}
	if l.Default != "personal" {
		t.Fatalf("default = %q, want the renamed instance", l.Default)
	}
	if v, _ := f.store.Get("personal"); v != "sk-stored" {
		t.Fatalf("stored key did not move: personal = %q", v)
	}
	if v, _ := f.store.Get("work"); v != "" {
		t.Fatalf("the old stored key was left behind: %q", v)
	}
	if _, err := authopenai.LoadAuth(f.stateDir, "personal"); err != nil {
		t.Fatalf("OAuth record did not move: %v", err)
	}
	if _, err := authopenai.LoadAuth(f.stateDir, "work"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("the old OAuth record was left behind (err = %v)", err)
	}
	resp := f.ctl.List()
	e := entry(t, resp, "personal")
	if !e.IsDefault || !e.HasStoredFile || !e.HasStoredOAuth {
		t.Fatalf("personal = %+v, want default with the stored key and OAuth record", e)
	}
	for _, e := range resp.Instances {
		if e.Name == "work" {
			t.Fatal("List still shows the old name")
		}
	}
}

func TestInstances_EditRenameRefusesAnImplicitInstance(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "groq", NewName: "g2"})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("Edit = %v, want an InvalidParams wire error", err)
	}
	if l, exists, _ := registry.ReadConfigFile(f.tomlPath); exists {
		if _, authored := l.Providers["g2"]; authored {
			t.Fatal("a refused rename authored [providers.g2]")
		}
		if _, authored := l.Providers["groq"]; authored {
			t.Fatal("a refused rename authored a shadow for groq")
		}
	}
}

func TestInstances_EditRenameRefusesATakenName(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	for _, name := range []string{"work", "other"} {
		if err := f.ctl.Create(appwire.InstanceCreateParams{Name: name, Base: "openai"}); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
	}
	for _, taken := range []string{"other", "groq"} {
		err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: taken})
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
			t.Fatalf("rename to %q = %v, want a Conflict wire error", taken, err)
		}
	}
	authoredEntry(t, f.tomlPath, "work")
	authoredEntry(t, f.tomlPath, "other")
}

func TestInstances_EditRenameRejectsAnInvalidName(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "Bad/Name"})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("Edit = %v, want an InvalidParams wire error", err)
	}
	authoredEntry(t, f.tomlPath, "work")
}

func TestInstances_EditRenameAppliesTheOtherFieldsToo(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "work2", BaseURL: "https://gw.example.test/v1"}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if p := authoredEntry(t, f.tomlPath, "work2"); p.Transport.BaseURL != "https://gw.example.test/v1" {
		t.Fatalf("work2 base_url = %q", p.Transport.BaseURL)
	}
}

func TestInstances_EditSameNameIsNotARename(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "work"}); err != nil {
		t.Fatalf("Edit with NewName == Name must be a plain no-op edit: %v", err)
	}
	authoredEntry(t, f.tomlPath, "work")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestInstances_EditRename|TestInstances_EditSameName' 2>&1 | tail -12`
Expected: rename tests FAIL (the name is silently ignored today).

- [ ] **Step 3: Implement**

In `Edit`, right after the block that resolves `p, authored := l.Providers[name]` (including its `if !authored { ... }`), add:

```go
	newName := strings.TrimSpace(params.NewName)
	renaming := newName != "" && newName != name
	if renaming {
		if !authored {
			return appwire.InvalidParams(fmt.Sprintf("instance %q comes from the environment and cannot be renamed", name))
		}
		if !registry.ValidInstanceName(newName) {
			return appwire.InvalidParams(fmt.Sprintf("invalid instance name %q (lowercase, no slash)", params.NewName))
		}
		if _, taken := l.Providers[newName]; taken {
			return appwire.Conflict(fmt.Sprintf("instance %q already exists", newName))
		}
		if _, taken := c.reg.Get().Instance(newName); taken {
			return appwire.Conflict(fmt.Sprintf("instance %q already exists", newName))
		}
	}
```

Replace the single line `l.Providers[name] = p` (just before `writeLoadable`) with:

```go
	if renaming {
		// The map key is the instance name providers.toml is written under;
		// the default pointer follows so the file still loads.
		delete(l.Providers, name)
		p.ID = newName
		if l.Default == name {
			l.Default = newName
		}
		l.Providers[newName] = p
	} else {
		l.Providers[name] = p
	}
```

Replace the final `return nil` of `Edit` (after the reload-and-restore block) with:

```go
	if renaming {
		return c.moveCredentials(name, newName)
	}
	return nil
```

Add after `Edit`:

```go
// moveCredentials carries an instance's stored key and OAuth record to its
// new name after a rename. It runs once providers.toml is written and
// reloaded: the config is already renamed, so a failure here is reported as
// what was left behind rather than undone - the list stays consistent with
// the file, and a leftover stays reachable under the old name through
// evener/auth/apiKey/clear or the state directory.
func (c *hubInstancesController) moveCredentials(oldName, newName string) error {
	var problems []string
	if value, ok := c.auth.creds.Get(oldName); ok {
		if err := c.auth.setCredential(newName, value); err != nil {
			problems = append(problems, fmt.Sprintf("stored key not copied: %v", err))
		} else if err := c.auth.clearCredential(oldName); err != nil {
			problems = append(problems, fmt.Sprintf("stored key for %q left behind: %v", oldName, err))
		}
	}
	record, err := c.auth.loadAuth(c.auth.stateDir, oldName)
	switch {
	case errors.Is(err, authopenai.ErrAuthNotFound):
	case err != nil:
		problems = append(problems, fmt.Sprintf("OAuth record not read: %v", err))
	default:
		if err := c.auth.saveAuth(c.auth.stateDir, newName, record); err != nil {
			problems = append(problems, fmt.Sprintf("OAuth record not copied: %v", err))
		} else if _, err := c.auth.deleteAuth(c.auth.stateDir, oldName); err != nil {
			problems = append(problems, fmt.Sprintf("OAuth record for %q left behind: %v", oldName, err))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("renamed %q to %q, but: %s", oldName, newName, strings.Join(problems, "; "))
	}
	return nil
}
```

Update `Edit`'s doc comment: after the first paragraph add `A NewName re-keys the entry, follows the default pointer, and then moves the stored key and OAuth record (moveCredentials); it is refused for an implicit instance, an invalid name, or a name any instance already has.`

- [ ] **Step 4: Run the hub tests**

Run: `go test ./cmd/evener-hub 2>&1 | tail -4`
Expected: ok.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/app_instances.go cmd/evener-hub/app_instances_test.go
git commit -m "feat(hub): rename a provider instance, moving its stored credentials"
```

---

### Task 5: The form model and its diff rules

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/instanceEdit.ts`
- Test: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/instanceEdit.test.ts`

**Interfaces:**
- Consumes: generated `InstanceEditParams`, `InstanceEntry`, `ProviderDescriptor`; `SelectOption` from `widgets`.
- Produces: `PROTOCOL_OPTIONS`, `SURFACE_OPTIONS: SelectOption[]`; `InstanceDraft { name; baseUrl; protocol; surface; vars: Record<string,string>; apiKeyEnv; credentialHeader }`; `draftFor(instance, template): InstanceDraft`; `varRows(draft, template): { key: string; label: string }[]`; `instanceEditParams(initial, draft): InstanceEditParams | null`.

- [ ] **Step 1: Write the failing tests**

Create `instanceEdit.test.ts`:

```ts
// @vitest-environment node
import { describe, expect, test } from "vitest";
import type { InstanceEntry, ProviderDescriptor } from "../../../../protocol/types.gen";
import { draftFor, instanceEditParams, PROTOCOL_OPTIONS, SURFACE_OPTIONS, varRows } from "./instanceEdit";

function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
  return {
    protocol: "openai-chat",
    auth: "bearer",
    implicit: false,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    ...overrides,
  };
}

const VERTEX: ProviderDescriptor = {
  id: "google-vertex-anthropic",
  protocol: "anthropic",
  auth: "gcp-adc",
  implicit: true,
  vars: { GOOGLE_VERTEX_PROJECT: "GOOGLE_VERTEX_PROJECT", GOOGLE_VERTEX_LOCATION: "GOOGLE_VERTEX_LOCATION" },
};

describe("draftFor", () => {
  test("seeds every field from the entry, blanking what is unset", () => {
    const draft = draftFor(instance({ name: "a", providerId: "openai", baseUrl: "https://x", surface: "generic" }), undefined);
    expect(draft).toEqual({
      name: "a",
      baseUrl: "https://x",
      protocol: "openai-chat",
      surface: "generic",
      vars: {},
      apiKeyEnv: "",
      credentialHeader: "",
    });
  });

  test("seeds a row for every template var, prefilled from the entry's vars", () => {
    const draft = draftFor(
      instance({ name: "v", providerId: "google-vertex-anthropic", vars: { GOOGLE_VERTEX_PROJECT: "p1", EXTRA: "e" } }),
      VERTEX,
    );
    expect(draft.vars).toEqual({ GOOGLE_VERTEX_PROJECT: "p1", GOOGLE_VERTEX_LOCATION: "", EXTRA: "e" });
  });

  test("carries the authored apiKeyEnv and credentialHeader", () => {
    const draft = draftFor(
      instance({ name: "a", providerId: "openai", apiKeyEnv: "PORTKEY_KEY", credentialHeader: "Authorization=Bearer $PORTKEY_KEY" }),
      undefined,
    );
    expect(draft.apiKeyEnv).toBe("PORTKEY_KEY");
    expect(draft.credentialHeader).toBe("Authorization=Bearer $PORTKEY_KEY");
  });
});

describe("varRows", () => {
  test("template vars first, labelled by env var name and sorted, then authored extras labelled by key", () => {
    const draft = draftFor(instance({ name: "v", providerId: "google-vertex-anthropic", vars: { EXTRA: "e" } }), VERTEX);
    expect(varRows(draft, VERTEX)).toEqual([
      { key: "GOOGLE_VERTEX_LOCATION", label: "GOOGLE_VERTEX_LOCATION" },
      { key: "GOOGLE_VERTEX_PROJECT", label: "GOOGLE_VERTEX_PROJECT" },
      { key: "EXTRA", label: "EXTRA" },
    ]);
  });
});

describe("instanceEditParams", () => {
  const base = draftFor(
    instance({ name: "a", providerId: "openai", baseUrl: "https://x", surface: "openai", apiKeyEnv: "K", credentialHeader: "H=$K", vars: { R: "1" } }),
    undefined,
  );

  test("returns null when nothing changed", () => {
    expect(instanceEditParams(base, { ...base })).toBeNull();
    expect(instanceEditParams(base, { ...base, name: " a ", baseUrl: "https://x " })).toBeNull();
  });

  test("a changed name is a rename", () => {
    expect(instanceEditParams(base, { ...base, name: "b" })).toEqual({ name: "a", newName: "b" });
  });

  test("a changed base URL is sent; an emptied one is a clear", () => {
    expect(instanceEditParams(base, { ...base, baseUrl: "https://y" })).toEqual({ name: "a", baseUrl: "https://y" });
    expect(instanceEditParams(base, { ...base, baseUrl: "" })).toEqual({ name: "a", clearBaseUrl: true });
  });

  test("protocol and surface: a value is sent, inherit is a clear", () => {
    expect(instanceEditParams(base, { ...base, protocol: "openai-responses" })).toEqual({ name: "a", protocol: "openai-responses" });
    expect(instanceEditParams(base, { ...base, protocol: "" })).toEqual({ name: "a", clearProtocol: true });
    expect(instanceEditParams(base, { ...base, surface: "generic" })).toEqual({ name: "a", surface: "generic" });
    expect(instanceEditParams(base, { ...base, surface: "" })).toEqual({ name: "a", clearSurface: true });
  });

  test("only changed vars are sent, trimmed; an emptied var is sent empty so the hub deletes it", () => {
    expect(instanceEditParams(base, { ...base, vars: { R: "1", Z: " 2 " } })).toEqual({ name: "a", vars: { Z: "2" } });
    expect(instanceEditParams(base, { ...base, vars: { R: "" } })).toEqual({ name: "a", vars: { R: "" } });
  });

  test("api key env and credential header: a value is sent, an emptied one is a clear", () => {
    expect(instanceEditParams(base, { ...base, apiKeyEnv: "K2" })).toEqual({ name: "a", apiKeyEnv: "K2" });
    expect(instanceEditParams(base, { ...base, apiKeyEnv: "" })).toEqual({ name: "a", clearApiKeyEnv: true });
    expect(instanceEditParams(base, { ...base, credentialHeader: "X=$K" })).toEqual({ name: "a", credentialHeader: "X=$K" });
    expect(instanceEditParams(base, { ...base, credentialHeader: "" })).toEqual({ name: "a", clearCredentialHeader: true });
  });

  test("several changes ride one request", () => {
    expect(instanceEditParams(base, { ...base, name: "b", baseUrl: "", protocol: "google" })).toEqual({
      name: "a",
      newName: "b",
      clearBaseUrl: true,
      protocol: "google",
    });
  });
});

test("the option lists lead with inherit and carry exactly the registry's vocabularies", () => {
  expect(PROTOCOL_OPTIONS.map((o) => o.value)).toEqual(["", "openai-chat", "openai-responses", "anthropic", "google"]);
  expect(SURFACE_OPTIONS.map((o) => o.value)).toEqual(["", "openai", "anthropic", "google", "generic"]);
  expect(PROTOCOL_OPTIONS[0]?.label).toBe("inherit from base");
  expect(SURFACE_OPTIONS[0]?.label).toBe("inherit from base");
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run (from `cmd/evener-hub/frontend`): `npx vitest run src/panes/settings/sections/credentials/instanceEdit.test.ts 2>&1 | tail -6`
Expected: FAIL, cannot resolve `./instanceEdit`.

- [ ] **Step 3: Implement**

Create `instanceEdit.ts`:

```ts
// instanceEdit.ts: the provider sheet's form model - the draft the sheet
// edits, how it is seeded from an InstanceEntry, and how a dirty draft
// becomes the smallest evener/instance/edit request. Pure, so the diff rules
// (empty means unchanged on the wire, a clear flag for an emptied field,
// only the vars that changed) are pinned without rendering anything.
//
// protocol and baseUrl on the wire are the RESOLVED values - InstanceEntry
// carries no authored/inherited distinction for them - so the selects show
// what is in effect and "inherit from base" is the way back to the base's
// value (a clear the hub treats as a no-op when nothing was authored).
import type { InstanceEditParams, InstanceEntry, ProviderDescriptor } from "../../../../protocol/types.gen";
import type { SelectOption } from "../../../../widgets";

export const PROTOCOL_OPTIONS: SelectOption[] = [
  { value: "", label: "inherit from base" },
  { value: "openai-chat", label: "openai-chat" },
  { value: "openai-responses", label: "openai-responses" },
  { value: "anthropic", label: "anthropic" },
  { value: "google", label: "google" },
];

export const SURFACE_OPTIONS: SelectOption[] = [
  { value: "", label: "inherit from base" },
  { value: "openai", label: "openai" },
  { value: "anthropic", label: "anthropic" },
  { value: "google", label: "google" },
  { value: "generic", label: "generic" },
];

export interface InstanceDraft {
  name: string;
  baseUrl: string;
  protocol: string;
  surface: string;
  vars: Record<string, string>;
  apiKeyEnv: string;
  credentialHeader: string;
}

/** The form's initial values for an instance. Every template variable of the
 * base provider gets a row (blank unless authored), keyed by template name
 * exactly as the Add form keys its inputs. */
export function draftFor(instance: InstanceEntry, template: ProviderDescriptor | undefined): InstanceDraft {
  const vars: Record<string, string> = {};
  for (const key of Object.keys(template?.vars ?? {})) vars[key] = "";
  for (const [key, value] of Object.entries(instance.vars ?? {})) vars[key] = value;
  return {
    name: instance.name,
    baseUrl: instance.baseUrl ?? "",
    protocol: instance.protocol,
    surface: instance.surface ?? "",
    vars,
    apiKeyEnv: instance.apiKeyEnv ?? "",
    credentialHeader: instance.credentialHeader ?? "",
  };
}

/** The variable rows to render: the template's, labelled by the env var
 * name the docs tell users to set (the Add form's own labelling), then any
 * authored var the template does not name, labelled by its key. */
export function varRows(draft: InstanceDraft, template: ProviderDescriptor | undefined): { key: string; label: string }[] {
  const templateVars = template?.vars ?? {};
  const rows = Object.entries(templateVars)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, env]) => ({ key, label: env }));
  for (const key of Object.keys(draft.vars).sort()) {
    if (!(key in templateVars)) rows.push({ key, label: key });
  }
  return rows;
}

/** The request that carries exactly the fields whose trimmed value differs
 * from `initial`, or null when none does. */
export function instanceEditParams(initial: InstanceDraft, draft: InstanceDraft): InstanceEditParams | null {
  const params: InstanceEditParams = { name: initial.name };
  let changed = false;

  const name = draft.name.trim();
  if (name !== initial.name) {
    params.newName = name;
    changed = true;
  }
  const baseUrl = draft.baseUrl.trim();
  if (baseUrl !== initial.baseUrl) {
    if (baseUrl === "") params.clearBaseUrl = true;
    else params.baseUrl = baseUrl;
    changed = true;
  }
  if (draft.protocol !== initial.protocol) {
    if (draft.protocol === "") params.clearProtocol = true;
    else params.protocol = draft.protocol;
    changed = true;
  }
  if (draft.surface !== initial.surface) {
    if (draft.surface === "") params.clearSurface = true;
    else params.surface = draft.surface;
    changed = true;
  }
  const vars: Record<string, string> = {};
  for (const [key, value] of Object.entries(draft.vars)) {
    const trimmed = value.trim();
    if (trimmed !== (initial.vars[key] ?? "")) vars[key] = trimmed;
  }
  if (Object.keys(vars).length > 0) {
    params.vars = vars;
    changed = true;
  }
  const apiKeyEnv = draft.apiKeyEnv.trim();
  if (apiKeyEnv !== initial.apiKeyEnv) {
    if (apiKeyEnv === "") params.clearApiKeyEnv = true;
    else params.apiKeyEnv = apiKeyEnv;
    changed = true;
  }
  const credentialHeader = draft.credentialHeader.trim();
  if (credentialHeader !== initial.credentialHeader) {
    if (credentialHeader === "") params.clearCredentialHeader = true;
    else params.credentialHeader = credentialHeader;
    changed = true;
  }
  return changed ? params : null;
}
```

- [ ] **Step 4: Run the tests**

Run: `npx biome check --write src/panes/settings/sections/credentials/instanceEdit.ts src/panes/settings/sections/credentials/instanceEdit.test.ts && npx vitest run src/panes/settings/sections/credentials/instanceEdit.test.ts 2>&1 | tail -6`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add src/panes/settings/sections/credentials/instanceEdit.ts src/panes/settings/sections/credentials/instanceEdit.test.ts
git commit -m "feat(web): provider instance form model and edit-request diff"
```

---

### Task 6: InstanceSheet: the sheet is the editor

**Files:**
- Rename: `credentials/InstanceDetailSheet.tsx` → `credentials/InstanceSheet.tsx`; `credentials/InstanceDetailSheet.module.css` → `credentials/InstanceSheet.module.css`; `credentials/InstanceDetailSheet.test.tsx` → `credentials/InstanceSheet.test.tsx`; `credentials/InstanceDetailSheet.wire.test.tsx` → `credentials/InstanceSheet.wire.test.tsx` (all with `git mv`).
- Test: `credentials/InstanceSheet.test.tsx`, `credentials/InstanceSheet.wire.test.tsx`

**Interfaces:**
- Consumes: Task 5's module; `credentialsStore.edit(params)`; widgets `FormRow`, `Input`, `Select`, `Sheet` (`size="wide"`), `useToasts`; `errorText`.
- Produces: `InstanceSheet(props: InstanceSheetProps)` where `InstanceSheetProps` = the old `InstanceDetailSheetProps` minus `onEdit` plus `onRenamed: (newName: string) => void`.

- [ ] **Step 1: Rename the four files**

```bash
git mv src/panes/settings/sections/credentials/InstanceDetailSheet.tsx src/panes/settings/sections/credentials/InstanceSheet.tsx
git mv src/panes/settings/sections/credentials/InstanceDetailSheet.module.css src/panes/settings/sections/credentials/InstanceSheet.module.css
git mv src/panes/settings/sections/credentials/InstanceDetailSheet.test.tsx src/panes/settings/sections/credentials/InstanceSheet.test.tsx
git mv src/panes/settings/sections/credentials/InstanceDetailSheet.wire.test.tsx src/panes/settings/sections/credentials/InstanceSheet.wire.test.tsx
```

- [ ] **Step 2: Rewrite the sheet tests**

In `InstanceSheet.test.tsx`:
- Change the import to `import { InstanceSheet } from "./InstanceSheet";` and add `import { Toast } from "../../../../widgets";`, `import { getToasts, resetToastStoreForTests } from "../../../../widgets/toast/store";`, `import type { InstanceEntry, ProviderDescriptor } from "../../../../protocol/types.gen";`.
- In `noopHandlers()` remove `onEdit: vi.fn(),` and add `onRenamed: vi.fn(),`.
- In `renderSheet`, render `<><Toast /><InstanceSheet name={inst?.name ?? null} onClose={onClose} {...handlers} {...extra} /></>` and add a third parameter `providers: ProviderDescriptor[] = []` that is set into the store alongside the instance: `credentialsStore.setState({ instances: inst === null ? [] : [inst], availableProviders: providers });`.
- In `beforeEach` add `resetToastStoreForTests();`.
- Delete the whole `describe("the meta table", ...)` block (the form replaces it).
- In `describe("actions are conditionally rendered")`, replace the two Edit tests with:

```tsx
  test("Remove is offered for a non-implicit instance", () => {
    renderSheet(instance({ name: "a", providerId: "x" }));
    expect(screen.getByRole("button", { name: "Remove" })).toBeTruthy();
  });

  // Removing an implicit instance is refused server-side (spec §11.3), so
  // the sheet must not even offer the button; the form stays, since editing
  // an implicit instance writes a shadow rather than changing it.
  test("an implicit instance offers the form but no Remove", () => {
    renderSheet(instance({ name: "groq", providerId: "groq", implicit: true }));
    expect(screen.getByLabelText("Base URL")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
  });
```

- Append a new block:

```tsx
describe("the form", () => {
  const OPENAI: ProviderDescriptor = { id: "openai", protocol: "openai-chat", auth: "bearer", implicit: true };
  const VERTEX: ProviderDescriptor = {
    id: "google-vertex-anthropic",
    protocol: "anthropic",
    auth: "gcp-adc",
    implicit: true,
    vars: { GOOGLE_VERTEX_PROJECT: "GOOGLE_VERTEX_PROJECT", GOOGLE_VERTEX_LOCATION: "GOOGLE_VERTEX_LOCATION" },
  };
  const WORK = instance({
    name: "work",
    providerId: "openai",
    protocol: "openai-responses",
    surface: "generic",
    baseUrl: "https://gw.example.test/v1",
    apiKeyEnv: "PORTKEY_KEY",
    credentialHeader: "Authorization=Bearer $PORTKEY_KEY",
    hasStoredFile: true,
    activeSource: "store",
  });

  function field(label: string): HTMLInputElement {
    return screen.getByLabelText(label) as HTMLInputElement;
  }
  function select(label: string): HTMLSelectElement {
    return screen.getByLabelText(label) as HTMLSelectElement;
  }
  function saveButton(): HTMLButtonElement {
    return screen.getByRole("button", { name: "Save" }) as HTMLButtonElement;
  }

  test("prefills every field from the instance and shows the base provider as a fact", () => {
    renderSheet(WORK, {}, [OPENAI]);
    expect(field("Name").value).toBe("work");
    expect(field("Base URL").value).toBe("https://gw.example.test/v1");
    expect(select("Protocol").value).toBe("openai-responses");
    expect(select("Surface").value).toBe("generic");
    expect(field("API key environment variable").value).toBe("PORTKEY_KEY");
    expect(field("Credential header").value).toBe("Authorization=Bearer $PORTKEY_KEY");
    // "openai" is also a Surface option's text, so read the meta row's value cell.
    expect(screen.getByText("Base provider").nextElementSibling?.textContent).toBe("openai");
  });

  test("renders one input per base-provider variable, labelled by env var name, plus authored extras", () => {
    renderSheet(
      instance({ name: "v", providerId: "google-vertex-anthropic", vars: { GOOGLE_VERTEX_PROJECT: "p1", EXTRA: "e" } }),
      {},
      [VERTEX],
    );
    expect(field("GOOGLE_VERTEX_PROJECT").value).toBe("p1");
    expect(field("GOOGLE_VERTEX_LOCATION").value).toBe("");
    expect(field("EXTRA").value).toBe("e");
  });

  test("Save is disabled until a field changes, and while writesRefused", async () => {
    renderSheet(WORK, {}, [OPENAI]);
    expect(saveButton().disabled).toBe(true);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    expect(saveButton().disabled).toBe(false);

    cleanup();
    renderSheet(WORK, { writesRefused: true }, [OPENAI]);
    await user.type(field("Base URL"), "/x");
    expect(saveButton().disabled).toBe(true);
  });

  test("an implicit instance's Name is disabled with the environment note", () => {
    renderSheet(instance({ name: "groq", providerId: "groq", implicit: true }));
    expect(field("Name").disabled).toBe(true);
    expect(screen.getByText(/comes from the environment/)).toBeTruthy();
  });

  test("changing the name shows the rename note", async () => {
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    expect(screen.getByText(/reference "work" keep the old name/)).toBeTruthy();
  });

  test("Save sends only the changed fields", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", (params) => {
      expect(params).toEqual({ name: "work", baseUrl: "https://gw.example.test/v1/x" });
      return { instances: [{ ...WORK, baseUrl: "https://gw.example.test/v1/x" }], availableProviders: [OPENAI] };
    });
    connectionStore.getState().connect(fake);
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    await waitFor(() => expect(getToasts().some((t) => t.text === "Saved work")).toBe(true));
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(1);
    // Reseeded from the refreshed instance: clean again.
    expect(saveButton().disabled).toBe(true);
    expect(field("Base URL").value).toBe("https://gw.example.test/v1/x");
  });

  test("emptying Base URL shows the reset note and sends clearBaseUrl", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", (params) => {
      expect(params).toEqual({ name: "work", clearBaseUrl: true });
      return { instances: [WORK], availableProviders: [OPENAI] };
    });
    connectionStore.getState().connect(fake);
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.clear(field("Base URL"));
    expect(screen.getByText("Resets the endpoint to the provider's default.")).toBeTruthy();
    await user.click(saveButton());
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/edit")).toBe(true));
  });

  test("choosing inherit from base sends clearProtocol", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", (params) => {
      expect(params).toEqual({ name: "work", clearProtocol: true });
      return { instances: [WORK], availableProviders: [OPENAI] };
    });
    connectionStore.getState().connect(fake);
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.selectOptions(select("Protocol"), "");
    await user.click(saveButton());
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/edit")).toBe(true));
  });

  test("emptying a var sends it empty so the hub deletes it", async () => {
    const V = instance({ name: "v", providerId: "google-vertex-anthropic", vars: { GOOGLE_VERTEX_PROJECT: "p1" } });
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", (params) => {
      expect(params).toEqual({ name: "v", vars: { GOOGLE_VERTEX_PROJECT: "" } });
      return { instances: [V], availableProviders: [VERTEX] };
    });
    connectionStore.getState().connect(fake);
    renderSheet(V, {}, [VERTEX]);
    const user = userEvent.setup();
    await user.clear(field("GOOGLE_VERTEX_PROJECT"));
    await user.click(saveButton());
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/edit")).toBe(true));
  });

  test("a credential header without $ is refused inline, with no RPC", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => {
      throw new Error("must not be called");
    });
    connectionStore.getState().connect(fake);
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.clear(field("Credential header"));
    await user.type(field("Credential header"), "Authorization=Bearer sk-literal");
    await user.click(saveButton());
    expect(screen.getByRole("alert").textContent).toContain("$VARIABLE");
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(0);
  });

  test("a failed save shows the error inline and toasts Save failed", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => {
      throw new Error("providers.toml: write: read-only");
    });
    connectionStore.getState().connect(fake);
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("read-only"));
    expect(getToasts().some((t) => t.text.startsWith("Save failed"))).toBe(true);
    expect(field("Base URL").value).toBe("https://gw.example.test/v1/x");
  });

  test("a rename toasts the new name, calls onRenamed, and does not close the sheet", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", (params) => {
      expect(params).toEqual({ name: "work", newName: "work2" });
      return { instances: [{ ...WORK, name: "work2" }], availableProviders: [OPENAI] };
    });
    connectionStore.getState().connect(fake);
    const { handlers, onClose } = renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.click(saveButton());
    await waitFor(() => expect(handlers.onRenamed).toHaveBeenCalledWith("work2"));
    expect(getToasts().some((t) => t.text === "Saved work2")).toBe(true);
    expect(onClose).not.toHaveBeenCalled();
  });
});
```

In `InstanceSheet.wire.test.tsx`, change the import to `InstanceSheet`, the rendered component to `<InstanceSheet ...>`, remove the `onEdit={vi.fn()}` prop and add `onRenamed={vi.fn()}`. Update the file's top comment to name `InstanceSheet.test.tsx`.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `npx vitest run src/panes/settings/sections/credentials/InstanceSheet 2>&1 | tail -10`
Expected: FAIL (`InstanceSheet` is not exported; the old file still exports `InstanceDetailSheet`).

- [ ] **Step 4: Rewrite the stylesheet**

In `InstanceSheet.module.css`, change the header comment's first line to `/* InstanceSheet body layout (the sheet is the editor, spec 2026-09-07 §2).` and add after `.headingRow`:

```css
/* The authored fields, as a form: FormRow's own label/control/help rhythm,
 * one column, sitting above the credential actions the inspector already
 * had. The base provider row reuses the meta-table vocabulary since it is
 * a fact, not a field. */
.form {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  margin-bottom: var(--space-4);
}

.formError {
  margin: 0;
  color: var(--ink-hi);
  font-size: var(--font-size-caption);
}
```

- [ ] **Step 5: Rewrite the component**

Replace the whole of `InstanceSheet.tsx` with:

```tsx
// InstanceSheet: the provider instance's editor (spec 2026-09-07 §2). Opens
// from an InstanceRow tap and IS the edit surface: the authored fields are
// form inputs prefilled from the instance, Save in the sheet footer lights
// up when any differs, and renaming is editing the Name field. Below the
// form sit the layered credential display and the actions the inspector
// this replaced already had (test, set/replace key or credential JSON,
// sign in/refresh OAuth, make default) and the danger zone; the secret
// entry and OAuth flows stay dialogs because they carry write-only values
// or several steps. A wide right Sheet on desktop, a bottom Sheet on mobile
// (useIsMobile, the shell's own source).
//
// The instance is read from the store by name so cross-client changes land
// live. The draft is seeded when a different instance opens and again
// after this sheet's own save lands, never on an unrelated refresh, so
// in-progress edits survive another client's change. The sheet closes
// itself when its instance disappears - except across its own rename,
// where the section re-selects the new name (onRenamed) and the vanish is
// the rename landing, not a removal.
//
// Owns the one mutation it edits (evener/instance/edit); the section still
// owns what every other action DOES (opening an editor, a confirm, or
// calling the store), the same division of labor as before.
import { useEffect, useId, useRef, useState } from "react";
import { errorText } from "../../../../protocol/errors";
import type { AuthTestResponse, InstanceEntry } from "../../../../protocol/types.gen";
import { useIsMobile } from "../../../../shell/useIsMobile";
import { credentialsStore, useCredentialsStore } from "../../../../stores/credentials";
import { Button, Chip, FormRow, Input, Select, Sheet, StatusDot, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import {
  credentialLayers,
  keylessByDesign,
  safeCredentialTestMessage,
  safeCredentialTestResult,
  unconfiguredLabel,
} from "./credentialLabels";
import { draftFor, type InstanceDraft, instanceEditParams, PROTOCOL_OPTIONS, SURFACE_OPTIONS, varRows } from "./instanceEdit";
import styles from "./InstanceSheet.module.css";

const CLASS = {
  headingRow: requireClass(styles.headingRow, "InstanceSheet.module.css", "headingRow"),
  form: requireClass(styles.form, "InstanceSheet.module.css", "form"),
  formError: requireClass(styles.formError, "InstanceSheet.module.css", "formError"),
  layers: requireClass(styles.layers, "InstanceSheet.module.css", "layers"),
  layer: requireClass(styles.layer, "InstanceSheet.module.css", "layer"),
  unconfigured: requireClass(styles.unconfigured, "InstanceSheet.module.css", "unconfigured"),
  metaRow: requireClass(styles.metaRow, "InstanceSheet.module.css", "metaRow"),
  metaLabel: requireClass(styles.metaLabel, "InstanceSheet.module.css", "metaLabel"),
  metaValue: requireClass(styles.metaValue, "InstanceSheet.module.css", "metaValue"),
  actionRows: requireClass(styles.actionRows, "InstanceSheet.module.css", "actionRows"),
  fullRow: requireClass(styles.fullRow, "InstanceSheet.module.css", "fullRow"),
  divider: requireClass(styles.divider, "InstanceSheet.module.css", "divider"),
  testResult: requireClass(styles.testResult, "InstanceSheet.module.css", "testResult"),
};

const LITERAL_HEADER_ERROR = "Credential header must reference a $VARIABLE, never a literal secret.";

export interface InstanceSheetProps {
  name: string | null;
  onClose: () => void;
  /** After a successful rename, with the new name: the section re-selects
   * it so the sheet stays open on the same instance. */
  onRenamed: (newName: string) => void;
  onSetApiKey: () => void;
  onSetCredentialJson: () => void;
  onOAuthStart: () => void;
  onClear: () => void;
  onClearStoredKey: () => void;
  onRemove: () => void;
  onSetDefault: () => void;
  onTestCredentials: () => void;
  testCredentialsPending?: boolean;
  testCredentialsResult?: AuthTestResponse;
  /** Disables Save/Remove/make default while providers.toml cannot be
   * written (InstanceListResponse.writesRefused, spec §11.3) - Set key/Sign
   * in/Clear/Clear stored key/Test credentials are unaffected: they write
   * the credentials store or an OAuth record, never providers.toml. */
  writesRefused?: boolean;
}

export function InstanceSheet({
  name,
  onClose,
  onRenamed,
  onSetApiKey,
  onSetCredentialJson,
  onOAuthStart,
  onClear,
  onClearStoredKey,
  onRemove,
  onSetDefault,
  onTestCredentials,
  testCredentialsPending = false,
  testCredentialsResult,
  writesRefused = false,
}: InstanceSheetProps) {
  const instances = useCredentialsStore((s) => s.instances);
  const availableProviders = useCredentialsStore((s) => s.availableProviders);
  const isMobile = useIsMobile();
  const toast = useToasts();
  const ids = useId();

  const instance = name === null ? undefined : instances.find((i) => i.name === name);
  const template = instance === undefined ? undefined : availableProviders.find((p) => p.id === instance.providerId);

  const [initial, setInitial] = useState<InstanceDraft | null>(null);
  const [draft, setDraft] = useState<InstanceDraft | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // Set for the span of a rename request: the old name vanishes from the
  // store when the response lands, and that vanish must not close the sheet.
  const pendingRename = useRef<string | null>(null);

  function seed(inst: InstanceEntry): void {
    const seeded = draftFor(
      inst,
      credentialsStore.getState().availableProviders.find((p) => p.id === inst.providerId),
    );
    setInitial(seeded);
    setDraft(seeded);
    setFormError(null);
  }

  // biome-ignore lint/correctness/useExhaustiveDependencies: reseed only when a different instance opens; a refresh of the same instance must not clobber in-progress edits
  useEffect(() => {
    if (instance === undefined) {
      setInitial(null);
      setDraft(null);
      setFormError(null);
      return;
    }
    seed(instance);
  }, [instance?.name]);

  // An inspector is only as alive as its subject: the instance can vanish
  // under an open sheet (its own Remove completing, or another client's
  // change), and an editor for a thing that no longer exists closes itself
  // rather than offering actions on a ghost. Its own rename is the one
  // vanish that is not a removal.
  useEffect(() => {
    if (name !== null && instance === undefined && pendingRename.current === null) onClose();
  }, [name, instance, onClose]);
  // The section moved the selection to the new name: the guard is spent.
  useEffect(() => {
    pendingRename.current = null;
  }, [name]);

  const open = name !== null && instance !== undefined;

  const params = initial !== null && draft !== null ? instanceEditParams(initial, draft) : null;
  const dirty = params !== null;

  function update(patch: Partial<InstanceDraft>): void {
    setDraft((current) => (current === null ? current : { ...current, ...patch }));
  }
  function updateVar(key: string, value: string): void {
    setDraft((current) => (current === null ? current : { ...current, vars: { ...current.vars, [key]: value } }));
  }

  async function handleSave(): Promise<void> {
    if (instance === undefined || params === null) return;
    if (params.credentialHeader !== undefined && !params.credentialHeader.includes("$")) {
      setFormError(LITERAL_HEADER_ERROR);
      return;
    }
    setFormError(null);
    setBusy(true);
    if (params.newName !== undefined) pendingRename.current = params.newName;
    try {
      await credentialsStore.getState().edit(params);
      toast.push("success", `Saved ${params.newName ?? instance.name}`);
      if (params.newName !== undefined) {
        onRenamed(params.newName);
      } else {
        const refreshed = credentialsStore.getState().instances.find((i) => i.name === instance.name);
        if (refreshed !== undefined) seed(refreshed);
      }
    } catch (err) {
      pendingRename.current = null;
      const message = errorText(err);
      setFormError(message);
      toast.push("error", `Save failed: ${message}`);
    } finally {
      setBusy(false);
    }
  }

  const supportsApiKey = instance !== undefined && (instance.authModes ?? []).includes("apiKey");
  const supportsCredentialJson = instance !== undefined && (instance.authModes ?? []).includes("credentialJson");
  const supportsOAuth = instance !== undefined && (instance.authModes ?? []).includes("oauth");
  const showClear = instance !== undefined && (instance.activeSource === "store" || instance.activeSource === "oauth");
  // showClearStoredKey: a stray stored key sits shadowed behind whatever IS
  // active (the same condition credentialLayers uses to render that second,
  // non-effective layer above) - true for an oauth/adc login with a leftover
  // credentials.toml entry, and just as much for a signed-out Codex row a
  // previous Clear left stranded (Clear's Codex branch removes the OAuth
  // record, not the file, when one is active; issue #713). This action
  // always targets the store layer only, so it is safe to offer regardless
  // of what is effective.
  const showClearStoredKey = instance?.hasStoredFile && instance.activeSource !== "store";
  // The danger zone is Clear + Clear stored key + Remove under a divider; an
  // implicit instance with nothing stored offers none of them, and a divider
  // over nothing reads as a rendering bug.
  const showDangerZone = instance !== undefined && (showClear || showClearStoredKey || !instance.implicit);
  const layers = instance === undefined ? [] : credentialLayers(instance);
  const unconfigured = instance === undefined ? null : unconfiguredLabel(instance);
  const safeTestResult = testCredentialsResult
    ? safeCredentialTestResult(name ?? "", testCredentialsResult)
    : undefined;
  const clearingBaseUrl =
    instance !== undefined && draft !== null && Boolean(instance.baseUrl) && draft.baseUrl.trim() === "";
  const renaming = initial !== null && draft !== null && draft.name.trim() !== initial.name;

  return (
    <Sheet
      open={open}
      onClose={onClose}
      title={instance?.name ?? ""}
      side={isMobile ? "bottom" : "right"}
      size="wide"
      footer={
        instance !== undefined && (
          <Button onClick={() => void handleSave()} disabled={!dirty || busy || writesRefused}>
            Save
          </Button>
        )
      }
    >
      {instance !== undefined && (
        <>
          <div className={CLASS.headingRow}>
            <StatusDot state={layers.length > 0 || keylessByDesign(instance) ? "idle" : "ended"} />
            {instance.isDefault && <Chip>★ default</Chip>}
            {instance.implicit && <Chip>from environment</Chip>}
          </div>
          {draft !== null && (
            <form
              className={CLASS.form}
              aria-label={`Edit ${instance.name}`}
              onSubmit={(event) => {
                event.preventDefault();
                void handleSave();
              }}
            >
              <FormRow
                label="Name"
                htmlFor={`${ids}-name`}
                help={
                  instance.implicit
                    ? "This instance comes from the environment and cannot be renamed."
                    : renaming
                      ? `Launch config and past sessions that reference "${instance.name}" keep the old name.`
                      : undefined
                }
              >
                <Input
                  id={`${ids}-name`}
                  value={draft.name}
                  onChange={(event) => update({ name: event.target.value })}
                  disabled={busy || instance.implicit}
                />
              </FormRow>
              <div className={CLASS.metaRow}>
                <span className={CLASS.metaLabel}>Base provider</span>
                <span className={CLASS.metaValue}>{instance.providerId}</span>
              </div>
              <FormRow
                label="Base URL"
                htmlFor={`${ids}-baseurl`}
                help={clearingBaseUrl ? "Resets the endpoint to the provider's default." : undefined}
              >
                <Input
                  id={`${ids}-baseurl`}
                  value={draft.baseUrl}
                  onChange={(event) => update({ baseUrl: event.target.value })}
                  placeholder="https://…"
                  disabled={busy}
                />
              </FormRow>
              <FormRow label="Protocol" htmlFor={`${ids}-protocol`}>
                <Select
                  id={`${ids}-protocol`}
                  value={draft.protocol}
                  onChange={(event) => update({ protocol: event.target.value })}
                  options={PROTOCOL_OPTIONS}
                  disabled={busy}
                />
              </FormRow>
              <FormRow label="Surface" htmlFor={`${ids}-surface`}>
                <Select
                  id={`${ids}-surface`}
                  value={draft.surface}
                  onChange={(event) => update({ surface: event.target.value })}
                  options={SURFACE_OPTIONS}
                  disabled={busy}
                />
              </FormRow>
              {varRows(draft, template).map(({ key, label }) => (
                <FormRow key={key} label={label} htmlFor={`${ids}-var-${key}`}>
                  <Input
                    id={`${ids}-var-${key}`}
                    value={draft.vars[key] ?? ""}
                    onChange={(event) => updateVar(key, event.target.value)}
                    disabled={busy}
                  />
                </FormRow>
              ))}
              <FormRow label="API key environment variable" htmlFor={`${ids}-apikeyenv`}>
                <Input
                  id={`${ids}-apikeyenv`}
                  value={draft.apiKeyEnv}
                  onChange={(event) => update({ apiKeyEnv: event.target.value })}
                  placeholder="e.g. PORTKEY_KEY"
                  disabled={busy}
                />
              </FormRow>
              <FormRow
                label="Credential header"
                htmlFor={`${ids}-credentialheader`}
                help="NAME=VALUE; the value must reference a $VARIABLE, never a literal secret."
              >
                <Input
                  id={`${ids}-credentialheader`}
                  value={draft.credentialHeader}
                  onChange={(event) => update({ credentialHeader: event.target.value })}
                  placeholder="Authorization=Bearer $VAR"
                  disabled={busy}
                />
              </FormRow>
              {formError !== null && (
                <p className={CLASS.formError} role="alert">
                  {formError}
                </p>
              )}
            </form>
          )}
          {unconfigured !== null ? (
            <p className={CLASS.unconfigured}>{unconfigured}</p>
          ) : (
            <div className={CLASS.layers}>
              {layers.map((layer) => (
                <div key={layer.source} className={CLASS.layer}>
                  <span>↳ {layer.label}</span>
                  <Chip tone={layer.effective ? "alive" : "neutral"}>{layer.effective ? "effective" : "shadowed"}</Chip>
                </div>
              ))}
            </div>
          )}
          <div className={CLASS.actionRows}>
            <div className={CLASS.fullRow}>
              <Button variant="quiet" onClick={onTestCredentials} disabled={testCredentialsPending}>
                {testCredentialsPending ? "Testing credentials…" : "Test credentials"}
              </Button>
            </div>
            {safeTestResult && (
              <p className={CLASS.testResult} role="status">
                {safeTestResult.status}: {safeCredentialTestMessage(safeTestResult.status)}
              </p>
            )}
            {supportsApiKey && (
              <div className={CLASS.fullRow}>
                <Button variant="quiet" onClick={onSetApiKey}>
                  {instance.hasStoredFile ? "Replace key" : "Set key"}
                </Button>
              </div>
            )}
            {supportsCredentialJson && (
              <div className={CLASS.fullRow}>
                <Button variant="quiet" onClick={onSetCredentialJson}>
                  {instance.hasStoredFile ? "Replace credential JSON" : "Set credential JSON"}
                </Button>
              </div>
            )}
            {supportsOAuth && (
              <div className={CLASS.fullRow}>
                <Button variant="quiet" onClick={onOAuthStart}>
                  {instance.hasStoredOAuth ? "Refresh OAuth" : "Sign in…"}
                </Button>
              </div>
            )}
            {!instance.isDefault && (
              <div className={CLASS.fullRow}>
                <Button variant="quiet" onClick={onSetDefault} disabled={writesRefused}>
                  ★ make default
                </Button>
              </div>
            )}
          </div>
          {showDangerZone && (
            <>
              <hr className={CLASS.divider} />
              <div className={CLASS.actionRows}>
                {showClearStoredKey && (
                  <div className={CLASS.fullRow}>
                    <Button variant="dangerQuiet" onClick={onClearStoredKey}>
                      {supportsCredentialJson ? "Clear stored credential JSON" : "Clear stored key"}
                    </Button>
                  </div>
                )}
                {showClear && (
                  <div className={CLASS.fullRow}>
                    <Button variant="dangerQuiet" onClick={onClear}>
                      Clear
                    </Button>
                  </div>
                )}
                {!instance.implicit && (
                  <div className={CLASS.fullRow}>
                    <Button variant="danger" onClick={onRemove} disabled={writesRefused}>
                      Remove
                    </Button>
                  </div>
                )}
              </div>
            </>
          )}
        </>
      )}
    </Sheet>
  );
}
```

Delete the now-unused `.metaList` and `.metaMono` rules from the stylesheet only if `requireClass` no longer references them (it does not in the code above); keep `.metaRow`, `.metaLabel`, `.metaValue`.

- [ ] **Step 6: Run the sheet tests**

Run: `npx biome check --write src/panes/settings/sections/credentials/InstanceSheet.tsx src/panes/settings/sections/credentials/InstanceSheet.module.css src/panes/settings/sections/credentials/InstanceSheet.test.tsx src/panes/settings/sections/credentials/InstanceSheet.wire.test.tsx && npx vitest run src/panes/settings/sections/credentials/InstanceSheet 2>&1 | tail -12`
Expected: every test in both files passes. `CredentialsSection` tests will fail until Task 7 rewires the section; that is expected here.

- [ ] **Step 7: Commit**

```bash
git add src/panes/settings/sections/credentials/InstanceSheet.tsx src/panes/settings/sections/credentials/InstanceSheet.module.css src/panes/settings/sections/credentials/InstanceSheet.test.tsx src/panes/settings/sections/credentials/InstanceSheet.wire.test.tsx
git commit -m "feat(web): the provider instance sheet is the editor"
```

---

### Task 7: Wire the section, delete the edit dialog

**Files:**
- Modify: `credentials/CredentialsSection.tsx`
- Modify: `credentials/instanceDialogs.tsx` (delete `EditInstanceDialogProps` and `EditInstanceDialog`)
- Test: `credentials/CredentialsSection.test.tsx`, `credentials/CredentialsSection.edge.test.tsx`, `credentials/instanceDialogs.test.tsx`

- [ ] **Step 1: Update the section tests first**

In `CredentialsSection.test.tsx`:
- In the `describe("single-open-editor invariant")` test, rename it to `"opening the Add form, then Replace key from a row's sheet, replaces it (only one editor open at a time)"` and change the clicked button from `"Edit"` to `"Replace key"`, and the final expectation from `screen.getByRole("dialog", { name: "Edit work" })` to `screen.getByRole("dialog", { name: "Set API key for work" })`.
- In the `writesRefused` test, rename it to `"writesRefused disables Add and each sheet's Save/Remove/make default, but not Test credentials/Set key/Clear"` and change the `"Edit"` assertion to `{ name: "Save" }`.
- At the other `"Edit"` assertion (around line 234, inside the diagnostics tests), change it to `{ name: "Remove" }`.
- Append to the file:

```tsx
describe("rename from the sheet", () => {
  test("re-selects the instance under its new name so the sheet stays open", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => LIST);
    fake.on("evener/instance/edit", (params) => ({
      instances: [{ ...WORK, name: params.newName ?? WORK.name }, PERSONAL],
      availableProviders: [],
    }));
    render(
      <>
        <Toast />
        <CredentialsSection sectionId="credentials" />
      </>,
    );
    await screen.findByText("work");
    const user = userEvent.setup();
    const inspector = await openSheet(user, "work");
    await user.type(within(inspector).getByLabelText("Name"), "2");
    await user.click(within(inspector).getByRole("button", { name: "Save" }));
    await screen.findByRole("dialog", { name: "work2" });
    expect(screen.queryByRole("dialog", { name: "work" })).toBeNull();
    expect(screen.getByRole("button", { name: /work2/ })).toBeTruthy();
  });
});
```

In `CredentialsSection.edge.test.tsx`, delete the test `"an edit dialog closes when a refreshed list removes its instance"` and its leading comment.

In `instanceDialogs.test.tsx`, delete the whole `describe("EditInstanceDialog", ...)` block and remove `EditInstanceDialog` from the import line.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `npx vitest run src/panes/settings/sections/credentials 2>&1 | tail -12`
Expected: `CredentialsSection*` fail to compile (`InstanceDetailSheet` no longer exists); `instanceDialogs.test.tsx` passes.

- [ ] **Step 3: Rewire the section**

In `CredentialsSection.tsx`:
- Replace the import `import { InstanceDetailSheet } from "./InstanceDetailSheet";` with `import { InstanceSheet } from "./InstanceSheet";`.
- Change `import { AddInstanceDialog, ApiKeyDialog, CredentialJsonDialog, EditInstanceDialog } from "./instanceDialogs";` to drop `EditInstanceDialog`.
- In `type OpenEditor`, delete the `| { kind: "edit"; name: string }` member.
- Delete the `{openEditor?.kind === "edit" && (() => { ... })()}` render block.
- Replace `<InstanceDetailSheet` with `<InstanceSheet`, delete the `onEdit={...}` prop, and add `onRenamed={setSelectedInstance}`.
- In the header comment, replace `every per-instance action (test, set key, sign in, edit, make default, clear, clear stored key, remove) lives in that sheet` with `the sheet is the instance's editor (name, base URL, protocol, surface, vars, api-key env, credential header; spec 2026-09-07 §2) and every other per-instance action (test, set key, sign in, make default, clear, clear stored key, remove) lives in it too`, and `The editors (add/apiKey/edit/OAuth flows) stay dialogs` with `The secret-entry and multi-step flows (add/apiKey/credential JSON/OAuth) stay dialogs`.

In `instanceDialogs.tsx`, delete `EditInstanceDialogProps` and `EditInstanceDialog` (the interface, the doc comment, and the function), and drop `InstanceEntry` from the type import only if nothing else in the file uses it (ApiKeyDialogProps does, so keep it). Update the file's top comment: `the 3 instance-CRUD editors ... Add, Edit, and Set/Replace API key` → `the instance-CRUD editors that stay dialogs (spec 2026-09-07 §2): Add, plus Set/Replace API key and credential JSON. Editing an existing instance lives in InstanceSheet.`

- [ ] **Step 4: Run the credentials tests and the whole settings suite**

Run: `npx biome check --write src/panes/settings/sections/credentials/CredentialsSection.tsx src/panes/settings/sections/credentials/instanceDialogs.tsx src/panes/settings/sections/credentials/CredentialsSection.test.tsx src/panes/settings/sections/credentials/CredentialsSection.edge.test.tsx src/panes/settings/sections/credentials/instanceDialogs.test.tsx && npx vitest run src/panes/settings 2>&1 | tail -10`
Expected: all pass. Then `grep -rn "InstanceDetailSheet\|EditInstanceDialog" src` must print nothing.

- [ ] **Step 5: Commit**

```bash
git add src/panes/settings/sections/credentials/CredentialsSection.tsx src/panes/settings/sections/credentials/instanceDialogs.tsx src/panes/settings/sections/credentials/CredentialsSection.test.tsx src/panes/settings/sections/credentials/CredentialsSection.edge.test.tsx src/panes/settings/sections/credentials/instanceDialogs.test.tsx
git commit -m "refactor(web): route instance editing through the sheet; drop the edit dialog"
```

---

### Task 8: Protocol and surface on the Add form

**Files:**
- Modify: `credentials/instanceDialogs.tsx` (`AddInstanceDialog`)
- Test: `credentials/instanceDialogs.test.tsx`

- [ ] **Step 1: Write the failing test**

Inside `describe("AddInstanceDialog", ...)` in `instanceDialogs.test.tsx`, append:

```tsx
  test("Protocol and Surface default to inherit and are sent only when chosen", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/create", (params) => {
      expect(params).toEqual({ name: "work", base: "anthropic", baseUrl: "", protocol: "openai-responses", surface: "generic" });
      return { instances: [], availableProviders: [] };
    });
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={onSuccess} />
        <Toast />
      </>,
    );
    expect((screen.getByLabelText("Protocol") as HTMLSelectElement).value).toBe("");
    expect((screen.getByLabelText("Surface") as HTMLSelectElement).value).toBe("");
    await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
    await user.type(screen.getByLabelText("Name"), "work");
    await user.selectOptions(screen.getByLabelText("Protocol"), "openai-responses");
    await user.selectOptions(screen.getByLabelText("Surface"), "generic");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await vi.waitFor(() => expect(onSuccess).toHaveBeenCalled());
  });
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `npx vitest run src/panes/settings/sections/credentials/instanceDialogs.test.tsx -t "Protocol and Surface" 2>&1 | tail -8`
Expected: FAIL, no element labelled "Protocol".

- [ ] **Step 3: Implement**

In `AddInstanceDialog`:
- Add `import { PROTOCOL_OPTIONS, SURFACE_OPTIONS } from "./instanceEdit";`.
- Add state after `baseUrl`: `const [protocol, setProtocol] = useState("");` and `const [surface, setSurface] = useState("");`.
- In the `create` call add `protocol: protocol || undefined,` and `surface: surface || undefined,` after `baseUrl`.
- After the Base URL `FormRow`, add:

```tsx
        <FormRow
          label="Protocol"
          htmlFor="add-instance-protocol"
          help="Leave on inherit unless the endpoint speaks a different wire protocol than its base."
        >
          <Select
            id="add-instance-protocol"
            value={protocol}
            onChange={(event) => setProtocol(event.target.value)}
            options={PROTOCOL_OPTIONS}
            disabled={busy}
          />
        </FormRow>
        <FormRow label="Surface" htmlFor="add-instance-surface">
          <Select
            id="add-instance-surface"
            value={surface}
            onChange={(event) => setSurface(event.target.value)}
            options={SURFACE_OPTIONS}
            disabled={busy}
          />
        </FormRow>
```

Update the file's top comment sentence `the openai-only API-style radio is gone (Protocol is no longer openai-specific data the form special-cases)` to `Protocol and Surface are plain selects over the registry's vocabularies (instanceEdit.ts), defaulting to inherit`.

- [ ] **Step 4: Run the dialog tests**

Run: `npx biome check --write src/panes/settings/sections/credentials/instanceDialogs.tsx src/panes/settings/sections/credentials/instanceDialogs.test.tsx && npx vitest run src/panes/settings/sections/credentials/instanceDialogs.test.tsx 2>&1 | tail -6`
Expected: all pass (the existing "submit calls instanceCreate" test still matches because `toEqual` ignores `undefined` properties).

- [ ] **Step 5: Commit**

```bash
git add src/panes/settings/sections/credentials/instanceDialogs.tsx src/panes/settings/sections/credentials/instanceDialogs.test.tsx
git commit -m "feat(web): protocol and surface selects on the add-instance form"
```

---

### Task 9: The design-system rule and the decisions entry

**Files:**
- Modify: `docs/web-ui/design-system.md` (§10, the paragraph beginning `**Rows are single tappable targets; actions live in a detail sheet.**`)
- Modify: `docs/web-ui/decisions.md` (append)

- [ ] **Step 1: Rewrite the §10 paragraph**

Replace that paragraph (through `...closing the pane out from under an open overlay.`) with:

```markdown
**Rows are single tappable targets; the detail sheet is the item's editor.** A collection row
carries identity and status only — `StatusDot`, name, state chips, one mono meta line
(`@ marketplace · v1.2.0`) — and a trailing chevron; it is one full-width `<button>`, so the
whole row is the target on desktop and touch alike. A row NEVER grows a trailing cluster of
small action buttons (the pre-redesign installed row had four): everything about the item lives
in its **detail sheet**, a `Sheet` with `side="right"` on desktop and `side="bottom"` at the
mobile breakpoint (chosen via `useIsMobile`, the same source the shell uses), `size="wide"`
when it carries a form. The sheet is the item's editor, not an inspector (2026-09-07): every
authored, editable fact renders as a prefilled form field in place — `FormRow` over `Input` /
`Select`, one column — with a dirty-gated **Save** as the footer's primary `Button`, and
renaming is editing the name field. Read-only facts keep the meta-table idiom below. A
separate `Dialog` is reserved for write-only secret entry (an API key, a credential JSON) and
multi-step flows (OAuth); it never exists to edit a field the sheet could show. Binary state
(Enabled, Auto-upgrade) is a `Switch` row that applies immediately, disabled while its RPC is
in flight; the destructive action keeps its `ConfirmDialog` even though that nests a second
modal over the sheet — `OverlayPanel` instances stack in DOM order, each traps and restores
focus down the stack, and its `preventDefault` on Escape is what keeps the settings pane's own
document-level Escape handler from closing the pane out from under an open overlay. Closing a
sheet with unsaved edits discards them silently; the sheet reseeds only when a different item
opens or its own save lands, so another client's refresh never clobbers a draft.
```

Also update the sentence in the section's first paragraph `It is now the reference implementation for the two idioms below` to `Settings → Providers & credentials (`panes/settings/sections/credentials/InstanceSheet.tsx`) is the reference implementation of the sheet-as-editor form; Marketplaces & Plugins remains the reference for segments`.

- [ ] **Step 2: Append the decisions entry**

Append to `docs/web-ui/decisions.md`:

```markdown
## 2026-09-07 detail sheets are editors

The provider instance sheet shipped as an inspector: a stack of quiet
buttons whose "Edit" opened a one-field dialog (Base URL), and nothing could
rename an instance or change its api_key_env or credential header after
creation. Jesse called the whole pattern out. The rule in design-system.md
§10 now reads: a detail sheet is the item's editor. Editable facts are
prefilled form fields in place with a dirty-gated Save in the footer;
rename is editing the name field; dialogs are reserved for write-only
secret entry and multi-step flows. `InstanceSheet` is the reference
implementation (spec
`docs/superpowers/specs/2026-09-07-settings-sheet-editors-and-agents-doc-design.md`);
the marketplaces list follows in its own slice, and the installed-plugin
sheet already edited its two switches in place and is unchanged.

Two consequences ride along. Protocol and surface are exposed on both the
sheet and the Add form as selects over the registry's four-value
vocabularies, with an "inherit from base" empty option that sends the new
clear flags. And the wire's instance entry now carries the authored
api_key_env and credential header (never a secret: the header value is a
`$VAR` template by construction) so the form can prefill them.
```

- [ ] **Step 3: Commit**

```bash
git add docs/web-ui/design-system.md docs/web-ui/decisions.md
git commit -m "docs(web-ui): a detail sheet is the item's editor"
```

---

### Task 10: Gates, browser pass, and the pull request

- [ ] **Step 1: Run every gate from the repo root, reading each for failures**

```bash
make test-web 2>&1 | tail -15
make test-web-browser 2>&1 | tail -10
make lint 2>&1 | tail -20
make vet 2>&1 | tail -5
make test 2>&1 | tail -20
```

Expected: all green. `make lint`'s generated-output freshness check passes because Task 1 committed the regenerated files. If `make test` reports one of the known flaky tests (drain-abandon cleanup race, execenv, procgroup, drain-continue), re-run that package once and say which it was.

- [ ] **Step 2: Live check against a throwaway hub**

Follow the WebUI screenshot recipe in memory (`webui-screenshot-recipe.md`) with `XDG_CONFIG_HOME` pointed at a temp dir. In Settings → Providers & credentials: add an instance `work` on base `openai` with base URL `https://gw.example.test/v1`; open its row; confirm the form prefills; change the name to `work2`, the protocol to `openai-responses`, and the API key env to `PORTKEY_KEY`; Save; confirm the sheet stays open titled `work2`, and `cat "$XDG_CONFIG_HOME/evener/providers.toml"` shows `[providers.work2]` with `protocol = "openai-responses"` and `api_key_env = ["PORTKEY_KEY"]` and no `[providers.work]`. Set a key first and confirm it moved (`[work2]` in credentials.toml). Take a desktop and a phone-width screenshot of the sheet for the PR.

- [ ] **Step 3: Push and open the PR**

```bash
git push -u origin HEAD
gh pr create --repo prime-radiant-inc/evener --base main --title "Provider sheet is the editor: rename, credential fields, protocol and surface" --body-file - <<'EOF'
Slice 2 of docs/superpowers/specs/2026-09-07-settings-sheet-editors-and-agents-doc-design.md.

- `evener/instance/edit` gains `newName`, `apiKeyEnv`/`clearApiKeyEnv`, `credentialHeader`/`clearCredentialHeader`, `clearProtocol`, `clearSurface`; an empty var value deletes the var. All additive within v4.
- The instance entry carries the authored `apiKeyEnv` and `credentialHeader` so the form can prefill.
- A rename re-keys providers.toml, follows the default pointer, and moves the stored key and OAuth record; refused for implicit instances, invalid names, and taken names.
- `InstanceDetailSheet` → `InstanceSheet`: a prefilled form with a dirty-gated Save; `EditInstanceDialog` is gone. The Add form gains Protocol and Surface selects.
- design-system.md §10 now says a detail sheet is the item's editor; decisions.md records why.

Gates: make lint, make vet, make test, make test-web, make test-web-browser. Screenshots attached.
EOF
```

Then watch CI and roborev; findings that recur on every push get fixed in code.
