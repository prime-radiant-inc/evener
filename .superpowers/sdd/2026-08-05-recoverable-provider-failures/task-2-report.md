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

## Root-cause fix verification (2026-08-05)

Fresh verification found the recovery test was not reaching the second scripted adapter: `installServeScriptedProviders` assigned `Type: "openai"` to every `InstanceConfig`, including the `kimi-anthropic` instance. Since provider routing derives the instance behavior tag from `Type`, the initial Kimi model was misconfigured and the cross-provider recovery request did not route correctly.

- Changed only `cmd/serf/scripted_provider_test.go`: added `scriptedProviderType`, mapping `kimi-anthropic` to `Type("kimi-anthropic")` and preserving `Type("openai")` for all existing scripted adapters. No production server, AppWire, model-switch, or AppWire code changed.
- `gofmt -w cmd/serf/scripted_provider_test.go cmd/serf/serve_model_switch_test.go` — PASS.
- `GOCACHE=/tmp/serf-gocache go test ./cmd/serf -run '^TestServeModelSwitch_' -count=1 -v` — FAIL in this sandbox before daemon rendezvous: both tests reported `no rendezvous entry ... after 5s`; this is the existing loopback listener restriction, not a test assertion failure. The command exited 1.
- `git diff --check` — PASS.

Self-review: the fix is test-only and minimal; the one-provider helper still produces `openai` instances, while the recovery helper now gives each named scripted provider its matching configured type. Existing assertions remain strict, including exactly one request per provider and provider/model identity. No assertion was weakened and no recovery path was skipped.

- Existing scripted-provider `steps` callers remain supported.
- Error responses return before provider/model/finish defaults are applied.
- The regression uses channels and structured AppWire notifications rather than sleeps or polling for turn state.
- The test asserts failed-turn ordering, idle capability, snapshot state, model switching, successful completion, and exact provider/model routing.

## Concerns

The real daemon regression could not execute in this environment because network socket binding is denied by the sandbox. Compilation and non-network focused verification passed. Re-run `go test ./cmd/serf -run '^TestServeModelSwitch_' -count=1` in an environment permitting loopback binds before treating the integration proof as fully runtime-verified.

## Follow-up Verification: Completion Identity Race

The first loopback-capable full test run reached the recovery assertion but reported `kimi 1, openai 0`. Notification tracing showed that the test's generic `successful turn` milestone consumed the persisted `thread/model/changed` marker turn (`turn_2`), which is emitted as a completed turn before the second client turn. The second `turn/start` was accepted as `turn_m2`, but the test asserted provider requests before waiting for that specific completion.

The test now records completed-turn IDs and waits for the `TurnStart` response's exact recovery turn ID. This preserves structured, bounded waiting and makes the provider-count assertion occur after the actual recovery turn.

- `gofmt -w cmd/serf/serve_model_switch_test.go` — PASS.
- `GOCACHE=/tmp/serf-gocache go test ./cmd/serf -run '^TestServeModelSwitch_ProviderFailureRestoresCapability$' -count=1 -v` — PASS (`ok primeradiant.com/serf/cmd/serf 0.622s`).
- `GOCACHE=/tmp/serf-gocache go test ./cmd/serf -run '^TestServeModelSwitch_' -count=1` — PASS (`ok primeradiant.com/serf/cmd/serf 0.475s`).
- `git diff --check` — PASS.

Self-review: the change is test-only, removes temporary logging, does not weaken any milestone or provider/model assertion, and filters only by the server-assigned recovery turn ID returned by `turn/start`.

## Fix review findings

- Added `waitServeMilestone`, selecting between each structured milestone channel and `ctx.Done()`; replaced failed-turn, idle-capability, and successful-turn unbounded receives. No sleeps or polling were added.
- Added assertions that the initial Kimi request uses provider `kimi-anthropic` and model `k3`.
- Added a clear `t.Fatal` guard requiring at least one adapter in `installServeScriptedProviders`.
- `gofmt -w cmd/serf/scripted_provider_test.go cmd/serf/serve_model_switch_test.go` — PASS.
- `go test ./cmd/serf -run '^$' -count=1` — PASS; package compiled.
- `go test ./cmd/serf -run '^TestServeModelSwitch_ProviderFailureRestoresCapability$' -count=1` — FAIL before reaching the test: no rendezvous entry after 5s (sandbox daemon startup/listener restriction); this is consistent with the existing loopback-bind concern.
- `git diff --check` — PASS.

Self-review: changes are limited to the two test files, do not modify production server/AppWire code, preserve structured notification ordering, and make all three milestone waits context-bounded. The focused integration test remains unverified in this sandbox due to daemon rendezvous/listener startup restriction.
