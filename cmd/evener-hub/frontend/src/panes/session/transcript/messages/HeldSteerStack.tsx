// HeldSteerStack: every held steering message rendered as the provisional
// user message it will become (steering-ghost spec §2). Reads the shared
// pendingTurnsStore (usePendingTurnEntries) and keeps ONLY the steer
// family - steer/drain/promote - except the terminal states QueueStrip
// owns rows for (blockedUnknown, canceled - the same filter the chips used
// to apply). Scope: every client's held steering renders, not only this
// client's own. Order arrives from reconcilePendingEntries' shared sort
// (§4) - this component never re-sorts.

import { formatElapsed, isTurnActive } from "@evener/appwire-client";
import type { JSX } from "react";
import { useContext, useEffect, useRef, useState } from "react";
import { useThreadsStore } from "../../../../stores/threads";
import type { PendingTurnEntry } from "../../composer/queue/pendingReconcile";
import { usePendingTurnEntries } from "../../composer/queue/pendingTurnsStore";
import { pendingEntryPreview } from "../../composer/queue/queueDisplay";
import { SessionNowContext } from "../../liveness";
import styles from "./heldsteerstack.module.css";
import { UserMessageView } from "./UserMessageItem";

// The one steer-family filter every consumer shares (Session's trailing-row
// predicate, the ghost stack, the announcements region).
export function heldSteerEntries(entries: readonly PendingTurnEntry[]): PendingTurnEntry[] {
  return entries.filter(
    (entry) =>
      (entry.method === "steer" || entry.method === "drain" || entry.method === "promote") &&
      entry.state !== "blockedUnknown" &&
      entry.state !== "canceled",
  );
}

// The caption state table (spec §4). Both arms read the thread STATUS TYPE
// (isTurnActive), never model.activeTurnId - the projector closes one turn
// row before opening the next, so the id goes false between inline turns
// while the run continues (#1330). The count is elapsed time from createdAt
// through the package's formatElapsed, and is omitted entirely when
// createdAt is unknown - never NaN, never a false 0s.
export function heldCaption(entry: PendingTurnEntry, turnActive: boolean, now: number): string {
  let label = "Delivers with the next turn";
  if (turnActive) {
    // An accepted steer waits out the running step; a submitting one
    // joins the turn already in flight.
    label = entry.state === "accepted" ? "Delivers when this step finishes" : "Joining this turn";
  }
  if (entry.createdAt === undefined) return label;
  const elapsed = formatElapsed(now - entry.createdAt);
  return entry.state === "accepted" ? `${label} · held ${elapsed}` : `${label} · ${elapsed}`;
}

// The held set's arrival counter, mirroring askDockStore's activationEpoch
// semantics: bumps when an id joins the current held set, never on removal
// (a departure is the announcements region's job, not new content). A pane
// reused across refs baselines the fresh ref's already-held entries without
// a bump - the reader opens scrolled to the bottom, so nothing is unseen.
// The comparison is against the PREVIOUS observation, not a lifetime set: a
// retried blocked/canceled mutation re-enters holding the id it already
// had, and its reappeared ghost is new content again for a scrolled-away
// reader - a lifetime set would suppress that bump forever.
// `visible` carries Session's §1 live gate: a suppressed surface counts no
// arrivals (a pill would point at ghosts that are not mounted), and a gate
// CLEARING counts the newly visible set as arrivals - the ghosts appearing
// IS new content for a scrolled-away reader.
export function useHeldSteerEpoch(ref: string, entries: readonly PendingTurnEntry[], visible: boolean): number {
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

export function HeldSteerStack({ ref: sessionRef }: { ref: string }): JSX.Element | null {
  const allEntries = usePendingTurnEntries(sessionRef);
  const entries = heldSteerEntries(allEntries);
  const statusType = useThreadsStore((s) => s.threads.get(sessionRef)?.status.type);
  // The caption reads the same clock the liveness line does: Session's
  // 3s SessionNowContext tick, so no per-second machinery re-renders the
  // virtualized row and the caption is never itself a ticking live region.
  const now = useContext(SessionNowContext);
  if (entries.length === 0) return null;
  const turnActive = isTurnActive(statusType ?? "");
  return (
    <ul className={styles.stack} data-testid="held-steer-stack">
      {entries.map((entry) => (
        <li key={entry.id}>
          <UserMessageView
            item={{
              id: entry.id,
              turnId: "",
              type: "userMessage",
              text: pendingEntryPreview(entry) || "[queued messages]",
            }}
            opensExchange={false}
            provisional={heldCaption(entry, turnActive, now)}
          />
        </li>
      ))}
    </ul>
  );
}
