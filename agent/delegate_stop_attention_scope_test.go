package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// A root's stop must not repeatedly parse every unrelated root's transcript.
// The general reconciliation path still validates the full attention set.
func TestDelegateStopDrainScopesAttentionToMembers(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_target")
	seedDelegateControllerIdle(t, c, "dlg_unrelated", "")
	targetPath := filepath.Join(c.stateDir, sessionsSubdir, "child-dlg_target.transcript.jsonl")
	childPath := filepath.Join(c.stateDir, sessionsSubdir, "child-dlg_child.transcript.jsonl")
	unrelatedPath := filepath.Join(c.stateDir, sessionsSubdir, "child-dlg_unrelated.transcript.jsonl")
	writeDelegateAttentionTranscriptState(t, targetPath, "child-dlg_target", "target-attention", false)
	writeDelegateAttentionTranscriptState(t, childPath, "child-dlg_child", "child-attention", false)
	// A complete invalid record makes an unwanted read observable, without
	// timing assertions or replacing transcript/reconciliation internals.
	corrupt := []byte("not-json\n")
	if err := os.WriteFile(unrelatedPath, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements()); err == nil {
		t.Fatal("general reconciliation omitted unrelated corrupt attention")
	}
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatal(err)
	}
	stop := c.stopForResult(result)
	if err := c.drainStopForClose(context.Background(), stop); err != nil {
		t.Fatalf("stop read attention outside its subtree: %v", err)
	}
	for _, input := range []struct{ path, id string }{{targetPath, "child-dlg_target"}, {childPath, "child-dlg_child"}} {
		pending, err := readPendingDelegateAttention(input.path, input.id)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) != 0 {
			t.Fatal("stop left member attention unresolved")
		}
	}
	if c.stop != nil {
		t.Fatal("stop did not complete")
	}
	if a := c.durable["dlg_unrelated"]; a.Phase != delegatestore.PhaseIdle || !a.Resumable {
		t.Fatal("stop changed unrelated delegate status")
	}
	got, err := os.ReadFile(unrelatedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, corrupt) {
		t.Fatal("stop modified unrelated transcript")
	}
	if _, err := collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements()); err == nil {
		t.Fatal("general reconciliation no longer checks unrelated attention")
	}
}

func TestDelegateStopDrainStillCollectsAllShellEvidence(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerIdle(t, c, "dlg_unrelated", "")
	storePath := filepath.Join(jobsDir(c.stateDir, "child-dlg_unrelated"), "jobs.jsonl")
	if err := os.MkdirAll(filepath.Dir(storePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storePath, []byte("not-json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.drainStopForClose(context.Background(), c.stopForResult(result)); err == nil {
		t.Fatal("stop omitted unrelated shell evidence")
	}
}

func TestDelegateStopReconcileRejectsScopedEvidence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*delegateTreeController, *delegateReconcileEvidence)
	}{
		{"request sequence mismatch", func(_ *delegateTreeController, evidence *delegateReconcileEvidence) {
			evidence.stopRequestSeq++
		}},
		{"same count different member", func(_ *delegateTreeController, evidence *delegateReconcileEvidence) {
			delete(evidence.attention, "dlg_child")
			evidence.attention["dlg_unrelated"] = nil
		}},
		{"missing member", func(_ *delegateTreeController, evidence *delegateReconcileEvidence) {
			delete(evidence.attention, "dlg_child")
		}},
		{"stale evidence version", func(c *delegateTreeController, _ *delegateReconcileEvidence) {
			c.mu.Lock()
			c.evidenceVersion++
			c.mu.Unlock()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, storePath := newDelegateControllerTestHarness(t, 2, 1)
			seedDelegateControllerIdle(t, c, "dlg_target", "")
			seedDelegateControllerIdle(t, c, "dlg_child", "dlg_target")
			seedDelegateControllerIdle(t, c, "dlg_unrelated", "")
			childPath := filepath.Join(c.stateDir, sessionsSubdir, "child-dlg_child.transcript.jsonl")
			writeDelegateAttentionTranscriptState(t, childPath, "child-dlg_child", "child-attention", false)
			result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
			if err != nil {
				t.Fatal(err)
			}
			stop := c.stopForResult(result)
			requirements, current := c.stopReconcileRequirements(stop)
			if !current || requirements.stopRequestSeq == 0 || requirements.stopRequestSeq != stop.requestSeq {
				t.Fatal("requirements are not bound to the current stop")
			}
			evidence, err := collectDelegateReconcileEvidence(c.stateDir, requirements)
			if err != nil {
				t.Fatal(err)
			}
			if len(evidence.shells) != 3 || len(evidence.attention) != 2 {
				t.Fatal("scoped evidence omitted shells or included unrelated attention")
			}
			if _, included := evidence.attention["dlg_unrelated"]; included {
				t.Fatal("scoped evidence included unrelated attention")
			}
			test.change(c, &evidence)
			before := cloneDelegateControllerState(t, c.durable)
			// The JSON snapshot helper omits empty PendingDeliveries. Preserve
			// its in-memory nil/empty distinction for exact mutation detection.
			for id, aggregate := range c.durable {
				if aggregate.PendingDeliveries != nil && before[id].PendingDeliveries == nil {
					before[id].PendingDeliveries = []delegatestore.PendingDelivery{}
				}
			}
			if !reflect.DeepEqual(c.durable, before) {
				t.Fatal("snapshot differs before Reconcile")
			}
			beforeBytes := readDelegateControllerFile(t, storePath)
			beforeVersion := c.evidenceVersion
			plans, err := c.Reconcile(evidence)
			if !errors.Is(err, errDelegateTargetBusy) || !reflect.DeepEqual(plans, delegateMutationPlans{}) {
				t.Fatalf("invalid scoped evidence: error=%v plans=%#v", err, plans)
			}
			if c.stop != stop || delegateStopDone(stop) || c.evidenceVersion != beforeVersion || !reflect.DeepEqual(c.durable, before) || !bytes.Equal(readDelegateControllerFile(t, storePath), beforeBytes) {
				t.Fatal("rejected scoped evidence changed controller or durable state")
			}
			// Fresh, complete evidence must reach the scoped attention branch,
			// proving rejection above is not an invalid fixture or blanket refusal.
			requirements, current = c.stopReconcileRequirements(stop)
			if !current {
				t.Fatal("rejection displaced the active stop")
			}
			evidence, err = collectDelegateReconcileEvidence(c.stateDir, requirements)
			if err != nil {
				t.Fatal(err)
			}
			plans, err = c.Reconcile(evidence)
			if err != nil || len(plans.attention) != 1 || plans.attention[0].delegateID != "dlg_child" || plans.attention[0].attentionID != "child-attention" {
				t.Fatalf("fresh scoped evidence: error=%v attention=%#v", err, plans.attention)
			}
		})
	}
}
