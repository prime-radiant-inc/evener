package credentials

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStore_LoadMissingFile(t *testing.T) {
	s, err := LoadStore(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("LoadStore missing: %v", err)
	}
	if v, ok := s.Get("anthropic"); v != "" || ok {
		t.Errorf("Get on empty store returned %q/%v, want \"\"/false", v, ok)
	}
	if names := s.Names(); len(names) != 0 {
		t.Errorf("Names on empty store = %v, want none", names)
	}
}

// The store is the file layer and nothing else (spec §10): a key in the
// environment is the registry's business, so Get must not report one.
func TestStore_GetNeverReadsTheEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-key")
	s, err := LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if v, ok := s.Get("openai"); v != "" || ok {
		t.Errorf("Get(openai) = %q/%v with OPENAI_API_KEY set, want \"\"/false", v, ok)
	}
}

func TestStore_SetGetClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	s, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if err := s.Set("anthropic", "sk-ant-1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, ok := s.Get("anthropic"); v != "sk-ant-1" || !ok {
		t.Errorf("Get = %q/%v, want sk-ant-1/true", v, ok)
	}
	// Reload from disk; persistence works.
	s2, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore reload: %v", err)
	}
	if v, ok := s2.Get("anthropic"); v != "sk-ant-1" || !ok {
		t.Errorf("reloaded = %q/%v", v, ok)
	}
	if err := s2.Clear("anthropic"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if v, ok := s2.Get("anthropic"); v != "" || ok {
		t.Errorf("after Clear = %q/%v", v, ok)
	}
	// Verify Clear persists to disk: a fresh LoadStore must not see the key.
	s3, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore after Clear: %v", err)
	}
	if v, ok := s3.Get("anthropic"); v != "" || ok {
		t.Errorf("after Clear+reload = %q/%v, want \"\"/false", v, ok)
	}
}

// An entry whose api_key is blank is not a credential: Get must report it
// missing rather than hand a caller an empty key.
func TestStore_GetIgnoresABlankEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	if err := os.WriteFile(path, []byte("schema = 1\n[providers.work]\napi_key = \"   \"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if v, ok := s.Get("work"); v != "" || ok {
		t.Errorf("Get(work) = %q/%v for a blank api_key, want \"\"/false", v, ok)
	}
}

// Set writes through a temp file and renames, so a reader never sees a
// half-written credentials.toml and the result is never group/world readable.
func TestStore_SetWritesMode0600AndLeavesNoTempFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "credentials.toml")
	s, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if err := s.Set("work", "sk-work"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("credentials.toml mode = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("the temp file survived the rename (stat err = %v)", err)
	}
	// The store must be reloadable through its own mode gate.
	if _, err := LoadStore(path); err != nil {
		t.Fatalf("LoadStore after Set: %v", err)
	}
}

// Names lists every entry, sorted, so a caller can report entries that name
// no instance (spec §14.1).
func TestStore_NamesAreSorted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	content := "schema = 1\n[providers.work]\napi_key = \"w\"\n[providers.anthropic]\napi_key = \"a\"\n[providers.kimi]\napi_key = \"k\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if got, want := s.Names(), []string{"anthropic", "kimi", "work"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Names = %v, want %v", got, want)
	}
}

func TestStore_PathIsTheFileItReadsAndWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	s, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if got := s.Path(); got != path {
		t.Errorf("Path = %q, want %q", got, path)
	}
}

func TestStore_PermissionsEnforced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	if err := os.WriteFile(path, []byte("schema = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStore(path); err == nil {
		t.Errorf("LoadStore should reject 0644-mode file")
	}
}
