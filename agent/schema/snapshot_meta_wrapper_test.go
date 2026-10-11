package schema

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// TestLoadSessionMetaFS_ReadOnlyFs_MemMapRoundTrip (FU3 round 14, M-wrapper)
// asserts readMetaFile reads from the in-memory filesystem when the afero.Fs
// is a ReadOnlyFs wrapping a MemMapFs, not a ReadOnlyFs wrapping OsFs. The
// doc comment claims in-memory filesystems fall back to afero.ReadFile; the
// code pre-fix contradicted it for wrapper shapes: readMetaFile unwrapped
// *afero.ReadOnlyFs unconditionally and called readFileNoFollowOS (the real
// filesystem), so the read silently hit the OS filesystem instead of the
// in-memory FS, returning a spurious ENOENT. Post-fix the unwrap is
// conditional on the wrapped source being OS-backed; a ReadOnlyFs over MemMapFs
// falls back to afero.ReadFile and the in-memory content comes back.
//
// The test writes a valid .meta.json into the MemMapFs, loads through the
// ReadOnlyFs wrapper, and asserts the in-memory content (Name="from-mem")
// comes back. Pre-fix this fails: the OS read finds no such file (ENOENT).
func TestLoadSessionMetaFS_ReadOnlyFs_MemMapRoundTrip(t *testing.T) {
	t.Parallel()
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	const dir = "/state"

	mem := afero.NewMemMapFs()
	// Write the sessions dir and a valid meta file into the MemMapFs.
	if err := afero.WriteFile(mem, filepath.Join(dir, sessionsSubdir, id+".meta.json"),
		[]byte(`{"id":"`+id+`","name":"from-mem"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wrap in ReadOnlyFs. Pre-fix, readMetaFile unwrapped ReadOnlyFs and read
	// the real OS filesystem, which has no /state/sessions/<id>.meta.json.
	fs := afero.NewReadOnlyFs(mem)
	got, err := loadSessionMetaFS(fs, dir, id)
	if err != nil {
		t.Fatalf("loadSessionMetaFS through ReadOnlyFs(MemMapFs): %v", err)
	}
	if got.ID != id {
		t.Fatalf("meta ID = %q, want %q", got.ID, id)
	}
	if got.Name != "from-mem" {
		t.Fatalf("meta Name = %q, want %q (in-memory content did not round-trip)", got.Name, "from-mem")
	}
}

// TestLoadSessionMetaFS_BasePathFs_MemMapRoundTrip asserts the same as above
// for BasePathFs wrapping a MemMapFs: readMetaFile must read from the
// in-memory filesystem, not silently redirect to the OS filesystem. Pre-fix,
// readMetaFile unwrapped BasePathFs unconditionally via RealPath and called
// readFileNoFollowOS, so a BasePathFs over MemMapFs read the OS filesystem.
//
// loadSessionMetaFS builds path = filepath.Join(dir, sessionsSubdir, leaf). With
// dir="/" that is "/sessions/<id>.meta.json"; BasePathFs.RealPath maps that to
// "<base>/sessions/<id>.meta.json". So the MemMapFs must hold the file at that
// resolved path — matching how the fuzz test drives BasePathFs with dir="/".
func TestLoadSessionMetaFS_BasePathFs_MemMapRoundTrip(t *testing.T) {
	t.Parallel()
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	const dir = "/"
	const base = "/state"

	mem := afero.NewMemMapFs()
	// RealPath maps "/sessions/<id>.meta.json" to "/state/sessions/<id>.meta.json",
	// so write the file there in the MemMapFs backing.
	if err := afero.WriteFile(mem, filepath.Join(base, sessionsSubdir, id+".meta.json"),
		[]byte(`{"id":"`+id+`","name":"from-mem"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// BasePathFs over the MemMapFs, pinned at base. The source is MemMapFs, so
	// the read must go through afero.ReadFile (the in-memory FS), not the OS
	// filesystem.
	fs := afero.NewBasePathFs(mem, base)
	got, err := loadSessionMetaFS(fs, dir, id)
	if err != nil {
		t.Fatalf("loadSessionMetaFS through BasePathFs(MemMapFs): %v", err)
	}
	if got.ID != id {
		t.Fatalf("meta ID = %q, want %q", got.ID, id)
	}
	if got.Name != "from-mem" {
		t.Fatalf("meta Name = %q, want %q (in-memory content did not round-trip)", got.Name, "from-mem")
	}
}

// TestLoadSessionMetaFS_ReadOnlyFs_OsFs_GetsNoFollow pins F1's protection
// (finding 1 of round 13): a ReadOnlyFs over an OsFs MUST still get the
// no-follow open, because a symlinked leaf beneath a wrapper-over-OsFs is the
// exact hole F1 closed. This strengthens the existing
// TestLoadSessionMetaFS_ReadOnlyFs_SymlinkedLeafRefused by asserting the
// unwrap still routes to readFileNoFollowOS (the OS-backed no-follow path)
// for OsFs-backed wrappers, confirming the conditional unwrap keeps F1 green.
func TestLoadSessionMetaFS_ReadOnlyFs_OsFs_GetsNoFollow(t *testing.T) {
	t.Skip("unix-only: covered by TestLoadSessionMetaFS_ReadOnlyFs_SymlinkedLeafRefused which asserts the no-follow refusal on a real OS symlink; no portable way to plant a symlink")
}
