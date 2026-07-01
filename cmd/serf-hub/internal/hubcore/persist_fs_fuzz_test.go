package hubcore

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// FuzzHubcorePersistFS is the differential that proves hubcore's afero
// filesystem seam changes nothing observable: the same program of persistence
// filesystem operations, replayed through two filesystems whose ONLY difference
// is the afero.Fs backing them — one an OS filesystem sandboxed under a
// t.TempDir (afero.NewBasePathFs over afero.NewOsFs), the other a pure
// in-memory afero.NewMemMapFs — must produce an identical resulting filesystem
// tree (paths + dir/file kind + content bytes) and identical error outcomes
// after every op.
//
// It drives the two hubcore persistence helpers that are FULLY afero-mediated:
//   - ensureDir            (roster.go)  — the runDir MkdirAll(0o700)
//   - chmodSQLiteIndexFilesFS (past.go) — the SQLite index chmod-sidecars loop,
//     which tolerates a missing sidecar via os.IsNotExist
//
// plus the afero primitives they compose against (WriteFile / Remove / MkdirAll)
// to set up and perturb the on-disk state between helper calls.
//
// SCOPE NOTE: hubcore's rich persistence (the PastIndex FTS index and the
// ArchiveStore) is SQLite-backed. modernc.org/sqlite opens its database file on
// the real OS filesystem by path and does NOT route through afero, so those
// decode/parse paths cannot be driven OsFs-vs-MemMapFs (the mem lane's DB file
// would not exist on real disk). The afero seam therefore covers only the
// surrounding directory/metadata ops, which is exactly what this differential
// exercises. See the fs-field notes on PastIndex/ArchiveStore.
//
// Oracle checked after EVERY operation:
//   - error parity: an op errors on the OS lane iff it errors on the mem lane.
//     (Error *classification* only — the two backends word missing-path errors
//     differently, but os.IsNotExist agrees, and here nil-vs-non-nil is enough.)
//   - filesystem-state parity: walking each filesystem from "/" yields the same
//     set of (path, isDir, content) tuples. File modes are deliberately NOT
//     compared: OsFs applies the process umask on create while MemMapFs stores
//     the requested mode verbatim, a benign backend quirk modeled out here.
//
// SAFETY: the OS lane writes only under a t.TempDir sandbox (BasePathFs pins
// every path beneath it); the mem lane never touches disk. No SQLite, no
// network, no subprocess.
func FuzzHubcorePersistFS(f *testing.F) {
	// op, operand... programs. Ops: 0=mkdir(1) 1=write(2) 2=remove(1) 3=chmoddb(0).
	f.Add([]byte{opMkdir, 1, opWrite, 0, 42, opChmodDB, opWrite, 4, 7, opRemove, 4})
	f.Add([]byte{opWrite, 0, 1, opWrite, 1, 2, opWrite, 2, 3, opWrite, 3, 4, opChmodDB})
	f.Add([]byte{opChmodDB, opChmodDB, opMkdir, 2, opMkdir, 3})
	f.Add([]byte{opRemove, 0, opRemove, 4, opChmodDB})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, program []byte) {
		osFs := afero.NewBasePathFs(afero.NewOsFs(), t.TempDir())
		memFs := afero.NewMemMapFs()

		r := &persistOpReader{b: program}
		const maxOps = 128

		for ops := 0; ops < maxOps && r.more(); ops++ {
			switch r.next() % persistOpCount {
			case opMkdir:
				dir := persistDirs[int(r.next())%len(persistDirs)]
				errOS := ensureDir(osFs, dir)
				errMem := ensureDir(memFs, dir)
				requirePersistErrParity(t, "mkdir", errOS, errMem)
			case opWrite:
				file := persistFiles[int(r.next())%len(persistFiles)]
				content := []byte{r.next()}
				errOS := writePersistFile(osFs, file, content)
				errMem := writePersistFile(memFs, file, content)
				requirePersistErrParity(t, "write", errOS, errMem)
			case opRemove:
				file := persistFiles[int(r.next())%len(persistFiles)]
				errOS := osFs.Remove(file)
				errMem := memFs.Remove(file)
				requirePersistErrParity(t, "remove", errOS, errMem)
			case opChmodDB:
				errOS := chmodSQLiteIndexFilesFS(osFs, persistDBBase)
				errMem := chmodSQLiteIndexFilesFS(memFs, persistDBBase)
				requirePersistErrParity(t, "chmoddb", errOS, errMem)
			}

			requireSameTree(t, osFs, memFs)
		}
	})
}

// opcodes for the persistence program.
const (
	opMkdir = iota
	opWrite
	opRemove
	opChmodDB
	persistOpCount
)

// persistDBBase is the logical SQLite index path; its 3 sidecars share the prefix.
const persistDBBase = "/idx/index.db"

// persistDirs is the fixed set of directories ensureDir may create. All are
// ordinary nested paths (no parent-is-a-regular-file constructions), keeping the
// program inside the region where OsFs and MemMapFs are known to agree.
var persistDirs = []string{"/idx", "/idx/a", "/idx/a/b", "/idx/c"}

// persistFiles is the fixed set of files the program may write/remove: the
// SQLite index base plus its 3 chmod sidecars, plus two ordinary files.
var persistFiles = []string{
	persistDBBase,
	persistDBBase + "-journal",
	persistDBBase + "-wal",
	persistDBBase + "-shm",
	"/idx/other.txt",
	"/idx/a/nested.txt",
}

// writePersistFile ensures the parent directory exists (so a missing-parent
// error can't diverge between the two backends — OsFs errors, MemMapFs is
// lenient) and then writes the file. Both backends then behave identically.
func writePersistFile(fs afero.Fs, path string, content []byte) error {
	if err := fs.MkdirAll(parentDir(path), 0o700); err != nil {
		return err
	}
	return afero.WriteFile(fs, path, content, 0o644)
}

// parentDir returns the directory component of a "/"-rooted logical path.
func parentDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return "/"
}

// requirePersistErrParity fails unless both lanes agree on whether the op errored.
func requirePersistErrParity(t *testing.T, op string, errOS, errMem error) {
	t.Helper()
	if (errOS == nil) != (errMem == nil) {
		t.Fatalf("%s error parity broken: os=%v mem=%v", op, errOS, errMem)
	}
}

// requireSameTree walks both filesystems from "/" and asserts an identical set
// of (path, isDir, content) tuples. Modes are intentionally excluded.
func requireSameTree(t *testing.T, osFs, memFs afero.Fs) {
	t.Helper()
	osTree := walkTree(t, osFs)
	memTree := walkTree(t, memFs)
	if osTree != memTree {
		t.Fatalf("filesystem trees diverge across backends:\n os =%s\n mem=%s", osTree, memTree)
	}
}

// walkTree renders a filesystem's full contents (below "/") as a stable,
// order-independent string of "path|isDir|content" lines for comparison.
func walkTree(t *testing.T, fs afero.Fs) string {
	t.Helper()
	var lines []string
	err := afero.Walk(fs, "/", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == "/" {
			return nil
		}
		if info.IsDir() {
			lines = append(lines, path+"|dir|")
			return nil
		}
		data, rerr := afero.ReadFile(fs, path)
		if rerr != nil {
			return rerr
		}
		lines = append(lines, fmt.Sprintf("%s|file|%x", path, data))
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// persistOpReader draws opcodes and operands from the fuzz program, one byte at
// a time, reporting exhaustion via more().
type persistOpReader struct {
	b   []byte
	pos int
}

func (r *persistOpReader) more() bool { return r.pos < len(r.b) }

func (r *persistOpReader) next() byte {
	if r.pos >= len(r.b) {
		return 0
	}
	v := r.b[r.pos]
	r.pos++
	return v
}
