package agent

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
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
//	   live after its durable end. terminalFlush also retains active callback
//	   sources: overlap must be the same config with an owed durable frame;
//	   a cleared watch_id never reappears live (fresh ids are minted per install,
//	   so a cleared id can never be reused).
//	O3 (delivery budget): the budget is a circuit breaker on CONDITION fires. For
//	   a watch with no progress interval, no config's delivery count ever exceeds
//	   watchDeliveryBudget and a LIVE watch is always strictly under budget — the
//	   breaker auto-clears on the budget-th fire (spec §4 F1), so nothing at or
//	   over budget is ever left installed. A periodic tick counts a delivery
//	   without being a condition fire, so a watch carrying a progress interval may
//	   sit at or over the budget and stay live.
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
// evener:fuzz rapid
func TestWatchSeqFuzz(t *testing.T) {
	if os.Getenv("EVENER_FUZZ_TESTS") != "1" {
		t.Skip("fuzz: skipped by default; run `make test-fuzz`, or EVENER_FUZZ_TESTS=1 go test ./agent -run TestWatchSeqFuzz -count=1 -v")
	}
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		h := ws_newHarness(t, rt)
		defer h.teardown()

		model := ws_newModel()
		n := rapid.IntRange(1, 24).Draw(rt, "nops")
		for i := range n {
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
		for range nEv {
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
		op.sendSel = rapid.IntRange(0, 1).Draw(rt, "sendSel")
	case ws_opEmitEvent:
		op.kindSel = rapid.IntRange(0, ws_numKinds-1).Draw(rt, "kindSel")
		op.dataJobSel = rapid.IntRange(0, ws_numTargets-1).Draw(rt, "dataJobSel")
		op.toolName = rapid.SampledFrom(ws_toolNames).Draw(rt, "emitTool")
		op.toolErr = rapid.Bool().Draw(rt, "emitToolErr")
	case ws_opFeedOutput:
		op.feedJobSel = rapid.IntRange(0, ws_numJobs-1).Draw(rt, "feedJobSel")
		nLines := rapid.IntRange(1, 8).Draw(rt, "feedLines")
		for range nLines {
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

const ws_sessionID = testOwnerSessionID

func ws_newHarness(t *testing.T, rt interface{ Fatalf(string, ...any) }) *ws_harness {
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

	for i := range ws_numJobs {
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
	if op.sendSel == 1 {
		a.Send = &watchSendArgs{To: runtimeMessageAliasCaller}
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

	// Isolate the no-send notification rail by shape. A job-finished notification
	// replaces the generic event reason with the concrete terminal reason, so the
	// reason prefix is not a stable delivery discriminator. The budget-cleared
	// circuit breaker remains distinguishable by its "watch cleared:" reason, and
	// a caller-targeted SEND carries a non-nil WatchSend token.
	observed := 0
	h.capMu.Lock()
	for _, n := range h.captured[before:] {
		if n.Kind == jobNotificationKindWatch && !strings.HasPrefix(n.Reason, "watch cleared:") {
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
	config     *watchConfig // identity only, never dereferenced outside jm.mu
	watchID    string
	generation string
	target     string
	deliveries int
	eventCount int
	nextSeq    uint64
	live       bool
	retired    bool
	pending    map[jobstore.WatchSendKey]jobstore.WatchSendState
	// periodic marks a config whose progress timer counts deliveries that are not
	// condition fires, which the budget deliberately does not bound (see O3).
	periodic bool
}

func (m *ws_model) check(rt *rapid.T, h *ws_harness, _ ws_op, out ws_opOutcome, step int) {
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

	// O2: registration ends and pending custody are separate facts. Read the
	// journal independently: losing a source from both maps must not hide it.
	watches, err := h.jm.store.LoadWatches()
	if err != nil {
		rt.Fatalf("load watch registrations: %v", err)
	}
	for i := range snaps {
		if record := watches[snaps[i].watchID]; record != nil {
			snaps[i].retired = !record.Active
		}
	}
	sends, err := h.jm.store.LoadWatchSends()
	if err != nil {
		rt.Fatalf("load watch source journal: %v", err)
	}
	if err := ws_checkCustody(snaps, sends.Pending, m.clearedIDs); err != nil {
		rt.Fatalf("step %d: %v", step, err)
	}

	// O3: delivery budget. A periodic tick counts a delivery without being a
	// condition fire, so the bound holds only for watches with no progress
	// interval.
	for _, s := range snaps {
		if s.periodic {
			continue
		}
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
	snap := ws_watchSnap{
		config:     cfg,
		watchID:    cfg.watchID,
		generation: cfg.generation,
		target:     cfg.target,
		deliveries: cfg.deliveries,
		eventCount: cfg.eventCount,
		nextSeq:    cfg.nextUpdateSeq,
		live:       live,
		periodic:   cfg.progressIntervalMS > 0,
		pending:    make(map[jobstore.WatchSendKey]jobstore.WatchSendState, len(cfg.pending)),
	}
	for key, state := range cfg.pending {
		snap.pending[key] = *state
	}
	return snap
}

// ws_checkCustody checks observations, not production routing decisions. A
// durable end forbids a live registration even when a valid frame is owed.
// Conversely, every owed journal frame must remain reachable until custody.
func ws_checkCustody(snaps []ws_watchSnap, pending map[jobstore.WatchSendKey]*jobstore.WatchSendState, cleared map[string]bool) error {
	live, flushing := map[string]ws_watchSnap{}, map[string]ws_watchSnap{}
	reachable := map[jobstore.WatchSendKey]jobstore.WatchSendState{}
	for _, s := range snaps {
		if s.watchID == "" || s.generation == "" {
			return fmt.Errorf("watch identity incomplete: id=%q generation=%q", s.watchID, s.generation)
		}
		set := flushing
		if s.live {
			if s.retired || cleared[s.watchID] {
				return fmt.Errorf("retired watch %s is live", s.watchID)
			}
			set = live
		}
		if _, exists := set[s.watchID]; exists {
			return fmt.Errorf("watch %s appears twice in one set", s.watchID)
		}
		set[s.watchID] = s
		for key, state := range s.pending {
			durable := pending[key]
			if durable == nil || durable.DeliveryID != state.DeliveryID || durable.UpdateSeq != state.UpdateSeq || key.WatchID != s.watchID || key.WatchGeneration != s.generation {
				return fmt.Errorf("watch %s pending frame lacks matching durable source", s.watchID)
			}
			reachable[key] = state
		}
	}
	for id, s := range flushing {
		if active, ok := live[id]; ok {
			if active.config != s.config || active.generation != s.generation || len(s.pending) == 0 {
				return fmt.Errorf("watch %s active/flush overlap is not one owed source", id)
			}
		}
	}
	for key, state := range pending {
		if source, ok := reachable[key]; !ok || source.DeliveryID != state.DeliveryID || source.UpdateSeq != state.UpdateSeq {
			return fmt.Errorf("watch %s durable pending frame disappeared from source maps", key.WatchID)
		}
	}
	return nil
}

func TestWatchSeqFuzz_CustodyInvariant(t *testing.T) {
	t.Parallel()
	key := jobstore.WatchSendKey{WatchID: "watch_source", WatchGeneration: "generation"}
	state := jobstore.WatchSendState{Key: key, DeliveryID: "delivery", UpdateSeq: 1}
	config := &watchConfig{}
	active := ws_watchSnap{config: config, watchID: key.WatchID, generation: key.WatchGeneration, live: true, pending: map[jobstore.WatchSendKey]jobstore.WatchSendState{key: state}}
	flushing := active
	flushing.live = false
	pending := map[jobstore.WatchSendKey]*jobstore.WatchSendState{key: &state}
	retired := active
	retired.retired = true
	other := flushing
	other.config = &watchConfig{}
	empty := active
	empty.pending = nil
	emptyFlush := empty
	emptyFlush.live = false
	tests := []struct {
		name    string
		snaps   []ws_watchSnap
		pending map[jobstore.WatchSendKey]*jobstore.WatchSendState
		wantErr bool
	}{
		{"active callback owes durable frame", []ws_watchSnap{active, flushing}, pending, false},
		{"retired source still reachable", []ws_watchSnap{flushing}, pending, false},
		{"live pending work disappeared", nil, pending, true},
		{"retired watch stays live", []ws_watchSnap{retired, flushing}, pending, true},
		{"different config reuses id", []ws_watchSnap{active, other}, pending, true},
		{"shared pointer alone is insufficient", []ws_watchSnap{empty, emptyFlush}, map[jobstore.WatchSendKey]*jobstore.WatchSendState{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ws_checkCustody(tt.snaps, tt.pending, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("custody error=%v, want error=%t", err, tt.wantErr)
			}
		})
	}
}

func TestWatchSeqFuzz_ActivePendingCallbackRetriesAutomatically(t *testing.T) {
	t.Parallel()
	h := ws_newHarness(t, t)
	defer h.teardown()
	stateDir := t.TempDir()
	owner := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{StateDir: stateDir}), withClock(h.clk))
	if err := owner.transcript.Close(); err != nil {
		t.Fatal(err)
	}
	fs := newAttentionFailNextSyncFS()
	writer, err := transcript.NewWriterWithFS(fs, transcriptPath(stateDir, owner.ID()), transcript.Header{SessionID: owner.ID()})
	if err != nil {
		t.Fatal(err)
	}
	owner.attachTranscript(writer)
	h.jm.sessionID, h.jm.retirementOwner = owner.ID(), owner
	wakes := make(chan struct{}, 2)
	owner.SetNotifyFunc(func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	})
	configured := h.applyConfigure(ws_op{targetSel: 1, events: []string{"*"}, outputMatch: "ready", progressMS: minWatchProgressIntervalMS})
	if configured.installedID == "" {
		t.Fatal("watch was not installed")
	}
	fs.failNextSync()
	h.applyProgressTick(ws_op{tickSel: 55})
	snaps := h.snapshotWatches()
	sends, err := h.jm.store.LoadWatchSends()
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 || len(sends.Pending) != 1 {
		t.Fatalf("active/owed snapshots=%d pending=%d, want 2/1", len(snaps), len(sends.Pending))
	}
	if err := ws_checkCustody(snaps, sends.Pending, nil); err != nil {
		t.Fatal(err)
	}
	var frame *jobstore.WatchSendState
	for _, state := range sends.Pending {
		frame = state
	}
	// Advance only the paced transport retry, not the watch's progress cadence.
	h.clk.Advance(jobNotificationRetryInitialDelay)
	h.clk.Drain()
	sends, err = h.jm.store.LoadWatchSends()
	if err != nil {
		t.Fatal(err)
	}
	if len(sends.Pending) != 0 {
		t.Fatal("automatic retry left the source frame pending")
	}
	entries := notificationRawEntries(t, transcriptPath(stateDir, owner.ID()))
	if len(entries) != 1 || entries[0].Turn.AttentionID != stableWatchAttentionID(*frame) {
		t.Fatalf("receiver custody=%+v, want one exact frame", entries)
	}
	select {
	case <-wakes:
	default:
		t.Fatal("automatic custody retry did not wake receiver")
	}
	snaps = h.snapshotWatches()
	if len(snaps) != 1 || !snaps[0].live || snaps[0].watchID != configured.installedID {
		t.Fatalf("settled active watch snapshots=%+v", snaps)
	}
	if err := ws_checkCustody(snaps, sends.Pending, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWatchSeqFuzz_RetirementRetainsPendingSource(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"replace", "clear", "stop"} {
		for _, send := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/send=%d", action, send), func(t *testing.T) {
				h := ws_newHarness(t, t)
				defer h.teardown()
				stateDir := t.TempDir()
				var request llm.Request
				owner := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{StateDir: stateDir}), withClock(h.clk), withSteps(func(req llm.Request) llm.Response {
					request = req
					return finalResponse("watch observed")
				}))
				if err := owner.transcript.Close(); err != nil {
					t.Fatal(err)
				}
				fs := newAttentionFailNextSyncFS()
				writer, err := transcript.NewWriterWithFS(fs, transcriptPath(stateDir, owner.ID()), transcript.Header{SessionID: owner.ID()})
				if err != nil {
					t.Fatal(err)
				}
				owner.attachTranscript(writer)
				h.jm.sessionID, h.jm.retirementOwner = owner.ID(), owner
				wakes := make(chan struct{}, 1)
				owner.SetNotifyFunc(func() {
					select {
					case wakes <- struct{}{}:
					default:
					}
				})
				first := h.applyConfigure(ws_op{targetSel: 0, progressMS: minWatchProgressIntervalMS, sendSel: send})
				if first.installedID == "" {
					t.Fatal("first watch was not installed")
				}
				fs.failNextSync()
				h.applyProgressTick(ws_op{})
				sends, err := h.jm.store.LoadWatchSends()
				if err != nil {
					t.Fatal(err)
				}
				if len(sends.Pending) != 1 {
					t.Fatalf("initial pending sends=%d, want 1", len(sends.Pending))
				}
				var frame jobstore.WatchSendState
				for _, state := range sends.Pending {
					frame = *state
				}
				if frame.CallbackFrame != (send == 0) || frame.ReceiverSessionID != owner.ID() {
					t.Fatalf("source callback=%t receiver=%q, want callback=%t receiver=%q", frame.CallbackFrame, frame.ReceiverSessionID, send == 0, owner.ID())
				}
				switch action {
				case "replace":
					second := h.applyConfigure(ws_op{targetSel: 0, outputMatch: "ready", sendSel: send})
					if second.installedID == "" || second.installedID == first.installedID {
						t.Fatal("replacement was not installed with fresh identity")
					}
				case "clear":
					if _, err := h.jm.clearWatchByID(first.installedID); err != nil {
						t.Fatal(err)
					}
				case "stop":
					if _, err := h.jm.stop(h.jobIDs[0]); err != nil {
						t.Fatal(err)
					}
				}
				sends, err = h.jm.store.LoadWatchSends()
				if err != nil {
					t.Fatal(err)
				}
				retired := map[string]bool{first.installedID: action != "stop"}
				if err := ws_checkCustody(h.snapshotWatches(), sends.Pending, retired); err != nil {
					t.Error(err)
				}
				// Only the existing paced retry recovers transport. The following
				// notification turn observes custody after the automatic wake.
				h.clk.Advance(jobNotificationRetryInitialDelay)
				h.clk.Drain()
				sends, err = h.jm.store.LoadWatchSends()
				if err != nil {
					t.Fatal(err)
				}
				if len(sends.Pending) != 0 {
					t.Fatalf("automatic retry left %d retired source frames owed", len(sends.Pending))
				}
				for _, snap := range h.snapshotWatches() {
					if snap.watchID == first.installedID {
						// A job_stop request is not the source's terminal edge.
						if action != "stop" || !snap.live || len(snap.pending) != 0 {
							t.Fatal("settled historical source remains in watch maps")
						}
					}
				}
				entries := notificationRawEntries(t, transcriptPath(stateDir, owner.ID()))
				id := stableWatchAttentionID(frame)
				var message llm.Message
				count := 0
				for _, entry := range entries {
					if entry.Turn.AttentionID == id {
						count++
						message = entry.Turn.Message
					}
				}
				if count != 1 {
					t.Fatalf("retired frame custody records=%d, want exactly one", count)
				}
				select {
				case <-wakes:
				default:
					t.Fatal("automatic custody did not wake receiver")
				}
				if _, err := owner.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
					t.Fatal(err)
				}
				observed := 0
				for _, got := range request.Messages {
					if reflect.DeepEqual(got, message) {
						observed++
					}
				}
				if observed != 1 {
					t.Fatalf("retired frame request occurrences=%d, want exactly one", observed)
				}
				assertAttentionResponseReceipts(t, owner, id, 1)
				if action == "stop" {
					if err := h.jm.finalize(h.jobIDs[0], jobstore.StatusCancelled, "cancelled", nil); err != nil {
						t.Fatal(err)
					}
					for _, snap := range h.snapshotWatches() {
						if snap.watchID == first.installedID {
							t.Fatal("terminal source retained its stopped watch")
						}
					}
				}
			})
		}
	}
}
