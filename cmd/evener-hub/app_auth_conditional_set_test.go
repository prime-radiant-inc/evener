package hub

// Host-side conditional credential set (evener/auth/apiKey/conditionalSet) and
// the ConfigRevision it fences against (design §07 "credential push"). The
// write is the 07c credential push's atomic replacement for the racy
// read-evener/auth/status-then-apiKey/set pair: the host re-resolves the
// instance's credential source and configuration revision under one
// credentialWrite critical section and refuses a stale observation instead of
// silently clobbering a credential that changed underneath the caller.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
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

// credentialHeadersInstanceToml is one instance whose credential resolves from
// an authored credential_headers entry, which outranks the file layer the push
// writes. The shape is the registry's own: credential_headers is a string map
// (llm/registry/write.go's setStringMap), and registry.credential reads the
// Authorization entry, expanding its $VAR reference
// (llm/registry/instances.go).
const credentialHeadersInstanceToml = `[providers.gateway-hdr]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"

[providers.gateway-hdr.credential_headers]
Authorization = "Bearer $WORK_HDR_KEY"
`

// authoredAuthHeaderInstanceToml is one instance on the header scheme whose own
// auth header is supplied by its authored credential_headers: `auth_header`
// points the key at X-Custom-Key, and a credential_headers entry supplies that
// very header. The registry resolves a credential source from the Authorization
// entry alone (llm/registry/instances.go), so this instance resolves "none" —
// or "store" once a key is stored — even though the authored header wins over
// any key (llm/authenticators.go's credentialHeaderWins, spec §10): the key
// such a push stores is one nothing ever sends.
const authoredAuthHeaderInstanceToml = `[providers.gateway-key]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
auth = "header"
auth_header = "X-Custom-Key"

[providers.gateway-key.credential_headers]
X-Custom-Key = "hdr-token"
`

// authoredAuthHeaderCaseVariantInstanceToml is the same shape with the authored
// header spelled in a different case: credentialHeaderWins compares
// case-insensitively, so the header still wins, while the registry's
// Authorization lookup (exact) still finds nothing.
const authoredAuthHeaderCaseVariantInstanceToml = `[providers.gateway-key]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
auth = "header"
auth_header = "X-Custom-Key"

[providers.gateway-key.credential_headers]
x-custom-key = "hdr-token"
`

// bearerAuthoredAuthorizationInstanceToml is the bearer half of the same
// exposure: the Authorization header is authored under a different case, which
// the registry's exact Authorization lookup does not see (so the source is not
// "credential_headers") but credentialHeaderWins does, so the bearer the scheme
// would derive from a key is never sent.
const bearerAuthoredAuthorizationInstanceToml = `[providers.gateway-bearer]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
auth = "bearer"

[providers.gateway-bearer.credential_headers]
AUTHORIZATION = "Bearer hdr-token"
`

// gatewayBearerInstanceToml is authNoneInstanceToml's instance re-authored onto
// a key-capable scheme under the same name, which is what makes a client's
// earlier view of it stale rather than merely changed.
const gatewayBearerInstanceToml = `[providers.gateway]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
auth = "bearer"
`

// headerWithoutAuthoredHeaderInstanceToml is the control: the same header
// scheme with no authored credential_headers entry at all, so the instance
// really does read a stored key and the push must still add it.
const headerWithoutAuthoredHeaderInstanceToml = `[providers.gateway-key]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
auth = "header"
auth_header = "X-Custom-Key"
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

// credentialDestinationInstanceToml is one instance with every field
// destinationIdentity fingerprints spelled out, plus the auth header, so a
// revision test can vary one of them at a time and prove the revision covers
// them all: a credential push prepared against one of these and applied after
// the field moved would otherwise land a secret aimed somewhere the client
// never reviewed.
const credentialDestinationInstanceToml = `[providers.gateway]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
auth = "header"
auth_header = "X-Custom-Key"
endpoint = "/v1/chat/completions"
stream_endpoint = "/v1/chat/stream"
models_endpoint = "/v1/models"
count_tokens_endpoint = "/v1/count-tokens"
`

// authoredGapInstanceToml is one instance whose credential is authored in
// providers.toml but resolves to nothing: api_key names a variable that is not
// set. The authored layer is terminal - registry.credential returns "none" at
// it without consulting the file store or the environment - so a key stored
// under this name is one nothing reads.
const authoredGapInstanceToml = `[providers.authored-gap]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
auth = "bearer"
api_key = "$AUTHORED_GAP_KEY"
`

// authoredHeaderGapInstanceToml is the same shape through the other terminal
// authored layer: an Authorization credential_headers entry whose variable is
// unset.
const authoredHeaderGapInstanceToml = `[providers.authored-gap]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
auth = "bearer"

[providers.authored-gap.credential_headers]
Authorization = "Bearer $AUTHORED_GAP_HDR"
`

// authoredGapCredentiallessInstanceToml is the same instance with no authored
// credential at all: the shape the revision has to distinguish from the two
// above, since all three resolve source "none".
const authoredGapCredentiallessInstanceToml = `[providers.authored-gap]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
auth = "bearer"
`

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

// TestAuth_ConfigRevisionCoversTheCredentialDestination proves the revision
// fence covers every destination field the endpoint fingerprint covers - the
// fields destinationIdentity digests - plus the instance's auth header, because
// the revision is the push's only no-clobber fence: the conditional set checks
// no endpoint fingerprint of its own. Changing any of them moves where the
// secret is sent (or, for the auth header, whether it is sent at all), so a
// revision that ignored one would let a stale push pass both fences and land a
// credential at a destination the client never reviewed.
func TestAuth_ConfigRevisionCoversTheCredentialDestination(t *testing.T) {
	cases := []struct {
		name  string
		field string
		want  string
		toml  string
	}{
		{
			name: "auth-header", field: "auth_header", want: "X-Other-Key",
			toml: strings.Replace(credentialDestinationInstanceToml, "auth_header = \"X-Custom-Key\"", "auth_header = \"X-Other-Key\"", 1),
		},
		{
			name: "base-url", field: "base_url", want: "http://127.0.0.1:9/v2",
			toml: strings.Replace(credentialDestinationInstanceToml, "base_url = \"http://127.0.0.1:9/v1\"", "base_url = \"http://127.0.0.1:9/v2\"", 1),
		},
		{
			name: "endpoint", field: "endpoint", want: "/v1/other-completions",
			toml: strings.Replace(credentialDestinationInstanceToml, "endpoint = \"/v1/chat/completions\"", "endpoint = \"/v1/other-completions\"", 1),
		},
		{
			name: "stream-endpoint", field: "stream_endpoint", want: "/v1/other-stream",
			toml: strings.Replace(credentialDestinationInstanceToml, "stream_endpoint = \"/v1/chat/stream\"", "stream_endpoint = \"/v1/other-stream\"", 1),
		},
		{
			name: "models-endpoint", field: "models_endpoint", want: "/v2/models",
			toml: strings.Replace(credentialDestinationInstanceToml, "models_endpoint = \"/v1/models\"", "models_endpoint = \"/v2/models\"", 1),
		},
		{
			name: "count-tokens-endpoint", field: "count_tokens_endpoint", want: "/v1/other-count-tokens",
			toml: strings.Replace(credentialDestinationInstanceToml, "count_tokens_endpoint = \"/v1/count-tokens\"", "count_tokens_endpoint = \"/v1/other-count-tokens\"", 1),
		},
	}
	dir := t.TempDir()
	stateDir := t.TempDir()
	writeProvidersToml(t, dir, credentialDestinationInstanceToml)
	ctrl := newTestAuthController(t, dir, stateDir, filepath.Join(dir, "providers.toml"))
	base, err := ctrl.Status(appwire.AuthStatusParams{Provider: "gateway"})
	if err != nil {
		t.Fatalf("Status(gateway): %v", err)
	}
	if base.ConfigRevision == "" {
		t.Fatal("ConfigRevision is empty; there is nothing to fence on")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.toml == credentialDestinationInstanceToml {
				t.Fatalf("the %s fixture does not differ from the base providers.toml", tc.field)
			}
			writeProvidersToml(t, dir, tc.toml)
			if err := ctrl.reg.Reload(); err != nil {
				t.Fatalf("reload after changing %s: %v", tc.field, err)
			}
			resolved, err := ctrl.registry().ResolveInstance("gateway")
			if err != nil {
				t.Fatalf("ResolveInstance(gateway): %v", err)
			}
			got := map[string]string{
				"auth_header":           resolved.Transport.AuthHeader,
				"base_url":              resolved.Transport.BaseURL,
				"endpoint":              resolved.Transport.Endpoint,
				"stream_endpoint":       resolved.Transport.StreamEndpoint,
				"models_endpoint":       resolved.Transport.ModelsEndpoint,
				"count_tokens_endpoint": resolved.Transport.CountTokensEndpoint,
			}[tc.field]
			if got != tc.want {
				t.Fatalf("the fixture does not set %s: resolved %q, want %q", tc.field, got, tc.want)
			}
			changed, err := ctrl.Status(appwire.AuthStatusParams{Provider: "gateway"})
			if err != nil {
				t.Fatalf("Status(gateway) after changing %s: %v", tc.field, err)
			}
			if changed.ConfigRevision == base.ConfigRevision {
				t.Fatalf("the revision did not move when %s changed (%q): a push prepared before the change would pass the fence and send the key to a destination the client never reviewed", tc.field, changed.ConfigRevision)
			}
		})
	}
}

// TestAuth_ApiKeyConditionalSet_SkipsAnAuthoredCredentialThatResolvesToNothing
// pins the other direction of the same class the authored-header skip closes:
// an authored credential whose variables are unset is terminal, so the instance
// resolves "none" (registry.credential returns there without consulting the file
// store or the environment) while a stored key is never read. Reported as
// "added" it is a live credential that is dead.
func TestAuth_ApiKeyConditionalSet_SkipsAnAuthoredCredentialThatResolvesToNothing(t *testing.T) {
	cases := []struct {
		name     string
		toml     string
		instance string
		layer    string
	}{
		{name: "api-key-var-unset", toml: authoredGapInstanceToml, instance: "authored-gap", layer: "api_key"},
		{name: "credential-headers-var-unset", toml: authoredHeaderGapInstanceToml, instance: "authored-gap", layer: "credential_headers"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stateDir := t.TempDir()
			ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, tc.toml))
			before, err := ctrl.Status(appwire.AuthStatusParams{Provider: tc.instance})
			if err != nil {
				t.Fatalf("Status(%s): %v", tc.instance, err)
			}
			// The premise, from the client's own read: nothing resolves here, so
			// "none" is what the push echoes back as ExpectedSource.
			if before.ActiveSource != "none" {
				t.Fatalf("ActiveSource = %q, want none", before.ActiveSource)
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
				t.Fatalf("Action = %q, want skipped: providers.toml authors this instance's %s and outranks the store", resp.Action, tc.layer)
			}
			if !strings.Contains(resp.Reason, tc.layer) {
				t.Fatalf("Reason = %q, want it to name the authored %s layer", resp.Reason, tc.layer)
			}
			if _, has := loadStoredKey(t, dir, tc.instance); has {
				t.Fatal("a key was stored for an instance whose authored credential outranks the store")
			}
		})
	}
}

// TestAuth_ConfigRevisionCoversTheAuthoredUnresolvedLayer pins the marker in the
// revision: an instance that authors a credential whose variables are unset
// resolves "none", exactly like one that authors nothing, so without the marker
// a push prepared against the credentialless instance and applied after the
// authored layer appeared would pass both fences - and the key it stores is one
// nothing reads.
func TestAuth_ConfigRevisionCoversTheAuthoredUnresolvedLayer(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	writeProvidersToml(t, dir, authoredGapCredentiallessInstanceToml)
	ctrl := newTestAuthController(t, dir, stateDir, filepath.Join(dir, "providers.toml"))
	credentialless, err := ctrl.Status(appwire.AuthStatusParams{Provider: "authored-gap"})
	if err != nil {
		t.Fatalf("Status(authored-gap): %v", err)
	}
	if credentialless.ActiveSource != "none" {
		t.Fatalf("ActiveSource = %q, want none", credentialless.ActiveSource)
	}

	for _, tc := range []struct {
		name string
		toml string
	}{
		{name: "api-key-var-unset", toml: authoredGapInstanceToml},
		{name: "credential-headers-var-unset", toml: authoredHeaderGapInstanceToml},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeProvidersToml(t, dir, tc.toml)
			if err := ctrl.reg.Reload(); err != nil {
				t.Fatalf("reload: %v", err)
			}
			authored, err := ctrl.Status(appwire.AuthStatusParams{Provider: "authored-gap"})
			if err != nil {
				t.Fatalf("Status(authored-gap): %v", err)
			}
			// The source is the same "none" both sides of the change, so the
			// revision is the only thing that can carry the difference.
			if authored.ActiveSource != "none" {
				t.Fatalf("ActiveSource = %q, want none", authored.ActiveSource)
			}
			if authored.ConfigRevision == credentialless.ConfigRevision {
				t.Fatalf("the revision did not move when providers.toml gained an authored credential whose variables are unset (%q)", authored.ConfigRevision)
			}
		})
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

// TestAuth_ConfigRevisionIsKeyedWithTheHubKey pins the H1 fix: the revision the
// credential push fences against is a keyed MAC over the configuration, not a
// bare digest. The configuration's destination half can carry a secret in a
// part a listing strips - a password in userinfo, a short query-string token in
// base_url - and an unkeyed digest of a guessable secret is a guessable
// function of it, so a reader who can see the revision could recover the secret
// offline. The property that separates keyed from unkeyed: a bare digest of one
// configuration is one value, while a keyed MAC follows the hub's key.
func TestAuth_ConfigRevisionIsKeyedWithTheHubKey(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, bearerInstanceToml))

	// The revision is keyed with the same hub-held key the endpoint fingerprints
	// use, resolved through the same seam, so a fixed key here is a fixed hub key.
	prev := resolveEndpointFingerprintKey
	t.Cleanup(func() { resolveEndpointFingerprintKey = prev })
	revisionWithKey := func(key []byte) string {
		resolveEndpointFingerprintKey = func(string) ([]byte, error) { return key, nil }
		status, err := ctrl.Status(appwire.AuthStatusParams{Provider: "work-ant"})
		if err != nil {
			t.Fatalf("Status(work-ant): %v", err)
		}
		return status.ConfigRevision
	}

	first := revisionWithKey([]byte("hub-key-one"))
	if first == "" {
		t.Fatal("ConfigRevision is empty for a resolvable instance under a usable key")
	}
	if again := revisionWithKey([]byte("hub-key-one")); again != first {
		t.Fatalf("ConfigRevision moved for one unchanged configuration under one key: %q then %q", first, again)
	}
	if other := revisionWithKey([]byte("hub-key-two")); other == first {
		t.Fatalf("ConfigRevision is identical under two different hub keys (%q): it is not keyed, so a secret in the destination it covers is brute-forceable from the served value", first)
	}
}

// TestAuth_ConditionalSetRefusesWhenTheRevisionKeyIsUnavailable pins the other
// half of H1: an empty revision means "no revision fence" to a client, so a hub
// that cannot key its revision must NOT answer that way and then accept the
// writes that follow. With a state root that cannot yield its key the host
// serves no revision (the same key powers the endpoint fingerprint, whose
// absence the listing already diagnoses) and the conditional set refuses a
// write - whether the client asserted a stale revision or nothing at all -
// rather than landing the key in an unverifiable configuration.
func TestAuth_ConditionalSetRefusesWhenTheRevisionKeyIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	unkeyableStateRoot(t, stateDir)
	ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, bearerInstanceToml))

	status, err := ctrl.Status(appwire.AuthStatusParams{Provider: "work-ant"})
	if err != nil {
		t.Fatalf("Status(work-ant): %v", err)
	}
	if status.ConfigRevision != "" {
		t.Fatalf("ConfigRevision = %q, want empty when the hub cannot key its revision", status.ConfigRevision)
	}

	for _, expected := range []string{"", "a-revision-from-an-older-read"} {
		_, err := ctrl.ApiKeyConditionalSet(appwire.ApiKeyConditionalSetParams{
			Provider:         "work-ant",
			Value:            "sk-must-not-land",
			ExpectedRevision: expected,
		})
		if err == nil {
			t.Fatalf("ApiKeyConditionalSet landed a key with no usable revision key (ExpectedRevision=%q)", expected)
		}
		assertWireCode(t, err, appwire.CodeConflict)
	}
	if value, ok := loadStoredKey(t, dir, "work-ant"); ok {
		t.Fatalf("stored key = %q, want nothing written while the revision cannot be keyed", value)
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
		// wantSource, when set, pins the source the fixture must actually resolve
		// from: a case that skipped for a different reason than it claims would
		// otherwise pass for the wrong reason.
		wantSource string
		// wantReason, when set, pins the exact Reason the classification must
		// produce. The Reason strings are user-visible, and for a source an
		// explicit branch names it is also what tells that branch apart from the
		// switch's catch-all skip: without this, moving credential_headers out of
		// its branch would still skip (via default) with a vaguer message, and no
		// test would notice.
		wantReason string
	}{
		{name: "codex-oauth", toml: codexInstanceToml, instance: "work"},
		{name: "gcp-adc", toml: gcpADCInstanceToml, instance: "vertexish"},
		{name: "auth-none", toml: authNoneInstanceToml, instance: "gateway"},
		{name: "providers-toml-api-key", toml: apiKeyInstanceToml, instance: "authored", env: map[string]string{"AUTHORED_KEY": "sk-authored"}},
		{
			name: "credential-headers", toml: credentialHeadersInstanceToml, instance: "gateway-hdr", env: map[string]string{"WORK_HDR_KEY": "sk-hdr"},
			wantSource: "credential_headers",
			wantReason: "gateway-hdr resolves its credential from providers.toml (credential_headers), which outranks the file layer",
		},
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
			if tc.wantSource != "" && before.ActiveSource != tc.wantSource {
				t.Fatalf("Status(%s).ActiveSource = %q, want %q: the fixture does not resolve from the source this case is about", tc.instance, before.ActiveSource, tc.wantSource)
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
			if tc.wantReason != "" && resp.Reason != tc.wantReason {
				t.Fatalf("Reason = %q, want %q", resp.Reason, tc.wantReason)
			}
			if _, has := loadStoredKey(t, dir, tc.instance); has {
				t.Fatalf("a key was stored for a skipped instance %q", tc.instance)
			}
		})
	}
}

// TestAuth_ApiKeyConditionalSet_SkipsWhenTheAuthoredHeaderShadowsTheKey pins
// the classification spec §10 needs and the source string alone cannot express:
// the registry names only the Authorization entry, so an instance whose own
// auth header is supplied by an authored credential_headers entry resolves
// "none" or "store" — the two sources the conditional set treats as writable —
// while the authored header wins over any key (credentialHeaderWins), so the
// key such a set persists is one nothing ever sends. Reporting "added"/"updated"
// here reports a live credential that is dead.
func TestAuth_ApiKeyConditionalSet_SkipsWhenTheAuthoredHeaderShadowsTheKey(t *testing.T) {
	cases := []struct {
		name     string
		toml     string
		instance string
		// stored is a key written through the auth surface before the
		// conditional set, which makes the instance resolve from "store" rather
		// than "none" — the other writable classification.
		stored string
		// wantScheme pins the auth scheme the fixture must actually resolve, so a
		// case cannot pass by skipping for some reason other than the header it
		// is about.
		wantScheme string
		wantSource string
		// wantAction is skipped for a shadowed instance and added for the
		// control, so the test cannot pass by skipping on the header scheme
		// wholesale.
		wantAction string
	}{
		{
			name: "header-authored-header-exact", toml: authoredAuthHeaderInstanceToml, instance: "gateway-key",
			wantScheme: registry.AuthHeader, wantSource: "none", wantAction: appwire.ApiKeyConditionalSetActionSkipped,
		},
		{
			name: "header-authored-header-exact-over-store", toml: authoredAuthHeaderInstanceToml, instance: "gateway-key", stored: "sk-old",
			wantScheme: registry.AuthHeader, wantSource: "store", wantAction: appwire.ApiKeyConditionalSetActionSkipped,
		},
		{
			name: "header-authored-header-case-variant", toml: authoredAuthHeaderCaseVariantInstanceToml, instance: "gateway-key", stored: "sk-old",
			wantScheme: registry.AuthHeader, wantSource: "store", wantAction: appwire.ApiKeyConditionalSetActionSkipped,
		},
		{
			name: "bearer-authored-authorization-case-variant", toml: bearerAuthoredAuthorizationInstanceToml, instance: "gateway-bearer",
			wantScheme: registry.AuthBearer, wantSource: "none", wantAction: appwire.ApiKeyConditionalSetActionSkipped,
		},
		{
			name: "control-header-without-authored-header", toml: headerWithoutAuthoredHeaderInstanceToml, instance: "gateway-key",
			wantScheme: registry.AuthHeader, wantSource: "none", wantAction: appwire.ApiKeyConditionalSetActionAdded,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stateDir := t.TempDir()
			ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, tc.toml))
			if tc.stored != "" {
				if _, err := ctrl.ApiKeySet(appwire.AuthApiKeySetParams{Provider: tc.instance, Value: tc.stored}); err != nil {
					t.Fatalf("seed %s: %v", tc.instance, err)
				}
			}
			inst, ok := ctrl.registry().Instance(tc.instance)
			if !ok {
				t.Fatalf("%s does not resolve on this fixture", tc.instance)
			}
			if inst.Auth != tc.wantScheme {
				t.Fatalf("%s resolves auth = %q, want %q: the fixture does not exercise the scheme this case is about", tc.instance, inst.Auth, tc.wantScheme)
			}
			if inst.CredentialSource != tc.wantSource {
				t.Fatalf("%s resolves its credential source = %q, want %q", tc.instance, inst.CredentialSource, tc.wantSource)
			}
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
			if resp.Action != tc.wantAction {
				t.Fatalf("Action = %q, want %q (reason %q)", resp.Action, tc.wantAction, resp.Reason)
			}
			if tc.wantAction == appwire.ApiKeyConditionalSetActionSkipped && resp.Reason == "" {
				t.Fatal("Reason is empty for a skip; the report cannot say why")
			}
			value, has := loadStoredKey(t, dir, tc.instance)
			switch tc.wantAction {
			case appwire.ApiKeyConditionalSetActionAdded:
				if !has || value != "sk-pushed" {
					t.Fatalf("stored key = %q present=%v, want the pushed value", value, has)
				}
			default:
				if tc.stored == "" {
					if has {
						t.Fatalf("a key was stored for a skipped instance: %q", value)
					}
				} else if !has || value != tc.stored {
					t.Fatalf("stored key = %q present=%v, want the pre-existing %q untouched", value, has, tc.stored)
				}
			}
		})
	}
}

// TestAuth_ApiKeyConditionalSet_SkipsACorruptCodexRecordInsteadOfConflicting
// pins the one place the client's observed source legitimately differs from the
// host's re-resolution, and the outcome the design's classification table gives
// for it.
//
// A Codex instance whose auth/<name>.json is unreadable is "none" to
// evener/auth/status — openAIInstanceStatus treats a corrupt record as absent,
// the state the spawn gate refuses — and "oauth" to registry resolution, which
// asks only whether the record file exists (registry.credential). The push
// echoes that observed source back as ExpectedSource, so a source fence applied
// to a scheme that consumes no key turns the documented skip into a Conflict:
// "... no longer resolves its credential from \"none\" (it is now \"oauth\"):
// re-read the instance and start the push again" — a remedy re-reading cannot
// deliver, because every read reproduces the pair. The push reports "failed"
// where the design says "skipped".
func TestAuth_ApiKeyConditionalSet_SkipsACorruptCodexRecordInsteadOfConflicting(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, codexInstanceToml))
	record := authopenai.AuthFilePath(stateDir, "work")
	if err := os.MkdirAll(filepath.Dir(record), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(record), err)
	}
	if err := os.WriteFile(record, []byte("{ not an auth record"), 0o600); err != nil {
		t.Fatalf("write corrupt record: %v", err)
	}

	// The two reads of one instance that the push pairs: the status read it
	// fences with, and the resolution the host classifies from.
	status, err := ctrl.Status(appwire.AuthStatusParams{Provider: "work"})
	if err != nil {
		t.Fatalf("Status(work): %v", err)
	}
	if status.ActiveSource != "none" {
		t.Fatalf("Status(work).ActiveSource = %q, want none: openAIInstanceStatus treats a corrupt record as absent", status.ActiveSource)
	}
	inst, ok := ctrl.registry().Instance("work")
	if !ok {
		t.Fatal("work does not resolve on this fixture")
	}
	if inst.CredentialSource != "oauth" {
		t.Fatalf("registry CredentialSource = %q, want oauth: the record file exists and the scheme reads it", inst.CredentialSource)
	}

	resp, err := ctrl.ApiKeyConditionalSet(appwire.ApiKeyConditionalSetParams{
		Provider:         "work",
		Value:            "sk-pushed",
		ExpectedSource:   status.ActiveSource,
		ExpectedRevision: status.ConfigRevision,
	})
	if err != nil {
		t.Fatalf("ApiKeyConditionalSet(work) with the source evener/auth/status reported: %v", err)
	}
	if resp.Action != appwire.ApiKeyConditionalSetActionSkipped {
		t.Fatalf("Action = %q, want skipped: a Codex instance consumes no key (reason %q)", resp.Action, resp.Reason)
	}
	if resp.Reason == "" {
		t.Fatal("Reason is empty for a skip; the report cannot say why")
	}
	if _, has := loadStoredKey(t, dir, "work"); has {
		t.Fatal("a key was stored for a Codex instance")
	}
}

// TestAuth_ApiKeyConditionalSet_RefusesAStaleViewOfANonKeyCapableScheme proves
// that classifying a non-key-capable scheme before the source fence did not drop
// the no-clobber fence for the case that fence exists for: a client that
// observed such an instance and then pushed at one re-authored onto a
// key-capable scheme. The revision fence still refuses it, which is why it is
// checked first - the revision hashes the resolved scheme and source, so the
// re-authoring moves it. (This is a guard, not a behavior change: it holds both
// before and after that reordering.)
func TestAuth_ApiKeyConditionalSet_RefusesAStaleViewOfANonKeyCapableScheme(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, authNoneInstanceToml)
	ctrl := newTestAuthController(t, dir, stateDir, tomlPath)
	before, err := ctrl.Status(appwire.AuthStatusParams{Provider: "gateway"})
	if err != nil {
		t.Fatalf("Status(gateway): %v", err)
	}
	if before.ActiveSource != "none" || before.ConfigRevision == "" {
		t.Fatalf("status = %+v, want an auth-none instance with a revision to fence on", before)
	}

	// The same name, now key-capable: the client's view of it is stale from here.
	writeProvidersToml(t, dir, gatewayBearerInstanceToml)
	if err := ctrl.reg.Reload(); err != nil {
		t.Fatalf("reload after re-authoring gateway: %v", err)
	}

	if _, err := ctrl.ApiKeyConditionalSet(appwire.ApiKeyConditionalSetParams{
		Provider:         "gateway",
		Value:            "sk-pushed",
		ExpectedSource:   before.ActiveSource,
		ExpectedRevision: before.ConfigRevision,
	}); err == nil {
		t.Fatal("ApiKeyConditionalSet applied a write carrying the stale revision of a scheme that has since become key-capable")
	} else {
		assertWireCode(t, err, appwire.CodeConflict)
	}
	if _, has := loadStoredKey(t, dir, "gateway"); has {
		t.Fatal("a key was stored for an instance whose observed configuration no longer matched")
	}

	// Positive control: the same call on a freshly read view of that same
	// re-authored instance lands, so the Conflict above is the stale revision
	// refusing it and not some other refusal the reordering introduced.
	fresh, err := ctrl.Status(appwire.AuthStatusParams{Provider: "gateway"})
	if err != nil {
		t.Fatalf("Status(gateway) after the re-authoring: %v", err)
	}
	if fresh.ConfigRevision == before.ConfigRevision {
		t.Fatalf("the revision did not move when the scheme changed: %q", fresh.ConfigRevision)
	}
	resp, err := ctrl.ApiKeyConditionalSet(appwire.ApiKeyConditionalSetParams{
		Provider:         "gateway",
		Value:            "sk-pushed",
		ExpectedSource:   fresh.ActiveSource,
		ExpectedRevision: fresh.ConfigRevision,
	})
	if err != nil {
		t.Fatalf("ApiKeyConditionalSet(gateway) with the fresh view: %v", err)
	}
	if resp.Action != appwire.ApiKeyConditionalSetActionAdded {
		t.Fatalf("Action = %q, want added for a key-capable instance with no credential (reason %q)", resp.Action, resp.Reason)
	}
	if value, has := loadStoredKey(t, dir, "gateway"); !has || value != "sk-pushed" {
		t.Fatalf("stored key = %q present=%v, want the pushed value", value, has)
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

// TestAuth_ApiKeyConditionalSet_SkippedSetDoesNotReload pins the write-only
// reload: a skip ran its decision under the credential lock but changed
// nothing, so it must not re-derive the instance set. A permitted write still
// does, which the positive control asserts so the skip assertion cannot pass
// vacuously.
func TestAuth_ApiKeyConditionalSet_SkippedSetDoesNotReload(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, authNoneInstanceToml))
	before := ctrl.reg.Generation()
	resp, err := ctrl.ApiKeyConditionalSet(appwire.ApiKeyConditionalSetParams{Provider: "gateway", Value: "sk-pushed"})
	if err != nil {
		t.Fatalf("ApiKeyConditionalSet(gateway): %v", err)
	}
	if resp.Action != appwire.ApiKeyConditionalSetActionSkipped {
		t.Fatalf("Action = %q, want skipped", resp.Action)
	}
	if after := ctrl.reg.Generation(); after != before {
		t.Fatalf("a skipped conditional set reloaded the registry (generation %d -> %d): it wrote nothing", before, after)
	}

	// Positive control: the same call on a writable instance does reload.
	dir2 := t.TempDir()
	stateDir2 := t.TempDir()
	writable := newTestAuthController(t, dir2, stateDir2, writeProvidersToml(t, dir2, bearerInstanceToml))
	beforeWrite := writable.reg.Generation()
	written, err := writable.ApiKeyConditionalSet(appwire.ApiKeyConditionalSetParams{Provider: "work-ant", Value: "sk-new"})
	if err != nil {
		t.Fatalf("ApiKeyConditionalSet(work-ant): %v", err)
	}
	if written.Action != appwire.ApiKeyConditionalSetActionAdded {
		t.Fatalf("Action = %q, want added", written.Action)
	}
	if after := writable.reg.Generation(); after == beforeWrite {
		t.Fatalf("a landed conditional set did not reload the registry (generation %d unchanged)", beforeWrite)
	}
}
