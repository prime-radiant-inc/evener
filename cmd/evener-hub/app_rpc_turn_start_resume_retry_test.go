package hub

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// TestHubRPCTurnStartAfterSuccessfulResumeStaysCorrelated pins what a
// turn/start that resumed an exited session reports when the resumed session
// refuses the retried send.
//
// The scenario is the reported one: the caller sends a prompt to an exited
// session, the hub's auto-resume succeeds (the session is now live), and the
// single retry of the original send fails because the replacement daemon is
// not ready to take it yet. The prompt has not been delivered, and the caller
// must be able to correlate that failure to the mutation it submitted — the
// browser's outbox, the TUI's draft restore and every other surface decide
// what to do with the text from that correlation alone.
//
// A failure that names no clientMutationId cannot be judged by any of them:
// the web dispatcher's correlation check finds no match for its record, takes
// the "stop" path, and leaves the record submitting — the message is neither
// sent nor surfaced as failed, so the user has to retype it.
func TestHubRPCTurnStartAfterSuccessfulResumeStaysCorrelated(t *testing.T) {
	oldResolve, oldResume := resolveTurnStartSource, resumeTurnStartThread
	t.Cleanup(func() {
		resolveTurnStartSource, resumeTurnStartThread = oldResolve, oldResume
	})

	const mutationID = "mutation-after-successful-resume"

	// The ref must be one the hub knows, or the handler returns the first
	// failure unchanged and never resumes at all.
	root := t.TempDir()
	workingDir := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	ref := "local:" + sessionID

	// The retry's send is refused by the just-resumed session, exactly as a
	// daemon still finishing its restore would refuse it.
	startCalls := 0
	source := &scriptedAppSource{
		id: "local",
		thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Evener: appwire.EvenerThread{
				Ref:          ref,
				Capabilities: appwire.ThreadCapabilities{Send: true},
			},
		},
		startTurn: func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			startCalls++
			return appwire.TurnStartResponse{}, appwire.SessionUnavailable("session is not ready to accept a turn yet")
		},
	}
	resolveTurnStartSource = func(*appsource.Registry, string, string) (appsource.Source, error) {
		return source, nil
	}
	resumeCalls := 0
	resumeTurnStartThread = func(context.Context, hubcore.WebConfig, *appsource.Registry, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
		resumeCalls++
		// The resume SUCCEEDS: the session is live again when this returns.
		return appwire.ThreadResumeResponse{Thread: source.thread}, nil
	}

	server := newHubAppServer(hubcore.WebConfig{Past: past}, appsource.NewRegistry())
	_, err := exactDispatch(context.Background(), t, server, appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref:              ref,
		ClientMutationID: mutationID,
		Input:            []appwire.InputItem{{Type: "text", Text: "do the thing"}},
	})
	if err == nil {
		t.Fatal("turn/start reported success although neither attempt was accepted")
	}
	if resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", resumeCalls)
	}
	if startCalls != 2 {
		t.Fatalf("start calls=%d, want 2 (the original and the post-resume retry)", startCalls)
	}

	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("turn/start error %T=%v, want a WireError", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok {
		t.Fatalf("wire data %#v is not appwire.ErrorData", wire.Data)
	}
	if data.ClientMutationID != mutationID {
		t.Fatalf("the failure of a resumed session's retried send names clientMutationId %q, want %q: the caller cannot correlate it with the prompt it submitted (wire=%+v)",
			data.ClientMutationID, mutationID, wire)
	}
	if data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown || data.RetryDisposition != appwire.RetryDispositionBlocked {
		t.Fatalf("the failure must be reported as a blocked unknown outcome so the caller retains the input; got info=%q retryDisposition=%q",
			data.EvenerErrorInfo, data.RetryDisposition)
	}
}
