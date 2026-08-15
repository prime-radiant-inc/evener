//go:build serffuzz

package agent

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func FuzzDelegateReclaimStopRestart(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3})
	f.Add([]byte{1, 0, 1, 0})
	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 16 {
			program = program[:16]
		}
		controller, path := newDelegateControllerTestHarness(t, 4, 2)
		controller.maxRetainedTerminal = 2
		seedDelegateReclaimRuntime(t, controller, "dlg_closed", "", time.Unix(10, 0).UTC(), true, true)
		seedDelegateReclaimRuntime(t, controller, "dlg_idle", "", time.Unix(20, 0).UTC(), true, false)

		claim, err := controller.ClaimRuntimeReclamation(1)
		if err != nil {
			t.Fatalf("ClaimRuntimeReclamation: %v", err)
		}
		complete := len(program) == 0 || program[0]&1 == 0
		if complete {
			closed := make(map[string]*Session, len(claim.entries))
			for _, entry := range claim.entries {
				closed[entry.delegateID] = entry.runtime
			}
			if err := controller.CompleteRuntimeReclamation(claim, closed); err != nil {
				t.Fatalf("CompleteRuntimeReclamation: %v", err)
			}
		} else if err := controller.AbortRuntimeReclamation(claim); err != nil {
			t.Fatalf("AbortRuntimeReclamation: %v", err)
		}

		if _, _, _, err := controller.StopSubtree(rootDelegateActor("root-session"), "dlg_idle"); err != nil {
			t.Fatalf("StopSubtree: %v", err)
		}
		if _, err := controller.Reconcile(emptyDelegateReconcileEvidence(controller)); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		before, err := json.Marshal(controller.durable)
		if err != nil {
			t.Fatalf("marshal pre-restart state: %v", err)
		}
		restarted := reopenDelegateController(t, controller, path)
		after, err := json.Marshal(restarted.durable)
		if err != nil {
			t.Fatalf("marshal restarted state: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("restart changed durable state:\n before=%s\n after=%s", before, after)
		}
		if len(restarted.live) != 0 || len(restarted.reclamations) != 0 || len(restarted.reclaiming) != 0 {
			t.Fatalf("restart retained process-only reclamation state: live=%#v claims=%#v reclaiming=%#v", restarted.live, restarted.reclamations, restarted.reclaiming)
		}
		if len(restarted.durable) != 2 {
			t.Fatalf("reclamation deleted stable history: %#v", restarted.durable)
		}
	})
}
