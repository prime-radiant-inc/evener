package hub

import (
	"context"
	"encoding/json"
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

// TestHubRPCTurnStartRetryCorrelation covers what a turn/start that resumed an
// exited session reports when the post-resume retry fails in ways other than a
// plain uncorrelated failure. Each failure must keep the meaning its caller can
// act on; only a failure that names a different (or no) mutation is wrapped as
// this caller's blocked-unknown outcome.
func TestHubRPCTurnStartRetryCorrelation(t *testing.T) {
	const mutationID = "mutation-retry-correlation"

	cases := []struct {
		name     string
		retryErr error
		check    func(t *testing.T, wire appwire.WireError, data appwire.ErrorData)
	}{
		{
			name:     "shape refusal keeps its own meaning",
			retryErr: appwire.InvalidParams("input item type is not supported"),
			check: func(t *testing.T, wire appwire.WireError, data appwire.ErrorData) {
				t.Helper()
				if wire.Code != appwire.CodeInvalidParams {
					t.Fatalf("an invalid-params refusal must survive the retry: code=%d want %d (wire=%+v)",
						wire.Code, appwire.CodeInvalidParams, wire)
				}
				if data.EvenerErrorInfo == appwire.ErrorMutationOutcomeUnknown ||
					data.MutationOutcome == appwire.MutationOutcomeUnknown ||
					data.RetryDisposition == appwire.RetryDispositionBlocked {
					t.Fatalf("a shape refusal must not be reported as a blocked unknown outcome: data=%#v", data)
				}
			},
		},
		{
			name:     "already-correlated refusal passes through",
			retryErr: appwire.MutationNotAccepted(mutationID, "the resumed session refused the retry"),
			check: func(t *testing.T, wire appwire.WireError, data appwire.ErrorData) {
				t.Helper()
				if data.ClientMutationID != mutationID || data.MutationOutcome != appwire.MutationOutcomeNotAccepted {
					t.Fatalf("a refusal already naming this caller's mutation must pass through intact: data=%#v (wire=%+v)", data, wire)
				}
			},
		},
		{
			name:     "refusal naming a different mutation is wrapped for this caller",
			retryErr: appwire.MutationNotAccepted("some-other-mutation", "another caller's mutation"),
			check: func(t *testing.T, wire appwire.WireError, data appwire.ErrorData) {
				t.Helper()
				if data.ClientMutationID != mutationID ||
					data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown ||
					data.MutationOutcome != appwire.MutationOutcomeUnknown ||
					data.RetryDisposition != appwire.RetryDispositionBlocked {
					t.Fatalf("a refusal naming a different mutation is not this caller's to judge and must be wrapped as blocked-unknown for %q: data=%#v (wire=%+v)",
						mutationID, data, wire)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldResolve, oldResume := resolveTurnStartSource, resumeTurnStartThread
			t.Cleanup(func() {
				resolveTurnStartSource, resumeTurnStartThread = oldResolve, oldResume
			})

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
					if startCalls == 1 {
						// The first send finds the exited session and triggers the resume.
						return appwire.TurnStartResponse{}, appwire.SessionUnavailable("session has exited")
					}
					return appwire.TurnStartResponse{}, tc.retryErr
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
			tc.check(t, wire, data)
		})
	}
}

// TestCorrelateRetryFailureEnrichesUnnamedTargetDeletion pins that a target
// deletion which names no mutation is enriched with the caller's
// clientMutationId, while one that already names it is returned unchanged.
//
// A preflight thread/read deletion relayed from a remote hub names no
// clientMutationId; the web outbox correlates by that id alone, so without the
// stamp the dispatcher cannot classify the failure at all and leaves the record
// submitting. Enriching keeps the deletion's own outcome
// (MutationOutcomeTargetDeleted) so it settles as orphaned instead of being
// reported as a generic outage.
func TestCorrelateRetryFailureEnrichesUnnamedTargetDeletion(t *testing.T) {
	const mutationID = "mutation-deleted-enrich"

	sessionID := webTestSessionID
	ref := localAppRef(sessionID)
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin("project-fence-0123456789", []hubcore.DeletionTarget{{
		Ref:      ref,
		ThreadID: sessionID,
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{DeletionStore: store}

	unnamed := deletionFenceError(cfg, ref, sessionID, "")
	if errorNamesClientMutation(unnamed, mutationID) {
		t.Fatalf("precondition: the deletion under test must name no mutation: %v", unnamed)
	}
	enriched := correlateRetryFailure(mutationID, unnamed)
	if enriched == nil {
		t.Fatal("an unnamed target deletion must be enriched, not passed through unchanged")
	}
	var wire appwire.WireError
	if !errors.As(enriched, &wire) {
		t.Fatalf("enriched error %T=%v, want a WireError", enriched, enriched)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok {
		t.Fatalf("wire data %#v is not appwire.ErrorData", wire.Data)
	}
	if data.ClientMutationID != mutationID {
		t.Fatalf("enriched deletion names clientMutationId %q, want %q: the dispatcher cannot settle the record without it (wire=%+v)",
			data.ClientMutationID, mutationID, wire)
	}
	if data.MutationOutcome != appwire.MutationOutcomeTargetDeleted || data.RetryDisposition != appwire.RetryDispositionNone {
		t.Fatalf("enriching must keep the deletion's own outcome: outcome=%q retryDisposition=%q (wire=%+v)",
			data.MutationOutcome, data.RetryDisposition, wire)
	}

	named := deletionFenceError(cfg, ref, sessionID, mutationID)
	if got := correlateRetryFailure(mutationID, named); got != nil {
		t.Fatalf("an already-correlated target deletion must be returned unchanged, got %v", got)
	}
}

// TestHubRPCTurnStartRetryKeepsPreDispatchRefusalOutcome pins that a
// pre-dispatch refusal of the post-resume retry keeps its own outcome rather
// than being blanket-rewritten as not-accepted.
//
// turn/start's retry runs because the request's own resume made it possible, so
// a retry that never reached a source is normally reported as not-accepted. But
// two refusals are raised before dispatch yet already carry this caller's
// clientMutationId and a meaning of their own, because the first attempt in the
// same handler deliberately preserves them: a target deletion (the target is
// gone, MutationOutcomeTargetDeleted) and a recovery-admission /
// daemon-restart-required refusal (MutationOutcomeUnknown with
// RetryDispositionBlocked). Rewriting either as not-accepted tells the caller a
// lie it can act on: the deletion is attributed to a dispatch that never
// happened, and the blocked outcome is downgraded to a plain rejection.
func TestHubRPCTurnStartRetryKeepsPreDispatchRefusalOutcome(t *testing.T) {
	const mutationID = "mutation-retry-pre-dispatch"

	cases := []struct {
		name     string
		refusal  func(t *testing.T, ref, sessionID string) error
		outcome  appwire.MutationOutcome
		retry    appwire.RetryDisposition
		infoCode appwire.ErrorInfo
	}{
		{
			name: "deletion fence keeps targetDeleted",
			refusal: func(t *testing.T, ref, sessionID string) error {
				t.Helper()
				store, err := hubcore.NewDeletionStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Begin("project-fence-0123456789", []hubcore.DeletionTarget{{
					Ref:      ref,
					ThreadID: sessionID,
				}}); err != nil {
					t.Fatal(err)
				}
				return deletionFenceError(hubcore.WebConfig{DeletionStore: store}, ref, sessionID, mutationID)
			},
			outcome:  appwire.MutationOutcomeTargetDeleted,
			retry:    appwire.RetryDispositionNone,
			infoCode: appwire.ErrorActionUnavailable,
		},
		{
			name: "recovery admission keeps blocked-unknown",
			refusal: func(t *testing.T, _, _ string) error {
				t.Helper()
				restartRequired := appwire.WireError{
					Code:    appwire.CodeConflict,
					Message: "Session restart required: daemon uses an incompatible protocol",
					Data:    appwire.ErrorData{EvenerErrorInfo: appwire.ErrorConflict, Cause: "daemonRestartRequired"},
				}
				return blockedAdmissionMutationError(restartRequired, mutationID)
			},
			outcome:  appwire.MutationOutcomeUnknown,
			retry:    appwire.RetryDispositionBlocked,
			infoCode: appwire.ErrorConflict,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldResolve, oldResume := resolveTurnStartSource, resumeTurnStartThread
			t.Cleanup(func() {
				resolveTurnStartSource, resumeTurnStartThread = oldResolve, oldResume
			})

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
			retryRefusal := tc.refusal(t, ref, sessionID)

			resolveCalls := 0
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
					// The first send finds the exited session and triggers the resume.
					return appwire.TurnStartResponse{}, appwire.SessionUnavailable("session has exited")
				},
			}
			resolveTurnStartSource = func(*appsource.Registry, string, string) (appsource.Source, error) {
				resolveCalls++
				if resolveCalls == 1 {
					return source, nil
				}
				// The retry's source resolution refuses before anything reached a
				// source, but the refusal already names this caller's mutation.
				return nil, retryRefusal
			}
			resumeTurnStartThread = func(context.Context, hubcore.WebConfig, *appsource.Registry, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
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
				t.Fatal("turn/start reported success although the retry was refused")
			}
			if resolveCalls != 2 {
				t.Fatalf("source resolution calls=%d, want 2 (the original and the post-resume retry)", resolveCalls)
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
				t.Fatalf("refusal names clientMutationId %q, want %q (wire=%+v)", data.ClientMutationID, mutationID, wire)
			}
			if data.MutationOutcome != tc.outcome || data.RetryDisposition != tc.retry || data.EvenerErrorInfo != tc.infoCode {
				t.Fatalf("pre-dispatch refusal of the retry must keep its own outcome: outcome=%q retryDisposition=%q info=%q, want outcome=%q retryDisposition=%q info=%q (wire=%+v)",
					data.MutationOutcome, data.RetryDisposition, data.EvenerErrorInfo, tc.outcome, tc.retry, tc.infoCode, wire)
			}
			if data.MutationOutcome == appwire.MutationOutcomeNotAccepted {
				t.Fatalf("an already-correlated pre-dispatch refusal must not be rewritten as not-accepted: data=%#v", data)
			}
		})
	}
}

// relayedWireDataMap decodes a WireError's Data the way a remote-hub relay
// leaves it: a JSON round-trip into map[string]any. The test reads finished
// failures through this shape so the assertions match what the web client
// actually receives for a relayed error.
func relayedWireDataMap(t *testing.T, data any) map[string]any {
	t.Helper()
	if data == nil {
		return map[string]any{}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal wire data %#v: %v", data, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal wire data %s: %v", raw, err)
	}
	return out
}

// TestCorrelateRetryFailureReadsMapShapedWireData pins that every
// retry-correlation decision reads a WireError whose Data a remote-hub relay
// decoded as map[string]any, not only the typed appwire.ErrorData a local
// source raises. A relayed failure crosses the wire and comes back as a map, so
// a decision that only understood the typed shape would misclassify every
// failure relayed from another hub -- wrapping an already-correlated refusal, or
// restamping a deletion that names a different mutation and thereby settling
// that other caller's record as orphaned.
//
// Each case asserts the three fields the client's mutation dispatcher actually
// reads: clientMutationId, mutationOutcome and retryDisposition.
func TestCorrelateRetryFailureReadsMapShapedWireData(t *testing.T) {
	const mutationID = "mutation-map-shaped-data"

	cases := []struct {
		name                 string
		retryErr             error
		wantClientMutationID string
		wantOutcome          appwire.MutationOutcome
		wantDisposition      appwire.RetryDisposition
	}{
		{
			name: "uncorrelated relayed failure is wrapped blocked-unknown",
			retryErr: appwire.WireError{
				Code:    appwire.CodeInternalError,
				Message: "relayed retry failure names no mutation",
				Data:    map[string]any{"evenerErrorInfo": string(appwire.ErrorInternal)},
			},
			wantClientMutationID: mutationID,
			wantOutcome:          appwire.MutationOutcomeUnknown,
			wantDisposition:      appwire.RetryDispositionBlocked,
		},
		{
			name: "already-correlated relayed refusal passes through",
			retryErr: appwire.WireError{
				Code:    appwire.CodeConflict,
				Message: "relayed refusal names this caller's mutation",
				Data: map[string]any{
					"evenerErrorInfo":  string(appwire.ErrorConflict),
					"clientMutationId": mutationID,
					"mutationOutcome":  string(appwire.MutationOutcomeNotAccepted),
					"retryDisposition": string(appwire.RetryDispositionNone),
				},
			},
			wantClientMutationID: mutationID,
			wantOutcome:          appwire.MutationOutcomeNotAccepted,
			wantDisposition:      appwire.RetryDispositionNone,
		},
		{
			name: "relayed target deletion naming no mutation is enriched",
			retryErr: appwire.WireError{
				Code:    appwire.CodeUnavailable,
				Message: "target has been deleted: local:th",
				Data: map[string]any{
					"evenerErrorInfo":  string(appwire.ErrorActionUnavailable),
					"mutationOutcome":  string(appwire.MutationOutcomeTargetDeleted),
					"retryDisposition": string(appwire.RetryDispositionNone),
				},
			},
			wantClientMutationID: mutationID,
			wantOutcome:          appwire.MutationOutcomeTargetDeleted,
			wantDisposition:      appwire.RetryDispositionNone,
		},
		{
			name: "relayed target deletion naming a different mutation is blocked-unknown",
			retryErr: appwire.WireError{
				Code:    appwire.CodeUnavailable,
				Message: "target has been deleted: local:th",
				Data: map[string]any{
					"evenerErrorInfo":  string(appwire.ErrorActionUnavailable),
					"clientMutationId": "some-other-mutation",
					"mutationOutcome":  string(appwire.MutationOutcomeTargetDeleted),
					"retryDisposition": string(appwire.RetryDispositionNone),
				},
			},
			wantClientMutationID: mutationID,
			wantOutcome:          appwire.MutationOutcomeUnknown,
			wantDisposition:      appwire.RetryDispositionBlocked,
		},
		{
			name: "relayed shape refusal passes through",
			retryErr: appwire.WireError{
				Code:    appwire.CodeInvalidParams,
				Message: "relayed shape refusal",
				Data:    map[string]any{"evenerErrorInfo": string(appwire.ErrorInvalidParams)},
			},
			wantClientMutationID: "",
			wantOutcome:          "",
			wantDisposition:      "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A nil correlateRetryFailure return means the error keeps its own
			// meaning; the finished failure is then the retry error itself.
			final := tc.retryErr
			if wrapped := correlateRetryFailure(mutationID, tc.retryErr); wrapped != nil {
				final = wrapped
			}
			var wire appwire.WireError
			if !errors.As(final, &wire) {
				t.Fatalf("finished error %T=%v, want a WireError", final, final)
			}
			data := relayedWireDataMap(t, wire.Data)
			gotID, _ := data["clientMutationId"].(string)
			gotOutcome, _ := data["mutationOutcome"].(string)
			gotDisposition, _ := data["retryDisposition"].(string)
			if gotID != tc.wantClientMutationID || gotOutcome != string(tc.wantOutcome) || gotDisposition != string(tc.wantDisposition) {
				t.Fatalf("clientMutationId=%q mutationOutcome=%q retryDisposition=%q, want %q/%q/%q (wire=%+v data=%#v)",
					gotID, gotOutcome, gotDisposition, tc.wantClientMutationID, tc.wantOutcome, tc.wantDisposition, wire, wire.Data)
			}
		})
	}
}
