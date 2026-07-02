package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/fuzz/promoter"
	"primeradiant.com/serf/llm"
)

// TestDelegateSeqFuzz is a stateful/sequence fuzz of the DELEGATE state machine.
// The delegate lifecycle — create → send/resume → finalize → restore-from-meta —
// is deep orchestration whose bugs live in the TRANSITION SEQUENCES (resume after
// finalize, double-finalize, restore of a running delegate, allowance accounting
// across grants), not in any single call. A rapid.Check state machine draws a
// short op sequence and replays it against a REAL agent.Session + jobManager built
// entirely offline: a real (local, temp-dir) exec env, an injected fake clock (all
// timers/sleeps are virtual), StateDir on a temp dir (so restore-from-meta reads
// real on-disk child meta + transcript), and a stateless scripted adapter whose
// every response is a terminal communicate/end_turn — so every delegate child turn
// terminates in one round with no wall-clock wait.
//
// Every delegate op is FOREGROUND (Background:false): createDelegate and
// sendDelegateMessage block until the spawned/resumed child quiesces, so no two
// child turns overlap and the shared adapter never races. That determinism is what
// lets the model be a monotonic prediction the oracles check after each op.
//
// The op model (declarative; a replay never touches rapid):
//
//	dsOpCreate:        createDelegate(foreground) with a fuzzed delegation_allowance.
//	dsOpResume:        sendDelegateMessage(on_idle=start, foreground) to a tracked,
//	                   terminal delegate — the resume-after-finalize transition.
//	dsOpFinalizeAgain: finalizeDelegate again on a tracked terminal job — the
//	                   double-finalize transition (must be an idempotent no-op).
//	dsOpRestoreSeed:   seed a stopped (runtime_lost) delegate restore record on disk,
//	                   optionally corrupt its descriptor/files, then assess + restore.
//	dsOpAdvanceClock:  advance virtual time (fires any armed timer/watchdog).
//	dsOpObserve:       read State/QueueDepth/job list at a boundary.
//	dsOpClose:         Close (idempotent; once closed, stays closed).
//
// Oracles, weakest-first (checked after each op):
//
//	O1 (panic/wedge):  no op panics; each runs under a recovering goroutine and a
//	                   real wall-time watchdog — a hang is a wedge (a deadlock or a
//	                   leaked never-joining goroutine surfaces here or at Close).
//	O2 (status):       State() at a boundary is idle or closed; once closed it stays.
//	O3 (allowance):    a created child's delegationAllowance == the granted value,
//	                   is >= 0, is strictly less than the parent's own allowance, and
//	                   its depth == parent depth + 1. A grant >= own allowance is
//	                   rejected (never spawns).
//	O4 (lifecycle):    terminal-stickiness — once a job is observed terminal it never
//	                   reappears non-terminal nor flips to a DIFFERENT terminal status
//	                   (survives finalize/resume/restore churn); resumability only ever
//	                   holds a terminal delegate, never re-marks it running.
//	O5 (no leaked job): every stored delegate record with status Running has a live
//	                   runtime in jm.running — no delegate is left Running with no
//	                   runtime after the sequence + final quiesce.
//	O6 (store):        every tracked delegate is present in the folded store (its
//	                   latest job terminal) and in the folded delegate table.
//	O7 (restore):      validator agreement — a record the machine deems resumable
//	                   satisfies validateDelegateRestoreState (pure gate == ""); one
//	                   the pure gate rejects is never accepted; a well-formed
//	                   runtime_lost record restores to an IDLE (not running) child; a
//	                   corrupted one is rejected; a RUNNING delegate is never restored
//	                   as a live runtime.
//
// A discovered failure is routed through fuzz/promoter (mirroring TestLifecycleSeqFuzz):
// a deterministic reproduction survives the flake-guard and is emitted as a
// regression test; a flaky one is quarantined.
func TestDelegateSeqFuzz(t *testing.T) {
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	emitDir, bucketsPath, _ := promoter.PersistPaths(pkgDir, t.TempDir(), filepath.Join(t.TempDir(), "buckets.json"))
	adapter := &ds_promoAdapter{emitDir: emitDir}
	store, err := promoter.OpenBucketStore(bucketsPath)
	if err != nil {
		t.Fatalf("OpenBucketStore: %v", err)
	}
	promo := promoter.New(adapter, store, ds_quietQuarantiner{}, 5)

	var captured *promoter.Failure
	t.Cleanup(func() {
		if captured == nil {
			return
		}
		out, err := promo.Promote(context.Background(), *captured)
		t.Logf("delegate-seq-fuzz failure promoted: outcome=%v err=%v detail=%q", out, err, captured.Detail)
	})

	rapid.Check(t, func(rt *rapid.T) {
		art := ds_drawArtifact(rt)
		if f := ds_oracleRun(art); f != nil {
			captured = f
			rt.Fatalf("delegate oracle: %s", f.Detail)
		}
	})
}

// --- op model ---

type ds_opCode int

const (
	dsOpCreate ds_opCode = iota
	dsOpResume
	dsOpFinalizeAgain
	dsOpRestoreSeed
	dsOpAdvanceClock
	dsOpObserve
	dsOpClose
)

var ds_allOps = []ds_opCode{
	dsOpCreate, dsOpResume, dsOpFinalizeAgain, dsOpRestoreSeed,
	dsOpAdvanceClock, dsOpObserve, dsOpClose,
}

// Restore-seed mutations: dsMutNone yields a well-formed runtime_lost delegate
// (must be resumable); each other kind corrupts the descriptor or its on-disk
// state so the validator/assessment must reject, or so restore must refuse.
const (
	dsMutNone             = iota
	dsMutBadParent        // desc.ParentSessionID mismatch -> parent linkage
	dsMutClearWorkDir     // desc.WorkingDir empty -> parent linkage
	dsMutBadTranscript    // desc.TranscriptRef != rec.TranscriptRef -> parent linkage
	dsMutRemoveMeta       // delete child meta.json -> missing child session meta
	dsMutRemoveTranscript // delete child transcript -> missing child transcript
	dsMutStatusRunning    // rec status Running (not runtime_lost) -> restore refused
	dsNumMut
)

// dsOwnAllowance is the root session's own delegation allowance (== MaxSubagentDepth).
// Grants of 0..dsOwnAllowance-1 are valid; grants >= it are rejected.
const dsOwnAllowance = 3

type ds_op struct {
	Code   int    `json:"c"`
	Allow  int    `json:"a,omitempty"` // dsOpCreate: fuzzed delegation_allowance
	Target int    `json:"t,omitempty"` // dsOpResume/dsOpFinalizeAgain: tracked-delegate index (mod len)
	Dur    int64  `json:"d,omitempty"` // dsOpAdvanceClock: advance (ns)
	Mut    int    `json:"m,omitempty"` // dsOpRestoreSeed: mutation kind
	Text   string `json:"x,omitempty"` // dsOpResume message text
}

type ds_artifact struct {
	Ops  []ds_op `json:"ops"`
	Seed uint64  `json:"seed"`
}

var ds_texts = []string{"go", "continue", "step 2", "??", "résumé"}

func ds_drawArtifact(rt *rapid.T) ds_artifact {
	n := rapid.IntRange(1, 20).Draw(rt, "nops")
	ops := make([]ds_op, 0, n)
	for i := 0; i < n; i++ {
		code := rapid.SampledFrom(ds_allOps).Draw(rt, "op")
		op := ds_op{Code: int(code)}
		switch code {
		case dsOpCreate:
			// 0..dsOwnAllowance-1 valid; dsOwnAllowance and above rejected. The tool
			// layer forbids negatives, so createDelegate never sees one.
			op.Allow = rapid.IntRange(0, dsOwnAllowance+1).Draw(rt, "allow")
		case dsOpResume:
			op.Target = rapid.IntRange(0, 255).Draw(rt, "target")
			op.Text = rapid.SampledFrom(ds_texts).Draw(rt, "text")
		case dsOpFinalizeAgain:
			op.Target = rapid.IntRange(0, 255).Draw(rt, "target")
		case dsOpRestoreSeed:
			op.Mut = rapid.IntRange(0, dsNumMut-1).Draw(rt, "mut")
		case dsOpAdvanceClock:
			op.Dur = int64(rapid.IntRange(0, int(5*time.Minute)).Draw(rt, "durNS"))
		}
		ops = append(ops, op)
	}
	return ds_artifact{Ops: ops, Seed: uint64(rapid.Int64().Draw(rt, "seed"))}
}

// --- tracked-delegate model ---

type ds_delegate struct {
	delegateID  string
	childID     string
	latestJobID string
	granted     int
}

// ds_lastAction carries the outcome of the just-applied op so ds_checkOracles can
// verify op-local invariants without the recovering op-goroutine needing to return
// anything but panic/wedge status.
type ds_lastAction struct {
	code ds_opCode

	// create
	createRan      bool
	createRejected bool // res.Err != nil
	createAllow    int
	createChildID  string

	// restore
	restoreRan       bool
	restoreMut       int
	restorePure      string // validateDelegateRestoreState reason ("" == pass)
	restoreResumable bool
	restoreAttempted bool
	restoreErr       bool
	restoredRunning  bool
	restoreSeedErr   string
}

type ds_model struct {
	closed       bool
	delegates    []ds_delegate
	terminalJobs map[string]string
	last         ds_lastAction
}

const (
	dsSurface     = "agent-delegate-seq"
	dsCallTimeout = 10 * time.Second
)

func ds_oracleRun(art ds_artifact) *promoter.Failure {
	stateDir, err := os.MkdirTemp("", "ds-state-")
	if err != nil {
		return ds_failure(promoter.Invariant, art, -1, "mkdir-state:"+err.Error())
	}
	defer os.RemoveAll(stateDir)
	workDir, err := os.MkdirTemp("", "ds-work-")
	if err != nil {
		return ds_failure(promoter.Invariant, art, -1, "mkdir-work:"+err.Error())
	}
	defer os.RemoveAll(workDir)

	clk := agenttest.NewFakeClock()
	client := llm.NewClient()
	client.Register(ds_terminalAdapter{name: "openai"})

	cfg := SessionConfig{
		clock:            clk,
		StateDir:         stateDir,
		MaxSubagentDepth: dsOwnAllowance,
		NoProjectPrompts: true,
		// Non-blocking retry sleep: the frozen fake clock must never strand a
		// backoff wait. The retry LOGIC still runs; only the delay is elided.
		LLMSleep: func(context.Context, time.Duration) error { return nil },
	}
	root, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), cfg)
	if err != nil {
		return ds_failure(promoter.Invariant, art, -1, "NewSession:"+err.Error())
	}

	// Drain events for the whole run so a turn emitting many events never blocks.
	drainDone := make(chan struct{})
	go func() {
		for range root.Events() {
		}
		close(drainDone)
	}()

	// Seed child sessions (produced only to write on-disk meta+transcript for a
	// restore) are closed at the end alongside the root.
	var seedSessions []*Session
	defer func() {
		root.Close()
		for _, s := range seedSessions {
			s.Close()
		}
		<-drainDone
	}()

	model := &ds_model{terminalJobs: map[string]string{}}

	for i, op := range art.Ops {
		res := ds_runOpSafely(root, clk, op, model, &seedSessions)
		if res.wedged {
			return ds_failure(promoter.Wedge, art, i, "wedge:"+ds_opName(ds_opCode(op.Code)))
		}
		if res.panicked {
			return ds_panicFailure(art, i, op, res)
		}
		if f := ds_checkOracles(root, model, op, art, i); f != nil {
			return f
		}
	}
	return nil
}

// --- action application (under a recovering goroutine + wall-time wedge) ---

type ds_opResult struct {
	panicked bool
	panicVal any
	stack    []string
	wedged   bool
}

func ds_runOpSafely(root *Session, clk *agenttest.FakeClock, op ds_op, model *ds_model, seed *[]*Session) ds_opResult {
	done := make(chan ds_opResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- ds_opResult{panicked: true, panicVal: r, stack: ds_captureStack()}
			}
		}()
		ds_applyOp(root, clk, op, model, seed)
		done <- ds_opResult{}
	}()
	select {
	case res := <-done:
		return res
	case <-time.After(dsCallTimeout):
		return ds_opResult{wedged: true}
	}
}

func ds_applyOp(root *Session, clk *agenttest.FakeClock, op ds_op, model *ds_model, seed *[]*Session) {
	model.last = ds_lastAction{code: ds_opCode(op.Code)}
	switch ds_opCode(op.Code) {
	case dsOpCreate:
		if model.closed {
			return
		}
		ds_applyCreate(root, op, model)
	case dsOpResume:
		if model.closed || len(model.delegates) == 0 {
			return
		}
		ds_applyResume(root, op, model)
	case dsOpFinalizeAgain:
		if model.closed || len(model.delegates) == 0 {
			return
		}
		ds_applyFinalizeAgain(root, op, model)
	case dsOpRestoreSeed:
		if model.closed {
			return
		}
		ds_applyRestoreSeed(root, op, model, seed)
	case dsOpAdvanceClock:
		clk.Advance(time.Duration(op.Dur))
	case dsOpObserve:
		_ = root.State()
		_ = root.QueueDepth()
		if root.jobManager != nil {
			_ = root.jobManager.list(listFilter{})
		}
	case dsOpClose:
		root.Close()
	}
}

func ds_applyCreate(root *Session, op ds_op, model *ds_model) {
	ctx, cancel := context.WithTimeout(context.Background(), dsCallTimeout)
	defer cancel()
	res := root.createDelegate(ctx, delegateArgs{
		Task:                "delegate task",
		Background:          false,
		BlockTimeoutMS:      2000,
		DelegationAllowance: op.Allow,
	})
	model.last.createRan = true
	model.last.createAllow = op.Allow
	model.last.createRejected = res.Err != nil
	if res.Err != nil || op.Allow >= dsOwnAllowance {
		return
	}
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		return
	}
	model.last.createChildID = childID
	model.delegates = append(model.delegates, ds_delegate{
		delegateID:  res.DelegateID,
		childID:     childID,
		latestJobID: res.JobID,
		granted:     op.Allow,
	})
}

func ds_applyResume(root *Session, op ds_op, model *ds_model) {
	d := &model.delegates[op.Target%len(model.delegates)]
	ctx, cancel := context.WithTimeout(context.Background(), dsCallTimeout)
	defer cancel()
	res := root.sendDelegateMessage(ctx, sendMessageArgs{
		Target:         d.delegateID,
		Message:        op.Text,
		OnIdle:         "start",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 2000,
	})
	if res.Err == nil && res.JobID != "" {
		d.latestJobID = res.JobID
	}
}

func ds_applyFinalizeAgain(root *Session, op ds_op, model *ds_model) {
	d := model.delegates[op.Target%len(model.delegates)]
	sub := root.subagents.get(d.childID)
	// finalizeDelegate on an already-terminal job is a manager-layer no-op
	// (finalizeWithRun returns nil when the job is no longer in jm.running). The
	// double-finalize invariant is that this neither panics nor mutates the
	// terminal record — verified by O4 terminal-stickiness after the op.
	_ = root.finalizeDelegate(d.latestJobID, d.childID, sub)
}

func ds_applyRestoreSeed(root *Session, op ds_op, model *ds_model, seed *[]*Session) {
	model.last.restoreRan = true
	model.last.restoreMut = op.Mut

	rec, child, err := ds_seedStoppedDelegateRestore(root)
	if child != nil {
		*seed = append(*seed, child)
	}
	if err != nil {
		model.last.restoreSeedErr = err.Error()
		return
	}

	// Corrupt the record / on-disk state per the drawn mutation.
	switch op.Mut {
	case dsMutBadParent:
		rec.DelegateRestore.ParentSessionID = "ds-bogus-parent"
	case dsMutClearWorkDir:
		rec.DelegateRestore.WorkingDir = ""
	case dsMutBadTranscript:
		rec.DelegateRestore.TranscriptRef = "local:ds-other-child"
	case dsMutRemoveMeta:
		_ = os.Remove(ds_childMetaPath(root, rec.DelegateRestore.ChildSessionID))
	case dsMutRemoveTranscript:
		_ = os.Remove(ds_childTranscriptPath(root, rec.DelegateRestore.ChildSessionID))
	case dsMutStatusRunning:
		rec.Status = jobstore.StatusRunning
		rec.Reason = ""
	}

	_, childID, err := decodeRef(rec.TranscriptRef)
	if err != nil {
		model.last.restoreSeedErr = "decode-ref:" + err.Error()
		return
	}

	model.last.restorePure = validateDelegateRestoreState(rec, root.ID(), root.stateDir != "")
	assessment := root.assessDelegateResumability(rec, delegateResumabilityPreflight)
	model.last.restoreResumable = assessment.Resumable
	if !assessment.Resumable {
		return
	}
	sub, rerr := root.restoreTerminalDelegateChild(rec, childID, assessment.Preflight)
	model.last.restoreAttempted = true
	model.last.restoreErr = rerr != nil
	if rerr == nil && sub != nil && sub.sess != nil {
		sub.mu.Lock()
		model.last.restoredRunning = sub.running
		sub.mu.Unlock()
	}
}

// --- oracle checks ---

func ds_checkOracles(root *Session, m *ds_model, op ds_op, art ds_artifact, step int) *promoter.Failure {
	st := root.State()

	// O2: boundary status is idle or closed; once closed, stays closed.
	if m.closed && st != SessionClosed {
		return ds_failure(promoter.Invariant, art, step, "status-regression:closed->"+string(st))
	}
	if st == SessionClosed {
		m.closed = true
	} else if st != SessionIdle {
		return ds_failure(promoter.Invariant, art, step, "status-nonboundary:"+string(st))
	}

	if f := ds_checkCreate(root, m, art, step); f != nil {
		return f
	}
	if f := ds_checkRestore(m, art, step); f != nil {
		return f
	}
	if f := ds_checkStore(root, m, art, step); f != nil {
		return f
	}
	return nil
}

func ds_checkCreate(root *Session, m *ds_model, art ds_artifact, step int) *promoter.Failure {
	if !m.last.createRan {
		return nil
	}
	// O3: a grant >= the parent's own allowance is rejected and never spawns.
	if m.last.createAllow >= dsOwnAllowance {
		if !m.last.createRejected {
			return ds_failure(promoter.Invariant, art, step,
				fmt.Sprintf("over-allowance-grant-accepted:%d>=%d", m.last.createAllow, dsOwnAllowance))
		}
		return nil
	}
	if m.last.createRejected || m.last.createChildID == "" {
		return ds_failure(promoter.Invariant, art, step,
			fmt.Sprintf("valid-grant-rejected:allow=%d", m.last.createAllow))
	}
	// O3: the child's allowance == the granted value, is >= 0 and < the parent's
	// own; its depth == parent depth + 1.
	sub := root.subagents.get(m.last.createChildID)
	if sub == nil || sub.sess == nil {
		return ds_failure(promoter.Invariant, art, step, "created-child-not-retained")
	}
	child := sub.sess
	child.mu.Lock()
	childAllow := child.delegationAllowance
	childDepth := child.depth
	child.mu.Unlock()
	if childAllow != m.last.createAllow {
		return ds_failure(promoter.Invariant, art, step,
			fmt.Sprintf("child-allowance-mismatch:got=%d want=%d", childAllow, m.last.createAllow))
	}
	if childAllow < 0 {
		return ds_failure(promoter.Invariant, art, step, fmt.Sprintf("child-allowance-negative:%d", childAllow))
	}
	if childAllow >= dsOwnAllowance {
		return ds_failure(promoter.Invariant, art, step,
			fmt.Sprintf("child-allowance-not-less-than-parent:%d>=%d", childAllow, dsOwnAllowance))
	}
	if childDepth != root.depth+1 {
		return ds_failure(promoter.Invariant, art, step,
			fmt.Sprintf("child-depth-mismatch:got=%d want=%d", childDepth, root.depth+1))
	}
	return nil
}

func ds_checkRestore(m *ds_model, art ds_artifact, step int) *promoter.Failure {
	if !m.last.restoreRan {
		return nil
	}
	if m.last.restoreSeedErr != "" {
		return ds_failure(promoter.Invariant, art, step, "restore-seed-failed:"+m.last.restoreSeedErr)
	}
	// O7: validator agreement. The pure gate is a necessary precondition assess
	// runs first, so a pure-rejected record must never be deemed resumable.
	if m.last.restorePure != "" && m.last.restoreResumable {
		return ds_failure(promoter.Invariant, art, step,
			"resumable-despite-pure-reject:"+m.last.restorePure)
	}
	switch m.last.restoreMut {
	case dsMutNone:
		// A well-formed runtime_lost delegate must restore to an idle child.
		if !m.last.restoreResumable {
			return ds_failure(promoter.Invariant, art, step, "wellformed-not-resumable")
		}
		if m.last.restorePure != "" {
			return ds_failure(promoter.Invariant, art, step, "wellformed-pure-reject:"+m.last.restorePure)
		}
		if m.last.restoreAttempted && m.last.restoreErr {
			return ds_failure(promoter.Invariant, art, step, "wellformed-restore-refused")
		}
		if m.last.restoreAttempted && !m.last.restoreErr && m.last.restoredRunning {
			return ds_failure(promoter.Invariant, art, step, "restored-delegate-marked-running")
		}
	case dsMutBadParent, dsMutClearWorkDir, dsMutBadTranscript, dsMutRemoveMeta, dsMutRemoveTranscript:
		// A corrupted descriptor/on-disk state must be rejected by assessment.
		if m.last.restoreResumable {
			return ds_failure(promoter.Invariant, art, step,
				fmt.Sprintf("corrupt-restore-accepted:mut=%d", m.last.restoreMut))
		}
	case dsMutStatusRunning:
		// A running (non-runtime-lost, non-resumable) delegate must never be
		// restored as a live runtime — restore refuses even if assess (which does
		// not inspect status) allowed the preflight.
		if m.last.restoreAttempted && !m.last.restoreErr {
			return ds_failure(promoter.Invariant, art, step, "running-delegate-restored-as-runtime")
		}
	}
	return nil
}

func ds_checkStore(root *Session, m *ds_model, art ds_artifact, step int) *promoter.Failure {
	jm := root.jobManager
	if jm == nil {
		return nil
	}
	recs, err := jm.store.Load()
	if err != nil {
		// A closed store after Close is expected; only a live-session load error is a defect.
		if m.closed {
			return nil
		}
		return ds_failure(promoter.Invariant, art, step, "store-load:"+err.Error())
	}

	// O4: terminal-stickiness across the full interleaving. Once a job is observed
	// terminal it must never reappear non-terminal nor flip to a different terminal
	// status — no matter what create/resume/finalize/restore churn ran between
	// observations. Only currently-present records are checked (eviction is fine).
	running := map[string]bool{}
	for _, id := range jm.runningJobIDs() {
		running[id] = true
	}
	for _, rec := range recs {
		if rec == nil {
			continue
		}
		if prev, seen := m.terminalJobs[rec.JobID]; seen {
			if !rec.Status.IsTerminal() {
				return ds_failure(promoter.Invariant, art, step,
					fmt.Sprintf("job-terminal-unstuck:%s:%s->%s", rec.JobID, prev, rec.Status))
			}
			if string(rec.Status) != prev {
				return ds_failure(promoter.Invariant, art, step,
					fmt.Sprintf("job-terminal-status-changed:%s:%s->%s", rec.JobID, prev, rec.Status))
			}
		} else if rec.Status.IsTerminal() {
			m.terminalJobs[rec.JobID] = string(rec.Status)
		}
		// O5: no delegate is left Running with no runtime.
		if rec.Type == jobstore.JobDelegate && rec.Status == jobstore.StatusRunning && !running[rec.JobID] {
			return ds_failure(promoter.Invariant, art, step, "delegate-running-without-runtime:"+rec.JobID)
		}
	}

	// O6: every tracked delegate is present in the folded store and delegate table.
	// (A closed session's store may already be torn down; skip once closed.)
	if m.closed {
		return nil
	}
	delegates, derr := jm.store.LoadDelegates()
	if derr != nil {
		return ds_failure(promoter.Invariant, art, step, "load-delegates:"+derr.Error())
	}
	for _, d := range m.delegates {
		if _, ok := delegates[d.delegateID]; !ok {
			return ds_failure(promoter.Invariant, art, step, "tracked-delegate-missing:"+d.delegateID)
		}
		rec := recs[d.latestJobID]
		if rec == nil {
			return ds_failure(promoter.Invariant, art, step, "tracked-job-missing:"+d.latestJobID)
		}
		if !rec.Status.IsTerminal() && !running[rec.JobID] {
			return ds_failure(promoter.Invariant, art, step,
				fmt.Sprintf("tracked-job-nonterminal-idle:%s:%s", d.latestJobID, rec.Status))
		}
	}
	return nil
}

// --- restore-record seeding (t-free; mirrors seedStoppedDelegateRestoreRecord) ---

// ds_seedStoppedDelegateRestore creates a real child session (persisting its
// on-disk meta + transcript into the root's StateDir), then appends the delegate
// created/started/finished(stopped, runtime_lost) events so the folded record is a
// resumable runtime_lost delegate. It returns the folded record and the seed child
// session (which the caller closes) or an error.
func ds_seedStoppedDelegateRestore(root *Session) (*jobstore.JobRecord, *Session, error) {
	childWorkDir, err := os.MkdirTemp("", "ds-child-work-")
	if err != nil {
		return nil, nil, err
	}
	cfg := SessionConfig{
		clock:            root.cfg.clock,
		StateDir:         root.stateDir,
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
	}
	cfg.spawn.depth = root.depth + 1
	cfg.spawn.parentSessionID = root.ID()
	child, err := NewSession(root.client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(childWorkDir), cfg)
	if err != nil {
		return nil, nil, err
	}
	childID := child.ID()
	if child.transcript != nil {
		if cerr := child.transcript.Close(); cerr != nil {
			return nil, child, cerr
		}
		child.transcript = nil
	}

	delegateID := jobstore.NewDelegateID()
	generation := jobstore.NewDelegateGeneration()
	jobID := jobstore.NewJobID()
	now := root.sclock().Now().UTC()
	ref := encodeRef("", childID)
	desc := &jobstore.DelegateRestoreDescriptor{
		Version:           1,
		ChildSessionID:    childID,
		TranscriptRef:     ref,
		ParentSessionID:   root.ID(),
		ParentJobID:       jobID,
		OwnerSessionID:    root.ID(),
		VisibleSessionID:  root.ID(),
		Task:              "retained delegate",
		ResolvedProfileID: "openai",
		ResolvedModel:     "gpt-5.2",
		WorkingDir:        childWorkDir,
		LocalEnvPolicy:    "default",
	}
	events := []jobstore.Event{
		{
			Kind:       jobstore.EventDelegateCreated,
			TS:         now,
			DelegateID: delegateID,
			Delegate: &jobstore.DelegateEvent{
				ChildSessionID:   childID,
				TranscriptRef:    ref,
				OwnerSessionID:   root.ID(),
				VisibleSessionID: root.ID(),
				Generation:       generation,
				Resumable:        true,
			},
		},
		{
			Kind:             jobstore.EventJobStarted,
			TS:               now,
			JobID:            jobID,
			DelegateID:       delegateID,
			Type:             jobstore.JobDelegate,
			Task:             desc.Task,
			OwnerSessionID:   root.ID(),
			VisibleToSession: root.ID(),
			StartedAt:        &now,
			TranscriptRef:    ref,
			DelegateRestore:  desc,
		},
		{
			Kind:        jobstore.EventJobFinished,
			TS:          now,
			JobID:       jobID,
			Status:      jobstore.StatusStopped,
			Reason:      "runtime_lost",
			EndedAt:     &now,
			TerminalGen: jobstore.NewWatchGeneration(),
		},
	}
	for _, ev := range events {
		if aerr := root.jobManager.appendEvent(ev); aerr != nil {
			return nil, child, aerr
		}
	}
	rec, err := findJobRecord(root.jobManager, jobID)
	if err != nil {
		return nil, child, err
	}
	return rec, child, nil
}

func ds_childMetaPath(root *Session, childID string) string {
	return filepath.Join(root.stateDir, sessionsSubdir, childID+".meta.json")
}

func ds_childTranscriptPath(root *Session, childID string) string {
	return filepath.Join(root.stateDir, sessionsSubdir, childID+".transcript.jsonl")
}

// --- scripted adapter: every response terminates the turn in one round ---

type ds_terminalAdapter struct{ name string }

func (a ds_terminalAdapter) Name() string { return a.name }

func (a ds_terminalAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	resp := communicateWithDefaultOutput("delegate done")
	resp.Provider = a.name
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

func (a ds_terminalAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

// --- promoter wiring (mirrors the lifecycle seqAdapter) ---

type ds_promoAdapter struct {
	emitDir string
}

func (a *ds_promoAdapter) Minimize(f promoter.Failure) promoter.Failure { return f }

func (a *ds_promoAdapter) Signature(f promoter.Failure) promoter.Signature {
	key := f.Detail
	if f.Oracle == promoter.Panic && len(f.Stack) > 0 {
		key = strings.Join(ds_topFrames(f.Stack, 4), "|")
	}
	if key == "" {
		key = promoter.ShortHash(f)
	}
	return promoter.Signature{Oracle: f.Oracle, Key: key}
}

func (a *ds_promoAdapter) Replay(_ context.Context, f promoter.Failure) (bool, bool) {
	var art ds_artifact
	if err := json.Unmarshal(f.Artifact, &art); err != nil {
		return false, false
	}
	repro := ds_oracleRun(art)
	if repro == nil {
		return false, false
	}
	return true, a.Signature(*repro) == a.Signature(f)
}

func (a *ds_promoAdapter) Emit(f promoter.Failure) (string, error) {
	return promoter.WriteGoTest(a.emitDir, promoter.GoTest{
		Package:    "agent",
		Surface:    f.Surface,
		Oracle:     f.Oracle,
		Signature:  a.Signature(f).String(),
		Seam:       "agent.Session delegate create/resume/finalize/restore lifecycle",
		Hash:       promoter.ShortHash(f),
		ReplayBody: "\tds_replayArtifact(t, " + strconv.Quote(string(f.Artifact)) + ")",
	})
}

// TestDelegateSeqFuzzReplayClean exercises the emitted-regression replay body: a
// clean recorded op sequence (create → resume → double-finalize → restore → close)
// replays without a failure. It also keeps ds_replayArtifact — the body
// WriteGoTest emits into a promoted regression test — compiled and covered.
func TestDelegateSeqFuzzReplayClean(t *testing.T) {
	clean, err := json.Marshal(ds_artifact{Ops: []ds_op{
		{Code: int(dsOpCreate), Allow: 1},
		{Code: int(dsOpResume), Target: 0, Text: "go"},
		{Code: int(dsOpFinalizeAgain), Target: 0},
		{Code: int(dsOpRestoreSeed), Mut: dsMutNone},
		{Code: int(dsOpClose)},
	}})
	if err != nil {
		t.Fatalf("marshal clean artifact: %v", err)
	}
	ds_replayArtifact(t, string(clean))
}

// ds_replayArtifact is the body an emitted regression test runs: it replays the
// recorded op sequence against the live config and asserts the delegate lifecycle
// no longer fails.
func ds_replayArtifact(t *testing.T, artifact string) {
	t.Helper()
	var art ds_artifact
	if err := json.Unmarshal([]byte(artifact), &art); err != nil {
		t.Fatalf("decode artifact: %v", err)
	}
	if f := ds_oracleRun(art); f != nil {
		t.Fatalf("delegate oracle still fails on recorded artifact: %s", f.Detail)
	}
}

// --- failure constructors / helpers ---

func ds_failure(oracle promoter.OracleTag, art ds_artifact, step int, detail string) *promoter.Failure {
	return &promoter.Failure{
		Surface:  dsSurface,
		Oracle:   oracle,
		Detail:   detail + fmt.Sprintf(":step=%d", step),
		Artifact: ds_json(art),
	}
}

func ds_panicFailure(art ds_artifact, step int, op ds_op, res ds_opResult) *promoter.Failure {
	return &promoter.Failure{
		Surface:  dsSurface,
		Oracle:   promoter.Panic,
		Stack:    res.stack,
		Detail:   "panic:" + ds_opName(ds_opCode(op.Code)) + ":" + ds_firstLine(fmt.Sprint(res.panicVal)),
		Artifact: ds_json(art),
	}
}

func ds_json(art ds_artifact) json.RawMessage {
	b, _ := json.Marshal(art)
	return b
}

func ds_opName(c ds_opCode) string {
	switch c {
	case dsOpCreate:
		return "create"
	case dsOpResume:
		return "resume"
	case dsOpFinalizeAgain:
		return "finalize_again"
	case dsOpRestoreSeed:
		return "restore_seed"
	case dsOpAdvanceClock:
		return "advance_clock"
	case dsOpObserve:
		return "observe"
	case dsOpClose:
		return "close"
	default:
		return "op(" + strconv.Itoa(int(c)) + ")"
	}
}

func ds_firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func ds_topFrames(frames []string, n int) []string {
	if len(frames) < n {
		return frames
	}
	return frames[:n]
}

func ds_captureStack() []string {
	var pcs [16]uintptr
	n := runtime.Callers(3, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	var out []string
	for {
		fr, more := frames.Next()
		fn := fr.Function
		if i := strings.LastIndex(fn, "serf/"); i >= 0 {
			fn = fn[i+len("serf/"):]
		}
		out = append(out, fmt.Sprintf("%s:%d", fn, fr.Line))
		if !more || len(out) >= 8 {
			break
		}
	}
	return out
}

type ds_quietQuarantiner struct{}

func (ds_quietQuarantiner) Quarantine(promoter.Failure, int) error { return nil }

var _ promoter.Adapter = (*ds_promoAdapter)(nil)
