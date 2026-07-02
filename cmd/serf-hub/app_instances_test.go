package main

// Tests for Phase 2: hubInstancesController Create/Edit/Remove/SetDefault/List.
//
// Each test uses a temp dir for providers.toml, credentials.toml, and OAuth
// state so tests remain fully isolated from the developer environment.

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/appwire"
	authopenai "primeradiant.com/serf/auth/openai"
	"primeradiant.com/serf/auth/openai/oaitest"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/llm/providercfg"
)

// newTestInstancesController builds an isolated hubInstancesController backed
// by a temp providers.toml at path, credentials store at credsDir, and OAuth
// state at stateDir.
func newTestInstancesController(t *testing.T, tomlPath, credsDir, stateDir string) *hubInstancesController {
	t.Helper()
	credsPath := filepath.Join(credsDir, "credentials.toml")
	store, err := credentials.LoadStore(credsPath)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	auth := newHubAuthControllerWithStore(credsDir, store)
	auth.stateDir = stateDir
	auth.providersConfigPath = tomlPath

	return &hubInstancesController{
		providersConfigPath: tomlPath,
		auth:                auth,
	}
}

// writeMinimalProvidersToml writes a providers.toml with at least one instance
// so LoadFile succeeds (zero instances is a parse error).
func writeMinimalProvidersToml(t *testing.T, path string) {
	t.Helper()
	content := `schema = 1
default = "base"

[instances.base]
type = "anthropic"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 1. Create → List includes the new instance with correct fields; file
//    round-trips through LoadFile; no api_key in the written file.
// ─────────────────────────────────────────────────────────────────────────────

func TestInstances_Create_ListIncludesEntry(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)

	ctl := newTestInstancesController(t, tomlPath, dir, stateDir)

	err := ctl.Create(appwire.InstanceCreateParams{
		Type: "anthropic",
		Name: "mywork",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resp := ctl.List()
	var found *appwire.InstanceEntry
	for i := range resp.Instances {
		if resp.Instances[i].Name == "mywork" {
			found = &resp.Instances[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("List did not include 'mywork'; instances=%v", resp.Instances)
	}
	if found.Type != "anthropic" {
		t.Errorf("Type = %q, want anthropic", found.Type)
	}

	// Verify round-trip: re-read from disk.
	reloaded, exists, err := providercfg.LoadFile(tomlPath)
	if err != nil {
		t.Fatalf("LoadFile after Create: %v", err)
	}
	if !exists {
		t.Fatal("providers.toml absent after Create")
	}
	var diskInst *providercfg.InstanceConfig
	for i := range reloaded.Instances {
		if reloaded.Instances[i].Name == "mywork" {
			diskInst = &reloaded.Instances[i]
			break
		}
	}
	if diskInst == nil {
		t.Fatal("'mywork' not found in providers.toml after Create")
	}
	if diskInst.APIKey != "" {
		t.Errorf("api_key must not be written; got %q", diskInst.APIKey)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. Create with duplicate name → error
// ─────────────────────────────────────────────────────────────────────────────

func TestInstances_Create_DuplicateName_Errors(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)

	ctl := newTestInstancesController(t, tomlPath, dir, stateDir)

	if err := ctl.Create(appwire.InstanceCreateParams{Type: "anthropic", Name: "dup"}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	err := ctl.Create(appwire.InstanceCreateParams{Type: "anthropic", Name: "dup"})
	if err == nil {
		t.Fatal("expected error for duplicate name, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. Create with apiStyle on a non-openai type → error
// ─────────────────────────────────────────────────────────────────────────────

func TestInstances_Create_APIStyleOnNonOpenAI_Errors(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)

	ctl := newTestInstancesController(t, tomlPath, dir, stateDir)

	err := ctl.Create(appwire.InstanceCreateParams{
		Type:     "anthropic",
		Name:     "bad-style",
		APIStyle: "responses",
	})
	if err == nil {
		t.Fatal("expected error for apiStyle on non-openai type, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. Create with invalid name → error
// ─────────────────────────────────────────────────────────────────────────────

func TestInstances_Create_InvalidName_Errors(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)

	ctl := newTestInstancesController(t, tomlPath, dir, stateDir)

	// uppercase → invalid
	err := ctl.Create(appwire.InstanceCreateParams{Type: "anthropic", Name: "BadName"})
	if err == nil {
		t.Fatal("expected error for uppercase name, got nil")
	}

	// slash → invalid
	err = ctl.Create(appwire.InstanceCreateParams{Type: "anthropic", Name: "foo/bar"})
	if err == nil {
		t.Fatal("expected error for name with slash, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 5. Edit changes baseURL / apiStyle but not type
// ─────────────────────────────────────────────────────────────────────────────

func TestInstances_Edit_ChangesBaseURLAndAPIStyle(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)

	ctl := newTestInstancesController(t, tomlPath, dir, stateDir)

	if err := ctl.Create(appwire.InstanceCreateParams{Type: "openai", Name: "myoai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err := ctl.Edit(appwire.InstanceEditParams{
		Name:     "myoai",
		APIStyle: "chat-completions",
		BaseURL:  "https://api.example.com/v1",
	})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}

	resp := ctl.List()
	var found *appwire.InstanceEntry
	for i := range resp.Instances {
		if resp.Instances[i].Name == "myoai" {
			found = &resp.Instances[i]
			break
		}
	}
	if found == nil {
		t.Fatal("'myoai' not found after Edit")
	}
	if found.APIStyle != "chat-completions" {
		t.Errorf("APIStyle = %q, want chat-completions", found.APIStyle)
	}
	if found.BaseURL != "https://api.example.com/v1" {
		t.Errorf("BaseURL = %q, want https://api.example.com/v1", found.BaseURL)
	}
	// Type must be unchanged.
	if found.Type != "openai" {
		t.Errorf("Type = %q after Edit, want openai (immutable)", found.Type)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 5b. Edit preserves fields it doesn't touch: Headers, Compat, and Models
//     survive an APIStyle/BaseURL edit instead of being silently dropped.
// ─────────────────────────────────────────────────────────────────────────────

func TestInstances_Edit_PreservesHeadersCompatModels(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")

	content := `schema = 1
default = "gw"

[instances.gw]
type = "openai"
api_style = "chat-completions"
base_url = "https://gw.example.com/v1"

[instances.gw.headers]
X-Custom = "value"

[instances.gw.compat]
thinking_format = "zai"

[instances.gw.models."glm-5"]
context_window = 131072
`
	if err := os.WriteFile(tomlPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}

	ctl := newTestInstancesController(t, tomlPath, dir, stateDir)

	err := ctl.Edit(appwire.InstanceEditParams{
		Name: "gw",
		// api_style stays "chat-completions": Models/Compat are only valid for
		// OpenAI-compatible instances, and switching to "responses" would make
		// the resulting file fail to load. Only BaseURL changes.
		APIStyle: "chat-completions",
		BaseURL:  "https://gw2.example.com/v1",
	})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}

	cfg, _, err := providercfg.LoadFile(tomlPath)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	var inst *providercfg.InstanceConfig
	for i := range cfg.Instances {
		if cfg.Instances[i].Name == "gw" {
			inst = &cfg.Instances[i]
			break
		}
	}
	if inst == nil {
		t.Fatal("'gw' not found after Edit")
	}
	if inst.APIStyle != "chat-completions" {
		t.Errorf("APIStyle = %q, want chat-completions", inst.APIStyle)
	}
	if inst.BaseURL != "https://gw2.example.com/v1" {
		t.Errorf("BaseURL = %q, want https://gw2.example.com/v1", inst.BaseURL)
	}
	if inst.Headers["X-Custom"] != "value" {
		t.Errorf("Headers dropped by Edit: got %#v", inst.Headers)
	}
	if inst.Compat == nil || inst.Compat.ThinkingFormat != "zai" {
		t.Errorf("Compat dropped by Edit: got %#v", inst.Compat)
	}
	mc, ok := inst.Models["glm-5"]
	if !ok || mc.ContextWindow != 131072 {
		t.Errorf("Models dropped by Edit: got %#v", inst.Models)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 6. Edit a missing instance → error
// ─────────────────────────────────────────────────────────────────────────────

func TestInstances_Edit_MissingInstance_Errors(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)

	ctl := newTestInstancesController(t, tomlPath, dir, stateDir)

	err := ctl.Edit(appwire.InstanceEditParams{Name: "nonexistent"})
	if err == nil {
		t.Fatal("expected error for editing missing instance, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 7. Remove drops the instance, clears credentials.toml and auth/<name>.json,
//    and reassigns default when the removed instance was the default.
// ─────────────────────────────────────────────────────────────────────────────

func TestInstances_Remove_DropsAndClearsCredentials(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath) // starts with "base" (anthropic)

	ctl := newTestInstancesController(t, tomlPath, dir, stateDir)

	// Add a second openai instance "todelete" and set credentials + OAuth.
	if err := ctl.Create(appwire.InstanceCreateParams{Type: "openai", Name: "todelete"}); err != nil {
		t.Fatalf("Create todelete: %v", err)
	}
	if err := ctl.auth.creds.Set("todelete", "sk-todelete"); err != nil {
		t.Fatalf("Set creds: %v", err)
	}
	record := makeOAuthRecord("todelete", "del@example.com")
	if err := authopenai.SaveAuth(stateDir, "todelete", record); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}

	err := ctl.Remove(appwire.InstanceRemoveParams{Name: "todelete"})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// List should no longer include it.
	resp := ctl.List()
	for _, inst := range resp.Instances {
		if inst.Name == "todelete" {
			t.Error("'todelete' still present in List after Remove")
		}
	}

	// credentials.toml["todelete"] should be cleared.
	v, _ := ctl.auth.creds.Get("todelete")
	if v != "" {
		t.Errorf("credentials still present for todelete after Remove, got %q", v)
	}

	// auth/todelete.json should be removed.
	authPath := authopenai.AuthFilePath(stateDir, "todelete")
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Errorf("auth/todelete.json still exists after Remove; stat err=%v", err)
	}
}

func TestInstances_Remove_DefaultReassigned(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	// Write toml with two instances; "alpha" is default.
	content := `schema = 1
default = "alpha"

[instances.alpha]
type = "anthropic"

[instances.beta]
type = "anthropic"
`
	if err := os.WriteFile(tomlPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}

	ctl := newTestInstancesController(t, tomlPath, dir, stateDir)

	if err := ctl.Remove(appwire.InstanceRemoveParams{Name: "alpha"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Default should now be "beta" (only remaining).
	reloaded, exists, err := providercfg.LoadFile(tomlPath)
	if err != nil {
		t.Fatalf("LoadFile after Remove: %v", err)
	}
	if !exists {
		t.Fatal("providers.toml absent after Remove")
	}
	if reloaded.Default != "beta" {
		t.Errorf("Default = %q, want beta", reloaded.Default)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 8. SetDefault persists; setting to a missing instance errors
// ─────────────────────────────────────────────────────────────────────────────

func TestInstances_SetDefault_Persists(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	content := `schema = 1
default = "alpha"

[instances.alpha]
type = "anthropic"

[instances.beta]
type = "anthropic"
`
	if err := os.WriteFile(tomlPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}

	ctl := newTestInstancesController(t, tomlPath, dir, stateDir)

	if err := ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "beta"}); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}

	// Verify disk.
	reloaded, exists, err := providercfg.LoadFile(tomlPath)
	if err != nil {
		t.Fatalf("LoadFile after SetDefault: %v", err)
	}
	if !exists {
		t.Fatal("providers.toml absent after SetDefault")
	}
	if reloaded.Default != "beta" {
		t.Errorf("Default = %q, want beta", reloaded.Default)
	}

	// Verify List.
	resp := ctl.List()
	for _, inst := range resp.Instances {
		wantDefault := inst.Name == "beta"
		if inst.IsDefault != wantDefault {
			t.Errorf("IsDefault(%q) = %v, want %v", inst.Name, inst.IsDefault, wantDefault)
		}
	}
}

func TestInstances_SetDefault_MissingInstance_Errors(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)

	ctl := newTestInstancesController(t, tomlPath, dir, stateDir)

	err := ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "nonexistent"})
	if err == nil {
		t.Fatal("expected error for SetDefault on missing instance, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 9. List: IsDefault is correctly set; sort is Type then Name
// ─────────────────────────────────────────────────────────────────────────────

func TestInstances_List_IsDefaultAndSort(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	// "zorro" is anthropic but sorts last by name; type-first ordering must
	// place it at index 1 (after alpha/anthropic, before bravo/openai).
	// Name-only ordering would put zorro at index 2, so this fixture
	// distinguishes the two sort strategies.
	content := `schema = 1
default = "bravo"

[instances.alpha]
type = "anthropic"

[instances.bravo]
type = "openai"

[instances.zorro]
type = "anthropic"
`
	if err := os.WriteFile(tomlPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}

	ctl := newTestInstancesController(t, tomlPath, dir, stateDir)
	resp := ctl.List()

	if len(resp.Instances) != 3 {
		t.Fatalf("len(Instances) = %d, want 3", len(resp.Instances))
	}

	// Find each entry.
	byName := map[string]appwire.InstanceEntry{}
	for _, inst := range resp.Instances {
		byName[inst.Name] = inst
	}

	alpha, ok := byName["alpha"]
	if !ok {
		t.Fatal("'alpha' not in List")
	}
	if alpha.IsDefault {
		t.Error("alpha.IsDefault = true, want false")
	}

	bravo, ok := byName["bravo"]
	if !ok {
		t.Fatal("'bravo' not in List")
	}
	if !bravo.IsDefault {
		t.Error("bravo.IsDefault = false, want true")
	}

	if _, ok := byName["zorro"]; !ok {
		t.Fatal("'zorro' not in List")
	}

	// Sort is Type then Name: anthropic/alpha, anthropic/zorro, openai/bravo.
	// A name-only sort would produce alpha, bravo, zorro — putting zorro last.
	// Asserting zorro at index 1 proves the type key is load-bearing.
	wantOrder := []struct{ typ, name string }{
		{"anthropic", "alpha"},
		{"anthropic", "zorro"},
		{"openai", "bravo"},
	}
	for i, want := range wantOrder {
		got := resp.Instances[i]
		if got.Type != want.typ || got.Name != want.name {
			t.Errorf("Instances[%d] = %q/%q, want %q/%q", i, got.Type, got.Name, want.typ, want.name)
		}
	}
}

// Switching an instance out of the compat family (chat-completions →
// responses) while it still carries compat/models tables must fail LOUDLY at
// edit time — silently writing that file would brick every future Load.
func TestInstances_Edit_RejectsLeavingCompatFamilyWithTables(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")

	content := `schema = 1
default = "gw"

[instances.gw]
type = "openai"
api_style = "chat-completions"
base_url = "https://gw.example.com/v1"

[instances.gw.compat]
thinking_format = "zai"
`
	if err := os.WriteFile(tomlPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}

	ctl := newTestInstancesController(t, tomlPath, dir, stateDir)
	err := ctl.Edit(appwire.InstanceEditParams{
		Name:     "gw",
		APIStyle: "responses",
		BaseURL:  "https://gw.example.com/v1",
	})
	if err == nil {
		t.Fatal("Edit succeeded; want a refusal (compat table only valid for chat-completions)")
	}
	// The on-disk file must be untouched and still loadable.
	cfg, exists, loadErr := providercfg.LoadFile(tomlPath)
	if loadErr != nil || !exists {
		t.Fatalf("providers.toml no longer loads after rejected edit: %v", loadErr)
	}
	if cfg.Instances[0].APIStyle != providercfg.StyleChatCompletions {
		t.Fatalf("rejected edit mutated the file: %+v", cfg.Instances[0])
	}
}

// Removing the LAST instance deletes providers.toml (the documented
// absent-file behavior re-seeds from env on next startup) instead of failing
// WriteFile's cannot-load validation or writing an unloadable empty file.
func TestInstances_Remove_LastInstanceDeletesFile(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")

	content := `schema = 1
default = "only"

[instances.only]
type = "glm"
`
	if err := os.WriteFile(tomlPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	ctl := newTestInstancesController(t, tomlPath, dir, stateDir)
	if err := ctl.Remove(appwire.InstanceRemoveParams{Name: "only"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(tomlPath); !os.IsNotExist(err) {
		t.Fatalf("providers.toml still exists after removing the last instance (stat err=%v)", err)
	}
}
