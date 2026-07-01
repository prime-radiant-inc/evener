package jobstore

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// FuzzStorePersistence is the differential that proves the afero filesystem seam
// on Store changes nothing observable: the same program of append/reopen/corrupt
// operations, replayed through two Stores whose ONLY difference is the injected
// afero.Fs — one an OS filesystem sandboxed under a t.TempDir (afero.NewBasePathFs
// over afero.NewOsFs), the other a pure in-memory afero.NewMemMapFs — must
// produce byte-identical jobs.jsonl bytes and identical error outcomes after
// every op, and fold identically after a reload.
//
// This is both (a) the behavior guard for the seam refactor (production defaults
// to afero.NewOsFs, which forwards every call to the os package, so if the
// MemMapFs path ever diverges from the OsFs path the refactor is unsound) and
// (b) a new in-memory persistence fuzzer that drives Store.Append/AppendBatch,
// the seq-recovery in Open, and the trailing-partial-line recovery with zero
// real-disk dependency in the mem lane.
//
// The Store mints no timestamps — the only field it assigns is the monotonic
// Seq — so an identical op program appended to two empty stores yields identical
// Seq assignments and thus identical bytes; the per-op event carries a
// deterministic TS derived from the op program itself, so replays are stable and
// the two lanes always receive the exact same Event value.
//
// Oracle checked after EVERY operation:
//   - error parity: an op errors on the OS lane iff it errors on the mem lane.
//   - byte-identical persistence: the jobs.jsonl file (read back through each
//     lane's own fs at the same logical path) is byte-for-byte equal.
//
// After the full program, each lane is reloaded through a fresh openFs on the
// same fs and their folded records are required to match, proving Load agrees
// across the two filesystems too.
//
// SAFETY: the OS lane writes only under a t.TempDir sandbox (BasePathFs pins
// every path beneath it); the mem lane never touches disk. No network, no
// subprocess.
func FuzzStorePersistence(f *testing.F) {
	// A plain append program.
	f.Add([]byte{opAppend, 0, opAppend, 2, opAppend, 3, opAppend, 4})
	// Appends interleaved with reopens (exercises seq recovery through Open).
	f.Add([]byte{opAppend, 0, opReopen, opAppend, 2, opReopen, opAppend, 3})
	// Batches plus a corrupt-tail-then-reopen (exercises trailing recovery).
	f.Add([]byte{opBatch, 3, 0, opCorrupt, 0, opAppend, 1, opCorrupt, 1})
	// Every kind once.
	f.Add([]byte{opAppend, 0, opAppend, 1, opAppend, 2, opAppend, 3, opAppend, 4, opAppend, 5, opAppend, 6, opAppend, 7, opAppend, 8, opAppend, 9})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, program []byte) {
		osLane := newLane(t, afero.NewBasePathFs(afero.NewOsFs(), t.TempDir()))
		memLane := newLane(t, afero.NewMemMapFs())

		r := &progReader{b: program}
		const maxOps = 128
		ec := 0 // event counter → distinct deterministic timestamps

		for ops := 0; ops < maxOps && r.more(); ops++ {
			switch r.next() % opCountP {
			case opAppend:
				e := drawEvent(r, &ec)
				requireErrParity(t, "Append", osLane.s.Append(e), memLane.s.Append(e))
			case opBatch:
				n := int(r.next()%3) + 1
				batch := make([]Event, n)
				for i := range batch {
					batch[i] = drawEvent(r, &ec)
				}
				requireErrParity(t, "AppendBatch", osLane.s.AppendBatch(batch), memLane.s.AppendBatch(batch))
			case opReopen:
				requireErrParity(t, "reopen", osLane.reopen(), memLane.reopen())
			case opCorrupt:
				frag := corruptFragments[int(r.next())%len(corruptFragments)]
				requireErrParity(t, "corrupt", osLane.corruptThenReopen(frag), memLane.corruptThenReopen(frag))
			}
			requireSamePersistedBytes(t, osLane, memLane)
		}

		// Load must also agree across the two filesystems: reload each persisted
		// file through a fresh store on the same fs and compare the outcome. A
		// non-recoverable corrupt tail makes Open legitimately hard-error on the
		// bad line; that error must occur on both lanes or neither, and when both
		// succeed the folded records must match byte-for-byte.
		osFold, osErr := reloadFold(t, osLane)
		memFold, memErr := reloadFold(t, memLane)
		requireErrParity(t, "reloadFold", osErr, memErr)
		if osErr == nil && !bytes.Equal(osFold, memFold) {
			t.Fatalf("reloaded folds diverge across filesystems:\n os =%s\n mem=%s", osFold, memFold)
		}
	})
}

// op codes for the persistence differential program.
const (
	opAppend byte = iota
	opBatch
	opReopen
	opCorrupt
	opCountP
)

// lane is one filesystem under test: its fs, the fixed logical jobs.jsonl path,
// and the live Store (replaced on reopen).
type lane struct {
	t    *testing.T
	fs   afero.Fs
	path string
	s    *Store
}

func newLane(t *testing.T, fs afero.Fs) *lane {
	t.Helper()
	l := &lane{t: t, fs: fs, path: "/jobs.jsonl"}
	s, err := openFs(fs, l.path)
	if err != nil {
		t.Fatalf("initial open: %v", err)
	}
	l.s = s
	return l
}

// reopen closes the live store and opens a fresh one on the same fs, exercising
// Open's seq recovery from existing content.
func (l *lane) reopen() error {
	_ = l.s.Close()
	s, err := openFs(l.fs, l.path)
	if err != nil {
		return err
	}
	l.s = s
	return nil
}

// corruptThenReopen closes the store, appends a raw (unterminated) fragment to
// the tail through the fs, then reopens — driving recoverTrailingPartialLine.
func (l *lane) corruptThenReopen(frag []byte) error {
	_ = l.s.Close()
	f, err := l.fs.OpenFile(l.path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, werr := f.Write(frag); werr != nil {
		_ = f.Close()
		return werr
	}
	if cerr := f.Close(); cerr != nil {
		return cerr
	}
	s, err := openFs(l.fs, l.path)
	if err != nil {
		return err
	}
	l.s = s
	return nil
}

// corruptFragments are unterminated trailing bytes appended after a clean,
// newline-terminated log to drive the trailing-partial-line recovery path.
var corruptFragments = [][]byte{
	[]byte(`{"kind":"job_started","job_id":"job_9"`),
	[]byte(`{"kind":"job_finished","job_id":"job_9","status":"comple`),
	[]byte(`{"seq":42,"kind":`),
	[]byte(`garbage without a brace`),
}

// progReader reads the fuzz program one byte at a time, yielding 0 past the end.
type progReader struct {
	b   []byte
	pos int
}

func (r *progReader) more() bool { return r.pos < len(r.b) }

func (r *progReader) next() byte {
	if r.pos >= len(r.b) {
		return 0
	}
	v := r.b[r.pos]
	r.pos++
	return v
}

var (
	fuzzJobIDs   = []string{"job_1", "job_2", "job_3"}
	fuzzSessions = []string{"s1", "s2"}
	fuzzStatuses = []Status{StatusCompleted, StatusFailed, StatusCancelled, StatusStopped}
	fuzzEpoch    = time.Unix(1_700_000_000, 0).UTC()
)

// drawEvent builds one legal Event from the program bytes with a deterministic
// timestamp. The same Event value is handed to both lanes, so identical Seq
// assignment yields identical persisted bytes.
func drawEvent(r *progReader, ec *int) Event {
	*ec++
	e := Event{
		TS:    fuzzEpoch.Add(time.Duration(*ec) * time.Second),
		JobID: fuzzJobIDs[int(r.next())%len(fuzzJobIDs)],
	}
	sess := fuzzSessions[int(r.next())%len(fuzzSessions)]
	switch r.next() % 10 {
	case 0:
		e.Kind = EventJobStarted
		e.Type = JobShell
		e.Command = "cmd"
		e.OwnerSessionID = sess
		e.VisibleToSession = sess
	case 1:
		did := "dlg_" + sess
		e.Kind = EventDelegateCreated
		e.DelegateID = did
		e.Delegate = &DelegateEvent{ChildSessionID: "c_" + did, AgentType: "engineer", Generation: "g1", Resumable: true}
	case 2:
		e.Kind = EventJobStarted
		e.Type = JobDelegate
		e.DelegateID = "dlg_" + sess
		e.OwnerSessionID = sess
		e.VisibleToSession = sess
	case 3:
		e.Kind = EventJobFinished
		e.Status = fuzzStatuses[int(r.next())%len(fuzzStatuses)]
		e.TerminalGen = "tg1"
	case 4:
		e.Kind = EventJobNotificationPending
		e.TerminalGen = "tg1"
	case 5:
		e.Kind = EventJobNotificationDelivered
		e.TerminalGen = "tg1"
	case 6:
		e.Kind = EventWatchRegistered
		e.WatchID = "watch_" + sess
		e.Watch = &WatchEvent{Generation: "wg1", OwnerSessionID: sess, VisibleSessionID: sess, Target: "job_target", ConfigHash: "h1"}
	case 7:
		e.Kind = EventWatchCleared
		e.WatchID = "watch_" + sess
		e.Watch = &WatchEvent{Generation: "wg1", EndReason: "done"}
	case 8:
		e.Kind = EventWatchSendPending
		e.WatchSend = &WatchSendState{
			Key:        WatchSendKey{VisibleSessionID: sess, WatchID: "watch_" + sess, WatchTarget: "job_t", ResolvedWatchedIdentity: "job_t", ResolvedSendTo: sess, WatchGeneration: "wg1"},
			DeliveryID: "wd1",
			UpdateSeq:  uint64(*ec),
			Message:    "m",
		}
	case 9:
		e.Kind = EventWatchReadGrant
		e.ObserverSessionID = sess
	}
	return e
}

// requireErrParity fails unless both lanes agree on whether the op errored.
func requireErrParity(t *testing.T, op string, errOS, errMem error) {
	t.Helper()
	if (errOS == nil) != (errMem == nil) {
		t.Fatalf("%s error parity broken: os=%v mem=%v", op, errOS, errMem)
	}
}

// requireSamePersistedBytes reads each lane's jobs.jsonl through its own fs at
// the shared logical path and asserts the bytes are identical.
func requireSamePersistedBytes(t *testing.T, osLane, memLane *lane) {
	t.Helper()
	osBytes := readLaneFile(osLane)
	memBytes := readLaneFile(memLane)
	if !bytes.Equal(osBytes, memBytes) {
		t.Fatalf("persisted bytes diverge across filesystems:\n os =%q\n mem=%q", osBytes, memBytes)
	}
}

// readLaneFile reads the persisted jobs.jsonl through the lane's own fs. A
// missing file reads as nil, matching on both lanes.
func readLaneFile(l *lane) []byte {
	data, err := afero.ReadFile(l.fs, l.path)
	if err != nil {
		return nil
	}
	return data
}

// reloadFold reopens the persisted log through a fresh store on the lane's fs
// and returns the marshaled folded records, proving Load round-trips what the
// store wrote on that filesystem. It returns any Open/parse error rather than
// failing, so the caller can assert the error occurs identically on both lanes.
func reloadFold(t *testing.T, l *lane) ([]byte, error) {
	t.Helper()
	_ = l.s.Close()
	s, err := openFs(l.fs, l.path)
	if err != nil {
		return nil, err
	}
	records, err := s.LoadOrdered()
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(records)
	if err != nil {
		t.Fatalf("marshal records: %v", err)
	}
	return out, nil
}
