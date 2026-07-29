// Optimistic send/steer/drain chips beside the composer. Reads the shared
// pendingTurnsStore (usePendingTurnEntries) and renders one compact chip per
// pending entry whose method is send, steer, or drain - the "queue" method is
// already chipped by QueueStrip, so it is filtered out here. Reconciliation
// is owned entirely by pendingTurnsStore's durable outbox and authoritative
// pending-mutation projection; this component adds no store state and imports
// the hook read-only.
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
import type { PendingMethod, PendingTurnEntry } from "../composer/queue/pendingReconcile";
import { usePendingTurnEntries } from "../composer/queue/pendingTurnsStore";
import { queueEntryPreviewText } from "../composer/queue/queueDisplay";
import styles from "./pendingchips.module.css";

type OptimisticMethod = Exclude<PendingMethod, "queue">;
type OptimisticEntry = PendingTurnEntry & { method: OptimisticMethod };

function isOptimistic(entry: PendingTurnEntry): entry is OptimisticEntry {
  return entry.method !== "queue";
}

const CLASS = {
  chips: requireClass(styles.chips, "pendingchips.module.css", "chips"),
  chip: requireClass(styles.chip, "pendingchips.module.css", "chip"),
  method: requireClass(styles.method, "pendingchips.module.css", "method"),
  text: requireClass(styles.text, "pendingchips.module.css", "text"),
};

// The three optimistic-submission methods this strip owns. "queue" is
// excluded - QueueStrip renders those. Present-tense labels convey the
// still-in-flight state a dimmed chip already hints at.
const METHOD_LABEL: Record<OptimisticMethod, string> = {
  send: "Sending",
  steer: "Steering",
  drain: "Draining",
};

export function PendingChips({ sessionRef }: { sessionRef: string }): JSX.Element | null {
  const entries = usePendingTurnEntries(sessionRef);
  // Filter to the three composer-submission methods (QueueStrip owns "queue").
  // Memoized against the store-stable entries array so an unrelated re-render
  // does not rebuild the list.
  const optimistic = useMemo(() => entries.filter(isOptimistic), [entries]);

  if (optimistic.length === 0) return null;

  return (
    <ul className={CLASS.chips} data-testid="pending-chips">
      {optimistic.map((entry) => (
        <li key={entry.id} className={CLASS.chip}>
          <span className={CLASS.method}>{METHOD_LABEL[entry.method]}</span>
          <span className={CLASS.text}>{queueEntryPreviewText(entry.text, entry.imageCount)}</span>
        </li>
      ))}
    </ul>
  );
}
