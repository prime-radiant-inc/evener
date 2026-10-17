// How both clients read a settled transcript item: did it fail, and is it
// still running? The web transcript projector and the native conversation
// projection call this one module, so the same settled command can never
// classify one way in the browser and another on the phone.

import type { TurnError } from "./types.gen";

/**
 * The item fields this classification reads. Both the wire `ThreadItem` and
 * the projected `ItemModel` satisfy it structurally, so neither client has to
 * convert a frame before asking.
 */
export interface ItemFailureSignals {
  readonly error?: string;
  readonly status?: string;
  readonly exitCode?: number;
}

// A shell call's process exit code. The daemon never fabricates exitCode as 0
// (appwire/types.go's ThreadItem.ExitCode doc), so a defined nonzero value is
// an honest signal that the process exited nonzero even when Error is empty
// (e.g. `make test` failing with no denial or exception). A real 0 is a clean
// exit, distinct from undefined — never conflate the two.
//
// This stays a real, honest signal: both clients render it as a small inline
// mark on the call. It is deliberately NOT part of hasItemFailure, because a
// nonzero exit is common and expected (Jesse, 2026-10-16: "don't call out
// failed in lists of tool calls just for nonzero exits ... show a small x
// inline on the call").
export function isNonZeroExit(item: ItemFailureSignals): boolean {
  return typeof item.exitCode === "number" && item.exitCode !== 0;
}

// Whether a turn's `unknown` error (model.ts deliberately keeps it
// wire-type-free) is a real wire TurnError by its one required field. The
// projector's terminal fallback and the web's end-cap renderer
// (turnFailure.ts's asTurnError) both gate on this one narrowing, so a
// malformed or absent error can never count as renderable in one consumer
// and not the other. The daemon always sends a well-formed TurnError on a
// failed turn; an interrupted turn carries none, and anything else is a
// malformed frame treated as no error at all.
export function isTurnError(error: unknown): error is TurnError {
  return error !== null && typeof error === "object" && typeof (error as { message?: unknown }).message === "string";
}

// The item's own status settled as a failure. The wire projects status
// "completed" even for an errored tool call, so this is one signal of two,
// never the whole answer.
export function hasFailureStatus(item: ItemFailureSignals): boolean {
  return item.status === "failed" || item.status === "interrupted";
}

// The item carries error text worth showing. Blank-but-present error strings
// come off the wire (a tool that failed with nothing to say), and rendering
// one puts an empty error block on the row, so "present" is not the question
// — "non-blank" is.
export function hasErrorText(item: ItemFailureSignals): boolean {
  return item.error !== undefined && item.error.trim() !== "";
}

// A settled call whose ONLY problem is a nonzero exit: it did not carry error
// text and its status is not failed/interrupted. This is the inline-mark rule
// (Jesse, 2026-10-16): such a call must not read as failed, but the exit stays
// visible as a small x on the call. It is deliberately expressed as the exact
// negation of the other two failure terms plus a nonzero exit, so the two
// clients can never disagree about which calls get the mark.
export function isExitOnly(item: ItemFailureSignals): boolean {
  return isNonZeroExit(item) && !hasErrorText(item) && !hasFailureStatus(item);
}

// A settled item is a failure when the tool result itself carried a non-blank
// error message, OR the item's own status settled as failed/interrupted. A
// non-zero exit is NOT one of the terms: it is common and expected (a grep
// that finds nothing, a check inside a passing run), so on its own it must not
// raise a call to failed. Both clients still show it, as a small inline x on
// the call (isExitOnly above) — visible, not alarming.
export function hasItemFailure(item: ItemFailureSignals): boolean {
  return hasErrorText(item) || hasFailureStatus(item);
}

// The wire sends "inProgress" for a still-executing turn or item, never
// "running" — every turn and item active-status check in both clients shares
// this predicate so they cannot drift apart.
export function isInProgressStatus(status: string | undefined): boolean {
  return status === "inProgress";
}

// Whether an item is still in flight. The item's own status decides when it
// has one; the turn's status covers an older or partial item frame that has
// not carried its item status yet. Shared by both projections — the web's
// transcript entries and the phone's rows (mobile/src/conversation) — so a
// second copy of "is this still running" is not how the two transcripts
// start disagreeing about a streaming row. Takes ItemFailureSignals, not a
// full ItemModel, so either host's item shape satisfies it without a
// conversion wrapper at the call site.
export function isActiveItem(item: ItemFailureSignals, turnStatus: string | undefined): boolean {
  return isInProgressStatus(item.status) || (isInProgressStatus(turnStatus) && item.status === undefined);
}

/**
 * The status to show for a turn (spec "Turn status": running state lives in
 * the overlay). A recorded open turn (`inProgress`) shows as running only while
 * it is the thread's running turn, and as `interrupted` otherwise: a daemonless
 * read of a turn a crash left open, or a turn whose execution stood down, never
 * shows as running forever. Every other status shows as recorded. A display
 * rule only: the result is never stored or merged back into the model.
 */
export function displayTurnStatus(
  turn: { readonly id: string; readonly status: string },
  thread: { readonly runningTurnId?: string },
): string {
  if (!isInProgressStatus(turn.status) || turn.id === thread.runningTurnId) return turn.status;
  return "interrupted";
}
