package hub

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// Each case stops at a real journal/admission boundary that a disconnected
// bridge can leave behind. Reconnect must recover the original mutation, not
// interpret the utterance again or mint another input identity.
func TestE2E_VoiceReconnectRecoversDurableIntent(t *testing.T) {
	for _, state := range []string{"prepared", "attempted_without_delivery", "accepted_without_receipt"} {
		t.Run(state, func(t *testing.T) {
			f := newVoiceCorrectionStack(t)
			_ = f.device.conn.CloseNow()
			turn, err := f.journal.CreateTurn(f.ctx, f.armed.BindingID, f.armed.Epoch, 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.journal.CommitTurnInput(f.ctx, turn.ID, "completed-provider-input"); err != nil {
				t.Fatal(err)
			}
			original, err := f.journal.PrepareIntent(f.ctx, turn.ID, appwire.MethodTurnStart, "RECOVERED-INPUT-67", newMutationID(t))
			if err != nil {
				t.Fatal(err)
			}
			if state != "prepared" {
				if _, err := f.journal.RecordAttempt(f.ctx, turn.ID, f.instance, false); err != nil {
					t.Fatal(err)
				}
			}
			var admitted appwire.MutationReceipt
			if state == "accepted_without_receipt" {
				var result appwire.TurnStartResponse
				if err := f.client.Request(f.ctx, original.Method, original.Params, &result); err != nil {
					t.Fatal(err)
				}
				admitted = result.Receipt
				if admitted.Disposition != appwire.MutationDispositionApplied || admitted.InstanceID != f.instance {
					t.Fatal("fixture did not cross the real manager admission boundary")
				}
			}
			ready := f.connect(t)
			voiceJourneyAcknowledgeReady(t, f.device, ready, 2)
			recoveryCtx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
			defer cancel()
			device := f.device
			device.ctx = recoveryCtx
			recovered := voiceJourneyEvent(t, device, "intent")
			if recovered["turnId"] != turn.ID || recovered["text"] != original.Text || recovered["custody"] != hubcore.VoiceIntentAccepted {
				t.Fatal("reconnect did not report custody of the original durable text")
			}
			stored, err := f.journal.Intent(f.ctx, turn.ID)
			if err != nil || stored.State != hubcore.VoiceIntentAccepted || stored.Receipt == nil {
				t.Fatalf("recovery did not durably settle the intent: %+v %v", stored, err)
			}
			if string(stored.Params) != string(original.Params) || stored.Method != original.Method || stored.MutationID != original.MutationID || stored.ExpectedInstanceID != original.ExpectedInstanceID {
				t.Fatal("recovery changed immutable mutation identity or payload")
			}
			if state == "accepted_without_receipt" && (stored.Receipt.Disposition != appwire.MutationDispositionReplayed || stored.Receipt.TurnID != admitted.TurnID) {
				t.Fatal("lost acknowledgement was not reconciled through the original durable receipt")
			}
			request := f.next(t)
			if !request.Contains(original.Text) {
				t.Fatal("recovered intent did not reach the actual manager request")
			}
			read := f.read(t)
			count := 0
			for _, turn := range read.Thread.Turns {
				for _, item := range turn.Items {
					if item.ClientMutationID == original.MutationID && item.Type == "userMessage" {
						count++
					}
				}
			}
			if count != 1 {
				t.Fatalf("recovery produced %d canonical user inputs for one intent", count)
			}
			select {
			case <-request.Cancelled():
				t.Fatal("recovery cancelled the original manager work")
			default:
			}
		})
	}
}
