package rendezvous

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// FuzzRendezvousPersistence is the differential that proves the afero filesystem
// seam changes nothing: the same program of rendezvous operations (Write /
// Remove / List), replayed through two filesystems whose ONLY difference is the
// injected afero.Fs — one an OS filesystem sandboxed under a t.TempDir
// (afero.NewBasePathFs over afero.NewOsFs), the other a pure in-memory
// afero.NewMemMapFs — must produce byte-identical persisted files and identical
// error and List outcomes after every op.
//
// This is both (a) the behavior guard for the seam refactor (production defaults
// to afero.NewOsFs(), which forwards every call to the os package, so if the
// MemMapFs path ever diverges from the OsFs path the refactor is unsound) and
// (b) a pure in-memory persistence fuzzer that drives writeFS/removeFS/listFS
// with zero real-disk dependency in the mem lane.
//
// Entries carry their own StartedAt timestamp (Write mints no time of its own),
// so the program supplies deterministic timestamps directly and no clock seam is
// needed for parity.
//
// Oracle checked after EVERY operation:
//   - error parity: an op errors on the OS lane iff it errors on the mem lane.
//   - byte-identical persistence: every file under the rendezvous dir (name +
//     bytes) is identical across the two filesystems.
//   - List parity: List returns the identical entry slice (marshaled) on both.
//
// SAFETY: the OS lane writes only under a t.TempDir sandbox (BasePathFs pins
// every path beneath it); the mem lane never touches disk. Every op targets a
// single fixed rendezvous directory that is always a real directory, so no path
// component is ever a file — the one construction where MemMapFs's lenient
// MkdirAll/Rename semantics would legitimately diverge from the OS and would be a
// modeled-out quirk rather than a product bug. No network, no subprocess.
func FuzzRendezvousPersistence(f *testing.F) {
	f.Add([]byte{opWrite, 1, opWrite, 2, opList})
	f.Add([]byte{opWrite, 7, opRemove, 7, opList})
	f.Add([]byte{opWrite, 3, opWrite, 3, opRemove, 9, opList})
	f.Add([]byte{opRemove, 5, opList, opWrite, 200})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, program []byte) {
		const dir = "/run"
		osFs := afero.NewBasePathFs(afero.NewOsFs(), t.TempDir())
		memFs := afero.NewMemMapFs()

		r := &opReader{b: program}
		const maxOps = 128

		for ops := 0; ops < maxOps && r.more(); ops++ {
			switch r.next() % opCount {
			case opWrite:
				entry := r.entry()
				_, errOS := writeFS(osFs, dir, entry)
				_, errMem := writeFS(memFs, dir, entry)
				requireErrParity(t, "Write", errOS, errMem)
			case opRemove:
				pid := r.pid()
				errOS := removeFS(osFs, dir, pid)
				errMem := removeFS(memFs, dir, pid)
				requireErrParity(t, "Remove", errOS, errMem)
			case opList:
				osEntries, errOS := listFS(osFs, dir)
				memEntries, errMem := listFS(memFs, dir)
				requireErrParity(t, "List", errOS, errMem)
				if !bytes.Equal(marshal(t, osEntries), marshal(t, memEntries)) {
					t.Fatalf("List diverges across filesystems:\n os =%s\n mem=%s",
						marshal(t, osEntries), marshal(t, memEntries))
				}
			}

			requireSameDir(t, osFs, memFs, dir)
		}

		// Final full-directory + List comparison after the whole program.
		requireSameDir(t, osFs, memFs, dir)
		osEntries, err := listFS(osFs, dir)
		if err != nil {
			t.Fatalf("final List (os) failed: %v", err)
		}
		memEntries, err := listFS(memFs, dir)
		if err != nil {
			t.Fatalf("final List (mem) failed: %v", err)
		}
		if !bytes.Equal(marshal(t, osEntries), marshal(t, memEntries)) {
			t.Fatalf("final List diverges across filesystems")
		}
	})
}

// opReader is a cursor over the fuzz program that draws operations and their
// parameters one byte at a time.
type opReader struct {
	b []byte
	i int
}

const (
	opWrite = iota
	opRemove
	opList
	opCount
)

func (r *opReader) more() bool { return r.i < len(r.b) }

func (r *opReader) next() byte {
	if r.i >= len(r.b) {
		return 0
	}
	v := r.b[r.i]
	r.i++
	return v
}

// pid draws a small process id. The bounded range forces collisions across
// Write/Remove ops so the fuzzer exercises overwrite and delete-existing paths.
func (r *opReader) pid() int { return int(r.next()) }

// entry builds an Entry with a deterministic StartedAt so both lanes marshal the
// identical bytes. A few string fields are drawn from the program to vary the
// serialized payload (and thus the persisted bytes) across ops.
func (r *opReader) entry() Entry {
	pid := r.pid()
	return Entry{
		PID:       pid,
		Address:   fmt.Sprintf("127.0.0.1:%d", r.next()),
		Provider:  drawString(r.next()),
		Model:     drawString(r.next()),
		HubToken:  drawString(r.next()),
		StartedAt: time.Unix(int64(pid), 0).UTC(),
	}
}

// drawString maps a byte to one of a small fixed set of tokens (including the
// empty string, which exercises omitempty on the JSON side).
func drawString(b byte) string {
	tokens := []string{"", "openai", "anthropic", "gpt-5.5", "sess-1", "tok"}
	return tokens[int(b)%len(tokens)]
}

// requireErrParity fails unless both lanes agree on whether the op errored.
func requireErrParity(t *testing.T, op string, errOS, errMem error) {
	t.Helper()
	if (errOS == nil) != (errMem == nil) {
		t.Fatalf("%s error parity broken: os=%v mem=%v", op, errOS, errMem)
	}
}

// requireSameDir asserts the two filesystems hold the identical set of files
// (by name) with byte-identical contents under dir.
func requireSameDir(t *testing.T, osFs, memFs afero.Fs, dir string) {
	t.Helper()
	osSnap := dirSnapshot(t, osFs, dir)
	memSnap := dirSnapshot(t, memFs, dir)
	if len(osSnap) != len(memSnap) {
		t.Fatalf("directory file set diverges: os=%v mem=%v", keys(osSnap), keys(memSnap))
	}
	for name, osData := range osSnap {
		memData, ok := memSnap[name]
		if !ok {
			t.Fatalf("file %q present on os lane, missing on mem lane", name)
		}
		if !bytes.Equal(osData, memData) {
			t.Fatalf("file %q bytes diverge:\n os =%s\n mem=%s", name, osData, memData)
		}
	}
}

// dirSnapshot reads every regular file directly under dir into a name→bytes map.
// A missing directory yields an empty map (both lanes start empty).
func dirSnapshot(t *testing.T, fs afero.Fs, dir string) map[string][]byte {
	t.Helper()
	infos, err := afero.ReadDir(fs, dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string][]byte{}
		}
		t.Fatalf("snapshot ReadDir: %v", err)
	}
	out := make(map[string][]byte, len(infos))
	for _, info := range infos {
		if info.IsDir() {
			continue
		}
		data, err := afero.ReadFile(fs, dir+"/"+info.Name())
		if err != nil {
			t.Fatalf("snapshot ReadFile %q: %v", info.Name(), err)
		}
		out[info.Name()] = data
	}
	return out
}

// keys returns the sorted file names of a snapshot for legible failure output.
func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// marshal serializes a slice of entries for cross-lane comparison.
func marshal(t *testing.T, entries []Entry) []byte {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal entries: %v", err)
	}
	return data
}
