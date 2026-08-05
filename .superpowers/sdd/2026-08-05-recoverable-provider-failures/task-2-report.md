# Task 2 Report: Prove AppWire restores model switching after provider failure

Status: DONE_WITH_CONCERNS

## Files changed

- `cmd/serf/scripted_provider_test.go`
  - Added optional `errorSteps` scripted responses.
  - `Complete` appends each request once, consumes error steps, preserves the existing `steps` behavior, and applies response defaults only on successful responses.
  - Added `installServeScriptedProviders(t, adapters...)`; retained `installServeScriptedProvider` as a one-adapter wrapper.
- `cmd/serf/serve_model_switch_test.go`
  - Added `TestServeModelSwitch_ProviderFailureRestoresCapability`.
  - Exercises the real daemon and AppWire websocket path with `kimi-anthropic/k3` followed by `openai/gpt-5.6-sol`.
  - Uses structured notification milestones for failed turn, idle status with `ChangeModel`, and successful recovery turn.
  - Verifies `thread/read`, `thread/model/set`, provider/model request capture, and clean `/shutdown`.

No production server or AppWire files were changed.

## Commands and results

- `gofmt -w cmd/serf/scripted_provider_test.go cmd/serf/serve_model_switch_test.go` — PASS.
- `go test ./cmd/serf -run '^$' -count=1` — PASS; package compiled.
- `go test ./cmd/serf -run '^TestWaitForFileContentWaitsForNonEmptyContent$' -count=1` — PASS.
- `go test ./cmd/serf -run '^TestServeModelSwitch_ProviderFailureRestoresCapability$' -count=1` — could not reach the test; daemon startup failed with `listen 127.0.0.1:0: bind: operation not permitted` in this sandbox.
- `go test ./cmd/serf -run '^TestServeModelSwitch_' -count=1` — could not reach either daemon test for the same sandbox network restriction (`bind: operation not permitted`).
- `git diff --check` — PASS before commit.
- `git status --short` after commit — clean.

The required old-behavior mutation run was not performed because Task 1 behavior was not altered, and the sandbox cannot bind the daemon's test listener.

## Commit

`72089db0eca164b818949c3e3e5c01d22d888826` — `test: cover model switch after provider failure`

The commit command emitted an initial `packed-refs.lock: Operation not permitted` warning from the outer repository path but completed successfully in this worktree; the resulting commit and clean status were verified.

## Self-review

- The change is limited to the two planned test files.
- Existing scripted-provider `steps` callers remain supported.
- Error responses return before provider/model/finish defaults are applied.
- The regression uses channels and structured AppWire notifications rather than sleeps or polling for turn state.
- The test asserts failed-turn ordering, idle capability, snapshot state, model switching, successful completion, and exact provider/model routing.

## Concerns

The real daemon regression could not execute in this environment because network socket binding is denied by the sandbox. Compilation and non-network focused verification passed. Re-run `go test ./cmd/serf -run '^TestServeModelSwitch_' -count=1` in an environment permitting loopback binds before treating the integration proof as fully runtime-verified.

## Fix review findings

- Added `waitServeMilestone`, selecting between each structured milestone channel and `ctx.Done()`; replaced failed-turn, idle-capability, and successful-turn unbounded receives. No sleeps or polling were added.
- Added assertions that the initial Kimi request uses provider `kimi-anthropic` and model `k3`.
- Added a clear `t.Fatal` guard requiring at least one adapter in `installServeScriptedProviders`.
- `gofmt -w cmd/serf/scripted_provider_test.go cmd/serf/serve_model_switch_test.go` — PASS.
- `go test ./cmd/serf -run '^$' -count=1` — PASS; package compiled.
- `go test ./cmd/serf -run '^TestServeModelSwitch_ProviderFailureRestoresCapability$' -count=1` — FAIL before reaching the test: no rendezvous entry after 5s (sandbox daemon startup/listener restriction); this is consistent with the existing loopback-bind concern.
- `git diff --check` — PASS.

Self-review: changes are limited to the two test files, do not modify production server/AppWire code, preserve structured notification ordering, and make all three milestone waits context-bounded. The focused integration test remains unverified in this sandbox due to daemon rendezvous/listener startup restriction.
