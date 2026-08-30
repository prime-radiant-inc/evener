package hub

// Tests for hubInstancesController Create/Edit/Remove/SetDefault/List on the
// provider registry.
//
// Each test owns its providers.toml, credentials.toml, OAuth state directory
// and environment, so nothing here reads the developer's machine.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestInstances_CreateRejectsBadInput(t *testing.T) {
	for _, tt := range []struct {
		name   string
		params appwire.InstanceCreateParams
		want   string
	}{
		{
			name:   "invalid instance name",
			params: appwire.InstanceCreateParams{Name: "Work/Two", Base: "openai"},
			want:   "invalid instance name",
		},
		{
			name:   "unknown base provider",
			params: appwire.InstanceCreateParams{Name: "work", Base: "not-a-provider"},
			want:   "unknown base provider",
		},
		{
			name:   "literal secret in a credential header",
			params: appwire.InstanceCreateParams{Name: "work", Base: "openai", CredentialHeader: "Authorization=Bearer sk-literal"},
			want:   "$VARIABLE",
		},
		{
			// The word the form itself used to show. The registry rejects it
			// as a parse error, so writing it would brick the pane: the next
			// reload fails and refuseWhenBroken then refuses the corrective
			// edit too.
			name:   "protocol outside the registry vocabulary",
			params: appwire.InstanceCreateParams{Name: "work", Base: "openai", Protocol: "chat-completions"},
			want:   "invalid protocol",
		},
		{
			name:   "surface outside the registry vocabulary",
			params: appwire.InstanceCreateParams{Name: "work", Base: "openai", Surface: "compat"},
			want:   "invalid surface",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newInstancesFixture(t, nil)
			err := f.ctl.Create(tt.params)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Create = %v, want an error mentioning %q", err, tt.want)
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

// TestInstances_EditRejectsAnUnloadableProtocol: the invariant is that a write
// the pane accepts must reload cleanly. An unvalidated protocol would be
// written, fail the reload, and then lock the pane out of its own recovery.
func TestInstances_EditRejectsAnUnloadableProtocol(t *testing.T) {
	f := newInstancesFixture(t, map[string]string{"GROQ_API_KEY": "gk"})
	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "groq", Protocol: "responses"})
	if err == nil || !strings.Contains(err.Error(), "invalid protocol") {
		t.Fatalf("Edit = %v, want an invalid-protocol error", err)
	}
	if _, statErr := os.Stat(f.tomlPath); !os.IsNotExist(statErr) {
		t.Fatalf("a rejected edit wrote providers.toml (stat err=%v)", statErr)
	}
	if f.ctl.reg.WritesRefused() {
		t.Fatal("the registry still loads after a rejected edit; the pane must not be locked out")
	}
	if err := f.ctl.Edit(appwire.InstanceEditParams{Name: "groq", Surface: "compat"}); err == nil ||
		!strings.Contains(err.Error(), "invalid surface") {
		t.Fatalf("Edit = %v, want an invalid-surface error", err)
	}
}

func TestInstances_EditRejectsUnknownInstance(t *testing.T) {
	f := newInstancesFixture(t, nil)
	err := f.ctl.Edit(appwire.InstanceEditParams{Name: "nowhere", Protocol: "openai-chat"})
	if err == nil || !strings.Contains(err.Error(), "nowhere") {
		t.Fatalf("Edit = %v, want an unknown-instance error", err)
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

func TestInstances_RemoveDeletesEntryStoreKeyAndOAuthRecord(t *testing.T) {
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
	if _, err := authopenai.LoadAuth(f.stateDir, "work"); err == nil {
		t.Fatal("the OAuth record survived Remove")
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
	if err := f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "nowhere"}); err == nil {
		t.Fatal("SetDefault accepted a name that is not an instance")
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
