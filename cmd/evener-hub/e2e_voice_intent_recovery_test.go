package hub

import (
	"context"
	"errors"
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

// The persisted boundary is the interleaving finishInput leaves when its idle
// read wins, but a typed start reaches the daemon before the voice mutation.
// Both requests below use the real daemon's ordinary admission and journal.
func TestE2E_VoiceJournaledRefusalDoesNotBlockRecovery(t *testing.T) {
	for _, lostRefusal := range []bool{false, true} {
		name := "live_refusal"
		if lostRefusal {
			name = "lost_refusal"
		}
		t.Run(name, func(t *testing.T) {
			f := newVoiceCorrectionStack(t)
			_ = f.device.conn.CloseNow()
			idle := f.read(t)
			if idle.Thread.Evener.ActiveTurnID != "" {
				t.Fatal("voice method selection did not observe an idle manager")
			}
			original := voiceRecoveryPrepared(t, f, 1, "REFUSED-VOICE-101")
			f.admit(t, "TYPED-WINNER-103", false)
			typed := f.next(t)
			if !typed.Contains("TYPED-WINNER-103") || typed.Contains(original.Text) {
				t.Fatal("typed input did not win the real admission race")
			}
			binding, err := f.journal.Binding(f.ctx, f.armed.BindingID)
			if err != nil {
				t.Fatal(err)
			}
			bridge := voiceBridge{client: f.client, journal: f.journal, binding: binding}
			if lostRefusal {
				if _, err := f.journal.RecordAttempt(f.ctx, original.TurnID, f.instance, false); err != nil {
					t.Fatal(err)
				}
				voiceRecoveryRequireRefusal(t, f, original)
			} else {
				settled, err := bridge.submitIntent(f.ctx, original, false)
				if err != nil || settled.State != "rejected" {
					t.Errorf("definitive daemon refusal remained unresolved: state=%s err=%v", settled.State, err)
				}
			}
			typed.RespondToolCall("communicate", communicateArgs("TYPED-FINISHED-107"))
			f.waitRead(t, "typed winner finished", func(read appwire.ThreadReadResponse) bool {
				return read.Thread.Evener.ActiveTurnID == ""
			})
			// The original identity MUST remain rejected even after idle. Recovery
			// may not change its method or silently mint another identity for it.
			voiceRecoveryRequireRefusal(t, f, original)
			t.Log("real busy rejection remains journaled after the original manager becomes idle")
			other := voiceRecoveryPrepared(t, f, 2, "OTHER-VOICE-109")
			ready := f.connect(t)
			voiceJourneyAcknowledgeReady(t, f.device, ready, 2)
			ctx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
			defer cancel()
			device := f.device
			device.ctx = ctx
			for {
				event := voiceJourneyEvent(t, device, "intent")
				if event["turnId"] == original.TurnID {
					if event["custody"] != "rejected" || event["text"] != original.Text {
						t.Fatal("recovery lost the refused utterance or misstated custody")
					}
					continue
				}
				if event["turnId"] != other.TurnID || event["custody"] != hubcore.VoiceIntentAccepted {
					t.Fatalf("unrelated prepared intent did not recover: %v", event)
				}
				break
			}
			stored, err := f.journal.Intent(f.ctx, original.TurnID)
			if err != nil || stored.State != "rejected" || stored.Text != original.Text || stored.Ref != original.Ref || stored.ExpectedInstanceID != original.ExpectedInstanceID || stored.Method != original.Method || stored.MutationID != original.MutationID || string(stored.Params) != string(original.Params) || stored.Receipt != nil {
				t.Fatalf("refusal lost immutable text/target or acquired acceptance: %+v %v", stored, err)
			}
			request := f.next(t)
			if !request.Contains(other.Text) || request.Contains(original.Text) {
				t.Fatal("recovery replayed refused input or lost healthy input")
			}
			request.RespondToolCall("communicate", communicateArgs("RECOVERY-WORKS-113"))
			f.waitRead(t, "recovered manager idle", func(read appwire.ThreadReadResponse) bool {
				return read.Thread.Evener.ActiveTurnID == ""
			})
			// Explicit resubmission is ordinary new input, with a new identity.
			resubmitted := f.admit(t, original.Text, false)
			if resubmitted == original.MutationID || !f.next(t).Contains(original.Text) {
				t.Fatal("retained voice text could not be explicitly resubmitted")
			}
		})
	}
}

func voiceRecoveryPrepared(t *testing.T, f *voiceCorrectionStack, sequence int64, text string) hubcore.VoiceIntent {
	t.Helper()
	turn, err := f.journal.CreateTurn(f.ctx, f.armed.BindingID, f.armed.Epoch, sequence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.journal.CommitTurnInput(f.ctx, turn.ID, "provider-input-"+turn.ID); err != nil {
		t.Fatal(err)
	}
	intent, err := f.journal.PrepareIntent(f.ctx, turn.ID, appwire.MethodTurnStart, text, newMutationID(t))
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func voiceRecoveryRequireRefusal(t *testing.T, f *voiceCorrectionStack, intent hubcore.VoiceIntent) {
	t.Helper()
	var response appwire.TurnStartResponse
	err := f.client.Request(f.ctx, intent.Method, intent.Params, &response)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("real admission did not return a wire refusal: %v", err)
	}
	data, ok := appwire.DecodedErrorData(wire.Data)
	if !ok || data.Cause != appwire.JournaledRejectionCause || data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.ClientMutationID != intent.MutationID || data.RetryDisposition != appwire.RetryDispositionNone {
		t.Fatalf("refusal was not a definitive journaled rejection: %+v", data)
	}
}
