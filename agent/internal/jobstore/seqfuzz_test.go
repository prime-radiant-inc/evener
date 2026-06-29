package jobstore

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"

	"pgregory.net/rapid"
)

// TestJobstoreSeqFuzz is roadmap item 8.3's jobstore-as-a-sequence stateful
// model. Every non-crash bug this project has hit has been a STATE bug, yet the
// existing FuzzJobEventLogReplay folds a SINGLE arbitrary blob and asserts
// persist↔reload idempotence — it never drives the reducer over a LEGAL event
// SEQUENCE and never checks invariants that hold ACROSS the sequence.
//
// This rapid state machine does. It draws sequences of legal job-lifecycle
// operations — the exact event shapes the real writer emits — building up a
// jobs.jsonl event log one op at a time while maintaining a thin parallel model
// of the essential state (per-job status + notify, per-delegate current/latest
// job, watch active flag, observer grant set, watch-send coalescing slots).
// After each op it re-folds the WHOLE log through all six reducers and checks,
// weakest-first:
//
//	I1 (determinism/idempotence): re-applying the just-appended event a second
//	   time (a duplicate with a fresh seq) leaves EVERY reducer's output byte-for-
//	   byte identical. The reducer's monotonic guards make a replayed event a
//	   no-op; this is the daemon-recovery / double-delivery safety property.
//	I2 (status sticky): a job's status only advances null→running→terminal, and
//	   the FIRST terminal write is final — a later job_finished with a different
//	   status/generation never reopens or overwrites it (checked both against the
//	   model AND directly across consecutive folds, so a buggy model cannot hide
//	   a regression).
//	I3 (notify monotonic): the terminal-notification state climbs not_armed→
//	   pending→delivered and never falls back; a notification whose generation
//	   does not match the job's terminal generation is inert.
//	I4 (delegate↔job coherence): a delegate's CurrentJobID is set iff that job is
//	   running (non-terminal) in Fold, and both CurrentJobID and LatestJobID name
//	   real jobs whose DelegateID points back at the delegate — the cross-reducer
//	   (FoldDelegates vs Fold) consistency the task calls out.
//	I5 (watch active monotonic): a watch is active until cleared with its matching
//	   generation; a wrong-generation clear is inert and a cleared watch never
//	   reactivates.
//	I6 (grants reference real jobs + match the model): every (observer, job) in
//	   FoldGrants names a job that exists in Fold.
//	I7 (watch-send coalescing): the pending set folds exactly to the highest
//	   un-settled update_seq per key; a settle of seq≥pending clears it and a
//	   stale (seq≤settled) pending is ignored.
//
// The op table (legalOps/applyOp) only ever proposes transitions that are legal
// from the current model state, so the generated log is always a shape the real
// system could emit — not arbitrary bytes (that is FuzzJobEventLogReplay's job).
//
// Run hard with: go test -run '^TestJobstoreSeqFuzz$' -rapid.checks=5000 .
// Under -tags serffuzz the reducer's own invariant.Hold assertions are live too,
// so a generated sequence that tripped the in-reducer monotonicity guard would
// surface as a panic.
func TestJobstoreSeqFuzz(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		m := newSeqModel()
		steps := rapid.IntRange(1, 40).Draw(rt, "steps")
		for i := 0; i < steps; i++ {
			op := rapid.SampledFrom(m.legalOps()).Draw(rt, "op")
			m.applyOp(rt, op)
			m.checkInvariants(rt, i)
		}
		m.finalChecks(rt)
	})
}

// --- op vocabulary ---

type seqOp int

const (
	opStartShell seqOp = iota
	opStartDelegate
	opAssignSession
	opFinishJob
	opRefinishJob
	opArmNotify
	opArmNotifyStale
	opDeliverNotify
	opMessageSent
	opSecondDelegateJob
	opRegisterWatch
	opClearWatch
	opClearWatchStale
	opGrant
	opWatchSendPending
	opWatchSendSettle
	opWatchSendStalePending
)

// --- the thin model ---

type jobModel struct {
	id          string
	status      Status // "" never seen, then running, then a terminal status
	terminalGen string
	notify      NotifyState
	delegateID  string // non-empty for delegate-owned jobs
}

type delegateModel struct {
	id           string
	currentJobID string
	latestJobID  string
}

type watchModel struct {
	id     string
	gen    string
	active bool
}

type wsKeyModel struct {
	key        WatchSendKey
	pending    bool
	pendingSeq uint64
	settledSeq uint64
	hasSettled bool
	lastSeq    uint64 // high-water update_seq ever emitted for this key
}

type seqModel struct {
	log   []Event
	seq   int64
	idCtr int

	jobs      []*jobModel
	jobByID   map[string]*jobModel
	delegates []*delegateModel
	delByID   map[string]*delegateModel
	watches   []*watchModel
	grants    map[string]map[string]bool
	wsKeys    []*wsKeyModel

	// Cross-fold monotonicity trackers, independent of the model so a buggy
	// model cannot mask a real regression.
	prevStatus map[string]Status
	prevNotify map[string]int
}

var (
	seqSessions  = []string{"s1", "s2"}
	seqObservers = []string{"obs1", "obs2"}
	terminalSet  = []Status{StatusCompleted, StatusFailed, StatusCancelled, StatusStopped}
	wsSendTerm   = []EventKind{EventWatchSendDelivered, EventWatchSendDropped, EventWatchSendEvicted}
)

func newSeqModel() *seqModel {
	return &seqModel{
		jobByID:    make(map[string]*jobModel),
		delByID:    make(map[string]*delegateModel),
		grants:     make(map[string]map[string]bool),
		prevStatus: make(map[string]Status),
		prevNotify: make(map[string]int),
		wsKeys: []*wsKeyModel{
			{key: WatchSendKey{VisibleSessionID: "s1", WatchID: "watch_1", WatchTarget: "job_t", ResolvedWatchedIdentity: "job_t", ResolvedSendTo: "s1", WatchGeneration: "wg_1"}},
			{key: WatchSendKey{VisibleSessionID: "s2", WatchID: "watch_2", WatchTarget: "job_t2", ResolvedWatchedIdentity: "job_t2", ResolvedSendTo: "s2", WatchGeneration: "wg_2"}},
			{key: WatchSendKey{VisibleSessionID: "s1", WatchID: "watch_3", WatchTarget: "job_t", ResolvedWatchedIdentity: "job_t", ResolvedSendTo: "s2", WatchGeneration: "wg_3"}},
		},
	}
}

func (m *seqModel) nextID(prefix string) string {
	m.idCtr++
	return prefix + strconv.Itoa(m.idCtr)
}

func (m *seqModel) emit(e Event) {
	m.seq++
	e.Seq = m.seq
	m.log = append(m.log, e)
}

// --- legality: only transitions legal from the current model state ---

func (m *seqModel) legalOps() []seqOp {
	// Always available: starting work, registering a watch, emitting a fresh
	// watch-send pending (the three slots always exist).
	ops := []seqOp{opStartShell, opStartDelegate, opRegisterWatch, opWatchSendPending}

	if len(m.jobs) > 0 {
		ops = append(ops, opAssignSession, opMessageSent, opGrant, opArmNotifyStale)
	}
	if len(m.nonterminalJobs()) > 0 {
		ops = append(ops, opFinishJob)
	}
	if len(m.terminalJobs()) > 0 {
		ops = append(ops, opRefinishJob)
	}
	if len(m.armableJobs()) > 0 {
		ops = append(ops, opArmNotify)
	}
	if len(m.deliverableJobs()) > 0 {
		ops = append(ops, opDeliverNotify)
	}
	if len(m.idleDelegates()) > 0 {
		ops = append(ops, opSecondDelegateJob)
	}
	if len(m.activeWatches()) > 0 {
		ops = append(ops, opClearWatch, opClearWatchStale)
	}
	if len(m.pendingKeys()) > 0 {
		ops = append(ops, opWatchSendSettle)
	}
	if len(m.settledIdleKeys()) > 0 {
		ops = append(ops, opWatchSendStalePending)
	}
	return ops
}

func (m *seqModel) nonterminalJobs() []*jobModel {
	var out []*jobModel
	for _, j := range m.jobs {
		if !j.status.IsTerminal() {
			out = append(out, j)
		}
	}
	return out
}

func (m *seqModel) terminalJobs() []*jobModel {
	var out []*jobModel
	for _, j := range m.jobs {
		if j.status.IsTerminal() {
			out = append(out, j)
		}
	}
	return out
}

func (m *seqModel) armableJobs() []*jobModel {
	var out []*jobModel
	for _, j := range m.jobs {
		if j.status.IsTerminal() && j.notify == NotifyNotArmed {
			out = append(out, j)
		}
	}
	return out
}

func (m *seqModel) deliverableJobs() []*jobModel {
	var out []*jobModel
	for _, j := range m.jobs {
		if j.status.IsTerminal() && j.notify == NotifyPending {
			out = append(out, j)
		}
	}
	return out
}

func (m *seqModel) idleDelegates() []*delegateModel {
	var out []*delegateModel
	for _, d := range m.delegates {
		if d.currentJobID == "" {
			out = append(out, d)
		}
	}
	return out
}

func (m *seqModel) activeWatches() []*watchModel {
	var out []*watchModel
	for _, w := range m.watches {
		if w.active {
			out = append(out, w)
		}
	}
	return out
}

func (m *seqModel) pendingKeys() []*wsKeyModel {
	var out []*wsKeyModel
	for _, k := range m.wsKeys {
		if k.pending {
			out = append(out, k)
		}
	}
	return out
}

func (m *seqModel) settledIdleKeys() []*wsKeyModel {
	var out []*wsKeyModel
	for _, k := range m.wsKeys {
		if k.hasSettled && !k.pending {
			out = append(out, k)
		}
	}
	return out
}

func pickOf[T any](rt *rapid.T, label string, xs []T) T {
	return xs[rapid.IntRange(0, len(xs)-1).Draw(rt, label)]
}

// --- op application: emit legal events, advance the model ---

func (m *seqModel) applyOp(rt *rapid.T, op seqOp) {
	switch op {
	case opStartShell:
		id := m.nextID("job_")
		owner := pickOf(rt, "owner", seqSessions)
		m.emit(Event{Kind: EventJobStarted, JobID: id, Type: JobShell, Command: "cmd", OwnerSessionID: owner, VisibleToSession: owner})
		jm := &jobModel{id: id, status: StatusRunning, notify: NotifyNotArmed}
		m.jobs = append(m.jobs, jm)
		m.jobByID[id] = jm

	case opStartDelegate:
		did := m.nextID("dlg_")
		gen := m.nextID("dg_")
		jid := m.nextID("job_")
		owner := pickOf(rt, "owner", seqSessions)
		m.emit(Event{Kind: EventDelegateCreated, DelegateID: did, Delegate: &DelegateEvent{
			ChildSessionID: "c_" + did, AgentType: "engineer", Generation: gen, Resumable: true,
		}})
		m.emit(Event{Kind: EventJobStarted, JobID: jid, Type: JobDelegate, DelegateID: did, OwnerSessionID: owner, VisibleToSession: owner})
		jm := &jobModel{id: jid, status: StatusRunning, notify: NotifyNotArmed, delegateID: did}
		m.jobs = append(m.jobs, jm)
		m.jobByID[jid] = jm
		dm := &delegateModel{id: did, currentJobID: jid, latestJobID: jid}
		m.delegates = append(m.delegates, dm)
		m.delByID[did] = dm

	case opSecondDelegateJob:
		dm := pickOf(rt, "idleDelegate", m.idleDelegates())
		jid := m.nextID("job_")
		owner := pickOf(rt, "owner", seqSessions)
		m.emit(Event{Kind: EventJobStarted, JobID: jid, Type: JobDelegate, DelegateID: dm.id, OwnerSessionID: owner, VisibleToSession: owner})
		jm := &jobModel{id: jid, status: StatusRunning, notify: NotifyNotArmed, delegateID: dm.id}
		m.jobs = append(m.jobs, jm)
		m.jobByID[jid] = jm
		dm.currentJobID = jid
		dm.latestJobID = jid

	case opAssignSession:
		jm := pickOf(rt, "assignJob", m.jobs)
		res := true
		m.emit(Event{Kind: EventJobSessionAssigned, JobID: jm.id, TranscriptRef: "t_" + jm.id, Resumable: &res})

	case opFinishJob:
		jm := pickOf(rt, "finishJob", m.nonterminalJobs())
		st := pickOf(rt, "finishStatus", terminalSet)
		gen := m.nextID("g_")
		m.emit(Event{Kind: EventJobFinished, JobID: jm.id, Status: st, TerminalGen: gen})
		jm.status = st
		jm.terminalGen = gen
		if jm.delegateID != "" {
			if dm := m.delByID[jm.delegateID]; dm != nil && dm.currentJobID == jm.id {
				dm.currentJobID = ""
			}
		}

	case opRefinishJob:
		// Second terminal write with a DIFFERENT status+generation: first wins,
		// so the model is unchanged and Fold must keep the original.
		jm := pickOf(rt, "refinishJob", m.terminalJobs())
		st := drawDifferentTerminal(rt, jm.status)
		m.emit(Event{Kind: EventJobFinished, JobID: jm.id, Status: st, TerminalGen: m.nextID("g_")})

	case opArmNotify:
		jm := pickOf(rt, "armJob", m.armableJobs())
		m.emit(Event{Kind: EventJobNotificationPending, JobID: jm.id, TerminalGen: jm.terminalGen})
		jm.notify = NotifyPending

	case opArmNotifyStale:
		// A notification whose generation does not match the job's terminal
		// generation (or a job not yet terminal) is inert: model unchanged.
		jm := pickOf(rt, "staleArmJob", m.jobs)
		m.emit(Event{Kind: EventJobNotificationPending, JobID: jm.id, TerminalGen: m.nextID("stale_")})

	case opDeliverNotify:
		jm := pickOf(rt, "deliverJob", m.deliverableJobs())
		m.emit(Event{Kind: EventJobNotificationDelivered, JobID: jm.id, TerminalGen: jm.terminalGen})
		jm.notify = NotifyDelivered

	case opMessageSent:
		jm := pickOf(rt, "msgJob", m.jobs)
		m.emit(Event{Kind: EventJobMessageSent, JobID: jm.id, Target: "s1", Action: "note"})

	case opRegisterWatch:
		wid := m.nextID("watch_")
		gen := m.nextID("wg_")
		sess := pickOf(rt, "watchSess", seqSessions)
		m.emit(Event{Kind: EventWatchRegistered, WatchID: wid, Watch: &WatchEvent{
			Generation: gen, OwnerSessionID: sess, VisibleSessionID: sess, Target: "job_target", ConfigHash: "h_" + wid,
		}})
		m.watches = append(m.watches, &watchModel{id: wid, gen: gen, active: true})

	case opClearWatch:
		wm := pickOf(rt, "clearWatch", m.activeWatches())
		m.emit(Event{Kind: EventWatchCleared, WatchID: wm.id, Watch: &WatchEvent{Generation: wm.gen, EndReason: "done"}})
		wm.active = false

	case opClearWatchStale:
		// Wrong-generation clear is inert: the watch stays active.
		wm := pickOf(rt, "staleClearWatch", m.activeWatches())
		m.emit(Event{Kind: EventWatchCleared, WatchID: wm.id, Watch: &WatchEvent{Generation: m.nextID("stale_"), EndReason: "x"}})

	case opGrant:
		jm := pickOf(rt, "grantJob", m.jobs)
		obs := pickOf(rt, "grantObs", seqObservers)
		m.emit(Event{Kind: EventWatchReadGrant, JobID: jm.id, ObserverSessionID: obs})
		if m.grants[obs] == nil {
			m.grants[obs] = make(map[string]bool)
		}
		m.grants[obs][jm.id] = true

	case opWatchSendPending:
		k := pickOf(rt, "wsPendKey", m.wsKeys)
		k.lastSeq++
		seq := k.lastSeq
		m.emit(Event{Kind: EventWatchSendPending, JobID: "job_t", WatchSend: &WatchSendState{
			Key: k.key, DeliveryID: m.nextID("wd_"), UpdateSeq: seq, Message: "m",
		}})
		k.pending = true
		k.pendingSeq = seq

	case opWatchSendSettle:
		k := pickOf(rt, "wsSettleKey", m.pendingKeys())
		kind := pickOf(rt, "wsSettleKind", wsSendTerm)
		m.emit(Event{Kind: kind, JobID: "job_t", WatchSend: &WatchSendState{
			Key: k.key, DeliveryID: m.nextID("wd_"), UpdateSeq: k.pendingSeq,
		}})
		k.pending = false
		k.settledSeq = k.pendingSeq
		k.hasSettled = true

	case opWatchSendStalePending:
		// A pending whose update_seq is at-or-below the last settled seq is
		// ignored: the key stays not-pending.
		k := pickOf(rt, "wsStaleKey", m.settledIdleKeys())
		m.emit(Event{Kind: EventWatchSendPending, JobID: "job_t", WatchSend: &WatchSendState{
			Key: k.key, DeliveryID: m.nextID("wd_"), UpdateSeq: k.settledSeq,
		}})
	}
}

func drawDifferentTerminal(rt *rapid.T, cur Status) Status {
	others := make([]Status, 0, len(terminalSet)-1)
	for _, s := range terminalSet {
		if s != cur {
			others = append(others, s)
		}
	}
	return pickOf(rt, "refinishStatus", others)
}

// --- invariant checks over the whole sequence so far ---

func (m *seqModel) checkInvariants(rt *rapid.T, step int) {
	fold := Fold(m.log)

	// I2 + I3: per-job status and notify match the model; status is terminal-
	// sticky and notify is monotonic across consecutive folds (model-independent).
	for _, jm := range m.jobs {
		rec := fold[jm.id]
		if rec == nil {
			rt.Fatalf("step %d: job %s missing from Fold", step, jm.id)
			continue // rapid.T.Fatalf halts the test; continue satisfies nil-flow analysis.
		}
		if rec.Status != jm.status {
			rt.Fatalf("step %d: job %s status=%s, model=%s", step, jm.id, rec.Status, jm.status)
		}
		if jm.status.IsTerminal() && rec.TerminalGen != jm.terminalGen {
			rt.Fatalf("step %d: job %s terminalGen=%q, model=%q (first terminal write must win)", step, jm.id, rec.TerminalGen, jm.terminalGen)
		}
		if rec.NotifyState != jm.notify {
			rt.Fatalf("step %d: job %s notify=%s, model=%s", step, jm.id, rec.NotifyState, jm.notify)
		}

		if prev, seen := m.prevStatus[jm.id]; seen && prev.IsTerminal() && rec.Status != prev {
			rt.Fatalf("step %d: job %s terminal status regressed %s -> %s", step, jm.id, prev, rec.Status)
		}
		if notifyRank(rec.NotifyState) < m.prevNotify[jm.id] {
			rt.Fatalf("step %d: job %s notify rank regressed %d -> %d", step, jm.id, m.prevNotify[jm.id], notifyRank(rec.NotifyState))
		}
		m.prevStatus[jm.id] = rec.Status
		m.prevNotify[jm.id] = notifyRank(rec.NotifyState)
	}

	// I4: delegate↔job coherence across FoldDelegates and Fold.
	delg := FoldDelegates(m.log)
	for _, dm := range m.delegates {
		d := delg[dm.id]
		if d == nil {
			rt.Fatalf("step %d: delegate %s missing from FoldDelegates", step, dm.id)
			continue // rapid.T.Fatalf halts the test; continue satisfies nil-flow analysis.
		}
		if d.CurrentJobID != dm.currentJobID {
			rt.Fatalf("step %d: delegate %s currentJob=%q, model=%q", step, dm.id, d.CurrentJobID, dm.currentJobID)
		}
		if d.LatestJobID != dm.latestJobID {
			rt.Fatalf("step %d: delegate %s latestJob=%q, model=%q", step, dm.id, d.LatestJobID, dm.latestJobID)
		}
	}
	for id, d := range delg {
		if d.CurrentJobID != "" {
			cur := fold[d.CurrentJobID]
			if cur == nil {
				rt.Fatalf("step %d: delegate %s currentJob %s absent from Fold", step, id, d.CurrentJobID)
				continue // rapid.T.Fatalf halts the test; continue satisfies nil-flow analysis.
			}
			if cur.Status.IsTerminal() {
				rt.Fatalf("step %d: delegate %s currentJob %s is terminal (%s) but still current", step, id, d.CurrentJobID, cur.Status)
			}
			if cur.DelegateID != id {
				rt.Fatalf("step %d: delegate %s currentJob %s points at delegate %q", step, id, d.CurrentJobID, cur.DelegateID)
			}
		}
		if d.LatestJobID != "" {
			l := fold[d.LatestJobID]
			if l == nil {
				rt.Fatalf("step %d: delegate %s latestJob %s absent from Fold", step, id, d.LatestJobID)
				continue // rapid.T.Fatalf halts the test; continue satisfies nil-flow analysis.
			}
			if l.DelegateID != id {
				rt.Fatalf("step %d: delegate %s latestJob %s points at delegate %q", step, id, d.LatestJobID, l.DelegateID)
			}
		}
	}

	// I5: watch active flag matches the model (monotonic by construction: a
	// cleared watch id is never re-registered, a wrong-gen clear is inert).
	watches := FoldWatches(m.log)
	for _, wm := range m.watches {
		w := watches[wm.id]
		if w == nil {
			rt.Fatalf("step %d: watch %s missing from FoldWatches", step, wm.id)
			continue // rapid.T.Fatalf halts the test; continue satisfies nil-flow analysis.
		}
		if w.Active != wm.active {
			rt.Fatalf("step %d: watch %s active=%v, model=%v", step, wm.id, w.Active, wm.active)
		}
	}

	// I6: grants match the model and reference real jobs.
	grants := FoldGrants(m.log)
	if eq, a, b := marshalEqual(grants, m.grants); !eq {
		rt.Fatalf("step %d: FoldGrants diverged from model:\n got=%s\n model=%s", step, a, b)
	}
	for obs, jobs := range grants {
		for j := range jobs {
			if fold[j] == nil {
				rt.Fatalf("step %d: grant (%s -> %s) references a job absent from Fold", step, obs, j)
			}
		}
	}

	// I7: watch-send pending set folds to exactly the highest un-settled seq per key.
	ws := FoldWatchSends(m.log)
	expPending := map[WatchSendKey]uint64{}
	for _, k := range m.wsKeys {
		if k.pending {
			expPending[k.key] = k.pendingSeq
		}
	}
	if len(ws.Pending) != len(expPending) {
		rt.Fatalf("step %d: watch-send pending count=%d, model=%d", step, len(ws.Pending), len(expPending))
	}
	for key, seq := range expPending {
		st := ws.Pending[key]
		if st == nil {
			rt.Fatalf("step %d: watch-send key %+v expected pending, absent", step, key)
			continue // rapid.T.Fatalf halts the test; continue satisfies nil-flow analysis.
		}
		if st.UpdateSeq != seq {
			rt.Fatalf("step %d: watch-send key %+v pending seq=%d, model=%d", step, key, st.UpdateSeq, seq)
		}
	}

	// I1 (strongest, last): re-applying the just-appended event is a no-op.
	m.assertReplayIdempotent(rt, step)
}

// assertReplayIdempotent re-folds the log with a duplicate of the most recent
// event (a fresh seq, same payload) appended, and asserts EVERY reducer's output
// is byte-for-byte unchanged. Re-applying any event the reducer just applied is a
// no-op under its monotonic guards (terminal-sticky, notify-monotonic, watch
// active-once, grant dedup, watch-send coalescing), so this is the double-deliver
// / restart-replay safety property.
func (m *seqModel) assertReplayIdempotent(rt *rapid.T, step int) {
	if len(m.log) == 0 {
		return
	}
	dup := m.log[len(m.log)-1]
	dup.Seq = m.seq + 1
	withDup := append(append([]Event(nil), m.log...), dup)

	cases := []struct {
		name string
		once any
		dupl any
	}{
		{"Fold", Fold(m.log), Fold(withDup)},
		{"FoldOrdered", FoldOrdered(m.log), FoldOrdered(withDup)},
		{"FoldDelegates", FoldDelegates(m.log), FoldDelegates(withDup)},
		{"FoldWatches", FoldWatches(m.log), FoldWatches(withDup)},
		{"FoldWatchSends", canonicalWatchSends(FoldWatchSends(m.log)), canonicalWatchSends(FoldWatchSends(withDup))},
		{"FoldGrants", FoldGrants(m.log), FoldGrants(withDup)},
	}
	for _, c := range cases {
		if eq, a, b := marshalEqual(c.once, c.dupl); !eq {
			rt.Fatalf("step %d: duplicating last event (%s) changed %s:\n once=%s\n twice=%s", step, dup.Kind, c.name, a, b)
		}
	}
}

func (m *seqModel) finalChecks(rt *rapid.T) {
	// Determinism: a second fold of the whole sequence is byte-identical.
	if eq, a, b := marshalEqual(Fold(m.log), Fold(m.log)); !eq {
		rt.Fatalf("Fold non-deterministic over sequence:\n a=%s\n b=%s", a, b)
	}
}

func marshalEqual(a, b any) (bool, string, string) {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb), string(ab), string(bb)
}
