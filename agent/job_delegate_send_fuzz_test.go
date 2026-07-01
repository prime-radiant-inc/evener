package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/fuzz/fault"
)

// This file fuzzes Session.sendDelegateMessage (job_delegate.go) — the
// delegate_send dispatcher — plus the send-path routing it drives
// (sendRunningDelegateMessage, resumeOrFindRunningDelegate) that the prior
// watch-delegate lane did not exercise (that lane hit assessDelegateResumability
// and restoreTerminalDelegateChildClaimed directly). Here those restore helpers
// are reached only incidentally, as steps of the real send flow.
//
// The dispatcher is a long chain of validation + store-lookup + routing branches:
// arg validation (empty target/message, negative timeout, bad on_idle, reserved
// aliases, transcript-ref prefixes), the runtime "caller" alias route, the job_/
// dlg_ target shapes, ownership + type + status checks on the resolved job record,
// the running/terminal split, and the restore-then-resume tail. A menu of seeded
// delegates (running, terminal, shell-typed, owner-mismatched, history-less,
// bad-ref, restorable-runtime-lost) plus an optionally installed caller route lets
// fuzzed args land in every arm. Persist faults are injected through the
// appendEvent seam via fuzz/fault so the resume/finalize error tails are driven
// too. Everything stays inside a t.TempDir / MemMapFs sandbox — no network, no
// process, no disk outside the sandbox; a scripted fake adapter (newSession's
// default) answers "done" so any launched resume run terminates immediately.
//
// ORACLES (real preserved-invariant / well-formed-result, not bare never-panic):
//   - Target passthrough: every returned sendMessageResult carries
//     Target == strings.TrimSpace(args.Target) — the dispatcher never loses or
//     rewrites the caller's target on any arm.
//   - Success shape: a nil-Err result always names an Action (delivered/steered/
//     started); a hard-failure delivery class always carries a non-nil Err.
//   - Delegate-set stability: a send never creates or destroys a delegate, so the
//     store's delegate count is identical before and after (a resume mints a new
//     job under the SAME delegate id, not a new delegate).
//   - Store integrity under fault: faults are injected at the append seam BEFORE
//     the real write, so the on-disk store is never corrupted — LoadDelegates
//     still succeeds afterward.
//
// All new top-level identifiers carry the dgfz_ lane prefix so parallel lanes
// editing package agent never collide.

// --- byte-stream reader ---

type dgfz_reader struct {
	data []byte
	pos  int
}

func (r *dgfz_reader) b() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	v := r.data[r.pos]
	r.pos++
	return v
}

func (r *dgfz_reader) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.b()) % n
}

func (r *dgfz_reader) boolean() bool { return r.b()&1 == 1 }

func (r *dgfz_reader) pick(opts []string) string {
	if len(opts) == 0 {
		return ""
	}
	return opts[r.intn(len(opts))]
}

func (r *dgfz_reader) take(n int) []byte {
	out := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, r.b())
	}
	return out
}

// --- fault gate over the append seam ---

// dgfz_faultGate derives a persist-failure decision from a fuzz-byte plan using
// fuzz/fault. It decorates an in-memory afero.Fs whose single gate file exists, so
// a Stat succeeds unless the fault Schedule trips — turning the library's
// deterministic FS-fault schedule into an append-seam fault the resume/finalize
// error branches can be driven with. The gate wraps writes only, never reads, so
// the on-disk store is never corrupted (LoadDelegates stays truthful).
func dgfz_faultGate(plan []byte) func() error {
	sched := fault.FromBytes(plan)
	if !sched.Active() {
		return func() error { return nil }
	}
	mem := afero.NewMemMapFs()
	_ = afero.WriteFile(mem, "gate", []byte("x"), 0o644)
	ffs := fault.FS(mem, sched)
	return func() error {
		if _, err := ffs.Stat("gate"); err != nil {
			return err
		}
		return nil
	}
}

// --- seeded delegate menu ---

// dgfz_delegateTargets collects the target strings the fuzzer may aim a send at,
// one per seeded delegate shape, so a fuzzed target index reaches every dispatch
// arm rather than always erroring out early.
type dgfz_delegateTargets struct {
	all []string
}

const dgfz_otherSession = "OTHER-SESSION"

// dgfz_appendDelegate seeds one delegate (created + optional job started + optional
// terminal finish) directly through the append seam. owner controls the resolved
// job record's OwnerSessionID (mismatch drives the not-controllable arm).
func dgfz_appendDelegate(t *testing.T, s *Session, dlgID, jobID, ref, owner string, jobType jobstore.JobType, withJob bool, terminal bool, status jobstore.Status, reason string) {
	t.Helper()
	now := time.Now().UTC()
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateCreated,
		TS:         now,
		DelegateID: dlgID,
		Delegate: &jobstore.DelegateEvent{
			ChildSessionID:   strings.TrimPrefix(ref, "local:"),
			TranscriptRef:    ref,
			OwnerSessionID:   owner,
			VisibleSessionID: owner,
			Generation:       jobstore.NewDelegateGeneration(),
			Resumable:        true,
		},
	}); err != nil {
		t.Fatalf("dgfz: seed delegate created: %v", err)
	}
	if !withJob {
		return
	}
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            jobID,
		DelegateID:       dlgID,
		Type:             jobType,
		Task:             "seeded",
		OwnerSessionID:   owner,
		VisibleToSession: owner,
		StartedAt:        &now,
		TranscriptRef:    ref,
	}); err != nil {
		t.Fatalf("dgfz: seed delegate job started: %v", err)
	}
	if !terminal {
		return
	}
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          now,
		JobID:       jobID,
		Status:      status,
		Reason:      reason,
		EndedAt:     &now,
		TerminalGen: jobstore.NewWatchGeneration(),
	}); err != nil {
		t.Fatalf("dgfz: seed delegate job finished: %v", err)
	}
}

// dgfz_seedDelegates installs the full menu and returns the candidate target set.
func dgfz_seedDelegates(t *testing.T, s *Session) dgfz_delegateTargets {
	t.Helper()
	self := s.ID()

	// restorable runtime-lost delegate (restore + resume tail).
	restorable := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, restorable)

	// running, retained-runtime absent -> "not retained" arm.
	runDlg := jobstore.NewDelegateID()
	runJob := jobstore.NewJobID()
	dgfz_appendDelegate(t, s, runDlg, runJob, encodeRef("", "child_run"), self, jobstore.JobDelegate, true, false, "", "")

	// running with a malformed transcript ref -> decodeRef error arm.
	badRefDlg := jobstore.NewDelegateID()
	badRefJob := jobstore.NewJobID()
	dgfz_appendDelegate(t, s, badRefDlg, badRefJob, "bogus-ref", self, jobstore.JobDelegate, true, false, "", "")

	// terminal (stopped) with a malformed ref -> terminal-path decodeRef error arm.
	termBadDlg := jobstore.NewDelegateID()
	termBadJob := jobstore.NewJobID()
	dgfz_appendDelegate(t, s, termBadDlg, termBadJob, "bogus-ref", self, jobstore.JobDelegate, true, true, jobstore.StatusStopped, "gone")

	// resolved job is shell-typed -> not-messageable arm.
	shellDlg := jobstore.NewDelegateID()
	shellJob := jobstore.NewJobID()
	dgfz_appendDelegate(t, s, shellDlg, shellJob, encodeRef("", "child_shell"), self, jobstore.JobShell, true, false, "", "")

	// resolved job owned by a descendant session -> not-controllable arm.
	ownDlg := jobstore.NewDelegateID()
	ownJob := jobstore.NewJobID()
	dgfz_appendDelegate(t, s, ownDlg, ownJob, encodeRef("", "child_owned"), dgfz_otherSession, jobstore.JobDelegate, true, false, "", "")

	// delegate with no job history -> target_not_resumable(no job history) arm.
	noHistDlg := jobstore.NewDelegateID()
	dgfz_appendDelegate(t, s, noHistDlg, "", encodeRef("", "child_nohist"), self, jobstore.JobDelegate, false, false, "", "")

	return dgfz_delegateTargets{all: []string{
		// reserved aliases & scheme prefixes (early validation arms)
		"caller", "main", "watched", "local:x", "proj:b:x",
		// job_ target shapes
		restorable.JobID, ownJob, "job_bogus",
		// dlg_ targets across the seeded menu
		restorable.DelegateID, runDlg, badRefDlg, termBadDlg, shellDlg, ownDlg, noHistDlg,
		"dlg_missing",
		// garbage / empty
		"", "   ", "nonsense",
	}}
}

var dgfz_onIdle = []string{"", "fail", "start", "bogus"}
var dgfz_messages = []string{"do a thing", "", "   "}

// dgfz_installCallerRoute optionally arms a parent caller route so the "caller"
// runtime alias arm (and its FromWatch/undeliverable variants) is reachable.
func dgfz_installCallerRoute(s *Session, mode int, deliver bool) {
	switch mode {
	case 1:
		s.cfg.spawn.parentSteer = func(string, *provenance.Causal) {}
		s.cfg.spawn.parentJobID = "job_parent"
	case 2:
		s.cfg.spawn.parentSteerDelivered = func(string, *provenance.Causal) bool { return deliver }
		s.cfg.spawn.parentMarkCallerCallbackDelivered = func(string) {}
		s.cfg.spawn.parentJobID = "job_parent"
	}
}

// dgfz_drawArgs builds a sendMessageArgs from fuzz bytes and the seeded target
// menu. It consumes a FIXED 6 bytes per call (no conditional consumption); the
// fuzz loop reads one more byte for the ctx selector, so the per-send stride is a
// stable 7 bytes. That stability is what lets dgfz_seed below aim seeds at every
// dispatch arm regardless of field values. Out-of-bytes reads yield 0, so a short
// input is a legal prefix.
func dgfz_drawArgs(r *dgfz_reader, targets dgfz_delegateTargets) sendMessageArgs {
	a := sendMessageArgs{
		Target:    targets.all[r.intn(len(targets.all))],
		Message:   r.pick(dgfz_messages),
		OnIdle:    r.pick(dgfz_onIdle),
		FromWatch: r.boolean(),
	}
	switch r.intn(3) {
	case 1:
		a.BackgroundSet = true
		a.Background = false
	case 2:
		a.BackgroundSet = true
		a.Background = true
	}
	switch r.intn(4) {
	case 0:
		a.BlockTimeoutMS = -1
	case 1:
		a.BlockTimeoutMS = 0
	case 2:
		a.BlockTimeoutMS = 5
	default:
		a.BlockTimeoutMS = 20
	}
	return a
}

// Target indices into dgfz_seedDelegates's returned menu (order-locked); the seed
// builder aims at arms by index so a decoded seed reaches a known dispatch branch.
const (
	dgfz_tCaller        = 0
	dgfz_tMain          = 1
	dgfz_tWatched       = 2
	dgfz_tLocal         = 3
	dgfz_tProj          = 4
	dgfz_tRestorableJob = 5
	dgfz_tOwnJob        = 6
	dgfz_tJobBogus      = 7
	dgfz_tRestorableDlg = 8
	dgfz_tRunDlg        = 9
	dgfz_tBadRefDlg     = 10
	dgfz_tTermBadDlg    = 11
	dgfz_tShellDlg      = 12
	dgfz_tOwnDlg        = 13
	dgfz_tNoHistDlg     = 14
	dgfz_tDlgMissing    = 15
	dgfz_tEmpty         = 16
	dgfz_tWhitespace    = 17
	dgfz_tNonsense      = 18
)

const (
	dgfz_msgReal  = 0 // "do a thing"
	dgfz_msgEmpty = 1
	dgfz_onStart  = 2
)

// dgfz_send encodes one send block (7 bytes) for the seed builder. background: 0
// unset, 1 set-false (foreground), 2 set-true (background).
func dgfz_send(target, msg, onIdle int, fromWatch bool, background, blockMode int) []byte {
	fw := byte(0)
	if fromWatch {
		fw = 1
	}
	return []byte{byte(target), byte(msg), byte(onIdle), fw, byte(background), byte(blockMode), 1}
}

// dgfz_seed assembles a full fuzz input: caller-route mode + a no-fault plan +
// nSends + the send blocks. Byte layout mirrors FuzzDgfzSendDelegateMessage's
// fixed-width reader. faultByte fills the 4-byte fault plan (use a non-multiple of
// 4 for "no fault"; 0 to arm the persist-fault gate).
func dgfz_seed(callerMode int, faultByte byte, sends ...[]byte) []byte {
	return dgfz_seedD(callerMode, false, faultByte, sends...)
}

// dgfz_seedD is dgfz_seed with an explicit caller-deliver bit (byte 1), so the
// parentSteerDelivered delivery/undeliverable arms of the "caller" alias can be
// aimed at deterministically.
func dgfz_seedD(callerMode int, callerDeliver bool, faultByte byte, sends ...[]byte) []byte {
	n := len(sends)
	if n == 0 {
		n = 1
	}
	deliver := byte(0)
	if callerDeliver {
		deliver = 1
	}
	out := []byte{byte(callerMode), deliver, faultByte, faultByte, faultByte, faultByte, byte(n - 1)}
	for _, s := range sends {
		out = append(out, s...)
	}
	return out
}

// dgfz_checkResult asserts the well-formed-result oracles on one send outcome.
func dgfz_checkResult(t *testing.T, args sendMessageArgs, res sendMessageResult) {
	t.Helper()
	if want := strings.TrimSpace(args.Target); res.Target != want {
		t.Fatalf("dgfz: result target = %q, want trimmed arg target %q", res.Target, want)
	}
	if res.Err == nil && res.Action == "" {
		t.Fatalf("dgfz: nil-error result names no action: %+v", res)
	}
	if res.WatchSendDeliveryClassSet && res.WatchSendDeliveryClass == watchSendHardFailure && res.Err == nil {
		t.Fatalf("dgfz: hard-failure delivery class with no error: %+v", res)
	}
}

// FuzzDgfzSendDelegateMessage drives sendDelegateMessage against one real Session
// seeded with the delegate menu, injecting persist faults through the append seam,
// and asserts the result + delegate-set invariants after every send.
func FuzzDgfzSendDelegateMessage(f *testing.F) {
	noFault := byte(1) // 1%4 != 0 -> the fault gate never trips

	// One seed per dispatch arm, plus multi-send and fault variants. The builder
	// aims each send at a target index (dgfz_t*), so a decoded seed deterministically
	// reaches a known branch — coverage-guided mutation then explores around them.
	seeds := [][]byte{
		{}, // empty input -> all-zero prefix
		dgfz_seed(1, noFault, dgfz_send(dgfz_tCaller, dgfz_msgReal, dgfz_onStart, false, 0, 2)),         // caller alias, parentSteer route
		dgfz_seedD(2, true, noFault, dgfz_send(dgfz_tCaller, dgfz_msgReal, dgfz_onStart, false, 0, 2)),  // caller, parentSteerDelivered -> delivered
		dgfz_seedD(2, false, noFault, dgfz_send(dgfz_tCaller, dgfz_msgReal, dgfz_onStart, false, 0, 2)), // caller, parentSteerDelivered -> undeliverable
		dgfz_seed(2, noFault, dgfz_send(dgfz_tCaller, dgfz_msgReal, dgfz_onStart, true, 0, 2)),          // caller + FromWatch (internal-bug arm)
		dgfz_seed(0, noFault, dgfz_send(dgfz_tCaller, dgfz_msgReal, dgfz_onStart, false, 0, 2)),         // caller without route
		dgfz_seed(0, noFault, dgfz_send(dgfz_tRestorableDlg, dgfz_msgReal, 1, false, 0, 2)),             // terminal + on_idle=fail -> target_idle
		dgfz_seed(0, noFault, dgfz_send(dgfz_tMain, dgfz_msgReal, dgfz_onStart, false, 0, 2)),           // reserved alias
		dgfz_seed(0, noFault, dgfz_send(dgfz_tWatched, dgfz_msgReal, dgfz_onStart, false, 0, 2)),        // reserved alias
		dgfz_seed(0, noFault, dgfz_send(dgfz_tLocal, dgfz_msgReal, dgfz_onStart, false, 0, 2)),          // transcript-ref prefix
		dgfz_seed(0, noFault, dgfz_send(dgfz_tProj, dgfz_msgReal, dgfz_onStart, false, 0, 2)),           // transcript-ref prefix
		dgfz_seed(0, noFault, dgfz_send(dgfz_tRestorableJob, dgfz_msgReal, dgfz_onStart, false, 0, 2)),  // job_ handle -> use delegate_id
		dgfz_seed(0, noFault, dgfz_send(dgfz_tOwnJob, dgfz_msgReal, dgfz_onStart, false, 0, 2)),         // job_ owned by descendant
		dgfz_seed(0, noFault, dgfz_send(dgfz_tJobBogus, dgfz_msgReal, dgfz_onStart, false, 0, 2)),       // job_ not found
		dgfz_seed(0, noFault, dgfz_send(dgfz_tRestorableDlg, dgfz_msgReal, dgfz_onStart, false, 2, 2)),  // restore+resume, background
		dgfz_seed(0, noFault, dgfz_send(dgfz_tRestorableDlg, dgfz_msgReal, dgfz_onStart, false, 1, 2)),  // restore+resume, foreground wait
		dgfz_seed(0, noFault, // resume then immediately re-send (running/steer arm)
			dgfz_send(dgfz_tRestorableDlg, dgfz_msgReal, dgfz_onStart, false, 2, 2),
			dgfz_send(dgfz_tRestorableDlg, dgfz_msgReal, dgfz_onStart, false, 2, 2)),
		dgfz_seed(0, noFault, // resume then FromWatch re-send (watch-steer branches)
			dgfz_send(dgfz_tRestorableDlg, dgfz_msgReal, dgfz_onStart, false, 2, 2),
			dgfz_send(dgfz_tRestorableDlg, dgfz_msgReal, dgfz_onStart, true, 2, 2)),
		dgfz_seed(0, noFault, dgfz_send(dgfz_tRunDlg, dgfz_msgReal, dgfz_onStart, false, 0, 2)),         // running, sub not retained
		dgfz_seed(0, noFault, dgfz_send(dgfz_tBadRefDlg, dgfz_msgReal, dgfz_onStart, false, 0, 2)),      // running, bad transcript ref
		dgfz_seed(0, noFault, dgfz_send(dgfz_tTermBadDlg, dgfz_msgReal, dgfz_onStart, false, 0, 2)),     // terminal, bad transcript ref
		dgfz_seed(0, noFault, dgfz_send(dgfz_tShellDlg, dgfz_msgReal, dgfz_onStart, false, 0, 2)),       // shell-typed -> not messageable
		dgfz_seed(0, noFault, dgfz_send(dgfz_tOwnDlg, dgfz_msgReal, dgfz_onStart, false, 0, 2)),         // owned by descendant
		dgfz_seed(0, noFault, dgfz_send(dgfz_tNoHistDlg, dgfz_msgReal, dgfz_onStart, false, 0, 2)),      // no job history
		dgfz_seed(0, noFault, dgfz_send(dgfz_tDlgMissing, dgfz_msgReal, dgfz_onStart, false, 0, 2)),     // dlg_ not found
		dgfz_seed(0, noFault, dgfz_send(dgfz_tNoHistDlg, dgfz_msgReal, 3, false, 0, 2)),                 // on_idle=bogus -> validation arm
		dgfz_seed(0, noFault, dgfz_send(dgfz_tRestorableDlg, dgfz_msgReal, dgfz_onStart, false, 0, 0)),  // negative max_wait arm
		dgfz_seed(0, noFault, dgfz_send(dgfz_tRestorableDlg, dgfz_msgEmpty, dgfz_onStart, false, 0, 2)), // empty message arm
		dgfz_seed(0, noFault, dgfz_send(dgfz_tEmpty, dgfz_msgReal, dgfz_onStart, false, 0, 2)),          // empty target arm
		dgfz_seed(0, noFault, dgfz_send(dgfz_tNonsense, dgfz_msgReal, dgfz_onStart, false, 0, 2)),       // unrecognized target
		dgfz_seed(0, 0, dgfz_send(dgfz_tRestorableDlg, dgfz_msgReal, dgfz_onStart, false, 2, 2)),        // resume under persist fault
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &dgfz_reader{data: data}
		s := newSession(t, withConfig(SessionConfig{
			StateDir:         t.TempDir(),
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
		}))

		targets := dgfz_seedDelegates(t, s)
		dgfz_installCallerRoute(s, r.intn(3), r.boolean())

		before, err := s.jobManager.store.LoadDelegates()
		if err != nil {
			t.Fatalf("dgfz: LoadDelegates(before): %v", err)
		}
		delegatesBefore := len(before)

		// Arm the append fault gate AFTER seeding so faults hit only the send path.
		origAppend := s.jobManager.appendEvent
		origAppends := s.jobManager.appendEvents
		gate := dgfz_faultGate(r.take(4))
		s.jobManager.appendEvent = func(e jobstore.Event) error {
			if ferr := gate(); ferr != nil {
				return ferr
			}
			return origAppend(e)
		}
		s.jobManager.appendEvents = func(es []jobstore.Event) error {
			if ferr := gate(); ferr != nil {
				return ferr
			}
			return origAppends(es)
		}
		t.Cleanup(func() {
			s.jobManager.appendEvent = origAppend
			s.jobManager.appendEvents = origAppends
		})

		nSends := r.intn(3) + 1
		for i := 0; i < nSends; i++ {
			args := dgfz_drawArgs(r, targets)
			var ctx context.Context
			if r.boolean() {
				ctx = context.Background()
			} // else nil ctx -> exercise the nil-context arm
			res := s.sendDelegateMessage(ctx, args)
			dgfz_checkResult(t, args, res)
		}

		// Delegate-set stability: a send never mints or drops a delegate. Reads go
		// straight to the store (the gate wraps writes only), so this also proves the
		// store stayed uncorrupted under any injected persist fault.
		after, err := s.jobManager.store.LoadDelegates()
		if err != nil {
			t.Fatalf("dgfz: LoadDelegates(after): %v", err)
		}
		if len(after) != delegatesBefore {
			t.Fatalf("dgfz: delegate count changed by a send: before=%d after=%d", delegatesBefore, len(after))
		}
	})
}
