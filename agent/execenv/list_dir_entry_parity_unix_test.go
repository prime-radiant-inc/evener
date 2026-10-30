//go:build unix

package execenv

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/agent/sandbox"
)

// The off-sandbox walks (legacy and budgeted) and the sandboxed walks (legacy
// and budgeted) each assemble a DirEntry. They must agree field for field on
// every kind of entry, since the list_dir tool renders the same row from any of
// them.
func TestListDirectoryEntriesAgreeAcrossWalks(t *testing.T) {
	sandboxed, _, root := sandboxedEnv(t, sandbox.ModeReadOnly)
	for rel, mode := range map[string]os.FileMode{"plain.txt": 0o644, "run.sh": 0o755, "sub/inner.txt": 0o600, "sub/deeper/leaf": 0o755} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("content of "+rel), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("plain.txt", filepath.Join(root, "link-to-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub", filepath.Join(root, "link-to-dir")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	plain := NewLocalExecutionEnvironment(root)

	want, err := plain.ListDirectory(root, 3)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]DirEntry{}
	for _, e := range want {
		kinds[e.Name] = e
	}
	// The fixture must exercise each kind, or equal output proves nothing.
	if e := kinds["run.sh"]; !e.IsExec || !e.IsRegular || e.Size == 0 {
		t.Fatalf("executable entry = %+v", e)
	}
	if e := kinds["link-to-file"]; !e.IsSymlink || e.IsRegular {
		t.Fatalf("symlink entry = %+v", e)
	}
	if e := kinds["pipe"]; e.IsRegular || e.IsDir {
		t.Fatalf("fifo entry = %+v", e)
	}
	if e := kinds["sub"]; !e.IsDir {
		t.Fatalf("directory entry = %+v", e)
	}

	budgeted := func(env *LocalExecutionEnvironment) []DirEntry {
		t.Helper()
		got, err := env.ListDirectoryWithBudget(t.Context(), root, 3, NewDirListBudget(0, 1000, 0))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	legacySandboxed, err := sandboxed.ListDirectory(root, 3)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string][]DirEntry{
		"unsandboxed budgeted": budgeted(plain),
		"sandboxed legacy":     legacySandboxed,
		"sandboxed budgeted":   budgeted(sandboxed),
	} {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s walk differs from the unsandboxed legacy walk:\n got %+v\nwant %+v", name, got, want)
		}
	}
}
