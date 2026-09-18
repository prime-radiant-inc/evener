// How both clients read a settled transcript item: did it fail, and is it
// still running? The web transcript projector and the native conversation
// projection call this one module, so the same settled command can never
// classify one way in the browser and another on the phone.

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
// an honest failure signal even when Error is empty (e.g. `make test` failing
// with no denial or exception). A real 0 is a clean exit, distinct from
// undefined — never conflate the two.
export function isNonZeroExit(item: ItemFailureSignals): boolean {
  return typeof item.exitCode === "number" && item.exitCode !== 0;
}

// The item's own status settled as a failure. The wire projects status
// "completed" even for an errored tool call, so this is one signal of three,
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

// A settled item is a failure when the tool result itself carried a non-blank
// error message, OR the item's own status settled as failed/interrupted, OR
// the process behind it exited nonzero.
export function hasItemFailure(item: ItemFailureSignals): boolean {
  return hasErrorText(item) || hasFailureStatus(item) || isNonZeroExit(item);
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
