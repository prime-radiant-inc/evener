package hubcore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestVoiceJournalBindingSurvivesReopen(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ctx := context.Background()
	journal, err := NewVoiceJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	want := VoiceBinding{
		ID: "binding-1", DeviceID: "device-1", Ref: "local:chief-1",
		InstanceID: "instance-1", Epoch: "epoch-1", GrantHash: "hash-1",
		Provider: "provider-1", Model: "model-1", Voice: "voice-1",
		ExpiresAt: time.Unix(2_000_000_000, 0).UTC(), Active: true,
	}
	if err := journal.CreateBinding(ctx, want, false); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewVoiceJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.Binding(ctx, want.ID)
	if err != nil {
		t.Fatalf("successful binding creation did not survive reopen: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restored binding = %#v, want %#v", got, want)
	}
}

func TestVoiceJournalRequiresExplicitTakeover(t *testing.T) {
	t.Parallel()
	journal, err := NewVoiceJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	ctx := context.Background()
	first := VoiceBinding{ID: "first", Ref: "local:chief", DeviceID: "phone-a", InstanceID: "instance", Epoch: "epoch-a", Active: true}
	second := first
	second.ID, second.DeviceID, second.Epoch = "second", "phone-b", "epoch-b"
	if err := journal.CreateBinding(ctx, first, false); err != nil {
		t.Fatal(err)
	}
	if err := journal.CreateBinding(ctx, second, false); !errors.Is(err, ErrVoiceBindingBusy) {
		t.Fatalf("second phone admission = %v, want owner conflict", err)
	}
	if err := journal.CreateBinding(ctx, second, true); err != nil {
		t.Fatal(err)
	}
	oldOwner, err := journal.Binding(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	newOwner, err := journal.Binding(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if oldOwner.Active || !newOwner.Active {
		t.Fatalf("takeover left old active=%v, new active=%v", oldOwner.Active, newOwner.Active)
	}
}

func TestVoiceJournalFailedTakeoverPreservesOwner(t *testing.T) {
	t.Parallel()
	journal, err := NewVoiceJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	ctx := context.Background()
	owner := VoiceBinding{ID: "owner", Ref: "local:chief", InstanceID: "instance", Epoch: "epoch", Active: true}
	if err := journal.CreateBinding(ctx, owner, false); err != nil {
		t.Fatal(err)
	}
	conflict := owner
	conflict.Epoch = "different-epoch"
	if err := journal.CreateBinding(ctx, conflict, true); err == nil {
		t.Fatal("binding identity was reused")
	}
	got, err := journal.Binding(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, owner) {
		t.Fatalf("failed takeover changed the owner: %#v", got)
	}
}
