//go:build unix

package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A regular file under a pseudo root is the kmsg shape: it reports itself
// regular, its stat means nothing, and without O_NONBLOCK its read can block
// in read(2) where neither cancellation nor a deadline can reach it (#4574
// round 4, HIGH). The admitting open keeps the flag for a regular file whose
// resolved path sits inside a pseudo root — one reached through a walk root
// inside the pseudo tree, or through a leaf symlink that aliases into it —
// so the runtime poller owns the descriptor and the capped read's deadline
// can interrupt the stream. An ordinary regular file still gets the ordinary
// blocking descriptor, and keeping the flag on a real disk file must not
// change what a search of it finds.
func TestGrepAdmitKeepsPseudoRootedRegularsNonblocking(t *testing.T) {
	// Not parallel: stubPseudoFsRoots swaps the package's root list.
	root := t.TempDir()
	pseudo := filepath.Join(root, "pseudo")
	if err := os.MkdirAll(pseudo, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(pseudo, "keep.txt")
	if err := os.WriteFile(keep, []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(root, "plain.txt")
	if err := os.WriteFile(plain, []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(keep, filepath.Join(root, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	stubPseudoFsRoots(t, pseudo)

	for _, tc := range []struct {
		name    string
		fsys    grepAdmitFS
		open    string
		wantSet bool
	}{
		{
			name:    "named target inside a pseudo root",
			fsys:    grepAdmitFS{FS: os.DirFS(pseudo), root: pseudo, ctx: t.Context(), rootPseudo: grepRootIsPseudo(pseudo)},
			open:    "keep.txt",
			wantSet: true,
		},
		{
			name:    "symlink leaf into a pseudo root",
			fsys:    grepAdmitFS{FS: os.DirFS(root), root: root, ctx: t.Context()},
			open:    "alias.txt",
			wantSet: true,
		},
		{
			name:    "ordinary regular file",
			fsys:    grepAdmitFS{FS: os.DirFS(root), root: root, ctx: t.Context()},
			open:    "plain.txt",
			wantSet: false,
		},
	} {
		f, err := tc.fsys.Open(tc.open)
		if err != nil {
			t.Fatalf("%s: open: %v", tc.name, err)
		}
		cf, ok := f.(*grepCtxFile)
		if !ok {
			_ = f.Close()
			t.Fatalf("%s: open returned %T, want *grepCtxFile", tc.name, f)
		}
		set := fdNonblocking(t, cf.File)
		_ = cf.Close()
		if set != tc.wantSet {
			t.Fatalf("%s: O_NONBLOCK set = %v, want %v", tc.name, set, tc.wantSet)
		}
	}
}

// Keeping O_NONBLOCK on a regular file must not change what a search of it
// finds: a named target under a (stubbed) pseudo root still reads back its
// matches through the full grep path, on the off arm as on the sandboxed one.
func TestGrepReadsAStubbedPseudoRootedRegularFile(t *testing.T) {
	// Not parallel: stubPseudoFsRoots swaps the package's root list.
	root := t.TempDir()
	pseudo := filepath.Join(root, "pseudo")
	if err := os.MkdirAll(pseudo, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(pseudo, "keep.txt")
	if err := os.WriteFile(keep, []byte("needle here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubPseudoFsRoots(t, pseudo)

	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }

	got, err := env.Grep(t.Context(), "needle", keep, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	if want := "1:needle here"; got != want {
		t.Fatalf("named pseudo-rooted regular = %q, want %q", got, want)
	}
}
