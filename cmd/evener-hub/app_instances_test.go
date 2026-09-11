package hub

// Tests for hubInstancesController Create/Edit/Remove/SetDefault/List on the
// provider registry.
//
// Each test owns its providers.toml, credentials.toml, OAuth state directory
// and environment, so nothing here reads the developer's machine.

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

// instancesFixture is one isolated instances pane: a providers.toml the
// controller writes, the registry that re-reads it, and the credential state
// the entries resolve against.
type instancesFixture struct {
	ctl       *hubInstancesController
	tomlPath  string
	stateDir  string
	credsPath string
	store     *credentials.Store
}

// newTestInstancesController builds an instances controller whose registry
// reads tomlPath as its user layer, with credentials at credsDir and OAuth
// state at stateDir.
func newTestInstancesController(t *testing.T, tomlPath, credsDir, stateDir string, env ...map[string]string) *hubInstancesController {
	t.Helper()
	store, err := credentials.LoadStore(filepath.Join(credsDir, "credentials.toml"))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	lookup := map[string]string{}
	if len(env) > 0 {
		lookup = env[0]
	}
	auth := newHubAuthControllerWithStore(credsDir, store)
	auth.stateDir = stateDir
	auth.providersConfigPath = tomlPath
	auth.reg = hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		opts := []registry.Option{
			registry.WithOffline(true),
			registry.WithoutCache(),
			registry.WithConfigPath(tomlPath),
			registry.WithStateRoot(stateDir),
			registry.WithCredentials(cmdutil.StoreCredentialSource{Store: store}),
			registry.WithEnv(func(name string) (string, bool) {
				v, ok := lookup[name]
				return v, ok
			}),
		}
		r, err := registry.Load(append(opts, extra...)...)
		return r, store, err
	})
	// A deliberately broken fixture must still produce a controller: the
	// refusal is what several tests are about.
	_ = auth.reg.Reload()
	return &hubInstancesController{reg: auth.reg, providersConfigPath: tomlPath, auth: auth}
}

// newInstancesFixture is one isolated instances pane over a fresh temp dir.
func newInstancesFixture(t *testing.T, env map[string]string) *instancesFixture {
	t.Helper()
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	ctl := newTestInstancesController(t, tomlPath, dir, stateDir, env)
	return &instancesFixture{
		ctl:       ctl,
		tomlPath:  tomlPath,
		stateDir:  stateDir,
		credsPath: filepath.Join(dir, "credentials.toml"),
		store:     ctl.auth.creds,
	}
}

// entry finds one instance in a list response.
func entry(t *testing.T, resp appwire.InstanceListResponse, name string) appwire.InstanceEntry {
	t.Helper()
	for _, e := range resp.Instances {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("List has no instance %q; got %+v", name, resp.Instances)
	return appwire.InstanceEntry{}
}

// authoredEntry re-reads providers.toml and returns the authored entry, which
// is the only way to tell what the controller actually persisted.
func authoredEntry(t *testing.T, path, name string) registry.Provider {
	t.Helper()
	l, exists, err := registry.ReadConfigFile(path)
	if err != nil {
		t.Fatalf("ReadConfigFile(%s): %v", path, err)
	}
	if !exists {
		t.Fatalf("providers.toml absent at %s", path)
	}
	p, ok := l.Providers[name]
	if !ok {
		t.Fatalf("providers.toml has no [providers.%s]; got %v", name, l.Providers)
	}
	return p
}

// writeMinimalProvidersToml writes a registry-schema providers.toml carrying
// one authored instance, so the file exists and the pane has something to edit.
func writeMinimalProvidersToml(t *testing.T, path string) {
	t.Helper()
	const content = `default = "base"

[providers.base]
base    = "anthropic"
api_key = "sk-inline"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
}

func TestInstances_CreateWritesRegistryEntryAndLists(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"PORTKEY_KEY": "pk"})

	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name:             "work",
		Base:             "openai",
		BaseURL:          "https://gw.example.test/v1",
		Protocol:         "openai-chat",
		Surface:          "generic",
		APIKeyEnv:        "WORK_KEY",
		CredentialHeader: "Authorization=Bearer $PORTKEY_KEY",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	p := authoredEntry(t, f.tomlPath, "work")
	if p.Base != "openai" || p.Protocol != "openai-chat" || p.Surface != "generic" {
		t.Fatalf("authored entry = %+v, want base/protocol/surface as created", p)
	}
	if p.Transport.BaseURL != "https://gw.example.test/v1" {
		t.Fatalf("authored base_url = %q", p.Transport.BaseURL)
	}
	if len(p.APIKeyEnv) != 1 || p.APIKeyEnv[0] != "WORK_KEY" {
		t.Fatalf("authored api_key_env = %v, want [WORK_KEY]", p.APIKeyEnv)
	}
	if got := p.CredentialHeaders["Authorization"]; got != "Bearer $PORTKEY_KEY" {
		t.Fatalf("authored credential header = %q, want the $VAR reference verbatim", got)
	}
	if p.APIKey != "" {
		t.Fatalf("api_key must never be written; got %q", p.APIKey)
	}

	got := entry(t, f.ctl.List(), "work")
	if got.Base != "openai" || got.Protocol != "openai-chat" || got.Surface != "generic" {
		t.Fatalf("entry = %+v, want the registry view of the new instance", got)
	}
	if got.BaseURL != "https://gw.example.test/v1" {
		t.Fatalf("entry BaseURL = %q", got.BaseURL)
	}
	if got.Implicit {
		t.Fatal("an authored instance is not implicit")
	}
	if got.ActiveSource != "credential_headers" {
		t.Fatalf("ActiveSource = %q, want credential_headers", got.ActiveSource)
	}
	if !got.CredentialRequired {
		t.Fatal("a bearer instance requires a credential")
	}
}

func TestInstances_ListReportsAvailableProvidersAndUserLayer(t *testing.T) {
	f := newInstancesFixture(t, nil)
	resp := f.ctl.List()
	var openai *appwire.ProviderDescriptor
	for i := range resp.AvailableProviders {
		if resp.AvailableProviders[i].ID == "openai" {
			openai = &resp.AvailableProviders[i]
		}
	}
	if openai == nil {
		t.Fatalf("AvailableProviders has no openai: %+v", resp.AvailableProviders)
	}
	if openai.Protocol == "" || openai.Auth == "" || !openai.Implicit {
		t.Fatalf("openai descriptor = %+v, want protocol, auth and implicit", *openai)
	}
	if len(openai.APIKeyEnv) == 0 {
		t.Fatalf("openai descriptor names no api_key_env: %+v", *openai)
	}
	if openai.Vars["BASE_URL"] != "OPENAI_BASE_URL" {
		t.Fatalf("openai descriptor Vars = %+v, want the template name BASE_URL keyed to OPENAI_BASE_URL", openai.Vars)
	}
	if !slices.Contains(openai.VarsEnv, "OPENAI_BASE_URL") {
		t.Fatalf("openai descriptor VarsEnv = %v, want the env-var names as a sorted list", openai.VarsEnv)
	}
	if !slices.IsSorted(openai.VarsEnv) {
		t.Fatalf("VarsEnv not sorted: %v", openai.VarsEnv)
	}
	if !strings.Contains(resp.UserLayer, f.tomlPath) && !strings.Contains(resp.UserLayer, "user layer: none") {
		t.Fatalf("UserLayer = %q, want the note the registry produced", resp.UserLayer)
	}
	if resp.WritesRefused {
		t.Fatal("a readable providers.toml does not refuse writes")
	}
	if resp.Instances == nil || resp.AvailableProviders == nil {
		t.Fatal("both lists are always arrays on the wire, never null")
	}
}

// TestInstances_ListOffersOnlyTemplateVars pins that a descriptor's variable
// inputs are the ones the registry substitutes: google-vertex's snapshot env
// list also names GOOGLE_APPLICATION_CREDENTIALS, which instance vars never
// feed (the credential is read from the environment or the store), so an
// input for it would persist a value nothing reads (roborev round 19).
func TestInstances_ListOffersOnlyTemplateVars(t *testing.T) {
	f := newInstancesFixture(t, nil)
	resp := f.ctl.List()
	var vertex *appwire.ProviderDescriptor
	for i := range resp.AvailableProviders {
		if resp.AvailableProviders[i].ID == "google-vertex" {
			vertex = &resp.AvailableProviders[i]
		}
	}
	if vertex == nil {
		t.Fatalf("AvailableProviders has no google-vertex: %+v", resp.AvailableProviders)
	}
	// GOOGLE_VERTEX_ENDPOINT is read only by the OpenAI-compatible rows' own
	// base URL, which is also the only place models.dev maps it.
	wantVars := map[string]string{
		"GOOGLE_VERTEX_ENDPOINT": "GOOGLE_VERTEX_ENDPOINT",
		"GOOGLE_VERTEX_LOCATION": "GOOGLE_VERTEX_LOCATION",
		"GOOGLE_VERTEX_PROJECT":  "GOOGLE_VERTEX_PROJECT",
	}
	if !maps.Equal(vertex.Vars, wantVars) {
		t.Fatalf("google-vertex descriptor Vars = %v, want only the URL templates' variables %v", vertex.Vars, wantVars)
	}
	if want := []string{"GOOGLE_VERTEX_ENDPOINT", "GOOGLE_VERTEX_LOCATION", "GOOGLE_VERTEX_PROJECT"}; !slices.Equal(vertex.VarsEnv, want) {
		t.Fatalf("google-vertex descriptor VarsEnv = %v, want %v", vertex.VarsEnv, want)
	}
}

func TestInstances_CreateRejectsBadInput(t *testing.T) {
	for _, tt := range []struct {
		name   string
		params appwire.InstanceCreateParams
		want   string
		wire   bool
	}{
		{
			wire:   true,
			name:   "invalid instance name",
			params: appwire.InstanceCreateParams{Name: "Work/Two", Base: "openai"},
			want:   "invalid instance name",
		},
		{
			wire:   true,
			name:   "unknown base provider",
			params: appwire.InstanceCreateParams{Name: "work", Base: "not-a-provider"},
			want:   "unknown base provider",
		},
		{
			wire:   true,
			name:   "invalid variable name",
			params: appwire.InstanceCreateParams{Name: "work", Base: "openai", Vars: map[string]string{"region": "value"}},
			want:   "invalid variable name",
		},
		{
			wire:   true,
			name:   "literal secret in a credential header",
			params: appwire.InstanceCreateParams{Name: "work", Base: "openai", CredentialHeader: "Authorization=Bearer sk-literal"},
			want:   "$VARIABLE",
		},
		{
			// A bare "contains a $" check takes this: the key rides in the
			// literal text beside the reference (spec §11.2).
			wire:   true,
			name:   "a literal secret smuggled beside a reference",
			params: appwire.InstanceCreateParams{Name: "work", Base: "openai", CredentialHeader: "Authorization=Bearer sk-live-abc$X"},
			want:   "$VARIABLE",
		},
		{
			wire:   true,
			name:   "a literal secret as its own word",
			params: appwire.InstanceCreateParams{Name: "work", Base: "openai", CredentialHeader: "Authorization=Bearer sk-live-abc $X"},
			want:   "$VARIABLE",
		},
		{
			wire:   true,
			name:   "unterminated variable reference in a credential header",
			params: appwire.InstanceCreateParams{Name: "work", Base: "openai", CredentialHeader: "Authorization=Bearer ${TOKEN"},
			want:   "unterminated",
		},
		{
			wire:   true,
			name:   "invalid variable name in a credential header",
			params: appwire.InstanceCreateParams{Name: "work", Base: "openai", CredentialHeader: "Authorization=Bearer ${1BAD}"},
			want:   "invalid environment variable name",
		},
		{
			wire:   true,
			name:   "a credential header that is not NAME=VALUE",
			params: appwire.InstanceCreateParams{Name: "work", Base: "openai", CredentialHeader: "Authorization $PORTKEY_KEY"},
			want:   "NAME=VALUE",
		},
		{
			// api_key_env names the variable holding the key, never the key.
			wire:   true,
			name:   "a literal key in api_key_env",
			params: appwire.InstanceCreateParams{Name: "work", Base: "openai", APIKeyEnv: "sk-live-abc"},
			want:   "environment variable",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newInstancesFixture(t, nil)
			err := f.ctl.Create(tt.params)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Create = %v, want an error mentioning %q", err, tt.want)
			}
			// A credential header the form got wrong is the caller's to fix,
			// so the pane is told which field, not handed a bare error.
			var wire appwire.WireError
			if tt.wire && (!errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams) {
				t.Fatalf("Create = %v, want an InvalidParams wire error", err)
			}
			if strings.Contains(err.Error(), "sk-live-abc") || strings.Contains(err.Error(), "sk-literal") {
				t.Fatalf("the refusal echoed the value: %v", err)
			}
			if _, err := os.Stat(f.tomlPath); !os.IsNotExist(err) {
				t.Fatalf("a rejected create wrote providers.toml (stat err=%v)", err)
			}
		})
	}
}

func TestInstances_CreateRejectsDuplicateName(t *testing.T) {
	f := newInstancesFixture(t, nil)
	params := appwire.InstanceCreateParams{Name: "work", Base: "openai", APIKeyEnv: "WORK_KEY"}
	if err := f.ctl.Create(params); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err := f.ctl.Create(params)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second Create = %v, want an already-exists error", err)
	}
	// The name collides with an existing entry, matching the Conflict class
	// hubDirsCreate and the pin-section store use for the same "taken" shape.
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("second Create = %v, want a Conflict wire error", err)
	}
}

// TestInstances_EditImplicitWritesShadowingEntry is spec §11.3: editing an
// instance that exists only from the environment authors an entry carrying
// the edited field alone — never the base_url the form merely displayed,
// which would trip §10's credential-inheritance stop.
func TestInstances_EditImplicitWritesShadowingEntry(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	before := entry(t, f.ctl.List(), "groq")
	if !before.Implicit {
		t.Fatalf("groq should be implicit before the edit: %+v", before)
	}
	if before.BaseURL == "" {
		t.Fatal("the pane displays groq's resolved base URL")
	}

	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "groq", Protocol: "openai-responses"}); err != nil {
		t.Fatalf("Edit: %v", err)
	}

	p := authoredEntry(t, f.tomlPath, "groq")
	if p.Protocol != "openai-responses" {
		t.Fatalf("authored protocol = %q, want openai-responses", p.Protocol)
	}
	if p.Transport.BaseURL != "" {
		t.Fatalf("the shadowing entry carries base_url = %q; only the edited fields belong in it", p.Transport.BaseURL)
	}
	if p.Base != "" && p.Base != "groq" {
		t.Fatalf("the shadowing entry has base = %q", p.Base)
	}

	after := entry(t, f.ctl.List(), "groq")
	if after.Protocol != "openai-responses" {
		t.Fatalf("the reloaded list still shows protocol %q", after.Protocol)
	}
	if after.ActiveSource != "env:GROQ_API_KEY" {
		t.Fatalf("ActiveSource = %q; the shadowing entry must not break credential inheritance", after.ActiveSource)
	}
}

// TestInstances_EditWithoutClearLeavesAnAuthoredBaseURLAlone: neither an
// omitted BaseURL nor ClearBaseURL=false must disturb an already-authored
// override — only an explicit new value or an explicit clear touches it
// (#711).
func TestInstances_EditWithoutClearLeavesAnAuthoredBaseURLAlone(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "groq", BaseURL: "http://127.0.0.1:9/v1"}); err != nil {
		t.Fatalf("Edit(set): %v", err)
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "groq", Protocol: "openai-responses"}); err != nil {
		t.Fatalf("Edit(protocol only): %v", err)
	}
	p := authoredEntry(t, f.tomlPath, "groq")
	if p.Transport.BaseURL != "http://127.0.0.1:9/v1" {
		t.Fatalf("authored base_url = %q, want the earlier override left alone", p.Transport.BaseURL)
	}
	if p.Protocol != "openai-responses" {
		t.Fatalf("authored protocol = %q", p.Protocol)
	}
}

// TestInstances_EditClearsAuthoredBaseURLBackToDefault is #711: an authored
// base_url stops spec §10's credential inheritance from the base provider,
// so an instance that could never clear it back to the registry default was
// also stuck without the base's api_key_env. ClearBaseURL clears the
// override and restores both the default endpoint and the inherited
// credential, additively — BaseURL itself keeps meaning "leave unchanged"
// when empty (v3).
func TestInstances_EditClearsAuthoredBaseURLBackToDefault(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	before := entry(t, f.ctl.List(), "groq")
	if before.ActiveSource != "env:GROQ_API_KEY" {
		t.Fatalf("groq should inherit its credential before any override: activeSource = %q", before.ActiveSource)
	}

	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "groq", BaseURL: "http://127.0.0.1:9/v1"}); err != nil {
		t.Fatalf("Edit(set): %v", err)
	}
	stopped := entry(t, f.ctl.List(), "groq")
	if stopped.BaseURL != "http://127.0.0.1:9/v1" {
		t.Fatalf("authored base URL = %q", stopped.BaseURL)
	}
	if stopped.ActiveSource == "env:GROQ_API_KEY" {
		t.Fatalf("a literal base_url should stop credential inheritance (spec §10), but activeSource is still %q", stopped.ActiveSource)
	}

	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "groq", ClearBaseURL: true}); err != nil {
		t.Fatalf("Edit(clear): %v", err)
	}
	p := authoredEntry(t, f.tomlPath, "groq")
	if p.Transport.BaseURL != "" {
		t.Fatalf("authored base_url = %q after clearing, want empty", p.Transport.BaseURL)
	}
	after := entry(t, f.ctl.List(), "groq")
	if after.BaseURL != before.BaseURL {
		t.Fatalf("after clearing, base URL = %q, want the registry default %q", after.BaseURL, before.BaseURL)
	}
	if after.ActiveSource != "env:GROQ_API_KEY" {
		t.Fatalf("clearing the base_url override should resume credential inheritance (spec §10); activeSource = %q", after.ActiveSource)
	}
}

// TestInstances_EditRejectsAClearThatWouldOrphanAStandaloneInstance is #711:
// an instance with no base and no base_url of its own cannot resolve an
// endpoint at all (llm/registry: "no base URL: set base_url = … or base =
// <registry id>"), and one bad instance record fails the whole registry
// reload, not just this one. writeLoadable's dry parse only checks TOML
// syntax against the schema, so clearing the only base_url a standalone
// instance has would otherwise write a config that parses fine but cannot
// load, falling back the whole pane to an implicit-only registry and
// refusing every instance operation until a human fixes the file outside
// the hub. Edit must refuse the clear and leave the file exactly as it was.
func TestInstances_EditRejectsAClearThatWouldOrphanAStandaloneInstance(t *testing.T) {
	dir := t.TempDir()
	// No `base` line: "standalone" names no registry id of its own either,
	// so its base_url is the only thing that lets it resolve at all.
	tomlPath := writeProvidersToml(t, dir, `[providers.standalone]
protocol = "openai-chat"
base_url = "http://127.0.0.1:9/v1"
`)
	ctl := newTestInstancesController(t, tomlPath, dir, t.TempDir())
	before := entry(t, ctl.List(), "standalone")
	if before.BaseURL != "http://127.0.0.1:9/v1" {
		t.Fatalf("fixture did not load as expected: baseURL = %q", before.BaseURL)
	}

	err := ctl.Edit(appwire.InstanceEditParams{Name: "standalone", ClearBaseURL: true})
	if err == nil {
		t.Fatal("Edit accepted a clear that would leave standalone unable to resolve an endpoint")
	}
	if !strings.Contains(err.Error(), "standalone") {
		t.Fatalf("Edit = %v, want the refusal to name the instance", err)
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("Edit = %v, want an InvalidParams wire error", err)
	}

	// The file must be exactly as it was: not left mid-write, not carrying
	// the rejected clear.
	p := authoredEntry(t, tomlPath, "standalone")
	if p.Transport.BaseURL != "http://127.0.0.1:9/v1" {
		t.Fatalf("authored base_url = %q after a rejected clear, want the original untouched", p.Transport.BaseURL)
	}

	// The pane must not be locked out: WritesRefused must still be false,
	// and a subsequent valid edit must still go through.
	if ctl.reg.WritesRefused() {
		t.Fatal("a rejected clear left the registry refusing writes; the pane is now locked out")
	}
	if err := ctl.Edit(appwire.InstanceEditParams{Name: "standalone", Protocol: "openai-responses"}); err != nil {
		t.Fatalf("the pane refused a valid edit after rejecting an orphaning clear: %v", err)
	}
}

func TestInstances_EditMergesVarsIntoAuthoredEntry(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"AWS_ACCESS_KEY_ID": "id", "AWS_SECRET_ACCESS_KEY": "secret"})
	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name: "bedrock", Base: "amazon-bedrock",
		Vars: map[string]string{"AWS_REGION": "us-east-1"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{
		Name: "bedrock",
		Vars: map[string]string{"AWS_REGION": "eu-west-1"},
	}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	p := authoredEntry(t, f.tomlPath, "bedrock")
	if p.Transport.Vars["AWS_REGION"] != "eu-west-1" {
		t.Fatalf("authored vars = %v, want the edited AWS_REGION", p.Transport.Vars)
	}
	if p.Base != "amazon-bedrock" {
		t.Fatalf("the edit dropped base = %q", p.Base)
	}
}

func TestInstances_EditRejectsUnknownInstance(t *testing.T) {
	f := newInstancesFixture(t, nil)
	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "nowhere", Protocol: "openai-chat"})
	if err == nil || !strings.Contains(err.Error(), "nowhere") {
		t.Fatalf("Edit = %v, want an unknown-instance error", err)
	}
	// A name that resolves to nothing is the caller's to fix, the same shape
	// as Create's unknown-base-provider check (#717/#748) — InvalidParams,
	// not an internal fault.
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("Edit = %v, want an InvalidParams wire error", err)
	}
}

// TestInstances_RemoveRefusesImplicitInstance: an instance that exists from
// the environment has no entry to delete, so the refusal says what to unset.
func TestInstances_RemoveRefusesImplicitInstance(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "groq"})
	if err == nil {
		t.Fatal("Remove accepted an implicit instance")
	}
	if !strings.Contains(err.Error(), "GROQ_API_KEY") {
		t.Fatalf("the refusal names the variable that creates the instance: %v", err)
	}
}

// TestInstances_RemoveRejectsUnknownInstance: a name that resolves to no
// instance is the caller's to fix, the same class Edit's unknown-instance
// refusal carries (#717/#748) — InvalidParams, not an internal fault.
func TestInstances_RemoveRejectsUnknownInstance(t *testing.T) {
	f := newInstancesFixture(t, nil)
	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "nowhere"})
	if err == nil || !strings.Contains(err.Error(), "nowhere") {
		t.Fatalf("Remove = %v, want an unknown-instance error", err)
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("Remove = %v, want an InvalidParams wire error", err)
	}
}

// TestInstances_RemoveRejectsAnInvalidName: a name the grammar refuses is
// the caller's to fix, the class Create and Edit give the same shape
// (#717/#748) - and it is the check that keeps a name carrying path
// separators away from the OAuth state file it would be joined into.
func TestInstances_RemoveRejectsAnInvalidName(t *testing.T) {
	f := newInstancesFixture(t, nil)
	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "Bad/Name"})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("Remove = %v, want an InvalidParams wire error", err)
	}
}

func TestInstances_RemoveDeletesEntryStoreKeyAndOAuthRecord(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// LoadAuth refuses a record that fails Validate, so only a complete one
	// lets the assertion below tell a deleted record from a rejected one.
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	if err := f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "work"}); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}

	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	l, _, err := registry.ReadConfigFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadConfigFile: %v", err)
	}
	if _, still := l.Providers["work"]; still {
		t.Fatal("[providers.work] survived Remove")
	}
	if l.Default == "work" {
		t.Fatal("default still names the removed instance; the next load would refuse the file")
	}
	if v, _ := f.store.Get("work"); v != "" {
		t.Fatalf("the stored key survived Remove: %q", v)
	}
	if _, err := authopenai.LoadAuth(f.stateDir, "work"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("the OAuth record survived Remove (err = %v)", err)
	}
	for _, e := range f.ctl.List().Instances {
		if e.Name == "work" {
			t.Fatal("List still shows the removed instance")
		}
	}
}

func TestInstances_SetDefaultWritesDefault(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	if err := f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "groq"}); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	l, _, err := registry.ReadConfigFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadConfigFile: %v", err)
	}
	if l.Default != "groq" {
		t.Fatalf("authored default = %q, want groq", l.Default)
	}
	if !entry(t, f.ctl.List(), "groq").IsDefault {
		t.Fatal("the reloaded list does not mark groq default")
	}
	err = f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "nowhere"})
	if err == nil {
		t.Fatal("SetDefault accepted a name that is not an instance")
	}
	// Same class as Edit's unknown-instance refusal (#717/#748): the name the
	// caller sent is theirs to fix, not a fault of the hub's own state.
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("SetDefault = %v, want an InvalidParams wire error", err)
	}
}

// The two tests below pin one rule for the writes that name an instance:
// the instance a write was validated against is the instance it acts on. A
// rename holds c.mu while it re-keys providers.toml and reloads the
// registry, so a lookup made outside that lock describes an instance the
// write no longer reaches.

// TestInstances_RemoveValidatesTheInstanceUnderTheControllerLock: what Remove
// deletes - the authored entry, the stored key and the OAuth record under the
// name - is decided by the lookup, so a lookup made before c.mu hands the
// deletion to whatever holds the name once the rename has landed.
func TestInstances_RemoveValidatesTheInstanceUnderTheControllerLock(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"OPENAI_API_KEY": "env-key"})
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "openai", Base: "anthropic"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("openai", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if inst, ok := f.ctl.reg.Get().Instance("openai"); !ok || inst.Implicit {
		t.Fatalf("the fixture's openai must be the authored entry Remove deletes (ok = %v, %+v)", ok, inst)
	}

	f.ctl.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai"}) }()
	// Only so a lookup made outside the lock has run by the time the rename
	// lands: what the test asserts does not depend on the wait.
	time.Sleep(100 * time.Millisecond)
	renameProvidersEntry(t, f.ctl.auth, f.tomlPath, `[providers.work]
base = "anthropic"
`)
	f.ctl.mu.Unlock()

	select {
	case err := <-done:
		// Errorf, not Fatalf: the credential check below is the other half of
		// the finding and is worth reporting in the same run.
		if err == nil || !strings.Contains(err.Error(), "exists from the environment") {
			t.Errorf("Remove = %v, want the refusal for the instance the name holds now", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Remove never returned after the rename released the lock")
	}
	if v, _ := f.store.Get("openai"); v != "sk-stored" {
		t.Fatalf("the credential of the instance now under the name was deleted: openai = %q", v)
	}
}

// TestInstances_SetDefaultValidatesTheInstanceUnderTheControllerLock: a
// default naming an instance a rename has moved away is one the next load
// refuses while it sits on disk, so the check and the write are one step.
func TestInstances_SetDefaultValidatesTheInstanceUnderTheControllerLock(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "anthropic"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	f.ctl.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "work"}) }()
	// Only so a lookup made outside the lock has run by the time the rename
	// lands: what the test asserts does not depend on the wait.
	time.Sleep(100 * time.Millisecond)
	renameProvidersEntry(t, f.ctl.auth, f.tomlPath, `[providers.work2]
base = "anthropic"
`)
	f.ctl.mu.Unlock()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), `instance "work" not found`) {
			t.Fatalf("SetDefault = %v, want the not-found refusal for the name the rename left behind", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SetDefault never returned after the rename released the lock")
	}
	l, _, err := registry.ReadConfigFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadConfigFile: %v", err)
	}
	if l.Default != "" {
		t.Fatalf("authored default = %q: a default the next load refuses was written", l.Default)
	}
}

// TestInstances_OldSchemaRefusesEveryWrite is spec §14.1's flag day at the
// pane: a providers.toml the registry cannot read is reported, and no write
// touches it until the user fixes it by hand.
func TestInstances_OldSchemaRefusesEveryWrite(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	const oldSchema = "default = \"openai\"\n[instances.openai]\ntype = \"openai\"\n"
	if err := os.WriteFile(f.tomlPath, []byte(oldSchema), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.reg.Reload(); err == nil {
		t.Fatal("the registry accepted an old-schema file")
	}

	resp := f.ctl.List()
	if !resp.WritesRefused {
		t.Fatal("List must report that writes are refused")
	}
	if !strings.Contains(strings.Join(resp.Diagnostics, "\n"), "§14.1") {
		t.Fatalf("diagnostics carry the flag-day pointer: %v", resp.Diagnostics)
	}
	if _, ok := func() (appwire.InstanceEntry, bool) {
		for _, e := range resp.Instances {
			if e.Name == "groq" {
				return e, true
			}
		}
		return appwire.InstanceEntry{}, false
	}(); !ok {
		t.Fatalf("the pane still lists the implicit instances the hub is running on: %+v", resp.Instances)
	}

	for name, call := range map[string]func() error{
		"Create":     func() error { return f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}) },
		"Edit":       func() error { return f.ctl.Edit(appwire.InstanceEditParams{Name: "groq", Protocol: "openai-chat"}) },
		"Remove":     func() error { return f.ctl.Remove(appwire.InstanceRemoveParams{Name: "groq"}) },
		"SetDefault": func() error { return f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "groq"}) },
	} {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "§14.1") {
			t.Fatalf("%s = %v, want a refusal carrying the pointer", name, err)
		}
	}
	data, err := os.ReadFile(f.tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != oldSchema {
		t.Fatalf("the hub rewrote a file it could not read:\n%s", data)
	}
}

func TestSanitizeEndpointURL(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"", ""},
		{"https://user:pw@gw.example.test/v1?token=abc#frag", "https://gw.example.test/v1"},
		{"not a url", ""},
		{"https://gw.example.test/v1", "https://gw.example.test/v1"},
	} {
		if got := sanitizeEndpointURL(tt.in); got != tt.want {
			t.Errorf("sanitizeEndpointURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestInstances_WritesRefusedBeforeTheFirstLoad: a holder that has not loaded
// has no registry to consult, and the mutators dereference it. Refusing is the
// same answer a broken file gets, and it is one condition rather than four
// nil checks at the call sites.
func TestInstances_WritesRefusedBeforeTheFirstLoad(t *testing.T) {
	ctl := &hubInstancesController{reg: hubcore.NewProviderRegistry(nil), providersConfigPath: filepath.Join(t.TempDir(), "providers.toml")}
	for name, call := range map[string]func() error{
		"Create":     func() error { return ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}) },
		"Edit":       func() error { return ctl.Edit(appwire.InstanceEditParams{Name: "work"}) },
		"Remove":     func() error { return ctl.Remove(appwire.InstanceRemoveParams{Name: "work"}) },
		"SetDefault": func() error { return ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "work"}) },
	} {
		if err := call(); err == nil {
			t.Errorf("%s = nil, want a refusal: there is no registry to write against", name)
		}
	}
	if resp := ctl.List(); !resp.WritesRefused || len(resp.Instances) != 0 {
		t.Fatalf("List = %+v, want no instances and writes refused", resp)
	}
}

// TestInstances_RefusesAnyWriteTheRegistryCouldNotReadBack is the invariant
// behind kata-shaped lockouts: a file the pane writes must be one the registry
// can read. Anything else lands on disk, fails the reload that follows, flips
// WritesRefused, and leaves refuseWhenBroken refusing the corrective edit —
// the pane locked out of its own recovery. Each case names a different way the
// parser refuses, and none of them is a field the controller checks by hand.
func TestInstances_RefusesAnyWriteTheRegistryCouldNotReadBack(t *testing.T) {
	for _, tt := range []struct {
		name   string
		params appwire.InstanceCreateParams
		want   string
	}{
		{
			// Var NAMES are checked by hand; their values are the parser's.
			name:   "unterminated variable reference in a var value",
			params: appwire.InstanceCreateParams{Name: "work", Base: "openai", Vars: map[string]string{"REGION": "${TOKEN"}},
			want:   "vars.REGION",
		},
		{
			// The word the form itself used to show.
			name:   "protocol outside the registry vocabulary",
			params: appwire.InstanceCreateParams{Name: "work", Base: "openai", Protocol: "chat-completions"},
			want:   "unknown protocol",
		},
		{
			name:   "surface outside the registry vocabulary",
			params: appwire.InstanceCreateParams{Name: "work", Base: "openai", Surface: "compat"},
			want:   "unknown surface",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
			err := f.ctl.Create(tt.params)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Create = %v, want an error mentioning %q", err, tt.want)
			}
			if _, statErr := os.Stat(f.tomlPath); !os.IsNotExist(statErr) {
				t.Fatalf("a rejected create wrote providers.toml (stat err=%v)", statErr)
			}
			// The pane is not locked out: the registry still loads, and the
			// next valid write goes through.
			if f.ctl.reg.WritesRefused() {
				t.Fatal("the registry stopped loading after a rejected create")
			}
			if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai", APIKeyEnv: "WORK_KEY"}); err != nil {
				t.Fatalf("the pane refused a valid create after rejecting an invalid one: %v", err)
			}
			if _, ok := f.ctl.reg.Get().Instance("work"); !ok {
				t.Fatal("the valid instance did not reach the reloaded registry")
			}
		})
	}
}

// TestInstances_RefusalKindsAreDistinguishable pins the two error classes a
// write can produce. A candidate providers.toml the registry could not read
// back is the caller's entry being wrong — InvalidParams, so the pane says
// which field to fix. A filesystem failure is the hub's problem, not the
// caller's, and must not come back as "the fields you sent are bad" or the
// pane sends the user to correct an entry that is fine.
func TestInstances_RefusalKindsAreDistinguishable(t *testing.T) {
	t.Run("unreadable candidate config is invalid params", func(t *testing.T) {
		f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
		err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai", Protocol: "chat-completions"})
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Fatalf("Create = %v, want an InvalidParams wire error", err)
		}
	})

	t.Run("a write failure is not invalid params", func(t *testing.T) {
		f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
		// A real unwritable directory, sealed after the registry has loaded
		// the file inside it: the read Create makes still succeeds (0555 is
		// readable), and the atomic write's temp file is what cannot be
		// created. Sealing it before the load would refuse the write for the
		// other reason — no registry — and never reach the refusal under test.
		dir := filepath.Dir(f.tomlPath)
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatalf("seal %s: %v", dir, err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"})
		if err == nil {
			t.Fatal("a failed write must be reported")
		}
		var wire appwire.WireError
		if errors.As(err, &wire) && wire.Code == appwire.CodeInvalidParams {
			t.Fatalf("err = %v, want the write error; InvalidParams blames the caller for the hub's failed write", err)
		}
		if !strings.Contains(err.Error(), "providers.toml: write:") {
			t.Fatalf("err = %v, want the write failure verbatim", err)
		}
	})
}

// TestInstances_EditRefusesAnUnreadableCredentialHeader: Edit holds the same
// invariant, on the field a shadowing entry can carry.
func TestInstances_EditRefusesAnUnreadableCredentialHeader(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk", "PORTKEY_KEY": "pk"})
	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name: "work", Base: "openai", CredentialHeader: "Authorization=Bearer $PORTKEY_KEY",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	before := authoredEntry(t, f.tomlPath, "work")

	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", Protocol: "responses"})
	if err == nil || !strings.Contains(err.Error(), "unknown protocol") {
		t.Fatalf("Edit = %v, want an unknown-protocol error", err)
	}
	if after := authoredEntry(t, f.tomlPath, "work"); after.Protocol != before.Protocol {
		t.Fatalf("a rejected edit changed the authored entry: %q → %q", before.Protocol, after.Protocol)
	}
	if f.ctl.reg.WritesRefused() {
		t.Fatal("the registry stopped loading after a rejected edit; the pane must not be locked out")
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", Protocol: "openai-responses"}); err != nil {
		t.Fatalf("the pane refused a valid edit after rejecting an invalid one: %v", err)
	}
}

// TestInstances_RefusesAVarsKeyThePlaceholderGrammarCannotName: a transport's
// {VAR} placeholders are uppercase names (llm/registry/load.go's
// placeholderRe), so a vars key in any other shape is one nothing will ever
// substitute. WriteConfigFile's dry parse checks the $ENV syntax in the
// values, not the shape of the keys, so a lowercase key was written and then
// silently ignored with no diagnostic anywhere.
func TestInstances_RefusesAVarsKeyThePlaceholderGrammarCannotName(t *testing.T) {
	for _, key := range []string{"region", "Region", "1REGION", "MY-REGION", ""} {
		t.Run("key="+key, func(t *testing.T) {
			f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
			err := f.ctl.Create(appwire.InstanceCreateParams{
				Name: "work", Base: "openai", Vars: map[string]string{key: "value"},
			})
			if err == nil {
				t.Fatalf("Create accepted the vars key %q, which no placeholder can name", key)
			}
			if !strings.Contains(err.Error(), "invalid variable name") {
				t.Fatalf("Create = %v, want the refusal to name the offending key", err)
			}
			if _, ok := readConfigProviders(t, f.tomlPath)["work"]; ok {
				t.Fatal("the refused instance was written anyway")
			}

			if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "ok", Base: "openai"}); err != nil {
				t.Fatalf("Create(ok): %v", err)
			}
			err = f.ctl.Edit(appwire.InstanceEditParams{Name: "ok", Vars: map[string]string{key: "value"}})
			if err == nil {
				t.Fatalf("Edit accepted the vars key %q", key)
			}
			if !strings.Contains(err.Error(), "invalid variable name") {
				t.Fatalf("Edit = %v, want the refusal to name the offending key", err)
			}
			// validVarNames is the same helper Create uses, and Create's
			// refusal is InvalidParams (#717/#748); Edit must classify it the
			// same way so a client can tell its own bad input from a hub fault.
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
				t.Fatalf("Edit = %v, want an InvalidParams wire error", err)
			}
			if got := readConfigProviders(t, f.tomlPath)["ok"].Transport.Vars; len(got) != 0 {
				t.Fatalf("the refused edit wrote vars %v", got)
			}
		})
	}
}

// TestInstances_AcceptsAPlaceholderShapedVarsKey is the other half: the shape
// the grammar does name goes through, on both mutators.
func TestInstances_AcceptsAPlaceholderShapedVarsKey(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name: "work", Base: "openai", Vars: map[string]string{"REGION": "us-east-1"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", Vars: map[string]string{"REGION_2": "eu-west-1"}}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	got := readConfigProviders(t, f.tomlPath)["work"].Transport.Vars
	if got["REGION"] != "us-east-1" || got["REGION_2"] != "eu-west-1" {
		t.Fatalf("vars = %v", got)
	}
}

// TestInstances_EditDeletesAVarThePlaceholderGrammarCannotName: an edit
// spells a delete as an empty value (appwire.InstanceEditParams), and the var
// most in need of deleting is a hand-authored one whose key no placeholder
// can name — the sheet renders a row for it, so holding the delete to the
// grammar leaves the user no way to remove it from this surface. A SET under
// the same key stays refused: that one lands in the file and is then silently
// ignored, which is what the grammar check is for.
func TestInstances_EditDeletesAVarThePlaceholderGrammarCannotName(t *testing.T) {
	dir, stateDir := t.TempDir(), t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	const content = `[providers.work]
base = "openai"
vars = { "bad-name" = "x", REGION = "us-east-1" }
`
	if err := os.WriteFile(tomlPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	ctl := newTestInstancesController(t, tomlPath, dir, stateDir, map[string]string{"GROQ_API_KEY": "gk"})
	if err := ctl.Edit(appwire.InstanceEditParams{Name: "work", Vars: map[string]string{"bad-name": ""}}); err != nil {
		t.Fatalf("Edit(delete bad-name): %v", err)
	}
	got := readConfigProviders(t, tomlPath)["work"].Transport.Vars
	if _, still := got["bad-name"]; still {
		t.Fatalf("the delete left the var behind: %v", got)
	}
	if got["REGION"] != "us-east-1" {
		t.Fatalf("the delete disturbed the vars beside it: %v", got)
	}
	err := ctl.Edit(appwire.InstanceEditParams{Name: "work", Vars: map[string]string{"bad-name": "y"}})
	if err == nil || !strings.Contains(err.Error(), "invalid variable name") {
		t.Fatalf("Edit(set bad-name) = %v, want the invalid-name refusal", err)
	}
	// Create has no delete: an empty value there is written under a key
	// nothing can substitute, so every key it carries is still held to the
	// grammar.
	err = ctl.Create(appwire.InstanceCreateParams{Name: "other", Base: "openai", Vars: map[string]string{"bad-name": ""}})
	if err == nil || !strings.Contains(err.Error(), "invalid variable name") {
		t.Fatalf("Create(empty bad-name) = %v, want the invalid-name refusal", err)
	}
}

// TestInstances_EditStoresAVarTrimmed: the delete marker is read off the
// TRIMMED value, so a value that survives that test has to be stored as the
// value that survived it — otherwise a stray space authors a variable holding
// one. base_url and api_key_env on the same request already store trimmed.
func TestInstances_EditStoresAVarTrimmed(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{
		Name: "work", Vars: map[string]string{"BASE_URL": "  https://x  "},
	}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if got := readConfigProviders(t, f.tomlPath)["work"].Transport.Vars["BASE_URL"]; got != "https://x" {
		t.Fatalf("BASE_URL = %q, want it stored trimmed", got)
	}
}

// TestInstances_CreateStoresAVarTrimmed: both authoring paths store the same
// value, so a var Create writes is trimmed the way Edit's is - and the way
// base_url and api_key_env on the same request already are.
func TestInstances_CreateStoresAVarTrimmed(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name: "work", Base: "openai", Vars: map[string]string{"BASE_URL": "  https://x  "},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := readConfigProviders(t, f.tomlPath)["work"].Transport.Vars["BASE_URL"]; got != "https://x" {
		t.Fatalf("BASE_URL = %q, want it stored trimmed", got)
	}
}

// readConfigProviders re-reads the authored providers.toml.
func readConfigProviders(t *testing.T, path string) map[string]registry.Provider {
	t.Helper()
	l, _, err := registry.ReadConfigFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return l.Providers
}

// TestInstances_ListReportsAuthoredCredentialFields: the sheet's form
// prefills api_key_env and the credential header from what the user
// authored, and only that — an implicit instance inherits both from the
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

// The wire carries a single api_key_env while the file may name several, so
// the entry reports the first (appwire.InstanceEntry) — the one a form that
// saves the field back would replace.
func TestInstances_ListReportsTheFirstAuthoredAPIKeyEnv(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	inst, ok := f.ctl.reg.Get().Instance("groq")
	if !ok {
		t.Fatal("the fixture registry has no groq instance")
	}
	got := f.ctl.entryFor(inst, &registry.Provider{APIKeyEnv: []string{"FIRST", "SECOND"}})
	if got.APIKeyEnv != "FIRST" {
		t.Fatalf("APIKeyEnv = %q, want FIRST", got.APIKeyEnv)
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
	// A literal only reaches credential_headers by hand-editing the file:
	// the loader accepts it, both authoring surfaces refuse it, and it must
	// never be broadcast to a client.
	if got := credentialHeaderField(map[string]string{"Authorization": "Bearer sk-literal"}); got != "" {
		t.Fatalf("a hand-authored literal reached the wire: %q", got)
	}
}

// TestInstances_ListOmitsALiteralStandingBesideAReference: the loader takes
// "Bearer supersecret $PORTKEY_KEY", where a secret made of letters alone
// reads as a second scheme word. The wire carries only what the authoring
// rule accepts, which is at most one scheme word ahead of the reference.
func TestInstances_ListOmitsALiteralStandingBesideAReference(t *testing.T) {
	dir, stateDir := t.TempDir(), t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	const content = `[providers.work]
base = "openai"
credential_headers = { "Authorization" = "Bearer supersecret $PORTKEY_KEY" }
`
	if err := os.WriteFile(tomlPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	ctl := newTestInstancesController(t, tomlPath, dir, stateDir, map[string]string{"PORTKEY_KEY": "pk"})
	if got := entry(t, ctl.List(), "work").CredentialHeader; got != "" {
		t.Fatalf("a hand-authored literal reached the wire: %q", got)
	}
}

// TestInstances_ListOmitsAHeaderWhoseNameIsNotAToken: the loader takes any
// name the TOML grammar spells, so a hand-authored name with a space or a
// CR/LF in it loads. Neither is a name a server would read, and prefilling
// one builds a form Edit refuses to save.
func TestInstances_ListOmitsAHeaderWhoseNameIsNotAToken(t *testing.T) {
	dir, stateDir := t.TempDir(), t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	const content = `[providers.spaced]
base = "openai"
credential_headers = { "Bad Name" = "Bearer $PORTKEY_KEY" }

[providers.forged]
base = "openai"
credential_headers = { "X-Api-Key\r\nX-Other: y" = "$PORTKEY_KEY" }

[providers.good]
base = "openai"
credential_headers = { "X-Api-Key" = "$PORTKEY_KEY" }
`
	if err := os.WriteFile(tomlPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	ctl := newTestInstancesController(t, tomlPath, dir, stateDir, map[string]string{"PORTKEY_KEY": "pk"})
	resp := ctl.List()
	for _, name := range []string{"spaced", "forged"} {
		if got := entry(t, resp, name).CredentialHeader; got != "" {
			t.Errorf("a hand-authored header name reached the wire: %s = %q", name, got)
		}
	}
	if got := entry(t, resp, "good").CredentialHeader; got != "X-Api-Key=$PORTKEY_KEY" {
		t.Errorf("good = %q, want the token-named header kept", got)
	}
}

// TestInstances_ListOmitsAnAPIKeyEnvThatIsNotAVariableName: api_key_env names
// an environment variable, and the loader takes any string the TOML grammar
// spells, so a key pasted into that field loads. Broadcasting it would hand
// the key to every client, and prefilling it builds a form Edit refuses.
func TestInstances_ListOmitsAnAPIKeyEnvThatIsNotAVariableName(t *testing.T) {
	dir, stateDir := t.TempDir(), t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	const content = `[providers.pasted]
base = "openai"
api_key_env = ["sk-live-abc"]

[providers.good]
base = "openai"
api_key_env = ["PORTKEY_KEY"]
`
	if err := os.WriteFile(tomlPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	ctl := newTestInstancesController(t, tomlPath, dir, stateDir, map[string]string{"PORTKEY_KEY": "pk"})
	resp := ctl.List()
	if got := entry(t, resp, "pasted").APIKeyEnv; got != "" {
		t.Errorf("a hand-authored key reached the wire: %q", got)
	}
	if got := entry(t, resp, "good").APIKeyEnv; got != "PORTKEY_KEY" {
		t.Errorf("good = %q, want the variable name kept", got)
	}
}

// TestInstances_EditRefusesAnAPIKeyEnvThatIsNotAVariableName: the authoring
// surface refuses what the wire would not carry, so a value the sheet cannot
// prefill is also one it cannot save.
func TestInstances_EditRefusesAnAPIKeyEnvThatIsNotAVariableName(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"PORTKEY_KEY": "pk"})
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", APIKeyEnv: "sk-live-abc"})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("Edit = %v, want an InvalidParams wire error", err)
	}
	if strings.Contains(err.Error(), "sk-live-abc") {
		t.Fatalf("the refusal echoed the value: %v", err)
	}
	if p := authoredEntry(t, f.tomlPath, "work"); len(p.APIKeyEnv) != 0 {
		t.Fatalf("a refused api_key_env was written: %v", p.APIKeyEnv)
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", APIKeyEnv: "PORTKEY_KEY"}); err != nil {
		t.Fatalf("Edit(a variable name) = %v, want it accepted", err)
	}
	if got := authoredEntry(t, f.tomlPath, "work").APIKeyEnv; !slices.Equal(got, []string{"PORTKEY_KEY"}) {
		t.Fatalf("authored api_key_env = %v", got)
	}
}

// The field the wire carries is exactly what registry.CheckCredentialHeaderValue
// accepts: the hub applies no rule of its own, so the sheet can save back
// every value it prefills.
func TestCredentialHeaderField_KeepsWhatTheAuthoringRuleAccepts(t *testing.T) {
	for _, value := range []string{"Bearer $PORTKEY_KEY", "$PORTKEY_KEY", "Bearer $A $B"} {
		got := credentialHeaderField(map[string]string{"Authorization": value})
		if got != "Authorization="+value {
			t.Errorf("credentialHeaderField(%q) = %q, want it kept", value, got)
		}
	}
	refused := []string{
		"Bearer supersecret $PORTKEY_KEY",
		"$PORTKEY_KEY trailing",
		"Bearer",
	}
	for _, value := range refused {
		if got := credentialHeaderField(map[string]string{"Authorization": value}); got != "" {
			t.Errorf("credentialHeaderField(%q) = %q, want it omitted", value, got)
		}
	}
}

// TestInstances_EditRefusesALiteralStandingBesideAReference: the authoring
// surface refuses what the wire would not carry, so a value the sheet cannot
// prefill is also one it cannot save.
func TestInstances_EditRefusesALiteralStandingBesideAReference(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"PORTKEY_KEY": "pk"})
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", CredentialHeader: "Authorization=Bearer supersecret $PORTKEY_KEY"})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("Edit = %v, want an InvalidParams wire error", err)
	}
	if p := authoredEntry(t, f.tomlPath, "work"); len(p.CredentialHeaders) != 0 {
		t.Fatalf("a refused header was written: %v", p.CredentialHeaders)
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", CredentialHeader: "Authorization=Bearer $PORTKEY_KEY"}); err != nil {
		t.Fatalf("Edit(a scheme word ahead of the reference) = %v, want it accepted", err)
	}
	if got := authoredEntry(t, f.tomlPath, "work").CredentialHeaders["Authorization"]; got != "Bearer $PORTKEY_KEY" {
		t.Fatalf("authored credential header = %q", got)
	}
}

// TestInstances_CreateRefusesALiteralStandingBesideAReference: Create shares
// the rule, so neither mutator authors a value the other would.
func TestInstances_CreateRefusesALiteralStandingBesideAReference(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"PORTKEY_KEY": "pk"})
	err := f.ctl.Create(appwire.InstanceCreateParams{
		Name: "work", Base: "openai", CredentialHeader: "Authorization=Bearer supersecret $PORTKEY_KEY",
	})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("Create = %v, want an InvalidParams wire error", err)
	}
	if l, exists, _ := registry.ReadConfigFile(f.tomlPath); exists {
		if _, authored := l.Providers["work"]; authored {
			t.Fatal("a refused create authored [providers.work]")
		}
	}
}

// TestInstances_EditRefusesACredentialHeaderNameThatIsNotAToken: the NAME
// half of the field is an HTTP header token too. A name no server could read
// is one the entry would carry uselessly, and a CR or LF in it would forge a
// second header at request time.
func TestInstances_EditRefusesACredentialHeaderNameThatIsNotAToken(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"PORTKEY_KEY": "pk"})
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, field := range []string{"Bad Name=Bearer $PORTKEY_KEY", "X-Api-Key\r\nX-Other: y=$PORTKEY_KEY"} {
		err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", CredentialHeader: field})
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Fatalf("Edit(%q) = %v, want an InvalidParams wire error", field, err)
		}
		if p := authoredEntry(t, f.tomlPath, "work"); len(p.CredentialHeaders) != 0 {
			t.Fatalf("a refused header was written: %v", p.CredentialHeaders)
		}
	}
}

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

// TestInstances_EditClearsACredentialHeaderOverAStaleValue: a clear wins over
// whatever value rides along with it, as it does for base URL, protocol,
// surface and api_key_env. The sheet sends the field it still has on screen
// beside the clear it just decided, and a value the request is throwing away
// must not get a vote — least of all the invalid one a user is clearing
// BECAUSE it is invalid.
func TestInstances_EditClearsACredentialHeaderOverAStaleValue(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"PORTKEY_KEY": "pk"})
	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name: "work", Base: "openai", CredentialHeader: "Authorization=Bearer $PORTKEY_KEY",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{
		Name: "work", ClearCredentialHeader: true, CredentialHeader: "Authorization=literal-secret",
	}); err != nil {
		t.Fatalf("Edit(clear over a stale value): %v", err)
	}
	if p := authoredEntry(t, f.tomlPath, "work"); len(p.CredentialHeaders) != 0 {
		t.Fatalf("after clearing: credential_headers %v", p.CredentialHeaders)
	}
	if e := entry(t, f.ctl.List(), "work"); e.CredentialHeader != "" {
		t.Fatalf("List after clearing = credentialHeader %q", e.CredentialHeader)
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
	e := entry(t, f.ctl.List(), "work")
	if e.Protocol == "" {
		t.Fatal("the resolved protocol must fall back to the base's, not vanish")
	}
	if e.Surface != "openai" {
		t.Fatalf("resolved surface = %q, want the base's", e.Surface)
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

func TestInstances_EditRenamesEntryDefaultStoredKeyAndOAuthRecord(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// The record has to be one LoadAuth accepts: moving it reads it back.
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "")); err != nil {
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
	moved, err := authopenai.LoadAuth(f.stateDir, "personal")
	if err != nil {
		t.Fatalf("OAuth record did not move: %v", err)
	}
	// The tokens carry the old name's marker, so they are what tells a moved
	// record apart from a freshly written one. Provider is the field that has
	// to change: it names the instance the record belongs to (both OAuth
	// completion paths set it), so it follows the instance to its new name.
	if moved.AccessToken != "access-work" {
		t.Fatalf("a different record sits under the new name: access token = %q", moved.AccessToken)
	}
	if moved.Provider != "personal" {
		t.Fatalf("the moved record still names the old instance: provider = %q", moved.Provider)
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

// breakCredentialWrites makes every later credentials.toml save fail for a
// real filesystem reason rather than a stubbed one: the store writes through
// <path>.tmp and renames, so a directory occupying that name refuses the open
// whoever the test runs as. What the rename does with that refusal is then
// the real store, the real move and the real providers.toml write.
func breakCredentialWrites(t *testing.T, credsPath string) {
	t.Helper()
	if err := os.Mkdir(credsPath+".tmp", 0o700); err != nil {
		t.Fatalf("blocking the credentials temp path: %v", err)
	}
}

// A store that refuses the write is the one failure that could lose a
// credential, and the move is one persist: the entry stays under the old name
// in memory and in the file, and the rename says what it left behind.
func TestInstances_EditRenameReportsAStoredKeyItCouldNotCopy(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	breakCredentialWrites(t, f.credsPath)

	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"})
	if err == nil || !strings.Contains(err.Error(), "stored key not copied") {
		t.Fatalf("Edit(rename) = %v, want the refused copy reported", err)
	}

	// The entry is renamed either way: moveCredentials runs once
	// providers.toml is written, so the file — the thing List answers from —
	// is already the new name.
	authoredEntry(t, f.tomlPath, "personal")
	l, _, err := registry.ReadConfigFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadConfigFile: %v", err)
	}
	if _, still := l.Providers["work"]; still {
		t.Fatal("[providers.work] survived the rename")
	}
	if v, _ := f.store.Get("work"); v != "sk-stored" {
		t.Fatalf("the stored key was lost rather than left behind: work = %q", v)
	}
	if v, _ := f.store.Get("personal"); v != "" {
		t.Fatalf("a key landed under the new name despite the refused write: %q", v)
	}
	// The hub answers status from the store's memory and reloads the registry
	// from the file, so the file has to say the same thing memory does.
	onDisk, err := credentials.LoadStore(f.credsPath)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if v, _ := onDisk.Get("work"); v != "sk-stored" {
		t.Fatalf("credentials.toml lost the key: work = %q", v)
	}
	if v, _ := onDisk.Get("personal"); v != "" {
		t.Fatalf("credentials.toml holds the new name despite the refused write: %q", v)
	}
	// Each half moves on its own, so a refused key move does not strand the
	// OAuth record under the old name.
	if _, err := authopenai.LoadAuth(f.stateDir, "personal"); err != nil {
		t.Fatalf("OAuth record did not move: %v", err)
	}
	if _, err := authopenai.LoadAuth(f.stateDir, "work"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("the old OAuth record was left behind (err = %v)", err)
	}
}

// What was left behind beats a failed reload: the rename is already on disk,
// and the next refresh reloads anyway, so the caller has to hear the thing
// only this call knows. moveCredentials reads the OAuth record right after
// moving the key, which is the one point a test can reach between the move
// and that reload, so the unreadable config is written from there.
func TestInstances_EditRenameReportsTheMoveFailureOverAFailedReload(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	breakCredentialWrites(t, f.credsPath)
	f.ctl.auth.loadAuth = func(string, string) (authopenai.AuthRecord, error) {
		if err := os.WriteFile(f.tomlPath, []byte("this is not toml\n"), 0o600); err != nil {
			t.Errorf("WriteFile: %v", err)
		}
		return authopenai.AuthRecord{}, authopenai.ErrAuthNotFound
	}

	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"})
	if err == nil || !strings.Contains(err.Error(), "stored key not copied") {
		t.Fatalf("Edit(rename) = %v, want the refused move, not the reload error", err)
	}
}

// A rename whose credentials moved cleanly and whose final reload failed is
// as persisted as one that succeeded outright: providers.toml and the
// credentials both carry the new name, and only the hub's own view is behind.
// So it is a renamePersistedError too, which is what has the handler announce
// it to the clients whose lists that file just made stale. The unreadable
// config is written from the loadAuth seam for the reason the sibling test
// above gives: it is the one point between the move and the reload a test can
// reach.
func TestInstances_EditRenameThatOnlyFailedItsReloadStillPersisted(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	f.ctl.auth.loadAuth = func(string, string) (authopenai.AuthRecord, error) {
		if err := os.WriteFile(f.tomlPath, []byte("this is not toml\n"), 0o600); err != nil {
			t.Errorf("WriteFile: %v", err)
		}
		return authopenai.AuthRecord{}, authopenai.ErrAuthNotFound
	}

	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"})
	if err == nil {
		t.Fatal("Edit(rename) = nil, want the failed reload reported")
	}
	if _, persisted := errors.AsType[renamePersistedError](err); !persisted {
		t.Fatalf("Edit(rename) = %v (%T), want a renamePersistedError so the rename is still broadcast", err, err)
	}
	// The move itself ran, which is what makes this the persisted case rather
	// than one with a credential left behind.
	if v, _ := f.store.Get("personal"); v != "sk-stored" {
		t.Fatalf("stored key did not move: personal = %q", v)
	}
}

// TestInstances_EditRenameWaitsForAnInFlightCredentialWrite pins the lock
// order the rename's atomicity rests on: it asks which credentials sit under
// the new name and then moves the old ones onto it, so a credential write
// that lands between those two steps is one the check never saw. Holding the
// read side is what an in-flight evener/auth/apiKey/set does, and the rename
// must wait for it.
func TestInstances_EditRenameWaitsForAnInFlightCredentialWrite(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	f.ctl.auth.credMu.RLock()
	done := make(chan error, 1)
	go func() {
		done <- f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"})
	}()
	select {
	case err := <-done:
		f.ctl.auth.credMu.RUnlock()
		t.Fatalf("the rename ran through a credential write still in flight (err = %v)", err)
	case <-time.After(100 * time.Millisecond):
	}
	f.ctl.auth.credMu.RUnlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Edit(rename): %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the rename never finished after the credential write released the lock")
	}
	if v, _ := f.store.Get("personal"); v != "sk-stored" {
		t.Fatalf("stored key did not move: personal = %q", v)
	}
}

// The other side of the same pin: a credential write waits for a rename that
// is mid-move, so it cannot store a key under a name the rename has already
// checked and is about to write over.
func TestAuth_ApiKeySetWaitsForARenameHoldingTheCredentialLock(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	f.ctl.auth.credMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := f.ctl.auth.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "work", Value: "sk-new"})
		done <- err
	}()
	select {
	case err := <-done:
		f.ctl.auth.credMu.Unlock()
		t.Fatalf("ApiKeySet wrote through a rename holding the credential lock (err = %v)", err)
	case <-time.After(100 * time.Millisecond):
	}
	f.ctl.auth.credMu.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ApiKeySet: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ApiKeySet never finished after the rename released the lock")
	}
	if v, _ := f.store.Get("work"); v != "sk-new" {
		t.Fatalf("stored key = %q, want the one ApiKeySet wrote", v)
	}
}

func TestInstances_EditRenameLeavesNoPhantomImplicitInstance(t *testing.T) {
	f := newInstancesFixture(t, nil)
	// A stored key is all the curated openai provider needs to resolve a
	// credential and exist as an implicit instance, and the registry only
	// looks that key up when it reloads.
	if err := f.store.Set("openai", "sk-x"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	// Editing the implicit instance authors the shadow entry that the rename
	// then re-keys, leaving the curated provider unshadowed again.
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "openai", BaseURL: "https://gw.example.test/v1", Protocol: "openai-chat"}); err != nil {
		t.Fatalf("Edit(shadow): %v", err)
	}

	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "openai", NewName: "work"}); err != nil {
		t.Fatalf("Edit(rename): %v", err)
	}

	for _, e := range f.ctl.List().Instances {
		if e.Name == "openai" {
			t.Errorf("the rename left a phantom openai instance: %+v", e)
		}
	}
	// A phantom also makes the name look taken, and the refusal returns
	// before any reload, so nothing would ever clear it.
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "openai"}); err != nil {
		t.Fatalf("renaming back was refused: %v", err)
	}
}

// TestInstances_EditRenameKeepsTheBaseAShadowInheritedByName: an authored
// entry named after a curated provider inherits that provider's protocol,
// surface and models by the name alone. The rename takes the name away, so
// the base goes into the file first and the effective configuration survives.
func TestInstances_EditRenameKeepsTheBaseAShadowInheritedByName(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.store.Set("openai", "sk-x"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	// The shadow carries a base URL and nothing else: everything it resolves
	// beyond that, it resolves by being named openai.
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "openai", BaseURL: "https://gw.example.test/v1"}); err != nil {
		t.Fatalf("Edit(shadow): %v", err)
	}
	before := entry(t, f.ctl.List(), "openai")

	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "openai", NewName: "work"}); err != nil {
		t.Fatalf("Edit(rename): %v", err)
	}

	if got := authoredEntry(t, f.tomlPath, "work").Base; got != "openai" {
		t.Fatalf("authored base = %q, want the base the shadow inherited by name", got)
	}
	if l, _, err := registry.ReadConfigFile(f.tomlPath); err == nil {
		if _, still := l.Providers["openai"]; still {
			t.Fatal("the rename left [providers.openai] behind")
		}
	}
	after := entry(t, f.ctl.List(), "work")
	if after.Protocol != before.Protocol || after.Surface != before.Surface {
		t.Fatalf("the rename changed the resolved config: protocol %q → %q, surface %q → %q",
			before.Protocol, after.Protocol, before.Surface, after.Surface)
	}
	if after.BaseURL != before.BaseURL {
		t.Fatalf("the rename changed the base URL: %q → %q", before.BaseURL, after.BaseURL)
	}
}

// The other half: an entry that authored its own base keeps it, even where a
// name match would have supplied a different one.
func TestInstances_EditRenameLeavesAnAuthoredBaseAlone(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "openai", Base: "anthropic"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "openai", NewName: "work"}); err != nil {
		t.Fatalf("Edit(rename): %v", err)
	}
	if got := authoredEntry(t, f.tomlPath, "work").Base; got != "anthropic" {
		t.Fatalf("authored base = %q, want anthropic", got)
	}
}

// TestInstances_EditRenameRefusesACuratedProviderIdForAStandaloneInstance is
// the mirror of the shadow rule above. A standalone entry — no base, and its
// own name is nobody's curated id — resolves everything from its own fields.
// Renamed onto a curated provider id it would carry no base to say so, and on
// the next load the new name alone would make it a shadow of that provider,
// silently changing its protocol, transport, models and credential
// resolution. Neither taken-name check sees it: a curated provider with no
// credential is not an instance.
func TestInstances_EditRenameRefusesACuratedProviderIdForAStandaloneInstance(t *testing.T) {
	dir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, `[providers.work]
protocol = "openai-chat"
base_url = "http://127.0.0.1:9/v1"
`)
	ctl := newTestInstancesController(t, tomlPath, dir, t.TempDir())

	err := ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "openai"})

	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("Edit = %v, want an InvalidParams wire error", err)
	}
	if !strings.Contains(err.Error(), "curated provider id") {
		t.Fatalf("Edit = %v, want the refusal to name what the new name would inherit", err)
	}
	p := authoredEntry(t, tomlPath, "work")
	if p.Transport.BaseURL != "http://127.0.0.1:9/v1" || p.Protocol != "openai-chat" {
		t.Fatalf("authored entry = %+v after a refused rename, want the original untouched", p)
	}
	l, _, readErr := registry.ReadConfigFile(tomlPath)
	if readErr != nil {
		t.Fatalf("ReadConfigFile: %v", readErr)
	}
	if _, moved := l.Providers["openai"]; moved {
		t.Fatal("a refused rename authored [providers.openai]")
	}
}

// The other half: an explicit base pins the inheritance whatever the entry is
// called, so the curated name is free to take.
func TestInstances_EditRenameOntoACuratedProviderIdKeepsAnExplicitBase(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "anthropic"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "openai"}); err != nil {
		t.Fatalf("Edit(rename): %v", err)
	}
	if got := authoredEntry(t, f.tomlPath, "openai").Base; got != "anthropic" {
		t.Fatalf("authored base = %q, want the base the rename must leave intact", got)
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

// A credential can outlive the instance it belonged to — providers.toml
// hand-edited while credentials.toml or the OAuth state kept its entry. Under a
// name the registry does not curate such an orphan resolves no instance at all,
// so neither taken-name check above sees it, and moveCredentials would
// overwrite it with nothing left to recover from: it runs once the file is
// re-keyed and the registry reloaded.
func TestInstances_EditRenameRefusesANameHoldingOrphanedCredentials(t *testing.T) {
	type check func(t *testing.T, f *instancesFixture)
	plantKey := func(t *testing.T, f *instancesFixture) {
		if err := f.store.Set("work2", "sk-orphan"); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	keyIntact := func(t *testing.T, f *instancesFixture) {
		if v, _ := f.store.Get("work2"); v != "sk-orphan" {
			t.Fatalf("the orphaned stored key was overwritten: work2 = %q", v)
		}
	}
	plantRecord := func(t *testing.T, f *instancesFixture) {
		if err := authopenai.SaveAuth(f.stateDir, "work2", makeOAuthRecord("work2", "")); err != nil {
			t.Fatalf("SaveAuth: %v", err)
		}
	}
	recordIntact := func(t *testing.T, f *instancesFixture) {
		orphan, err := authopenai.LoadAuth(f.stateDir, "work2")
		if err != nil {
			t.Fatalf("the orphaned OAuth record is gone: %v", err)
		}
		if orphan.AccessToken != "access-work2" {
			t.Fatalf("the orphaned OAuth record was overwritten: access token = %q", orphan.AccessToken)
		}
	}
	bothOf := func(a, b check) check {
		return func(t *testing.T, f *instancesFixture) {
			a(t, f)
			b(t, f)
		}
	}
	for _, tc := range []struct {
		kind   string
		plant  check
		named  []string
		intact check
	}{
		{
			kind:   "a stored key",
			plant:  plantKey,
			named:  []string{`credentials.toml entry for "work2"`},
			intact: keyIntact,
		},
		{
			kind:   "an OAuth record",
			plant:  plantRecord,
			named:  []string{`OAuth record for "work2"`},
			intact: recordIntact,
		},
		{
			// Hand-editing can leave both behind, and the refusal has to name
			// both: clearing one still loses the other.
			kind:   "both kinds at once",
			plant:  bothOf(plantKey, plantRecord),
			named:  []string{`credentials.toml entry for "work2"`, `OAuth record for "work2"`},
			intact: bothOf(keyIntact, recordIntact),
		},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			f := newInstancesFixture(t, nil)
			if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
				t.Fatalf("Create: %v", err)
			}
			if err := f.store.Set("work", "sk-work"); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "")); err != nil {
				t.Fatalf("SaveAuth: %v", err)
			}
			if err := f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "work"}); err != nil {
				t.Fatalf("SetDefault: %v", err)
			}
			tc.plant(t, f)
			// The reload is what would have made an orphan visible if it could
			// be: a curated provider picks a credential up and appears as an
			// implicit instance. work2 is not one, so nothing appears.
			if err := f.ctl.reg.Reload(); err != nil {
				t.Fatalf("Reload: %v", err)
			}
			for _, e := range f.ctl.List().Instances {
				if e.Name == "work2" {
					t.Fatalf("the planted credential resolved an instance, so this is not the orphan case: %+v", e)
				}
			}

			err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "work2"})
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
				t.Fatalf("Edit(rename) = %v, want a Conflict wire error", err)
			}
			for _, named := range tc.named {
				if !strings.Contains(err.Error(), named) {
					t.Fatalf("Edit(rename) = %v, want the leftover named as %s", err, named)
				}
			}

			// Refused before any write, so every piece of state the rename
			// would have touched is untouched.
			authoredEntry(t, f.tomlPath, "work")
			l, _, err := registry.ReadConfigFile(f.tomlPath)
			if err != nil {
				t.Fatalf("ReadConfigFile: %v", err)
			}
			if _, authored := l.Providers["work2"]; authored {
				t.Fatal("a refused rename authored [providers.work2]")
			}
			if l.Default != "work" {
				t.Fatalf("default = %q, want the pointer left on the old name", l.Default)
			}
			if v, _ := f.store.Get("work"); v != "sk-work" {
				t.Fatalf("the instance's stored key moved despite the refusal: work = %q", v)
			}
			if _, err := authopenai.LoadAuth(f.stateDir, "work"); err != nil {
				t.Fatalf("the instance's OAuth record moved despite the refusal: %v", err)
			}
			tc.intact(t, f)
		})
	}
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

// Decode the public JSON contract so missing additive fields fail at runtime,
// and catalogue tests also prove the metadata actually crosses appwire.
type providerSetupDescriptor struct {
	ID        string                 `json:"id"`
	AuthModes []string               `json:"authModes"`
	Setup     *appwire.InstanceEntry `json:"setup"`
}

func providerSetup(t *testing.T, list appwire.InstanceListResponse, id string) providerSetupDescriptor {
	t.Helper()
	var wire struct {
		AvailableProviders []providerSetupDescriptor `json:"availableProviders"`
	}
	if err := json.Unmarshal(mustMarshal(t, list), &wire); err != nil {
		t.Fatal(err)
	}
	for _, p := range wire.AvailableProviders {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("catalogue has no provider %q", id)
	return providerSetupDescriptor{}
}

func requireNoProviderSecrets(t *testing.T, value any, secrets ...string) {
	t.Helper()
	encoded := string(mustMarshal(t, value))
	for _, secret := range secrets {
		if strings.Contains(encoded, secret) {
			t.Fatal("wire response contains credential or endpoint secret sentinel")
		}
	}
}

// Dropping setup metadata, conflating discovery with Instances, or replacing
// transport-specific auth modes with a universal key form must fail here.
func TestProviderSetup_DiscoveryWithoutCredentials(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{})
	list := f.ctl.List()
	for _, tc := range []struct {
		id, auth, destination string
		modes                 []string
		hidden, member        bool
	}{
		{"anthropic", registry.AuthHeader, "https://api.anthropic.com/v1", []string{"apiKey"}, false, false},
		{"openai", registry.AuthBearer, "https://api.openai.com/v1", []string{"apiKey"}, false, false},
		{"openai-codex", registry.AuthOAuthOpenAICodex, "https://chatgpt.com/backend-api/codex", []string{"oauth"}, false, false},
		{"ollama", registry.AuthOptionalBearer, "http://localhost:11434/v1", []string{"none", "apiKey"}, false, true},
		{"google-vertex", registry.AuthGCPADC, "", []string{"adc", "credentialJson"}, true, false},
		{"openai-compatible", registry.AuthOptionalBearer, "", []string{"none", "apiKey"}, true, false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			p := providerSetup(t, list, tc.id)
			if !slices.Equal(p.AuthModes, tc.modes) {
				t.Errorf("auth modes=%v, want %v", p.AuthModes, tc.modes)
			}
			if p.Setup == nil {
				t.Fatal("missing discovery setup")
			}
			s := p.Setup
			if s.Name != tc.id || s.ProviderID != tc.id || s.Auth != tc.auth || !s.Implicit || s.ActiveSource != "none" || !slices.Equal(s.AuthModes, tc.modes) {
				t.Fatalf("incorrect setup identity/auth: %+v", s)
			}
			if s.BaseURL != tc.destination || s.Hidden != tc.hidden {
				t.Fatalf("destination=%q hidden=%v; want %q hidden=%v", s.BaseURL, s.Hidden, tc.destination, tc.hidden)
			}
			if s.CredentialRequired != (tc.auth != registry.AuthOptionalBearer) {
				t.Fatalf("credentialRequired=%v for auth %q", s.CredentialRequired, tc.auth)
			}
			member := slices.ContainsFunc(list.Instances, func(i appwire.InstanceEntry) bool { return i.Name == tc.id })
			if member != tc.member {
				t.Fatalf("launch-ready membership=%v, want %v", member, tc.member)
			}
		})
	}
	// Hidden implicit IDs are addressable discovery, not launch readiness.
	// Nonimplicit providers have no setup until an instance addresses that ID.
	for _, p := range list.AvailableProviders {
		s := providerSetup(t, list, p.ID)
		if !slices.Equal(s.AuthModes, authModesFor(p.Auth)) {
			t.Errorf("%s modes=%v do not match descriptor auth %q", p.ID, s.AuthModes, p.Auth)
		}
		if !p.Implicit && s.Setup != nil {
			t.Errorf("nonimplicit %s unexpectedly has setup", p.ID)
		}
	}
}

// Using the curated vendor record instead of the authored instance would
// expose the wrong destination, credential source, and credential edit fields.
func TestProviderSetup_AuthoredOverrideIsSafeResolvedView(t *testing.T) {
	const key = "fixture-inline-key-sentinel"
	const header = "fixture-header-secret-sentinel"
	const stored = "fixture-stored-secret-sentinel"
	const envKey = "fixture-env-secret-sentinel"
	const endpoint = "https://user-sentinel:password-sentinel@gateway.test/v1?token=query-sentinel#fragment-sentinel"
	f := newInstancesFixture(t, map[string]string{"ANTHROPIC_API_KEY": envKey})
	if err := f.store.Set("anthropic", stored); err != nil {
		t.Fatal(err)
	}
	if err := registry.WriteConfigFile(f.tomlPath, &registry.Layer{Providers: map[string]registry.Provider{
		"anthropic": {
			APIKey: key, APIKeyEnv: []string{"OVERRIDE_KEY"},
			Transport:         registry.Transport{BaseURL: endpoint},
			CredentialHeaders: map[string]string{"X-Credential": header},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatal(err)
	}
	list := f.ctl.List()
	p := providerSetup(t, list, "anthropic")
	if p.Setup == nil {
		t.Fatal("missing authored discovery setup")
	}
	actual := entry(t, list, "anthropic")
	if !reflect.DeepEqual(*p.Setup, actual) {
		t.Fatalf("setup differs from authored instance: setup=%+v instance=%+v", p.Setup, actual)
	}
	if p.Setup.BaseURL != "https://gateway.test/v1" || p.Setup.Implicit || p.Setup.APIKeyEnv != "OVERRIDE_KEY" || p.Setup.CredentialHeader != "" || p.Setup.ActiveSource != "api_key" {
		t.Fatalf("incorrect safe override metadata: %+v", p.Setup)
	}
	requireNoProviderSecrets(t, list, key, header, stored, envKey, "user-sentinel", "password-sentinel", "query-sentinel", "fragment-sentinel")
}

func TestProviderSetup_EnvironmentDestinationIsSanitized(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{
		"ANTHROPIC_BASE_URL": "https://user-sentinel:password-sentinel@proxy.test/v1?token=query-sentinel#fragment-sentinel",
	})
	list := f.ctl.List()
	p := providerSetup(t, list, "anthropic")
	if p.Setup == nil || p.Setup.BaseURL != "https://proxy.test/v1" || p.Setup.ActiveSource != "none" {
		t.Fatalf("setup does not reflect sanitized resolved environment destination: %+v", p.Setup)
	}
	requireNoProviderSecrets(t, list, "user-sentinel", "password-sentinel", "query-sentinel", "fragment-sentinel")
}
