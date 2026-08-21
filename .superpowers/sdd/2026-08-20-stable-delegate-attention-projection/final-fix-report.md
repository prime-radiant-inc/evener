# Final Review Fix Report: Issue #307 Round 1/5

## Status

Complete. The five validated final-review findings are fixed within the allowed stable-delegate attention boundaries. The two pre-existing lint-only edits (`slices.Backward` and `maps.Copy`) are preserved.

## Base / deliverable

- Base: `7b6bc8e6630f17b94486efc79d94541b14f98dc1`
- Deliverable commit: `fix(delegate): close attention recovery gaps` (the final hash is reported in the handoff; a commit cannot embed its own hash)

## Exact paths

Production:

- `agent/delegate_delivery.go`
- `agent/delegate_runtime.go` (pre-existing `slices.Backward` lint edit preserved)
- `agent/delegate_tree_controller.go`
- `agent/delegate_tree_restore.go` (pre-existing `maps.Copy` lint edit preserved)
- `agent/internal/delegatestore/fold.go`
- `agent/job_watch.go`
- `agent/jobs.go`
- `agent/jobs_activity_past.go`
- `agent/session_attention.go`
- `agent/status.go`

Tests:

- `agent/delegate_attention_ancestor_fence_test.go`
- `agent/delegate_delivery_test.go`
- `agent/delegate_resource_shell_test.go`
- `agent/delegate_resource_supervision_test.go`
- `agent/delegate_resource_watch_test.go`
- `agent/internal/delegatestore/envelope_fuzz_test.go`
- `agent/internal/delegatestore/fold_test.go`
- `agent/jobs_activity_past_test.go`
- `agent/status_test.go`

Report:

- `.superpowers/sdd/2026-08-20-stable-delegate-attention-projection/final-fix-report.md`

No frontend or production path outside the brief's conceptual allowlist was changed.

## Behavioral RED / GREEN

### 1. Ancestor closure normalizes and publishes descendants

RED:

- `TestApplyResumabilityClosureIsMonotonic`: child remained `NeedsAttention=true` after parent resumability closure.
- `TestDelegateAttentionWake_PermanentClosedAncestorEscalatesToRootOnce`: committed grandchild projection remained `attention:true revision:2`, expected `false/3`, and no matching child update was published.

GREEN:

- The closure fold clears attention for the target and every descendant. Existing public-projection comparison increments only changed revisions.
- Closure update capture includes the target subtree whenever the target is non-resumable, covering every existing closure producer that already captures the target plan.
- Focused fold and ancestor tests pass.

### 2. Arm failures retain live retry/source ownership

RED:

- Resident quiet owner: exact durable quiet ID had `retrying:false` while `quietNotified:true` after an injected post-append arm fold failure.
- Shell: first finalization returned `nil` after the injected arm failure; source notification had already been acknowledged and finalization released.
- Cold watch: exact pending watch source disappeared after delegate-journal arm failure.

GREEN:

- `delegateAttentionArmIDs` now schedules and clears exact-ID retries for root and non-root resident sessions. Quiet uses this one general arm retry: `quietNotified` prevents duplicate production while the exact durable ID remains retryable until arm succeeds.
- Stable shell now arms before `JobNotificationDelivered`, running-source removal, output/finalization closure, and propagates failure through the existing idempotent finalization retry.
- Stable watch now arms before `WatchSendDelivered`; current, superseded, resident, and cold sources retain their existing pending/receipt replay state until arm succeeds.
- Focused quiet, shell, and watch failure/retry scenarios pass.

### 3. Caller-committed nested inline replay suppresses owner attention

RED:

- `TestDelegateControllerInlineReplayAfterReceiverCommitIsIdempotent`: nested replay with durable caller `DelegateDeliveryCommit` opened owner attention and installed `delegate:dlg_target/delivery/1`.

GREEN:

- One `callerCommitted` provenance bit now travels through the existing replay plan and admission receipt. Such replay durably acknowledges the sender but is treated as inline for owner-attention suppression.
- The replay journal tail is acknowledgment-only; owner attention and local attention IDs remain absent.

### 4. Job activity remains journal-only

RED:

- `TestLoadSessionJobActivityTree_FollowsOnlyStableDelegateChildren`: malformed eligible child transcript caused job activity load to fail (`decode transcript header boundary`).

GREEN:

- `LoadSessionJobActivityTree` folds only the delegate journal and no longer reads eligible child transcripts.
- Strict transcript attention overlay is called only by `LoadSessionDelegateStatus`.
- The existing job-activity scenario passes with one malformed and one missing eligible child transcript while retaining journal state.

### 5. Envelope fuzz registry includes attention

RED:

- With the new attention seed present, a temporary validator mutation that rejected the attention payload failed `FuzzDelegateEventEnvelope/seed#7` with `payload does not match kind`. The mutation was immediately reverted.

GREEN:

- The existing registry contains `EventDelegateAttentionChanged` with `DelegateAttentionChanged`.
- Payload masks/seeds use `uint16`; the full mask is `511`.
- `go test -tags=evenerfuzz ./agent/internal/delegatestore -run '^FuzzDelegateEventEnvelope/seed#7$' -count=1`: PASS.

## Verification

Focused:

- `go test ./agent/internal/delegatestore -count=1`: PASS (`0.367s` on final broader run).
- Affected agent groups (`TestDelegateAttention`, `TestStableDelegateAttention`, `TestStableDelegateShell`, `TestStableDelegateWatch`, `TestLoadSessionJobActivityTree`, `TestLoadSessionDelegateStatus`, `TestDelegateController`): PASS (`8.432s`).
- Exact selected five-finding scenarios plus attention fuzz seed: PASS.

Required/static:

- `go vet ./agent`: PASS.
- `gofmt` over every changed Go path: PASS.
- `git diff --check`: PASS.
- `GOLANGCI_LINT_CACHE="$EVENER_SCRATCH_DIR/golangci-cache" make lint` (with the existing host module cache supplied because this fresh worktree had no dependencies and network is disabled): PASS, 8 modules, 69s.

Full agent limitation:

- Exact `go test ./agent -count=1` compiled and began running, but the fixed sandbox denied an unrelated `httptest.NewServer` loopback bind in `TestSession_ExcludesConfiguredCredentialFromResponseEndpointArtifacts`: `listen tcp6 [::1]:0: bind: operation not permitted`.
- A second run skipping only that test reached the same sandbox denial in `TestSession_OpenAIResponsesContinuationOffUsesFullHistory` after 29.111s.
- A full agent run excluding the root-agent test functions in files that use `httptest.New*` passed in 68.582s. This is strong product coverage but not a zero-exit verdict for the exact required full command. The parent must rerun exact `go test ./agent -count=1` outside the restricted sandbox.

## Test and line deltas

- Top-level `Test`/`Fuzz` function count: `5751 -> 5751` (delta `0`). Two existing watch tests were renamed to state the corrected arm-before-source-settlement order; no top-level test was added or removed.
- Test files added: `0`.
- Test helpers/frameworks added: `0`.
- Production: `+80 / -46` (net `+34`), including the two pre-existing lint-only edits.
- Tests: `+303 / -33` (net `+270`).
- Before this report: 19 Go files, `+383 / -79` total.

## Self-review / concerns

- Descendant closure changes only the materialized public attention projection; transcript attention IDs remain untouched until normal transfer/discard.
- Every production resumability-close producer already captures its target plan; expanding that existing capture only for a non-resumable target publishes all normalized descendants without another event or retry domain.
- Resident quiet, shell, and watch recovery reuse existing exact-ID, finalization, pending-send, receipt, and settlement replay states. No cache, poller, worker, migration, TTL, or second producer retry domain was introduced.
- Shell and watch durable source acknowledgments now occur only after successful arm. Replay remains idempotent because transcript attention append and controller attention open are exact-ID no-ops after success.
- Caller-commit provenance is process-local replay provenance only; no transcript field or compatibility state was added.
- Job activity and job list remain journal-only; strict transcript overlay remains exclusive to delegate status.
- Concern: the exact full agent test command is environmentally incomplete due sandbox loopback-bind denial, as detailed above. All affected tests, the sandbox-compatible remainder of the full agent package, vet, diff checks, and full lint pass.
- Scratch directory: `/private/var/folders/46/dz2z92w907j150sqxn8b8y1c0000gn/T/evener-sandbox-3279988824`. No scratch artifact needs retention.
