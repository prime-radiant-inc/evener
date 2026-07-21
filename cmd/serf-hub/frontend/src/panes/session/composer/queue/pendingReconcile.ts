// Pure reconciliation algorithm for optimistic pending turn submissions
// (send/steer/queue/drain), ported from the legacy optimistic-pending
// registry's own matching rules (cmd/serf-hub/assets/pending.js's
// `tryReconcile`/`tryReconcileQueue`) but re-expressed against ThreadModel
// diffs instead of raw notifications - pendingTurnsStore.ts subscribes to
// the threads store (wire truth) rather than the client directly (a wave
// binding constraint), so reconciliation here works by comparing the
// PREVIOUS and CURRENT ThreadModel snapshots for one ref, not by pattern-
// matching individual notification payloads.
//
// Every function in this file is pure and side-effect-free: given the same
// inputs, it always computes the same list of entry ids to resolve, and
// never mutates its arguments. pendingTurnsStore.ts owns turning that list
// into an actual store update.
import type { ItemModel, ThreadModel } from "../../../../protocol/model";
import { normalizeText, queueEntryPreviewText } from "./queueDisplay";

export type PendingMethod = "send" | "steer" | "queue" | "drain";

// The raw, un-normalized data one optimistic submission was registered
// with. Deliberately minimal/display-agnostic - queueDisplay.ts's
// queueEntryPreviewText computes whatever a UI or a matching rule needs from
// text+imageCount, so this shape never needs a redundant cached label.
export interface PendingTurnEntry {
  id: string;
  ref: string;
  method: PendingMethod;
  text: string;
  imageCount: number;
  createdAt: number;
}

// collectItemIds gathers every item id present anywhere in `model` (across
// every turn), or an empty set for an undefined model (the "no prior
// snapshot yet" case - e.g. this ref's very first observed change). Used by
// callers as the "prior" baseline passed into computeReconciledIds below.
export function collectItemIds(model: ThreadModel | undefined): Set<string> {
  const ids = new Set<string>();
  if (!model) return ids;
  for (const t of model.turns) {
    for (const it of t.items) ids.add(it.id);
  }
  return ids;
}

// collectNewItems returns every item in `model` not present in `priorIds`,
// in turn order then item order - the deterministic FIFO order every
// matching rule below relies on (mirrors legacy's own Map-iteration-order
// FIFO for drain's first-come-first-served rule).
function collectNewItems(model: ThreadModel, priorIds: Set<string>): ItemModel[] {
  const fresh: ItemModel[] = [];
  for (const t of model.turns) {
    for (const it of t.items) {
      if (!priorIds.has(it.id)) fresh.push(it);
    }
  }
  return fresh;
}

// --- queue-method reconciliation ---------------------------------------

// buildQueueMultiset turns the daemon's authoritative queue arrays into a
// normalized-text -> remaining-count map, so duplicate-text entries
// reconcile one-for-one rather than one preview entry confirming every
// chip that happens to share its text (legacy's own tryReconcileQueue
// comment: "Duplicate texts are consumed one-for-one").
//
// texts (the full, untruncated per-entry text - QueueState.texts) is
// preferred over preview (server-truncated to the entry's first line -
// QueueState.preview) whenever both are present: a pending entry's own text
// is always the user's full, untruncated input, so matching against the
// full wire text is strictly more correct for a multi-line message than
// matching against its first line only. This is a deliberate improvement
// over the legacy JS registry (which only ever had the truncated preview
// array available to it) rather than a faithful bug-for-bug port - falling
// back to `preview` keeps old-daemon compatibility where `texts` is absent.
function buildQueueMultiset(queue: ThreadModel["queue"]): Map<string, number> {
  const wanted = new Map<string, number>();
  const source = queue?.texts ?? queue?.preview ?? [];
  for (const raw of source) {
    const key = normalizeText(raw) || raw; // empty raw stays "" -> won't match a real placeholder key below
    wanted.set(key, (wanted.get(key) ?? 0) + 1);
  }
  return wanted;
}

function reconcileQueueEntries(entries: PendingTurnEntry[], queue: ThreadModel["queue"]): string[] {
  const wanted = buildQueueMultiset(queue);
  const resolved: string[] = [];
  for (const entry of entries) {
    const key = queueEntryPreviewText(entry.text, entry.imageCount);
    const remaining = wanted.get(key) ?? 0;
    if (remaining <= 0) continue;
    wanted.set(key, remaining - 1);
    resolved.push(entry.id);
  }
  return resolved;
}

// --- send-method reconciliation (new userMessage items) -----------------

// matchesSendEntry mirrors legacy's tryReconcile "turn/start" branch
// (pending.js:191-194): a blank incoming text only matches an entry that is
// ALSO textless and where BOTH sides report at least one image (an
// image-only send matched against an image-only echo, ignoring exact
// counts); a non-blank incoming text requires an exact normalized-text
// match, images notwithstanding.
function matchesSendEntry(entry: PendingTurnEntry, itemText: string, itemImageCount: number): boolean {
  const want = normalizeText(itemText);
  if (!want) return normalizeText(entry.text) === "" && entry.imageCount > 0 && itemImageCount > 0;
  return normalizeText(entry.text) === want;
}

function findAndConsume(
  entries: PendingTurnEntry[],
  consumed: Set<string>,
  predicate: (entry: PendingTurnEntry) => boolean,
): PendingTurnEntry | undefined {
  for (const entry of entries) {
    if (consumed.has(entry.id)) continue;
    if (predicate(entry)) return entry;
  }
  return undefined;
}

// --- steer/drain-method reconciliation (new steering items) -------------

// matchesSteerEntry requires an exact normalized-text match - a classic
// turn/steer placeholder only ever represents the literal text the user
// steered with, never a merge.
function matchesSteerEntry(entry: PendingTurnEntry, itemText: string): boolean {
  return normalizeText(entry.text) === normalizeText(itemText);
}

// computeReconciledIds is the single entry point pendingTurnsStore.ts calls
// on every threads-store change: given the pending entries currently
// tracked for one ref (already scoped to that ref by the caller - this
// function does not filter by `entry.ref` itself), that ref's CURRENT
// ThreadModel, and the set of item ids already seen before this change
// (collectItemIds, called on the PREVIOUS model), returns the ids of every
// entry this change confirms.
//
// Matching order per new "steering" item: an exact-text steer match is
// preferred over a FIFO drain match (a specific match should not be starved
// by an always-eligible drain entry registered earlier) - the legacy
// registry doesn't have to make this call (its two methods are reconciled
// via two independent tryReconcile invocations against the same event, in
// whatever order renderer.js happens to call them), so this ordering is
// this port's own considered, documented choice, not a verbatim rule.
export function computeReconciledIds(
  entries: PendingTurnEntry[],
  model: ThreadModel,
  priorItemIds: Set<string>,
): string[] {
  if (entries.length === 0) return [];
  const resolved: string[] = [];
  const consumed = new Set<string>();

  const queueEntries = entries.filter((e) => e.method === "queue");
  for (const id of reconcileQueueEntries(queueEntries, model.queue)) {
    resolved.push(id);
    consumed.add(id);
  }

  const newItems = collectNewItems(model, priorItemIds);
  for (const item of newItems) {
    if (item.type === "userMessage") {
      const match = findAndConsume(
        entries.filter((e) => e.method === "send"),
        consumed,
        (entry) => matchesSendEntry(entry, item.text, item.images?.length ?? 0),
      );
      if (match) {
        resolved.push(match.id);
        consumed.add(match.id);
      }
    } else if (item.type === "steering") {
      const steerMatch = findAndConsume(
        entries.filter((e) => e.method === "steer"),
        consumed,
        (entry) => matchesSteerEntry(entry, item.text),
      );
      if (steerMatch) {
        resolved.push(steerMatch.id);
        consumed.add(steerMatch.id);
        continue;
      }
      // Drain matches first-come-first-served, no text comparison - the
      // daemon collapses the whole queue into one joined steering entry
      // whose exact text a placeholder can't have predicted.
      const drainMatch = findAndConsume(
        entries.filter((e) => e.method === "drain"),
        consumed,
        () => true,
      );
      if (drainMatch) {
        resolved.push(drainMatch.id);
        consumed.add(drainMatch.id);
      }
    }
  }

  return resolved;
}
