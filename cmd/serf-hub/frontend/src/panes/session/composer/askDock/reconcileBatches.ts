// reconcileBatches is the stateful half of the dock's multi-pending-set
// bookkeeping (wave-5 plan T4: "late-arriving questions never swept into an
// in-flight settlement"). deriveAskQuestions.ts's liveAskQuestions gives a
// purely POSITIONAL snapshot of "what the transcript currently says is
// live" - sufficient for cold attach and the common (nothing in flight)
// case, but not for the in-flight submission race: while our own composed
// answer's turn/start round-trip is pending, its eventual echo (a fresh
// userMessage item) and a SIBLING ask_user call's own ack are both being
// appended to the SAME transcript by two independent, unordered wire
// events. Position alone can't tell "my own reply landed, resolving
// everything before it" apart from "a new question arrived that has
// nothing to do with my in-flight send" when the two race - see
// askDockStore.ts's own comment for the concrete interleaving this
// protects against.
//
// The fix is identity, not position: once a batch is marked `sending`, its
// keys are frozen and immune to the live-set signal entirely (never
// pruned, never merged into) until ITS OWN submission settles (handled
// directly by whoever owns that promise - askDockStore.sendBatch - not by
// this function). Any newly-live key not already tracked by an existing
// batch joins the single open (non-sending) batch if one exists, or mints
// a fresh one if every existing batch is currently sending (or none exist
// yet) - this is what lets a late-arriving question remain independently
// answerable and independently sendable rather than being folded into
// whichever batch happens to be mid-flight.
export interface AskBatch {
  id: string;
  keys: string[]; // preserves global posting order within this batch
  sending: boolean;
  sendError?: string;
}

// pruneBatch drops keys no longer present in the live set, but only for a
// batch that isn't sending (a sending batch's keys are frozen - see this
// file's header). Returns the SAME object when nothing was actually
// removed, so the caller's own same-reference check can short-circuit a
// re-render when reconciliation was a pure no-op.
function pruneBatch(b: AskBatch, liveSet: ReadonlySet<string>): AskBatch {
  if (b.sending) return b;
  const kept = b.keys.filter((k) => liveSet.has(k));
  return kept.length === b.keys.length ? b : { ...b, keys: kept };
}

function sameBatches(a: AskBatch[], b: AskBatch[]): boolean {
  return a.length === b.length && a.every((batch, i) => batch === b[i]);
}

export function reconcileBatches(prevBatches: AskBatch[], liveKeys: string[], mintId: () => string): AskBatch[] {
  const liveSet = new Set(liveKeys);
  const pruned = prevBatches.map((b) => pruneBatch(b, liveSet)).filter((b) => b.sending || b.keys.length > 0);

  const tracked = new Set(pruned.flatMap((b) => b.keys));
  const newKeys = liveKeys.filter((k) => !tracked.has(k));

  if (newKeys.length === 0) {
    return sameBatches(prevBatches, pruned) ? prevBatches : pruned;
  }

  const openIndex = pruned.findIndex((b) => !b.sending);
  if (openIndex === -1) {
    return [...pruned, { id: mintId(), keys: newKeys, sending: false }];
  }
  return pruned.map((b, i) => (i === openIndex ? { ...b, keys: [...b.keys, ...newKeys] } : b));
}
