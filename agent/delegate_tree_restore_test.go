package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestDelegateAttentionSchemaStrictRoundTrip(t *testing.T) {
	t.Parallel()
	entries := [][]byte{
		[]byte(`{"kind":"entry","seq":0,"turn":{"kind":"STEERING","message":{},"timestamp":"0001-01-01T00:00:00Z","usage":{},"attention_id":"shell:job-shell:terminal-1"}}`),
		[]byte(`{"kind":"entry","seq":1,"turn":{"kind":"ATTENTION_RESOLUTION","message":{},"timestamp":"0001-01-01T00:00:00Z","usage":{},"attention_resolution":{"attention_id":"shell:job-shell:terminal-1","disposition":"discarded"}}}`),
	}
	for _, raw := range entries {
		entry, err := transcript.DecodeEntry(raw)
		if err != nil {
			t.Fatalf("strict decode %s: %v", raw, err)
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal decoded entry: %v", err)
		}
		if bytes.Contains(raw, []byte(`"attention_id"`)) && !bytes.Contains(encoded, []byte(`"attention_id"`)) {
			t.Fatalf("private attention identity was dropped: %s", encoded)
		}
	}
}

func TestDelegateControllerCloseExecutesColdAttentionCleanup(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	path := filepath.Join(c.stateDir, schema.SessionsSubdir, "child-dlg_target.transcript.jsonl")
	writeDelegateAttentionTranscript(t, path, "child-dlg_target", "attention-close")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Close(ctx); err != nil {
		t.Fatalf("Close with synchronously repairable attention: %v", err)
	}
	// Close resolves the cold row in delegatestore; no transcript marker is
	// written, so the journal is the record of the repair.
	c.mu.Lock()
	_, resolved := c.durable["dlg_target"].ResolvedAttention["attention-close"]
	pendingRows := append([]delegatestore.PendingAttention(nil), c.durable["dlg_target"].PendingAttention...)
	c.mu.Unlock()
	if !resolved || len(pendingRows) != 0 {
		t.Fatalf("attention after close resolved=%t pending=%#v, want the journal resolution", resolved, pendingRows)
	}
}

// A restart between a busy-class refusal and the retry that would present the
// reopened row must not read the transcript as one delegate owing two
// generations: the consume markers the failed starts left behind claim the
// same unjournaled generation — the source's claim and the reopened row's own
// claim, one reopen identity deeper per refusal — and both are superseded by
// the pending row their reopens re-delivered. The restore read skips any
// claim whose reopen row exists as pending content: the reopened row presents
// through the ordinary pending path, so admitting the superseded source would
// double-present the report, and the chain's second claim would read as a
// second owed generation — an error that recurs on every restart, because
// nothing rewrites the transcript.
func TestDelegateAttentionRestoreSkipsSupersededResumeClaims(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 8, 8)
	const chainedID = "dlg_reopen_chain"
	const singleID = "dlg_reopen_single"
	seedDelegateControllerIdle(t, c, chainedID, "")
	seedDelegateControllerIdle(t, c, singleID, "")

	const report = "the retained child's report"
	chainedPath := filepath.Join(c.stateDir, schema.SessionsSubdir, "child-"+chainedID+".transcript.jsonl")
	const source = "delegate:chain-source"
	firstReopen := delegateAttentionReopenID(source, 1)
	secondReopen := delegateAttentionReopenID(firstReopen, 1)
	appendColdAttentionRow(t, chainedPath, "child-"+chainedID, source, report)
	appendColdAttentionRow(t, chainedPath, "child-"+chainedID, firstReopen, report)
	appendColdAttentionRow(t, chainedPath, "child-"+chainedID, secondReopen, report)
	appendColdConsumedClaim(t, chainedPath, "child-"+chainedID, source, 1)
	appendColdConsumedClaim(t, chainedPath, "child-"+chainedID, firstReopen, 1)

	singlePath := filepath.Join(c.stateDir, schema.SessionsSubdir, "child-"+singleID+".transcript.jsonl")
	const singleSource = "delegate:single-source"
	singleReopen := delegateAttentionReopenID(singleSource, 1)
	appendColdAttentionRow(t, singlePath, "child-"+singleID, singleSource, report)
	appendColdAttentionRow(t, singlePath, "child-"+singleID, singleReopen, report)
	appendColdConsumedClaim(t, singlePath, "child-"+singleID, singleSource, 1)

	owed, err := c.owedAttentionStartsFromTranscripts()
	if err != nil {
		t.Fatalf("restore read of a reopen-chain transcript: %v", err)
	}
	for _, start := range owed {
		if start.delegateID == chainedID || start.delegateID == singleID {
			t.Fatalf("restore admitted generation %d for superseded attention %q on %s; the pending reopened row owns the presentation",
				start.generation, start.attentionID, start.delegateID)
		}
	}
	// The reopened rows are what the restart presents: one pending row per
	// delegate, the deepest chain member, and the superseded sources resolved.
	chainedFold, foldErr := readDelegateAttentionFold(chainedPath, "child-"+chainedID)
	if foldErr != nil {
		t.Fatalf("read the chained delegate's fold: %v", foldErr)
	}
	if pending := chainedFold.pendingIDs(); len(pending) != 1 || pending[0] != secondReopen {
		t.Fatalf("the chained delegate's pending attention = %#v, want only the twice-reopened row %q", pending, secondReopen)
	}
	singleFold, foldErr := readDelegateAttentionFold(singlePath, "child-"+singleID)
	if foldErr != nil {
		t.Fatalf("read the single-reopen delegate's fold: %v", foldErr)
	}
	if pending := singleFold.pendingIDs(); len(pending) != 1 || pending[0] != singleReopen {
		t.Fatalf("the single-reopen delegate's pending attention = %#v, want only the reopened row %q", pending, singleReopen)
	}
}

func appendColdAttentionRow(t *testing.T, path, sessionID, attentionID, content string) {
	t.Helper()
	if _, statErr := os.Stat(path); statErr != nil {
		writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
		if err != nil {
			t.Fatalf("create attention transcript: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close created attention transcript: %v", err)
		}
	}
	if _, err := appendColdDelegateNotificationDurablyWithOpen(path, sessionID, attentionID, content, time.Unix(100, 0).UTC(), transcript.OpenWriterForSession); err != nil {
		t.Fatalf("append cold attention row %q: %v", attentionID, err)
	}
}

func appendColdConsumedClaim(t *testing.T, path, sessionID, attentionID string, generation uint64) {
	t.Helper()
	appendColdAttentionResolutionMarker(t, path, sessionID, attentionID, delegateAttentionConsumed, generation)
}

// appendColdAttentionResolutionMarker seeds a legacy on-disk resolution marker.
// Production no longer writes them; the read side still honors them.
func appendColdAttentionResolutionMarker(t *testing.T, path, sessionID, attentionID string, disposition delegateAttentionResolution, generation uint64) {
	t.Helper()
	writer, _, err := transcript.OpenWriterForSession(path, sessionID)
	if err != nil {
		t.Fatalf("open cold attention transcript %q: %v", sessionID, err)
	}
	if err := writer.AppendDurable(delegateAttentionResolutionTurnForGeneration(attentionID, disposition, generation)); err != nil {
		_ = writer.Close()
		t.Fatalf("append cold attention resolution %q: %v", attentionID, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close cold attention transcript %q: %v", sessionID, err)
	}
}

func TestDelegateAttentionAppendFailureLeavesStopPending(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	path := filepath.Join(c.stateDir, schema.SessionsSubdir, "child-dlg_target.transcript.jsonl")
	writeDelegateAttentionTranscript(t, path, "child-dlg_target", "attention-failure")
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	evidence, err := collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
	if err != nil {
		t.Fatalf("collect evidence: %v", err)
	}
	plans, err := c.Reconcile(evidence)
	if err != nil || len(plans.attention) != 1 {
		t.Fatalf("Reconcile attention plans = %#v, err=%v", plans.attention, err)
	}
	// The discard appends its resolution to delegatestore; a failed journal
	// append must leave the stop pending rather than complete it.
	journalDown := errors.New("delegate journal unavailable")
	c.appendFault = func(events []delegatestore.Event) error {
		for _, event := range events {
			if event.AttentionResolved != nil && event.AttentionResolved.AttentionID == "attention-failure" {
				return journalDown
			}
		}
		return nil
	}
	t.Cleanup(func() { c.appendFault = nil })
	if err := c.executeDelegateAttentionCleanup(plans.attention[0]); !errors.Is(err, journalDown) {
		t.Fatalf("attention cleanup with a failing journal = %v, want %v", err, journalDown)
	}
	if c.stop == nil {
		t.Fatal("attention append failure completed stop")
	}
}

func TestDelegateAttentionCleanupRejectsReplacedRuntime(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	path := filepath.Join(c.stateDir, schema.SessionsSubdir, "child-dlg_target.transcript.jsonl")
	writeDelegateAttentionTranscript(t, path, "child-dlg_target", "attention-stale")
	originalWriter, _, err := transcript.OpenWriterForSession(path, "child-dlg_target")
	if err != nil {
		t.Fatalf("OpenWriterForSession original runtime: %v", err)
	}
	defer func() { _ = originalWriter.Close() }()
	original := &Session{id: "child-dlg_target", stateDir: c.stateDir, transcript: originalWriter, transcriptReady: true}
	c.live["dlg_target"] = &delegateLiveState{runtime: original}
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	evidence, err := collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
	if err != nil {
		t.Fatalf("collect evidence: %v", err)
	}
	plans, err := c.Reconcile(evidence)
	if err != nil || len(plans.attention) != 1 || plans.attention[0].runtime != original {
		t.Fatalf("Reconcile attention plans = %#v, err=%v", plans.attention, err)
	}
	c.mu.Lock()
	c.live["dlg_target"].runtime = &Session{}
	c.evidenceVersion++
	c.mu.Unlock()
	if err := c.executeDelegateAttentionCleanup(plans.attention[0]); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("cleanup after runtime replacement = %v, want stale lease", err)
	}
	if c.stop == nil {
		t.Fatal("stale runtime report completed stop")
	}
}

func TestDelegateAttentionCleanupRejectsNilRuntimeReplacementAndStaleEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*delegateTreeController)
	}{
		{
			name: "nil runtime replaced",
			mutate: func(c *delegateTreeController) {
				c.live["dlg_target"] = &delegateLiveState{runtime: &Session{}}
				c.evidenceVersion++
			},
		},
		{
			name: "evidence version changed",
			mutate: func(c *delegateTreeController) {
				c.evidenceVersion++
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := newDelegateControllerTestHarness(t, 1, 1)
			seedDelegateControllerIdle(t, c, "dlg_target", "")
			path := filepath.Join(c.stateDir, schema.SessionsSubdir, "child-dlg_target.transcript.jsonl")
			writeDelegateAttentionTranscript(t, path, "child-dlg_target", "attention-stale")
			if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
				t.Fatalf("StopSubtree: %v", err)
			}
			evidence, err := collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
			if err != nil {
				t.Fatalf("collect evidence: %v", err)
			}
			plans, err := c.Reconcile(evidence)
			if err != nil || len(plans.attention) != 1 || plans.attention[0].runtime != nil {
				t.Fatalf("Reconcile attention plans = %#v, err=%v", plans.attention, err)
			}
			c.mu.Lock()
			test.mutate(c)
			c.mu.Unlock()
			if err := c.executeDelegateAttentionCleanup(plans.attention[0]); !errors.Is(err, errDelegateStaleLease) {
				t.Fatalf("stale cleanup error = %v, want stale lease", err)
			}
			// A rejected cleanup resolves nothing: the journal never records the
			// stale report and the stop stays pending.
			c.mu.Lock()
			_, resolved := c.durable["dlg_target"].ResolvedAttention["attention-stale"]
			stopPending := c.stop != nil
			c.mu.Unlock()
			if resolved || !stopPending {
				t.Fatalf("stale report state resolved=%t stopPending=%t, want unresolved and pending", resolved, stopPending)
			}
		})
	}
}

func TestDelegateControllerLiveAttentionCleanupKeepsOneWriterSequence(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	sessionID := "child-dlg_target"
	path := schema.SessionTranscriptPath(c.stateDir, sessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	runtime := &Session{id: sessionID, stateDir: c.stateDir, transcript: writer, transcriptReady: true}
	attention := schema.NewTurn(schema.TurnSteering, llm.User("attention"))
	attention.AttentionID = "attention-live"
	if err := runtime.writeTranscriptDurable(attention); err != nil {
		t.Fatalf("write attention: %v", err)
	}
	c.live["dlg_target"] = &delegateLiveState{runtime: runtime}
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	evidence, err := collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
	if err != nil {
		t.Fatalf("collect evidence: %v", err)
	}
	plans, err := c.Reconcile(evidence)
	if err != nil || len(plans.attention) != 1 || plans.attention[0].runtime != runtime {
		t.Fatalf("attention plans = %#v err=%v", plans.attention, err)
	}
	if err := c.executeDelegateAttentionCleanup(plans.attention[0]); err != nil {
		t.Fatalf("execute live cleanup: %v", err)
	}
	c.mu.Lock()
	_, resolved := c.durable["dlg_target"].ResolvedAttention["attention-live"]
	c.mu.Unlock()
	if !resolved {
		t.Fatal("live cleanup did not resolve the row in the journal")
	}
	evidence, err = collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
	if err != nil {
		t.Fatalf("recollect after live cleanup: %v", err)
	}
	if _, err := c.Reconcile(evidence); err != nil {
		t.Fatalf("complete stop after live cleanup: %v", err)
	}
	if c.stop != nil {
		t.Fatalf("stop remained after live cleanup: %#v", c.stop)
	}
	ordinary := schema.NewTurn(schema.TurnSteering, llm.User("resident write after cleanup"))
	if err := runtime.writeTranscriptDurable(ordinary); err != nil {
		t.Fatalf("resident write after cleanup: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}
	reopened, entries, err := transcript.OpenWriterForSession(path, sessionID)
	if err != nil {
		t.Fatalf("reopen one-writer transcript: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close reopened writer: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entry count = %d, want attention and the resident write", len(entries))
	}
	for i, entry := range entries {
		if entry.Seq != i {
			t.Fatalf("entry %d sequence = %d, want %d", i, entry.Seq, i)
		}
	}
	if entries[1].Turn.Message.Text() != "resident write after cleanup" {
		t.Fatalf("one-writer entry order = %#v", entries)
	}
}

func TestDelegateControllerStopRescansAttentionCreatedByCancellation(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	path := filepath.Join(c.stateDir, schema.SessionsSubdir, "child-dlg_target.transcript.jsonl")
	writeDelegateAttentionTranscript(t, path, "child-dlg_target", "attention-before-cancel")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	c.live["dlg_target"].binding.cancel = func() {
		appendDelegateAttentionTurn(t, path, "child-dlg_target", "attention-from-cancel")
		if _, err := c.FinishGeneration(lease, delegateFinish{}); err != nil {
			t.Fatalf("FinishGeneration from cancellation: %v", err)
		}
	}
	result, cancelPlan, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.drainStopForClose(ctx, c.stopForResult(result)); err != nil {
		t.Fatalf("drain cancellation attention: %v", err)
	}
	// The drain resolves both rows in delegatestore -- the one the evidence
	// snapshot carried and the one cancellation created after it -- and the
	// transcript keeps both messages because a child resolution writes no marker.
	c.mu.Lock()
	aggregate := c.durable["dlg_target"]
	stopPending := c.stop != nil
	resolved := make([]bool, 0, 2)
	for _, attentionID := range []string{"attention-before-cancel", "attention-from-cancel"} {
		_, done := aggregate.ResolvedAttention[attentionID]
		resolved = append(resolved, done)
	}
	pendingRows := append([]delegatestore.PendingAttention(nil), aggregate.PendingAttention...)
	c.mu.Unlock()
	if !resolved[0] || !resolved[1] || len(pendingRows) != 0 || stopPending {
		t.Fatalf("attention after the drain resolved=%v pending=%#v stopPending=%t, want both resolved and the stop complete", resolved, pendingRows, stopPending)
	}
}

func TestDelegateControllerRestartThreeLevelTreeIsProviderFree(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 3, 1)
	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerRunning(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerRunning(t, c, "dlg_grandchild", "dlg_child")
	restarted := reopenDelegateController(t, c, path)
	for _, id := range []string{"dlg_parent", "dlg_child", "dlg_grandchild"} {
		if !restarted.durable[id].CurrentRunOpen {
			t.Fatalf("%s was reconciled before external evidence collection", id)
		}
	}
	evidence, err := collectDelegateReconcileEvidence(restarted.stateDir, restarted.ReconcileRequirements())
	if err != nil {
		t.Fatalf("collectDelegateReconcileEvidence: %v", err)
	}
	if _, err := restarted.Reconcile(evidence); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, id := range []string{"dlg_parent", "dlg_child", "dlg_grandchild"} {
		aggregate := restarted.durable[id]
		if aggregate.CurrentRunOpen || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeFailed || aggregate.LatestOutcome.Reason != "runtime_lost" {
			t.Fatalf("%s restart aggregate = %#v", id, aggregate)
		}
	}
	if len(restarted.live) != 0 {
		t.Fatalf("restart constructed live runtimes: %#v", restarted.live)
	}
}

func TestDelegateControllerRestartRepairsPreparedTerminalOnce(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	packet := delegateControllerReportedPacket("prepared")
	if _, _, err := c.prepareSettlementForTest(delegateLease{delegateID: "dlg_target", generation: 1}, &packet); err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	restarted := reopenDelegateController(t, c, path)
	if aggregate := restarted.durable["dlg_target"]; !aggregate.CurrentRunOpen || aggregate.Phase != delegatestore.PhaseSettling {
		t.Fatalf("prepared run changed before Reconcile: %#v", aggregate)
	}
	if _, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted)); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	firstCount := countDelegateRunFinished(t, restarted, "dlg_target")
	if firstCount != 1 || restarted.durable["dlg_target"].LatestOutcome == nil || restarted.durable["dlg_target"].LatestOutcome.Status != delegatestore.OutcomeCompleted {
		t.Fatalf("prepared repair count=%d aggregate=%#v", firstCount, restarted.durable["dlg_target"])
	}
	if _, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted)); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got := countDelegateRunFinished(t, restarted, "dlg_target"); got != firstCount {
		t.Fatalf("run-finished count after second reconcile = %d, want %d", got, firstCount)
	}
}

// A run that failed and prepared its terminal packet, then lost its process
// before settling, keeps its cause through the restart's repair (#3327): the
// packet's metadata carries the error, and the repaired outcome has it.
func TestDelegateControllerRestartRepairKeepsAFailedRunsError(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	finish := delegateFinishFromRun(delegateTerminalRunInputs{runErr: errors.New("provider returned 500\nretry-after: 30")})
	if _, _, err := c.prepareSettlementForTest(delegateLease{delegateID: "dlg_target", generation: 1}, finish.packet); err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	restarted := reopenDelegateController(t, c, path)
	if _, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	outcome := restarted.durable["dlg_target"].LatestOutcome
	if outcome == nil || outcome.Status != delegatestore.OutcomeFailed || outcome.Error != "provider returned 500" {
		t.Fatalf("repaired outcome = %#v, want failed with the run's error", outcome)
	}
}

func TestDelegateControllerRestartCompletesStopBeforeAdmission(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	requestSeq := appendDelegateControllerStopRequest(t, c, "dlg_target")
	restarted := reopenDelegateController(t, c, path)
	if restarted.stop == nil || restarted.stop.requestSeq != requestSeq {
		t.Fatalf("restored stop = %#v, want request %d", restarted.stop, requestSeq)
	}
	if _, err := restarted.ReserveStart(rootDelegateActor("root-session"), "dlg_target"); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("ReserveStart before reconcile = %v, want busy", err)
	}
	preRepairEvidence := emptyDelegateReconcileEvidence(restarted)
	if _, err := restarted.Reconcile(preRepairEvidence); err != nil {
		t.Fatalf("runtime-loss Reconcile: %v", err)
	}
	if restarted.stop == nil || restarted.durable["dlg_target"].CurrentRunOpen {
		t.Fatalf("runtime repair used pre-repair evidence to complete stop: stop=%#v aggregate=%#v", restarted.stop, restarted.durable["dlg_target"])
	}
	if _, err := restarted.Reconcile(preRepairEvidence); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("pre-repair evidence reuse error = %v, want busy", err)
	}
	if _, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted)); err != nil {
		t.Fatalf("fresh Reconcile: %v", err)
	}
	if restarted.stop != nil || restarted.durable["dlg_target"].PendingStopSeq != 0 {
		t.Fatalf("restart stop remained pending: stop=%#v aggregate=%#v", restarted.stop, restarted.durable["dlg_target"])
	}
	if _, err := restarted.ReserveStart(rootDelegateActor("root-session"), "dlg_target"); err != nil {
		t.Fatalf("ReserveStart after completion: %v", err)
	}
}

func TestDelegateControllerReconcileRequiresExactEvidenceSnapshot(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	complete := emptyDelegateReconcileEvidence(c)
	tests := map[string]func(delegateReconcileEvidence) delegateReconcileEvidence{
		"missing shell": func(e delegateReconcileEvidence) delegateReconcileEvidence {
			delete(e.shells, "dlg_target")
			return e
		},
		"missing attention": func(e delegateReconcileEvidence) delegateReconcileEvidence {
			delete(e.attention, "dlg_target")
			return e
		},
		"extra shell": func(e delegateReconcileEvidence) delegateReconcileEvidence {
			e.shells["dlg_extra"] = shellRuntimeLossEvidence{}
			return e
		},
		"extra attention": func(e delegateReconcileEvidence) delegateReconcileEvidence {
			e.attention["dlg_extra"] = nil
			return e
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			evidence := delegateReconcileEvidence{
				evidenceVersion: complete.evidenceVersion,
				shells:          map[string]shellRuntimeLossEvidence{"dlg_target": {}},
				attention:       map[string][]string{"dlg_target": nil},
			}
			if _, err := c.Reconcile(mutate(evidence)); !errors.Is(err, errDelegateTargetBusy) {
				t.Fatalf("Reconcile error = %v, want exact-snapshot rejection", err)
			}
		})
	}
}

func TestDelegateControllerRestartAfterStoppedFinishDoesNotFinishTwice(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if _, err := c.FinishGeneration(delegateLease{delegateID: "dlg_target", generation: 1}, delegateFinish{}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	before := countDelegateRunFinished(t, c, "dlg_target")
	restarted := reopenDelegateController(t, c, path)
	if _, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := countDelegateRunFinished(t, restarted, "dlg_target"); got != before {
		t.Fatalf("restart finished stopped generation twice: before=%d after=%d", before, got)
	}
}

func TestDelegateControllerRestartPreservesStopAdmissionClassificationAfterRunClosure(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	first, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if _, err := c.FinishGeneration(delegateLease{delegateID: "dlg_target", generation: 1}, delegateFinish{}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	restarted := reopenDelegateController(t, c, path)
	second, _, _, err := restarted.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("retry restored StopSubtree: %v", err)
	}
	if second.requestSeq != first.requestSeq || second.previousLifecycle != delegateLifecycleRunning || second.outcome != "cancelled_by_request" {
		t.Fatalf("restored stop classification = %#v, want request %d running/cancelled_by_request", second, first.requestSeq)
	}
}

func TestDelegateControllerRestartPreservesSubtreeStopAdmissionClassification(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerRunning(t, c, "dlg_child", "dlg_parent")

	first, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_parent")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if first.previousLifecycle != delegateLifecycleIdle || first.outcome != "cancelled_by_request" {
		t.Fatalf("stop classification = %#v, want idle/cancelled_by_request", first)
	}

	restarted := reopenDelegateController(t, c, path)
	second, _, _, err := restarted.StopSubtree(rootDelegateActor("root-session"), "dlg_parent")
	if err != nil {
		t.Fatalf("retry restored StopSubtree: %v", err)
	}
	if second.requestSeq != first.requestSeq || second.previousLifecycle != delegateLifecycleIdle || second.outcome != "cancelled_by_request" {
		t.Fatalf("restored stop classification = %#v, want request %d idle/cancelled_by_request", second, first.requestSeq)
	}
}

func TestDelegateControllerRestartCleansAttentionWithoutRuntime(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	requestSeq := appendDelegateControllerStopRequest(t, c, "dlg_target")
	transcriptPath := filepath.Join(c.stateDir, schema.SessionsSubdir, "child-dlg_target.transcript.jsonl")
	writeDelegateAttentionTranscriptState(t, transcriptPath, "child-dlg_target", "attention-1", false)
	restarted := reopenDelegateController(t, c, path)
	requirements := restarted.ReconcileRequirements()
	evidence, err := collectDelegateReconcileEvidence(restarted.stateDir, requirements)
	if err != nil {
		t.Fatalf("collectDelegateReconcileEvidence: %v", err)
	}
	if got := evidence.attention["dlg_target"]; len(got) != 1 || got[0] != "attention-1" {
		t.Fatalf("pending attention = %#v", got)
	}
	plans, err := restarted.Reconcile(evidence)
	if err != nil || len(plans.attention) != 1 || plans.attention[0].runtime != nil {
		t.Fatalf("cold attention plans = %#v err=%v", plans.attention, err)
	}
	writeDelegateAttentionTranscriptState(t, transcriptPath, "child-dlg_target", "attention-1", true)
	if _, err := restarted.ReportAttentionResolved(requestSeq, evidence.evidenceVersion, "dlg_target", "attention-1", delegateAttentionDiscarded, nil); err != nil {
		t.Fatalf("ReportAttentionResolved: %v", err)
	}
	evidence, err = collectDelegateReconcileEvidence(restarted.stateDir, restarted.ReconcileRequirements())
	if err != nil {
		t.Fatalf("recollect evidence: %v", err)
	}
	if _, err := restarted.Reconcile(evidence); err != nil {
		t.Fatalf("final Reconcile: %v", err)
	}
	if restarted.stop != nil {
		t.Fatalf("stop remained after attention cleanup: %#v", restarted.stop)
	}
}

func TestDelegateControllerRestartRepairsDescendantShellBeforeStopCompletion(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	appendDelegateControllerStopRequest(t, c, "dlg_parent")
	shellPath := filepath.Join(jobsDir(c.stateDir, "child-dlg_child"), "jobs.jsonl")
	seedDelegateShellStoreAt(t, shellPath)
	restarted := reopenDelegateController(t, c, path)
	evidence, err := collectDelegateReconcileEvidence(restarted.stateDir, restarted.ReconcileRequirements())
	if err != nil {
		t.Fatalf("collectDelegateReconcileEvidence: %v", err)
	}
	plans, err := restarted.Reconcile(evidence)
	if err != nil || len(plans.shellRepairs) != 1 {
		t.Fatalf("shell repair plans = %#v err=%v", plans.shellRepairs, err)
	}
	if restarted.stop == nil {
		t.Fatal("stop completed before shell repair")
	}
	if err := executeDelegateShellRepair(plans.shellRepairs[0], time.Unix(20, 0).UTC()); err != nil {
		t.Fatalf("executeDelegateShellRepair: %v", err)
	}
	evidence, err = collectDelegateReconcileEvidence(restarted.stateDir, restarted.ReconcileRequirements())
	if err != nil {
		t.Fatalf("recollect: %v", err)
	}
	if _, err := restarted.Reconcile(evidence); err != nil {
		t.Fatalf("final Reconcile: %v", err)
	}
	if restarted.stop != nil {
		t.Fatalf("stop remained after shell repair: %#v", restarted.stop)
	}
}

func TestDelegateControllerReconcileRejectsStaleExternalEvidence(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	evidence := emptyDelegateReconcileEvidence(c)
	plan := c.ReplayDeliveries()[0]
	token, admitted, err := c.BeginDelivery(plan)
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	if _, err := c.Reconcile(evidence); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("stale Reconcile error = %v, want busy", err)
	}
	if _, err := c.CompleteDelivery(token, false); err != nil {
		t.Fatalf("CompleteDelivery: %v", err)
	}
}

func TestDelegateControllerRestartPreservesOrderedDeliveries(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	restarted := reopenDelegateController(t, c, path)
	plans := restarted.ReplayDeliveries()
	if len(plans) != 1 || plans[0].deliveryID != "dlg_target/delivery/1" {
		t.Fatalf("restart delivery head = %#v", plans)
	}
	token, admitted, err := restarted.BeginDelivery(plans[0])
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	next, err := restarted.CompleteDelivery(token, true)
	if err != nil || len(next.deliveries) != 1 || next.deliveries[0].deliveryID != "dlg_target/delivery/2" {
		t.Fatalf("next ordered delivery = %#v err=%v", next.deliveries, err)
	}
}

func TestDelegateControllerRestartDefersExternalStopDeliveryUntilCompletion(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	appendDelegateControllerStopRequest(t, c, "dlg_target")
	restarted := reopenDelegateController(t, c, path)
	if plans := restarted.ReplayDeliveries(); len(plans) != 0 {
		t.Fatalf("delivery replayed before stop completion: %#v", plans)
	}
	plans, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted))
	if err != nil || len(plans.deliveries) != 1 {
		t.Fatalf("completion deliveries = %#v err=%v", plans.deliveries, err)
	}
}

func TestDelegateControllerRestartIdleStopFencesQueuedCoveredDelivery(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child")
	appendDelegateControllerStopRequest(t, c, "dlg_parent")
	restarted := reopenDelegateController(t, c, path)
	plans, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(plans.deliveries) != 0 || len(restarted.durable["dlg_child"].PendingDeliveries) != 0 {
		t.Fatalf("covered delivery survived restart stop: plans=%#v pending=%#v", plans.deliveries, restarted.durable["dlg_child"].PendingDeliveries)
	}
}

func TestDelegateControllerRestartCannotCollideStopOrDeliveryIdentity(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	firstSeq := appendDelegateControllerStopRequest(t, c, "dlg_target")
	restarted := reopenDelegateController(t, c, path)
	plans, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted))
	if err != nil || len(plans.deliveries) != 1 {
		t.Fatalf("Reconcile: plans=%#v err=%v", plans.deliveries, err)
	}
	token, admitted, err := restarted.BeginDelivery(plans.deliveries[0])
	if err != nil || !admitted || token.deliveryID == "" || token.processID == 0 {
		t.Fatalf("delivery token = %#v admitted=%t err=%v", token, admitted, err)
	}
	if _, err := restarted.CompleteDelivery(token, false); err != nil {
		t.Fatalf("CompleteDelivery: %v", err)
	}
	second, _, _, err := restarted.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("second StopSubtree: %v", err)
	}
	if second.requestSeq <= firstSeq {
		t.Fatalf("new stop seq = %d, want > %d", second.requestSeq, firstSeq)
	}
}

func reopenDelegateController(t *testing.T, c *delegateTreeController, path string) *delegateTreeController {
	t.Helper()
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	store, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	restarted, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: "root-session",
		stateDir:      c.stateDir,
		worktreeRoot:  c.worktreeRoot,
		turnLimit:     c.turnLimit,
		driveLimit:    c.driveLimit,
		now:           c.now,
	})
	if err != nil {
		t.Fatalf("openDelegateTreeController: %v", err)
	}
	return restarted
}

func countDelegateRunFinished(t *testing.T, c *delegateTreeController, delegateID string) int {
	t.Helper()
	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	count := 0
	for _, event := range events {
		if event.DelegateID == delegateID && event.Kind == delegatestore.EventDelegateRunFinished {
			count++
		}
	}
	return count
}

func writeDelegateAttentionTranscriptState(t *testing.T, path, sessionID, attentionID string, resolved bool) {
	t.Helper()
	writeDelegateAttentionTranscript(t, path, sessionID, attentionID)
	if resolved {
		appendColdAttentionResolutionMarker(t, path, sessionID, attentionID, delegateAttentionDiscarded, 0)
	}
}

func writeDelegateAttentionTranscript(t *testing.T, path, sessionID, attentionID string) {
	t.Helper()
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	turn := schema.NewTurn(schema.TurnSteering, llm.User("attention"))
	turn.AttentionID = attentionID
	if err := writer.AppendDurable(turn); err != nil {
		_ = writer.Close()
		t.Fatalf("AppendDurable attention: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close attention writer: %v", err)
	}
}

func appendDelegateAttentionTurn(t *testing.T, path, sessionID, attentionID string) {
	t.Helper()
	writer, _, err := transcript.OpenWriterForSession(path, sessionID)
	if err != nil {
		t.Fatalf("OpenWriterForSession: %v", err)
	}
	turn := schema.NewTurn(schema.TurnSteering, llm.User("attention"))
	turn.AttentionID = attentionID
	if err := writer.AppendDurable(turn); err != nil {
		_ = writer.Close()
		t.Fatalf("AppendDurable attention: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close attention writer: %v", err)
	}
}

func seedDelegateShellStoreAt(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	store, err := jobstore.OpenNoSync(path)
	if err != nil {
		t.Fatalf("OpenNoSync: %v", err)
	}
	now := time.Unix(10, 0).UTC()
	if err := store.Append(jobstore.Event{
		Kind:           jobstore.EventJobStarted,
		TS:             now,
		JobID:          "job-shell",
		Type:           jobstore.JobShell,
		OwnerSessionID: "child-dlg_child",
		StartedAt:      &now,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
