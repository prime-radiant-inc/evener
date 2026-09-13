package server

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestAppWireNotesPayloadMismatchIsInvalidRequest mirrors
// TestAppWireMutationPayloadMismatchIsInvalidRequest for the notes verbs: a
// reused outer ID with different input yields an InvalidRequest wire error
// (via NormalizeClientMutationError at the daemon boundary), and the stored
// note is untouched.
func TestAppWireNotesPayloadMismatchIsInvalidRequest(t *testing.T) {
	sess := newNotesTestSession(t)
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())
	srv.SetNotesHumanSetFunc(sess.SetHumanNote)
	srv.SetUrlsRemoveFunc(sess.RemoveSessionURL)

	conn := srv.AppServer().NewConnection("notes-mismatch")
	params := appwire.NotesHumanSetParams{
		Ref:                "local:" + sess.ID(),
		ClientMutationID:   "reused-notes-id",
		ExpectedInstanceID: sess.ID(),
		Note:               "first note",
	}
	first := notesRPC(conn, 2, appwire.MethodNotesHumanSet, params)
	if first.Kind() != appwire.MessageResponse {
		t.Fatalf("first response kind = %v, error = %#v", first.Kind(), first.Error)
	}
	params.Note = "different note"
	mismatch := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodNotesHumanSet, params),
	)
	if mismatch.Kind() != appwire.MessageError || mismatch.Error.Error.Code != appwire.CodeInvalidRequest {
		t.Fatalf("mismatch response = %#v, want InvalidRequest", mismatch)
	}
	if got := sess.HumanNoteForTest(); got != "first note" {
		t.Fatalf("stored note after mismatch = %q, want %q", got, "first note")
	}
}

// TestAppWireUrlsPayloadMismatchIsInvalidRequest is the urls/remove half of
// the K3 contract: a reused outer ID naming a different entry id yields an
// InvalidRequest wire error, and the URL list is untouched.
func TestAppWireUrlsPayloadMismatchIsInvalidRequest(t *testing.T) {
	sess := newNotesTestSession(t)
	a, err := sess.AddSessionURLForTest("https://x.test/a", "")
	if err != nil {
		t.Fatalf("add a: %v", err)
	}
	b, err := sess.AddSessionURLForTest("https://x.test/b", "")
	if err != nil {
		t.Fatalf("add b: %v", err)
	}
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())
	srv.SetNotesHumanSetFunc(sess.SetHumanNote)
	srv.SetUrlsRemoveFunc(sess.RemoveSessionURL)

	conn := srv.AppServer().NewConnection("urls-mismatch")
	params := appwire.UrlsRemoveParams{
		Ref:                "local:" + sess.ID(),
		ClientMutationID:   "reused-urls-id",
		ExpectedInstanceID: sess.ID(),
		ID:                 a.ID,
	}
	first := notesRPC(conn, 2, appwire.MethodUrlsRemove, params)
	if first.Kind() != appwire.MessageResponse {
		t.Fatalf("first response kind = %v, error = %#v", first.Kind(), first.Error)
	}
	params.ID = b.ID
	mismatch := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodUrlsRemove, params),
	)
	if mismatch.Kind() != appwire.MessageError || mismatch.Error.Error.Code != appwire.CodeInvalidRequest {
		t.Fatalf("mismatch response = %#v, want InvalidRequest", mismatch)
	}
	if got := sess.SessionURLsForTest(); len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("url list after mismatch = %+v, want only b", got)
	}
}

// TestAppWireNotesPersistenceFailureCarriesMutationMetadata mirrors
// TestAppWireMutationPersistenceFailureCanRecoverInProcess for the notes
// verbs: a callback persistence failure surfaces the blocked-retry metadata
// (outcome unknown, mutation ID attached), and the retry applies.
func TestAppWireNotesPersistenceFailureCarriesMutationMetadata(t *testing.T) {
	sess := newNotesTestSession(t)
	attempts := 0
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())
	srv.SetNotesHumanSetFunc(func(outerID, note string) (appwire.NotesHumanSetResponse, error) {
		attempts++
		if attempts == 1 {
			return appwire.NotesHumanSetResponse{}, errors.New("journal write failed")
		}
		return sess.SetHumanNote(outerID, note)
	})
	srv.SetUrlsRemoveFunc(sess.RemoveSessionURL)

	conn := srv.AppServer().NewConnection("notes-persistence")
	params := appwire.NotesHumanSetParams{
		Ref:                "local:" + sess.ID(),
		ClientMutationID:   "recover-notes-after-persistence",
		ExpectedInstanceID: sess.ID(),
		Note:               "apply after recovery",
	}
	failed := notesRPC(conn, 2, appwire.MethodNotesHumanSet, params)
	if failed.Kind() != appwire.MessageError {
		t.Fatalf("persistence response kind = %v, want error", failed.Kind())
	}
	data, ok := failed.Error.Error.Data.(appwire.ErrorData)
	if !ok ||
		data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown ||
		data.ClientMutationID != params.ClientMutationID ||
		data.MutationOutcome != appwire.MutationOutcomeUnknown ||
		data.RetryDisposition != appwire.RetryDispositionBlocked ||
		data.Cause != "persistenceUnavailable" {
		t.Fatalf("persistence error = %#v, want blocked-retry metadata with the mutation ID", failed.Error.Error)
	}
	recovered := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodNotesHumanSet, params),
	)
	if recovered.Kind() != appwire.MessageResponse {
		t.Fatalf("recovery response kind = %v, error = %#v", recovered.Kind(), recovered.Error)
	}
	if out := recovered.Response.Result.(appwire.NotesHumanSetResponse); out.Note != "apply after recovery" {
		t.Fatalf("recovery note = %q, want %q", out.Note, "apply after recovery")
	}
}
