package hub

// Tests for hubInstancesController Create/Edit/Remove/SetDefault/List on the
// provider registry.
//
// Each test owns its providers.toml, credentials.toml, OAuth state directory
// and environment, so nothing here reads the developer's machine.

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
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

// testProbeRegistryOptions is the registry composition the instances fixture
// and the credential-probe override share - everything except the config layer,
// which each caller picks (a path, or no user layer at all). Keeping the shared
// options in one place means a new option cannot land on only one of them and
// silently probe a different composition than the fixture builds.
func testProbeRegistryOptions(
	stateDir string,
	store *credentials.Store,
	env func(string) (string, bool),
) []registry.Option {
	return []registry.Option{
		registry.WithOffline(true),
		registry.WithoutCache(),
		registry.WithStateRoot(stateDir),
		registry.WithCredentials(cmdutil.StoreCredentialSource{Store: store}),
		registry.WithEnv(env),
	}
}

// instancesControllerHooks shapes a test instances controller's registry
// without re-implementing its wiring. seed runs once after the credential
// store is loaded and before the first registry load, so it can hand that load
// the store state it must read; load runs before every registry load, counted
// from 1 for the controller's own first load, and returns the error that load
// should fail with (or nil to load normally).
type instancesControllerHooks struct {
	seed func(*credentials.Store)
	load func(load int) error
}

// newTestInstancesController builds an instances controller whose registry
// reads tomlPath as its user layer, with credentials at credsDir and OAuth
// state at stateDir.
func newTestInstancesController(t *testing.T, tomlPath, credsDir, stateDir string, env map[string]string, hooks ...instancesControllerHooks) *hubInstancesController {
	t.Helper()
	store, err := credentials.LoadStore(filepath.Join(credsDir, "credentials.toml"))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	var hook instancesControllerHooks
	if len(hooks) > 0 {
		hook = hooks[0]
	}
	if hook.seed != nil {
		hook.seed(store)
	}
	lookup := env
	if lookup == nil {
		lookup = map[string]string{}
	}
	auth := newHubAuthControllerWithStore(credsDir, store)
	auth.stateDir = stateDir
	auth.providersConfigPath = tomlPath
	loads := 0
	auth.reg = hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		loads++
		if hook.load != nil {
			if err := hook.load(loads); err != nil {
				return nil, nil, err
			}
		}
		opts := append(
			testProbeRegistryOptions(stateDir, store, func(name string) (string, bool) {
				v, ok := lookup[name]
				return v, ok
			}),
			registry.WithConfigPath(tomlPath),
		)
		r, err := registry.Load(append(opts, extra...)...)
		return r, store, err
	})
	// A deliberately broken fixture must still produce a controller: the
	// refusal is what several tests are about.
	_ = auth.reg.Reload()
	return &hubInstancesController{reg: auth.reg, providersConfigPath: tomlPath, auth: auth}
}

// unkeyableStateRoot puts an obstacle where the fingerprint key file belongs: a
// non-empty directory, which cannot be read as a key, created around, or
// removed, so the hub has none and omits every fingerprint. An *empty*
// directory would not do - the key loader repairs a state root it can reach.
func unkeyableStateRoot(t *testing.T, stateDir string) {
	t.Helper()
	path := filepath.Join(stateDir, endpointFingerprintKeyFile)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir(%s): %v", path, err)
	}
	if err := os.WriteFile(filepath.Join(path, "obstacle"), []byte("in the way"), 0o600); err != nil {
		t.Fatalf("WriteFile(obstacle): %v", err)
	}
}

// pinEndpointFingerprintKey gives a state root a fixed fingerprint key, so a
// corpus that compares a committed file byte for byte has the same digests on
// every run. A state root without one gets its own key, created there and
// different from every other root's.
func pinEndpointFingerprintKey(t *testing.T, stateDir string) {
	t.Helper()
	path := filepath.Join(stateDir, endpointFingerprintKeyFile)
	if err := os.WriteFile(path, []byte("pinned-test-endpoint-fingerprint-key"), 0o600); err != nil {
		t.Fatalf("pin %s: %v", path, err)
	}
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

// newFlakyReloadFixture is newInstancesFixture whose registry loader fails for
// the loads its fail predicate names, counted from 1 for the fixture's own
// first load. It is what lets a test park the holder exactly where a failed
// reload leaves it: a config that cannot be read at the moment a mutation's
// reload runs, without the file having to be broken beforehand (which
// refuseWhenBroken would refuse the mutation over).
func newFlakyReloadFixture(t *testing.T, storedKeyFor string, fail func(load int) bool) *instancesFixture {
	t.Helper()
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	ctl := newTestInstancesController(t, tomlPath, dir, stateDir, nil, instancesControllerHooks{
		seed: func(store *credentials.Store) {
			if storedKeyFor == "" {
				return
			}
			if err := store.Set(storedKeyFor, "gk"); err != nil {
				t.Fatalf("Set(%s): %v", storedKeyFor, err)
			}
		},
		load: func(load int) error {
			if fail(load) {
				return errors.New("providers config could not be read")
			}
			return nil
		},
	})
	if err := ctl.reg.LoadError(); err != nil {
		t.Fatalf("initial Reload: %v", err)
	}
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

// TestInstances_UnreadableCredentialsStoreRefusesInsteadOfPanicking pins the
// instance-callers half of TestAuth_UnreadableCredentialsStoreRefusesInsteadOfPanicking:
// an unreadable credentials store is a first-class state (creds nil, the write
// closures replaced with refusals), but the instance rename and remove paths
// still dereferenced c.auth.creds directly. credentials.Store.Get takes its
// receiver's lock, so a nil store panicked inside the RPC handler - in exactly
// the configuration the series now tolerates. A rename or removal that cannot
// see the store must refuse typed, before it persists any config change, rather
// than panic or strand a credential under a name it just gave up.
func TestInstances_UnreadableCredentialsStoreRefusesInsteadOfPanicking(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	// The default store a nil CredsStore resolves to: credentials.toml beside
	// the state root (hubAuthCredentialsPath). The store refuses a file with
	// group or other bits set, which is how a real file becomes unreadable
	// without being absent.
	credsPath := filepath.Join(root, "credentials.toml")
	if err := os.WriteFile(credsPath, []byte("[providers]\n"), 0o600); err != nil {
		t.Fatalf("write credentials.toml: %v", err)
	}
	if err := os.Chmod(credsPath, 0o644); err != nil {
		t.Fatalf("chmod credentials.toml: %v", err)
	}
	if _, err := credentials.LoadStore(credsPath); err == nil {
		t.Fatal("LoadStore accepted the fixture, so this test is not exercising an unreadable store")
	}
	providersToml := writeProvidersToml(t, root, bearerInstanceToml)
	before, err := os.ReadFile(providersToml)
	if err != nil {
		t.Fatalf("read providers.toml: %v", err)
	}
	// The registry still resolves against a healthy store: only the auth
	// controller's own store is unreadable, which is the state the mutation
	// paths used to dereference.
	healthy, err := credentials.LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	auth := newHubAuthControllerWithStore(stateDir, nil)
	auth.stateDir = stateDir
	auth.providersConfigPath = providersToml
	auth.reg = newTestRegistry(t, stateDir, providersToml, healthy, nil)
	ctl := &hubInstancesController{reg: auth.reg, providersConfigPath: providersToml, auth: auth}

	// The read paths route through storedKey, so the listing builds rather than
	// panicking on a nil store.
	if row := entry(t, ctl.List(), "work-ant"); row.Name != "work-ant" {
		t.Fatalf("row = %+v, want the instance to still list", row)
	}

	// A rename refuses typed, naming the file the operator has to fix.
	err = ctl.Edit(appwire.InstanceEditParams{Name: "work-ant", NewName: "work-ant-renamed"})
	if err == nil {
		t.Fatal("Edit renamed an instance with no readable credentials store")
	}
	assertWireCode(t, err, appwire.CodeInternalError)
	if !strings.Contains(err.Error(), credsPath) {
		t.Fatalf("rename refusal = %v, want it to name %s", err, credsPath)
	}

	// A removal refuses the same way.
	if err := ctl.Remove(appwire.InstanceRemoveParams{Name: "work-ant"}); err == nil {
		t.Fatal("Remove removed an instance with no readable credentials store")
	} else {
		assertWireCode(t, err, appwire.CodeInternalError)
	}

	// Neither persisted anything: providers.toml is byte-for-byte what it was,
	// so no rename or removal reaches the file before the credential cleanup it
	// cannot complete.
	after, err := os.ReadFile(providersToml)
	if err != nil {
		t.Fatalf("read providers.toml after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("providers.toml changed under a refused rename/removal:\nbefore:\n%s\nafter:\n%s", before, after)
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
	ctl := newTestInstancesController(t, tomlPath, dir, t.TempDir(), nil)
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

// TestInstances_RemoveRefusesImplicitInstance: an environment-backed instance
// has no entry to delete and the variable that makes it exist would put it
// straight back, so the refusal says what to unset.
func TestInstances_RemoveRefusesImplicitInstance(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "groq"})
	if err == nil {
		t.Fatal("Remove accepted an implicit instance")
	}
	if !strings.Contains(err.Error(), "GROQ_API_KEY") {
		t.Fatalf("the refusal names the variable that creates the instance: %v", err)
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("Remove = %v, want an InvalidParams wire error", err)
	}
}

// listedInstance reports whether a listing still carries a row under name.
func listedInstance(resp appwire.InstanceListResponse, name string) bool {
	return slices.ContainsFunc(resp.Instances, func(e appwire.InstanceEntry) bool { return e.Name == name })
}

// seedOAuthRecord gives an instance a signed-in Codex account - the credential
// a user adds through the UI - and reloads so the registry derives the
// instance from it.
func seedOAuthRecord(t *testing.T, f *instancesFixture, name, email string) {
	t.Helper()
	if err := authopenai.SaveAuth(f.stateDir, name, makeOAuthRecord(name, email)); err != nil {
		t.Fatalf("SaveAuth(%s): %v", name, err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
}

// TestInstances_RemoveDeletesASignedInCodexAccount: the OAuth record is a file
// under the instance name, so the instance the user signed in to is theirs to
// remove - and removing it is what takes the account away.
func TestInstances_RemoveDeletesASignedInCodexAccount(t *testing.T) {
	f := newInstancesFixture(t, nil)
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")

	before := entry(t, f.ctl.List(), "openai-codex")
	if !before.Implicit || before.ActiveSource != "oauth" {
		t.Fatalf("fixture: openai-codex = %+v, want an implicit instance resolving the OAuth record", before)
	}

	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai-codex"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := authopenai.LoadAuth(f.stateDir, "openai-codex"); !errors.Is(err, authopenai.ErrAuthNotFound) {
		t.Fatalf("the OAuth record survived the removal (LoadAuth = %v)", err)
	}
	if listedInstance(f.ctl.List(), "openai-codex") {
		t.Fatal("openai-codex is still listed after its account was removed")
	}
}

// TestInstances_RemoveDeletesAStoredKeyForACuratedProvider: the other half of
// environmentBacked. A key the user pasted through the UI for a curated
// provider is theirs, so a removal deletes it and the row goes with it - the
// same no-authored-entry path the Codex account takes.
func TestInstances_RemoveDeletesAStoredKeyForACuratedProvider(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.store.Set("groq", "gk"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	before := entry(t, f.ctl.List(), "groq")
	if !before.Implicit || before.ActiveSource != "store" {
		t.Fatalf("fixture: groq = %+v, want an implicit instance resolving the stored key", before)
	}

	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "groq"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if v, _ := f.store.Get("groq"); v != "" {
		t.Fatalf("the stored key survived the removal: %q", v)
	}
	if listedInstance(f.ctl.List(), "groq") {
		t.Fatal("groq is still listed after its stored key was removed")
	}
}

// TestInstances_RemoveRefusesAKeylessInstanceWithAStoredKey: the keyless
// schemes are re-derived with or without a credential, so a removal would
// delete the key and leave the row standing - with the badge the affordance
// just said it did not have. The client offers no Remove for one, and the hub
// refuses it; clearing the credential is the action for that key.
func TestInstances_RemoveRefusesAKeylessInstanceWithAStoredKey(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.store.Set("ollama", "gk"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	before := entry(t, f.ctl.List(), "ollama")
	if !before.Implicit || before.ActiveSource != "store" || before.CredentialRequired {
		t.Fatalf("fixture: ollama = %+v, want an implicit keyless instance resolving the stored key", before)
	}

	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "ollama"}); err == nil {
		t.Fatal("Remove accepted an instance the reload would re-derive anyway")
	}
	if v, _ := f.store.Get("ollama"); v != "gk" {
		t.Fatalf("the refused removal deleted the stored key: %q", v)
	}
}

// TestInstances_RemoveLeavesTheEnvironmentRowWhenAVariableAlsoSuppliesIt: the
// stored key is what makes the instance the user's, so removing it takes that
// key - but the environment then supplies the instance again, and the row that
// comes back says so. The pane's own removal message reports the same thing.
func TestInstances_RemoveLeavesTheEnvironmentRowWhenAVariableAlsoSuppliesIt(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "env-key"})
	if err := f.store.Set("groq", "gk"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	if before := entry(t, f.ctl.List(), "groq"); before.ActiveSource != "store" {
		t.Fatalf("fixture: groq = %+v, want the stored key to outrank the variable", before)
	}

	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "groq"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	after := entry(t, f.ctl.List(), "groq")
	if after.ActiveSource != "env:GROQ_API_KEY" || !after.Implicit {
		t.Fatalf("groq = %+v, want the row the environment supplies back", after)
	}
}

// TestInstances_RemoveClearsADefaultNamingTheRemovedInstance: the default
// pointer is a change to a file that already exists, so it is written even
// when the instance itself had no authored entry to delete. Left behind, it
// would name an instance the next load cannot find.
func TestInstances_RemoveClearsADefaultNamingTheRemovedInstance(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := os.WriteFile(f.tomlPath, []byte("default = \"groq\"\n"), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	if err := f.store.Set("groq", "gk"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	if name, _, _ := f.ctl.reg.Get().DefaultInstance(); name != "groq" {
		t.Fatalf("fixture default = %q, want groq", name)
	}

	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "groq"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	l, _, err := registry.ReadConfigFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadConfigFile: %v", err)
	}
	if l.Default != "" {
		t.Fatalf("the file still defaults to the removed instance: %q", l.Default)
	}
	if listedInstance(f.ctl.List(), "groq") {
		t.Fatal("groq is still listed after its stored key was removed")
	}
}

// TestInstances_RemoveRetriesTheReloadWhenNothingWasWritten: a credential-only
// removal writes no file, so a reload that fails leaves the registry parked on
// the implicit-only view a failed load produces - writes refused, the row gone
// from listings - while the state the file describes never changed. Putting the
// credentials back makes a second attempt the recovery; one that fails too
// leaves the registry as unusable as any other failed load, and says so instead
// of reporting only a rolled-back removal.
func TestInstances_RemoveRetriesTheReloadWhenNothingWasWritten(t *testing.T) {
	for _, tt := range []struct {
		name    string
		fail    func(load int) bool
		wantErr string
	}{
		{name: "the retry brings the registry back", fail: func(load int) bool { return load == 2 }},
		{name: "the retry fails too", fail: func(load int) bool { return load >= 2 }, wantErr: "instance writes stay refused"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFlakyReloadFixture(t, "groq", tt.fail)
			if before := entry(t, f.ctl.List(), "groq"); before.ActiveSource != "store" {
				t.Fatalf("fixture: groq = %+v, want an implicit instance resolving the stored key", before)
			}

			err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "groq"})
			if err == nil || !strings.Contains(err.Error(), "was rolled back") {
				t.Fatalf("Remove = %v, want the removal reported as rolled back", err)
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Remove = %v, want it to name the registry %q", err, tt.wantErr)
			}
			// Either way the credential this call deleted is back, because a
			// rollback that dropped it would leave the instance unauthenticated.
			if v, _ := f.store.Get("groq"); v != "gk" {
				t.Fatalf("the stored key was not restored: %q", v)
			}
			if tt.wantErr == "" {
				if f.ctl.reg.WritesRefused() {
					t.Fatalf("the registry stayed refused after the retry: %v", f.ctl.reg.LoadError())
				}
				if before := entry(t, f.ctl.List(), "groq"); before.ActiveSource != "store" {
					t.Fatalf("groq = %+v, want the instance back on its stored key", before)
				}
			}
		})
	}
}

// TestInstances_EditRenamesASignedInCodexAccount: the rename authors an entry
// under the new name and moves the OAuth record with it, so the account keeps
// working under the new name and the old row is gone.
func TestInstances_EditRenamesASignedInCodexAccount(t *testing.T) {
	f := newInstancesFixture(t, nil)
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")

	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "openai-codex", NewName: "codex-work"}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	p := authoredEntry(t, f.tomlPath, "codex-work")
	if p.Base != "openai-codex" {
		t.Fatalf("authored base = %q, want the provider the account was signed in to", p.Base)
	}
	if _, err := authopenai.LoadAuth(f.stateDir, "codex-work"); err != nil {
		t.Fatalf("the OAuth record must move with the rename: %v", err)
	}
	resp := f.ctl.List()
	if listedInstance(resp, "openai-codex") {
		t.Fatal("the old row must be gone once its record moved")
	}
	got := entry(t, resp, "codex-work")
	if got.Implicit || got.ActiveSource != "oauth" {
		t.Fatalf("codex-work = %+v, want an authored instance resolving the moved record", got)
	}
}

// TestInstances_EditRenameKeepsTheRankedDefault: an unqualified launch resolves
// the default instance by ranking when providers.toml names none, so renaming
// the instance that wins that ranking has to carry the default with it. The
// rename authors an entry under a name outside default_order - it ranks after
// every curated id - so without pinning the pointer the next bare launch would
// resolve whatever instance ranked behind it (here a configured Groq key).
func TestInstances_EditRenameKeepsTheRankedDefault(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.store.Set("groq", "gk"); err != nil {
		t.Fatalf("Set(groq): %v", err)
	}
	seedOAuthRecord(t, f, "openai-codex", "codex@example.com")

	before := f.ctl.List()
	if got := entry(t, before, "openai-codex"); !got.IsDefault {
		t.Fatalf("fixture: openai-codex = %+v, want the instance ranking as default", got)
	}
	if got := entry(t, before, "groq"); got.IsDefault {
		t.Fatalf("fixture: groq = %+v, want the ranking to pick openai-codex", got)
	}

	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "openai-codex", NewName: "codex-work"}); err != nil {
		t.Fatalf("Edit: %v", err)
	}

	after := f.ctl.List()
	if got := entry(t, after, "codex-work"); !got.IsDefault {
		t.Fatalf("codex-work = %+v, want the default the rename carries over", got)
	}
	if got := entry(t, after, "groq"); got.IsDefault {
		t.Fatalf("groq = %+v, want the renamed instance to keep the default", got)
	}
}

// TestInstances_RemoveReportsAStandingRemovalWhenTheRestoreFails: the
// credential-only rollback is the only thing a removal of a UI-credentialed
// instance can undo, so when a put-back fails the removal stands. The caller
// must hear that - and the layer that could not be restored - rather than that
// the removal "was rolled back", and the reload must not run: it would publish
// a listing without the row while the caller was told an instance still had it.
func TestInstances_RemoveReportsAStandingRemovalWhenTheRestoreFails(t *testing.T) {
	f := newFlakyReloadFixture(t, "groq", func(load int) bool { return load >= 2 })
	if before := entry(t, f.ctl.List(), "groq"); before.ActiveSource != "store" {
		t.Fatalf("fixture: groq = %+v, want an implicit instance resolving the stored key", before)
	}
	// The put-back the rollback performs cannot land.
	f.ctl.auth.setCredential = func(string, string) error {
		return errors.New("credentials.toml: write: read-only")
	}

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "groq"})
	if err == nil {
		t.Fatal("Remove = nil, want the failure")
	}
	if strings.Contains(err.Error(), "was rolled back") {
		t.Fatalf("Remove = %v, must not claim the removal was rolled back when its credential could not be restored", err)
	}
	if !strings.Contains(err.Error(), "the removal stands") {
		t.Fatalf("Remove = %v, want the standing removal named", err)
	}
	if !strings.Contains(err.Error(), "stored key could not be restored") {
		t.Fatalf("Remove = %v, want the unrestored layer named", err)
	}
	// The key really is gone: an honest "stands" report matches the disk.
	if v, _ := f.store.Get("groq"); v != "" {
		t.Fatalf("the stored key = %q, want it gone with the standing removal", v)
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

// A removal that cannot clean up the credentials filed under the name must not
// report success: a stored key or OAuth record left behind sits under a name
// nothing curates, and the next instance to hold that name inherits it. The
// failed cleanup also has to leave the removal itself undone, so the caller
// can retry it rather than being told a deletion happened that did not.
func TestInstances_RemoveFailsWhenTheStoredCredentialCannotBeCleared(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	f.ctl.auth.clearCredential = func(string) error { return errors.New("clear refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})

	if err == nil || !strings.Contains(err.Error(), "clear refused") {
		t.Fatalf("Remove = %v, want the stored-key cleanup failure", err)
	}
	if v, _ := f.store.Get("work"); v != "sk-stored" {
		t.Fatalf("stored key = %q, want it retained by the failed removal", v)
	}
	l, _, readErr := registry.ReadConfigFile(f.tomlPath)
	if readErr != nil {
		t.Fatalf("ReadConfigFile: %v", readErr)
	}
	if _, still := l.Providers["work"]; !still {
		t.Fatal("[providers.work] was removed even though its credential could not be cleared")
	}
	if _, ok := f.ctl.reg.Get().Instance("work"); !ok {
		t.Fatal("the registry no longer resolves work after a failed removal")
	}
}

func TestInstances_RemoveFailsWhenTheOAuthRecordCannotBeDeleted(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	f.ctl.auth.deleteAuth = func(string, string) (bool, error) { return false, errors.New("delete refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})

	if err == nil || !strings.Contains(err.Error(), "delete refused") {
		t.Fatalf("Remove = %v, want the OAuth-record cleanup failure", err)
	}
	if _, loadErr := authopenai.LoadAuth(f.stateDir, "work"); loadErr != nil {
		t.Fatalf("the OAuth record did not survive the failed removal: %v", loadErr)
	}
	l, _, readErr := registry.ReadConfigFile(f.tomlPath)
	if readErr != nil {
		t.Fatalf("ReadConfigFile: %v", readErr)
	}
	if _, still := l.Providers["work"]; !still {
		t.Fatal("[providers.work] was removed even though its OAuth record could not be deleted")
	}
}

// The other side of the same rule: a removal that fails before it can write
// the config must not have deleted anything. The instance is still authored,
// so it still resolves, and its credential has to still be there - a caller
// told the removal failed would otherwise be holding an instance that quietly
// lost its key.
func TestInstances_RemoveKeepsCredentialsWhenTheConfigCannotBeRead(t *testing.T) {
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
	if err := os.WriteFile(f.tomlPath, []byte("this is not toml\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})

	if err == nil {
		t.Fatal("Remove = nil, want the config read failure")
	}
	if v, _ := f.store.Get("work"); v != "sk-stored" {
		t.Fatalf("the failed removal deleted the stored key: %q", v)
	}
	if _, loadErr := authopenai.LoadAuth(f.stateDir, "work"); loadErr != nil {
		t.Fatalf("the failed removal deleted the OAuth record: %v", loadErr)
	}
}

// TestInstances_RemoveRestoresCredentialsWhenTheConfigWriteFails: the write
// happens after the cleanup, so a failure there would otherwise leave the
// instance authored with its credential already durable-deleted. The path is
// swapped for a directory from inside a cleanup seam, which is what makes the
// rename-based write fail without depending on permissions or uid; it stands
// in for any write that cannot land (a full disk, a read-only config root).
func TestInstances_RemoveRestoresCredentialsWhenTheConfigWriteFails(t *testing.T) {
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
	originalDelete := f.ctl.auth.deleteAuth
	f.ctl.auth.deleteAuth = func(dir, name string) (bool, error) {
		if err := os.Remove(f.tomlPath); err != nil {
			t.Errorf("Remove(%s): %v", f.tomlPath, err)
		}
		if err := os.Mkdir(f.tomlPath, 0o700); err != nil {
			t.Errorf("Mkdir(%s): %v", f.tomlPath, err)
		}
		return originalDelete(dir, name)
	}

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})

	if err == nil {
		t.Fatal("Remove = nil, want the config write failure")
	}
	if v, _ := f.store.Get("work"); v != "sk-stored" {
		t.Fatalf("stored key = %q, want the failed removal to have restored it", v)
	}
	if _, loadErr := authopenai.LoadAuth(f.stateDir, "work"); loadErr != nil {
		t.Fatalf("the OAuth record was not restored: %v", loadErr)
	}
	if _, ok := f.ctl.reg.Get().Instance("work"); !ok {
		t.Fatal("the instance left the registry even though the removal failed")
	}
}

// The cleanup is two destructive steps, so its own failure has the same
// asymmetry the config write has: the stored key is deleted before the OAuth
// record is even attempted, and a failure on the second step leaves the
// instance authored with the key already durable-gone. A retry cannot recover
// it - creds.Get is empty by then - so the failed removal has to put it back.
func TestInstances_RemoveRestoresTheStoredKeyWhenTheOAuthRecordCannotBeDeleted(t *testing.T) {
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
	f.ctl.auth.deleteAuth = func(string, string) (bool, error) { return false, errors.New("delete refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})

	if err == nil || !strings.Contains(err.Error(), "delete refused") {
		t.Fatalf("Remove = %v, want the OAuth-record cleanup failure", err)
	}
	if v, _ := f.store.Get("work"); v != "sk-stored" {
		t.Fatalf("stored key = %q, want the failed removal to have restored it", v)
	}
	if _, loadErr := authopenai.LoadAuth(f.stateDir, "work"); loadErr != nil {
		t.Fatalf("the OAuth record did not survive the failed removal: %v", loadErr)
	}
	if _, ok := f.ctl.reg.Get().Instance("work"); !ok {
		t.Fatal("the instance left the registry even though the removal failed")
	}
}

// The destructive confirmations carry the endpoint assertion the credential
// writes do, and for the same reason: the user confirmed an action on the row
// the pane listed, so a name another client has re-pointed since must not have
// its replacement instance removed or its replacement's key cleared. A stale
// assertion is refused and nothing moves; the current one acts.
func TestInstances_DestructiveConfirmationsCarryTheEndpoint(t *testing.T) {
	for _, tt := range []struct {
		name string
		act  func(f *instancesFixture, fingerprint string) error
	}{
		{"remove", func(f *instancesFixture, fingerprint string) error {
			return f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work", ExpectedEndpointFingerprint: fingerprint})
		}},
		{"clear the stored key", func(f *instancesFixture, fingerprint string) error {
			_, err := f.ctl.auth.ApiKeyClear(appwire.AuthApiKeyClearParams{Provider: "work", ExpectedEndpointFingerprint: fingerprint})
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newInstancesFixture(t, nil)
			if err := f.ctl.Create(appwire.InstanceCreateParams{
				Name:    "work",
				Base:    "openai",
				BaseURL: "https://a.example.test/v1",
			}); err != nil {
				t.Fatalf("Create: %v", err)
			}
			if err := f.store.Set("work", "sk-keep"); err != nil {
				t.Fatalf("Set: %v", err)
			}
			stale := f.ctl.auth.endpointFingerprintFor("work")
			if stale == "" {
				t.Fatal("fixture drift: the endpoint must be fingerprintable here")
			}
			// What another client does while the confirmation is open.
			if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", BaseURL: "https://b.example.test/v1"}); err != nil {
				t.Fatalf("Edit: %v", err)
			}
			current := f.ctl.auth.endpointFingerprintFor("work")
			if current == "" || current == stale {
				t.Fatalf("fixture drift: the edit must move the endpoint (stale=%q current=%q)", stale, current)
			}

			if err := tt.act(f, stale); err == nil {
				t.Fatal("the action landed for a confirmation given against a different endpoint")
			}
			if v, ok := f.store.Get("work"); !ok || v != "sk-keep" {
				t.Fatalf("stored key = %q/%v, want it untouched by the refused action", v, ok)
			}
			if _, still := f.ctl.reg.Get().Instance("work"); !still {
				t.Fatal("the refused action removed the instance anyway")
			}

			if err := tt.act(f, current); err != nil {
				t.Fatalf("action for the endpoint the name resolves to now: %v", err)
			}
		})
	}
}

// An edit is applied to the row the client listed, so the endpoint that row was
// served with travels with the save. A name another client has re-pointed since
// - or replaced with a different instance - must not have its replacement
// edited or renamed. A stale assertion is refused and changes nothing, the
// endpoint the name resolves to now applies, and an empty assertion is the
// pre-existing contract and is skipped.
func TestInstances_EditRefusesAnEndpointItsCallerDidNotSee(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name:    "work",
		Base:    "openai",
		BaseURL: "https://a.example.test/v1",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	stale := f.ctl.auth.endpointFingerprintFor("work")
	if stale == "" {
		t.Fatal("fixture drift: the endpoint must be fingerprintable here")
	}
	// What another client does while the form is open: the name now resolves to
	// a different endpoint.
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", BaseURL: "https://b.example.test/v1"}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	current := f.ctl.auth.endpointFingerprintFor("work")
	if current == "" || current == stale {
		t.Fatalf("fixture drift: the edit must move the endpoint (stale=%q current=%q)", stale, current)
	}

	// A rename carrying the stale assertion is refused, and the name does not
	// move onto the replacement.
	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal", ExpectedEndpointFingerprint: stale})
	if err == nil {
		t.Fatal("Edit renamed the replacement for a form opened on a different endpoint")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("Edit = %v, want a conflict wire error", err)
	}
	if _, ok := f.ctl.reg.Get().Instance("personal"); ok {
		t.Fatal("the refused rename landed anyway")
	}

	// A field edit carrying the stale assertion is refused, and the field is
	// untouched.
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", APIKeyEnv: "PORTKEY_KEY", ExpectedEndpointFingerprint: stale}); err == nil {
		t.Fatal("Edit applied a field change for a form opened on a different endpoint")
	}
	if got := entry(t, f.ctl.List(), "work"); got.APIKeyEnv != "" {
		t.Fatalf("apiKeyEnv = %q, want the refused edit to change nothing", got.APIKeyEnv)
	}

	// The endpoint the name resolves to now is accepted.
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", APIKeyEnv: "PORTKEY_KEY", ExpectedEndpointFingerprint: current}); err != nil {
		t.Fatalf("Edit with the endpoint the name resolves to now: %v", err)
	}
	if got := entry(t, f.ctl.List(), "work"); got.APIKeyEnv != "PORTKEY_KEY" {
		t.Fatalf("apiKeyEnv = %q, want the matching edit to land", got.APIKeyEnv)
	}

	// An empty assertion asserts nothing (the pre-existing contract) and is
	// skipped rather than refused.
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", Protocol: "openai-chat"}); err != nil {
		t.Fatalf("Edit with no assertion: %v", err)
	}
	if got := entry(t, f.ctl.List(), "work"); got.Protocol != "openai-chat" {
		t.Fatalf("protocol = %q, want the unasserted edit to land", got.Protocol)
	}
}

// unwritableCredentialsPath puts a directory where credentials.toml belongs, so
// the store's next persist cannot land: the shape of a credentials path that is
// gone, read-only, or on a filesystem that has stopped taking writes.
func unwritableCredentialsPath(t *testing.T, credsPath string) {
	t.Helper()
	if err := os.RemoveAll(credsPath); err != nil {
		t.Fatalf("RemoveAll(%s): %v", credsPath, err)
	}
	if err := os.Mkdir(credsPath, 0o700); err != nil {
		t.Fatalf("Mkdir(%s): %v", credsPath, err)
	}
	if err := os.WriteFile(filepath.Join(credsPath, "obstacle"), []byte("in the way"), 0o600); err != nil {
		t.Fatalf("WriteFile(obstacle): %v", err)
	}
}

// A removal with no credential to clear must not depend on the credentials
// path: Store.Clear persists the file it holds, so clearing an entry that was
// never there is a rewrite of state the instance does not have, and on an
// unwritable path that rewrite fails a removal for nothing. The control in the
// same test keeps the injection honest: a removal that does have a key to clear
// still fails, which is what the store's persist failing there means.
func TestInstances_RemoveDoesNotNeedACredentialsPathWithNothingToClear(t *testing.T) {
	f := newInstancesFixture(t, nil)
	for _, name := range []string{"work", "work2"} {
		if err := f.ctl.Create(appwire.InstanceCreateParams{Name: name, Base: "openai"}); err != nil {
			t.Fatalf("Create(%s): %v", name, err)
		}
	}
	// Stored while the path still works, so the control has something to clear.
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	unwritableCredentialsPath(t, f.credsPath)

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})
	if err == nil || !strings.Contains(err.Error(), "clear stored credential") {
		t.Fatalf("Remove(work) = %v, want the store's failure for the key it has to clear", err)
	}
	if _, still := f.ctl.reg.Get().Instance("work"); !still {
		t.Fatal("the refused removal lost the instance it could not clean up")
	}

	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work2"}); err != nil {
		t.Fatalf("Remove(work2) = %v, want a removal that has no credential to clear", err)
	}
	if _, still := f.ctl.reg.Get().Instance("work2"); still {
		t.Fatal("the removed instance still resolves")
	}
}

// An OAuth record the hub cannot parse is still one DeleteAuth deletes by
// path, so a rollback that re-encoded a parsed record could not put it back.
// The capture is the file's bytes, which is what makes this case restorable.
func TestInstances_RemoveRestoresACorruptOAuthRecordWhenTheConfigWriteFails(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	authPath := authopenai.AuthFilePath(f.stateDir, "work")
	corrupt := []byte("this is not an auth record\n")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(authPath, corrupt, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	originalDelete := f.ctl.auth.deleteAuth
	f.ctl.auth.deleteAuth = func(dir, name string) (bool, error) {
		if err := os.Remove(f.tomlPath); err != nil {
			t.Errorf("Remove(%s): %v", f.tomlPath, err)
		}
		if err := os.Mkdir(f.tomlPath, 0o700); err != nil {
			t.Errorf("Mkdir(%s): %v", f.tomlPath, err)
		}
		return originalDelete(dir, name)
	}

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})

	if err == nil {
		t.Fatal("Remove = nil, want the config write failure")
	}
	restored, readErr := os.ReadFile(authPath)
	if readErr != nil {
		t.Fatalf("the corrupt OAuth record was not restored: %v", readErr)
	}
	if !bytes.Equal(restored, corrupt) {
		t.Fatalf("OAuth record bytes = %q, want the original %q", restored, corrupt)
	}
}

// A create whose config parses but cannot resolve is the hazard Edit's and
// Remove's rollbacks exist for, one step earlier: the entry it just wrote would
// stay in providers.toml while the registry sits on the implicit-only fallback
// and refuses every instance write, leaving hand-editing the file as the only
// way back. The create restores the file and reports what it could not load.
func TestInstances_CreateRollsBackWhenTheReloadFails(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	before, err := os.ReadFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// An entry that parses but cannot resolve an endpoint (#711: no base and no
	// base_url of its own): the registry loaded before it appeared, so the
	// create still starts, and the layer the create writes still carries it, so
	// the reload that follows fails.
	raw := append(slices.Clone(before), []byte("\n[providers.standalone]\nprotocol = \"openai-chat\"\n")...)
	if err := os.WriteFile(f.tomlPath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err = f.ctl.Create(appwire.InstanceCreateParams{Name: "second", Base: "openai"})
	if err == nil {
		t.Fatal("Create = nil, want the reload failure")
	}
	// Pins the branch: a write-loadable refusal never carries this text, so a
	// fixture that stopped parsing before the write would not pass as a
	// reload-rollback test.
	if !strings.Contains(err.Error(), "cannot be loaded") {
		t.Fatalf("Create = %v, want the create to name the config it could not load", err)
	}
	// The rollback re-serializes the layer, so the check is what the file holds,
	// not its bytes: the entry this create wrote must be gone and everything it
	// found must still be there.
	after, _, err := registry.ReadConfigFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadConfigFile after the rollback: %v", err)
	}
	if _, ok := after.Providers["second"]; ok {
		t.Fatal("the instance whose config cannot load is still in providers.toml")
	}
	for _, name := range []string{"work", "standalone"} {
		if _, ok := after.Providers[name]; !ok {
			t.Fatalf("the rollback lost the %q entry this create found", name)
		}
	}
	if _, ok := f.ctl.reg.Get().Instance("second"); ok {
		t.Fatal("the instance whose config cannot load still resolves")
	}
}

// A reload failure is the last way a removal can fail after it has deleted
// things. It drops the registry to implicit-only and refuses every instance
// write until the file loads again, so leaving the removal in place would have
// the file, the hub's view and every client's listing disagreeing about an
// instance only some of them still have - with nothing but hand-editing the
// file to get back. The removal rolls back instead.
func TestInstances_RemoveRollsBackWhenTheReloadFails(t *testing.T) {
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
	// An entry that parses but cannot resolve an endpoint (#711: no base and
	// no base_url of its own): the registry loaded before it appeared, so the
	// removal still starts, and the layer the removal writes still carries it,
	// so the reload that follows fails.
	raw, err := os.ReadFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	raw = append(raw, []byte("\n[providers.standalone]\nprotocol = \"openai-chat\"\n")...)
	if err := os.WriteFile(f.tomlPath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err = f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})

	if err == nil {
		t.Fatal("Remove = nil, want the reload failure")
	}
	// Pins the branch: a write-loadable refusal never carries this text, so a
	// fixture that stopped parsing before the write would not pass as a
	// reload-rollback test.
	if !strings.Contains(err.Error(), "was rolled back") {
		t.Fatalf("Remove = %v, want the reload rollback", err)
	}
	// The file the rollback put back is the same unresolvable one, so the reload
	// this branch runs fails too and the registry stays on the implicit-only
	// view a failed load leaves: instance writes are refused until the file
	// loads (registry.go's WritesRefused, spec §10), and the pane's diagnostics
	// already say so. That is the honest reading of a config that cannot be
	// loaded - reinstating the registry's previous view would have the hub serve
	// and rewrite a file it cannot read - but the failure has to say it: a
	// caller told only that the removal "was rolled back" would read the hub as
	// healthy.
	if !strings.Contains(err.Error(), "does not load either") {
		t.Fatalf("Remove = %v, want the rollback to name the config it could not load", err)
	}
	if !f.ctl.reg.WritesRefused() {
		t.Fatal("the registry accepts instance writes over a config it cannot load")
	}
	l, _, readErr := registry.ReadConfigFile(f.tomlPath)
	if readErr != nil {
		t.Fatalf("ReadConfigFile: %v", readErr)
	}
	if _, still := l.Providers["work"]; !still {
		t.Fatal("[providers.work] was not restored by the rollback")
	}
	if v, _ := f.store.Get("work"); v != "sk-stored" {
		t.Fatalf("stored key = %q, want the rollback to have restored it", v)
	}
	if _, loadErr := authopenai.LoadAuth(f.stateDir, "work"); loadErr != nil {
		t.Fatalf("the OAuth record was not restored by the rollback: %v", loadErr)
	}
}

// An implicit instance the config only points at through `default` still has
// configChanged on removal - clearing the pointer is a real change to the file -
// but the pointer is not what carries the instance: its stored key is. When the
// removal's reload fails, the config rollback restores the pointer, and the key
// restore also fails, the instance no longer resolves, so the removal stands.
// supplyAny would report it as "still configured" here; supplyOf(locked) names
// the carrying key, so the frame and the discriminator instanceRemoveError reads
// both say the removal applied.
func TestInstances_RemoveStandsWhenAnImplicitDefaultLosesItsKeyOnRollback(t *testing.T) {
	// Load 1 is the fixture's own, load 2 this test's SetDefault, load 3 the
	// removal's - the one made to fail.
	f := newFlakyReloadFixture(t, "groq", func(load int) bool { return load == 3 })
	if err := f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "groq"}); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	if before := entry(t, f.ctl.List(), "groq"); !before.IsDefault || before.ActiveSource != "store" {
		t.Fatalf("fixture: groq = %+v, want an implicit stored-key instance that is the default", before)
	}
	// The key that carries the instance cannot be put back after the cleanup
	// deleted it.
	f.ctl.auth.setCredential = func(string, string) error { return errors.New("restore refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "groq"})
	if err == nil {
		t.Fatal("Remove = nil, want the failed restore reported")
	}
	if !strings.Contains(err.Error(), "the removal stands") {
		t.Fatalf("Remove = %v, want the standing frame: the carrying key is gone", err)
	}
	// The removal stood, so the one sentence must not also claim it was rolled
	// back: the user reads this text verbatim as the TUI notice reason and the
	// web toast, and two opposite outcomes in it leave them unable to tell
	// whether to retry. The rolled-back wording belongs to the branch where the
	// carrying layer actually came back.
	if strings.Contains(err.Error(), "was rolled back") {
		t.Fatalf("Remove = %v, must not claim the removal was rolled back when its carrying key could not be restored", err)
	}
	if _, applied := errors.AsType[removeAppliedError](err); !applied {
		t.Fatalf("Remove = %v (%T), want the standing-removal discriminator: the carrying key stayed deleted", err, err)
	}
	if v, _ := f.store.Get("groq"); v != "" {
		t.Fatalf("stored key = %q, want it to stay deleted", v)
	}
}

// removeCredentials deletes the stored key first and only then the OAuth
// record, so its own failure can leave the key already gone. On an implicit
// instance - no [providers.<name>] entry, the stored key IS what carries it - a
// put-back that also fails leaves the instance unresolvable, so the removal
// stands and the caller must hear that rather than a retry against an instance
// that is already gone. The config-write and reload branches classify this with
// supplyOf(locked); the cleanup branch asked supplyAny with the "still
// configured" frame and was the one site left behind.
// TestInstances_RemoveMarksAppliedWhenTheDeletedCredentialCannotBeRestored is
// the authored sibling: there [providers.work] never moved, so supplyAny and
// the configured frame are right and no discriminator is owed.
func TestInstances_RemoveStandsWhenTheCleanupFailsOnAnImplicitStoredKey(t *testing.T) {
	f := newFlakyReloadFixture(t, "groq", func(int) bool { return false })
	if before := entry(t, f.ctl.List(), "groq"); before.ActiveSource != "store" || !before.Implicit {
		t.Fatalf("fixture: groq = %+v, want an implicit instance resolving the stored key", before)
	}
	// The cleanup deletes the stored key and then fails on the OAuth delete; the
	// put-back of the key fails too.
	f.ctl.auth.deleteAuth = func(string, string) (bool, error) { return false, errors.New("delete refused") }
	f.ctl.auth.setCredential = func(string, string) error { return errors.New("restore refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "groq"})
	if err == nil {
		t.Fatal("Remove = nil, want the cleanup and restore failures reported")
	}
	if !strings.Contains(err.Error(), "the removal stands") {
		t.Fatalf("Remove = %v, want the standing frame: the carrying key is gone", err)
	}
	if strings.Contains(err.Error(), "still configured") {
		t.Fatalf("Remove = %v, want no configured frame on an implicit instance whose key stayed deleted", err)
	}
	if _, applied := errors.AsType[removeAppliedError](err); !applied {
		t.Fatalf("Remove = %v (%T), want the standing-removal discriminator: the carrying key stayed deleted", err, err)
	}
	if v, _ := f.store.Get("groq"); v != "" {
		t.Fatalf("stored key = %q, want it to stay deleted", v)
	}
}

// The test above covers a rollback file that does not load either. This one
// covers the branch where it does: the removal's reload fails, the rollback
// lands, and the reload that follows it succeeds. The file the rollback puts
// back is the pre-removal one, and the removal's own failed load read a file
// that was not - which the real ones can only differ by if an external writer
// replaced providers.toml between Remove's two reads (before and l are
// adjacent statements), so the failure is injected at the loader instead: that
// is what makes the ordering deterministic, and the error is still the real
// registry error a load of an unresolvable layer produces (#711).
func TestInstances_RemoveRestoresTheCredentialBeforeTheRollbackReload(t *testing.T) {
	f := newInstancesFixture(t, nil)
	// A bearer base, so the stored key is the credential this instance
	// resolves: the Codex transport reads an OAuth record and ignores the
	// store (spec §5.1).
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// The layer the removal's reload is made to fail on: an entry that parses
	// but cannot resolve an endpoint.
	brokenPath := filepath.Join(filepath.Dir(f.tomlPath), "broken.toml")
	if err := os.WriteFile(brokenPath, []byte("[providers.standalone]\nprotocol = \"openai-chat\"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	var loads int
	loadFn := func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		loads++
		// Credentials from disk, the way cmdutil.LoadRegistry loads them: the
		// registry then resolves each instance's credential from
		// credentials.toml as it stood at that moment. The fixture's shared
		// in-memory store would hide the order this test is about - the store
		// object the controller restores into is not the one a reload builds
		// its registry over.
		store, err := credentials.LoadStore(f.credsPath)
		if err != nil {
			return nil, nil, err
		}
		path := f.tomlPath
		if loads == 2 {
			path = brokenPath
		}
		opts := append(
			testProbeRegistryOptions(f.stateDir, store, func(string) (string, bool) { return "", false }),
			registry.WithConfigPath(path),
		)
		r, err := registry.Load(append(opts, extra...)...)
		return r, store, err
	}
	replacement := hubcore.NewProviderRegistry(loadFn)
	f.ctl.reg = replacement
	f.ctl.auth.reg = replacement
	// Load 1 primes it over the clean file; load 2 is the removal's reload,
	// load 3 is the implicit-only fallback that failure takes, and load 4 is
	// the rollback's reload.
	if err := replacement.Reload(); err != nil {
		t.Fatalf("prime Reload: %v", err)
	}

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})

	if err == nil {
		t.Fatal("Remove = nil, want the reload failure")
	}
	if !strings.Contains(err.Error(), "was rolled back") {
		t.Fatalf("Remove = %v, want the reload rollback", err)
	}
	// Pins the branch: this rollback's reload succeeded, so the failure must not
	// claim a config that does not load.
	if strings.Contains(err.Error(), "does not load either") {
		t.Fatalf("Remove = %v, want a rollback whose reload succeeded", err)
	}
	if v, _ := f.store.Get("work"); v != "sk-stored" {
		t.Fatalf("stored key = %q, want the rollback to have restored it", v)
	}
	// A reload resolves each instance's credential from the stores, so the key
	// has to be back before it runs: this is what a launch reads
	// (spawn.go's validateProviderCredentials) and what the pane shows as the
	// active source. Reloading first caches "none" here, and restoring the key
	// afterwards does not rebuild the view.
	inst, ok := replacement.Get().Instance("work")
	if !ok {
		t.Fatal("the reloaded registry has no work instance")
	}
	if inst.CredentialSource != "store" {
		t.Fatalf("the restored instance resolves CredentialSource = %q, want store", inst.CredentialSource)
	}
	if got := entry(t, f.ctl.List(), "work"); got.ActiveSource != "store" || !got.HasStoredFile {
		t.Fatalf("list row = activeSource %q hasStoredFile %v, want store/true", got.ActiveSource, got.HasStoredFile)
	}
}

// The rollback write is itself a write, so it can fail too, and then there is
// nothing left that can put the entry back while the file still refuses to
// load. The removal stands, but the credentials the cleanup deleted belong to
// the name the caller re-authors after being told the removal failed, so they
// still go back - losing them would report the failure and take the secret too.
func TestInstances_RemoveRestoresCredentialsWhenTheRollbackCannotBeWritten(t *testing.T) {
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

	// The reload failure is arranged the way the sibling test arranges it; the
	// rollback write is what has to fail here. It is broken through the
	// registry's own load callback, so no stub stands in for the write: the
	// removal's reload is the second load the replacement registry serves (the
	// first primes it over the clean file) and it turns the writer's temp path
	// into a directory, which is the same disk that refuses any full disk or
	// read-only root. The first write's temp file is gone once its rename
	// landed, so the removal's own write still succeeds and only the rollback
	// after it cannot land.
	var loads int
	loadFn := func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		loads++
		if loads == 2 {
			resolved, err := filepath.EvalSymlinks(f.tomlPath)
			if err != nil {
				t.Errorf("EvalSymlinks(%s): %v", f.tomlPath, err)
			}
			if err := os.Mkdir(resolved+".tmp", 0o700); err != nil {
				t.Errorf("Mkdir(%s): %v", resolved+".tmp", err)
			}
		}
		opts := append(
			testProbeRegistryOptions(f.stateDir, f.store, func(string) (string, bool) { return "", false }),
			registry.WithConfigPath(f.tomlPath),
		)
		r, err := registry.Load(append(opts, extra...)...)
		return r, f.store, err
	}
	replacement := hubcore.NewProviderRegistry(loadFn)
	f.ctl.reg = replacement
	f.ctl.auth.reg = replacement
	// Primed before the unresolvable entry lands, so the removal still starts
	// from a registry that holds work.
	if err := replacement.Reload(); err != nil {
		t.Fatalf("prime Reload: %v", err)
	}

	// An entry that parses but cannot resolve an endpoint (#711: no base and no
	// base_url of its own): the layer the removal writes carries it, so the
	// reload that follows the write fails.
	raw, err := os.ReadFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	raw = append(raw, []byte("\n[providers.standalone]\nprotocol = \"openai-chat\"\n")...)
	if err := os.WriteFile(f.tomlPath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err = f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})

	if err == nil {
		t.Fatal("Remove = nil, want the reload failure")
	}
	// Failing here first is the red-first symptom: the early return skipped the
	// restore, so the secret the cleanup deleted never came back.
	if v, _ := f.store.Get("work"); v != "sk-stored" {
		t.Fatalf("stored key = %q, want the failed rollback to have restored it", v)
	}
	if _, loadErr := authopenai.LoadAuth(f.stateDir, "work"); loadErr != nil {
		t.Fatalf("the OAuth record was not restored: %v", loadErr)
	}
	// The removal stands, so the error must say so and must not claim a
	// rollback that never landed.
	if strings.Contains(err.Error(), "was rolled back") {
		t.Fatalf("Remove = %v, want the failed rollback reported as standing", err)
	}
	if !strings.Contains(err.Error(), "removal stands in the config") {
		t.Fatalf("Remove = %v, want the removal named as still applied", err)
	}
	l, _, readErr := registry.ReadConfigFile(f.tomlPath)
	if readErr != nil {
		t.Fatalf("ReadConfigFile: %v", readErr)
	}
	if _, still := l.Providers["work"]; still {
		t.Fatal("[providers.work] is in the config, want the failed rollback to have left the removal applied")
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
//
// The observables moved with the rule that a UI credential makes an instance
// removable: the refusal below is the environment-backed instance the name
// holds now. The old half of this test - a stored key surviving the refusal -
// is no longer expressible, because a stored key outranks the variable
// (registry spec §10) and would make the instance the user's own, and so
// correctly removable.
func TestInstances_RemoveValidatesTheInstanceUnderTheControllerLock(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"OPENAI_API_KEY": "env-key"})
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "openai", Base: "anthropic"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inst, ok := f.ctl.reg.Get().Instance("openai"); !ok || inst.Implicit {
		t.Fatalf("the fixture's openai must be the authored entry Remove deletes (ok = %v, %+v)", ok, inst)
	}
	// A credential under the name the rewrite introduces: nothing this removal
	// refuses may take it.
	if err := f.store.Set("work", "sk-neighbour"); err != nil {
		t.Fatalf("Set: %v", err)
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
	if v, _ := f.store.Get("work"); v != "sk-neighbour" {
		t.Fatalf("a removal that refused deleted a credential anyway: work = %q", v)
	}
}

// TestInstances_RemoveWaitsForAnInFlightCredentialWrite pins the other half of
// the removal's atomicity: the credential cleanup, the providers.toml write
// and the reload are one step against credential writers. Holding the read
// side is what an in-flight evener/auth/apiKey/set does, and a removal that
// runs through it clears the store before the writer has written, leaving the
// key behind under a name it just deleted.
func TestInstances_RemoveWaitsForAnInFlightCredentialWrite(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	f.ctl.auth.credMu.RLock()
	done := make(chan error, 1)
	go func() { done <- f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"}) }()
	select {
	case err := <-done:
		f.ctl.auth.credMu.RUnlock()
		t.Fatalf("the removal ran through a credential write still in flight (err = %v)", err)
	case <-time.After(100 * time.Millisecond):
	}
	f.ctl.auth.credMu.RUnlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Remove: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the removal never finished after the credential write released the lock")
	}
	if v, ok := f.store.Get("work"); ok || v != "" {
		t.Fatalf("credential remains after Remove: value=%q present=%v", v, ok)
	}
}

// Every instance mutation that rewrites providers.toml and reloads is one step
// with the credential writes: a client's endpoint assertion is checked inside
// the credential lock, so a mutation that landed between that check and the
// store would put the secret on an endpoint the client never reviewed. A
// reload is not just a read either - it commits what it read, so one that
// overlapped a credential clear could publish a view the clear had already
// invalidated. Each mutation below holds credMu exclusively across its write
// and reload, so it cannot run through a credential write still in flight.
func TestInstances_MutationsWaitForAnInFlightCredentialWrite(t *testing.T) {
	for _, tt := range []struct {
		name string
		run  func(f *instancesFixture) error
	}{
		{"edit moves the endpoint", func(f *instancesFixture) error {
			return f.ctl.Edit(appwire.InstanceEditParams{Name: "work", BaseURL: "https://moved.example.test/v1"})
		}},
		{"create authors a shadowing entry", func(f *instancesFixture) error {
			return f.ctl.Create(appwire.InstanceCreateParams{Name: "work2", Base: "openai"})
		}},
		{"set default rewrites the file", func(f *instancesFixture) error {
			return f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "work"})
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newInstancesFixture(t, nil)
			if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
				t.Fatalf("Create: %v", err)
			}

			f.ctl.auth.credMu.RLock()
			done := make(chan error, 1)
			go func() { done <- tt.run(f) }()
			select {
			case err := <-done:
				f.ctl.auth.credMu.RUnlock()
				t.Fatalf("the mutation ran through a credential write still in flight (err = %v)", err)
			case <-time.After(100 * time.Millisecond):
			}
			f.ctl.auth.credMu.RUnlock()

			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("mutation: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the mutation never finished after the credential write released the lock")
			}
		})
	}
}

// The race the lock exists for, end to end: a credential write that starts
// before the removal (it has already read the registry and passed its checks)
// must not leave its key behind. The removal holds the credential lock
// exclusively, so the write lands first and the cleanup that follows it
// removes what it wrote.
func TestInstances_RemoveClearsACredentialItsRacerWrote(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	originalSet := f.ctl.auth.setCredential
	setEntered := make(chan struct{})
	releaseSet := make(chan struct{})
	f.ctl.auth.setCredential = func(name, value string) error {
		close(setEntered)
		<-releaseSet
		return originalSet(name, value)
	}

	writeDone := make(chan error, 1)
	go func() {
		_, err := f.ctl.auth.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "work", Value: "sk-race"})
		writeDone <- err
	}()
	<-setEntered

	removeDone := make(chan error, 1)
	go func() { removeDone <- f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"}) }()
	// Only so the removal has reached the credential lock: what the test
	// asserts does not depend on the wait.
	time.Sleep(100 * time.Millisecond)
	close(releaseSet)

	if err := <-writeDone; err != nil {
		t.Fatalf("ApiKeySet: %v", err)
	}
	select {
	case err := <-removeDone:
		if err != nil {
			t.Fatalf("Remove: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Remove never finished after the credential write completed")
	}
	if v, ok := f.store.Get("work"); ok || v != "" {
		t.Fatalf("a credential written across the removal survived it: value=%q present=%v", v, ok)
	}
	if _, still := f.ctl.reg.Get().Instance("work"); still {
		t.Fatal("the removed instance still resolves")
	}
}

// TestInstances_RemoveRefusesWhenARacerMadeItEnvironmentBacked is the mirror
// image of the race the credential lock exists for: the removal classifies the
// row before it holds credMu, and a credential clear that held the lock first
// can change what the instance resolves. Here the cleared key is the only thing
// making the instance the user's - the provider's environment variable is also
// set - so once the clear lands and the registry reloads, the row is the
// environment's and a removal must refuse rather than report success and leave
// it standing. The classification is re-asked under the lock for exactly this
// interleaving.
func TestInstances_RemoveRefusesWhenARacerMadeItEnvironmentBacked(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"OPENAI_API_KEY": "env-key"})
	if err := f.store.Set("openai", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	if before := entry(t, f.ctl.List(), "openai"); !before.Implicit || before.ActiveSource != "store" {
		t.Fatalf("fixture: openai = %+v, want the stored key to outrank the variable", before)
	}

	// The clear is held inside its seam, having already taken credMu
	// exclusively, so the removal below classifies the row as the user's and
	// then blocks on the credential lock the clear holds. The barrier below
	// reports that the removal has made that pre-lock classification and is
	// parked at the lock, so releasing the clear cannot race the
	// classification the way a sleep would let it.
	originalClear := f.ctl.auth.clearCredential
	clearEntered := make(chan struct{})
	releaseClear := make(chan struct{})
	f.ctl.auth.clearCredential = func(name string) error {
		close(clearEntered)
		<-releaseClear
		return originalClear(name)
	}

	clearDone := make(chan error, 1)
	go func() {
		_, err := f.ctl.auth.ApiKeyClear(appwire.AuthApiKeyClearParams{Provider: "openai"})
		clearDone <- err
	}()
	<-clearEntered

	removeAtLock := make(chan struct{})
	f.ctl.beforeCredentialLock = func() { close(removeAtLock) }
	removeDone := make(chan error, 1)
	go func() { removeDone <- f.ctl.Remove(appwire.InstanceRemoveParams{Name: "openai"}) }()
	// The removal is now past the classification that read the row as the
	// user's; the clear's own reload runs inside credMu, so the removal's
	// locked re-check is guaranteed to read the registry the clear produced.
	select {
	case <-removeAtLock:
	case <-time.After(10 * time.Second):
		t.Fatal("Remove never reached the credential lock")
	}
	close(releaseClear)

	if err := <-clearDone; err != nil {
		t.Fatalf("ApiKeyClear: %v", err)
	}
	select {
	case err := <-removeDone:
		if err == nil || !strings.Contains(err.Error(), "exists from the environment") {
			t.Fatalf("Remove = %v, want the refusal for the instance the environment supplies once its stored key was cleared", err)
		}
		// The locked re-check answers with the same wire class the pre-lock
		// classification uses: a caller-fixable condition is InvalidParams
		// whichever race loses, not an internal fault when a concurrent clear
		// makes the environment supply the row (mirrors
		// TestInstances_RemoveRefusesImplicitInstance).
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Fatalf("Remove = %v, want an InvalidParams wire error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Remove never finished after the credential clear completed")
	}
	after := entry(t, f.ctl.List(), "openai")
	if !after.Implicit || after.ActiveSource != "env:OPENAI_API_KEY" {
		t.Fatalf("openai = %+v, want the row the environment supplies back", after)
	}
}

// A key is only worth storing under a name something reads. The pane offers
// that write for the rows its listing had, so a name that is neither an
// instance nor a curated provider is one an instance was removed from since -
// and storing the key there would leave it under a name nothing curates until
// a later instance of that name inherited it, which is the orphan credential
// the removal's own cleanup exists to prevent.
func TestInstances_ApiKeySetRefusesAKeyForARemovedInstance(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	_, err := f.ctl.auth.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "work", Value: "sk-orphan"})

	if err == nil {
		t.Fatal("ApiKeySet stored a key under a name no instance or provider has")
	}
	// The name the caller sent is theirs to fix, like every other refusal of an
	// unknown instance (#717/#748).
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("ApiKeySet = %v, want an InvalidParams wire error", err)
	}
	if v, ok := f.store.Get("work"); ok {
		t.Fatalf("stored key = %q, want nothing stored for a name nothing curates", v)
	}
	// The hazard the refusal removes: the name comes back as an instance and
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := entry(t, f.ctl.List(), "work"); got.ActiveSource != "none" || got.HasStoredFile {
		t.Fatalf("recreated instance = activeSource %q hasStoredFile %v, want none/false", got.ActiveSource, got.HasStoredFile)
	}
	// Positive control: the names the pane does offer still take a key - a
	// curated implicit provider here, an authored instance above.
	if _, err := f.ctl.auth.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "anthropic", Value: "sk-ant-live"}); err != nil {
		t.Fatalf("ApiKeySet(anthropic): %v", err)
	}
	if v, _ := f.store.Get("anthropic"); v != "sk-ant-live" {
		t.Fatalf("stored key for anthropic = %q, want it stored", v)
	}
}

// An endpoint's identity includes what the displayed URL leaves out: a query
// parameter can name a different endpoint (an API version, a deployment) and
// can carry a token, so it must not cross the wire as text but must still be
// comparable. The fingerprint is what lets a client tell a query-only endpoint
// change from no change at all.
func TestInstances_ListExposesAnEndpointFingerprint(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name:    "work",
		Base:    "openai",
		BaseURL: "https://gateway.test/v1?api-version=2024-02-01",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	first := entry(t, f.ctl.List(), "work")
	if first.BaseURL != "https://gateway.test/v1" {
		t.Fatalf("displayed BaseURL = %q, want the query parameter kept off the wire", first.BaseURL)
	}
	if len(first.EndpointFingerprint) != 64 {
		t.Fatalf("EndpointFingerprint = %q, want a digest", first.EndpointFingerprint)
	}
	if again := entry(t, f.ctl.List(), "work"); again.EndpointFingerprint != first.EndpointFingerprint {
		t.Fatalf("the fingerprint moved between listings: %q then %q", first.EndpointFingerprint, again.EndpointFingerprint)
	}

	// A change the displayed URL cannot show: the sanitized copy stays put and
	// the fingerprint is what moves.
	if err := f.ctl.Edit(appwire.InstanceEditParams{
		Name:    "work",
		BaseURL: "https://gateway.test/v1?api-version=2025-01-01",
	}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	second := entry(t, f.ctl.List(), "work")
	if second.BaseURL != first.BaseURL {
		t.Fatalf("displayed BaseURL = %q, want the sanitized copy unchanged", second.BaseURL)
	}
	if second.EndpointFingerprint == first.EndpointFingerprint {
		t.Fatal("a query-parameter-only endpoint change left the fingerprint unchanged, so a client cannot see it")
	}
}

// A row's displayed URL and its endpoint fingerprint have to come from one
// snapshot of the registry. Pairing a URL read from one state of providers.toml
// with a fingerprint computed against a later one would serve a client a
// destination that never existed, and a credential write asserting that pair
// would be checked against it.
func TestInstances_ListingRowFingerprintsTheSnapshotItCameFrom(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name:    "work",
		Base:    "openai",
		BaseURL: "https://a.example.test/v1",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	served := entry(t, f.ctl.List(), "work")
	stale := f.ctl.reg.Get()
	staleInst, ok := stale.Instance("work")
	if !ok {
		t.Fatal("the fixture registry has no work instance")
	}

	// Another client moves the endpoint while the snapshot above is what a
	// listing would have been built from.
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", BaseURL: "https://b.example.test/v1"}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	moved := f.ctl.reg.Get()
	movedInst, ok := moved.Instance("work")
	if !ok {
		t.Fatal("the fixture registry has no work instance after the edit")
	}

	got := f.ctl.entryFor(stale, staleInst, nil, endpointFingerprintKey(f.ctl.authStateDir()))
	if got.BaseURL != served.BaseURL || got.EndpointFingerprint != served.EndpointFingerprint {
		t.Fatalf("a row built from the snapshot = %q/%q, want the %q/%q that snapshot served",
			got.BaseURL, got.EndpointFingerprint, served.BaseURL, served.EndpointFingerprint)
	}
	if movedFP := destinationFingerprint(f.ctl.authStateDir(), moved, movedInst); got.EndpointFingerprint == movedFP {
		t.Fatal("the row was fingerprinted against the current registry instead of the snapshot it came from")
	}
}

// The digest covers the whole destination a credential-bearing request is built
// from, not only the URL the listing displays: a protocol switch or a change to
// a request path template sends the secret somewhere else while every visible
// field of the row stays byte-identical, so a form comparing only what it can
// see would carry a key to a destination nobody reviewed.
func TestInstances_EndpointFingerprintCoversTheResolvedTransport(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name:    "work",
		Base:    "openai",
		BaseURL: "https://gateway.test/v1",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	first := entry(t, f.ctl.List(), "work")
	if first.EndpointFingerprint == "" {
		t.Fatal("fixture drift: the endpoint must be fingerprintable here")
	}

	// The protocol selects which request templates apply. Both are supported for
	// this provider, and the displayed URL is the same either way.
	next := "openai-responses"
	if first.Protocol == next {
		next = "openai-chat"
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", Protocol: next}); err != nil {
		t.Fatalf("Edit(protocol=%s): %v", next, err)
	}
	second := entry(t, f.ctl.List(), "work")
	if second.BaseURL != first.BaseURL {
		t.Fatalf("displayed BaseURL = %q, want the sanitized copy unchanged", second.BaseURL)
	}
	if second.EndpointFingerprint == first.EndpointFingerprint {
		t.Fatal("a protocol change left the fingerprint unchanged, so a client cannot see where the request now goes")
	}

	// And a request path template authored by hand, which the edit form cannot
	// reach: the same visible row, a different path.
	if err := os.WriteFile(f.tomlPath, []byte(`[providers.work]
base = "openai"
base_url = "https://gateway.test/v1"
protocol = "`+next+`"
endpoint = "/somewhere-else"
`), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", f.tomlPath, err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	third := entry(t, f.ctl.List(), "work")
	if third.BaseURL != first.BaseURL {
		t.Fatalf("displayed BaseURL = %q, want the sanitized copy unchanged", third.BaseURL)
	}
	if third.EndpointFingerprint == second.EndpointFingerprint {
		t.Fatal("a request-path change left the fingerprint unchanged, so a client cannot see where the request now goes")
	}
}

// The fingerprint stands in for parts of the endpoint that can be low-entropy
// (a password in userinfo, a short query token), so it is keyed with the hub's
// own secret: an unkeyed digest of a guessable secret is a guessable function
// of it, and a client holding the listing could recover the secret by brute
// force - exactly what the sanitized copy exists to prevent.
func TestInstances_EndpointFingerprintIsKeyedWithTheHubSecret(t *testing.T) {
	f := newInstancesFixture(t, nil)
	const endpoint = "https://gateway.test/v1?token=hunter2"
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai", BaseURL: endpoint}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := entry(t, f.ctl.List(), "work").EndpointFingerprint
	if again := entry(t, f.ctl.List(), "work").EndpointFingerprint; again != got {
		t.Fatalf("the fingerprint moved between listings: %q then %q", got, again)
	}
	// The same endpoint under a second state root has its own key and so a
	// different digest. That is what keying means here: the value a client
	// holds is a function of the hub's secret, not of the endpoint alone, so
	// the secret parts it covers cannot be recovered from it.
	other := newInstancesFixture(t, nil)
	if err := other.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai", BaseURL: endpoint}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if otherFP := entry(t, other.ctl.List(), "work").EndpointFingerprint; otherFP == got {
		t.Fatal("two state roots fingerprinted the same endpoint identically, so the digest is not keyed")
	}
	// The key is machine-local and closed to other users, the same discipline
	// the OAuth records beside it keep.
	info, err := os.Stat(filepath.Join(f.stateDir, endpointFingerprintKeyFile))
	if err != nil {
		t.Fatalf("Stat(%s): %v", endpointFingerprintKeyFile, err)
	}
	hubtest.AssertFileMode0600(t, info, "the key file")
}

// A state root the hub cannot key under omits the fingerprint rather than
// serving a digest anyone can recompute.
func TestInstances_EndpointFingerprintIsOmittedWithoutAKey(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A directory where the key file belongs: neither reading nor creating it
	// can succeed, which is the shape of a state root the hub cannot key under.
	unkeyableStateRoot(t, f.stateDir)
	if got := entry(t, f.ctl.List(), "work").EndpointFingerprint; got != "" {
		t.Fatalf("EndpointFingerprint = %q, want it omitted when no key is available", got)
	}
}

// An edit lands no secret, so an edit that asserts nothing must not consult a
// fingerprint key it does not need: a hub whose state root cannot yield the key
// still edits and renames, exactly as it did before the edit assertion existed.
// A caller that DID assert an endpoint still fails closed, because the hub
// cannot say the name resolves there.
func TestInstances_EditAndRenameNeedNoKeyWhenNothingIsAsserted(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A directory where the key file belongs: neither reading nor creating it
	// can succeed, so the hub cannot key a fingerprint and the listing omits one.
	unkeyableStateRoot(t, f.stateDir)
	if got := entry(t, f.ctl.List(), "work").EndpointFingerprint; got != "" {
		t.Fatalf("EndpointFingerprint = %q, want it omitted when no key is available", got)
	}

	// An unasserted field edit: no key is needed, so it lands.
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", APIKeyEnv: "PORTKEY_KEY"}); err != nil {
		t.Fatalf("unasserted edit on a hub that cannot key fingerprints: %v", err)
	}
	if got := entry(t, f.ctl.List(), "work"); got.APIKeyEnv != "PORTKEY_KEY" {
		t.Fatalf("apiKeyEnv = %q, want the unasserted edit to land", got.APIKeyEnv)
	}

	// A non-empty assertion still fails closed: the hub cannot resolve it.
	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", APIKeyEnv: "OTHER_KEY", ExpectedEndpointFingerprint: "stale"})
	if err == nil {
		t.Fatal("an asserted edit must refuse while the hub cannot resolve the endpoint")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("asserted edit = %v, want a conflict refusal", err)
	}
	if got := entry(t, f.ctl.List(), "work"); got.APIKeyEnv != "PORTKEY_KEY" {
		t.Fatalf("apiKeyEnv = %q, want the refused asserted edit to change nothing", got.APIKeyEnv)
	}

	// An unasserted rename: no key needed either.
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"}); err != nil {
		t.Fatalf("unasserted rename on a hub that cannot key fingerprints: %v", err)
	}
	if _, ok := f.ctl.reg.Get().Instance("personal"); !ok {
		t.Fatal("the unasserted rename did not land")
	}
}

// A state root the hub cannot key under is not silent: the listing has to say
// why every fingerprint is missing (the rows a client cannot verify against,
// and the writes that fail closed with nothing visible behind them). The entry
// names the key file and the reason, never key material, and a healthy hub has
// no such entry.
func TestInstances_ListDiagnosesAnUnusableEndpointFingerprintKey(t *testing.T) {
	newWork := func(t *testing.T) *instancesFixture {
		t.Helper()
		f := newInstancesFixture(t, nil)
		if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		return f
	}
	keyDiagnostics := func(diags []string) []string {
		var out []string
		for _, d := range diags {
			if strings.HasPrefix(d, endpointFingerprintKeyFile+": ") {
				out = append(out, d)
			}
		}
		return out
	}

	t.Run("a healthy hub reports nothing", func(t *testing.T) {
		f := newWork(t)
		if got := keyDiagnostics(f.ctl.List().Diagnostics); len(got) != 0 {
			t.Fatalf("a healthy hub diagnosed its key file: %v", got)
		}
	})

	t.Run("an unusable key is reported", func(t *testing.T) {
		f := newWork(t)
		// A directory where the key file belongs: neither reading nor creating
		// it can succeed, so every fingerprint is omitted and the pane has to
		// say why.
		unkeyableStateRoot(t, f.stateDir)
		diags := f.ctl.List().Diagnostics
		found := keyDiagnostics(diags)
		if len(found) != 1 {
			t.Fatalf("Diagnostics = %v, want exactly one entry for the unusable %s", diags, endpointFingerprintKeyFile)
		}
		if !strings.Contains(found[0], "(endpoint fingerprints are unavailable until it can be read or written)") {
			t.Fatalf("diagnostic = %q, want it to say the fingerprints are unavailable until the key can be read or written", found[0])
		}
		if strings.Contains(found[0], "in the way") {
			t.Fatalf("diagnostic carried the obstacle file's content: %q", found[0])
		}
	})
}

// A curated provider with no credential yet has no instance, but the listing
// still advertises a setup entry for it whose endpoint fingerprint is built by
// resolving the provider. A client asserting that value has to be answered from
// the same lookup, or the first key for every credential-requiring provider
// could never be saved: the hub would compute no fingerprint and call the
// write a conflict.
func TestInstances_ApiKeySetAcceptsTheFingerprintTheCatalogueAdvertises(t *testing.T) {
	// No ANTHROPIC_API_KEY and no stored key: anthropic has no instance here.
	f := newInstancesFixture(t, map[string]string{"ANTHROPIC_API_KEY": ""})
	var setup *appwire.InstanceEntry
	for _, p := range f.ctl.List().AvailableProviders {
		if p.ID == "anthropic" {
			setup = p.Setup
			break
		}
	}
	if setup == nil {
		t.Fatal("the listing carries no setup entry for anthropic")
	}
	if setup.EndpointFingerprint == "" {
		t.Fatal("the setup entry advertises no endpoint fingerprint to assert")
	}
	if _, ok := f.ctl.reg.Get().Instance("anthropic"); ok {
		t.Fatal("fixture drift: anthropic must have no instance without a credential")
	}

	// The check is live for such a provider, not skipped: a form opened on a
	// different endpoint is still refused. Without the resolution fallback the
	// hub would have no fingerprint to compare and would let this through.
	if _, err := f.ctl.auth.ApiKeySet(appwire.AuthApiKeySetParams{
		Provider:                    "anthropic",
		Value:                       "sk-ant-stale",
		ExpectedEndpointFingerprint: "an-endpoint-this-name-does-not-resolve-to",
	}); err == nil {
		t.Fatal("ApiKeySet accepted a stale assertion for an uncredentialed provider")
	}
	if v, ok := f.store.Get("anthropic"); ok {
		t.Fatalf("stored key = %q, want nothing stored for the stale assertion", v)
	}

	if _, err := f.ctl.auth.ApiKeySet(appwire.AuthApiKeySetParams{
		Provider:                    "anthropic",
		Value:                       "sk-ant-first",
		ExpectedEndpointFingerprint: setup.EndpointFingerprint,
	}); err != nil {
		t.Fatalf("ApiKeySet refused the endpoint the catalogue advertises: %v", err)
	}
	if v, _ := f.store.Get("anthropic"); v != "sk-ant-first" {
		t.Fatalf("stored key = %q, want the first key for the provider to land", v)
	}
}

// A key file this hub did not write 0600 is one another local user may be able
// to read, which is the whole guarantee the digest rests on (only a holder of
// the key can recompute it). Using it as-is would keep keying digests with a
// value someone else knows; the hub rotates it instead, so the exposed key stops
// describing anything. The write happens before any listing is read: the file is
// read fresh on every use, so the next read is the one that sees the rotation.
func TestInstances_EndpointFingerprintRotatesAKeyOthersCanRead(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	keyPath := filepath.Join(f.stateDir, endpointFingerprintKeyFile)
	exposed := "a-key-another-local-user-could-read"
	if err := os.WriteFile(keyPath, []byte(exposed), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", endpointFingerprintKeyFile, err)
	}

	if got := entry(t, f.ctl.List(), "work").EndpointFingerprint; got == "" {
		t.Fatal("a key file the hub rotates should leave it serving fingerprints")
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("Stat(%s): %v", endpointFingerprintKeyFile, err)
	}
	hubtest.AssertFileMode0600(t, info, "the key file after the read")
	rotated, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", endpointFingerprintKeyFile, err)
	}
	if string(rotated) == exposed {
		t.Fatal("the exposed key is still the hub's key: a leaked key has to be rotated, not used")
	}
}

// The owner arm of the same judgement: a key file that is not this hub's own is
// not one it wrote, so it is rotated like an unreadable one. The expected uid is
// injected because a test cannot own a file as another user.
func TestInstances_EndpointFingerprintRotatesAKeyThatIsNotItsOwn(t *testing.T) {
	original := endpointFingerprintKeyOwner
	endpointFingerprintKeyOwner = func() int { return os.Getuid() + 1 }
	t.Cleanup(func() { endpointFingerprintKeyOwner = original })

	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	keyPath := filepath.Join(f.stateDir, endpointFingerprintKeyFile)
	foreign := "a-key-file-the-hub-did-not-write"
	if err := os.WriteFile(keyPath, []byte(foreign), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", endpointFingerprintKeyFile, err)
	}

	if got := entry(t, f.ctl.List(), "work").EndpointFingerprint; got == "" {
		t.Fatal("a key file the hub rotates should leave it serving fingerprints")
	}
	rotated, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", endpointFingerprintKeyFile, err)
	}
	if string(rotated) == foreign {
		t.Fatal("a key file belonging to another uid was used as the hub's own")
	}
}

// Rotation is the answer to a key that may have leaked, so it has to take effect
// in a running hub: the key file is read on every use, so a key file an operator
// replaces stops keying digests immediately. That includes a replacement that
// changes neither the file's size nor its modification time, which a size-and-
// mtime cache could not tell apart. A key file that is deleted is rotated rather
// than kept, so the next use writes a fresh one.
func TestInstances_EndpointFingerprintFollowsARotatedKeyFile(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	first := entry(t, f.ctl.List(), "work").EndpointFingerprint
	if first == "" {
		t.Fatal("fixture drift: the endpoint must be fingerprintable here")
	}

	keyPath := filepath.Join(f.stateDir, endpointFingerprintKeyFile)
	if err := os.WriteFile(keyPath, []byte("a-rotated-key-an-operator-just-put-here"), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", endpointFingerprintKeyFile, err)
	}
	second := entry(t, f.ctl.List(), "work").EndpointFingerprint
	if second == first {
		t.Fatal("the fingerprint still came from the cached key after the key file was replaced")
	}

	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("Remove(%s): %v", endpointFingerprintKeyFile, err)
	}
	third := entry(t, f.ctl.List(), "work").EndpointFingerprint
	if third == "" || third == second {
		t.Fatal("a deleted key file was not replaced by a fresh one")
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("Stat(%s) after the rotation: %v", endpointFingerprintKeyFile, err)
	}

	// The reviewer's rotation, against the 43-byte key the hub just generated:
	// a different key of the same length, with the replaced file's own
	// modification time put back, so size and mtime together cannot tell it apart
	// from the file it replaced. Only reading the file on every use can see it.
	before, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("Stat(%s): %v", endpointFingerprintKeyFile, err)
	}
	sameLength := []byte("b-rotated-key-an-operator-just-put-here-now")
	if int64(len(sameLength)) != before.Size() {
		t.Fatalf("the replacement key is %d bytes, want the %d bytes of the file it replaces", len(sameLength), before.Size())
	}
	if err := os.WriteFile(keyPath, sameLength, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", endpointFingerprintKeyFile, err)
	}
	if err := os.Chtimes(keyPath, before.ModTime(), before.ModTime()); err != nil {
		t.Fatalf("Chtimes(%s): %v", endpointFingerprintKeyFile, err)
	}
	rotatedSameSize := entry(t, f.ctl.List(), "work").EndpointFingerprint
	if rotatedSameSize == third {
		t.Fatal("the fingerprint still came from the stale cached key after a same-size, same-mtime replacement")
	}
}

// A key file that is there but empty is a corrupt one, and treating it as "no
// key" would fail the endpoint-change protection open without a word. The hub
// replaces it, so the listing keeps serving fingerprints a client can compare.
func TestInstances_EndpointFingerprintRepairsAnEmptyKeyFile(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	keyPath := filepath.Join(f.stateDir, endpointFingerprintKeyFile)
	if err := os.WriteFile(keyPath, nil, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", endpointFingerprintKeyFile, err)
	}

	if got := entry(t, f.ctl.List(), "work").EndpointFingerprint; got == "" {
		t.Fatal("an empty key file left the endpoint fingerprint omitted")
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("Stat(%s): %v", endpointFingerprintKeyFile, err)
	}
	if info.Size() == 0 {
		t.Fatal("the empty key file was left in place")
	}
	hubtest.AssertFileMode0600(t, info, "the repaired key file")
}

// Something unusable at the key path does not leave the state root unkeyable
// when it can be lifted: an empty directory cannot be renamed over and is not
// a key, so it is removed and the path keyed atomically. (A non-empty
// directory cannot be either and stays the obstacle the diagnostics name.)
func TestInstances_EndpointFingerprintRepairsAnEmptyKeyPathDirectory(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	keyPath := filepath.Join(f.stateDir, endpointFingerprintKeyFile)
	if err := os.Mkdir(keyPath, 0o700); err != nil {
		t.Fatalf("Mkdir(%s): %v", keyPath, err)
	}

	if got := entry(t, f.ctl.List(), "work").EndpointFingerprint; got == "" {
		t.Fatal("an empty directory at the key path left the endpoint fingerprint omitted")
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("Stat(%s): %v", keyPath, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("key path mode = %v, want a regular key file", info.Mode())
	}
	hubtest.AssertFileMode0600(t, info, "the repaired key file")
}

// A platform that does not record POSIX permission bits - Windows synthesizes
// 0666 for every file, 0444 when read-only - must not have the hub's own key
// refused for the mode it reports: the refusal would send every read through
// the repair, so the hub would rotate the key on every use and every
// fingerprint a client had been shown would stop matching. The seam stands in
// for that platform, answering unjudged, which no mode written on this host
// makes the real helper do.
func TestInstances_EndpointFingerprintIsReadWhereThePlatformDoesNotReportModes(t *testing.T) {
	original := endpointFingerprintKeyMode
	endpointFingerprintKeyMode = func(os.FileInfo) (os.FileMode, bool) { return 0, false }
	t.Cleanup(func() { endpointFingerprintKeyMode = original })

	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	keyPath := filepath.Join(f.stateDir, endpointFingerprintKeyFile)
	// What the hub's own 0600 key reads back as on Windows: a mode the hub has
	// to accept as its own rather than judge against a POSIX 0600.
	if err := os.WriteFile(keyPath, []byte("a-key-the-hub-just-wrote"), 0o666); err != nil {
		t.Fatalf("WriteFile(%s): %v", endpointFingerprintKeyFile, err)
	}
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", endpointFingerprintKeyFile, err)
	}

	first := entry(t, f.ctl.List(), "work").EndpointFingerprint
	if first == "" {
		t.Fatal("a key the platform reports no modes for left the endpoint fingerprint omitted")
	}
	if second := entry(t, f.ctl.List(), "work").EndpointFingerprint; second != first {
		t.Fatal("the key was rotated between two reads on a platform that reports no modes")
	}
	after, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", endpointFingerprintKeyFile, err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("the hub rotated a key it should have accepted; every fingerprint a client was shown would stop matching")
	}
}

// A credential write that landed must not be reported as failed because the
// config it belongs to cannot be loaded: the secret is stored either way, and a
// caller told it failed retypes one the hub already has. The reload failure
// stays visible where it belongs - as the registry's own state, which is what
// carries the diagnostics and refuses instance writes (spec §10).
func TestInstances_ApiKeySetLandsWhenTheRegistryCannotReload(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// An entry that parses but cannot resolve an endpoint (#711): the reload
	// that follows the write fails.
	raw, err := os.ReadFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	raw = append(raw, []byte("\n[providers.standalone]\nprotocol = \"openai-chat\"\n")...)
	if err := os.WriteFile(f.tomlPath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := f.ctl.auth.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "work", Value: "sk-landed"}); err != nil {
		t.Fatalf("ApiKeySet reported a failure for a write that landed: %v", err)
	}
	if v, _ := f.store.Get("work"); v != "sk-landed" {
		t.Fatalf("stored key = %q, want the write to have landed", v)
	}
	if !f.ctl.reg.WritesRefused() {
		t.Fatal("the reload failure is no longer visible as the registry's own state")
	}
}

// An asserted endpoint the hub can no longer match is refused, not waved
// through: the client was shown an endpoint (it asserted one), and a hub that
// computes no fingerprint for the name cannot say the key would land there. The
// unkeyable state root is exactly the state that would otherwise switch the
// protection off silently, so a write carrying an assertion has to fail closed
// and ask the user to look again. An empty assertion is refused there too: the
// client was shown nothing because the hub could not key a fingerprint, and
// landing the key anyway would leave the destination unverified without a word.
// Only a hub with no state root at all has nothing to key with and nothing to
// refuse (TestInstances_ApiKeySetAcceptsAnEmptyAssertionWithoutAStateRoot).
func TestInstances_ApiKeySetRefusesAnAssertionTheHubCannotCheck(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A directory where the key file belongs: neither reading nor creating it
	// can succeed, so every fingerprint is omitted.
	unkeyableStateRoot(t, f.stateDir)
	if got := entry(t, f.ctl.List(), "work").EndpointFingerprint; got != "" {
		t.Fatalf("EndpointFingerprint = %q, want it omitted with no usable key", got)
	}

	if _, err := f.ctl.auth.ApiKeySet(appwire.AuthApiKeySetParams{
		Provider:                    "work",
		Value:                       "sk-unchecked",
		ExpectedEndpointFingerprint: "asserted-by-a-form-the-hub-cannot-describe",
	}); err == nil {
		t.Fatal("ApiKeySet accepted an assertion the hub cannot check")
	}
	if v, _ := f.store.Get("work"); v != "" {
		t.Fatalf("stored key = %q, want nothing stored for an assertion that cannot be checked", v)
	}
	// The client that was shown nothing asserts nothing - and it was shown
	// nothing because the hub cannot key a fingerprint, so the write has to fail
	// closed here too. Accepting the empty assertion would let a concurrent
	// endpoint change receive the credential with no verification at all, which
	// is the hole an unusable key used to open silently.
	_, err := f.ctl.auth.ApiKeySet(appwire.AuthApiKeySetParams{
		Provider: "work",
		Value:    "sk-no-assertion",
	})
	if err == nil {
		t.Fatal("ApiKeySet accepted a write whose destination the hub cannot key a fingerprint for")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("ApiKeySet = %v, want a Conflict saying the destination cannot be verified", err)
	}
	if !strings.Contains(err.Error(), "cannot key its endpoint fingerprints right now") {
		t.Fatalf("refusal = %q, want it to say the hub cannot key its endpoint fingerprints", err)
	}
	if v, _ := f.store.Get("work"); v != "" {
		t.Fatalf("stored key = %q, want nothing stored for a write the hub cannot verify", v)
	}
}

// An empty assertion is accepted while the hub can key a fingerprint, whatever
// the destination: verifyEndpointFingerprint checks an asserted endpoint, and a
// client that asserts nothing is refused only when the hub itself cannot key
// one (TestInstances_ApiKeySetRefusesAnAssertionTheHubCannotCheck). This pins
// the accepted side for a name with no destination at all: openai-compatible
// with no base URL is hidden, so endpointHasDestination answers false while the
// hub can key fingerprints.
func TestInstances_ApiKeySetAcceptsAnEmptyAssertionWithNoDestination(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if f.ctl.auth.endpointHasDestination("openai-compatible") {
		t.Fatal("fixture drift: openai-compatible must have no destination with no base URL")
	}
	if _, err := f.ctl.auth.ApiKeySet(appwire.AuthApiKeySetParams{
		Provider: "openai-compatible",
		Value:    "sk-no-destination",
	}); err != nil {
		t.Fatalf("ApiKeySet refused a write for a name with no destination: %v", err)
	}
	if v, _ := f.store.Get("openai-compatible"); v != "sk-no-destination" {
		t.Fatalf("stored key = %q, want the write to have landed", v)
	}
}

// The other side of the same rule: a hub with no state root at all (a bare test
// controller) has nothing to key an endpoint fingerprint with, so no client
// could have been shown one and there is no verification to fail closed on. The
// empty assertion is accepted there, and the write lands.
func TestInstances_ApiKeySetAcceptsAnEmptyAssertionWithoutAStateRoot(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// No state root: nothing to key with, and every row omits its fingerprint.
	f.ctl.auth.stateDir = ""
	if got := entry(t, f.ctl.List(), "work").EndpointFingerprint; got != "" {
		t.Fatalf("EndpointFingerprint = %q, want it omitted with no state root", got)
	}

	if _, err := f.ctl.auth.ApiKeySet(appwire.AuthApiKeySetParams{
		Provider: "work",
		Value:    "sk-no-root",
	}); err != nil {
		t.Fatalf("ApiKeySet refused a write with no state root to key an endpoint with: %v", err)
	}
	if v, _ := f.store.Get("work"); v != "sk-no-root" {
		t.Fatalf("stored key = %q, want the write to have landed", v)
	}
	// Nothing to report, either: there is no key file that could be unusable.
	for _, d := range f.ctl.List().Diagnostics {
		if strings.HasPrefix(d, endpointFingerprintKeyFile+": ") {
			t.Fatalf("a hub with no state root diagnosed a key file it does not have: %q", d)
		}
	}
}

// removeCredentials documents that it reports which credential layers it
// actually deleted, and its caller's restore gate relies on that reading: a
// flag set for a file that was never there says there is something to put back.
func TestInstances_RemoveCredentialsReportsOnlyWhatItDeleted(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	deleted, err := f.ctl.removeCredentials("work")
	if err != nil {
		t.Fatalf("removeCredentials: %v", err)
	}
	if deleted.storedKey || deleted.oauthRecord {
		t.Fatalf("removeCredentials reported %+v for a name holding nothing", deleted)
	}

	// With a layer present the flag follows the deletion it just performed.
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(authopenai.AuthFilePath(f.stateDir, "work")), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(authopenai.AuthFilePath(f.stateDir, "work"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	deleted, err = f.ctl.removeCredentials("work")
	if err != nil {
		t.Fatalf("removeCredentials: %v", err)
	}
	if !deleted.storedKey || !deleted.oauthRecord {
		t.Fatalf("removeCredentials reported %+v for a name holding both layers", deleted)
	}
}

// A credential write may assert the endpoint whose form the user was shown, and
// the hub checks that assertion where the write lands. A client's own
// comparison reads a listing a concurrent change can outdate, so without this
// the secret could still land on an endpoint the user never reviewed.
func TestInstances_ApiKeySetRefusesAnEndpointItsCallerDidNotSee(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name:    "work",
		Base:    "openai",
		BaseURL: "https://gateway.test/v1?api-version=2024-02-01",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	shown := entry(t, f.ctl.List(), "work").EndpointFingerprint
	if shown == "" {
		t.Fatal("the listing carried no fingerprint to assert")
	}
	// The endpoint moves after the form was opened: only the query parameter
	// changes, so the displayed URL stays identical.
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "work", BaseURL: "https://gateway.test/v1?api-version=2025-01-01"}); err != nil {
		t.Fatalf("Edit: %v", err)
	}

	_, err := f.ctl.auth.ApiKeySet(appwire.AuthApiKeySetParams{
		Provider:                    "work",
		Value:                       "sk-stale",
		ExpectedEndpointFingerprint: shown,
	})
	if err == nil {
		t.Fatal("ApiKeySet stored a key against an endpoint its caller no longer sees")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("ApiKeySet = %v, want a conflict wire error", err)
	}
	if v, ok := f.store.Get("work"); ok {
		t.Fatalf("stored key = %q, want nothing stored for the stale endpoint", v)
	}

	// The endpoint the caller can see now is accepted.
	current := entry(t, f.ctl.List(), "work").EndpointFingerprint
	if _, err := f.ctl.auth.ApiKeySet(appwire.AuthApiKeySetParams{
		Provider:                    "work",
		Value:                       "sk-current",
		ExpectedEndpointFingerprint: current,
	}); err != nil {
		t.Fatalf("ApiKeySet with the endpoint the caller sees: %v", err)
	}
	if v, _ := f.store.Get("work"); v != "sk-current" {
		t.Fatalf("stored key = %q, want the matching write to land", v)
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
	got := f.ctl.entryFor(f.ctl.reg.Get(), inst, &registry.Provider{APIKeyEnv: []string{"FIRST", "SECOND"}}, endpointFingerprintKey(f.ctl.authStateDir()))
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

// Both halves of a rename can fail at once: the credential move leaves
// something behind, and the reload that follows cannot read the config. The
// caller has to hear both - what only this call knows (the leftover
// credential) and what the hub's own view is left in (a registry that may
// still list the old name and refuse instance writes) - and the error still
// carries the applied marker the rename's persisted file earns it, so the
// clients whose lists just went stale are announced. moveCredentials reads the
// OAuth record right after moving the key, which is the one point a test can
// reach between the move and that reload, so the unreadable config is written
// from there.
func TestInstances_EditRenameReportsTheMoveAndReloadFailuresTogether(t *testing.T) {
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
	if err == nil {
		t.Fatal("Edit(rename) = nil, want both failures reported")
	}
	if !strings.Contains(err.Error(), "stored key not copied") {
		t.Fatalf("Edit(rename) = %v, want the leftover credential reported", err)
	}
	if !strings.Contains(err.Error(), "could not be reloaded") {
		t.Fatalf("Edit(rename) = %v, want the failed reload reported too", err)
	}
	if !writeDidApply(err) {
		t.Fatalf("Edit(rename) = %v (%T), want the applied marker: the rename is on disk", err, err)
	}
	persisted, _ := instanceRenameError(err)
	if !persisted {
		t.Fatalf("Edit(rename) = %v, want the renamePersistedError discriminator for the handler", err)
	}
}

// A rename whose credentials moved cleanly and whose final reload failed is
// as persisted as one that succeeded outright: providers.toml and the
// credentials both carry the new name, and only the hub's own view is behind.
// So it is an applied write too, which is what has the handler announce it to
// the clients whose lists that file just made stale. The unreadable
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
	if !writeDidApply(err) {
		t.Fatalf("Edit(rename) = %v (%T), want an applied write so the rename is still broadcast", err, err)
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
	ctl := newTestInstancesController(t, tomlPath, dir, t.TempDir(), nil)

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

// TestInstances_EditRenamesAnImplicitInstanceUnderANewName: an instance with no
// authored entry renames by authoring one under the new name. Nothing shadows
// the old name: the rename moved the row it could move, and for an
// environment-backed instance the old row was never the rename's to move, so
// it simply stays as the environment supplies it.
func TestInstances_EditRenamesAnImplicitInstanceUnderANewName(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "groq", NewName: "g2"}); err != nil {
		t.Fatalf("Edit = %v, want the rename to land", err)
	}
	p := authoredEntry(t, f.tomlPath, "g2")
	if p.Base != "groq" {
		t.Fatalf("authored base = %q, want the curated id the unnamed entry inherited from", p.Base)
	}
	l, _, err := registry.ReadConfigFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadConfigFile: %v", err)
	}
	if _, shadowed := l.Providers["groq"]; shadowed {
		t.Fatal("the rename authored a shadow for the old name")
	}
	resp := f.ctl.List()
	if got := entry(t, resp, "g2"); got.Implicit {
		t.Fatalf("the renamed instance = %+v, want an authored instance", got)
	}
	if !listedInstance(resp, "groq") {
		t.Fatal("the environment instance must stay listed")
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
	ID        string
	AuthModes []string
	Setup     *appwire.InstanceEntry
}

func providerSetup(t *testing.T, list appwire.InstanceListResponse, id string) providerSetupDescriptor {
	t.Helper()
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(mustMarshal(t, list), &wire); err != nil {
		t.Fatal(err)
	}
	catalogue, ok := wire["availableProviders"]
	if !ok {
		t.Fatal("missing public availableProviders key")
	}
	var providers []map[string]json.RawMessage
	if err := json.Unmarshal(catalogue, &providers); err != nil {
		t.Fatal(err)
	}
	for _, fields := range providers {
		var p providerSetupDescriptor
		if err := json.Unmarshal(fields["id"], &p.ID); err != nil {
			t.Fatal(err)
		}
		if p.ID != id {
			continue
		}
		modes, ok := fields["authModes"]
		if !ok {
			t.Fatal("missing public authModes key")
		}
		if err := json.Unmarshal(modes, &p.AuthModes); err != nil {
			t.Fatal(err)
		}
		if setup, ok := fields["setup"]; ok {
			if err := json.Unmarshal(setup, &p.Setup); err != nil {
				t.Fatal(err)
			}
		}
		return p
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

// List reads the registry snapshot and providers.toml as one view. A mutation
// writes the file and reloads the registry inside c.mu, so a listing that ran
// between those two halves of a mutation would serve a row carrying the fresh
// authored credential fields beside the endpoint and endpoint fingerprint of
// the view the reload has not committed yet. List has to hold the lock for its
// whole snapshot: it may not return while a mutation is mid-section, and the
// listing it does return must be one generation throughout.
func TestInstances_ListWaitsForAMutationHoldingTheLock(t *testing.T) {
	const (
		aURL = "https://a.example.test/v1"
		bURL = "https://b.example.test/v1"
		aKey = "KEY_A"
		bKey = "KEY_B"
	)

	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name:      "work",
		Base:      "openai",
		BaseURL:   aURL,
		APIKeyEnv: aKey,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	settled := entry(t, f.ctl.List(), "work")
	if settled.BaseURL != aURL || settled.APIKeyEnv != aKey {
		t.Fatalf("fixture drift: settled row = %+v, want baseURL %q apiKeyEnv %q", settled, aURL, aKey)
	}
	fpA := settled.EndpointFingerprint
	if fpA == "" {
		t.Fatal("fixture drift: the endpoint must be fingerprintable here")
	}

	// A registry whose loader can be paused. The controller swaps its live
	// registry (the rollback tests replace f.ctl.reg and f.ctl.auth.reg the same
	// way), and the pause catches the mutation between its providers.toml write
	// and the reload that commits it: the file already carries the new values
	// while the registry still holds the old snapshot.
	armed := atomic.Bool{}
	loadEntered := make(chan struct{})
	releaseLoad := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLoad) }) }
	loadFn := func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		if armed.CompareAndSwap(true, false) {
			close(loadEntered)
			<-releaseLoad
		}
		opts := append(
			testProbeRegistryOptions(f.stateDir, f.store, func(string) (string, bool) { return "", false }),
			registry.WithConfigPath(f.tomlPath),
		)
		r, err := registry.Load(append(opts, extra...)...)
		return r, f.store, err
	}
	replacement := hubcore.NewProviderRegistry(loadFn)
	if err := replacement.Reload(); err != nil {
		t.Fatalf("prime Reload: %v", err)
	}
	f.ctl.reg = replacement
	f.ctl.auth.reg = replacement

	editDone := make(chan error, 1)
	editFinished := make(chan struct{})
	armed.Store(true)
	go func() {
		defer close(editFinished)
		editDone <- f.ctl.Edit(appwire.InstanceEditParams{Name: "work", BaseURL: bURL, APIKeyEnv: bKey})
	}()

	select {
	case <-loadEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("Edit never reached its reload, so the loader was not paused")
	}

	listDone := make(chan appwire.InstanceListResponse, 1)
	listFinished := make(chan struct{})
	go func() {
		defer close(listFinished)
		listDone <- f.ctl.List()
	}()
	t.Cleanup(func() {
		release()
		for _, finished := range []chan struct{}{editFinished, listFinished} {
			select {
			case <-finished:
			case <-time.After(10 * time.Second):
				t.Error("a goroutine was still blocked after the loader was released")
			}
		}
	})

	// The loader is paused inside the mutation's held lock, so List has to be
	// waiting on it: one that returns here read a half-committed mutation.
	select {
	case got := <-listDone:
		row := entry(t, got, "work")
		t.Fatalf("List returned while a mutation held the controller lock: row baseURL=%q apiKeyEnv=%q endpointFingerprint=%q; want the listing to wait and then serve one generation (A: %q/%q/%q, B: %q/%q)",
			row.BaseURL, row.APIKeyEnv, row.EndpointFingerprint, aURL, aKey, fpA, bURL, bKey)
	case <-time.After(100 * time.Millisecond):
	}

	release()
	if err := <-editDone; err != nil {
		t.Fatalf("Edit: %v", err)
	}

	var resp appwire.InstanceListResponse
	select {
	case resp = <-listDone:
	case <-time.After(10 * time.Second):
		t.Fatal("List never returned after the mutation completed")
	}

	// The generations in question, read from the settled pane after the edit.
	settledB := entry(t, f.ctl.List(), "work")
	if settledB.BaseURL != bURL || settledB.APIKeyEnv != bKey {
		t.Fatalf("fixture drift: after the edit the pane serves %+v, want baseURL %q apiKeyEnv %q", settledB, bURL, bKey)
	}
	fpB := settledB.EndpointFingerprint
	if fpB == "" || fpB == fpA {
		t.Fatalf("fixture drift: moving the endpoint must move the fingerprint: %q then %q", fpA, fpB)
	}

	row := entry(t, resp, "work")
	switch {
	case row.BaseURL == aURL && row.APIKeyEnv == aKey && row.EndpointFingerprint == fpA:
		// The pre-edit generation, self-consistent.
	case row.BaseURL == bURL && row.APIKeyEnv == bKey && row.EndpointFingerprint == fpB:
		// The post-edit generation, self-consistent.
	default:
		t.Fatalf("List served a mixed row: baseURL=%q apiKeyEnv=%q endpointFingerprint=%q; want all of A (%q/%q/%q) or all of B (%q/%q/%q)",
			row.BaseURL, row.APIKeyEnv, row.EndpointFingerprint, aURL, aKey, fpA, bURL, bKey, fpB)
	}
}

// List's snapshot covers credential writes too, not only providers.toml
// mutations: Logout holds credMu exclusively while it decides which layer the
// name clears and removes it, and a listing that ran inside that section would
// pair the credential state of one generation with the registry view of
// another. List takes the shared side of credMu across its whole snapshot (mu
// then credMu, the documented order), so it cannot return while an exclusive
// section is held, and the listing taken afterwards is one generation
// throughout.
func TestInstances_ListWaitsForACredentialWriteHoldingTheLock(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	settled := entry(t, f.ctl.List(), "work")
	if settled.ActiveSource != "store" || !settled.HasStoredFile {
		t.Fatalf("fixture drift: settled row = %+v, want the stored key's source", settled)
	}
	if settled.EndpointFingerprint == "" {
		t.Fatal("fixture drift: the endpoint must be fingerprintable here")
	}

	// Paused inside Logout's exclusive credMu section, before the clear it
	// performs: that is the section a listing must not read through.
	originalClear := f.ctl.auth.clearCredential
	clearEntered := make(chan struct{})
	releaseClear := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseClear) }) }
	f.ctl.auth.clearCredential = func(name string) error {
		close(clearEntered)
		<-releaseClear
		return originalClear(name)
	}
	t.Cleanup(release)

	logoutDone := make(chan error, 1)
	go func() {
		_, err := f.ctl.auth.Logout(appwire.AuthLogoutParams{Provider: "work"})
		logoutDone <- err
	}()
	select {
	case <-clearEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("Logout never reached the credential clear, so its exclusive section was not held")
	}

	listDone := make(chan appwire.InstanceListResponse, 1)
	listFinished := make(chan struct{})
	go func() {
		defer close(listFinished)
		listDone <- f.ctl.List()
	}()
	t.Cleanup(func() {
		release()
		// The listing is joined before the fixture's temp roots are removed:
		// building a row can still land the endpoint-fingerprint key file under
		// the state root, and one still running at cleanup races RemoveAll.
		select {
		case <-listFinished:
		case <-time.After(10 * time.Second):
			t.Error("the listing goroutine was still blocked after the section was released")
		}
	})

	// The exclusive section is held, so List has to be waiting on it: one that
	// returns here read a credential state a writer is still deciding.
	select {
	case got := <-listDone:
		row := entry(t, got, "work")
		t.Fatalf("List returned while a credential write held the lock: row activeSource=%q hasStoredFile=%v endpointFingerprint=%q; want the listing to wait for the section to end",
			row.ActiveSource, row.HasStoredFile, row.EndpointFingerprint)
	case <-time.After(100 * time.Millisecond):
	}

	release()
	select {
	case err := <-logoutDone:
		if err != nil {
			t.Fatalf("Logout: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Logout never finished after its section was released")
	}

	// The listing afterwards is one generation: the cleared credential state
	// beside the registry view that names it, never one half beside the other.
	after := entry(t, f.ctl.List(), "work")
	if after.ActiveSource != "none" || after.HasStoredFile {
		t.Fatalf("post-logout row = activeSource %q hasStoredFile %v, want the cleared generation", after.ActiveSource, after.HasStoredFile)
	}
}

// TestEnvironmentBackedTreatsACodexInstanceAsTheUsersOwn: the registry resolves
// a Codex instance only to the oauth source (a readable record) or none (an
// absent or corrupt one), and the allow-list places both with the user - so the
// server agrees with the client's fromEnvironment whatever source the status
// resolved. The cases that must stay environment-backed (and the
// credential-bearing ones that must not) are pinned beside it, so the allow-list
// cannot widen into "every implicit instance is the user's".
func TestEnvironmentBackedTreatsACodexInstanceAsTheUsersOwn(t *testing.T) {
	for _, tc := range []struct {
		name string
		inst registry.Instance
		want bool
	}{
		{"codex, no resolved source", registry.Instance{Implicit: true, Auth: registry.AuthOAuthOpenAICodex, CredentialSource: "none"}, false},
		{"codex, empty source", registry.Instance{Implicit: true, Auth: registry.AuthOAuthOpenAICodex, CredentialSource: ""}, false},
		{"codex, oauth source", registry.Instance{Implicit: true, Auth: registry.AuthOAuthOpenAICodex, CredentialSource: "oauth"}, false},
		{"keyless local endpoint", registry.Instance{Implicit: true, Auth: registry.AuthNone, CredentialSource: "none"}, true},
		{"optional-bearer gateway", registry.Instance{Implicit: true, Auth: registry.AuthOptionalBearer, CredentialSource: "env:GATEWAY_KEY"}, true},
		{"env-backed bearer", registry.Instance{Implicit: true, Auth: registry.AuthBearer, CredentialSource: "env:OPENAI_API_KEY"}, true},
		{"application-default credentials", registry.Instance{Implicit: true, Auth: registry.AuthGCPADC, CredentialSource: "adc"}, true},
		{"stored curated key", registry.Instance{Implicit: true, Auth: registry.AuthBearer, CredentialSource: "store"}, false},
		{"credential-required, no source", registry.Instance{Implicit: true, Auth: registry.AuthBearer, CredentialSource: "none"}, false},
		{"credential-required, empty source", registry.Instance{Implicit: true, Auth: registry.AuthBearer, CredentialSource: ""}, false},
		{"credential-required, unknown source", registry.Instance{Implicit: true, Auth: registry.AuthBearer, CredentialSource: "saml"}, false},
		{"authored instance", registry.Instance{Implicit: false, Auth: registry.AuthBearer, CredentialSource: "env:OPENAI_API_KEY"}, false},
	} {
		if got := environmentBacked(tc.inst); got != tc.want {
			t.Fatalf("environmentBacked(%+v) = %v, want %v (%s)", tc.inst, got, tc.want, tc.name)
		}
	}
}

// TestInstances_RemovalRemedyNamesSomethingThatExists: the refusal reaches the
// CLI and direct RPC callers, so each remedy has to name an action this
// instance can actually take - the variable it reads, the host's ADC
// credentials, the stored credential a keyless instance holds, or nothing when
// it holds none and needs none. A keyless instance whose active source is a
// variable is the exception: the variable supplies an optional credential, so
// the remedy names the provider endpoint that keeps the row instead of sending
// the caller to unset a key the scheme never needed.
func TestInstances_RemovalRemedyNamesSomethingThatExists(t *testing.T) {
	for _, tt := range []struct {
		name    string
		inst    registry.Instance
		want    string
		refuses []string
	}{
		{
			name: "an environment variable is unset by name",
			inst: registry.Instance{Name: "groq", Implicit: true, Auth: registry.AuthBearer, CredentialSource: "env:GROQ_API_KEY"},
			want: "unset GROQ_API_KEY instead",
		},
		{
			name:    "the ADC file is the host's to remove",
			inst:    registry.Instance{Name: "vertex", Implicit: true, Auth: registry.AuthGCPADC, CredentialSource: "adc"},
			want:    "application-default credentials",
			refuses: []string{"unset", "OAuth record"},
		},
		{
			name:    "a keyless instance holding a stored key names its endpoint, not the clear",
			inst:    registry.Instance{Name: "ollama", Implicit: true, Auth: registry.AuthOptionalBearer, CredentialSource: "store"},
			want:    "remove or disable that endpoint instead",
			refuses: []string{"unset", "OAuth record", "clear the stored credential instead"},
		},
		{
			name:    "a credential-required instance whose stored key carries it keeps its wording",
			inst:    registry.Instance{Name: "groq", Implicit: true, Auth: registry.AuthBearer, CredentialSource: "store"},
			want:    "remove the credential that supplies it instead",
			refuses: []string{"remove or disable that endpoint", "unset", "OAuth record"},
		},
		{
			name:    "a keyless instance holding nothing does not invent one",
			inst:    registry.Instance{Name: "ollama", Implicit: true, Auth: registry.AuthNone, CredentialSource: "none"},
			want:    "holds no credential of its own to clear",
			refuses: []string{"clear the stored credential", "unset", "OAuth record"},
		},
		{
			name:    "a keyless instance the environment supplies names its endpoint, not the optional variable",
			inst:    registry.Instance{Name: "ollama", Implicit: true, Auth: registry.AuthOptionalBearer, CredentialSource: "env:OLLAMA_API_KEY"},
			want:    "remove or disable that endpoint instead",
			refuses: []string{"unset", "OLLAMA_API_KEY", "holds no credential of its own to clear", "clear the stored credential"},
		},
		{
			name:    "a non-keyless instance with no credential source names none to remove",
			inst:    registry.Instance{Name: "work", Implicit: true, Auth: registry.AuthBearer, CredentialSource: "none"},
			want:    "holds no credential of its own to clear",
			refuses: []string{"remove the credential that supplies it", "unset", "OAuth record"},
		},
		{
			name:    "an empty credential source is treated the same as none",
			inst:    registry.Instance{Name: "work", Implicit: true, Auth: registry.AuthBearer, CredentialSource: ""},
			want:    "holds no credential of its own to clear",
			refuses: []string{"remove the credential that supplies it", "unset", "OAuth record"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := removalRemedy(tt.inst)
			if !strings.Contains(got, tt.want) {
				t.Fatalf("removalRemedy = %q, want it to name %q", got, tt.want)
			}
			for _, wrong := range tt.refuses {
				if strings.Contains(got, wrong) {
					t.Fatalf("removalRemedy = %q, which names the action %q that this instance cannot take", got, wrong)
				}
			}
		})
	}
}

// TestInstances_RemoveRefusalNamesTheSourceSpecificRemedy: the refusal's advice
// has to match what actually makes the instance exist. The old message told
// every environment-backed row to remove the OAuth record, which for a keyless
// gateway with a stored key names a record that does not exist beside a key
// that does.
func TestInstances_RemoveRefusalNamesTheSourceSpecificRemedy(t *testing.T) {
	for _, tt := range []struct {
		name    string
		env     map[string]string
		store   string
		remove  string
		want    string
		refuses []string
	}{
		{
			name:    "a keyless optional-bearer row with a stored key",
			store:   "ollama",
			remove:  "ollama",
			want:    "remove or disable that endpoint instead",
			refuses: []string{"OAuth record", "clear the stored credential instead"},
		},
		{
			name:    "an environment-supplied bearer row",
			env:     map[string]string{"OPENAI_API_KEY": "env-key"},
			remove:  "openai",
			want:    "unset OPENAI_API_KEY instead",
			refuses: []string{"OAuth record"},
		},
		{
			name:    "a keyless optional-bearer row whose optional key is set",
			env:     map[string]string{"OLLAMA_API_KEY": "gk"},
			remove:  "ollama",
			want:    "remove or disable that endpoint instead",
			refuses: []string{"unset"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newInstancesFixture(t, tt.env)
			if tt.store != "" {
				if err := f.store.Set(tt.store, "gk"); err != nil {
					t.Fatalf("Set: %v", err)
				}
				if err := f.ctl.auth.reloadRegistry(); err != nil {
					t.Fatalf("reloadRegistry: %v", err)
				}
			}
			err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: tt.remove})
			if err == nil {
				t.Fatalf("Remove(%q) = nil, want the environment-backed refusal", tt.remove)
			}
			if !strings.Contains(err.Error(), "exists from the environment") {
				t.Fatalf("Remove(%q) = %v, want the environment-backed refusal", tt.remove, err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Remove(%q) = %v, want the remedy %q", tt.remove, err, tt.want)
			}
			for _, refuses := range tt.refuses {
				if strings.Contains(err.Error(), refuses) {
					t.Fatalf("Remove(%q) = %v, must not name %q", tt.remove, err, refuses)
				}
			}
		})
	}
}

// TestInstances_KeylessRowOutlivesUnsettingItsOptionalKey is the premise of the
// keyless row's remedy: computeInstances derives a curated keyless provider
// whether or not a credential resolves, so unsetting the variable its active
// source names drops the optional key and leaves the row where it was. The
// refusal must not send the caller there; its provider endpoint is what keeps
// the row.
func TestInstances_KeylessRowOutlivesUnsettingItsOptionalKey(t *testing.T) {
	env := map[string]string{"OLLAMA_API_KEY": "gk"}
	f := newInstancesFixture(t, env)
	if got := entry(t, f.ctl.List(), "ollama"); got.ActiveSource != "env:OLLAMA_API_KEY" {
		t.Fatalf("ollama activeSource = %q, want the variable to resolve first", got.ActiveSource)
	}

	delete(env, "OLLAMA_API_KEY")
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	got := entry(t, f.ctl.List(), "ollama")
	if !got.Implicit || got.ActiveSource != "none" {
		t.Fatalf("ollama after unsetting OLLAMA_API_KEY = %+v, want the same implicit row with no credential", got)
	}
}

// TestInstances_ListExposesRenameLeavesRow pins the wire bit the rename note
// reads. The hub decides whether freeing an instance's name re-supplies a row,
// because a client cannot: it cannot see ADC availability, and the curated set
// is the hub's. The cases are the ones the old client-side inference got wrong.
func TestInstances_ListExposesRenameLeavesRow(t *testing.T) {
	home := t.TempDir() // no ADC file yet
	env := map[string]string{
		"HOME":                   home,
		"GROQ_API_KEY":           "gk",
		"OLLAMA_HOST":            "localhost",
		"GOOGLE_VERTEX_PROJECT":  "p",
		"GOOGLE_VERTEX_LOCATION": "global",
	}
	f := newInstancesFixture(t, env)
	raw := `[providers.groq]
api_key = "sk-authored"

[providers.ollama]
base_url = "http://127.0.0.1:11434/v1"

[providers.work]
base = "anthropic"
api_key = "sk-inline"
`
	if err := os.WriteFile(f.tomlPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}

	// The authored groq shadows the curated env-backed provider; freeing the
	// id re-derives it from GROQ_API_KEY, which the shadowed entry hid.
	if got := entry(t, f.ctl.List(), "groq"); !got.RenameLeavesRow {
		t.Fatalf("groq = %+v, want renameLeavesRow true (curated groq re-derives from GROQ_API_KEY)", got)
	}
	// The authored keyless curated ollama re-derives with no credential.
	if got := entry(t, f.ctl.List(), "ollama"); !got.RenameLeavesRow {
		t.Fatalf("ollama = %+v, want renameLeavesRow true (keyless curated provider)", got)
	}
	// A name nothing curates leaves nothing behind.
	if got := entry(t, f.ctl.List(), "work"); got.RenameLeavesRow {
		t.Fatalf("work = %+v, want renameLeavesRow false (no curated provider behind the name)", got)
	}

	// A stored gcp-adc credential is the user's. Without an ADC file the
	// rename moves the only credential, so freeing the id recreates nothing -
	// the old inference's false positive.
	if err := f.store.Set("google-vertex", `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}`); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	noADC := entry(t, f.ctl.List(), "google-vertex")
	if !noADC.Implicit || noADC.ActiveSource != "store" {
		t.Fatalf("fixture: google-vertex = %+v, want the stored JSON to resolve", noADC)
	}
	if noADC.RenameLeavesRow {
		t.Fatalf("google-vertex = %+v, want renameLeavesRow false without ADC", noADC)
	}

	// With the ADC file present the curated row re-derives once the stored
	// JSON moves away.
	adc := filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	if err := os.MkdirAll(filepath.Dir(adc), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(adc, []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reloadRegistry: %v", err)
	}
	if got := entry(t, f.ctl.List(), "google-vertex"); !got.RenameLeavesRow {
		t.Fatalf("google-vertex = %+v, want renameLeavesRow true once ADC exists", got)
	}
}

// TestInstances_EditAnswersWithItsOwnState: an edit asked for its captured
// answer returns a listing carrying the row that edit wrote, and the returned
// value is a snapshot - a later edit does not change it. (The write-lock GAP is
// not exercised: Edit exposes no seam inside its critical section to pause on,
// so a concurrent edit cannot be made to land between the write and the
// capture. What the code enforces is that both happen under c.mu; see edit.)
func TestInstances_EditAnswersWithItsOwnState(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "groq", BaseURL: "http://127.0.0.1:9/first"}); err != nil {
		t.Fatalf("Edit(first): %v", err)
	}

	var out appwire.InstanceListResponse
	if err := f.ctl.edit(appwire.InstanceEditParams{Name: "groq", BaseURL: "http://127.0.0.1:9/second"}, &out); err != nil {
		t.Fatalf("edit(second): %v", err)
	}

	// A later edit does not mutate the already-returned captured answer.
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "groq", BaseURL: "http://127.0.0.1:9/foreign"}); err != nil {
		t.Fatalf("Edit(foreign): %v", err)
	}

	second := entry(t, out, "groq")
	if !strings.Contains(second.BaseURL, "/second") {
		t.Fatalf("captured answer shows base_url %q, want the second edit's own state", second.BaseURL)
	}
	if foreign := entry(t, f.ctl.List(), "groq"); !strings.Contains(foreign.BaseURL, "/foreign") {
		t.Fatalf("later List shows base_url %q, want the foreign edit", foreign.BaseURL)
	}
}

// TestInstances_EditCapturesTheListingForARename: a successful rename answers
// with the same lock-scoped listing a plain edit does. The rename branch must
// fall through to the capture rather than return before it, or the client is
// handed an empty response and blanks the pane.
func TestInstances_EditCapturesTheListingForARename(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var out appwire.InstanceListResponse
	if err := f.ctl.edit(appwire.InstanceEditParams{Name: "work", NewName: "personal"}, &out); err != nil {
		t.Fatalf("edit(rename): %v", err)
	}
	if len(out.Instances) == 0 {
		t.Fatal("a successful rename returned an empty listing")
	}
	if renamed := entry(t, out, "personal"); renamed.Name != "personal" {
		t.Fatalf("captured row = %+v, want the renamed instance", renamed)
	}
}

// The implicit-provider fallback resolves at presence depth. A
// command-credentialed record always has an instance of its own (the
// config layer instantiates whatever it keys), so this fallback cannot
// see command material today; the pin keeps its answer identical to a
// full resolve's, so the depth swap can never lose a field.
func TestResolvedInstanceForPresenceShape(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(tomlPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	if err != nil {
		t.Fatal(err)
	}
	holder := newTestRegistry(t, t.TempDir(), tomlPath, store, map[string]string{"AWS_BEARER_TOKEN_BEDROCK": "tok"})
	r := holder.Get()
	p, ok := r.Provider("amazon-bedrock")
	if !ok {
		t.Fatal("the curated implicit provider is missing")
	}
	inst, addressable := resolvedInstanceFor(r, "amazon-bedrock", p.Hidden)
	if !addressable {
		t.Fatal("the implicit-provider fallback stopped resolving")
	}
	full, err := r.ResolveInstance("amazon-bedrock")
	if err != nil {
		t.Fatal(err)
	}
	if inst.CredentialSource != full.Credential.Source {
		t.Fatalf("fallback CredentialSource = %q, want the full resolve's %q", inst.CredentialSource, full.Credential.Source)
	}
	if inst.Auth != full.Transport.Auth || inst.Protocol != full.Protocol {
		t.Fatal("the fallback's auth/protocol drifted from a full resolve")
	}
	if inst.ShadowedEnvVar != full.ShadowedEnvVar {
		t.Fatalf("fallback ShadowedEnvVar = %q, want %q", inst.ShadowedEnvVar, full.ShadowedEnvVar)
	}
}
