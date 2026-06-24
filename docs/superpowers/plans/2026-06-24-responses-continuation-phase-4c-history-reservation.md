# Responses Continuation Phase 4C History Reservation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the history-base reservation check required by the Phase 0A serialization audit before later phases rely on continuation anchor selection.

**Architecture:** Keep runtime continuation disabled. Add a small session-owned reservation value in `agent` that captures the history base used for continuation candidate selection and can validate that the dispatch-time history still has the same base. Phase 4C does not call the helper from `prepareModelRequest`; it only proves the guard semantics that Phase 4D-i/4D-ii will use when they start selecting anchors.

**Tech Stack:** Go, deterministic unit tests, existing `agent/schema.Turn` metadata.

---

## Dependency Recheck

Phase 0A proof recorded:

- `reservation required: yes`;
- current code is serialized within one `ProcessInputKind` invocation;
- public `Session` API does not provide a history-base reservation spanning future anchor selection to dispatch.

Phase 4C therefore implements a reservation primitive, not just a regression note.

## File Structure

- Modify: `agent/responses_continuation_eligibility.go`
  - Add `responsesContinuationHistoryReservation`.
  - Add `reserveResponsesContinuationHistoryBase(history []schema.Turn)`.
  - Add `responsesContinuationHistoryBaseStillCurrent(reservation, history []schema.Turn)`.
- Modify: `agent/responses_continuation_eligibility_test.go`
  - Add deterministic reservation tests.
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4c.md`
  - Record proof commands and the Phase 0A dependency recheck.

## Non-Goals

- Do not call the reservation helper from `prepareModelRequest`.
- Do not select runtime anchors.
- Do not set `llm.Request.PreviousResponseID`.
- Do not send `responses_delta`.
- Do not add a global `ProcessInputKind` single-flight lock.
- Do not enable production endpoint-family registry entries.
- Do not add live provider tests.

## Reservation Shape

Add this private shape:

```go
type responsesContinuationHistoryReservation struct {
	TurnCount int
	LastKind  schema.TurnKind
	LastStamp time.Time
}
```

Use `TurnCount` plus the last committed turn's kind/timestamp as the current Phase 4C base identity. This is intentionally narrow: later runtime phases only need to reject continuation if another turn commits after anchor selection. The helper should treat an empty history as a valid base with `TurnCount=0`.

Add:

```go
func reserveResponsesContinuationHistoryBase(history []schema.Turn) responsesContinuationHistoryReservation
func responsesContinuationHistoryBaseStillCurrent(reservation responsesContinuationHistoryReservation, history []schema.Turn) bool
```

Validation rules:

- same turn count and same last turn kind/timestamp: current;
- longer history: stale;
- shorter history: stale;
- same length but different last committed turn identity: stale;
- empty history remains current only while still empty.

---

### Task 1: Add Reservation Tests

**Files:**
- Modify: `agent/responses_continuation_eligibility_test.go`

- [ ] **Step 1: Add failing tests**

Add tests:

```go
func TestResponsesContinuationHistoryReservationStillCurrentForSameBase(t *testing.T)
func TestResponsesContinuationHistoryReservationRejectsAppendedTurn(t *testing.T)
func TestResponsesContinuationHistoryReservationRejectsCompactedOrShortenedHistory(t *testing.T)
func TestResponsesContinuationHistoryReservationRejectsSameLengthDifferentLastTurn(t *testing.T)
func TestResponsesContinuationHistoryReservationAllowsEmptyHistoryUntilChanged(t *testing.T)
```

Use existing `responsesContinuationEligibleAssistantTurn` for committed assistant turns, and set deterministic timestamps on replacement turns.

Expected first run:

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestResponsesContinuationHistoryReservation' -count=1 -v
```

Expected: FAIL because the reservation helper does not exist.

- [ ] **Step 2: Implement the reservation helper**

Add the helper to `agent/responses_continuation_eligibility.go`. Keep it private and pure.

- [ ] **Step 3: Verify Task 1**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestResponsesContinuationHistoryReservation|TestResponsesContinuationAnchorCandidate' -count=1 -v
git diff --check
```

- [ ] **Step 4: Commit**

```sh
git status --short
git add agent/responses_continuation_eligibility.go agent/responses_continuation_eligibility_test.go
git commit -m "feat(agent): add responses continuation history reservation"
```

---

### Task 2: Proof and Verification

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4c.md`
- Modify: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-4c-history-reservation.md`

- [ ] **Step 1: Add proof artifact**

Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4c.md`:

```markdown
# Responses Continuation Phase 4C Proof

## Scope

Phase 4C adds a pure history-base reservation guard for future continuation anchor selection. Runtime continuation remains disabled.

## Dependency Recheck

Phase 0A recorded `reservation required: yes`; Phase 4C implements the reservation primitive required before Phase 4D-i/4D-ii can depend on anchor selection.

## Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestResponsesContinuationHistoryReservation|TestResponsesContinuationAnchorCandidate' -count=1 -v
```

Result: pass.

```sh
git diff --check
```

Result: pass.

## Contracts Proven

- The same history base validates.
- Appending a turn after reservation invalidates the base.
- Shortening or compacting history invalidates the base.
- Same-length replacement of the last committed turn invalidates the base.
- Empty history validates only while it remains empty.
- Runtime continuation remains disabled; no request-shaping path calls the reservation helper.
```

- [ ] **Step 2: Run focused verification**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestResponsesContinuationHistoryReservation|TestResponsesContinuationAnchorCandidate' -count=1 -v
git diff --check
```

- [ ] **Step 3: Mark this plan complete and commit**

Update all completed checkboxes in this plan, then:

```sh
git status --short
git add docs/superpowers/plans/2026-06-24-responses-continuation-phase-4c-history-reservation.md docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4c.md
git commit -m "docs: record responses continuation phase 4c proof"
```

## Self-Review

- Spec coverage: addresses Phase 0A's `reservation required: yes` verdict with deterministic reservation tests.
- Runtime safety: no `previous_response_id`, no `responses_delta`, no production registry enablement, and no live provider calls.
- Test quality: tests structured reservation semantics directly instead of asserting rendered requests or logs.
