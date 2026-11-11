# Manager Filtered-Read Continuation Resolver Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every filtered-read continuation resume the exact logical turn range that minted it across live, saved, remote, and older-daemon sources.

**Architecture:** A self-contained cursor carries a stable `ResumeWindow` plus exactly one validation proof: an item anchor or an all-empty range digest. One hub resolver loads that stable range, validates its proof independently, normalizes legacy fragments into logical turns, and hands the result to the existing filtered-read packer. Manager follow-ups enumerate every page-level and per-turn continuation without changing the public AppWire schema.

**Tech Stack:** Go, AppWire, the existing `appitempaging` cursor codec and filtered-read packer, transcript indexes, scripted AppWire daemons and hubs.

**Spec:** `docs/superpowers/specs/2026-11-09-manager-filtered-read-continuation-resolver-design.md`

## Global Constraints

- The existing filtered-read packer remains the only packer.
- `ResumeWindow` is stable and never contains `latest`; continuation handling never reuses the caller's original moving selector.
- A cursor contains exactly one proof: `AnchorTurnID` with position and nonzero item version, or one lowercase SHA-256 `EmptyRangeDigest` with no item anchor fields.
- Existing thread ref, incarnation, projection, direction, selection-shape, position, and item-version fences remain exact.
- Local and saved-index windows have no artificial turn-count maximum. Only older-daemon network traversal is limited to eight pages and rejects cursor cycles.
- Stopped remote filtered reads are attempted without a new capability bit; typed remote failures pass through.
- Every emitted continuation preserves ref, original turn window, item types, byte bounds, budget, and item limit.
- No backward decoder is added for the superseded unshipped window-cursor form.
- Tests assert behavior, not human-readable phrasing. Default tests use scripted local components and make no live provider or network request.
- Do not change canonical fetch bytes, fetch cursor behavior, the canonical-byte store, AppWire fields, or generated clients.

## Review Focus

- A cursor with both proof forms, neither proof form, a malformed digest, or a zero item version must be rejected as stale; Task 1 tests each shape.
- An anchor outside the resumed range must still be found and version-validated, while a missing or changed anchor must fail before returning content; Tasks 3 and 4 test both outcomes.
- An all-empty digest range that gains a positioned item, changes order, inserts or removes a shell, or changes shell state must fail stale; Tasks 2 and 3 cover these mutations.
- An older daemon that repeats one logical turn across page fragments, cycles its cursor, or needs a ninth page must never return a partial or broadened window; Task 4 tests all three bounds.
- Equal page-level and per-turn cursors must be deduplicated without reordering distinct cursors, and each follow-up must retain the original read shape; Task 5 tests ordering, deduplication, and arguments.

---

### Task 1: Add Exact Cursor Proof Forms

**Files:**
- Modify: `internal/appitempaging/cursor_forms.go:164-311`
- Modify: `internal/appitempaging/window_cursor_test.go`

**Interfaces:**
- Consumes: existing `CursorIdentity`, `WindowCursorDirection`, `appwire.ThreadTurnWindow`, and the 4096-byte cursor bound.
- Produces: `WindowCursor{AnchorTurnID string, Position appwire.ThreadItemPosition, ItemVersion uint64, EmptyRangeDigest string, ResumeWindow *appwire.ThreadTurnWindow}`; unchanged `EncodeWindowCursor`, `DecodeWindowCursor`, `DecodeWindowCursorForRef`, and `TranslateWindowCursorRef` signatures.

- [ ] **Step 1: Write the failing item-anchor and empty-digest round-trip tests**

Add table cases to `TestWindowCursorRoundTripByDirection` for an item anchor and a 64-character lowercase SHA-256 digest. Assert exact decoded equality, raw URL-safe encoding, direction fencing, and ref-scoped decode. Extend `TestWindowCursorTranslatesHubNamespace` to assert that translation changes only `ThreadRef` and preserves both proof variants byte-for-value after decode.

- [ ] **Step 2: Write malformed-proof red tests**

Extend `TestWindowCursorRejectsLegacyMalformedIdentityAndDirection` with missing proof, both proofs, anchor without turn ID, anchor with zero version, digest with anchor position/version, short digest, non-hex digest, and uppercase digest. Assert each encode/decode path returns the typed stale classification. Add an encoded-size assertion that the largest valid proof form stays within the existing cursor maximum.

- [ ] **Step 3: Run the cursor tests and record RED**

Run: `go test ./internal/appitempaging -run '^TestWindowCursor' -count=1`

Expected: FAIL because `WindowCursor` has no anchor/digest proof and the old codec requires an unqualified position/version pair.

- [ ] **Step 4: Implement mutually exclusive proof validation**

Keep the public codec function signatures. Add `AnchorTurnID` and `EmptyRangeDigest` to the public and wire forms. Encode item-anchor position/version only with a nonempty anchor turn ID; encode a digest only when it is exactly lowercase hex for 32 bytes and all item-anchor fields are absent/zero. Decode with pointer fields so absence is distinguishable from a valid zero position, reject unknown fields, and reconstruct the public form. Preserve the selected proof in `TranslateWindowCursorRef`.

- [ ] **Step 5: Run the cursor tests and package suite GREEN**

Run: `go test ./internal/appitempaging -run '^TestWindowCursor' -count=1`

Then: `go test ./internal/appitempaging -count=1`

Expected: both commands exit 0 with no failures.

- [ ] **Step 6: Commit**

```bash
git add internal/appitempaging/cursor_forms.go internal/appitempaging/window_cursor_test.go
git commit -m "feat(manager): fence window cursors with explicit proofs"
```

### Task 2: Mint Item Anchors and Empty-Range Digests

**Files:**
- Modify: `cmd/evener-hub/app_item_page_fit.go:35-252,604-898`
- Modify: `cmd/evener-hub/app_item_page_fit_test.go`

**Interfaces:**
- Consumes: Task 1 `WindowCursor` proof fields and existing `transcriptItemCandidateResult` / packer options.
- Produces: `emptyTranscriptWindowDigest(result transcriptItemCandidateResult) (string, error)`, `transcriptItemCandidateResult.TurnShells []appwire.Turn`, and proof-bearing `transcriptWindowContinuation{AnchorTurnID, Position, Version, EmptyRangeDigest, ResumeWindow}`. The one existing packer remains the only response packer.

- [ ] **Step 1: Write red tests for independent item anchors**

Extend the backward continuation test with positioned A, empty B, positioned C. Decode the minted cursor and assert `ResumeWindow` continues before B while `AnchorTurnID`, position, and version identify C. Assert a forward intra-turn continuation anchors the first withheld candidate's turn and version.

- [ ] **Step 2: Write the all-empty budget proof RED**

Add `TestPackTranscriptWindowAllEmptyBudgetContinuationUsesDigest`. Construct enough ordered empty turn shells to exceed the 16 KiB effective content budget. Pack the first page and assert it serves a bounded ordered prefix, returns a continuation instead of an error, uses only `EmptyRangeDigest`, and keeps the encoded cursor within the existing bound. Change one shell field in an otherwise identical result and assert the digest changes.

- [ ] **Step 3: Run the packer tests and record RED**

Run: `go test ./cmd/evener-hub -run '^(TestTranscriptWindowBackwardContinuationKeepsInterleavedLogicalTurn|TestPackTranscriptWindowAllEmptyBudgetContinuationUsesDigest)$' -count=1`

Expected: FAIL because backward cursors infer the proof from the resume boundary and all-empty trimming has no positioned boundary candidate.

- [ ] **Step 4: Extract canonical turn-shell construction and digesting**

Add `TurnShells` to `transcriptItemCandidateResult` and populate it from each materialized response; saved-index ranges with no richer shell use the index's ordered selected IDs to synthesize the same fragment shells the response already exposes. Extract the packer's candidate-turn/synthetic-shell construction into one helper reused by packing and `emptyTranscriptWindowDigest`. Clone each ordered shell, clear `Items` and `Elision`, marshal once, and return lowercase SHA-256. Refuse digesting any range with positioned candidates.

- [ ] **Step 5: Mint the correct proof without changing packing policy**

Add proof fields to `transcriptWindowContinuation`. For item-bearing continuations, set `AnchorTurnID` from the actual boundary candidate independently of `ResumeWindow`. When budget trimming leaves an all-empty remaining range, mint the digest proof instead of calling `transcriptWindowBoundaryCandidate`. Pass the exact proof through `transcriptWindowOptionsFor` into Task 1's codec. Keep filtering, item caps, cursor-line limits, budgeting, and response shaping untouched.

- [ ] **Step 6: Run focused and complete packer tests GREEN**

Run: `go test ./cmd/evener-hub -run '^(TestPackTranscriptItemWindow|TestTranscriptWindow|TestSpendTranscriptWindowCursor)' -count=1`

Expected: exit 0 with no failures.

- [ ] **Step 7: Commit**

```bash
git add cmd/evener-hub/app_item_page_fit.go cmd/evener-hub/app_item_page_fit_test.go
git commit -m "feat(manager): mint anchored window continuations"
```

### Task 3: Resolve Stable Live and Saved Continuations

**Files:**
- Create: `cmd/evener-hub/transcript_window_continuation.go`
- Create: `cmd/evener-hub/transcript_window_continuation_test.go`
- Modify: `cmd/evener-hub/session_reads.go:76-260`
- Modify: `cmd/evener-hub/app_threadread.go:230-279`
- Modify: `cmd/evener-hub/app_threadread_index_test.go:345-423`
- Modify: `cmd/evener-hub/app_rpc_thread_read_test.go`

**Interfaces:**
- Consumes: proof-bearing Task 1 cursors, Task 2 digest helper, source-specific range loaders, and the existing packer.
- Produces: `type transcriptWindowLoadedRange struct { Result transcriptItemCandidateResult; Thread appwire.Thread }`, `type transcriptWindowRangeLoader func(context.Context, *appwire.ThreadTurnWindow) (transcriptWindowLoadedRange, error)`, and `resolveTranscriptWindowContinuation(context.Context, appwire.ThreadTurnsListParams, int, transcriptWindowRangeLoader) (transcriptWindowLoadedRange, appitempaging.WindowCursorDirection, error)`.

- [ ] **Step 1: Write resolver unit reds**

In the new test file, use an in-memory loader that records every selector. Assert the resolver first decodes the cursor, loads only its stable non-`latest` `ResumeWindow`, loads `AnchorTurnID` separately when absent from the range, validates exact position/version/view identity, and never invokes the loader with the original request's `latest`. Assert missing anchor, changed version, wrong identity, and wrong direction fail typed before a range is returned. Add digest cases for unchanged empty shells and every Review Focus mutation.

- [ ] **Step 2: Write saved append-stability RED**

Add `TestDaemonlessLatestContinuationIgnoresLaterAppend`. Seed more than one response page, request `latest:3`, save the first cursor, append a new turn to the transcript/index, follow the complete original chain, and assert only the three originally selected turn IDs appear once in order. Assert the appended turn never appears.

- [ ] **Step 3: Write empty-boundary and all-empty continuation REDs**

Add focused live and saved cases with positioned A, empty B, positioned C and a budget that serves B+C first. Follow the backward cursor and assert A succeeds. Decode the cursor to prove the resume boundary names B while its item anchor names C. Change C's version before spending a second cursor and assert typed item-changed/stale before A is returned.

Add `TestSessionOperationsContinuesAllEmptyBudgetWindow`. Follow an unchanged all-empty range through every digest cursor and assert every shell appears once in order. Re-spend independently minted cursors after adding a positioned item, changing one shell field, reordering shells, inserting a shell, and removing a shell; assert typed stale before content is returned.

- [ ] **Step 4: Run the new journeys and record RED**

Run: `go test ./cmd/evener-hub -run '^(TestResolveTranscriptWindowContinuation|TestDaemonlessLatestContinuationIgnoresLaterAppend|TestSessionOperationsContinuesAcrossEmptyBoundary|TestSessionOperationsContinuesAllEmptyBudgetWindow)' -count=1`

Expected: FAIL because saved continuation reloads the original moving selector and backward validation assumes the resume-boundary turn contains the positioned anchor.

- [ ] **Step 5: Implement the shared proof resolver**

Implement the exact loader and resolver types above. Decode with `DecodeWindowCursorForRef`, replace the caller selector with `ResumeWindow`, load the stable range, derive its view identity from the original request shape, and spend one proof. For an item proof, locate/load `AnchorTurnID`, validate exact position/version, then apply forward intra-turn slicing only when direction requires it. For a digest proof, require zero positioned candidates and compare `emptyTranscriptWindowDigest`. Set exhaustion relative to the resolved range and return no content on proof failure.

- [ ] **Step 6: Route live and saved continuation owners through it**

In `continueLiveTranscriptWindowOwned`, provide a loader backed by the current daemon window API and exact-turn reads; retain existing canonicalization and fetch-origin behavior after resolution. In `pastThreadTurnsListOwned`, decode before index access and provide a loader backed by `pastEntryTurnWindowItems`; never call the index with the request's original `latest` once a cursor exists. Delete the superseded backward special case and the saved path's direct call to `spendTranscriptWindowCursor`; retain legacy unwindowed handling.

- [ ] **Step 7: Run focused and affected suites GREEN**

Run: `go test ./cmd/evener-hub -run '^(TestResolveTranscriptWindowContinuation|TestDaemonless.*Window|TestSessionOperations.*Window|TestTranscriptWindow)' -count=1`

Then: `go test ./internal/transcriptindex ./server -count=1`

Expected: both commands exit 0 with no failures.

- [ ] **Step 8: Commit**

```bash
git add cmd/evener-hub/transcript_window_continuation.go cmd/evener-hub/transcript_window_continuation_test.go cmd/evener-hub/session_reads.go cmd/evener-hub/app_threadread.go cmd/evener-hub/app_threadread_index_test.go cmd/evener-hub/app_rpc_thread_read_test.go
git commit -m "fix(manager): resolve stable transcript continuations"
```

### Task 4: Normalize Older-Daemon Logical Turns

**Files:**
- Modify: `cmd/evener-hub/transcript_window_continuation.go`
- Modify: `cmd/evener-hub/transcript_window_continuation_test.go`
- Modify: `cmd/evener-hub/session_reads.go:262-363`
- Modify: `cmd/evener-hub/session_tools.go:894-919,1013-1047,1163-1170`
- Modify: `cmd/evener-hub/app_rpc_thread_read_test.go:562-991`

**Interfaces:**
- Consumes: Task 3 resolver/loader contract and existing `LegacyPageCandidateSource` conversion.
- Produces: `resolveCompatibilityTurnWindow(turns []appwire.Turn, window *appwire.ThreadTurnWindow, exhausted bool) ([]appwire.Turn, bool)` whose unit is an adjacent run of equal nonempty turn IDs, plus one bounded compatibility loader used by both initial and continuation reads.

- [ ] **Step 1: Write fragmented-turn RED**

Add `TestSessionOperationsCompatibilityCountsLogicalTurnsAcrossFragments`. Script newest page C plus the first B fragment and an older page containing the rest of B plus A. Assert `latest:2` selects all B fragments and C, counts B once, and exact B follows item-limit cursors through every B item without exposing A.

- [ ] **Step 2: Write selection-relative exhaustion RED**

Add `TestSessionOperationsCompatibilityExhaustsSelectedWindowBeforeNativeHistory`. Script selected B+C with an unrelated native cursor to A. Follow B's continuation to completion and assert `WithheldTurns == 0`, no page-level continuation remains, and no follow-up can reach A.

- [ ] **Step 3: Write bounded-failure reds**

Add table cases where the native cursor cycles and where the requested logical boundary would require a ninth network page. Assert typed stale, no partial logical turn, and exact page-call counts of the existing eight-page cap.

- [ ] **Step 4: Run compatibility tests and record RED**

Run: `go test ./cmd/evener-hub -run '^TestSessionOperations.*Compatibility' -count=1`

Expected: FAIL because initial latest slices transport rows, continuation selection can discard same-turn fragments, and exhaustion inherits the unrelated native cursor.

- [ ] **Step 5: Implement one logical-fragment normalizer and bounded loader**

Replace `compatibilityLatestTurns` and `compatibilityResolveTurnWindow` with the exact `resolveCompatibilityTurnWindow` interface. Group adjacent equal IDs for counting and selection while retaining every selected fragment in source order. The loader may fetch at most eight pages, rejects cursor reuse, keeps reading while a selected boundary turn advertises earlier fragments, and optionally loads the proof anchor. It returns exhaustion for the selected logical range, never for unrelated native history.

- [ ] **Step 6: Use the same loader for initial and continuation reads**

Route initial older-daemon `latest` selection and Task 3 continuation resolution through the bounded loader. Keep window fields hub-local on every legacy hop. Preserve source identity/history, overlay filtering, canonical candidate retention, and current typed unsupported behavior for non-latest initial windows.

- [ ] **Step 7: Run local and remote compatibility suites GREEN**

Run: `go test ./cmd/evener-hub -run '^(TestSessionOperations.*(Compatibility|OlderDaemon|OlderRemote)|TestResolveCompatibilityTurnWindow)' -count=1`

Expected: exit 0 with no failures, including the existing current/older remote fetch and continuation cases.

- [ ] **Step 8: Commit**

```bash
git add cmd/evener-hub/transcript_window_continuation.go cmd/evener-hub/transcript_window_continuation_test.go cmd/evener-hub/session_reads.go cmd/evener-hub/session_tools.go cmd/evener-hub/app_rpc_thread_read_test.go
git commit -m "fix(manager): normalize legacy transcript windows"
```

### Task 5: Publish Every Structured Continuation

**Files:**
- Modify: `cmd/evener-hub/session_tools.go:403-418`
- Modify: `cmd/evener-hub/session_reads_test.go`
- Modify: `cmd/evener-hub/app_rpc_thread_read_test.go`

**Interfaces:**
- Consumes: `appwire.ThreadTurnsListResponse` and the original `appwire.ThreadTurnsListParams`.
- Produces: `transcriptReadFollowUps(params appwire.ThreadTurnsListParams, page appwire.ThreadTurnsListResponse) []appwire.SessionStatusFollowUp` in page-level, turn-order, legacy order with equal cursors deduplicated.

- [ ] **Step 1: Write exact-turn per-turn recovery RED**

Add `TestManagerTurnsPublishesPerTurnElisionFollowUp`. Force an exact-turn `itemLimit` elision with no page-level continuation. Assert `Invoke` returns one `read_transcript` follow-up whose cursor equals the turn elision cursor and whose ref, exact turn selector, item types, preview/message bytes, budget, and item limit equal the original request.

- [ ] **Step 2: Write ordering and deduplication reds**

Add a table around `transcriptReadFollowUps`: page-level plus two distinct per-turn cursors plus legacy `NextCursor` preserve that order; repeated equal cursors appear once; absent cursors produce an empty non-nil/ordinary slice according to existing response conventions. Assert structured fields, not serialized wording.

- [ ] **Step 3: Run the follow-up tests and record RED**

Run: `go test ./cmd/evener-hub -run '^(TestManagerTurnsPublishesPerTurnElisionFollowUp|TestTranscriptReadFollowUps)' -count=1`

Expected: FAIL because the current code chooses only one page-level or legacy cursor and ignores turn elisions.

- [ ] **Step 4: Implement the collector and use it from `Invoke`**

Walk `page.Window.ContinuationCursor`, each `page.Data[i].Elision.ContinuationCursor`, then `page.NextCursor`; deduplicate by exact string while retaining first occurrence. Build every follow-up from the original `ThreadWindowParams`, ref, and item limit plus one cursor. Replace the single-cursor branch in `sessionOperations.Invoke` with this helper.

- [ ] **Step 5: Run Manager turns and shape tests GREEN**

Run: `go test ./cmd/evener-hub -run '^(TestManagerTurns|TestSessionOperations.*(Window|Compatibility|OlderDaemon|OlderRemote)|TestTranscriptReadFollowUps)' -count=1`

Expected: exit 0 with no failures.

- [ ] **Step 6: Commit**

```bash
git add cmd/evener-hub/session_tools.go cmd/evener-hub/session_reads_test.go cmd/evener-hub/app_rpc_thread_read_test.go
git commit -m "fix(manager): publish every transcript continuation"
```

### Task 6: Update Ownership Docs and Verify the Slice

**Files:**
- Modify: `docs/product/subsystems.md` (S10)
- Modify: `docs/automation.md` (Manager filtered-read follow-ups)
- Modify only if a proven contract conflict stops implementation: `specs/manager-tools/2-cheap-filtered-reads.md`

**Interfaces:**
- Consumes: Tasks 1-5 behavior and the approved design.
- Produces: evergreen ownership/recovery documentation and clean focused verification evidence. No generated files are expected.

- [ ] **Step 1: Update evergreen ownership and recovery docs**

State that the hub continuation resolver owns stable selection, independent proof validation, and older-daemon logical-fragment normalization. State that every page-level and per-turn continuation is exposed as a shape-preserving Manager follow-up. Do not add rollout history, issue numbers, or dated review narrative.

- [ ] **Step 2: Run formatting and focused verification**

Run serially:

```bash
gofmt -w internal/appitempaging/cursor_forms.go internal/appitempaging/window_cursor_test.go cmd/evener-hub/app_item_page_fit.go cmd/evener-hub/app_item_page_fit_test.go cmd/evener-hub/transcript_window_continuation.go cmd/evener-hub/transcript_window_continuation_test.go cmd/evener-hub/session_reads.go cmd/evener-hub/session_tools.go cmd/evener-hub/app_threadread.go cmd/evener-hub/app_threadread_index_test.go cmd/evener-hub/app_rpc_thread_read_test.go cmd/evener-hub/session_reads_test.go
go test ./internal/appitempaging -count=1
go test ./internal/transcriptindex ./server -count=1
go test ./cmd/evener-hub -run '^(TestWindow|TestPackTranscriptItemWindow|TestTranscriptWindow|TestDaemonless.*Window|TestManagerTurns|TestSessionOperations.*(Window|Compatibility|OlderDaemon|OlderRemote)|TestResolveTranscriptWindowContinuation|TestResolveCompatibilityTurnWindow)' -count=1
make docs-check
```

Expected: every command exits 0; test output has no failures, and docs-check completes its documented checks and VitePress build.

- [ ] **Step 3: Run affected package suites and static gates one at a time**

Run serially, never in parallel:

```bash
go test ./cmd/evener-hub/internal/appsource -count=1
go test ./cmd/evener-hub -count=1
make vet
make lint
```

Expected: each command exits 0. If the known under-load voice E2E timeout recurs, rerun only the named test to classify it and preserve CI as the full-suite source of truth under Jesse's existing ruling; do not weaken or skip the gate.

- [ ] **Step 4: Self-audit the approved invariants**

Confirm with code search and tests that no continuation path rereads original `latest`, no second packer exists, both proof forms are exact, remote translation preserves the proof, only legacy network traversal has the eight-page cap, every cursor is structured, and canonical fetch behavior is untouched.

- [ ] **Step 5: Commit docs and any verification-only corrections**

```bash
git add docs/product/subsystems.md docs/automation.md
git commit -m "docs(manager): document continuation resolver ownership"
```

- [ ] **Step 6: Continue the existing delivery pipeline**

Run the required two-reviewer competing implementation PAR on the immutable base/head diff, resolve significant findings red-first, invoke `simplify-code:simplify-code`, rerun affected checks, then shepherd the fresh PR with one push/CI/review/wait per round. Merge only through `scripts/ops/merge-enqueue.sh --pr <N> --wait 570`, verify `MERGED` and the merge SHA, then clean owned scratch and report the six journeys' red-to-green evidence.
