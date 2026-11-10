import type { PendingTurnEntry } from "@evener/appwire-client/state/mutation";
import { useEffect, useRef, useState } from "react";

// The steer-family membership the announcements region maintains. The visible
// pending stack itself now covers every method; this predicate stays only for
// the departure announcements that predate the one-stack merge.
export function heldSteerEntries(
  entries: readonly PendingTurnEntry[],
  editableIds?: ReadonlySet<string>,
): PendingTurnEntry[] {
  return entries.filter(
    (entry) =>
      (entry.method === "steer" || entry.method === "drain" || entry.method === "promote") &&
      !editableIds?.has(entry.id) &&
      entry.state !== "blockedUnknown" &&
      entry.state !== "canceled",
  );
}

// The composer owns the recovery row it has activated. One predicate for the
// mount gate and the stack's own list, so the arrival epoch and the rendered
// rows cannot disagree.
export function withoutActiveRecovery<T extends { id: string }>(
  rows: readonly T[],
  activeRecoveryId: string | undefined,
): readonly T[] {
  return activeRecoveryId === undefined ? rows : rows.filter((row) => row.id !== activeRecoveryId);
}

// The live-edge arrival counter: bumps when a new pending identity joins the
// visible set, never on removal. A pane reused across refs baselines the fresh
// ref's already-present entries without a bump.
export function usePendingArrivalEpoch(ref: string, entries: readonly { id: string }[], visible: boolean): number {
  const [epoch, setEpoch] = useState(0);
  const prevRef = useRef<{ ref: string; ids: ReadonlySet<string> } | null>(null);
  useEffect(() => {
    const prev = prevRef.current;
    const ids = new Set(visible ? entries.map((entry) => entry.id) : []);
    prevRef.current = { ref, ids };
    if (prev?.ref !== ref) return;
    const added = visible && entries.some((entry) => !prev.ids.has(entry.id));
    if (!added) return;
    setEpoch((count) => count + 1);
  }, [ref, entries, visible]);
  return epoch;
}
