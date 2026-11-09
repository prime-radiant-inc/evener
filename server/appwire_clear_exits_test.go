package server

import (
	"context"
	"errors"
	"os"
	"testing"

	"primeradiant.com/evener/appwire"
)

// clearFailure reduces a thread/clear error to the wire code and mutation
// disposition a client keys its retry decision on.
func clearFailure(t *testing.T, err error) (code int, outcome appwire.MutationOutcome) {
	t.Helper()
	if err == nil {
		t.Fatal("thread/clear succeeded, want a refusal")
	}
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("thread/clear error = %T %v, want WireError", err, err)
	}
	data, _ := wire.Data.(appwire.ErrorData)
	return wire.Code, data.MutationOutcome
}

func clearParams(id string) appwire.ThreadClearParams {
	return appwire.ThreadClearParams{Ref: "local:old", ClientMutationID: id, ExpectedInstanceID: "old"}
}

// replaceJournalWithDirectory makes every later journal write fail without a
// production fault seam: a rename cannot replace a directory.
func replaceJournalWithDirectory(t *testing.T, path string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func restoreJournalWrites(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// serverOverClearJournal writes records the way a daemon that died mid-clear left
// them, then returns a server started over that journal.
func serverOverClearJournal(t *testing.T, stateDir string, records ...threadClearRecord) *Server {
	t.Helper()
	byID := map[string]threadClearRecord{}
	for _, record := range records {
		byID[record.ClientMutationID] = record
	}
	if err := persistThreadClearJournal(threadClearJournalPath(stateDir), byID); err != nil {
		t.Fatal(err)
	}
	return NewServer(ServerConfig{StateDir: stateDir})
}

func reservedClear(t *testing.T, params appwire.ThreadClearParams) threadClearRecord {
	t.Helper()
	hash, err := threadClearRequestHash(params)
	if err != nil {
		t.Fatal(err)
	}
	return threadClearRecord{
		ClientMutationID:   params.ClientMutationID,
		RequestHash:        hash,
		Ref:                params.Ref,
		ExpectedInstanceID: params.ExpectedInstanceID,
		State:              threadClearReserved,
	}
}

// requireDurableReplay restarts a server over stateDir with the replacement
// installed under the stable ref and requires the clear to replay from the
// journal without invoking a replacement callback.
func requireDurableReplay(t *testing.T, stateDir string, params appwire.ThreadClearParams) {
	t.Helper()
	restarted := NewServer(ServerConfig{StateDir: stateDir})
	prepared, err := PrepareAppIdentityForRef("local", "new", params.Ref, "")
	if err != nil {
		t.Fatal(err)
	}
	restarted.ReplaceAppIdentity(prepared.WithBootGeneration("1"), nil)
	restarted.SetClearFunc(func(context.Context, appwire.ThreadClearParams) error {
		t.Fatal("restart replay invoked the replacement callback")
		return nil
	})
	response, err := restarted.handleAppThreadClear(context.Background(), params)
	if err != nil {
		t.Fatalf("restart replay: %v", err)
	}
	if response.Receipt.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("restart disposition = %q, want replayed", response.Receipt.Disposition)
	}
}

func TestThreadClearRefusals(t *testing.T) {
	cases := []struct {
		name        string
		setup       func(t *testing.T, srv *Server)
		params      appwire.ThreadClearParams
		wantCode    int
		wantOutcome appwire.MutationOutcome
	}{
		{
			name:     "missing ref",
			params:   appwire.ThreadClearParams{ClientMutationID: "c", ExpectedInstanceID: "old"},
			wantCode: appwire.CodeInvalidParams,
		},
		{
			name:     "missing mutation id",
			params:   appwire.ThreadClearParams{Ref: "local:old", ExpectedInstanceID: "old"},
			wantCode: appwire.CodeInvalidParams,
		},
		{
			name:     "missing expected instance",
			params:   appwire.ThreadClearParams{Ref: "local:old", ClientMutationID: "c"},
			wantCode: appwire.CodeInvalidParams,
		},
		{
			name:     "unparseable ref",
			params:   appwire.ThreadClearParams{Ref: "not a ref", ClientMutationID: "c", ExpectedInstanceID: "old"},
			wantCode: appwire.CodeInvalidParams,
		},
		{
			name:     "ref of another source",
			params:   appwire.ThreadClearParams{Ref: "other:old", ClientMutationID: "c", ExpectedInstanceID: "old"},
			wantCode: appwire.CodeUnavailable,
		},
		{
			name:        "another retry-safe mutation holds the gate",
			setup:       func(_ *testing.T, srv *Server) { srv.appMutationGate.Lock() },
			params:      clearParams("c"),
			wantCode:    appwire.CodeConflict,
			wantOutcome: appwire.MutationOutcomeNotAccepted,
		},
		{
			name:        "ref names another workspace",
			params:      appwire.ThreadClearParams{Ref: "local:elsewhere", ClientMutationID: "c", ExpectedInstanceID: "old"},
			wantCode:    appwire.CodeConflict,
			wantOutcome: appwire.MutationOutcomeNotAccepted,
		},
		{
			name:        "expected instance is stale",
			params:      appwire.ThreadClearParams{Ref: "local:old", ClientMutationID: "c", ExpectedInstanceID: "older"},
			wantCode:    appwire.CodeConflict,
			wantOutcome: appwire.MutationOutcomeNotAccepted,
		},
		{
			name:        "a turn is unresolved",
			setup:       func(_ *testing.T, srv *Server) { srv.SetProcessing(true) },
			params:      clearParams("c"),
			wantCode:    appwire.CodeConflict,
			wantOutcome: appwire.MutationOutcomeNotAccepted,
		},
		{
			name: "an approval is unresolved",
			setup: func(_ *testing.T, srv *Server) {
				srv.mu.Lock()
				srv.appEnvelope.PendingQuestion = &appwire.PendingQuestion{}
				srv.mu.Unlock()
			},
			params:      clearParams("c"),
			wantCode:    appwire.CodeConflict,
			wantOutcome: appwire.MutationOutcomeNotAccepted,
		},
		{
			name:     "no clear callback is installed",
			setup:    func(_ *testing.T, srv *Server) { srv.SetClearFunc(nil) },
			params:   clearParams("c"),
			wantCode: appwire.CodeUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(ServerConfig{StateDir: t.TempDir()})
			srv.SetAppIdentity("local", "old")
			called := false
			srv.SetClearFunc(func(context.Context, appwire.ThreadClearParams) error {
				called = true
				return nil
			})
			if tc.setup != nil {
				tc.setup(t, srv)
			}
			_, err := srv.handleAppThreadClear(context.Background(), tc.params)
			code, outcome := clearFailure(t, err)
			if code != tc.wantCode || outcome != tc.wantOutcome {
				t.Fatalf("refusal = (code %d, outcome %q), want (%d, %q)", code, outcome, tc.wantCode, tc.wantOutcome)
			}
			if called {
				t.Fatal("a refused clear still invoked the replacement callback")
			}
		})
	}
}

func TestThreadClearRefusesReusedMutationIDWithDifferentRequest(t *testing.T) {
	srv := NewServer(ServerConfig{StateDir: t.TempDir()})
	srv.SetAppIdentity("local", "old")
	setReplacingClearFunc(srv, "new")
	if _, err := srv.handleAppThreadClear(context.Background(), clearParams("reused")); err != nil {
		t.Fatalf("first clear: %v", err)
	}
	other := clearParams("reused")
	other.ExpectedInstanceID = "different"
	code, _ := clearFailure(t, clearError(srv.handleAppThreadClear(context.Background(), other)))
	if code != appwire.CodeInvalidRequest {
		t.Fatalf("reused id code = %d, want invalid request", code)
	}
}

func clearError(_ appwire.ThreadClearResponse, err error) error { return err }

func TestThreadClearWithUnreadableJournalIsOutcomeUnknown(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.WriteFile(threadClearJournalPath(stateDir), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ServerConfig{StateDir: stateDir})
	srv.SetAppIdentity("local", "old")
	called := false
	srv.SetClearFunc(func(context.Context, appwire.ThreadClearParams) error {
		called = true
		return nil
	})
	code, outcome := clearFailure(t, clearError(srv.handleAppThreadClear(context.Background(), clearParams("c"))))
	if code != appwire.CodeInternalError || outcome != appwire.MutationOutcomeUnknown {
		t.Fatalf("refusal = (code %d, outcome %q), want internal error, unknown", code, outcome)
	}
	if called {
		t.Fatal("a clear ran with no usable journal to record it")
	}
}

func TestThreadClearReplayOfAppliedRecordWithoutReceiptIsOutcomeUnknown(t *testing.T) {
	params := clearParams("c")
	record := reservedClear(t, params)
	record.State = threadClearApplied
	srv := serverOverClearJournal(t, t.TempDir(), record)
	srv.SetAppIdentity("local", "old")
	code, outcome := clearFailure(t, clearError(srv.handleAppThreadClear(context.Background(), params)))
	if code != appwire.CodeInternalError || outcome != appwire.MutationOutcomeUnknown {
		t.Fatalf("refusal = (code %d, outcome %q), want internal error, unknown", code, outcome)
	}
}

// A daemon that died after installing the replacement but before recording the
// receipt restarts with a reserved record and the new instance under the stable
// ref. The retry has to report that replacement as applied, not run a second one.
func TestThreadClearRecoversReservedRecordWhenReplacementAlreadyInstalled(t *testing.T) {
	stateDir := t.TempDir()
	params := clearParams("c")
	srv := serverOverClearJournal(t, stateDir, reservedClear(t, params))
	prepared, err := PrepareAppIdentityForRef("local", "new", params.Ref, "")
	if err != nil {
		t.Fatal(err)
	}
	srv.ReplaceAppIdentity(prepared.WithBootGeneration("1"), nil)
	calls := 0
	srv.SetClearFunc(func(context.Context, appwire.ThreadClearParams) error {
		calls++
		return nil
	})

	response, err := srv.handleAppThreadClear(context.Background(), params)
	if err != nil {
		t.Fatalf("recovery retry: %v", err)
	}
	if response.Thread.ID != "new" || response.Receipt.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("recovery = (%q, %q), want (new, replayed)", response.Thread.ID, response.Receipt.Disposition)
	}
	if calls != 0 {
		t.Fatalf("recovery invoked the replacement callback %d times", calls)
	}

	// The receipt is now durable: a restart replays it with no reservation left.
	restarted := NewServer(ServerConfig{StateDir: stateDir})
	restarted.ReplaceAppIdentity(prepared.WithBootGeneration("1"), nil)
	again, err := restarted.handleAppThreadClear(context.Background(), params)
	if err != nil || again.Receipt.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("restart after recovery = (%v, %v), want replayed", again.Receipt.Disposition, err)
	}
}

func TestThreadClearRecoveryThatCannotPersistIsOutcomeUnknownThenRetries(t *testing.T) {
	stateDir := t.TempDir()
	params := clearParams("c")
	srv := serverOverClearJournal(t, stateDir, reservedClear(t, params))
	prepared, err := PrepareAppIdentityForRef("local", "new", params.Ref, "")
	if err != nil {
		t.Fatal(err)
	}
	srv.ReplaceAppIdentity(prepared.WithBootGeneration("1"), nil)
	srv.SetClearFunc(func(context.Context, appwire.ThreadClearParams) error {
		t.Fatal("recovery invoked the replacement callback")
		return nil
	})
	journalPath := threadClearJournalPath(stateDir)
	replaceJournalWithDirectory(t, journalPath)

	code, outcome := clearFailure(t, clearError(srv.handleAppThreadClear(context.Background(), params)))
	if code != appwire.CodeInternalError || outcome != appwire.MutationOutcomeUnknown {
		t.Fatalf("refusal = (code %d, outcome %q), want internal error, unknown", code, outcome)
	}

	restoreJournalWrites(t, journalPath)
	response, err := srv.handleAppThreadClear(context.Background(), params)
	if err != nil {
		t.Fatalf("retry after persistence recovered: %v", err)
	}
	if response.Receipt.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("retry disposition = %q, want replayed", response.Receipt.Disposition)
	}
	requireDurableReplay(t, stateDir, params)
}

// A reserved record whose replacement never installed (the daemon died before
// the callback finished) is retried through the normal path, under the same id.
func TestThreadClearResumesReservedRecordWhenReplacementNeverInstalled(t *testing.T) {
	params := clearParams("c")
	srv := serverOverClearJournal(t, t.TempDir(), reservedClear(t, params))
	srv.SetAppIdentity("local", "old")
	setReplacingClearFunc(srv, "new")

	response, err := srv.handleAppThreadClear(context.Background(), params)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if response.Thread.ID != "new" || response.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("resume = (%q, %q), want (new, applied)", response.Thread.ID, response.Receipt.Disposition)
	}
}

// When the replacement installs but its receipt cannot be written, the client
// hears outcome-unknown, the new instance stays, and the same id later reports
// the replacement as replayed without running a second one.
func TestThreadClearReceiptThatCannotPersistIsOutcomeUnknownThenReplays(t *testing.T) {
	stateDir := t.TempDir()
	srv := NewServer(ServerConfig{StateDir: stateDir})
	srv.SetAppIdentity("local", "old")
	journalPath := threadClearJournalPath(stateDir)
	calls := 0
	srv.SetClearFunc(func(_ context.Context, params appwire.ThreadClearParams) error {
		calls++
		prepared, err := PrepareAppIdentityForRef("local", "new", params.Ref, "")
		if err != nil {
			return err
		}
		srv.ReplaceAppIdentity(prepared.WithBootGeneration("1"), nil)
		replaceJournalWithDirectory(t, journalPath)
		return nil
	})
	params := clearParams("c")

	code, outcome := clearFailure(t, clearError(srv.handleAppThreadClear(context.Background(), params)))
	if code != appwire.CodeInternalError || outcome != appwire.MutationOutcomeUnknown {
		t.Fatalf("refusal = (code %d, outcome %q), want internal error, unknown", code, outcome)
	}

	restoreJournalWrites(t, journalPath)
	response, err := srv.handleAppThreadClear(context.Background(), params)
	if err != nil {
		t.Fatalf("retry after persistence recovered: %v", err)
	}
	if response.Thread.ID != "new" || response.Receipt.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("retry = (%q, %q), want (new, replayed)", response.Thread.ID, response.Receipt.Disposition)
	}
	if calls != 1 {
		t.Fatalf("replacement callback ran %d times, want 1", calls)
	}
	requireDurableReplay(t, stateDir, params)
}

// A reservation that cannot be written refuses the clear before the callback
// runs, and the same id clears once the journal is writable again.
func TestThreadClearReservationThatCannotPersistIsOutcomeUnknownThenClears(t *testing.T) {
	stateDir := t.TempDir()
	srv := NewServer(ServerConfig{StateDir: stateDir})
	srv.SetAppIdentity("local", "old")
	journalPath := threadClearJournalPath(stateDir)
	replaceJournalWithDirectory(t, journalPath)
	called := false
	srv.SetClearFunc(func(context.Context, appwire.ThreadClearParams) error {
		called = true
		return nil
	})
	params := clearParams("c")

	code, outcome := clearFailure(t, clearError(srv.handleAppThreadClear(context.Background(), params)))
	if code != appwire.CodeInternalError || outcome != appwire.MutationOutcomeUnknown {
		t.Fatalf("refusal = (code %d, outcome %q), want internal error, unknown", code, outcome)
	}
	if called {
		t.Fatal("a clear ran without a durable reservation")
	}

	restoreJournalWrites(t, journalPath)
	setReplacingClearFunc(srv, "new")
	response, err := srv.handleAppThreadClear(context.Background(), params)
	if err != nil {
		t.Fatalf("retry after persistence recovered: %v", err)
	}
	if response.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("retry disposition = %q, want applied", response.Receipt.Disposition)
	}
}
