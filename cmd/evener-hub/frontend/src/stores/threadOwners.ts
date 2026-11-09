// Who holds a thread ref open. A ref is owned while a pane (foreground), a
// background lease, a history hold or a mutation pin holds it. The counters,
// generations and the final-release marker live here; threads.ts reads them
// through these accessors and keeps the work each ownership change triggers.
// This module never imports threads.ts.

const foregroundCounts = new Map<string, number>();
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

export function holdForeground(ref: string): void {
  hold(foregroundCounts, ref);
}

/** Drops one foreground hold and returns how many remain. */
export function releaseForeground(ref: string): number {
  return release(foregroundCounts, ref);
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
  backgroundCounts.clear();
  historyHoldCounts.clear();
  mutationPins.clear();
  ownerGenerations.clear();
  finalReleaseOwners.clear();
  backgroundLeaseSequences.clear();
}
