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
	path := filepath.Join(c.stateDir, sessionsSubdir, "child-dlg_target.transcript.jsonl")
	writeDelegateAttentionTranscript(t, path, "child-dlg_target", "attention-close")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Close(ctx); err != nil {
		t.Fatalf("Close with synchronously repairable attention: %v", err)
	}
	pending, err := readPendingDelegateAttention(path, "child-dlg_target")
	if err != nil {
		t.Fatalf("read attention after close: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("attention after close = %#v, want durable resolution", pending)
	}
}

func TestDelegateColdAttentionResolutionIsDurableAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attention.transcript.jsonl")
	writeDelegateAttentionTranscript(t, path, "child-attention", "attention-idempotent")
	for i := range 2 {
		if err := appendColdAttentionResolution(path, "child-attention", []string{"attention-idempotent"}, delegateAttentionDiscarded); err != nil {
			t.Fatalf("append resolution %d: %v", i, err)
		}
	}
	writer, entries, err := transcript.OpenWriterForSession(path, "child-attention")
	if err != nil {
		t.Fatalf("OpenWriterForSession: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}
	resolutions := 0
	for _, entry := range entries {
		if entry.Turn.AttentionResolution != nil {
			resolutions++
		}
	}
	if resolutions != 1 {
		t.Fatalf("durable resolution count = %d, want 1", resolutions)
	}
}

func TestDelegateAttentionAppendFailureLeavesStopPending(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	path := filepath.Join(c.stateDir, sessionsSubdir, "child-dlg_target.transcript.jsonl")
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
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove transcript fixture: %v", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("replace transcript fixture: %v", err)
	}
	if err := c.executeDelegateAttentionCleanup(plans.attention[0]); err == nil {
		t.Fatal("attention cleanup succeeded against a directory")
	}
	if c.stop == nil {
		t.Fatal("attention append failure completed stop")
	}
}

func TestDelegateAttentionCleanupRejectsReplacedRuntime(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	path := filepath.Join(c.stateDir, sessionsSubdir, "child-dlg_target.transcript.jsonl")
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
			path := filepath.Join(c.stateDir, sessionsSubdir, "child-dlg_target.transcript.jsonl")
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
			pending, err := readPendingDelegateAttention(path, "child-dlg_target")
			if err != nil {
				t.Fatalf("read resolved attention: %v", err)
			}
			if len(pending) != 0 || c.stop == nil {
				t.Fatalf("stale report state pending=%#v stop=%#v", pending, c.stop)
			}
		})
	}
}

func TestDelegateControllerLiveAttentionCleanupKeepsOneWriterSequence(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	sessionID := "child-dlg_target"
	path := filepath.Join(c.stateDir, sessionsSubdir, sessionID+".transcript.jsonl")
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
	if err := runtime.resolveAttentionDurably([]string{"attention-live"}, delegateAttentionDiscarded); err != nil {
		t.Fatalf("repeat live cleanup: %v", err)
	}
	if err := runtime.resolveAttentionDurably([]string{"attention-live"}, delegateAttentionConsumed); err == nil {
		t.Fatal("conflicting live cleanup disposition succeeded")
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
	if len(entries) != 3 {
		t.Fatalf("entry count = %d, want attention, resolution, resident write", len(entries))
	}
	for i, entry := range entries {
		if entry.Seq != i {
			t.Fatalf("entry %d sequence = %d, want %d", i, entry.Seq, i)
		}
	}
	if entries[1].Turn.AttentionResolution == nil || entries[2].Turn.Message.Text() != "resident write after cleanup" {
		t.Fatalf("one-writer entry order = %#v", entries)
	}
}

func TestDelegateControllerLiveAttentionCleanupRequiresUsableAttachedWriter(t *testing.T) {
	for _, test := range []struct {
		name   string
		attach func(*testing.T, *Session, *transcript.Writer)
	}{
		{
			name: "closed writer",
			attach: func(t *testing.T, runtime *Session, writer *transcript.Writer) {
				runtime.transcript = writer
				runtime.transcriptReady = true
				if err := writer.Close(); err != nil {
					t.Fatalf("Close writer: %v", err)
				}
			},
		},
		{
			name: "unattached writer",
			attach: func(t *testing.T, _ *Session, writer *transcript.Writer) {
				if err := writer.Close(); err != nil {
					t.Fatalf("Close writer: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := newDelegateControllerTestHarness(t, 1, 1)
			seedDelegateControllerIdle(t, c, "dlg_target", "")
			sessionID := "child-dlg_target"
			path := filepath.Join(c.stateDir, sessionsSubdir, sessionID+".transcript.jsonl")
			writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
			if err != nil {
				t.Fatalf("NewWriter: %v", err)
			}
			attention := schema.NewTurn(schema.TurnSteering, llm.User("attention"))
			attention.AttentionID = "attention-live-unusable"
			if err := writer.AppendDurable(attention); err != nil {
				_ = writer.Close()
				t.Fatalf("AppendDurable attention: %v", err)
			}
			runtime := &Session{id: sessionID, stateDir: c.stateDir}
			test.attach(t, runtime, writer)
			c.live["dlg_target"] = &delegateLiveState{runtime: runtime}
			if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
				t.Fatalf("StopSubtree: %v", err)
			}
			evidence, err := collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
			if err != nil {
				t.Fatalf("collect evidence: %v", err)
			}
			plans, err := c.Reconcile(evidence)
			if err != nil || len(plans.attention) != 1 {
				t.Fatalf("attention plans = %#v err=%v", plans.attention, err)
			}
			if err := c.executeDelegateAttentionCleanup(plans.attention[0]); err == nil {
				t.Fatal("live cleanup succeeded without a usable attached writer")
			}
			pending, err := readPendingDelegateAttention(path, sessionID)
			if err != nil {
				t.Fatalf("read pending attention: %v", err)
			}
			if len(pending) != 1 || c.stop == nil {
				t.Fatalf("failed live cleanup pending=%#v stop=%#v", pending, c.stop)
			}
		})
	}
}

func TestDelegateControllerStopRescansAttentionCreatedByCancellation(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	path := filepath.Join(c.stateDir, sessionsSubdir, "child-dlg_target.transcript.jsonl")
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
	pending, err := readPendingDelegateAttention(path, "child-dlg_target")
	if err != nil {
		t.Fatalf("read final attention: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending attention after repeated fold = %#v", pending)
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
	transcriptPath := filepath.Join(c.stateDir, sessionsSubdir, "child-dlg_target.transcript.jsonl")
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
		if err := appendColdAttentionResolution(path, sessionID, []string{attentionID}, delegateAttentionDiscarded); err != nil {
			t.Fatalf("append cold attention resolution: %v", err)
		}
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
