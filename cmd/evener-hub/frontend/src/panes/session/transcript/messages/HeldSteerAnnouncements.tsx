// HeldSteerAnnouncements is the held-steer surface's ONE aria-live region,
// mounted OUTSIDE the virtual list (Session.tsx, beside
// AskDockAnnouncements - that component's pattern; NOT extracted, because
// the two key on different transitions: ask counts an answered/total string
// keyed on an epoch, this keys on id-set appearance/disappearance). The
// ghost rows are virtualized, so an in-row region would re-announce on
// every scroll-away/scroll-back remount. Announces exactly once each:
// appearance, delivery (the delivered item replaces the ghost in place -
// the announcement is the only audible trace of the swap), and each
// non-delivery departure (spec §5). Never announces on the held-timer's
// cadence: this component does not read the clock at all.
import { reflectedMutationIds } from "@evener/appwire-client/state/mutation";
import type { JSX } from "react";
import { useEffect, useRef, useState } from "react";
import { useThreadsStore } from "../../../../stores/threads";
import { VisuallyHidden } from "../../../../widgets/internal/VisuallyHidden";
import type { PendingTurnEntry } from "../../composer/queue/pendingReconcile";
import {
  useBlockedMutationEntries,
  useCanceledMutationEntries,
  usePendingTurnEntries,
  useRecoveryEntries,
} from "../../composer/queue/pendingTurnsStore";
import { heldSteerEntries } from "./HeldSteerStack";

export function HeldSteerAnnouncements({ ref: sessionRef }: { ref: string }): JSX.Element {
  const held = heldSteerEntries(usePendingTurnEntries(sessionRef));
  const recovery = useRecoveryEntries(sessionRef);
  const blocked = useBlockedMutationEntries(sessionRef);
  const canceled = useCanceledMutationEntries(sessionRef);
  const model = useThreadsStore((s) => s.threads.get(sessionRef));
  const [announcement, setAnnouncement] = useState({ text: "", key: 0 });
  const prevRef = useRef<{ ref: string; ids: ReadonlySet<string> } | null>(null);

  useEffect(() => {
    const prev = prevRef.current;
    const ids = new Set(held.map((entry: PendingTurnEntry) => entry.id));
    prevRef.current = { ref: sessionRef, ids };
    // A pane reused across refs baselines silently, and the first
    // observation never announces (the reader who just opened the pane
    // scrolled to the bottom and sees the ghost).
    if (prev?.ref !== sessionRef) return;
    const announce = (text: string) => setAnnouncement((a) => ({ text, key: a.key + 1 }));
    const appeared = held.some((entry: PendingTurnEntry) => !prev.ids.has(entry.id));
    const disappeared = [...prev.ids].filter((id) => !ids.has(id));
    // Departure priority in a same-batch collision (review ruling): a
    // departure is the outcome of a message the reader was already told
    // about and is the event's only audible trace - the departed id never
    // comes back, so an appeared-first short-circuit lost the departure
    // announcement permanently - while a same-batch arrival still reaches
    // the reader two other ways: the visible ghost in place and the pill's
    // heldEpoch edge.
    if (disappeared.length > 0) {
      const reflected = reflectedMutationIds(model);
      const why = (id: string): string => {
        if (reflected.has(id)) return "Steering message delivered.";
        if (recovery.some((record) => record.clientMutationId === id))
          return "Steering message was rejected. It's kept with the queue.";
        if (canceled.some((record) => record.clientMutationId === id))
          return "Steering message was canceled by Stop. It's kept with the queue.";
        if (blocked.some((record) => record.clientMutationId === id))
          return "Steering message delivery is uncertain. It's kept with the queue.";
        // Post-settle failed delivery: nothing holds the id anywhere. No
        // QueueStrip row exists for this departure (spec §5) - the
        // announcement is the message's only trace; the failed turn's
        // error surface is the explanation.
        return "Steering message failed to deliver.";
      };
      const outcomes = [...new Set(disappeared.map(why))];
      announce(outcomes.join(" "));
    } else if (appeared) {
      announce("Steering message held.");
    }
  }, [sessionRef, held, recovery, blocked, canceled, model]);

  return (
    <div role="status" aria-live="polite" data-testid="held-steer-announcements">
      <VisuallyHidden key={announcement.key}>{announcement.text}</VisuallyHidden>
    </div>
  );
}
