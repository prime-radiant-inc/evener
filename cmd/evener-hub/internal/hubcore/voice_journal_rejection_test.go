package hubcore

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
)

func voiceJournalRejection(in VoiceIntent) appwire.WireError {
	return appwire.WireError{Data: appwire.ErrorData{
		Cause: appwire.JournaledRejectionCause, ClientMutationID: in.MutationID,
		MutationOutcome: appwire.MutationOutcomeNotAccepted, RetryDisposition: appwire.RetryDispositionNone,
	}}
}

func TestVoiceJournalRejectionRequiresDefinitiveOriginalRefusal(t *testing.T) {
	t.Parallel()
	j, _, b := voiceJournalFixture(t)
	ctx := context.Background()
	in := voiceJournalPrepared(t, j, b, 1)
	if _, err := j.RecordRejection(ctx, in.TurnID, voiceJournalRejection(in)); !errors.Is(err, ErrVoiceConflict) {
		t.Fatalf("refusal before attempt: %v", err)
	}
	attempted, err := j.RecordAttempt(ctx, in.TurnID, b.InstanceID, false)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*appwire.ErrorData){
		"unstamped": func(d *appwire.ErrorData) { d.Cause = "" },
		"unknown":   func(d *appwire.ErrorData) { d.MutationOutcome = appwire.MutationOutcomeUnknown },
		"foreign":   func(d *appwire.ErrorData) { d.ClientMutationID += "other" },
		"retryable": func(d *appwire.ErrorData) { d.RetryDisposition = appwire.RetryDispositionAutomatic },
	} {
		t.Run(name, func(t *testing.T) {
			refusal := voiceJournalRejection(in)
			data, _ := appwire.DecodedErrorData(refusal.Data)
			change(&data)
			refusal.Data = data
			if _, err := j.RecordRejection(ctx, in.TurnID, refusal); !errors.Is(err, ErrVoiceConflict) {
				t.Fatalf("non-authoritative refusal settled custody: %v", err)
			}
			stored, err := j.Intent(ctx, in.TurnID)
			if err != nil || !reflect.DeepEqual(stored, attempted) {
				t.Fatalf("refused settlement changed uncertainty or immutable input: %+v %v", stored, err)
			}
		})
	}
	if _, err := j.RecordReceipt(ctx, in.TurnID, voiceJournalReceipt(in)); err != nil {
		t.Fatal(err)
	}
	if _, err := j.RecordRejection(ctx, in.TurnID, voiceJournalRejection(in)); !errors.Is(err, ErrVoiceConflict) {
		t.Fatalf("refusal replaced an accepted intent: %v", err)
	}
}

func TestVoiceJournalRejectionSurvivesReopenWithoutRevivingIdentity(t *testing.T) {
	t.Parallel()
	j, root, b := voiceJournalFixture(t)
	ctx := context.Background()
	in := voiceJournalPrepared(t, j, b, 1)
	if _, err := j.RecordAttempt(ctx, in.TurnID, b.InstanceID, false); err != nil {
		t.Fatal(err)
	}
	if err := j.RetireBinding(ctx, b.ID, b.Epoch); err != nil {
		t.Fatal(err)
	}
	// Like an acceptance receipt, definitive evidence remains useful after the
	// socket or binding retires. No active grant is needed to settle custody.
	refused, err := j.RecordRejection(ctx, in.TurnID, voiceJournalRejection(in))
	if err != nil || refused.State != VoiceIntentRejected || refused.Receipt != nil {
		t.Fatalf("late refusal did not settle: %+v %v", refused, err)
	}
	in.State = VoiceIntentRejected
	if !reflect.DeepEqual(refused, in) {
		t.Fatal("refusal changed original text, target or mutation bytes")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewVoiceJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	stored, err := reopened.Intent(ctx, in.TurnID)
	if err != nil || !reflect.DeepEqual(stored, refused) {
		t.Fatalf("reopen lost terminal refusal or immutable input: %+v %v", stored, err)
	}
	duplicate, err := reopened.RecordRejection(ctx, in.TurnID, voiceJournalRejection(in))
	if err != nil || !reflect.DeepEqual(duplicate, refused) {
		t.Fatalf("duplicate refusal changed custody: %+v %v", duplicate, err)
	}
	if _, err := reopened.RecordAttempt(ctx, in.TurnID, b.InstanceID, true); !errors.Is(err, ErrVoiceConflict) {
		t.Fatalf("rejected identity was retried: %v", err)
	}
	if _, err := reopened.RecordReceipt(ctx, in.TurnID, voiceJournalReceipt(in)); !errors.Is(err, ErrVoiceConflict) {
		t.Fatalf("rejected identity acquired acceptance: %v", err)
	}
	if _, err := reopened.PrepareIntent(ctx, in.TurnID, appwire.MethodTurnSteer, in.Text, in.MutationID); !errors.Is(err, ErrVoiceConflict) {
		t.Fatalf("refusal was silently converted into a steer: %v", err)
	}
}
