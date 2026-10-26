import type { ThreadModel, TurnModel } from "@evener/appwire-client";
import { isAtBottom, type ScrollMetrics } from "./scrollMetrics";

export function totalItemCount(model: ThreadModel | undefined): number {
  if (!model) return 0;
  let total = 0;
  for (const turn of model.turns) total += turn.items.length;
  return total;
}

// Attention follows liveness.ts's raw-status mapping to needs-you.
// shell/sessionStatus.ts maps normalized navigation summaries.
// askPending is independent because it need not coincide with status.type.
export function isAttentionWorthy(model: ThreadModel | undefined): boolean {
  if (!model) return false;
  return (
    model.askPending ||
    model.status.type === "awaiting" ||
    model.status.type === "warning" ||
    model.status.type === "restartRequired"
  );
}

// The error-anchor's failure signal is TURN-level (TurnModel.status/error,
// stamped by turn/completed - see reducer.test.ts's EventError-shape tests),
// not a tool-call-level "did this one call fail"
// (contracts-transcript-scroll-liveness.md §5 line 114 talks about a
// finalized tool call; turn.status === "failed" is the equivalent signal,
// confirmed against appwire/types.go's TurnStatusFailed). error is
// `unknown` on TurnModel, so this only ever checks presence, never shape.
export function isFailedTurn(turn: TurnModel): boolean {
  return turn.status === "failed" || turn.error !== undefined;
}

// A COUNT (not a boolean, not "the first failed turn's index/id") - failed-
// ness is terminal (a turn never un-fails) and turns only ever append or
// get prepended, never removed, so this only ever goes UP. Deliberately a
// count rather than the first-failed index: a turn's own failure can be
// resolved (seen live at the bottom, or anchored-then-cleared) while an
// EARLIER-index turn stays the "first" failed one by array position -
// pinning the dependency to "the first index" would then never change
// again and a genuinely new, later failure would never get a chance to be
// evaluated. Recomputed every render (same O(turns) cost class as
// totalItemCount above) but its VALUE is identical across a pure streaming
// delta (which touches item text/pendingText, never turn.status/error), so
// it doesn't defeat the "per-delta work never re-runs the content-changed
// effect" property - see that effect's own dependency-array comment.
export function failedTurnCount(model: ThreadModel | undefined): number {
  if (!model) return 0;
  let n = 0;
  for (const turn of model.turns) if (isFailedTurn(turn)) n++;
  return n;
}

/**
 * The scrollTop change a vertical input asks for: 1 toward the bottom, -1 toward
 * the top. A wheel with positive deltaY and a finger moving UP both push content
 * down, so both are 1.
 */
type ScrollDirection = 1 | -1;

/** PointerEvent.buttons bits. A drag is only ever begun for these two. */
const PRIMARY_BUTTON_BIT = 1;
export const MIDDLE_BUTTON_BIT = 4;
export const DRAG_BUTTON_BITS = PRIMARY_BUTTON_BIT | MIDDLE_BUTTON_BIT;

/**
 * Can the PORT still move that way? Uses the same at-bottom band the rest of the
 * hook decides by, so this agrees with what wasAtBottom will say. The band's
 * error here is safe: a port within the band of its bottom reads as "cannot
 * move", so a downward input goes unmarked.
 */
function portCanScroll(metrics: ScrollMetrics, direction: ScrollDirection): boolean {
  if (direction === -1) return metrics.scrollTop > 0;
  return !isAtBottom(metrics);
}

/**
 * Has this element any room left that way, EXACTLY - no tolerance band.
 *
 * The band's error inverts for a nested candidate: a scroller three pixels from
 * its own bottom would read as "at its limit", the walk below would let the
 * input through, the port would mark - and then that scroller would eat the
 * wheel and the port would never move. That is precisely the false veto this
 * predicate must never produce, so the nested question is asked exactly.
 */
function hasRoomToScroll(el: Element, direction: ScrollDirection): boolean {
  if (direction === -1) return el.scrollTop > 0;
  return el.scrollHeight - el.scrollTop - el.clientHeight > 0;
}

/** An element that scrolls vertically on its own, independently of the port. */
function isIndependentVerticalScroller(el: Element): boolean {
  if (el.scrollHeight <= el.clientHeight) return false;
  const overflowY = getComputedStyle(el).overflowY;
  return overflowY === "auto" || overflowY === "scroll";
}

/**
 * Can a vertical input landing on `target` actually move `port`? Two ways it
 * cannot:
 *
 * The port's own geometry arrives as `portMetrics` rather than being read off
 * the element: the at-bottom decision goes through this hook's injectable
 * measurement seam everywhere else, for the reason scrollMetrics.ts states (a
 * jsdom element's layout is all zeroes, so a helper that takes the element
 * cannot be tested honestly). The nested walk below has no such seam and reads
 * the DOM directly - there is nothing it could be measured through, and its
 * answer is about elements this hook never otherwise looks at.
 *
 *   the port is already at that limit; or
 *   something between the target and the port is an independent vertical
 *   scroller with room left in that direction, so IT answers the input and the
 *   event merely bubbles past (the sandbox-escalation panel is overflow-y:auto
 *   inside the transcript).
 *
 * Both are read from state that exists BEFORE the browser scrolls, which is a
 * plain read of the present - not the after-the-fact geometry inference this
 * design replaced. A nested scroller with NO room that way passes the input on,
 * so that case still counts as a gesture.
 *
 * Marking therefore implies the port has room beyond the at-bottom band AND
 * nothing on the way to it has any room to answer the input first - so a mark
 * is a scroll the port will really feel, and being wrong costs an unmarked real
 * scroll rather than a false veto. That matters because a false veto disarms the
 * bottom-hold correction until the reader returns to the bottom, where an
 * unmarked scroll costs a single frame.
 *
 * A nested scroller with LESS room than the input's delta does not split it:
 * Chrome scrolls that element to its own limit and keeps the remainder, chaining
 * to the port only on a LATER event, which then finds no room here and marks.
 * Measured in the browser guards' own headless Chrome 153.0.8010.36 - a 120px
 * wheel over a scroller with 3px left moved the scroller 3px and the port not at
 * all - so "has any room" is the right question to ask, not "has room enough".
 * Latching is browser-defined; an engine that split the delta instead would make
 * this under-mark, which is the safe direction.
 *
 * The one band left: an ancestor with overscroll-behavior other than `auto`
 * refuses to chain the input onward when it reaches its own limit, so it can
 * swallow an input this says will reach the port. Not modelled - computing it
 * would mean reading a second property per ancestor for a case the transcript
 * does not currently build.
 */
export function verticalInputCanMovePort(
  port: HTMLElement,
  portMetrics: ScrollMetrics,
  target: EventTarget | null,
  direction: ScrollDirection,
): boolean {
  if (!portCanScroll(portMetrics, direction)) return false;
  let node = target instanceof Element ? target : null;
  while (node !== null && node !== port) {
    if (isIndependentVerticalScroller(node) && hasRoomToScroll(node, direction)) return false;
    node = node.parentElement;
  }
  return true;
}
