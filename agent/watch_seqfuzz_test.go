package agent

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
)

// TestWatchSeqFuzz is the stateful / sequence fuzz of the watch state machine —
// the agent's biggest remaining fuzz gap. Single-shot fuzzers (FuzzWx*, the
// per-op cov_w2watch_* unit tests) each poke one transition; the bugs that only
// appear ACROSS a sequence of transitions (configure → event → output-feed →
// progress-tick → deliver → clear → terminal) are invisible to them. This rapid
// state machine draws a sequence (len ~1-24) of watch operations and replays each
// against ONE real jobManager, checking oracles WEAKEST-FIRST after every op:
//
//	O1 (panic/wedge): no op panics and every op returns under a real wall-time
//	   watchdog (a hang is a wedge). Each op runs under a recovering goroutine.
//	O2 (watch-set consistency): every live watch has a non-empty watch_id and
//	   generation; no watch_id is duplicated among the live set; no watch_id is
//	   simultaneously live (jm.watches) and torn-down-but-flushing (terminalFlush);
//	   a cleared watch_id never reappears live (fresh ids are minted per install,
//	   so a cleared id can never be reused).
//	O3 (delivery budget): no watch config's delivery count ever exceeds
//	   watchDeliveryBudget, and a LIVE watch is always strictly under budget — the
//	   circuit breaker auto-clears exactly at the budget (spec §4 F1), so nothing at
//	   or over budget is ever left installed.
//	O4 (delivery matches the pure core): on a session-event op, the number of
//	   no-send caller notifications the effectful onSessionEvent actually delivers
//	   equals the number the pure evaluateWatchEvent core predicts for the same
//	   (snapshot, event). This is the no-spurious / no-double-delivery guarantee for
//	   the event path: the wrapper must deliver exactly what the core decides.
//	O5 (terminal cleanup): after a job is driven terminal, no live watch targets it
//	   — its watches are flushed and removed (no orphans in jm.watches).
//	O6 (counters monotonic): within a watch config's lifetime (a stable
//	   watch_id/generation), deliveries, the every-Nth event counter, and the
//	   watch-send update sequence never decrease.
//
// Determinism: the job manager runs on a frozen clock (jm.now) and an injected
// fake clock (jm.clock) that never advances, so background progress-timer
// goroutines park on the fake ticker and never fire on wall time; progress ticks
// are driven synchronously via fireProgressTick, exactly as the cov_w2conc
// progress-tick unit tests do. No wall-clock, no rand: a replay of the same drawn
// sequence is identical.
//
// All new symbols are ws_-prefixed so this file never collides with the
// delegate-sequence fuzzer a parallel effort adds to package agent.
func TestWatchSeqFuzz(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		h := ws_newHarness(t, rt)
		defer h.teardown()

		model := ws_newModel()
		n := rapid.IntRange(1, 24).Draw(rt, "nops")
		for i := 0; i < n; i++ {
			op := ws_drawOp(rt, model)
			out, res := h.runOpSafely(op)
			if res.wedged {
				rt.Fatalf("step %d: wedge on op %s", i, ws_opName(op.code))
			}
			if res.panicked {
				rt.Fatalf("step %d: panic on op %s: %v", i, ws_opName(op.code), res.panicVal)
			}
			model.check(rt, h, op, out, i)
		}
	})
}

// --- op vocabulary ---

const (
	ws_opConfigure = iota
	ws_opEmitEvent
	ws_opFeedOutput
	ws_opProgressTick
	ws_opClearByID
	ws_opFinalizeJob
	ws_numOps
)

func ws_opName(code int) string {
	switch code {
	case ws_opConfigure:
		return "configure"
	case ws_opEmitEvent:
		return "emit_event"
	case ws_opFeedOutput:
		return "feed_output"
	case ws_opProgressTick:
		return "progress_tick"
	case ws_opClearByID:
		return "clear_by_id"
	case ws_opFinalizeJob:
		return "finalize_job"
	default:
		return fmt.Sprintf("op(%d)", code)
	}
}

// ws_op is one fully-drawn operation: self-contained so replay never touches
// rapid.
type ws_op struct {
	code int

	// configure
	targetSel   int
	events      []string
	outputMatch string
	progressMS  int
	every       int
	filter      *watchEventFilter
	sendSel     int

	// emit event
	kindSel    int
	dataJobSel int
	toolName   string
	toolErr    bool

	// feed output
	feedJobSel int
	feedTokens []int

	// progress tick
	tickSel int

	// clear
	clearSel int

	// finalize
	finJobSel int
}

var (
	ws_eventNames   = []string{"assistant.tool", "communicate", "job.notification", "*"}
	ws_outputMatch  = []string{"", "ready", "ERROR", "line[0-9]+", "("} // "(" is a bad regex (error path)
	ws_progressMS   = []int{0, minWatchProgressIntervalMS, 60000}
	ws_toolNames    = []string{"", "read_file", "shell"}
	ws_filterStatus = []string{"", "ok", "error"}
	ws_feedTokens   = []string{"ready\n", "ERROR boom\n", "line5\n", "noise\n"}
)

func ws_drawOp(rt *rapid.T, model *ws_model) ws_op {
	op := ws_op{code: rapid.IntRange(0, ws_numOps-1).Draw(rt, "opCode")}
	switch op.code {
	case ws_opConfigure:
		op.targetSel = rapid.IntRange(0, ws_numTargets-1).Draw(rt, "targetSel")
		nEv := rapid.IntRange(0, 2).Draw(rt, "nEvents")
		for j := 0; j < nEv; j++ {
			op.events = append(op.events, rapid.SampledFrom(ws_eventNames).Draw(rt, "eventName"))
		}
		op.outputMatch = rapid.SampledFrom(ws_outputMatch).Draw(rt, "outputMatch")
		op.progressMS = rapid.SampledFrom(ws_progressMS).Draw(rt, "progressMS")
		op.every = rapid.IntRange(0, 3).Draw(rt, "every")
		if rapid.Bool().Draw(rt, "hasFilter") {
			op.filter = &watchEventFilter{
				ToolName: rapid.SampledFrom(ws_toolNames).Draw(rt, "filterTool"),
				Status:   rapid.SampledFrom(ws_filterStatus).Draw(rt, "filterStatus"),
			}
		}
		op.sendSel = rapid.IntRange(0, 2).Draw(rt, "sendSel")
	case ws_opEmitEvent:
		op.kindSel = rapid.IntRange(0, ws_numKinds-1).Draw(rt, "kindSel")
		op.dataJobSel = rapid.IntRange(0, ws_numTargets-1).Draw(rt, "dataJobSel")
		op.toolName = rapid.SampledFrom(ws_toolNames).Draw(rt, "emitTool")
		op.toolErr = rapid.Bool().Draw(rt, "emitToolErr")
	case ws_opFeedOutput:
		op.feedJobSel = rapid.IntRange(0, ws_numJobs-1).Draw(rt, "feedJobSel")
		nLines := rapid.IntRange(1, 8).Draw(rt, "feedLines")
		for j := 0; j < nLines; j++ {
			op.feedTokens = append(op.feedTokens, rapid.IntRange(0, len(ws_feedTokens)-1).Draw(rt, "feedToken"))
		}
	case ws_opProgressTick:
		op.tickSel = rapid.IntRange(0, 63).Draw(rt, "tickSel")
	case ws_opClearByID:
		// index == len(installed) selects a bogus id (idempotent no-op path).
		op.clearSel = rapid.IntRange(0, len(model.installedIDs)).Draw(rt, "clearSel")
	case ws_opFinalizeJob:
		op.finJobSel = rapid.IntRange(0, ws_numJobs-1).Draw(rt, "finJobSel")
	}
	return op
}

// --- target vocabulary ---

const (
	ws_numJobs    = 3 // concrete running-shell slots
	ws_numTargets = ws_numJobs + 3
	ws_numKinds   = 5
)

// --- harness (one real jobManager per rapid iteration) ---

type ws_harness struct {
	t      *testing.T
	jm     *jobManager
	clk    *agenttest.FakeClock
	dir    string
	jobIDs []string

	capMu    sync.Mutex
	captured []jobNotification

	// feedOffsets tracks each job's monotone lifetime output offset so
	// feedJobOutput's monotone-offset contract holds across sequential feeds.
	feedOffsets map[string]int64

	// installedSnapshot mirrors the model's installed-id slice so applyClear
	// (which runs on the op goroutine) can resolve the drawn selector without
	// touching the model concurrently. The driver loop refreshes it before the
	// next op via model.check.
	installedSnapshot []string
}

const ws_sessionID = "S1"

func ws_newHarness(t *testing.T, rt *rapid.T) *ws_harness {
	t.Helper()
	dir, err := os.MkdirTemp("", "ws_seqfuzz")
	if err != nil {
		rt.Fatalf("mkdir temp: %v", err)
	}
	h := &ws_harness{t: t, dir: dir, feedOffsets: map[string]int64{}}
	jm, err := newJobManager(dir, ws_sessionID, func(n jobNotification) {
		h.capMu.Lock()
		h.captured = append(h.captured, n)
		h.capMu.Unlock()
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		rt.Fatalf("newJobManager: %v", err)
	}
	h.jm = jm
	// Frozen jm.now plus a never-advanced fake clock: background progress-timer
	// goroutines park on the fake ticker and never fire on wall time.
	freezeClock(jm)
	h.clk = agenttest.NewFakeClock()
	jm.clock = h.clk

	// A seeded delegate target so send-to-delegate watches validate and install.
	seedWatchSendDelegateTarget(t, jm, "dlg_obs")

	for i := 0; i < ws_numJobs; i++ {
		rec, err := jm.createShell(createShellOpts{Command: fmt.Sprintf("job %d", i), Description: "seqfuzz"})
		if err != nil {
			rt.Fatalf("createShell: %v", err)
		}
		h.jobIDs = append(h.jobIDs, rec.JobID)
	}
	return h
}

func (h *ws_harness) teardown() {
	// Finalize any still-running job so jm.close does not wait on the never-
	// advanced fake clock, then close (tears down every remaining watch, which
	// unblocks its parked progress-timer goroutine).
	for _, id := range h.jobIDs {
		_ = h.jm.finalize(id, jobstore.StatusCompleted, "seqfuzz_teardown", nil)
	}
	_ = h.jm.close()
	_ = os.RemoveAll(h.dir)
}

func (h *ws_harness) target(sel int) string {
	switch sel {
	case 0, 1, 2:
		return h.jobIDs[sel]
	case ws_numJobs:
		return runtimeMessageAliasCaller
	case ws_numJobs + 1:
		return "*"
	default:
		return "job_ws_absent"
	}
}

// --- op application under panic/wedge safety ---

// ws_opOutcome carries the observations a post-op oracle needs from applyOp.
type ws_opOutcome struct {
	installedID   string // configure: the watch_id of a newly-installed live watch
	clearedID     string // clear: the watch_id that was targeted
	finalizedJob  string // finalize: the job driven terminal
	emitChecked   bool   // emit: the differential prediction is meaningful
	emitPredicted int    // emit: predicted no-send caller deliveries
	emitObserved  int    // emit: observed "event:" caller notifications
}

type ws_safeResult struct {
	panicked bool
	panicVal any
	wedged   bool
}

const ws_opTimeout = 10 * time.Second

func (h *ws_harness) runOpSafely(op ws_op) (ws_opOutcome, ws_safeResult) {
	type done struct {
		out ws_opOutcome
		res ws_safeResult
	}
	ch := make(chan done, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- done{res: ws_safeResult{panicked: true, panicVal: r}}
			}
		}()
		out := h.applyOp(op)
		ch <- done{out: out}
	}()
	select {
	case d := <-ch:
		return d.out, d.res
	case <-time.After(ws_opTimeout):
		return ws_opOutcome{}, ws_safeResult{wedged: true}
	}
}

func (h *ws_harness) applyOp(op ws_op) ws_opOutcome {
	switch op.code {
	case ws_opConfigure:
		return h.applyConfigure(op)
	case ws_opEmitEvent:
		return h.applyEmit(op)
	case ws_opFeedOutput:
		h.applyFeed(op)
	case ws_opProgressTick:
		h.applyProgressTick(op)
	case ws_opClearByID:
		return h.applyClear(op)
	case ws_opFinalizeJob:
		return h.applyFinalize(op)
	}
	return ws_opOutcome{}
}

func (h *ws_harness) applyConfigure(op ws_op) ws_opOutcome {
	a := watchArgs{
		Target:             h.target(op.targetSel),
		Events:             op.events,
		OutputMatch:        op.outputMatch,
		ProgressIntervalMS: op.progressMS,
		Every:              op.every,
		EventFilter:        op.filter,
	}
	switch op.sendSel {
	case 1:
		a.Send = &watchSendArgs{To: runtimeMessageAliasCaller}
	case 2:
		a.Send = &watchSendArgs{To: "dlg_obs", Message: "observe"}
	}
	res, err := h.jm.configureWatch(a)
	if err != nil || !res.Watching {
		return ws_opOutcome{}
	}
	return ws_opOutcome{installedID: res.WatchID}
}

func (h *ws_harness) applyEmit(op ws_op) ws_opOutcome {
	ev := h.buildEvent(op)

	// Predict, under the same lock onSessionEvent takes, what the pure core
	// decides for every live watch: the number of no-send caller deliveries.
	predicted := 0
	h.jm.mu.Lock()
	for _, cfg := range h.jm.watches {
		dec := evaluateWatchEvent(cfg.eventSnapshot(isActiveWatchTargetLocked(h.jm, cfg.target)), ev)
		if dec.matched && !dec.send {
			predicted++
		}
	}
	h.jm.mu.Unlock()

	h.capMu.Lock()
	before := len(h.captured)
	h.capMu.Unlock()

	h.jm.onSessionEvent(ev)

	// Isolate the no-send notification rail: its notifications carry reason
	// "event: <kind>" and a nil WatchSend token. The budget-cleared circuit
	// breaker carries reason "watch cleared: ...", and a caller-targeted SEND also
	// enqueues an "event:"-reason notification but with a non-nil WatchSend token
	// (it rides the watch-send rail, which evaluateWatchEvent routes as dec.send) —
	// both are excluded here so the count matches the pure core's no-send decision.
	observed := 0
	h.capMu.Lock()
	for _, n := range h.captured[before:] {
		if n.WatchSend == nil && strings.HasPrefix(n.Reason, "event:") {
			observed++
		}
	}
	h.capMu.Unlock()

	return ws_opOutcome{emitChecked: true, emitPredicted: predicted, emitObserved: observed}
}

func (h *ws_harness) buildEvent(op ws_op) events.SessionEvent {
	ev := events.SessionEvent{SessionID: h.jm.sessionID}
	jobID := h.target(op.dataJobSel)
	switch op.kindSel {
	case 0:
		ev.Kind = events.EventToolCallEnd
		errStr := ""
		if op.toolErr {
			errStr = "boom"
		}
		ev.Data = events.ToolCallEndData{ToolName: op.toolName, CallID: "c1", Error: errStr}
	case 1:
		ev.Kind = events.EventCommunicate
		ev.Data = events.CommunicateData{Message: "hi"}
	case 2:
		ev.Kind = events.EventJobFinished
		ev.Data = events.JobFinishedData{JobID: jobID, Status: string(jobstore.StatusCompleted)}
	case 3:
		ev.Kind = events.EventJobStarted
		ev.Data = events.JobStartedData{JobID: jobID, Status: string(jobstore.StatusRunning)}
	default:
		ev.Kind = events.EventError
		ev.Data = events.ErrorData{}
	}
	return ev
}

func (h *ws_harness) applyFeed(op ws_op) {
	jobID := h.jobIDs[op.feedJobSel]
	var b strings.Builder
	for _, tok := range op.feedTokens {
		b.WriteString(ws_feedTokens[tok])
	}
	chunk := []byte(b.String())
	if len(chunk) == 0 {
		return
	}
	h.feedOffsets[jobID] += int64(len(chunk))
	h.jm.feedJobOutput(jobID, chunk, h.feedOffsets[jobID])
}

func (h *ws_harness) applyProgressTick(op ws_op) {
	// Pick a live watch deterministically (sorted by watch_id) and fire one tick
	// synchronously — the same call the ticker goroutine would make.
	type liveWatch struct {
		key watchKey
		cfg *watchConfig
	}
	var live []liveWatch
	h.jm.mu.Lock()
	for key, cfg := range h.jm.watches {
		live = append(live, liveWatch{key, cfg})
	}
	h.jm.mu.Unlock()
	if len(live) == 0 {
		return
	}
	sort.Slice(live, func(i, j int) bool { return live[i].cfg.watchID < live[j].cfg.watchID })
	w := live[op.tickSel%len(live)]
	h.jm.fireProgressTick(w.key, w.cfg)
}

func (h *ws_harness) applyClear(op ws_op) ws_opOutcome {
	// clearSel was drawn against the model's installed set at draw time; resolve
	// it against the harness mirror of that set. Out-of-range selects a bogus id
	// (the idempotent no-op path).
	id := "watch_ws_absent"
	if op.clearSel < len(h.installedSnapshot) {
		id = h.installedSnapshot[op.clearSel]
	}
	_, _ = h.jm.clearWatchByID(id)
	return ws_opOutcome{clearedID: id}
}

func (h *ws_harness) applyFinalize(op ws_op) ws_opOutcome {
	jobID := h.jobIDs[op.finJobSel]
	_ = h.jm.finalize(jobID, jobstore.StatusCompleted, "seqfuzz_finalize", nil)
	return ws_opOutcome{finalizedJob: jobID}
}

// --- the monotonic model + oracle checks ---

type ws_watchState struct {
	gen        string
	deliveries int
	eventCount int
	nextSeq    uint64
}

type ws_model struct {
	installedIDs []string
	clearedIDs   map[string]bool
	perWatch     map[string]ws_watchState
}

func ws_newModel() *ws_model {
	return &ws_model{
		clearedIDs: map[string]bool{},
		perWatch:   map[string]ws_watchState{},
	}
}

// ws_watchSnap is a read-only copy of the per-config oracle fields, taken under
// jm.mu.
type ws_watchSnap struct {
	watchID    string
	generation string
	target     string
	deliveries int
	eventCount int
	nextSeq    uint64
	live       bool
}

func (m *ws_model) check(rt *rapid.T, h *ws_harness, op ws_op, out ws_opOutcome, step int) {
	// Fold op outcomes into the model up front so the invariant pass sees the
	// post-op installed/cleared sets.
	if out.installedID != "" {
		m.installedIDs = append(m.installedIDs, out.installedID)
	}
	if out.clearedID != "" && out.clearedID != "watch_ws_absent" {
		m.clearedIDs[out.clearedID] = true
	}
	// Refresh the harness snapshot of installed ids for the NEXT op's applyClear.
	h.installedSnapshot = append(h.installedSnapshot[:0], m.installedIDs...)

	snaps := h.snapshotWatches()

	// O2: watch-set consistency.
	liveIDs := map[string]bool{}
	flushIDs := map[string]bool{}
	for _, s := range snaps {
		if s.watchID == "" {
			rt.Fatalf("step %d: watch config with empty watch_id (target=%q)", step, s.target)
		}
		if s.generation == "" {
			rt.Fatalf("step %d: watch %s has empty generation", step, s.watchID)
		}
		if s.live {
			if liveIDs[s.watchID] {
				rt.Fatalf("step %d: watch_id %s appears twice among live watches", step, s.watchID)
			}
			liveIDs[s.watchID] = true
			if m.clearedIDs[s.watchID] {
				rt.Fatalf("step %d: cleared watch_id %s is live again", step, s.watchID)
			}
		} else {
			flushIDs[s.watchID] = true
		}
	}
	for id := range liveIDs {
		if flushIDs[id] {
			rt.Fatalf("step %d: watch_id %s is both live and torn-down (terminalFlush)", step, id)
		}
	}

	// O3: delivery budget.
	for _, s := range snaps {
		if s.deliveries > watchDeliveryBudget {
			rt.Fatalf("step %d: watch %s deliveries=%d exceeds budget %d", step, s.watchID, s.deliveries, watchDeliveryBudget)
		}
		if s.live && s.deliveries >= watchDeliveryBudget {
			rt.Fatalf("step %d: live watch %s at/over budget (%d) was not auto-cleared", step, s.watchID, s.deliveries)
		}
	}

	// O4: on an event op, the effectful deliver count equals the pure core's
	// prediction (no spurious, no double, no dropped no-send delivery).
	if out.emitChecked && out.emitPredicted != out.emitObserved {
		rt.Fatalf("step %d: emit delivered %d no-send notifications, pure core predicted %d",
			step, out.emitObserved, out.emitPredicted)
	}

	// O5: terminal cleanup — no live watch targets a just-finalized job.
	if out.finalizedJob != "" {
		for _, s := range snaps {
			if s.live && s.target == out.finalizedJob {
				rt.Fatalf("step %d: watch %s still targets finalized job %s (orphan)", step, s.watchID, out.finalizedJob)
			}
		}
	}

	// O6: per-config counters are monotonic within a generation.
	for _, s := range snaps {
		if prev, ok := m.perWatch[s.watchID]; ok && prev.gen == s.generation {
			if s.deliveries < prev.deliveries {
				rt.Fatalf("step %d: watch %s deliveries regressed %d -> %d", step, s.watchID, prev.deliveries, s.deliveries)
			}
			if s.eventCount < prev.eventCount {
				rt.Fatalf("step %d: watch %s eventCount regressed %d -> %d", step, s.watchID, prev.eventCount, s.eventCount)
			}
			if s.nextSeq < prev.nextSeq {
				rt.Fatalf("step %d: watch %s update-seq regressed %d -> %d", step, s.watchID, prev.nextSeq, s.nextSeq)
			}
		}
		m.perWatch[s.watchID] = ws_watchState{gen: s.generation, deliveries: s.deliveries, eventCount: s.eventCount, nextSeq: s.nextSeq}
	}
}

func (h *ws_harness) snapshotWatches() []ws_watchSnap {
	h.jm.mu.Lock()
	defer h.jm.mu.Unlock()
	out := make([]ws_watchSnap, 0, len(h.jm.watches)+len(h.jm.terminalFlush))
	for _, cfg := range h.jm.watches {
		out = append(out, ws_snapConfig(cfg, true))
	}
	for cfg := range h.jm.terminalFlush {
		out = append(out, ws_snapConfig(cfg, false))
	}
	return out
}

func ws_snapConfig(cfg *watchConfig, live bool) ws_watchSnap {
	return ws_watchSnap{
		watchID:    cfg.watchID,
		generation: cfg.generation,
		target:     cfg.target,
		deliveries: cfg.deliveries,
		eventCount: cfg.eventCount,
		nextSeq:    cfg.nextUpdateSeq,
		live:       live,
	}
}
