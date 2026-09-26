// Optimistic send chips beside the composer. Reads the shared
// pendingTurnsStore (usePendingTurnEntries) and renders one compact chip per
// pending entry whose method is send - the strip keeps send only, because
// steer/drain/promote render as ghosts in the transcript's trailing row per
// the steering-ghost spec (docs/web-ui/specs/2026-09-20-steering-ghost-live-edge.md).
// Reconciliation is owned entirely by pendingTurnsStore's durable outbox and
// authoritative pending-mutation projection; this component adds no store
// state and imports the hook read-only.
//
// Deliberately rendered here beside the composer, NOT injected into the
// virtualized transcript: an optimistic item in the virtual list is beyond the
// parity bar, and the legacy chip was itself a lightweight out-of-transcript
// indicator (recorded as a conscious presentation choice in the wave close
// sweep). Chips are dimmed, never colored - "in flight" is not an
// attention-family state (color-is-attention).
import type { JSX } from "react";
import { useMemo } from "react";
import { requireClass } from "../../../widgets/internal/requireClass";
import type { PendingTurnEntry } from "../composer/queue/pendingReconcile";
import { usePendingTurnEntries } from "../composer/queue/pendingTurnsStore";
import { pendingEntryPreview } from "../composer/queue/queueDisplay";
import styles from "./pendingchips.module.css";

function isOptimistic(entry: PendingTurnEntry): boolean {
  // blockedUnknown and canceled rows are QueueStrip's durable rows, never
  // in-flight chips: a canceled row would otherwise read as still Sending
  // here while QueueStrip simultaneously reports it as canceled.
  return entry.method === "send" && entry.state !== "blockedUnknown" && entry.state !== "canceled";
}

const CLASS = {
  chips: requireClass(styles.chips, "pendingchips.module.css", "chips"),
  chip: requireClass(styles.chip, "pendingchips.module.css", "chip"),
  method: requireClass(styles.method, "pendingchips.module.css", "method"),
  text: requireClass(styles.text, "pendingchips.module.css", "text"),
};

export function PendingChips({ sessionRef }: { sessionRef: string }): JSX.Element | null {
  const entries = usePendingTurnEntries(sessionRef);
  // Filter to the sends this strip owns - steer/drain/promote ghosts render
  // in the transcript's trailing row (steering-ghost spec) and QueueStrip
  // owns "queue". The one label this strip renders is present-tense: it
  // conveys the still-in-flight state the dimmed chip already hints at.
  // Memoized against the store-stable entries array so an unrelated re-render
  // does not rebuild the list.
  const optimistic = useMemo(() => entries.filter(isOptimistic), [entries]);

  if (optimistic.length === 0) return null;

  return (
    <ul className={CLASS.chips} data-testid="pending-chips">
      {optimistic.map((entry) => (
        <li key={entry.id} className={CLASS.chip}>
          <span className={CLASS.method}>Sending</span>
          <span className={CLASS.text}>{pendingEntryPreview(entry)}</span>
        </li>
      ))}
    </ul>
  );
}
