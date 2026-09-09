package credentials

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/spf13/afero"
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

// TestStore_ConcurrentAccessIsRaceFree drives Set/Get/Clear/Names from many
// goroutines against one Store: the hub's auth controller shares exactly one
// Store across every evener/auth/* RPC plus evener/instance/remove, and
// AppWire serializes requests only within a single connection - two browser
// tabs (or a browser and the TUI) calling ApiKeySet/Logout/ApiKeyClear/
// Remove/Status concurrently must not race the underlying map or corrupt
// the on-disk file via colliding .tmp writes. `go test -race` is what makes
// this test meaningful; without it, a torn map access can pass silently.
func TestStore_ConcurrentAccessIsRaceFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	s, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}

	const goroutines = 8
	const iterations = 50
	var wg sync.WaitGroup
	for g := range goroutines {
		name := fmt.Sprintf("provider-%d", g)
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			for range iterations {
				if err := s.Set(name, "sk-test"); err != nil {
					t.Errorf("Set(%s): %v", name, err)
				}
				// Get/Names only need to not race or panic here - a
				// concurrent Clear from another goroutine legitimately owns
				// whether this particular read observes the key.
				s.Get(name)
				s.Names()
				if err := s.Clear(name); err != nil {
					t.Errorf("Clear(%s): %v", name, err)
				}
			}
		}(name)
	}
	wg.Wait()

	// The store must still be in a coherent, reloadable state: every
	// goroutine's last op was Clear, so nothing should remain, and the file
	// itself must parse (not a half-written .tmp left over from a collision).
	reloaded, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore after concurrent access: %v", err)
	}
	if names := reloaded.Names(); len(names) != 0 {
		t.Errorf("Names after concurrent access = %v, want none", names)
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

// The hub answers auth status from the in-memory map and reloads the registry
// from the file, so a mutation whose save failed must leave memory exactly as
// the file still reads: a key remembered but not written is one the
// credentials pane reports and no launch can resolve.
func TestStore_SetRestoresMemoryWhenTheSaveFails(t *testing.T) {
	const path = "/creds/credentials.toml"
	mem := afero.NewMemMapFs()
	s := mustLoadFS(t, mem, path)
	if err := s.Set("work", "sk-old"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	s.fs = afero.NewReadOnlyFs(mem)

	if err := s.Set("work", "sk-new"); err == nil {
		t.Fatal("Set over a read-only filesystem returned nil")
	}
	if v, ok := s.Get("work"); v != "sk-old" || !ok {
		t.Errorf("Get after a failed overwrite = %q/%v, want sk-old/true", v, ok)
	}
	if err := s.Set("personal", "sk-p"); err == nil {
		t.Fatal("Set of a new name over a read-only filesystem returned nil")
	}
	if v, ok := s.Get("personal"); ok {
		t.Errorf("a failed Set of a name that had no entry left %q behind", v)
	}
}

func TestStore_ClearRestoresMemoryWhenTheSaveFails(t *testing.T) {
	const path = "/creds/credentials.toml"
	mem := afero.NewMemMapFs()
	s := mustLoadFS(t, mem, path)
	if err := s.Set("work", "sk-work"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	s.fs = afero.NewReadOnlyFs(mem)

	if err := s.Clear("work"); err == nil {
		t.Fatal("Clear over a read-only filesystem returned nil")
	}
	if v, ok := s.Get("work"); v != "sk-work" || !ok {
		t.Errorf("Get after a failed Clear = %q/%v, want sk-work/true", v, ok)
	}
}

// Move is what a renamed instance carries its key with: one persist, so the
// key is never briefly filed under both names or neither.
func TestStore_MoveCarriesTheKeyToTheNewName(t *testing.T) {
	const path = "/creds/credentials.toml"
	mem := afero.NewMemMapFs()
	s := mustLoadFS(t, mem, path)
	if err := s.Set("work", "sk-work"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set("other", "sk-other"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := s.Move("work", "personal"); err != nil {
		t.Fatalf("Move: %v", err)
	}

	if v, ok := s.Get("personal"); v != "sk-work" || !ok {
		t.Errorf("Get(personal) = %q/%v, want sk-work/true", v, ok)
	}
	if v, ok := s.Get("work"); ok {
		t.Errorf("the old name still holds %q", v)
	}
	// The file is the layer a registry reload reads, so it has to carry the
	// same move, and carry nothing else away with it.
	reloaded := mustLoadFS(t, mem, path)
	if v, ok := reloaded.Get("personal"); v != "sk-work" || !ok {
		t.Errorf("reloaded Get(personal) = %q/%v, want sk-work/true", v, ok)
	}
	if v, ok := reloaded.Get("work"); ok {
		t.Errorf("the persisted file still holds the old name: %q", v)
	}
	if v, ok := reloaded.Get("other"); v != "sk-other" || !ok {
		t.Errorf("an untouched entry changed: %q/%v", v, ok)
	}
}

func TestStore_MoveRestoresBothNamesWhenTheSaveFails(t *testing.T) {
	const path = "/creds/credentials.toml"
	mem := afero.NewMemMapFs()
	s := mustLoadFS(t, mem, path)
	if err := s.Set("work", "sk-work"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	s.fs = afero.NewReadOnlyFs(mem)

	if err := s.Move("work", "personal"); err == nil {
		t.Fatal("Move over a read-only filesystem returned nil")
	}
	if v, ok := s.Get("work"); v != "sk-work" || !ok {
		t.Errorf("Get(work) after a failed Move = %q/%v, want sk-work/true", v, ok)
	}
	if v, ok := s.Get("personal"); ok {
		t.Errorf("a failed Move left %q under the new name", v)
	}
	reloaded := mustLoadFS(t, mem, path)
	if v, ok := reloaded.Get("work"); v != "sk-work" || !ok {
		t.Errorf("the persisted file lost the key: work = %q/%v", v, ok)
	}
	if v, ok := reloaded.Get("personal"); ok {
		t.Errorf("the persisted file holds the new name: %q", v)
	}
}

// A rename of an instance that never had a stored key has nothing to move,
// and must not report that as a failure.
func TestStore_MoveOfAMissingNameIsANoOp(t *testing.T) {
	const path = "/creds/credentials.toml"
	mem := afero.NewMemMapFs()
	s := mustLoadFS(t, mem, path)
	if err := s.Set("personal", "sk-personal"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	s.fs = afero.NewReadOnlyFs(mem)

	if err := s.Move("work", "personal"); err != nil {
		t.Fatalf("Move of a missing name = %v, want nil", err)
	}
	if v, ok := s.Get("personal"); v != "sk-personal" || !ok {
		t.Errorf("the destination entry changed: %q/%v", v, ok)
	}
}

// The store lower-cases every name, so a rename that only changes case names
// one entry twice: moving it would delete the key it had just copied.
func TestStore_MoveToTheSameKeyKeepsTheEntry(t *testing.T) {
	const path = "/creds/credentials.toml"
	mem := afero.NewMemMapFs()
	s := mustLoadFS(t, mem, path)
	if err := s.Set("work", "sk-work"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := s.Move("Work", "work"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if v, ok := s.Get("work"); v != "sk-work" || !ok {
		t.Errorf("Get(work) = %q/%v, want sk-work/true", v, ok)
	}
}
