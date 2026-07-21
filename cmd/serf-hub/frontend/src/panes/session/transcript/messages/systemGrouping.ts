// Consecutive-run detection for systemMessage items within one turn -
// SystemNoticeItem.tsx's grouping mechanic. Grouping is derived fresh from
// turn.items on every render (no persisted run state of its own), so it's
// naturally correct as items stream in - mirrors legacy's
// ensureSystemRun/coalesceSystemRun "adjacency-only" run continuation
// (parity-m4-transcript.md #9: any other item type in between forces a new
// run) without needing a stateful accumulator.
import type { ItemModel } from "../../../../protocol/model";

export interface SystemRun {
  items: ItemModel[];
  /** True only for the run's first member - the one that renders the
   * group's disclosure (or, for a sub-threshold run, is simply the first
   * of several standalone lines). Every other member renders nothing of
   * its own once grouped - see SystemNoticeItem. */
  isFirst: boolean;
}

// Below this many adjacent systemMessage items, each renders as its own
// standalone quiet line - no grouping chrome. Parity: contracts-transcript-
// scroll-liveness.md #12 / test-system-churn.js ("fewer than 3 adjacent
// lifecycle events do not coalesce").
const MIN_GROUP_SIZE = 3;

function isSystemMessage(item: ItemModel): boolean {
  return item.type === "systemMessage";
}

// systemRunFor finds the contiguous run of systemMessage items in
// `turnItems` that contains `itemId`. Returns undefined when the item
// itself isn't a systemMessage (or isn't found at all) - callers only ever
// invoke this for an item they've already dispatched as "systemMessage",
// so that's a defensive case, not an expected one.
export function systemRunFor(turnItems: ItemModel[], itemId: string): SystemRun | undefined {
  const index = turnItems.findIndex((it) => it.id === itemId);
  const item = turnItems[index];
  if (!item || !isSystemMessage(item)) return undefined;

  let start = index;
  while (start > 0) {
    const prev = turnItems[start - 1];
    if (!prev || !isSystemMessage(prev)) break;
    start--;
  }
  let end = index;
  while (end < turnItems.length - 1) {
    const next = turnItems[end + 1];
    if (!next || !isSystemMessage(next)) break;
    end++;
  }

  return { items: turnItems.slice(start, end + 1), isFirst: start === index };
}

export function shouldGroup(run: SystemRun): boolean {
  return run.items.length >= MIN_GROUP_SIZE;
}
