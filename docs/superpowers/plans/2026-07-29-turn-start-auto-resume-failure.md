# Turn Start Auto-Resume Failure Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make both `turn/start` auto-resume failure paths return a mutation-correlated blocked unknown outcome so the browser retains the input without retrying automatically.

**Architecture:** Keep strict persisted-state decoding unchanged. Normalize only failed Hub auto-resume attempts through one Hub-local error constructor; the existing frontend dispatcher consumes that wire contract and moves the matching outbox record to `blockedUnknown`.

**Tech Stack:** Go, AppWire JSON-RPC, deterministic `go test`

## Global Constraints

- Add no legacy snapshot migration, aliases, fallbacks, or compatibility decoder.
- Preserve the original `clientMutationId` and resume failure message.
- Report `mutationOutcome: unknown`, `retryDisposition: blocked`, and `cause: persistenceUnavailable`.
- Preserve the browser outbox payload for manual retry, copy, or export.
- Exercise the real AppWire router and Hub handler; replace only source resolution and resume with deterministic seams.
- Make the smallest coherent production change and do not change frontend production code.

---

### Task 1: Correlate Failed Turn-Start Auto-Resume

**Files:**
- Modify: `cmd/serf-hub/app_rpc_test.go`
- Modify: `cmd/serf-hub/app_rpc.go`

**Interfaces:**
- Consumes: `appwire.TurnStartParams.ClientMutationID`, `appwire.WireError`, and the existing `resolveTurnStartSource` and `resumeTurnStartThread` seams.
- Produces: `blockedUnknownMutationError(clientMutationID string, err error) error`, used by both failed auto-resume branches.

- [ ] **Step 1: Add the failing AppWire regression**

Add this test near the existing `TestHubRPCTurnStartResumesPastThread` coverage:

```go
func TestHubRPCTurnStartBlocksUnknownMutationWhenAutoResumeFails(t *testing.T) {
	oldResolve, oldResume := resolveTurnStartSource, resumeTurnStartThread
	t.Cleanup(func() {
		resolveTurnStartSource, resumeTurnStartThread = oldResolve, oldResume
	})

	const (
		mutationID   = "mutation-resume-failed"
		resumeMessage = "restore session: incompatible mutation snapshot"
	)
	tests := []struct {
		name           string
		configure      func(*int)
		wantStartCalls int
	}{
		{
			name: "initial source resolution",
			configure: func(_ *int) {
				resolveTurnStartSource = func(context.Context, hubcore.WebConfig, *appsource.Registry, string, string) (appsource.Source, error) {
					return nil, errors.New("source unavailable")
				}
			},
		},
		{
			name: "session unavailable while starting turn",
			configure: func(startCalls *int) {
				source := &scriptedAppSource{
					id: "local",
					startTurn: func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
						(*startCalls)++
						return appwire.TurnStartResponse{}, appwire.SessionUnavailable("daemon went away")
					},
				}
				resolveTurnStartSource = func(context.Context, hubcore.WebConfig, *appsource.Registry, string, string) (appsource.Source, error) {
					return source, nil
				}
			},
			wantStartCalls: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			startCalls := 0
			resumeCalls := 0
			tc.configure(&startCalls)
			resumeTurnStartThread = func(context.Context, hubcore.WebConfig, *appsource.Registry, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
				resumeCalls++
				return appwire.ThreadResumeResponse{}, appwire.HubLaunchError(resumeMessage)
			}

			server := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
			_, err := exactDispatch(context.Background(), t, server, appwire.MethodTurnStart, appwire.TurnStartParams{
				ClientMutationID: mutationID,
			})

			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("TurnStart error %T=%v, want WireError", err, err)
			}
			data, ok := wire.Data.(appwire.ErrorData)
			if !ok ||
				wire.Code != appwire.CodeInternalError ||
				wire.Message != resumeMessage ||
				data.SerfErrorInfo != appwire.ErrorMutationOutcomeUnknown ||
				data.ClientMutationID != mutationID ||
				data.MutationOutcome != appwire.MutationOutcomeUnknown ||
				data.RetryDisposition != appwire.RetryDispositionBlocked ||
				data.Cause != "persistenceUnavailable" {
				t.Fatalf("wire=%+v", wire)
			}
			if resumeCalls != 1 {
				t.Fatalf("resume calls=%d, want 1", resumeCalls)
			}
			if startCalls != tc.wantStartCalls {
				t.Fatalf("start calls=%d, want %d", startCalls, tc.wantStartCalls)
			}
		})
	}
}
```

- [ ] **Step 2: Run the regression and verify the real RED**

Run:

```bash
go test ./cmd/serf-hub -run '^TestHubRPCTurnStartBlocksUnknownMutationWhenAutoResumeFails$' -count=1
```

Expected: both subtests fail because the current handler returns the raw
`hubLaunch` error, which lacks the mutation identity and blocked unknown
outcome. A compile error does not count; correct the test until it fails on
those behavioral assertions.

- [ ] **Step 3: Add the minimal Hub-local error constructor**

Add this function beside the turn-start handler support code in
`cmd/serf-hub/app_rpc.go`:

```go
func blockedUnknownMutationError(clientMutationID string, err error) error {
	return appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: err.Error(),
		Data: appwire.ErrorData{
			SerfErrorInfo:    appwire.ErrorMutationOutcomeUnknown,
			ClientMutationID: clientMutationID,
			MutationOutcome:  appwire.MutationOutcomeUnknown,
			RetryDisposition: appwire.RetryDispositionBlocked,
			Cause:            "persistenceUnavailable",
		},
	}
}
```

In both branches where `resumeTurnStartThread` returns `resumeErr`, replace the
raw `resumeErr` return with:

```go
return appwire.TurnStartResponse{}, blockedUnknownMutationError(params.ClientMutationID, resumeErr)
```

- [ ] **Step 4: Format the changed Go files**

Run:

```bash
gofmt -w cmd/serf-hub/app_rpc.go cmd/serf-hub/app_rpc_test.go
```

- [ ] **Step 5: Run the focused regression and verify GREEN**

Run:

```bash
go test ./cmd/serf-hub -run '^TestHubRPCTurnStartBlocksUnknownMutationWhenAutoResumeFails$' -count=1
```

Expected: PASS for both subtests.

- [ ] **Step 6: Run adjacent auto-resume and mutation-contract tests**

Run:

```bash
go test ./cmd/serf-hub -run 'TestHubRPCTurnStart|TestHubRPCTurnMutationsForwardWithoutDynamicCapabilityGates' -count=1
go test ./cmd/serf-hub/internal/appsource -run 'Test.*Mutation' -count=1
```

Expected: PASS. Successful auto-resume, non-resumable errors, managed refs, and
existing unknown-outcome forwarding remain unchanged.

- [ ] **Step 7: Run package and repository verification**

Run:

```bash
go test ./cmd/serf-hub -count=1
go test ./... -count=1
git diff --check
```

Expected: all tests pass and `git diff --check` reports no errors. These are
deterministic plumbing tests and must not use provider credentials or live
network behavior.

- [ ] **Step 8: Review and commit the implementation**

Review:

```bash
git status --short
git diff -- cmd/serf-hub/app_rpc.go cmd/serf-hub/app_rpc_test.go
```

Confirm that removing either normalized return would make its corresponding
subtest fail, and that no persistence or frontend production file changed.
Then commit only the two Go files:

```bash
git add cmd/serf-hub/app_rpc.go cmd/serf-hub/app_rpc_test.go
git commit
```

Use a detailed commit message explaining the retry storm, the mutation
correlation contract, the preserved recovery payload, and the deliberate lack
of legacy-state compatibility.
