# Stable Delegate Attention Projection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make one revisioned stable delegate projection the authoritative source of delegate attention across status APIs, Appwire, and Hub cards.

**Architecture:** The child transcript remains durable authority. The delegate journal materializes `NeedsAttention`; consumed answers may record `ResumeGeneration` for crash recovery. Existing claims and `AppendBatch` serialize changes. Add no second revision, cache, poller, or transaction framework.

**Tech Stack:** Go delegate controller/journal/transcript, Appwire and generated TypeScript, React/Zustand, Go tests, Vitest.

**Spec:** `docs/superpowers/specs/2026-08-20-stable-delegate-attention-projection-design.md`

## Constraints and Test Shape

- Read `docs/testing.md` before editing tests.
- Keep the transcript authoritative and the journal field derived. Same-value attention updates are no-ops on the existing `ProjectionRevision`.
- Keep `job_list` and generic child-thread status unchanged.
- Use existing claims, `AppendBatch`, fixtures, and fault seams. Add no attention IDs to the journal, second revision, cache, poller, TTL, janitor, migration, worker, or generalized transaction layer.
- Add no test infrastructure, shell scripts, meta-tests, source-string assertions, compiler/browser/process mocks, mutation scaffolding, matrices, or coverage cases.
- Keep exactly three behavior groups, extending existing suites:
  1. **Controller/store transition:** opening, consumption, batching, stop ordering, lifecycle normalization, append failure, revision behavior, and owed-generation recovery in cohesive state-machine scenarios.
  2. **Restore and cold read:** stale true/false, eligibility, bootstrap order, and eligible transcript errors in one table-driven group.
  3. **Projection and card:** existing event/Appwire fixtures plus one real card test proving stable attention wins over child status.
- Delete obsolete frontend reconciliation tests; do not translate them into replacement unit tests.
- Stage only the named paths in each commit.

---

### Task 1: Add the Stable Projection Field

**Files:**
- Modify: `agent/internal/delegatestore/event.go`
- Modify: `agent/internal/delegatestore/record.go`
- Modify: `agent/internal/delegatestore/fold.go`
- Test: `agent/internal/delegatestore/fold_test.go`
- Modify: `agent/delegate_tree_controller.go`
- Modify: `agent/status.go`
- Modify: `agent/events/payloads.go`
- Test: `agent/events/payloads_test.go`
- Modify: `agent/session_tools_jobs.go`
- Test: `agent/delegate_resource_tools_test.go`
- Modify: `appwire/types.go`
- Test: `appwire/types_test.go`
- Modify: `cmd/evener-hub/app_threadread.go`
- Modify: `server/server.go`
- Modify: `cmd/evener/serve.go`
- Test: `cmd/evener/serve_test.go`
- Modify: `server/appwire_runtime.go`
- Test: `server/appwire_runtime_test.go`
- Modify: `internal/appprojector/appwire_projection.go`
- Test: `internal/appprojector/delegate_projection_test.go`
- Generate: `cmd/evener-hub/frontend/src/protocol/types.gen.ts`

**Interfaces:**
- Add `EventDelegateAttentionChanged` and `DelegateAttentionChanged{NeedsAttention bool}` (`needs_attention`).
- Add required `NeedsAttention bool` to `delegatestore.Aggregate`, `delegateSnapshot`, both `DelegateStatusInfo` structs, `events.DelegateUpdatedData`, `stableDelegateStatusResult`, and `appwire.EvenerDelegateInfo`.
- JSON remains snake_case for Go status/tool/event surfaces and `needsAttention` for Appwire/generated TypeScript.
- Preserve `ProjectionRevision` and `LatestActivityAt` merge behavior; do not add the field to `jobListEntry`.

- [ ] **Step 1: Extend the existing projection fixtures**

In the listed tests, add `NeedsAttention: true` to the existing fold, event, Appwire, server bridge, projector, and `job_status` fixtures. In `TestApplyProjectionRevisionIncrementsOnlyAffectedDelegate`, add one transition table covering false→true, repeated true, and true→false; extend existing closure/subtree cases to assert lifecycle-folded false without a separate attention event.

- [ ] **Step 2: Run the focused RED command**

```bash
go test ./agent/internal/delegatestore ./agent/events ./agent ./appwire ./internal/appprojector ./server ./cmd/evener ./cmd/evener-hub \
  -run '^(TestApplyProjectionRevisionIncrementsOnlyAffectedDelegate|TestDelegateUpdatedDataJSONRoundTrip|TestStableDelegateTools_StatusReadsMetadataWithoutPacketOrAck|TestEvenerDiagnosticsDelegatesJSONRoundTrip|TestDelegateProjection_RevisionRejectsStaleStateButMergesLatestActivityByMax|TestAppDiagnosticsFromDetailedStatus_DelegatesLossless|TestAgentToServerDetailedStatus_DelegatesLossless)$' -count=1
```

Expected: FAIL because the event and required fields do not exist.

- [ ] **Step 3: Implement the field and closed projection**

Fold the event only when its value differs from the aggregate. Include the boolean in the aggregate's existing public-projection comparison, capture it in `delegateSnapshot`, normalize it inside existing stopping/permanent-closure/subtree folds, and map it through every listed status, event, Appwire, `ThreadModel.delegates`, and `job_status` conversion. Leave `job_list` unchanged.

- [ ] **Step 4: Generate and run the focused GREEN command**

```bash
make generate
go test ./agent/internal/delegatestore ./agent/events ./agent ./appwire ./internal/appprojector ./server ./cmd/evener ./cmd/evener-hub \
  -run '^(TestApplyProjectionRevisionIncrementsOnlyAffectedDelegate|TestDelegateUpdatedDataJSONRoundTrip|TestStableDelegateTools_StatusReadsMetadataWithoutPacketOrAck|TestEvenerDiagnosticsDelegatesJSONRoundTrip|TestDelegateProjection_RevisionRejectsStaleStateButMergesLatestActivityByMax|TestAppDiagnosticsFromDetailedStatus_DelegatesLossless|TestAgentToServerDetailedStatus_DelegatesLossless)$' -count=1
```

Expected: PASS; generated `EvenerDelegateInfo` has required `needsAttention: boolean`.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/delegatestore/event.go agent/internal/delegatestore/record.go agent/internal/delegatestore/fold.go agent/internal/delegatestore/fold_test.go agent/delegate_tree_controller.go agent/status.go agent/events/payloads.go agent/events/payloads_test.go agent/session_tools_jobs.go agent/delegate_resource_tools_test.go appwire/types.go appwire/types_test.go cmd/evener-hub/app_threadread.go server/server.go cmd/evener/serve.go cmd/evener/serve_test.go server/appwire_runtime.go server/appwire_runtime_test.go internal/appprojector/appwire_projection.go internal/appprojector/delegate_projection_test.go cmd/evener-hub/frontend/src/protocol/types.gen.ts
git commit -m "feat(delegate): project durable attention state"
```

### Task 2: Serialize Delivery and Answer Acceptance

**Files:**
- Modify: `agent/schema/turn.go`
- Modify: `agent/delegate_tree_controller.go`
- Modify: `agent/delegate_tree_attention.go`
- Modify: `agent/delegate_delivery.go`
- Modify: `agent/session_attention.go`
- Modify: `agent/delegate_tree_start.go`
- Modify: `agent/delegate_tree_stop.go`
- Modify: `agent/delegate_tree_restore.go`
- Modify: `agent/delegate_runtime.go`
- Test: `agent/delegate_delivery_test.go`
- Test: `agent/session_attention_test.go`
- Test: `agent/delegate_tree_start_test.go`

**Interfaces:**
- Extend `schema.AttentionResolutionInfo` with `ResumeGeneration uint64 \`json:"resume_generation,omitempty"\``.
- Keep `ReserveAttention`/`delegateStartReservation` as the acceptance claim and generation reservation.
- Replace the split wake/arm mirrors with one controller-owned unresolved-ID set; reservations, delivery claims, and settlement claims remain separate.
- Use the existing `appendLocked`/`AppendBatch` path for owner-open plus sender acknowledgment and final-clear plus `RunStarted`.
- Recover a consumed nonzero `ResumeGeneration` missing its `RunStarted` through dedicated bootstrap admission.

- [ ] **Step 1: Extend the controller/store behavior group**

Extend the existing delivery and attention state-machine scenarios rather than adding edge-specific tests. Together they must prove: first open and sender acknowledgment share one batch; additional unresolved IDs do not change revision; a failed batch leaves delivery replayable and local state unchanged; nonfinal/final consumption preserves or clears the boolean correctly; stop wins before acceptance or waits after admission; failed post-consumption append blocks launch and retries the same batch; lifecycle folds clear without an extra attention event; restart recovers a consumed nonzero generation while another unresolved ID keeps attention true, and a historical zero marker launches nothing.

- [ ] **Step 2: Run the focused RED command**

```bash
go test ./agent -run '^(TestDelegateControllerCommittedDeliveryCompletionAcknowledgesExactHead|TestDelegateControllerDeliveryAckRemovesOnlyExactID|TestDelegateControllerDeliveryAcknowledgedAppendFailureKeepsReceiptAndHead|TestDelegateAttention_ResolutionFsyncPrecedesSourceAck|TestDelegateAttention_StopLeavesBoundAttentionForDiscard|TestDelegateControllerAttentionCommitBindsSelectedPendingTranscriptEntry|TestDelegateAttention_RestartRearmsColdChildAndDrainsExactAttention)$' -count=1
```

Expected: FAIL on the new batch, revision, and acceptance-order assertions.

- [ ] **Step 3: Implement serialized transitions**

After receiver transcript append, have `CompleteDelivery` derive the delivered attention ID from its existing token/receipt and commit an optional owner `DelegateAttentionChanged(true)` plus required sender `DeliveryAcknowledged` in one batch. Root-owned delivery omits the owner event. Publish the unresolved set and update plans only after success.

For accepted answers, reserve the generation before transcript consumption and persist that generation on the consumed marker. Derive the remaining unresolved IDs, then commit the optional final `DelegateAttentionChanged(false)` and `RunStarted` together. Remove the ID and launch only after success. A failed append retains only the narrow retry state needed to replay that batch; live projections remain at the committed revision.

Use zero `ResumeGeneration` for discards and historical records. Do not add an acceptance-intent journal event or a new transaction abstraction.

During bootstrap, scan consumed nonzero markers and admit any generation without a matching `RunStarted`. Reuse persisted descriptor/transcript construction and the normal committed-start failure path; do not route it through lost-runtime reconciliation. Task 3 places this admission before boolean repair.

- [ ] **Step 4: Run the focused GREEN command**

```bash
go test ./agent -run '^(TestDelegateControllerCommittedDeliveryCompletionAcknowledgesExactHead|TestDelegateControllerDeliveryAckRemovesOnlyExactID|TestDelegateControllerDeliveryAcknowledgedAppendFailureKeepsReceiptAndHead|TestDelegateAttention_ResolutionFsyncPrecedesSourceAck|TestDelegateAttention_StopLeavesBoundAttentionForDiscard|TestDelegateControllerAttentionCommitBindsSelectedPendingTranscriptEntry|TestDelegateAttention_RestartRearmsColdChildAndDrainsExactAttention)$' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/schema/turn.go agent/delegate_tree_controller.go agent/delegate_tree_attention.go agent/delegate_delivery.go agent/session_attention.go agent/delegate_tree_start.go agent/delegate_tree_stop.go agent/delegate_tree_restore.go agent/delegate_runtime.go agent/delegate_delivery_test.go agent/session_attention_test.go agent/delegate_tree_start_test.go
git commit -m "fix(delegate): serialize attention transitions"
```

### Task 3: Reconcile Bootstrap and Cold Reads

**Files:**
- Modify: `agent/delegate_tree_attention.go`
- Modify: `agent/delegate_tree_restore.go`
- Modify: `agent/delegate_runtime.go`
- Modify: `agent/status.go`
- Modify: `agent/jobs_activity_past.go`
- Test: `agent/status_test.go`
- Test: `agent/jobs_activity_past_test.go`

**Interfaces:**
- Bootstrap recovers each nonzero `ResumeGeneration` missing its matching `RunStarted`, then repairs remaining boolean mismatches, then publishes the rebuilt unresolved sets.
- `LoadSessionDelegateStatus` overlays the same transcript-derived boolean without writing the journal.
- Eligible missing/unreadable transcripts return an error; ineligible delegates normalize false without reading a transcript.

- [ ] **Step 1: Add the restore/cold-read behavior group**

In `agent/status_test.go`, add `TestStableDelegateAttention_RestoreAndColdRead` as one table-driven group reusing the existing restore and cold-status fixtures. Its rows cover stale false with pending attention, stale true without pending attention, ineligible closed/stopping/fenced delegates, eligible missing/unreadable transcript, and placement of Task 2 owed-generation admission before boolean repair.

- [ ] **Step 2: Run the focused RED command**

```bash
go test ./agent -run '^(TestStableDelegateAttention_RestoreAndColdRead)$' -count=1
```

Expected: FAIL because final-boundary repair, cold overlay, and owed-generation admission are absent.

- [ ] **Step 3: Implement final-boundary reconciliation**

Run this only after existing stop reconciliation, missing-input cleanup, and unreachable-attention transfer. Invoke Task 2 owed-generation admission, then append ordinary `DelegateAttentionChanged` repairs for remaining mismatches and publish the exact unresolved sets.

Apply the same eligibility and transcript fold in `LoadSessionDelegateStatus` as a read-only replacement-snapshot overlay. Existing absent journal fields remain migration-free false values until cold overlay or bootstrap repair.

- [ ] **Step 4: Run the focused GREEN command**

```bash
go test ./agent -run '^(TestStableDelegateAttention_RestoreAndColdRead)$' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/delegate_tree_attention.go agent/delegate_tree_restore.go agent/delegate_runtime.go agent/status.go agent/jobs_activity_past.go agent/status_test.go agent/jobs_activity_past_test.go
git commit -m "fix(delegate): reconcile attention on restore"
```

### Task 4: Delete Frontend Reconciliation and Trust Stable Attention

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModule.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModuleStore.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/ToolCallItem.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/tools/jobTools.tsx`
- Delete: `cmd/evener-hub/frontend/src/panes/session/transcript/tools/watchedChild.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModule.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModuleStore.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/ToolCallItem.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/tools/jobTools.test.tsx`
- Delete: `cmd/evener-hub/frontend/src/panes/session/transcript/tools/watchedChild.test.tsx`

**Interfaces:**
- After hydration, required `EvenerDelegateInfo.needsAttention` and the stable delegate entry solely determine lifecycle, outcome, reason, timing, exhaustion, resumability, and attention.
- Frozen tool output remains the pre-hydration fallback. Expanded child watches retain content only: quotes, turns, calls/counts, and latest-quote activity.

- [ ] **Step 1: Replace reconciliation tests with one card behavior test**

In `subagentModule.test.tsx`, use a real `ToolCallItem` to prove stable `needsAttention=true` produces the needs-you glyph/hidden status while child content status varies across active, idle, and awaiting; stable false suppresses child-derived attention; stable terminal lifecycle wins. Delete tests whose only subject is removed reconciliation.

- [ ] **Step 2: Run the focused RED command**

```bash
cd cmd/evener-hub/frontend
npx vitest run src/panes/session/transcript/tools/subagentModule.test.tsx
```

Expected: FAIL because lifecycle and attention still read child status.

- [ ] **Step 3: Delete the obsolete production paths**

Delete `WatchedChildIndicator` and its lean mount; `rowKindFromChildStatus`; `setWatchedLiveKind`, `liveKind`, and no-resurrection/shadow state; follow-up-tool writes to spawn-row lifecycle/reason/resumability/exhaustion; and child-derived lifecycle, attention, and run-window fallbacks. Keep only the expanded content watch and stable-projection consumption.

- [ ] **Step 4: Format and run the focused GREEN command**

```bash
npx biome check --write \
  src/panes/session/transcript/tools/subagentModule.tsx \
  src/panes/session/transcript/tools/subagentModuleStore.ts \
  src/panes/session/transcript/ToolCallItem.tsx \
  src/panes/session/transcript/tools/jobTools.tsx \
  src/panes/session/transcript/tools/subagentModule.test.tsx \
  src/panes/session/transcript/tools/subagentModuleStore.test.ts \
  src/panes/session/transcript/ToolCallItem.test.tsx \
  src/panes/session/transcript/tools/jobTools.test.tsx
npx vitest run \
  src/panes/session/transcript/tools/subagentModule.test.tsx \
  src/panes/session/transcript/tools/subagentModuleStore.test.ts \
  src/panes/session/transcript/ToolCallItem.test.tsx \
  src/panes/session/transcript/tools/jobTools.test.tsx
```

Expected: PASS; obsolete test count decreases.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModule.tsx cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModuleStore.ts cmd/evener-hub/frontend/src/panes/session/transcript/ToolCallItem.tsx cmd/evener-hub/frontend/src/panes/session/transcript/tools/jobTools.tsx cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModule.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModuleStore.test.ts cmd/evener-hub/frontend/src/panes/session/transcript/ToolCallItem.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/tools/jobTools.test.tsx
git add -u cmd/evener-hub/frontend/src/panes/session/transcript/tools/watchedChild.tsx cmd/evener-hub/frontend/src/panes/session/transcript/tools/watchedChild.test.tsx
git commit -m "refactor(web): trust stable delegate attention"
```

## Final Verification

Run once after all four commits:

```bash
git diff --check origin/main...HEAD
rg -n 'liveKind|setWatchedLiveKind|rowKindFromChildStatus|WatchedChildIndicator' cmd/evener-hub/frontend/src
make lint
make vet
make test
make test-web-browser
```

Expected: `git diff --check` and all Make targets exit 0; `rg` exits 1 with no production or test matches. Confirm the final diff contains only the materialized boolean/event, optional `ResumeGeneration`, narrow ordering/retry state, projection plumbing, and frontend deletion; `job_list` and generic child-thread status remain unchanged.
