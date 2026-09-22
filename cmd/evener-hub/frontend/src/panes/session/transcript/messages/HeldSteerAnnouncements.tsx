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
import type { JSX } from "react";
import { useEffect, useRef, useState } from "react";
import { useThreadsStore } from "../../../../stores/threads";
import type { PendingTurnEntry } from "../../composer/queue/pendingReconcile";
import {
  useBlockedMutationEntries,
  useCanceledMutationEntries,
  usePendingTurnEntries,
  useRecoveryEntries,
} from "../../composer/queue/pendingTurnsStore";
import { heldSteerEntries } from "./HeldSteerStack";

// Mirrors pendingEntries.ts's reflectedMutationIds, which is not exported:
// the transcript's own record of which client mutation ids landed. The
// queue arm is widened with null because the live model's queue is
// QueueState | null (model.ts), not optional-only.
function reflectedIds(
  model:
    | {
        queue?: { clientMutationIds?: readonly string[] } | null;
        turns?: ReadonlyArray<{ items: ReadonlyArray<{ clientMutationId?: string }> }>;
      }
    | undefined,
): Set<string> {
  const ids = new Set<string>(model?.queue?.clientMutationIds ?? []);
  for (const turn of model?.turns ?? []) {
    for (const item of turn.items) {
      if (item.clientMutationId) ids.add(item.clientMutationId);
    }
  }
  return ids;
}

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
    if (appeared) {
      announce("Steering message held.");
      return;
    }
    const disappeared = [...prev.ids].filter((id) => !ids.has(id));
    if (disappeared.length === 0) return;
    const reflected = reflectedIds(model);
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
      // announcement is the message's only trace; the failed turn's error
      // surface is the explanation.
      return "Steering message failed to deliver.";
    };
    // One announcement per transition batch, classified by the first
    // departed id - a batch departure is one audible event, not a burst.
    announce(why(disappeared[0] ?? ""));
  }, [sessionRef, held, recovery, blocked, canceled, model]);

  return (
    <div role="status" aria-live="polite" data-testid="held-steer-announcements">
      <span key={announcement.key}>{announcement.text}</span>
    </div>
  );
}
