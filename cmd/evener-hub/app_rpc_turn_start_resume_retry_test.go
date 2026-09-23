package hub

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
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

// TestCorrelateRetryFailureShapeRefusalMutationScope pins that a shape refusal
// is preserved only when it is this caller's to own. A true shape refusal that
// names NO mutation, or the caller's own, keeps its own meaning: resending the
// identical payload can never answer differently, and the client already
// recovers from an uncorrelated invalid-params. One that names a DIFFERENT
// mutation belongs to that other caller; the client dispatcher correlates by
// that id alone, so it would treat this refusal as unrelated to its record and
// skip its no-id recovery path, leaving this caller's mutation stuck
// submitting. It is wrapped as this caller's blocked-unknown instead.
func TestCorrelateRetryFailureShapeRefusalMutationScope(t *testing.T) {
	const mutationID = "mutation-shape-refusal-scope"

	cases := []struct {
		name                 string
		callerID             string
		refusalID            string
		wantPreserved        bool
		wantClientMutationID string
		wantOutcome          appwire.MutationOutcome
		wantDisposition      appwire.RetryDisposition
	}{
		{
			name:                 "unnamed shape refusal is preserved",
			callerID:             mutationID,
			wantPreserved:        true,
			wantClientMutationID: "",
		},
		{
			name:                 "shape refusal naming this caller's mutation is preserved",
			callerID:             mutationID,
			refusalID:            mutationID,
			wantPreserved:        true,
			wantClientMutationID: mutationID,
		},
		{
			name:                 "shape refusal naming a different mutation is blocked-unknown",
			callerID:             mutationID,
			refusalID:            "some-other-mutation",
			wantClientMutationID: mutationID,
			wantOutcome:          appwire.MutationOutcomeUnknown,
			wantDisposition:      appwire.RetryDispositionBlocked,
		},
		{
			// The daemon trims the id before naming it, so the hub holds a padded
			// caller id while the refusal names the normalized form. The
			// comparison is canonical (see mutationIDsMatch), so the refusal is
			// still recognized as this caller's rather than another caller's.
			name:                 "padded caller id recognizes the daemon-normalized shape refusal",
			callerID:             " " + mutationID + " ",
			refusalID:            mutationID,
			wantPreserved:        true,
			wantClientMutationID: mutationID,
		},
		{
			name:                 "padded caller id still blocks a genuinely different mutation",
			callerID:             " " + mutationID + " ",
			refusalID:            "some-other-mutation",
			wantClientMutationID: " " + mutationID + " ",
			wantOutcome:          appwire.MutationOutcomeUnknown,
			wantDisposition:      appwire.RetryDispositionBlocked,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			retryErr := appwire.WireError{
				Code:    appwire.CodeInvalidParams,
				Message: "input item type is not supported",
				Data: appwire.ErrorData{
					EvenerErrorInfo:  appwire.ErrorInvalidParams,
					ClientMutationID: tc.refusalID,
				},
			}
			// A nil correlateRetryFailure return means the refusal keeps its own
			// meaning; the finished failure is then the refusal itself.
			preserved := correlateRetryFailure(tc.callerID, retryErr) == nil
			if preserved != tc.wantPreserved {
				t.Fatalf("preserved=%v, want %v (refusal names %q)", preserved, tc.wantPreserved, tc.refusalID)
			}
			var final error = retryErr
			if !preserved {
				final = correlateRetryFailure(tc.callerID, retryErr)
			}
			var wire appwire.WireError
			if !errors.As(final, &wire) {
				t.Fatalf("finished error %T=%v, want a WireError", final, final)
			}
			data, ok := wire.Data.(appwire.ErrorData)
			if !ok {
				t.Fatalf("wire data %#v is not appwire.ErrorData", wire.Data)
			}
			if data.ClientMutationID != tc.wantClientMutationID ||
				data.MutationOutcome != tc.wantOutcome ||
				data.RetryDisposition != tc.wantDisposition {
				t.Fatalf("clientMutationId=%q mutationOutcome=%q retryDisposition=%q, want %q/%q/%q (wire=%+v)",
					data.ClientMutationID, data.MutationOutcome, data.RetryDisposition,
					tc.wantClientMutationID, tc.wantOutcome, tc.wantDisposition, wire)
			}
			if tc.wantPreserved && wire.Code != appwire.CodeInvalidParams {
				t.Fatalf("a preserved shape refusal must keep its invalid-params code: code=%d want %d (wire=%+v)",
					wire.Code, appwire.CodeInvalidParams, wire)
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
		callerID             string
		retryErr             error
		wantClientMutationID string
		wantOutcome          appwire.MutationOutcome
		wantDisposition      appwire.RetryDisposition
	}{
		{
			name:     "uncorrelated relayed failure is wrapped blocked-unknown",
			callerID: mutationID,
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
			name:     "already-correlated relayed refusal passes through",
			callerID: mutationID,
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
			name:     "relayed target deletion naming no mutation is enriched",
			callerID: mutationID,
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
			name:     "relayed target deletion naming a different mutation is blocked-unknown",
			callerID: mutationID,
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
			name:     "relayed shape refusal passes through",
			callerID: mutationID,
			retryErr: appwire.WireError{
				Code:    appwire.CodeInvalidParams,
				Message: "relayed shape refusal",
				Data:    map[string]any{"evenerErrorInfo": string(appwire.ErrorInvalidParams)},
			},
			wantClientMutationID: "",
			wantOutcome:          "",
			wantDisposition:      "",
		},
		{
			name:     "relayed shape refusal naming this caller's mutation passes through",
			callerID: mutationID,
			retryErr: appwire.WireError{
				Code:    appwire.CodeInvalidParams,
				Message: "relayed shape refusal for this caller",
				Data: map[string]any{
					"evenerErrorInfo":  string(appwire.ErrorInvalidParams),
					"clientMutationId": mutationID,
				},
			},
			wantClientMutationID: mutationID,
			wantOutcome:          "",
			wantDisposition:      "",
		},
		{
			name:     "relayed shape refusal naming a different mutation is blocked-unknown",
			callerID: mutationID,
			retryErr: appwire.WireError{
				Code:    appwire.CodeInvalidParams,
				Message: "relayed shape refusal for another caller",
				Data: map[string]any{
					"evenerErrorInfo":  string(appwire.ErrorInvalidParams),
					"clientMutationId": "some-other-mutation",
				},
			},
			wantClientMutationID: mutationID,
			wantOutcome:          appwire.MutationOutcomeUnknown,
			wantDisposition:      appwire.RetryDispositionBlocked,
		},
		{
			// A padded caller id against a relayed refusal that names the
			// daemon-normalized id: the canonical comparison recognizes it as this
			// caller's, so the refusal keeps its own outcome instead of being
			// wrapped blocked-unknown.
			name:     "padded caller id recognizes a relayed refusal naming the normalized id",
			callerID: " " + mutationID + " ",
			retryErr: appwire.WireError{
				Code:    appwire.CodeConflict,
				Message: "relayed refusal names the normalized caller mutation",
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
			name:     "padded caller id still blocks a relayed refusal naming a different mutation",
			callerID: " " + mutationID + " ",
			retryErr: appwire.WireError{
				Code:    appwire.CodeConflict,
				Message: "relayed refusal names another mutation",
				Data: map[string]any{
					"evenerErrorInfo":  string(appwire.ErrorConflict),
					"clientMutationId": "some-other-mutation",
					"mutationOutcome":  string(appwire.MutationOutcomeNotAccepted),
					"retryDisposition": string(appwire.RetryDispositionNone),
				},
			},
			wantClientMutationID: " " + mutationID + " ",
			wantOutcome:          appwire.MutationOutcomeUnknown,
			wantDisposition:      appwire.RetryDispositionBlocked,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A nil correlateRetryFailure return means the error keeps its own
			// meaning; the finished failure is then the retry error itself.
			final := tc.retryErr
			if wrapped := correlateRetryFailure(tc.callerID, tc.retryErr); wrapped != nil {
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

// TestAdoptCallerMutationIDRestoresVerbatimCallerID pins that an error naming
// the daemon's normalized id is handed back carrying the caller's own,
// verbatim id, so the response echoes exactly what the caller submitted.
//
// The hub does not trim the caller's clientMutationId; the daemon trims it at
// its own boundary before a refusal names it. Without this rewrite the known
// outcome would be recognized (see mutationIDsMatch) but the response would
// name the trimmed id, and the client -- which correlates its outbox record
// byte-for-byte against the id it submitted -- could never settle it. The
// rewrite is confined to an error that names the same mutation in canonical
// form: an unrelated id, an absent id, or a non-WireError is returned
// unchanged. Both decoded data shapes (typed ErrorData and map[string]any) are
// rewritten.
func TestAdoptCallerMutationIDRestoresVerbatimCallerID(t *testing.T) {
	const verbatim = " mutation-padded "
	const normalized = "mutation-padded"

	cases := []struct {
		name        string
		callerID    string
		err         error
		wantID      string
		wantOutcome appwire.MutationOutcome
	}{
		{
			name:     "typed data naming the normalized caller id adopts the verbatim id",
			callerID: verbatim,
			err: appwire.WireError{Code: appwire.CodeConflict, Message: "refused", Data: appwire.ErrorData{
				EvenerErrorInfo:  appwire.ErrorConflict,
				ClientMutationID: normalized,
				MutationOutcome:  appwire.MutationOutcomeNotAccepted,
				RetryDisposition: appwire.RetryDispositionNone,
			}},
			wantID:      verbatim,
			wantOutcome: appwire.MutationOutcomeNotAccepted,
		},
		{
			name:     "map-shaped data naming the normalized caller id adopts the verbatim id",
			callerID: verbatim,
			err: appwire.WireError{Code: appwire.CodeConflict, Message: "refused", Data: map[string]any{
				"evenerErrorInfo":  string(appwire.ErrorConflict),
				"clientMutationId": normalized,
				"mutationOutcome":  string(appwire.MutationOutcomeNotAccepted),
				"retryDisposition": string(appwire.RetryDispositionNone),
			}},
			wantID:      verbatim,
			wantOutcome: appwire.MutationOutcomeNotAccepted,
		},
		{
			name:     "an id already verbatim is left alone",
			callerID: verbatim,
			err: appwire.WireError{Code: appwire.CodeConflict, Message: "refused", Data: appwire.ErrorData{
				EvenerErrorInfo:  appwire.ErrorConflict,
				ClientMutationID: verbatim,
				MutationOutcome:  appwire.MutationOutcomeNotAccepted,
			}},
			wantID:      verbatim,
			wantOutcome: appwire.MutationOutcomeNotAccepted,
		},
		{
			name:     "another caller's id is not restamped",
			callerID: verbatim,
			err: appwire.WireError{Code: appwire.CodeConflict, Message: "refused", Data: appwire.ErrorData{
				EvenerErrorInfo:  appwire.ErrorConflict,
				ClientMutationID: "some-other-mutation",
			}},
			wantID: "some-other-mutation",
		},
		{
			name:     "an error naming no id is left alone",
			callerID: verbatim,
			err: appwire.WireError{Code: appwire.CodeConflict, Message: "refused", Data: appwire.ErrorData{
				EvenerErrorInfo: appwire.ErrorConflict,
			}},
			wantID: "",
		},
		{
			name:     "an empty caller id leaves the error alone",
			callerID: "",
			err: appwire.WireError{Code: appwire.CodeConflict, Message: "refused", Data: appwire.ErrorData{
				EvenerErrorInfo:  appwire.ErrorConflict,
				ClientMutationID: normalized,
			}},
			wantID: normalized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adopted := adoptCallerMutationID(tc.err, tc.callerID)
			wire, ok := wireErrorFromError(adopted)
			if !ok {
				t.Fatalf("adopted error %T=%v, want a WireError", adopted, adopted)
			}
			if got := clientMutationIDFromData(wire.Data); got != tc.wantID {
				t.Fatalf("adopted id=%q, want %q", got, tc.wantID)
			}
			// The rewrite may change only the id: the outcome the error already
			// carried must survive it.
			raw := relayedWireDataMap(t, wire.Data)
			gotOutcome, _ := raw["mutationOutcome"].(string)
			if gotOutcome != string(tc.wantOutcome) {
				t.Fatalf("mutationOutcome=%v, want %q: the rewrite must keep the error's own outcome (data=%#v)",
					raw["mutationOutcome"], tc.wantOutcome, raw)
			}
		})
	}

	plain := errors.New("plain failure")
	adoptedPlain := adoptCallerMutationID(plain, verbatim)
	if _, isWire := wireErrorFromError(adoptedPlain); isWire {
		t.Fatalf("a non-WireError must never be rewritten into a WireError, got %v", adoptedPlain)
	}
	if !errors.Is(adoptedPlain, plain) {
		t.Fatalf("a non-WireError must be returned unchanged, got %v", adoptedPlain)
	}
	if got := adoptCallerMutationID(nil, verbatim); got != nil {
		t.Fatalf("nil must stay nil, got %v", got)
	}
}

// TestHubRPCTurnStartRetryKeepsPaddedCallerIDForDaemonNamedFailure drives the
// padded-id normalization through the real turn/start retry path.
//
// The caller submits a padded clientMutationId (the hub does not trim it); the
// resumed daemon names the id it trimmed. The hub must recognize that refusal as
// this caller's -- not rewrite a known rejection as blocked-unknown -- and the
// response must still carry the caller's own, verbatim id, because the client's
// outbox correlates its record byte-for-byte. A refusal naming a genuinely
// different mutation is still blocked-unknown, still carrying the caller's id.
func TestHubRPCTurnStartRetryKeepsPaddedCallerIDForDaemonNamedFailure(t *testing.T) {
	const verbatim = " mutation-padded "
	const normalized = "mutation-padded"

	cases := []struct {
		name            string
		daemonNamedID   string
		wantOutcome     appwire.MutationOutcome
		wantDisposition appwire.RetryDisposition
	}{
		{
			name:            "refusal naming the normalized caller id keeps not-accepted",
			daemonNamedID:   normalized,
			wantOutcome:     appwire.MutationOutcomeNotAccepted,
			wantDisposition: appwire.RetryDispositionNone,
		},
		{
			name:            "refusal naming a different mutation is blocked-unknown",
			daemonNamedID:   "some-other-mutation",
			wantOutcome:     appwire.MutationOutcomeUnknown,
			wantDisposition: appwire.RetryDispositionBlocked,
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
					// The resumed daemon names the id it normalized at its boundary.
					return appwire.TurnStartResponse{}, appwire.MutationNotAccepted(tc.daemonNamedID, "the resumed session refused the retry")
				},
			}
			resolveTurnStartSource = func(*appsource.Registry, string, string) (appsource.Source, error) {
				return source, nil
			}
			resumeCalls := 0
			resumeTurnStartThread = func(context.Context, hubcore.WebConfig, *appsource.Registry, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
				resumeCalls++
				return appwire.ThreadResumeResponse{Thread: source.thread}, nil
			}

			server := newHubAppServer(hubcore.WebConfig{Past: past}, appsource.NewRegistry())
			_, err := exactDispatch(context.Background(), t, server, appwire.MethodTurnStart, appwire.TurnStartParams{
				Ref:              ref,
				ClientMutationID: verbatim,
				Input:            []appwire.InputItem{{Type: "text", Text: "do the thing"}},
			})
			if err == nil {
				t.Fatal("turn/start reported success although the retry was refused")
			}
			if startCalls != 2 {
				t.Fatalf("start calls=%d, want 2 (the original and the post-resume retry)", startCalls)
			}
			if resumeCalls != 1 {
				t.Fatalf("resume calls=%d, want 1", resumeCalls)
			}

			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("turn/start error %T=%v, want a WireError", err, err)
			}
			data, ok := wire.Data.(appwire.ErrorData)
			if !ok {
				t.Fatalf("wire data %#v is not appwire.ErrorData", wire.Data)
			}
			if data.ClientMutationID != verbatim {
				t.Fatalf("response names clientMutationId %q, want the caller's verbatim %q: the client cannot settle its record (wire=%+v)",
					data.ClientMutationID, verbatim, wire)
			}
			if data.MutationOutcome != tc.wantOutcome || data.RetryDisposition != tc.wantDisposition {
				t.Fatalf("mutationOutcome=%q retryDisposition=%q, want %q/%q (wire=%+v)",
					data.MutationOutcome, data.RetryDisposition, tc.wantOutcome, tc.wantDisposition, wire)
			}
		})
	}
}

// TestAdoptCallerMutationReceiptRestoresVerbatimCallerID pins that a SUCCESSFUL
// receipt naming the daemon's normalized id is handed back carrying the caller's
// own, verbatim id, and that nothing else about the receipt changes.
//
// The daemon trims the caller's clientMutationId at its boundary before minting
// the receipt, so a proud caller that submitted " mutation-padded " would
// otherwise get a receipt naming "mutation-padded" -- and the client correlates
// its outbox record byte-for-byte against the id it submitted, so the record
// would stay submitting even though the mutation applied.
func TestAdoptCallerMutationReceiptRestoresVerbatimCallerID(t *testing.T) {
	const verbatim = " mutation-padded "
	const normalized = "mutation-padded"

	base := appwire.MutationReceipt{
		Disposition:     appwire.MutationDispositionApplied,
		ThreadID:        "th_1",
		InstanceID:      "inst_1",
		TurnID:          "turn_1",
		QueueEntryIDs:   []string{"q1"},
		ProjectionState: appwire.MutationProjectionReflected,
	}

	cases := []struct {
		name      string
		callerID  string
		receiptID string
		wantID    string
	}{
		{
			name:      "padded caller id adopts the receipt's normalized id",
			callerID:  verbatim,
			receiptID: normalized,
			wantID:    verbatim,
		},
		{
			name:      "a matching non-padded id is unchanged",
			callerID:  normalized,
			receiptID: normalized,
			wantID:    normalized,
		},
		{
			name:      "a genuinely different mutation is left alone",
			callerID:  verbatim,
			receiptID: "some-other-mutation",
			wantID:    "some-other-mutation",
		},
		{
			name:      "ids differing inside the string are left alone",
			callerID:  "mutation padded",
			receiptID: "mutationpadded",
			wantID:    "mutationpadded",
		},
		{
			name:      "an unnamed receipt is left alone",
			callerID:  verbatim,
			receiptID: "",
			wantID:    "",
		},
		{
			name:      "an empty caller id leaves the receipt alone",
			callerID:  "",
			receiptID: normalized,
			wantID:    normalized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			receipt := base
			receipt.ClientMutationID = tc.receiptID
			adopted := adoptCallerMutationReceipt(receipt, tc.callerID)
			if adopted.ClientMutationID != tc.wantID {
				t.Fatalf("receipt clientMutationId=%q, want %q", adopted.ClientMutationID, tc.wantID)
			}
			// Only the id may change: every other field carries the receipt's own
			// meaning.
			adopted.ClientMutationID = tc.receiptID
			if adopted.Disposition != base.Disposition ||
				adopted.ThreadID != base.ThreadID ||
				adopted.InstanceID != base.InstanceID ||
				adopted.TurnID != base.TurnID ||
				adopted.ProjectionState != base.ProjectionState ||
				!slices.Equal(adopted.QueueEntryIDs, base.QueueEntryIDs) {
				t.Fatalf("adopted receipt = %+v, want only the id changed from %+v", adopted, base)
			}
		})
	}
}

// TestAdoptResponseClientMutationIDCoversReceiptShapes pins that every response
// shape the hub can return with a mutation receipt adopts the caller's verbatim
// id, that the receipt's other fields survive, and that responses carrying no
// receipt, failing responses, and an empty caller id all pass through untouched.
//
// The extractor below is deliberately independent of the production type switch,
// so dropping a case there makes this test fail.
func TestAdoptResponseClientMutationIDCoversReceiptShapes(t *testing.T) {
	const verbatim = " mutation-padded "
	const normalized = "mutation-padded"

	receipt := appwire.MutationReceipt{
		ClientMutationID: normalized,
		Disposition:      appwire.MutationDispositionApplied,
		ThreadID:         "th_1",
		TurnID:           "turn_1",
		ProjectionState:  appwire.MutationProjectionReflected,
	}

	cases := []struct {
		name string
		resp any
		get  func(any) appwire.MutationReceipt
	}{
		{"turn/start", appwire.TurnStartResponse{Receipt: receipt}, func(r any) appwire.MutationReceipt {
			return r.(appwire.TurnStartResponse).Receipt
		}},
		{"turn/steer", appwire.TurnSteerResponse{Receipt: receipt}, func(r any) appwire.MutationReceipt {
			return r.(appwire.TurnSteerResponse).Receipt
		}},
		{"turn/interrupt", appwire.TurnInterruptResponse{Receipt: receipt}, func(r any) appwire.MutationReceipt {
			return r.(appwire.TurnInterruptResponse).Receipt
		}},
		{"turn/queue", appwire.TurnQueueResponse{Receipt: receipt}, func(r any) appwire.MutationReceipt {
			return r.(appwire.TurnQueueResponse).Receipt
		}},
		{"turn/drainAsSteer", appwire.TurnDrainAsSteerResponse{Receipt: receipt}, func(r any) appwire.MutationReceipt {
			return r.(appwire.TurnDrainAsSteerResponse).Receipt
		}},
		{"turn/promoteQueuedAsSteer", appwire.TurnPromoteQueuedAsSteerResponse{Receipt: receipt}, func(r any) appwire.MutationReceipt {
			return r.(appwire.TurnPromoteQueuedAsSteerResponse).Receipt
		}},
		{"turn/cancelQueued", appwire.TurnCancelQueuedResponse{Receipt: receipt}, func(r any) appwire.MutationReceipt {
			return r.(appwire.TurnCancelQueuedResponse).Receipt
		}},
		{"thread/clear", appwire.ThreadClearResponse{Receipt: receipt}, func(r any) appwire.MutationReceipt {
			return r.(appwire.ThreadClearResponse).Receipt
		}},
		{"notes/human/set", appwire.NotesHumanSetResponse{Receipt: receipt}, func(r any) appwire.MutationReceipt {
			return r.(appwire.NotesHumanSetResponse).Receipt
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adopted, err := adoptResponseClientMutationID[any](tc.resp, nil, verbatim)
			if err != nil {
				t.Fatalf("adopt response %s returned error %v", tc.name, err)
			}
			got := tc.get(adopted)
			if got.ClientMutationID != verbatim {
				t.Fatalf("receipt clientMutationId=%q, want the caller's verbatim %q", got.ClientMutationID, verbatim)
			}
			got.ClientMutationID = normalized
			if !reflect.DeepEqual(got, receipt) {
				t.Fatalf("adopted receipt = %+v, want only the id changed from %+v", got, receipt)
			}
		})
	}

	// Responses with no clientMutationId have nothing to adopt and pass through.
	for _, tc := range []struct {
		name string
		resp any
	}{
		{"goal/set", appwire.GoalSetResponse{Started: true}},
		{"urls/remove", appwire.UrlsRemoveResponse{}},
		{"empty", appwire.EmptyResponse{}},
	} {
		t.Run(tc.name+" carries no receipt", func(t *testing.T) {
			adopted, err := adoptResponseClientMutationID[any](tc.resp, nil, verbatim)
			if err != nil {
				t.Fatalf("adopt response %s returned error %v", tc.name, err)
			}
			if adopted != tc.resp {
				t.Fatalf("adopted %#v, want the response unchanged (%#v)", adopted, tc.resp)
			}
		})
	}

	failing := appwire.TurnStartResponse{Receipt: receipt}
	adopted, err := adoptResponseClientMutationID[any](failing, errors.New("mutation failed"), verbatim)
	if err == nil {
		t.Fatal("adopt must hand the failure back")
	}
	if got := adopted.(appwire.TurnStartResponse).Receipt.ClientMutationID; got != normalized {
		t.Fatalf("a failing response must keep the receipt as the daemon minted it: got %q, want %q", got, normalized)
	}

	unnamed, err := adoptResponseClientMutationID[any](appwire.TurnStartResponse{Receipt: receipt}, nil, "")
	if err != nil {
		t.Fatalf("adopt with an empty caller id returned error %v", err)
	}
	if got := unnamed.(appwire.TurnStartResponse).Receipt.ClientMutationID; got != normalized {
		t.Fatalf("an empty caller id must leave the receipt alone: got %q, want %q", got, normalized)
	}
}

// TestAdoptResponseClientMutationIDAdoptsFailureID pins that the response
// adapter adopts the caller's verbatim id onto a FAILURE as well as onto a
// receipt, and that it leaves alone every failure adoptCallerMutationID must not
// touch.
//
// A direct (non-retry) refusal never passes through the resume-retry adoption,
// so without this the daemon's normalized id would come back to a padded caller
// and its outbox record would stay submitting with nothing to settle it -- the
// same failure the receipt path and the retry paths already prevent.
func TestAdoptResponseClientMutationIDAdoptsFailureID(t *testing.T) {
	const verbatim = " mutation-padded "
	const normalized = "mutation-padded"

	refusal := func(id string) error {
		return appwire.MutationNotAccepted(id, "the daemon refused the mutation")
	}

	cases := []struct {
		name        string
		callerID    string
		err         error
		wantID      string
		wantOutcome appwire.MutationOutcome
	}{
		{
			name:        "a daemon refusal naming the normalized caller id adopts the verbatim id",
			callerID:    verbatim,
			err:         refusal(normalized),
			wantID:      verbatim,
			wantOutcome: appwire.MutationOutcomeNotAccepted,
		},
		{
			name:        "a hub-minted refusal already naming the caller is unchanged",
			callerID:    verbatim,
			err:         refusal(verbatim),
			wantID:      verbatim,
			wantOutcome: appwire.MutationOutcomeNotAccepted,
		},
		{
			name:        "a refusal naming a different mutation is left alone",
			callerID:    verbatim,
			err:         refusal("some-other-mutation"),
			wantID:      "some-other-mutation",
			wantOutcome: appwire.MutationOutcomeNotAccepted,
		},
		{
			name:     "a refusal naming no mutation is left alone",
			callerID: verbatim,
			err:      appwire.Unavailable("no mutation id here"),
			wantID:   "",
		},
		{
			name:        "an empty caller id leaves the refusal alone",
			callerID:    "",
			err:         refusal(normalized),
			wantID:      normalized,
			wantOutcome: appwire.MutationOutcomeNotAccepted,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1"}}
			gotResp, gotErr := adoptResponseClientMutationID[appwire.TurnStartResponse](resp, tc.err, tc.callerID)
			if gotErr == nil {
				t.Fatal("the adapter dropped the failure")
			}
			if !reflect.DeepEqual(gotResp, resp) {
				t.Fatalf("response = %+v, want the given response unchanged (%+v)", gotResp, resp)
			}
			// Only the id in the error's data may change (adopt rebuilds the
			// WireError value, so identity is not preserved); the refusal's message
			// and code must survive.
			if gotErr.Error() != tc.err.Error() {
				t.Fatalf("adopted error %q, want the failure's own message %q", gotErr, tc.err)
			}
			var wire appwire.WireError
			if !errors.As(gotErr, &wire) {
				t.Fatalf("adopted error %T=%v, want a WireError", gotErr, gotErr)
			}
			data, ok := wire.Data.(appwire.ErrorData)
			if !ok {
				t.Fatalf("wire data %#v is not appwire.ErrorData", wire.Data)
			}
			if data.ClientMutationID != tc.wantID {
				t.Fatalf("error names clientMutationId %q, want %q", data.ClientMutationID, tc.wantID)
			}
			if data.MutationOutcome != tc.wantOutcome {
				t.Fatalf("error outcome=%q, want %q: only the id may change", data.MutationOutcome, tc.wantOutcome)
			}
		})
	}

	plain := errors.New("plain failure")
	if _, gotErr := adoptResponseClientMutationID[appwire.TurnStartResponse](appwire.TurnStartResponse{}, plain, verbatim); !errors.Is(gotErr, plain) {
		t.Fatalf("a non-WireError must be returned unchanged, got %v", gotErr)
	}
}

// TestAdoptResponseClientMutationIDStampsUnnamedTargetDeletion pins that a direct
// mutation path's ID-LESS target deletion comes back naming the caller's own id
// -- with the deletion's outcome intact -- while a deletion that names a
// DIFFERENT mutation is left exactly as it is and an ID-LESS error that is not a
// deletion never acquires an id.
//
// An id-less deletion is the shape a remote hub relays: the preflight thread/read
// that discovered the deleted target names no mutation, so without the stamp the
// client cannot correlate the failure and the record stays submitting instead of
// being reconciled as orphaned.
func TestAdoptResponseClientMutationIDStampsUnnamedTargetDeletion(t *testing.T) {
	const verbatim = " mutation-padded "
	const normalized = "mutation-padded"

	deletion := func(data any) error {
		return appwire.WireError{
			Code:    appwire.CodeUnavailable,
			Message: "target has been deleted: local:th",
			Data:    data,
		}
	}

	cases := []struct {
		name            string
		callerID        string
		err             error
		wantID          string
		wantOutcome     appwire.MutationOutcome
		wantDisposition appwire.RetryDisposition
	}{
		{
			name:     "an id-less typed deletion is stamped with the caller's verbatim id",
			callerID: verbatim,
			err: deletion(appwire.ErrorData{
				EvenerErrorInfo:  appwire.ErrorActionUnavailable,
				MutationOutcome:  appwire.MutationOutcomeTargetDeleted,
				RetryDisposition: appwire.RetryDispositionNone,
			}),
			wantID:          verbatim,
			wantOutcome:     appwire.MutationOutcomeTargetDeleted,
			wantDisposition: appwire.RetryDispositionNone,
		},
		{
			name:     "an id-less relayed (map-shaped) deletion is stamped too",
			callerID: verbatim,
			err: deletion(map[string]any{
				"evenerErrorInfo":  string(appwire.ErrorActionUnavailable),
				"mutationOutcome":  string(appwire.MutationOutcomeTargetDeleted),
				"retryDisposition": string(appwire.RetryDispositionNone),
			}),
			wantID:          verbatim,
			wantOutcome:     appwire.MutationOutcomeTargetDeleted,
			wantDisposition: appwire.RetryDispositionNone,
		},
		{
			name:     "a deletion naming a different mutation is untouched",
			callerID: verbatim,
			err: deletion(appwire.ErrorData{
				EvenerErrorInfo:  appwire.ErrorActionUnavailable,
				ClientMutationID: "some-other-mutation",
				MutationOutcome:  appwire.MutationOutcomeTargetDeleted,
				RetryDisposition: appwire.RetryDispositionNone,
			}),
			wantID:          "some-other-mutation",
			wantOutcome:     appwire.MutationOutcomeTargetDeleted,
			wantDisposition: appwire.RetryDispositionNone,
		},
		{
			name:     "a deletion naming the caller's normalized id adopts the verbatim id",
			callerID: verbatim,
			err: deletion(appwire.ErrorData{
				EvenerErrorInfo:  appwire.ErrorActionUnavailable,
				ClientMutationID: normalized,
				MutationOutcome:  appwire.MutationOutcomeTargetDeleted,
				RetryDisposition: appwire.RetryDispositionNone,
			}),
			wantID:          verbatim,
			wantOutcome:     appwire.MutationOutcomeTargetDeleted,
			wantDisposition: appwire.RetryDispositionNone,
		},
		{
			name:     "an id-less non-deletion error is left alone",
			callerID: verbatim,
			err: appwire.WireError{
				Code:    appwire.CodeInternalError,
				Message: "the daemon failed without naming a mutation",
				Data:    appwire.ErrorData{EvenerErrorInfo: appwire.ErrorInternal},
			},
			wantID: "",
		},
		{
			name:     "an empty caller id leaves the deletion alone",
			callerID: "",
			err: deletion(appwire.ErrorData{
				EvenerErrorInfo:  appwire.ErrorActionUnavailable,
				MutationOutcome:  appwire.MutationOutcomeTargetDeleted,
				RetryDisposition: appwire.RetryDispositionNone,
			}),
			wantID:          "",
			wantOutcome:     appwire.MutationOutcomeTargetDeleted,
			wantDisposition: appwire.RetryDispositionNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1"}}
			gotResp, gotErr := adoptResponseClientMutationID[appwire.TurnStartResponse](resp, tc.err, tc.callerID)
			if gotErr == nil {
				t.Fatal("the adapter dropped the failure")
			}
			if !reflect.DeepEqual(gotResp, resp) {
				t.Fatalf("response = %+v, want the given response unchanged (%+v)", gotResp, resp)
			}
			var wire appwire.WireError
			if !errors.As(gotErr, &wire) {
				t.Fatalf("adopted error %T=%v, want a WireError", gotErr, gotErr)
			}
			data := relayedWireDataMap(t, wire.Data)
			if gotID, _ := data["clientMutationId"].(string); gotID != tc.wantID {
				t.Fatalf("error names clientMutationId %q, want %q (wire=%+v)", gotID, tc.wantID, wire)
			}
			if gotOutcome, _ := data["mutationOutcome"].(string); gotOutcome != string(tc.wantOutcome) {
				t.Fatalf("mutationOutcome=%q, want %q: only the id may change (wire=%+v)", gotOutcome, tc.wantOutcome, wire)
			}
			if gotDisposition, _ := data["retryDisposition"].(string); gotDisposition != string(tc.wantDisposition) {
				t.Fatalf("retryDisposition=%q, want %q: only the id may change (wire=%+v)", gotDisposition, tc.wantDisposition, wire)
			}
		})
	}
}

// deletionPreflightSource is a scripted source whose preflight thread read fails
// with a target-deletion error, so a direct turn/start can be driven into the
// relay's own preflight deletion branch (app_relay.go's startTurn) without any
// resume or retry in play.
type deletionPreflightSource struct {
	*scriptedAppSource
	readErr error
}

func (s *deletionPreflightSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{}, s.readErr
}

// TestHubRPCTurnStartPreflightDeletionStampsCallerID drives the ID-LESS
// target-deletion shape through the real turn/start direct path: the relay's
// preflight thread read discovers a deleted target and names no mutation, so the
// caller must get that deletion back carrying its own id and the deletion's
// outcome. A deletion naming a different mutation is handed back untouched.
func TestHubRPCTurnStartPreflightDeletionStampsCallerID(t *testing.T) {
	const verbatim = " mutation-padded "

	cases := []struct {
		name    string
		readErr error
		wantID  string
	}{
		{
			name: "an id-less preflight deletion is stamped with the caller's id",
			readErr: appwire.WireError{
				Code:    appwire.CodeUnavailable,
				Message: "target has been deleted: local:th",
				Data: appwire.ErrorData{
					EvenerErrorInfo:  appwire.ErrorActionUnavailable,
					MutationOutcome:  appwire.MutationOutcomeTargetDeleted,
					RetryDisposition: appwire.RetryDispositionNone,
				},
			},
			wantID: verbatim,
		},
		{
			name: "a preflight deletion naming a different mutation is untouched",
			readErr: appwire.WireError{
				Code:    appwire.CodeUnavailable,
				Message: "target has been deleted: local:th",
				Data: appwire.ErrorData{
					EvenerErrorInfo:  appwire.ErrorActionUnavailable,
					ClientMutationID: "some-other-mutation",
					MutationOutcome:  appwire.MutationOutcomeTargetDeleted,
					RetryDisposition: appwire.RetryDispositionNone,
				},
			},
			wantID: "some-other-mutation",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldResolve, oldResume := resolveTurnStartSource, resumeTurnStartThread
			t.Cleanup(func() {
				resolveTurnStartSource, resumeTurnStartThread = oldResolve, oldResume
			})

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
			source := &deletionPreflightSource{
				scriptedAppSource: &scriptedAppSource{
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
						return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1"}}, nil
					},
				},
				readErr: tc.readErr,
			}
			resolveTurnStartSource = func(*appsource.Registry, string, string) (appsource.Source, error) {
				return source, nil
			}
			resumeCalls := 0
			resumeTurnStartThread = func(context.Context, hubcore.WebConfig, *appsource.Registry, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
				resumeCalls++
				return appwire.ThreadResumeResponse{Thread: source.thread}, nil
			}

			server := newHubAppServer(hubcore.WebConfig{Past: past}, appsource.NewRegistry())
			_, err := exactDispatch(context.Background(), t, server, appwire.MethodTurnStart, appwire.TurnStartParams{
				Ref:              ref,
				ClientMutationID: verbatim,
				Input:            []appwire.InputItem{{Type: "text", Text: "do the thing"}},
			})
			if err == nil {
				t.Fatal("turn/start reported success although the target is deleted")
			}
			if startCalls != 0 {
				t.Fatalf("start calls=%d, want 0 (the preflight deletion refused before the send)", startCalls)
			}
			if resumeCalls != 0 {
				t.Fatalf("resume calls=%d, want 0 (a deletion is not a session-unavailable failure)", resumeCalls)
			}

			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("turn/start error %T=%v, want a WireError", err, err)
			}
			data := relayedWireDataMap(t, wire.Data)
			if gotID, _ := data["clientMutationId"].(string); gotID != tc.wantID {
				t.Fatalf("deletion names clientMutationId %q, want %q: the client cannot reconcile the record (wire=%+v)", gotID, tc.wantID, wire)
			}
			if gotOutcome, _ := data["mutationOutcome"].(string); gotOutcome != string(appwire.MutationOutcomeTargetDeleted) {
				t.Fatalf("mutationOutcome=%q, want %q (wire=%+v)", gotOutcome, appwire.MutationOutcomeTargetDeleted, wire)
			}
			if gotDisposition, _ := data["retryDisposition"].(string); gotDisposition != string(appwire.RetryDispositionNone) {
				t.Fatalf("retryDisposition=%q, want %q (wire=%+v)", gotDisposition, appwire.RetryDispositionNone, wire)
			}
		})
	}
}

// TestMutationResumeFailureError pins the rule that decides what a mutation
// reports when the resume it needed failed: a target deletion keeps its own
// outcome and is named for this caller (an ID-LESS one stamped, one named in the
// daemon's normalized form rewritten to the caller's own), while every other
// resume failure keeps the blocked-unknown envelope untouched.
func TestMutationResumeFailureError(t *testing.T) {
	const verbatim = " mutation-padded "
	const normalized = "mutation-padded"

	target := identifier.MustNewSessionID()
	ref := localAppRef(target)

	deletion := func(id string) error {
		return appwire.WireError{
			Code:    appwire.CodeUnavailable,
			Message: "target has been deleted: local:th",
			Data: appwire.ErrorData{
				EvenerErrorInfo:  appwire.ErrorActionUnavailable,
				ClientMutationID: id,
				MutationOutcome:  appwire.MutationOutcomeTargetDeleted,
				RetryDisposition: appwire.RetryDispositionNone,
			},
		}
	}

	// A store with nothing recorded stands for the sibling-alias case: the resume
	// reported a deletion, but not of the target this mutation addressed.
	storeFor := func(recordTarget bool) hubcore.WebConfig {
		store, err := hubcore.NewDeletionStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if recordTarget {
			if _, err := store.Begin("project-fence-0123456789", []hubcore.DeletionTarget{{
				Ref:      ref,
				ThreadID: target,
			}}); err != nil {
				t.Fatal(err)
			}
		}
		return hubcore.WebConfig{DeletionStore: store}
	}

	cases := []struct {
		name            string
		callerID        string
		resumeErr       error
		recordTarget    bool
		nilStore        bool
		wantID          string
		wantOutcome     appwire.MutationOutcome
		wantDisposition appwire.RetryDisposition
	}{
		{
			name:            "a deletion of the requested target keeps the deletion outcome, named for the caller",
			callerID:        verbatim,
			resumeErr:       deletion(""),
			recordTarget:    true,
			wantID:          verbatim,
			wantOutcome:     appwire.MutationOutcomeTargetDeleted,
			wantDisposition: appwire.RetryDispositionNone,
		},
		{
			name:            "a deletion of the requested target is named with the caller's verbatim id even when the resume named the normalized form",
			callerID:        verbatim,
			resumeErr:       deletion(normalized),
			recordTarget:    true,
			wantID:          verbatim,
			wantOutcome:     appwire.MutationOutcomeTargetDeleted,
			wantDisposition: appwire.RetryDispositionNone,
		},
		{
			name:            "a deletion of the requested target is settled for this caller whatever the resume's error named",
			callerID:        verbatim,
			resumeErr:       deletion("some-other-mutation"),
			recordTarget:    true,
			wantID:          verbatim,
			wantOutcome:     appwire.MutationOutcomeTargetDeleted,
			wantDisposition: appwire.RetryDispositionNone,
		},
		{
			name:            "a sibling-alias deletion stays blocked-unknown",
			callerID:        verbatim,
			resumeErr:       deletion(""),
			wantID:          verbatim,
			wantOutcome:     appwire.MutationOutcomeUnknown,
			wantDisposition: appwire.RetryDispositionBlocked,
		},
		{
			name:            "a deletion with no store to confirm the requested target stays blocked-unknown",
			callerID:        verbatim,
			resumeErr:       deletion(""),
			nilStore:        true,
			wantID:          verbatim,
			wantOutcome:     appwire.MutationOutcomeUnknown,
			wantDisposition: appwire.RetryDispositionBlocked,
		},
		{
			name:            "an ordinary resume failure stays blocked-unknown",
			callerID:        verbatim,
			resumeErr:       appwire.InternalError("resume failed for an unrelated reason"),
			nilStore:        true,
			wantID:          verbatim,
			wantOutcome:     appwire.MutationOutcomeUnknown,
			wantDisposition: appwire.RetryDispositionBlocked,
		},
		{
			name:      "an empty caller id hands the resume failure back unchanged",
			callerID:  "",
			resumeErr: appwire.InternalError("resume failed for an unrelated reason"),
			nilStore:  true,
			wantID:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := hubcore.WebConfig{}
			if !tc.nilStore {
				cfg = storeFor(tc.recordTarget)
			}
			got := mutationResumeFailureError(cfg, ref, "", tc.callerID, tc.resumeErr)
			if tc.callerID == "" {
				if !errors.Is(got, tc.resumeErr) {
					t.Fatalf("an empty caller id must return the failure unchanged, got %v", got)
				}
				return
			}
			var wire appwire.WireError
			if !errors.As(got, &wire) {
				t.Fatalf("resume failure error %T=%v, want a WireError", got, got)
			}
			data := relayedWireDataMap(t, wire.Data)
			if gotID, _ := data["clientMutationId"].(string); gotID != tc.wantID {
				t.Fatalf("error names clientMutationId %q, want %q (wire=%+v)", gotID, tc.wantID, wire)
			}
			if gotOutcome, _ := data["mutationOutcome"].(string); gotOutcome != string(tc.wantOutcome) {
				t.Fatalf("mutationOutcome=%q, want %q (wire=%+v)", gotOutcome, tc.wantOutcome, wire)
			}
			if gotDisposition, _ := data["retryDisposition"].(string); gotDisposition != string(tc.wantDisposition) {
				t.Fatalf("retryDisposition=%q, want %q (wire=%+v)", gotDisposition, tc.wantDisposition, wire)
			}
		})
	}
}

// TestHubRPCTurnStartResumeFailureKeepsDeletionOutcome drives BOTH of
// turn/start's own resume-failure sites: the post-attempt resume (the source's
// startTurn fails session-unavailable, so the handler resumes after the attempt)
// and the pre-dispatch resume (source resolution fails, so the handler resumes
// before any attempt reached a source). A resume that fails because the target
// was deleted must come back as that deletion, named for this caller; an ordinary
// resume failure keeps blocked-unknown, and so does a deletion naming a different
// mutation.
func TestHubRPCTurnStartResumeFailureKeepsDeletionOutcome(t *testing.T) {
	const verbatim = " mutation-padded "
	const normalized = "mutation-padded"

	deletion := func(id string) error {
		return appwire.WireError{
			Code:    appwire.CodeUnavailable,
			Message: "target has been deleted: local:th",
			Data: appwire.ErrorData{
				EvenerErrorInfo:  appwire.ErrorActionUnavailable,
				ClientMutationID: id,
				MutationOutcome:  appwire.MutationOutcomeTargetDeleted,
				RetryDisposition: appwire.RetryDispositionNone,
			},
		}
	}

	cases := []struct {
		name string
		// resolveErr, when set, fails source resolution so the handler resumes
		// from the pre-dispatch site; otherwise the source's startTurn fails
		// session-unavailable and the handler resumes after the attempt.
		resolveErr error
		resumeErr  error
		// recordTarget records the caller's requested target as deleted. Without
		// it the resume's deletion (if any) names a sibling alias of the ownership
		// group, which is not this caller's to reconcile.
		recordTarget    bool
		wantID          string
		wantOutcome     appwire.MutationOutcome
		wantDisposition appwire.RetryDisposition
		wantStartCalls  int
	}{
		{
			name:            "post-attempt resume failing on an id-less deletion keeps the deletion",
			resumeErr:       deletion(""),
			recordTarget:    true,
			wantID:          verbatim,
			wantOutcome:     appwire.MutationOutcomeTargetDeleted,
			wantDisposition: appwire.RetryDispositionNone,
			wantStartCalls:  1,
		},
		{
			name:            "pre-dispatch resume failing on an id-less deletion keeps the deletion",
			resolveErr:      appwire.SessionUnavailable("session has exited"),
			resumeErr:       deletion(""),
			recordTarget:    true,
			wantID:          verbatim,
			wantOutcome:     appwire.MutationOutcomeTargetDeleted,
			wantDisposition: appwire.RetryDispositionNone,
			wantStartCalls:  0,
		},
		{
			name:            "a resume deletion of the caller's target is named with the verbatim id even when the resume named the normalized form",
			resumeErr:       deletion(normalized),
			recordTarget:    true,
			wantID:          verbatim,
			wantOutcome:     appwire.MutationOutcomeTargetDeleted,
			wantDisposition: appwire.RetryDispositionNone,
			wantStartCalls:  1,
		},
		{
			name:            "a resume deletion of a sibling alias stays blocked-unknown",
			resumeErr:       deletion(""),
			recordTarget:    false,
			wantID:          verbatim,
			wantOutcome:     appwire.MutationOutcomeUnknown,
			wantDisposition: appwire.RetryDispositionBlocked,
			wantStartCalls:  1,
		},
		{
			name:            "an ordinary resume failure stays blocked-unknown",
			resumeErr:       appwire.InternalError("resume failed for an unrelated reason"),
			wantID:          verbatim,
			wantOutcome:     appwire.MutationOutcomeUnknown,
			wantDisposition: appwire.RetryDispositionBlocked,
			wantStartCalls:  1,
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
					return appwire.TurnStartResponse{}, appwire.SessionUnavailable("session has exited")
				},
			}
			resolveTurnStartSource = func(*appsource.Registry, string, string) (appsource.Source, error) {
				if tc.resolveErr != nil {
					return nil, tc.resolveErr
				}
				return source, nil
			}
			// The store decides whether the deletion the resume reported is the
			// caller's own target's: it starts empty (the attempt's own fence must
			// pass) and the resume records the deletion when this case wants it to
			// be the caller's (see mutationResumeFailureError).
			store, err := hubcore.NewDeletionStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			resumeCalls := 0
			resumeTurnStartThread = func(context.Context, hubcore.WebConfig, *appsource.Registry, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
				resumeCalls++
				// The target is deleted at the resume: after the attempt's own
				// deletion fence passed and before the resume (or, in production,
				// its fences) reads the store. With recordTarget the deletion is
				// the caller's own target's, so the caller reconciles it; without
				// it the resume's deletion stands for a sibling alias of the
				// ownership group, which is not this caller's to claim.
				if tc.recordTarget {
					if _, err := store.Begin("project-fence-0123456789", []hubcore.DeletionTarget{{
						Ref:      ref,
						ThreadID: sessionID,
					}}); err != nil {
						t.Fatalf("record the deletion: %v", err)
					}
				}
				return appwire.ThreadResumeResponse{}, tc.resumeErr
			}

			server := newHubAppServer(hubcore.WebConfig{Past: past, DeletionStore: store}, appsource.NewRegistry())
			_, err = exactDispatch(context.Background(), t, server, appwire.MethodTurnStart, appwire.TurnStartParams{
				Ref:              ref,
				ClientMutationID: verbatim,
				Input:            []appwire.InputItem{{Type: "text", Text: "do the thing"}},
			})
			if err == nil {
				t.Fatal("turn/start reported success although the resume failed")
			}
			if resumeCalls != 1 {
				t.Fatalf("resume calls=%d, want 1", resumeCalls)
			}
			if startCalls != tc.wantStartCalls {
				t.Fatalf("start calls=%d, want %d", startCalls, tc.wantStartCalls)
			}

			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("turn/start error %T=%v, want a WireError", err, err)
			}
			data := relayedWireDataMap(t, wire.Data)
			if gotID, _ := data["clientMutationId"].(string); gotID != tc.wantID {
				t.Fatalf("error names clientMutationId %q, want %q: the client cannot correlate it (wire=%+v)", gotID, tc.wantID, wire)
			}
			if gotOutcome, _ := data["mutationOutcome"].(string); gotOutcome != string(tc.wantOutcome) {
				t.Fatalf("mutationOutcome=%q, want %q (wire=%+v)", gotOutcome, tc.wantOutcome, wire)
			}
			if gotDisposition, _ := data["retryDisposition"].(string); gotDisposition != string(tc.wantDisposition) {
				t.Fatalf("retryDisposition=%q, want %q (wire=%+v)", gotDisposition, tc.wantDisposition, wire)
			}
		})
	}
}

// TestHubRPCTurnStartRetirementResumeFailureIsCorrelated pins what turn/start
// reports when its retirement resume fails.
//
// A daemon that refuses a mutation because it is retiring is resolved by
// resumeAfterConfirmedRetirement, whose failures name no mutation at all: the
// "retiring" lifecycle refusal (appwire.LifecycleUnavailable carries no
// clientMutationId), admission fences, ownership and roster errors. Returning
// one raw leaves the client's record submitting with nothing to settle it, so the
// path now goes through mutationResumeFailureError like every other resume site:
// an unnamed failure keeps the blocked-unknown envelope carrying the caller's own
// id, and a deletion is kept only when it is the caller's own target's (which the
// retirement path's own fence reports, named for the caller).
func TestHubRPCTurnStartRetirementResumeFailureIsCorrelated(t *testing.T) {
	const verbatim = " mutation-padded "

	cases := []struct {
		name string
		// recordTarget makes the caller's requested target the thing that is
		// deleted: the refusal then names a deletion of the caller's own target.
		recordTarget    bool
		wantID          string
		wantOutcome     appwire.MutationOutcome
		wantDisposition appwire.RetryDisposition
	}{
		{
			name:            "an unnamed retiring resume failure is correlated for the caller",
			wantID:          verbatim,
			wantOutcome:     appwire.MutationOutcomeUnknown,
			wantDisposition: appwire.RetryDispositionBlocked,
		},
		{
			name:            "a deletion of the caller's own target keeps the deletion outcome",
			recordTarget:    true,
			wantID:          verbatim,
			wantOutcome:     appwire.MutationOutcomeTargetDeleted,
			wantDisposition: appwire.RetryDispositionNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldResolve := resolveTurnStartSource
			t.Cleanup(func() { resolveTurnStartSource = oldResolve })

			// The ref must be one the hub knows, or the handler returns the first
			// failure unchanged.
			root := t.TempDir()
			workingDir := t.TempDir()
			stateDir := filepath.Join(root, "projects", "project-past-0000000000")
			sessionID := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
			past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
			if _, err := past.Rebuild(); err != nil {
				t.Fatal(err)
			}
			ref := "local:" + sessionID

			store, err := hubcore.NewDeletionStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}

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
					if tc.recordTarget {
						// The target is deleted after the attempt's own deletion
						// fence passed, so the retirement fence is what reports it.
						if _, err := store.Begin("project-fence-0123456789", []hubcore.DeletionTarget{{
							Ref:      ref,
							ThreadID: sessionID,
						}}); err != nil {
							t.Fatalf("record the deletion: %v", err)
						}
					}
					// The owning daemon refuses because it is retiring.
					return appwire.TurnStartResponse{}, appwire.LifecycleUnavailable("retiring")
				},
			}
			resolveTurnStartSource = func(*appsource.Registry, string, string) (appsource.Source, error) {
				return source, nil
			}

			// No ResumeLocks: the retirement resume cannot proceed and fails with
			// its own unnamed "retiring" refusal (app_retirement_resume.go's
			// lifecycle guard), which is the shape under test.
			server := newHubAppServer(hubcore.WebConfig{Past: past, DeletionStore: store}, appsource.NewRegistry())
			_, err = exactDispatch(context.Background(), t, server, appwire.MethodTurnStart, appwire.TurnStartParams{
				Ref:              ref,
				ClientMutationID: verbatim,
				Input:            []appwire.InputItem{{Type: "text", Text: "do the thing"}},
			})
			if err == nil {
				t.Fatal("turn/start reported success although the retirement resume failed")
			}
			if startCalls != 1 {
				t.Fatalf("start calls=%d, want 1 (a failed retirement resume does not retry)", startCalls)
			}

			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("turn/start error %T=%v, want a WireError", err, err)
			}
			data := relayedWireDataMap(t, wire.Data)
			if gotID, _ := data["clientMutationId"].(string); gotID != tc.wantID {
				t.Fatalf("error names clientMutationId %q, want %q: a retirement-resume failure must be correlatable (wire=%+v)", gotID, tc.wantID, wire)
			}
			if gotOutcome, _ := data["mutationOutcome"].(string); gotOutcome != string(tc.wantOutcome) {
				t.Fatalf("mutationOutcome=%q, want %q (wire=%+v)", gotOutcome, tc.wantOutcome, wire)
			}
			if gotDisposition, _ := data["retryDisposition"].(string); gotDisposition != string(tc.wantDisposition) {
				t.Fatalf("retryDisposition=%q, want %q (wire=%+v)", gotDisposition, tc.wantDisposition, wire)
			}
		})
	}
}

// TestHubRPCTurnStartDirectRefusalKeepsPaddedCallerID drives a DIRECT refusal
// (no resume in play) through turn/start with a padded caller id: the source's
// startTurn refuses naming the id the daemon normalized, and the caller must get
// that refusal back carrying its own verbatim id with its outcome intact.
func TestHubRPCTurnStartDirectRefusalKeepsPaddedCallerID(t *testing.T) {
	const verbatim = " mutation-padded "
	const normalized = "mutation-padded"

	oldResolve, oldResume := resolveTurnStartSource, resumeTurnStartThread
	t.Cleanup(func() {
		resolveTurnStartSource, resumeTurnStartThread = oldResolve, oldResume
	})

	// The ref must be one the hub knows, or the handler returns the first failure
	// unchanged before any resume decision.
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
			// A direct refusal: the daemon names the id it trimmed.
			return appwire.TurnStartResponse{}, appwire.MutationNotAccepted(normalized, "the session refused the send")
		},
	}
	resolveTurnStartSource = func(*appsource.Registry, string, string) (appsource.Source, error) {
		return source, nil
	}
	resumeCalls := 0
	resumeTurnStartThread = func(context.Context, hubcore.WebConfig, *appsource.Registry, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
		resumeCalls++
		return appwire.ThreadResumeResponse{Thread: source.thread}, nil
	}

	server := newHubAppServer(hubcore.WebConfig{Past: past}, appsource.NewRegistry())
	_, err := exactDispatch(context.Background(), t, server, appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref:              ref,
		ClientMutationID: verbatim,
		Input:            []appwire.InputItem{{Type: "text", Text: "do the thing"}},
	})
	if err == nil {
		t.Fatal("turn/start reported success although the source refused")
	}
	if startCalls != 1 {
		t.Fatalf("start calls=%d, want 1 (a direct refusal needs no retry)", startCalls)
	}
	if resumeCalls != 0 {
		t.Fatalf("resume calls=%d, want 0 (a refusal is not a session-unavailable failure)", resumeCalls)
	}

	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("turn/start error %T=%v, want a WireError", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok {
		t.Fatalf("wire data %#v is not appwire.ErrorData", wire.Data)
	}
	if data.ClientMutationID != verbatim {
		t.Fatalf("refusal names clientMutationId %q, want the caller's verbatim %q: the client cannot settle its record (wire=%+v)",
			data.ClientMutationID, verbatim, wire)
	}
	if data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.RetryDisposition != appwire.RetryDispositionNone {
		t.Fatalf("outcome=%q retryDisposition=%q, want notAccepted/none (wire=%+v)",
			data.MutationOutcome, data.RetryDisposition, wire)
	}
}

// urlsRefusalSource is a scripted source whose urls/remove returns a
// daemon-style refusal, so the hub's urls/remove path -- which returns a
// receipt-less response but can still fail with a refusal naming an id -- can be
// driven with a padded caller id.
type urlsRefusalSource struct {
	*scriptedAppSource
	urlsRemove func(appwire.UrlsRemoveParams) error
}

func (s *urlsRefusalSource) UrlsRemove(_ context.Context, params appwire.UrlsRemoveParams) (appwire.UrlsRemoveResponse, error) {
	return appwire.UrlsRemoveResponse{}, s.urlsRemove(params)
}

// TestHubRPCUrlsRemoveDirectRefusalKeepsPaddedCallerID covers the receipt-less
// half of the same rule: urls/remove returns no receipt at all, but a refusal it
// relays still names a mutation id, so the caller must get its own verbatim id
// back or its record never settles.
func TestHubRPCUrlsRemoveDirectRefusalKeepsPaddedCallerID(t *testing.T) {
	const verbatim = " mutation-padded "
	const normalized = "mutation-padded"

	source := &urlsRefusalSource{
		scriptedAppSource: &scriptedAppSource{
			id: "local",
			thread: appwire.Thread{
				ID:        "th_1",
				SessionID: "sess_1",
				Source:    "local",
				Evener: appwire.EvenerThread{
					Ref:          "local:th_1",
					Capabilities: appwire.ThreadCapabilities{SharedNotes: true},
				},
			},
		},
		urlsRemove: func(appwire.UrlsRemoveParams) error {
			// The daemon names the id it normalized at its own boundary.
			return appwire.MutationNotAccepted(normalized, "no such url")
		},
	}
	registry := appsource.NewRegistry()
	registry.Add(source)
	server := newHubAppServer(hubcore.WebConfig{}, registry)

	_, err := exactDispatch(context.Background(), t, server, appwire.MethodUrlsRemove, appwire.UrlsRemoveParams{
		Ref:                "local:th_1",
		ClientMutationID:   verbatim,
		ExpectedInstanceID: "sess_1",
		ID:                 "u1",
	})
	if err == nil {
		t.Fatal("urls/remove reported success although the source refused")
	}

	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("urls/remove error %T=%v, want a WireError", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok {
		t.Fatalf("wire data %#v is not appwire.ErrorData", wire.Data)
	}
	if data.ClientMutationID != verbatim {
		t.Fatalf("refusal names clientMutationId %q, want the caller's verbatim %q: the client cannot settle its record (wire=%+v)",
			data.ClientMutationID, verbatim, wire)
	}
	if data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.RetryDisposition != appwire.RetryDispositionNone {
		t.Fatalf("outcome=%q retryDisposition=%q, want notAccepted/none (wire=%+v)",
			data.MutationOutcome, data.RetryDisposition, wire)
	}
}

// TestHubRPCTurnStartResumedSuccessCarriesVerbatimPaddedCallerID drives the
// success path through the real turn/start resume flow: the caller submits a
// padded clientMutationId, the hub resumes the exited session, and the resumed
// daemon's receipt names the id the daemon trimmed. The response must carry the
// caller's own, verbatim id so its outbox record settles, and the receipt's
// other fields must be the daemon's.
func TestHubRPCTurnStartResumedSuccessCarriesVerbatimPaddedCallerID(t *testing.T) {
	const verbatim = " mutation-padded "
	const normalized = "mutation-padded"
	oldResolve, oldResume := resolveTurnStartSource, resumeTurnStartThread
	t.Cleanup(func() {
		resolveTurnStartSource, resumeTurnStartThread = oldResolve, oldResume
	})

	// The ref must be one the hub knows, or the handler returns the first failure
	// unchanged and never resumes at all.
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
			// The resumed daemon mints its receipt naming the id it normalized at
			// its own boundary.
			return appwire.TurnStartResponse{
				Turn: appwire.Turn{ID: "turn_1"},
				Receipt: appwire.MutationReceipt{
					ClientMutationID: normalized,
					Disposition:      appwire.MutationDispositionApplied,
					ThreadID:         sessionID,
					InstanceID:       sessionID,
					TurnID:           "turn_1",
					ProjectionState:  appwire.MutationProjectionReflected,
				},
			}, nil
		},
	}
	resolveTurnStartSource = func(*appsource.Registry, string, string) (appsource.Source, error) {
		return source, nil
	}
	resumeCalls := 0
	resumeTurnStartThread = func(context.Context, hubcore.WebConfig, *appsource.Registry, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
		resumeCalls++
		return appwire.ThreadResumeResponse{Thread: source.thread}, nil
	}

	server := newHubAppServer(hubcore.WebConfig{Past: past}, appsource.NewRegistry())
	raw, err := exactDispatch(context.Background(), t, server, appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref:              ref,
		ClientMutationID: verbatim,
		Input:            []appwire.InputItem{{Type: "text", Text: "do the thing"}},
	})
	if err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	if startCalls != 2 {
		t.Fatalf("start calls=%d, want 2 (the original and the post-resume retry)", startCalls)
	}
	if resumeCalls != 1 {
		t.Fatalf("resume calls=%d, want 1", resumeCalls)
	}

	resp, ok := raw.(appwire.TurnStartResponse)
	if !ok {
		t.Fatalf("turn/start response %T=%v, want appwire.TurnStartResponse", raw, raw)
	}
	if resp.Receipt.ClientMutationID != verbatim {
		t.Fatalf("receipt names clientMutationId %q, want the caller's verbatim %q: the client cannot settle its record (receipt=%+v)",
			resp.Receipt.ClientMutationID, verbatim, resp.Receipt)
	}
	if resp.Receipt.Disposition != appwire.MutationDispositionApplied ||
		resp.Receipt.ThreadID != sessionID ||
		resp.Receipt.InstanceID != sessionID ||
		resp.Receipt.TurnID != "turn_1" ||
		resp.Receipt.ProjectionState != appwire.MutationProjectionReflected {
		t.Fatalf("receipt = %+v, want the daemon's own fields with only the id adopted", resp.Receipt)
	}
}
