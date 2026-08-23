// ordering.ts — comprehension view ordering: parent leftmost, then
// most-recent-activity with 60s hysteresis + FLIP animation.
//
// Ported from desiredCompOrder() in the reference implementation. The
// hysteresis prevents flicker: a challenger must be clearly (≥ margin) more
// recent before a swap happens, so the order doesn't jump on every event.
//
// Pure, no React, no DOM. Unit-testable.

import { REORDER_MARGIN_MS } from "./axis";

/**
 * A session entry for ordering — the minimal data needed to sort rails.
 */
export interface OrderableSession {
  /** Unique index or key. */
  index: number;
  /** True if this is the parent session (always leftmost). */
  isParent: boolean;
  /** Last activity timestamp (epoch ms). 0 if unknown. */
  lastActivityMs: number;
}

/**
 * Compute the desired order of sessions for the comprehension view.
 *
 * Parent is always leftmost. Remaining sessions sort by most-recent-activity
 * with 60s hysteresis: a swap only happens when the challenger is clearly
 * (≥ REORDER_MARGIN_MS) more recent than the current occupant.
 *
 * @param sessions All sessions (parent + children).
 * @param nowMs The current "now" timestamp (epoch ms) for recency comparison.
 * @param currentOrder The current order (indices), so hysteresis is applied
 *   relative to the existing arrangement.
 * @returns The new order as an array of indices.
 */
export function desiredCompOrder(sessions: OrderableSession[], nowMs: number, currentOrder: number[]): number[] {
  const parentIdx = sessions.findIndex((s) => s.isParent);
  const parent = parentIdx >= 0 ? parentIdx : 0;

  // Recency score: how recently was each session active? Lower = more idle.
  // lastActivityMs that is 0 (unknown) sorts as least recent.
  const recency = (idx: number): number => {
    const s = sessions[idx];
    // Unknown lastActivityMs (0) sorts as least recent: return +Infinity
    // (maximum idle time) so it never swaps ahead of a session with known
    // activity.
    if (!s?.lastActivityMs) return Number.POSITIVE_INFINITY;
    return nowMs - s.lastActivityMs;
  };

  // Start from the current order, excluding the parent (which is always first).
  const rest = currentOrder.filter((i) => i !== parent);

  // Bubble-sort with hysteresis: swap only when the challenger is clearly
  // (≥ margin) more recent (i.e., has LOWER recency value = less idle time).
  let swapped = true;
  while (swapped) {
    swapped = false;
    for (let k = 0; k + 1 < rest.length; k++) {
      const a = rest[k];
      const b = rest[k + 1];
      if (a === undefined || b === undefined) continue;
      // Swap b ahead of a only if b is clearly more recent:
      // b's recency (idle time) must be less than a's recency minus margin.
      if (recency(b) < recency(a) - REORDER_MARGIN_MS) {
        rest[k] = b;
        rest[k + 1] = a;
        swapped = true;
      }
    }
  }

  return [parent, ...rest];
}
