package main

// Tests for Phase 2: hubInstancesController Create/Edit/Remove/SetDefault/List.
//
// Each test uses a temp dir for providers.toml, credentials.toml, and OAuth
// state so tests remain fully isolated from the developer environment.

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/internal/appwire"
	authopenai "primeradiant.com/serf/internal/auth/openai"
	"primeradiant.com/serf/internal/auth/openai/oaitest"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/internal/providerconfig"
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

// writeEmptyDirProvidersToml creates the directory for providers.toml but does
// NOT write the file (tests Create from scratch need the file absent but the
// directory present for WriteFile).
func ensureParentDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
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
	reloaded, exists, err := providerconfig.LoadFile(tomlPath)
	if err != nil {
		t.Fatalf("LoadFile after Create: %v", err)
	}
	if !exists {
		t.Fatal("providers.toml absent after Create")
	}
	var diskInst *providerconfig.InstanceConfig
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
	reloaded, exists, err := providerconfig.LoadFile(tomlPath)
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
	reloaded, exists, err := providerconfig.LoadFile(tomlPath)
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
// 9. After Create, List reads fresh from disk and reflects the new instance
// ─────────────────────────────────────────────────────────────────────────────

func TestInstances_Create_ListReflectsNewInstance(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)

	ctl := newTestInstancesController(t, tomlPath, dir, stateDir)

	if err := ctl.Create(appwire.InstanceCreateParams{Type: "anthropic", Name: "newone"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// List must read fresh from disk and include the new instance.
	resp := ctl.List()
	found := false
	for _, inst := range resp.Instances {
		if inst.Name == "newone" {
			found = true
			break
		}
	}
	if !found {
		t.Error("List did not include 'newone' after Create")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 10. List: IsDefault is correctly set; sort is Type then Name
// ─────────────────────────────────────────────────────────────────────────────

func TestInstances_List_IsDefaultAndSort(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	content := `schema = 1
default = "bravo"

[instances.alpha]
type = "anthropic"

[instances.bravo]
type = "openai"
`
	if err := os.WriteFile(tomlPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}

	ctl := newTestInstancesController(t, tomlPath, dir, stateDir)
	resp := ctl.List()

	if len(resp.Instances) != 2 {
		t.Fatalf("len(Instances) = %d, want 2", len(resp.Instances))
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

	// Sort: anthropic (alpha) before openai (bravo) by type, then name.
	if resp.Instances[0].Type != "anthropic" || resp.Instances[0].Name != "alpha" {
		t.Errorf("first entry = %q/%q, want anthropic/alpha", resp.Instances[0].Type, resp.Instances[0].Name)
	}
	if resp.Instances[1].Type != "openai" || resp.Instances[1].Name != "bravo" {
		t.Errorf("second entry = %q/%q, want openai/bravo", resp.Instances[1].Type, resp.Instances[1].Name)
	}
}
