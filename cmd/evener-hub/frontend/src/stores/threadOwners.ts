// Who holds a thread ref open. A ref is owned while a pane (foreground), a
// background lease, a history hold or a mutation pin holds it. The counters,
// generations and the final-release marker live here; threads.ts reads them
// through these accessors and keeps the work each ownership change triggers.
// This module never imports threads.ts.

const foregroundCounts = new Map<string, number>();
// The same holds split by what the acquisition took. The total answers "is this
// ref kept alive"; it cannot answer "is the hold I am releasing the one I took".
// A rollback undoing its own failed acquisition would otherwise spend whichever
// hold is standing - and once a pane closes during its own read, the only hold
// left can be a resend's routing lease, which that rollback then retires the
// model out from under. A release drops a hold of its own kind, or nothing.
const paneForegroundCounts = new Map<string, number>();
const routingForegroundCounts = new Map<string, number>();
// A foreground holder that also owns the live transcript bodies - a mounted
// pane. Its own count, not foregroundCounts: a routing holder keeps the model
// alive without becoming the reader a later pane finds and defers to, and
// without attaching a repository reader of its own.
const bodyOwnerCounts = new Map<string, number>();
// Background claims keep canonical thread hydration/routing alive without
// borrowing a pane's cache or ensure-generation lifetime.
const backgroundCounts = new Map<string, number>();
const historyHoldCounts = new Map<string, number>();
const mutationPins = new Set<string>();
const ownerGenerations = new Map<string, number>();
// The last background lease owns removal while its bounded final cache drain
// is in flight. Receipt/pin cleanup may still reconcile mutations, but cannot
// remove the model out from under that awaited drain.
const finalReleaseOwners = new Map<string, number>();
const backgroundLeaseSequences = new Map<string, number>();

function release(counts: Map<string, number>, ref: string): number {
  const remaining = (counts.get(ref) ?? 0) - 1;
  if (remaining > 0) {
    counts.set(ref, remaining);
    return remaining;
  }
  counts.delete(ref);
  return Math.max(remaining, 0);
}

function hold(counts: Map<string, number>, ref: string): void {
  counts.set(ref, (counts.get(ref) ?? 0) + 1);
}

export function foregroundHolds(ref: string): number {
  return foregroundCounts.get(ref) ?? 0;
}

export function holdForeground(ref: string, routing = false): void {
  hold(foregroundCounts, ref);
  hold(routing ? routingForegroundCounts : paneForegroundCounts, ref);
}

/** Drops one foreground hold of this kind and returns how many remain. Nothing
 * of this kind left means nothing of it to drop: the standing count is another
 * lifecycle's, and spending it would retire a model out from under its holder. */
export function releaseForeground(ref: string, routing = false): number {
  const counts = routing ? routingForegroundCounts : paneForegroundCounts;
  if ((counts.get(ref) ?? 0) <= 0) return foregroundHolds(ref);
  release(counts, ref);
  return release(foregroundCounts, ref);
}

/** Foreground holders that own the live transcript bodies. */
export function bodyOwners(ref: string): number {
  return bodyOwnerCounts.get(ref) ?? 0;
}

export function holdBodyOwnership(ref: string): void {
  hold(bodyOwnerCounts, ref);
}

/** Drops one body-owning hold and returns how many remain. */
export function releaseBodyOwnership(ref: string): number {
  return release(bodyOwnerCounts, ref);
}

export function backgroundHolds(ref: string): number {
  return backgroundCounts.get(ref) ?? 0;
}

export function holdBackground(ref: string): void {
  hold(backgroundCounts, ref);
}

/** Drops one background hold and returns how many remain. */
export function releaseBackground(ref: string): number {
  return release(backgroundCounts, ref);
}

export function backgroundHeldRefs(): IterableIterator<string> {
  return backgroundCounts.keys();
}

export function historyHolds(ref: string): number {
  return historyHoldCounts.get(ref) ?? 0;
}

export function holdHistory(ref: string): void {
  hold(historyHoldCounts, ref);
}

/** Drops one history hold and returns how many remain. */
export function releaseHistory(ref: string): number {
  return release(historyHoldCounts, ref);
}

export function isMutationPinned(ref: string): boolean {
  return mutationPins.has(ref);
}

export function pinMutation(ref: string): void {
  mutationPins.add(ref);
}

export function unpinMutation(ref: string): void {
  mutationPins.delete(ref);
}

export function mutationPinnedRefs(): IterableIterator<string> {
  return mutationPins.values();
}

export function canonicalThreadOwnerActive(ref: string): boolean {
  return foregroundHolds(ref) > 0 || backgroundHolds(ref) > 0 || historyHolds(ref) > 0 || isMutationPinned(ref);
}

/** True when only a mutation pin keeps the ref owned. */
export function ownedOnlyByMutationPin(ref: string): boolean {
  return foregroundHolds(ref) === 0 && backgroundHolds(ref) === 0 && isMutationPinned(ref);
}

// A live foreground/history/pin holder. Background membership is not one: it
// must never be what lifts a clear's suppression.
export function heldCountsAtStore(ref: string): boolean {
  return foregroundHolds(ref) > 0 || historyHolds(ref) > 0 || isMutationPinned(ref);
}

// The one answer to "is this thread body-owned": a mounted pane, a history
// hold or a mutation pin owns the live transcript bodies. A routing hold is
// excluded by construction, not by each caller remembering to exclude it - it
// takes foregroundCounts alone and never holdBodyOwnership. Every site that
// asks whether someone is already showing what a body read would restore calls
// this.
export function threadBodyOwned(ref: string): boolean {
  return bodyOwners(ref) > 0 || historyHolds(ref) > 0 || isMutationPinned(ref);
}

export function ownerGeneration(ref: string): number {
  return ownerGenerations.get(ref) ?? 0;
}

export function bumpOwnerGeneration(ref: string): void {
  ownerGenerations.set(ref, ownerGeneration(ref) + 1);
}

export function hasFinalReleaseOwner(ref: string): boolean {
  return finalReleaseOwners.has(ref);
}

export function isFinalReleaseOwner(ref: string, sequence: number): boolean {
  return finalReleaseOwners.get(ref) === sequence;
}

export function setFinalReleaseOwner(ref: string, sequence: number): void {
  finalReleaseOwners.set(ref, sequence);
}

export function clearFinalReleaseOwner(ref: string): void {
  finalReleaseOwners.delete(ref);
}

export function backgroundLeaseSequence(ref: string): number {
  return backgroundLeaseSequences.get(ref) ?? 0;
}

/** Advances and returns the ref's background lease sequence. */
export function nextBackgroundLeaseSequence(ref: string): number {
  const next = backgroundLeaseSequence(ref) + 1;
  backgroundLeaseSequences.set(ref, next);
  return next;
}

export function resetThreadOwnersForTests(): void {
  foregroundCounts.clear();
  paneForegroundCounts.clear();
  routingForegroundCounts.clear();
  bodyOwnerCounts.clear();
  backgroundCounts.clear();
  historyHoldCounts.clear();
  mutationPins.clear();
  ownerGenerations.clear();
  finalReleaseOwners.clear();
  backgroundLeaseSequences.clear();
}
