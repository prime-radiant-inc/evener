== panel b8acad95-28fa-4a25-a55c-9707d520e39b head 755f6a1ec outcome review_posted synthesis codex
-- member 0 codex/gpt-5.6-luna type=default status=done job=22412 verdict=0 chars=1183
-- member 1 codex/meta/muse-spark-1.3-contributor type=default status=done job=22413 verdict=1 chars=173
-- member 2 pi/deepseek-4.1-flash-background type=default status=done job=22414 verdict=0 chars=3138

######## member 0 (codex default)
## Review Findings

- **Severity**: Medium  
  **Location**: `agent/session_lifecycle.go:1405`  
  **Problem**: Rejected interrupt markers recompute only `SessionState`; `s.askPending` remains based on in-memory execution. This can leave live pending-ask state inconsistent with the durable transcript and restore behavior after a failed/canceled ask round.  
  **Fix**: Rebuild both state and pending questions from the durable transcript using `deriveRestoredAskPending`, updating them atomically.

---

- **Severity**: Medium  
  **Location**: `agent/session_state.go:300-301`  
  **Problem**: The restore-equivalent state is derived from `s.history`, although `recordTurn` appends to that slice before writing and tolerates write failures. A failed prior append can therefore make live state include a non-durable “phantom” completion that restore will not see.  
  **Fix**: Derive from durable transcript entries, or maintain a history boundary that excludes turns whose transcript append failed.

## Summary

The change improves durable interrupt-marker handling and distinguishes salvage steering, but its rejected-marker fallback can still diverge from durable restore state.

######## member 1 (codex default)
No issues found.
Summary: Durably gates interrupt markers behind transcript admission while isolating salvage as non-resolving across Go, TypeScript, and frontend surfaces.


######## member 2 (pi default)
## Summary

The series makes two coupled changes around interrupted turns:

1. **Durable interrupt-marker admission.** The cancellation branch in `agent/session_lifecycle.go` now appends the "user interrupted" steering marker through `appendSteeringTurnDurablyForOwner` and only clears `askPending` / settles the boundary to `SessionIdle` after the marker is actually admitted. If the marker write is rejected, the error is joined onto the cancellation, the drain is suppressed, and the boundary is settled with `finishProcessingAtRestoredFailureBoundary` (a new helper that derives state from the transcript tail via `deriveRestoredState`, so a live cancellation and a restore agree). The two early `ctx.Done()` exits in `processOneInput` no longer settle the boundary themselves, deferring that to the marker-admission handler.

2. **Salvage is no longer a boundary.** A new `SteeringKindInterruptedSalvage` ("interrupted-salvage") is introduced; `settleInterruptedRound` now stamps the salvage explanation with it instead of `SteeringKindInterrupted`. That keeps the salvage out of `turnResolvesAskBoundary` (Go) and `isResolutionItem` (TS), so only the admitted marker resolves a pending ask. The generated `types.gen.ts` catalog, the web label map, and tests (Go, TS, TUI) are updated.

The implementation is coherent and the new tests exercise the write-failure, retained-write, answered-round, and streamed-salvage paths well.

---

**Severity: low** — `mobile-native/src/timeline.ts:8-15` and `:38-44`

The mobile-native label map is the remaining client that keys off the raw steering kind and was not updated for the new enum value. Before this series the salvage turn carried `interrupted` and `steeringNoticeLabel` returned `"Interrupted"` (and `isInterruptedNotice` matched it); now the same message arrives as `interrupted-salvage`, `steeringNoticeLabel` falls through to `default: return undefined`, and `isInterruptedNotice` returns false. The salvage explanation therefore renders on mobile without its label/compact-disclosure treatment (and `timelineGap` widens it to 24 instead of 8). If unintended, add `case "interrupted-salvage": return "Interrupted draft";` (mirroring the web's `KIND_LABELS`). The content itself is still shown, so impact is cosmetic.

---

**Severity: low** — `cmd/evener-tui/question_overlay_test.go:107-121`

The new test is described as covering that an `interrupted-salvage` explanation does not resolve a pending ask, but it constructs `transcript.ChatMessage{Kind: transcript.MsgSteering, Text: salvage}` — and `ChatMessage` (cmd/evener-tui/internal/transcript/types.go:117-159) has no steering-kind field at all. The TUI's `pendingAskQuestions` already treats *every* `MsgSteering` as non-resolving, so the first assertion is identical to the pre-existing `TestPendingAskQuestions_SteeringDoesNotResolve` and cannot fail if the kind handling changes. The test provides false confidence about covering the new kind. Either drop it or extend the TUI message type/reducer to carry the kind and assert on it. (The render half of the test — that the salvage text is retained — does add value.)
