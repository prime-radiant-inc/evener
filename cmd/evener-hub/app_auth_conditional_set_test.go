package hub

// Host-side conditional credential set (evener/auth/apiKey/conditionalSet) and
// the ConfigRevision it fences against (design §07 "credential push"). The
// write is the 07c credential push's atomic replacement for the racy
// read-evener/auth/status-then-apiKey/set pair: the host re-resolves the
// instance's credential source and configuration revision under one
// credentialWrite critical section and refuses a stale observation instead of
// silently clobbering a credential that changed underneath the caller.

import (
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/internal/credentials"
)

// gcpADCInstanceToml is one instance on the Google application-default
// credentials transport, which does not read an API key.
const gcpADCInstanceToml = `[providers.vertexish]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
auth = "gcp-adc"
`

// authNoneInstanceToml is one instance that authenticates with nothing at all.
const authNoneInstanceToml = `[providers.gateway]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
auth = "none"
`

// apiKeyInstanceToml is one instance whose credential resolves from
// providers.toml, which outranks the file layer the push writes.
const apiKeyInstanceToml = `[providers.authored]
base = "anthropic"
api_key = "$AUTHORED_KEY"
`

func loadStoredKey(t *testing.T, credsDir, name string) (string, bool) {
	t.Helper()
	store, err := credentials.LoadStore(filepath.Join(credsDir, "credentials.toml"))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	return store.Get(name)
}

// ─────────────────────────────────────────────────────────────────────────────
// Requirement 2: AuthStatusResponse / InstanceEntry carry ConfigRevision.
// ─────────────────────────────────────────────────────────────────────────────

// TestAuth_StatusExposesConfigRevision proves AuthStatusResponse carries a
// non-empty ConfigRevision, that it is stable while the instance's credential
// configuration is unchanged, and that it moves when the effective source
// changes — the property a conditional set fences against.
func TestAuth_StatusExposesConfigRevision(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, bearerInstanceToml))

	// No credential: the instance resolves from nothing, and the revision is
	// still a defined value (there is a configuration to fence).
	credentialless, err := ctrl.Status(appwire.AuthStatusParams{Provider: "work-ant"})
	if err != nil {
		t.Fatalf("Status(work-ant): %v", err)
	}
	if credentialless.ActiveSource != "none" {
		t.Fatalf("ActiveSource = %q, want none", credentialless.ActiveSource)
	}
	if credentialless.ConfigRevision == "" {
		t.Fatal("ConfigRevision is empty for a resolvable instance; the credential push has nothing to source ExpectedRevision from")
	}

	// Stable while nothing changes.
	again, err := ctrl.Status(appwire.AuthStatusParams{Provider: "work-ant"})
	if err != nil {
		t.Fatalf("Status(work-ant) again: %v", err)
	}
	if again.ConfigRevision != credentialless.ConfigRevision {
		t.Fatalf("ConfigRevision moved across two reads of an unchanged instance: %q then %q", credentialless.ConfigRevision, again.ConfigRevision)
	}

	// A store write changes the effective source, so the revision must move.
	if _, err := ctrl.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "work-ant", Value: "sk-stored"}); err != nil {
		t.Fatalf("ApiKeySet(work-ant): %v", err)
	}
	stored, err := ctrl.Status(appwire.AuthStatusParams{Provider: "work-ant"})
	if err != nil {
		t.Fatalf("Status(work-ant) after store: %v", err)
	}
	if stored.ActiveSource != "store" {
		t.Fatalf("ActiveSource after store = %q, want store", stored.ActiveSource)
	}
	if stored.ConfigRevision == credentialless.ConfigRevision {
		t.Fatalf("ConfigRevision did not move when the effective credential source changed: %q", stored.ConfigRevision)
	}
}

// TestAuth_InstanceEntryExposesConfigRevision proves the evener/instance/list
// row carries the same revision AuthStatusResponse does, so the credential
// push's implicit-provider fallback has a defined source for
// ExpectedRevision too.
func TestAuth_InstanceEntryExposesConfigRevision(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, bearerInstanceToml)
	ctl := newTestInstancesController(t, tomlPath, dir, stateDir, map[string]string{"WORK_ANT_KEY": "env-key"})

	row := entry(t, ctl.List(), "work-ant")
	if row.ConfigRevision == "" {
		t.Fatal("InstanceEntry.ConfigRevision is empty; the instance list cannot source ExpectedRevision")
	}
	status, err := ctl.auth.Status(appwire.AuthStatusParams{Provider: "work-ant"})
	if err != nil {
		t.Fatalf("Status(work-ant): %v", err)
	}
	if row.ConfigRevision != status.ConfigRevision {
		t.Fatalf("InstanceEntry.ConfigRevision = %q, want the status value %q", row.ConfigRevision, status.ConfigRevision)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Requirement 1: the conditional set writes only when permitted, under the lock.
// ─────────────────────────────────────────────────────────────────────────────

// TestAuth_ApiKeyConditionalSet_AddedForCredentiallessInstance proves the
// compare-and-set path writes when the instance has no credential and can
// consume an API key, reporting the "added" action and the post-write status.
func TestAuth_ApiKeyConditionalSet_AddedForCredentiallessInstance(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, bearerInstanceToml))

	before, err := ctrl.Status(appwire.AuthStatusParams{Provider: "work-ant"})
	if err != nil {
		t.Fatalf("Status(work-ant): %v", err)
	}
	resp, err := ctrl.ApiKeyConditionalSet(appwire.ApiKeyConditionalSetParams{
		Provider:         "work-ant",
		Value:            "sk-pushed",
		ExpectedSource:   before.ActiveSource,
		ExpectedRevision: before.ConfigRevision,
	})
	if err != nil {
		t.Fatalf("ApiKeyConditionalSet(work-ant): %v", err)
	}
	if resp.Action != appwire.ApiKeyConditionalSetActionAdded {
		t.Fatalf("Action = %q, want %q (reason %q)", resp.Action, appwire.ApiKeyConditionalSetActionAdded, resp.Reason)
	}
	if resp.Status.ActiveSource != "store" || !resp.Status.HasStoredFile {
		t.Fatalf("Status = %+v, want the post-write store source", resp.Status)
	}
	if resp.Status.ConfigRevision == before.ConfigRevision {
		t.Fatalf("post-write ConfigRevision did not move: %q", resp.Status.ConfigRevision)
	}
	value, ok := loadStoredKey(t, dir, "work-ant")
	if !ok || value != "sk-pushed" {
		t.Fatalf("stored key = %q present=%v, want sk-pushed", value, ok)
	}
}

// TestAuth_ApiKeyConditionalSet_UpdatedForStoreSource proves a second push over
// an existing file-layer key reports "updated".
func TestAuth_ApiKeyConditionalSet_UpdatedForStoreSource(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, bearerInstanceToml))
	if err := ctrl.setCredential("work-ant", "sk-first"); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if err := ctrl.reloadRegistry(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	before, err := ctrl.Status(appwire.AuthStatusParams{Provider: "work-ant"})
	if err != nil {
		t.Fatalf("Status(work-ant): %v", err)
	}
	resp, err := ctrl.ApiKeyConditionalSet(appwire.ApiKeyConditionalSetParams{
		Provider:         "work-ant",
		Value:            "sk-second",
		ExpectedSource:   before.ActiveSource,
		ExpectedRevision: before.ConfigRevision,
	})
	if err != nil {
		t.Fatalf("ApiKeyConditionalSet(work-ant): %v", err)
	}
	if resp.Action != appwire.ApiKeyConditionalSetActionUpdated {
		t.Fatalf("Action = %q, want %q (reason %q)", resp.Action, appwire.ApiKeyConditionalSetActionUpdated, resp.Reason)
	}
	if value, _ := loadStoredKey(t, dir, "work-ant"); value != "sk-second" {
		t.Fatalf("stored key = %q, want sk-second", value)
	}
}

// TestAuth_ApiKeyConditionalSet_RefusesStaleRevision is the core fence: a write
// prepared against a revision the host has since moved is refused typed, and
// the credential that changed underneath the caller is NOT clobbered. The
// source stays "store" across the change on purpose, so the source fence cannot
// be what refuses it: only the revision fence can. This test fails if the write
// is applied with a stale revision.
func TestAuth_ApiKeyConditionalSet_RefusesStaleRevision(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	const tomlBefore = `[providers.work-ant]
base = "anthropic"
api_key_env = ["WORK_ANT_KEY"]
base_url = "http://127.0.0.1:9/v1"
`
	const tomlAfter = `[providers.work-ant]
base = "anthropic"
api_key_env = ["WORK_ANT_KEY"]
base_url = "http://127.0.0.1:10/v1"
`
	writeProvidersToml(t, dir, tomlBefore)
	ctrl := newTestAuthController(t, dir, stateDir, filepath.Join(dir, "providers.toml"))
	if err := ctrl.setCredential("work-ant", "sk-other"); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if err := ctrl.reloadRegistry(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	// The controller observes the instance resolving from the store.
	observed, err := ctrl.Status(appwire.AuthStatusParams{Provider: "work-ant"})
	if err != nil {
		t.Fatalf("Status(work-ant): %v", err)
	}
	if observed.ActiveSource != "store" {
		t.Fatalf("ActiveSource = %q, want store", observed.ActiveSource)
	}
	if observed.ConfigRevision == "" {
		t.Fatal("ConfigRevision is empty; the fence would be vacuous")
	}

	// The instance is re-pointed before this write lands: the source is still
	// the store, so only the configuration revision has moved.
	writeProvidersToml(t, dir, tomlAfter)
	if err := ctrl.reloadRegistry(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	_, err = ctrl.ApiKeyConditionalSet(appwire.ApiKeyConditionalSetParams{
		Provider:         "work-ant",
		Value:            "sk-mine",
		ExpectedSource:   observed.ActiveSource,
		ExpectedRevision: observed.ConfigRevision,
	})
	if err == nil {
		t.Fatal("ApiKeyConditionalSet applied a write carrying a stale ExpectedRevision; it must be refused")
	}
	assertWireCode(t, err, appwire.CodeConflict)

	// Nothing was written: the concurrent actor's key survives.
	value, ok := loadStoredKey(t, dir, "work-ant")
	if !ok || value != "sk-other" {
		t.Fatalf("stored key = %q present=%v, want the concurrent actor's sk-other", value, ok)
	}
}

// TestAuth_ApiKeyConditionalSet_RefusesStaleSource proves the source fence
// applies even when the caller sent no revision: a non-empty ExpectedSource the
// instance no longer resolves from is refused typed, never applied.
func TestAuth_ApiKeyConditionalSet_RefusesStaleSource(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, bearerInstanceToml))

	observed, err := ctrl.Status(appwire.AuthStatusParams{Provider: "work-ant"})
	if err != nil {
		t.Fatalf("Status(work-ant): %v", err)
	}
	if observed.ActiveSource != "none" {
		t.Fatalf("ActiveSource = %q, want none to start", observed.ActiveSource)
	}
	if err := ctrl.setCredential("work-ant", "sk-other"); err != nil {
		t.Fatalf("seed concurrent store: %v", err)
	}
	if err := ctrl.reloadRegistry(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	_, err = ctrl.ApiKeyConditionalSet(appwire.ApiKeyConditionalSetParams{
		Provider:       "work-ant",
		Value:          "sk-mine",
		ExpectedSource: observed.ActiveSource,
	})
	if err == nil {
		t.Fatal("ApiKeyConditionalSet applied a write carrying a stale ExpectedSource; it must be refused")
	}
	assertWireCode(t, err, appwire.CodeConflict)
	if value, ok := loadStoredKey(t, dir, "work-ant"); !ok || value != "sk-other" {
		t.Fatalf("stored key = %q present=%v, want the concurrent actor's sk-other", value, ok)
	}
}

// TestAuth_ApiKeyConditionalSet_ClassifiesNonWritableSchemes proves a scheme
// whose credential a file-layer key must not shadow comes back as a successful
// typed "skipped" with a reason — the design's classification, never a silent
// write.
func TestAuth_ApiKeyConditionalSet_ClassifiesNonWritableSchemes(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	cases := []struct {
		name     string
		toml     string
		instance string
		env      map[string]string
	}{
		{name: "codex-oauth", toml: codexInstanceToml, instance: "work"},
		{name: "gcp-adc", toml: gcpADCInstanceToml, instance: "vertexish"},
		{name: "auth-none", toml: authNoneInstanceToml, instance: "gateway"},
		{name: "providers-toml-api-key", toml: apiKeyInstanceToml, instance: "authored", env: map[string]string{"AUTHORED_KEY": "sk-authored"}},
		{name: "environment", toml: bearerInstanceToml, instance: "work-ant", env: map[string]string{"WORK_ANT_KEY": "sk-env"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stateDir := t.TempDir()
			ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, tc.toml), tc.env)
			before, err := ctrl.Status(appwire.AuthStatusParams{Provider: tc.instance})
			if err != nil {
				t.Fatalf("Status(%s): %v", tc.instance, err)
			}
			resp, err := ctrl.ApiKeyConditionalSet(appwire.ApiKeyConditionalSetParams{
				Provider:       tc.instance,
				Value:          "sk-pushed",
				ExpectedSource: before.ActiveSource,
			})
			if err != nil {
				t.Fatalf("ApiKeyConditionalSet(%s): %v", tc.instance, err)
			}
			if resp.Action != appwire.ApiKeyConditionalSetActionSkipped {
				t.Fatalf("Action = %q, want skipped", resp.Action)
			}
			if resp.Reason == "" {
				t.Fatal("Reason is empty for a skip; the report cannot say why")
			}
			if _, has := loadStoredKey(t, dir, tc.instance); has {
				t.Fatalf("a key was stored for a skipped instance %q", tc.instance)
			}
		})
	}
}

// TestAuth_ApiKeyConditionalSet_UnknownNameSkips proves a name the host no
// longer resolves is a typed skip, not a write under a name nothing reads.
func TestAuth_ApiKeyConditionalSet_UnknownNameSkips(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, bearerInstanceToml))
	resp, err := ctrl.ApiKeyConditionalSet(appwire.ApiKeyConditionalSetParams{
		Provider: "no-such-instance",
		Value:    "sk-pushed",
	})
	if err != nil {
		t.Fatalf("ApiKeyConditionalSet(no-such-instance): %v", err)
	}
	if resp.Action != appwire.ApiKeyConditionalSetActionSkipped || resp.Reason == "" {
		t.Fatalf("resp = %+v, want a skipped classification with a reason", resp)
	}
	if _, has := loadStoredKey(t, dir, "no-such-instance"); has {
		t.Fatal("a key was stored under an unknown name")
	}
}

// TestAuth_ApiKeyConditionalSet_NoFenceWhenUnobserved proves the zero revision
// and zero source assert no fence: an instance with no credential is still
// added, matching the design's "the zero value is no revision fence" rule.
func TestAuth_ApiKeyConditionalSet_NoFenceWhenUnobserved(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, bearerInstanceToml))
	resp, err := ctrl.ApiKeyConditionalSet(appwire.ApiKeyConditionalSetParams{
		Provider: "work-ant",
		Value:    "sk-pushed",
	})
	if err != nil {
		t.Fatalf("ApiKeyConditionalSet(work-ant): %v", err)
	}
	if resp.Action != appwire.ApiKeyConditionalSetActionAdded {
		t.Fatalf("Action = %q, want added", resp.Action)
	}
}

// TestAuth_ApiKeyConditionalSet_RejectsEmptyValue keeps the shared validation
// the other credential writes have.
func TestAuth_ApiKeyConditionalSet_RejectsEmptyValue(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, bearerInstanceToml))
	_, err := ctrl.ApiKeyConditionalSet(appwire.ApiKeyConditionalSetParams{Provider: "work-ant", Value: "  "})
	if err == nil {
		t.Fatal("ApiKeyConditionalSet accepted an empty value")
	}
	assertWireCode(t, err, appwire.CodeInvalidParams)
}
