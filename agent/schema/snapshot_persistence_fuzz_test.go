package schema

import (
	"bytes"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// FuzzSessionMetaPersistence is the differential that proves the afero
// filesystem seam beneath SaveSessionMeta/LoadSessionMeta/ListSessionMetas
// changes nothing: the same program of persistence operations, replayed through
// two lanes whose ONLY difference is the injected afero.Fs — one an OS
// filesystem sandboxed under a t.TempDir (afero.NewBasePathFs over
// afero.NewOsFs), the other a pure in-memory afero.NewMemMapFs — must produce
// byte-identical on-disk state and identical error outcomes after every op.
//
// This is both (a) the behavior guard for the seam refactor (production defaults
// to OsFs via the exported functions, so if the MemMapFs path ever diverges from
// the OsFs path the refactor is unsound) and (b) an in-memory persistence fuzzer
// that drives the save/load/list seams with zero real-disk dependency in the mem
// lane.
//
// Time is supplied deterministically: each Save mints its meta's UpdatedAt from
// a shared monotonically advancing counter, so the two lanes write identical
// bytes and ListSessionMetas' UpdatedAt-descending sort is exercised
// deterministically (no wall-clock nondeterminism enters the oracle).
//
// Oracle checked after EVERY operation:
//   - error parity: an op errors on the OS lane iff it errors on the mem lane.
//   - byte-identical on-disk state: the full sessions/ file tree (every path and
//     its contents) read back through each lane's own fs is byte-for-byte equal.
//
// For Load and List the returned values (marshaled meta / marshaled list) are
// also required equal across the two lanes.
//
// SAFETY: the OS lane writes only under a t.TempDir sandbox (BasePathFs pins
// every path beneath it) and session ids are drawn from a fixed safe pool so no
// fuzzed byte can craft a path-traversal filename; the mem lane never touches
// disk. No network, no subprocess.
func FuzzSessionMetaPersistence(f *testing.F) {
	// Seeds: opcodes are (op, id, profile, name, note, turns) tuples; trailing
	// bytes are consumed as available and default to zero when exhausted.
	f.Add([]byte{opSave, 0, 1, 2, 0, 7, opSave, 1, 0, 1, 3, 4, opList})
	f.Add([]byte{opSave, 0, 0, 0, 0, 0, opLoad, 0, opLoad, 1, opList})
	f.Add([]byte{opSave, 3, 2, 3, 1, 9, opSave, 3, 1, 0, 0, 2, opLoad, 3, opList})
	f.Add([]byte{opList, opLoad, 2, opSave, 2, 1, 1, 1, 1})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, program []byte) {
		const dir = "/"
		osFs := afero.NewBasePathFs(afero.NewOsFs(), t.TempDir())
		memFs := afero.NewMemMapFs()

		r := &metaOpReader{b: program}
		var tick int64
		const maxOps = 128

		for ops := 0; ops < maxOps && r.more(); ops++ {
			switch r.next() % opMetaCount {
			case opSave:
				tick++
				meta := r.drawMeta(time.Unix(tick*60, 0).UTC())
				errOS := saveSessionMetaFS(osFs, dir, meta)
				errMem := saveSessionMetaFS(memFs, dir, meta)
				requireMetaErrParity(t, "SaveSessionMeta", errOS, errMem)
			case opLoad:
				id := metaIDPool[int(r.next())%len(metaIDPool)]
				gotOS, errOS := loadSessionMetaFS(osFs, dir, id)
				gotMem, errMem := loadSessionMetaFS(memFs, dir, id)
				requireMetaErrParity(t, "LoadSessionMeta", errOS, errMem)
				if errOS == nil && !bytes.Equal(marshalMeta(t, gotOS), marshalMeta(t, gotMem)) {
					t.Fatalf("LoadSessionMeta value diverges across filesystems for id=%s", id)
				}
			case opList:
				listOS, errOS := listSessionMetasFS(osFs, dir)
				listMem, errMem := listSessionMetasFS(memFs, dir)
				requireMetaErrParity(t, "ListSessionMetas", errOS, errMem)
				if errOS == nil && !bytes.Equal(marshalMetas(t, listOS), marshalMetas(t, listMem)) {
					t.Fatalf("ListSessionMetas value diverges across filesystems:\n os =%s\n mem=%s",
						marshalMetas(t, listOS), marshalMetas(t, listMem))
				}
			}

			requireSameFileTree(t, osFs, memFs)
		}
	})
}

// Op codes for the persistence differential program.
const (
	opSave = iota
	opLoad
	opList
	opMetaCount
)

// metaIDPool is the fixed set of safe session ids the fuzzer draws from. Keeping
// ids to a small collision-prone pool exercises overwrite (rename-over) and
// makes filenames well-formed regardless of the fuzzed bytes (no path
// traversal). Every id is also a valid path component on both filesystems.
var metaIDPool = []string{"a", "b", "c", "d"}

// metaProfilePool / metaNamePool / metaNotePool are small value pools so drawn
// metas vary (producing distinct persisted bytes) while staying deterministic.
var (
	metaProfilePool = []string{"", "openai", "anthropic", "kimi"}
	metaNamePool    = []string{"", "Session title", "Fix the bug", "Another"}
	metaNotePool    = []string{"", "note", "remember this"}
)

// metaOpReader is a byte cursor over the fuzzed program. Reads past the end
// return 0, so short programs are well-defined rather than rejected.
type metaOpReader struct {
	b []byte
	i int
}

func (r *metaOpReader) more() bool { return r.i < len(r.b) }

func (r *metaOpReader) next() byte {
	if r.i >= len(r.b) {
		return 0
	}
	v := r.b[r.i]
	r.i++
	return v
}

// drawMeta builds a SessionMeta from the next program bytes with the supplied
// deterministic UpdatedAt. CreatedAt mirrors UpdatedAt so the marshaled bytes
// are fully determined by the draw and the clock.
func (r *metaOpReader) drawMeta(now time.Time) SessionMeta {
	id := metaIDPool[int(r.next())%len(metaIDPool)]
	profile := metaProfilePool[int(r.next())%len(metaProfilePool)]
	name := metaNamePool[int(r.next())%len(metaNamePool)]
	note := metaNotePool[int(r.next())%len(metaNotePool)]
	turns := int(r.next())
	return SessionMeta{
		ID:         id,
		ProfileID:  profile,
		Name:       name,
		PinnedNote: note,
		TurnCount:  turns,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// requireMetaErrParity fails unless both lanes agree on whether the op errored.
func requireMetaErrParity(t *testing.T, op string, errOS, errMem error) {
	t.Helper()
	if (errOS == nil) != (errMem == nil) {
		t.Fatalf("%s error parity broken: os=%v mem=%v", op, errOS, errMem)
	}
}

// requireSameFileTree asserts the two filesystems hold byte-identical state
// under the sessions/ directory: the same set of relative paths, each with
// identical contents. This is the core "persisted bytes identical" oracle.
// Keys are relative to the sessions dir so the comparison is independent of how
// each fs renders the absolute root (BasePathFs strips its base; MemMapFs keeps
// the leading slash).
func requireSameFileTree(t *testing.T, osFs, memFs afero.Fs) {
	t.Helper()
	root := "/" + sessionsSubdir
	osTree := readTree(t, osFs, root)
	memTree := readTree(t, memFs, root)
	if !sameTree(osTree, memTree) {
		t.Fatalf("on-disk state diverges across filesystems:\n os =%v\n mem=%v",
			treeKeys(osTree), treeKeys(memTree))
	}
}

// readTree walks root on fs and returns a map of every regular file's path
// (relative to root) to its bytes. A missing root yields an empty map (both
// lanes start empty).
func readTree(t *testing.T, fs afero.Fs, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	exists, err := afero.DirExists(fs, root)
	if err != nil {
		t.Fatalf("DirExists(%s): %v", root, err)
	}
	if !exists {
		return out
	}
	err = afero.Walk(fs, root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := afero.ReadFile(fs, path)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(path, root)
		out[rel] = data
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

func sameTree(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || !bytes.Equal(va, vb) {
			return false
		}
	}
	return true
}

func treeKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func marshalMeta(t *testing.T, m SessionMeta) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	return b
}

func marshalMetas(t *testing.T, ms []SessionMeta) []byte {
	t.Helper()
	b, err := json.Marshal(ms)
	if err != nil {
		t.Fatalf("marshal metas: %v", err)
	}
	return b
}
