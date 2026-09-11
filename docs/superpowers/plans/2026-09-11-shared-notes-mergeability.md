# Shared Notes Mergeability Repair Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve independently reproduced PR #1070 blockers while preserving supported read access and retry durability.

**Architecture:** Use the existing reservation prepare callback to durably reject unknown URL targets before any recoverable in-flight reservation. Gate Notes read navigation on the authoritative sharedNotes capability, independently of write liveness. Repair the incoming main test fixture without changing its assertions.

**Tech Stack:** Go, TypeScript, React, Vitest, existing mutation journal and thread store.

**Spec:** `docs/superpowers/specs/2026-09-08-shared-notes-design.md:108-147`; `docs/superpowers/specs/2026-09-10-shared-notes-simplify-design.md`.

## Global Constraints

- Preserve all existing behavior outside the reproduced findings. No backward compatibility, migration, schema expansion, editor/outbox redesign, or dependency upgrades.
- Capability unset hides Notes. Supported ended/closed/notLoaded sessions retain read-only notes and URLs, without edit/remove controls.
- URL retry success for an existing removed entry must survive interruption after metadata save and before receipt persistence.
- Read `docs/developing-evener/testing.md`. Use real components/stores and disposable filesystem fixtures.
- Main is integrated locally as `3f6b84e5cbc07e82db15c1a984670bfaf5e8c6fd`; do not merge PR, force-push, weaken strict-base checks, or manufacture human approval.

## Task 1: Complete incoming file-link fixture

**Files:** `cmd/evener-hub/frontend/src/panes/session/transcript/messages/agentFileLinks.test.tsx`.
**Interface:** Existing `ThreadModel` fixture; production interfaces unchanged.

- [x] Run integrated baseline. Typecheck fails on required `sharedNotes`, then exposes missing note fields after the nested capability object is corrected.
- [x] Complete the fixture with the branch's required empty values:

```typescript
capabilities: { /* existing fields retained */ sharedNotes: false },
humanNote: "",
agentNote: "",
sessionUrls: [],
```

- [x] Run from frontend: `npx biome check --write src/panes/session/transcript/messages/agentFileLinks.test.tsx && npx vitest run src/panes/session/transcript/messages/agentFileLinks.test.tsx --maxWorkers=4 && npm run typecheck`. All must exit zero; preserve all 48 assertions/tests.
- [x] Independently review; stage that exact file and commit the fixture repair (`696d45cf3`).

## Task 2: Validate URL targets before recoverable reservation

**Files:** `agent/session_notes_rpc.go`; new `agent/session_notes_mergeability_test.go`.
**Interfaces:** Existing `RemoveSessionURL(outerID, id string) (bool, error)` and `reservePrepared(request, prepare)`; no new wire or snapshot fields.

- [x] Run known-good `GOMAXPROCS=4 go test -p 4 ./agent -run '^TestHumanNoteAtomicDurability$' -count=1`.
- [x] Add `TestURLRemoveReservationCrashUnknownTarget`: actual API call with AfterReservation fault, store reload, two retries expecting false plus InvalidParams. Add existing-target controls for normal replay, reservation interruption and pre-receipt interruption.
- [x] Run `GOMAXPROCS=4 go test -p 4 ./agent -run '^TestURLRemove(ReservationCrashUnknownTarget|ExistingTargetRecovery)$' -count=1 -v`. Unknown-target regression must fail, controls pass. Receipt interruption returns true plus error by the existing applyUrlsRemoveResult contract; assert that exact pair.
- [x] Under notesUpdateMu, capture the current committed URL list and use the existing prepare callback to journal rejection for an absent target. Release notesUpdateMu before existing join/replay handling. The later mutation/save/rollback critical section stays intact. Reuse one InvalidParams value across fresh validation and the later absent-target branch.

```go
unknown := appwire.InvalidParams("no URL entry with id " + id)
s.notesUpdateMu.Lock()
urls := s.snapshotSessionURLsLocked()
lookup, err := s.clientMutations.reservePrepared(request, func(_ *clientMutationSnapshot, record *clientMutationRecord) error {
    if !slices.ContainsFunc(urls, func(entry schema.SessionURL) bool { return entry.ID == id }) {
        rejectClientMutation(record, unknown)
    }
    return nil
})
s.notesUpdateMu.Unlock()
```

This callback only runs for unseen mutation IDs. Completed records still replay, payload mismatch still conflicts, and in-flight takeover proves the target previously passed validation. Update the existing recovery comment to state this invariant accurately.

- [x] Run unchanged regressions, focused notes/mutation tests, and `GOMAXPROCS=4 go test -race -p 4 ./agent -run '(Notes|Note|URL|Urls|ClientMutation)' -count=1`. Review callback lock ordering and concurrent removal controls independently.
- [x] Stage only the two named Go files and commit after review (`b766b70b8`).

## Task 3: Gate Notes navigation by read capability

**Owner:** Existing isolated UI investigator `dlg_034MjbmZzzHvXx5XlCA79A`; parent owns review/integration.
**Production files:** `cmd/evener-hub/frontend/src/protocol/sharedNotesAvailability.ts` (new), `shell/sessionMenu/SessionMenu.tsx`, `shell/rail/RailRow.tsx`, `shell/palette/commands.ts`, `panes/session/chrome/SessionChrome.tsx`, `panes/session/chrome/NotesPanel.tsx` under the same frontend `src/` prefix.
**Tests:** Existing corresponding SessionMenu tests, Rail tests, commands.test.ts, SessionChrome.test.tsx and NotesPanel.test.tsx. No other production files authorized.
**Interfaces:** New `canReadSharedNotes(model: Pick<ThreadModel, "capabilities"> | undefined): boolean` and required `SessionMenuProps.canReadNotes: boolean`.

- [x] Investigator runs unchanged 127-test smoke, adds actual UI/store regressions: unsupported palette availability/invocation, desktop/menu/mobile/standalone blank panel; supported live/ended/closed/notLoaded read/edit controls.
- [x] Parent inspects complete test diff and independently reruns it: five failures, 140 passes. Ended read access is valid and must remain.
- [ ] Add tests for unhydrated capability, rail false/unknown/true hydration transition, and imperative opener gating before implementation. Unhydrated rail rows have no capability in navigation summaries. Use the existing reactive thread store, not a new wire field, live-status inference, or fetch per rail row. Opening the session loads its authoritative model; its Notes menu then becomes available when supported.
- [ ] Implement the shared read predicate:

```typescript
import type { ThreadModel } from "./model";
export function canReadSharedNotes(model: Pick<ThreadModel, "capabilities"> | undefined): boolean {
  return model?.capabilities.sharedNotes === true;
}
```

Pass it through both shared-menu adapters. Omit only the Notes row when false. Guard direct chrome/standalone imperative opening as well as visible affordances. Set `capability: "sharedNotes"` on the notes command, guard direct invocation with the current model, and make unresolved Notes capability unavailable without changing other commands' unhydrated policy. Reuse the predicate in NotesPanelBody. Preserve read-only controls, current pane lifetimes and all editor state.

- [ ] Run the original four-suite command plus new rail/menu tests; all original and new assertions must pass. Run touched-file Biome and canonical `GOMAXPROCS=4 make test-web`.
- [ ] Independently review, commit named paths only, then parent safely integrates under exact branch/ref/ancestry checks.

## Final acceptance and delivery

- [ ] On the integrated tree run `GOMAXPROCS=4 make merge-approval-gate`, `GOMAXPROCS=4 make vet`, relevant race tests, and `TMPDIR=/tmp GOMAXPROCS=4 make test-web-browser`. Inspect all output; do not waive failures.
- [ ] Verify generated freshness and clean tracked tree. Recheck remote URL, exact PR branch/head, ancestry and current main. Normal push only.
- [ ] Post concrete finding resolutions and the ended-read clarification to PR #1070. Monitor actual current-head CI and mutable Roborev comment, including reviewer completion, rather than trusting a status badge.
- [ ] Recheck strict-base readiness and one required human approval. Report remaining blockers honestly; do not merge the PR.
