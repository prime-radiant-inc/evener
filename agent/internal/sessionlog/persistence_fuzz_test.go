package sessionlog

import (
	"bytes"
	"testing"

	"github.com/spf13/afero"
)

// FuzzSessionLogPersistence is the differential that proves the afero filesystem
// seam changes nothing observable: the same program of Append operations,
// replayed through two SessionLogs whose ONLY difference is the injected
// afero.Fs — one an OS filesystem sandboxed under a t.TempDir
// (afero.NewBasePathFs over afero.NewOsFs), the other a pure in-memory
// afero.NewMemMapFs — must produce byte-identical persisted JSONL and identical
// error outcomes after every op.
//
// This is both (a) the behavior guard for the seam refactor (production defaults
// to OsFs via NewSessionLog, so if the MemMapFs path ever diverges from the OsFs
// path the refactor is unsound) and (b) a new in-memory persistence fuzzer that
// drives SessionLog.appendToDisk/loadFromDisk with zero real-disk dependency in
// the mem lane.
//
// The fuzzed bytes decode into a sequence of SessionLogEntry values (turn,
// action, outcome, kind, and optional files/failures slices) that are appended
// in lockstep to both logs. Because SessionLog holds no clock and mints no
// timestamps, the two lanes run identical in-memory logic and marshal identical
// bytes; the oracle is the cross-filesystem differential.
//
// Oracle checked after EVERY Append:
//   - error parity: an Append errors on the OS lane iff it errors on the mem lane.
//   - byte-identical persistence: the persisted JSONL file (read back through each
//     lane's own fs at the same logical path) is byte-for-byte equal across the
//     two filesystems.
//
// After the full program, each persisted file is reloaded through a fresh log on
// the same fs and the reloaded Entries() are required to match across the two
// filesystems, proving loadFromDisk agrees across the two too.
//
// SAFETY: the OS lane writes only under a t.TempDir sandbox (BasePathFs pins
// every path beneath it); the mem lane never touches disk. No network, no
// subprocess.
func FuzzSessionLogPersistence(f *testing.F) {
	f.Add([]byte{1, 0, 1, 2, 1, 3})
	f.Add([]byte{7, 1, 0, 2, 1, 1, 9, 2, 3})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0})
	f.Add([]byte{255, 255, 255, 255})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, program []byte) {
		const path = "/logs/session.jsonl"
		osFs := afero.NewBasePathFs(afero.NewOsFs(), t.TempDir())
		memFs := afero.NewMemMapFs()

		osLog := newFsLog(t, osFs, path)
		memLog := newFsLog(t, memFs, path)

		r := &entryReader{b: program}
		const maxOps = 128

		for ops := 0; ops < maxOps && r.more(); ops++ {
			entry := r.entry()

			errOS := osLog.Append(entry)
			errMem := memLog.Append(entry)
			requireErrParity(t, errOS, errMem)

			requireSamePersistedBytes(t, osFs, memFs, path)
		}

		// loadFromDisk must also agree across the two filesystems: reload each
		// persisted file through a fresh log on the same fs and compare views.
		requireSameReload(t, osFs, memFs, path)
	})
}

// newFsLog constructs a SessionLog over the given fs at a fixed logical path.
// The path does not exist yet on a fresh fs, so no initial load occurs.
func newFsLog(t *testing.T, fs afero.Fs, path string) *SessionLog {
	t.Helper()
	log, err := newSessionLogFS(path, fs)
	if err != nil {
		t.Fatalf("construct log: %v", err)
	}
	return log
}

// requireErrParity fails unless both lanes agree on whether the Append errored.
func requireErrParity(t *testing.T, errOS, errMem error) {
	t.Helper()
	if (errOS == nil) != (errMem == nil) {
		t.Fatalf("Append error parity broken: os=%v mem=%v", errOS, errMem)
	}
}

// requireSamePersistedBytes reads the persisted file through each fs at the
// shared logical path and asserts the bytes are identical. A missing file
// (nothing persisted yet) reads as nil on both lanes.
func requireSamePersistedBytes(t *testing.T, osFs, memFs afero.Fs, path string) {
	t.Helper()
	osBytes := readFile(t, osFs, path)
	memBytes := readFile(t, memFs, path)
	if !bytes.Equal(osBytes, memBytes) {
		t.Fatalf("persisted bytes diverge across filesystems:\n os =%q\n mem=%q", osBytes, memBytes)
	}
}

// readFile reads the persisted log file through the given fs; a missing file
// reads as nil.
func readFile(t *testing.T, fs afero.Fs, path string) []byte {
	t.Helper()
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		return nil
	}
	return data
}

// requireSameReload reloads each persisted file through a fresh log on its own
// fs and asserts the reloaded entries marshal identically across the two lanes,
// proving loadFromDisk round-trips the same way on both filesystems.
func requireSameReload(t *testing.T, osFs, memFs afero.Fs, path string) {
	t.Helper()
	osReloaded := newFsLog(t, osFs, path)
	memReloaded := newFsLog(t, memFs, path)
	osView := mustMarshal(t, osReloaded.Entries())
	memView := mustMarshal(t, memReloaded.Entries())
	if !bytes.Equal(osView, memView) {
		t.Fatalf("reload views diverge across filesystems:\n os =%s\n mem=%s", osView, memView)
	}
}

// entryReader decodes a fuzzed byte program into a sequence of SessionLogEntry
// values. Each entry consumes a small, bounded number of bytes; running past
// the end yields zero bytes, so short programs simply produce zero-valued
// entries. The decoding is deterministic and identical for both lanes.
type entryReader struct {
	b   []byte
	pos int
}

func (r *entryReader) more() bool { return r.pos < len(r.b) }

func (r *entryReader) next() byte {
	if r.pos >= len(r.b) {
		return 0
	}
	c := r.b[r.pos]
	r.pos++
	return c
}

// entry draws one SessionLogEntry from the byte stream. The alphabet of actions
// and outcomes is small on purpose so the same entry recurs and the append log
// grows, while the optional Kind/files/failures branches exercise the omitempty
// marshaling paths.
func (r *entryReader) entry() SessionLogEntry {
	actions := []string{"shell", "edit_file", "read_file", "assistant"}
	outcomes := []string{"success", "failure"}
	summaries := []string{"", "did a thing", "line one\nline two", "unicode ✓ tab\there"}

	shape := r.next()
	e := SessionLogEntry{
		Turn:    int(r.next()),
		Action:  actions[int(r.next())%len(actions)],
		Summary: summaries[int(r.next())%len(summaries)],
		Outcome: outcomes[int(r.next())%len(outcomes)],
	}
	if shape&0x01 != 0 {
		e.Kind = "advisory"
	}
	if shape&0x02 != 0 {
		n := int(r.next()%3) + 1
		for i := 0; i < n; i++ {
			e.FilesTouched = append(e.FilesTouched, "path/file")
		}
	}
	if shape&0x04 != 0 {
		e.Failures = append(e.Failures, "boom")
	}
	return e
}
