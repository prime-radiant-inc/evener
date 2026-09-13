package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// TestServerAppWireNotesHumanSetRejectsStaleInstance mirrors
// TestServerRejectsOldInstanceTurnMutationsAfterClear for notes/human/set: a
// delayed old-generation request after thread/clear replaces the instance
// must not store or steer on the replacement session.
func TestServerAppWireNotesHumanSetRejectsStaleInstance(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "old")
	prepared, err := PrepareAppIdentityForRef("local", "new", "local:old", "")
	if err != nil {
		t.Fatalf("prepare replacement: %v", err)
	}
	srv.ReplaceAppIdentity(prepared, nil)
	calls := 0
	srv.SetNotesHumanSetFunc(func(outerID, note string) (appwire.NotesHumanSetResponse, error) {
		calls++
		return appwire.NotesHumanSetResponse{Note: note}, nil
	})

	_, err = srv.handleAppNotesHumanSet(context.Background(), appwire.NotesHumanSetParams{
		Ref:                "local:old",
		ClientMutationID:   "notes-old",
		ExpectedInstanceID: "old",
		Note:               "late write",
	})
	if err == nil {
		t.Fatal("old-generation notes/human/set succeeded")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("old-generation notes error = %T %v, want WireError", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.RetryDisposition != appwire.RetryDispositionNone {
		t.Fatalf("old-generation notes error data = %#v, want notAccepted", wire.Data)
	}
	if calls != 0 {
		t.Fatalf("old-generation notes callback calls = %d, want 0", calls)
	}
}

// TestServerAppWireUrlsRemoveRejectsStaleInstance mirrors the notes/human/set
// stale-instance test for urls/remove: a delayed old-generation removal must
// not touch the replacement session's URL list.
func TestServerAppWireUrlsRemoveRejectsStaleInstance(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "old")
	prepared, err := PrepareAppIdentityForRef("local", "new", "local:old", "")
	if err != nil {
		t.Fatalf("prepare replacement: %v", err)
	}
	srv.ReplaceAppIdentity(prepared, nil)
	calls := 0
	srv.SetUrlsRemoveFunc(func(outerID, id string) (bool, error) {
		calls++
		return true, nil
	})

	_, err = srv.handleAppUrlsRemove(context.Background(), appwire.UrlsRemoveParams{
		Ref:                "local:old",
		ClientMutationID:   "urls-old",
		ExpectedInstanceID: "old",
		ID:                 "u1",
	})
	if err == nil {
		t.Fatal("old-generation urls/remove succeeded")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("old-generation urls error = %T %v, want WireError", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.RetryDisposition != appwire.RetryDispositionNone {
		t.Fatalf("old-generation urls error data = %#v, want notAccepted", wire.Data)
	}
	if calls != 0 {
		t.Fatalf("old-generation urls callback calls = %d, want 0", calls)
	}
}

// TestServerAppWireNotesMutationFencedWhileClearReplaces mirrors
// TestServerAppWireThreadClearFencesTurnMutationWhileReplacing for the notes
// verbs: while thread/clear holds the write gate, a notes/human/set and a
// urls/remove are both refused before their callbacks run.
func TestServerAppWireNotesMutationFencedWhileClearReplaces(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "old")
	entered := make(chan struct{})
	release := make(chan struct{})
	srv.SetClearFunc(func(_ context.Context, _ appwire.ThreadClearParams) error {
		close(entered)
		<-release
		prepared, err := PrepareAppIdentityForRef("local", "new", "local:old", "")
		if err != nil {
			return err
		}
		srv.ReplaceAppIdentity(prepared, nil)
		return nil
	})
	notesCalls, urlsCalls := 0, 0
	srv.SetNotesHumanSetFunc(func(outerID, note string) (appwire.NotesHumanSetResponse, error) {
		notesCalls++
		return appwire.NotesHumanSetResponse{Note: note}, nil
	})
	srv.SetUrlsRemoveFunc(func(outerID, id string) (bool, error) {
		urlsCalls++
		return true, nil
	})

	clearResult := make(chan error, 1)
	go func() {
		_, err := srv.handleAppThreadClear(context.Background(), appwire.ThreadClearParams{
			Ref: "local:old", ClientMutationID: "clear-1", ExpectedInstanceID: "old",
		})
		clearResult <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("clear did not enter callback")
	}

	_, notesErr := srv.handleAppNotesHumanSet(context.Background(), appwire.NotesHumanSetParams{
		Ref: "local:old", ClientMutationID: "notes-1", ExpectedInstanceID: "old", Note: "late",
	})
	if notesErr == nil {
		t.Fatal("notes/human/set succeeded while clear was replacing the instance")
	}
	_, urlsErr := srv.handleAppUrlsRemove(context.Background(), appwire.UrlsRemoveParams{
		Ref: "local:old", ClientMutationID: "urls-1", ExpectedInstanceID: "old", ID: "u1",
	})
	if urlsErr == nil {
		t.Fatal("urls/remove succeeded while clear was replacing the instance")
	}
	for name, err := range map[string]error{"notes/human/set": notesErr, "urls/remove": urlsErr} {
		var wire appwire.WireError
		if !errors.As(err, &wire) {
			t.Fatalf("%s error = %T %v, want WireError", name, err, err)
		}
		data, ok := wire.Data.(appwire.ErrorData)
		if !ok || data.MutationOutcome != appwire.MutationOutcomeNotAccepted {
			t.Fatalf("%s error data = %#v, want named notAccepted mutation", name, wire.Data)
		}
	}
	if notesCalls != 0 || urlsCalls != 0 {
		t.Fatalf("notes/url callback calls = %d/%d, want 0/0", notesCalls, urlsCalls)
	}

	close(release)
	if err := <-clearResult; err != nil {
		t.Fatalf("clear: %v", err)
	}
}
