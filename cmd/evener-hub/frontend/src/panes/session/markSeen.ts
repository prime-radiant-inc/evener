import { decodeActivityRead, type NavigationSessionSummary } from "@evener/appwire-client";
import { hubTime } from "@evener/appwire-client/state/navigation";
import { useEffect } from "react";
import { connectionStore } from "../../stores/connection";
import { selectSessionSummary } from "../../stores/navigation/selectors";
import { navigationStore } from "../../stores/navigation/store";
import { sessionReadProgress } from "../../stores/readProgress";

/** The seenThrough a pane opening on this row sends: the row's own
 * turn_ended_at in milliseconds, when the hub reports that turn unseen. A seen
 * row, and a row with no turn end (an older hub, or a session that has not
 * ended a turn), has nothing to mark. */
export function seenThroughToMark(
  summary: Pick<NavigationSessionSummary, "unseen" | "turn_ended_at"> | undefined,
): number | undefined {
  if (summary?.unseen !== true || summary.turn_ended_at === undefined) return undefined;
  const seenThrough = Date.parse(summary.turn_ended_at);
  return Number.isFinite(seenThrough) ? seenThrough : undefined;
}

/** The mark an opening pane sends: through the later of an unseen turn end
 * and the session's last motion (the activity read's lastMovedAt) when that
 * motion came after the hub's seen_through, so output that streamed after the
 * turn ended is seen too. Undefined when neither has anything new. */
export function seenThroughWithMotion(
  summary: Pick<NavigationSessionSummary, "unseen" | "turn_ended_at" | "seen_through"> | undefined,
  lastMovedAt: number | undefined,
): number | undefined {
  const turn = seenThroughToMark(summary);
  const mark = Date.parse(summary?.seen_through ?? "");
  const motion = lastMovedAt !== undefined && Number.isFinite(mark) && lastMovedAt > mark ? lastMovedAt : undefined;
  if (turn === undefined) return motion;
  return motion === undefined ? turn : Math.max(turn, motion);
}

/** The row state a stay in front has marked: the hub's turn end when it has a
 * readable one, else the update the browser's own mark runs through. As on the
 * phone, one turn end is marked once and a newer one is marked again. */
function markedRowKey(summary: Pick<NavigationSessionSummary, "turn_ended_at" | "updated_at">): string | undefined {
  const turn = hubTime(summary.turn_ended_at);
  if (turn !== null) return `turn ${turn}`;
  const updated = hubTime(summary.updated_at);
  return updated === null ? undefined : `update ${updated}`;
}

/** Marks a session read while its pane is in front: when the pane opens, when
 * the page becomes visible with the pane still open, and when a turn ends
 * while you watch, so its unread marker clears here and on every device (spec
 * 18, S4; the phone's useMarkSeenInFront). The browser's own read progress
 * records the read too, so a row the hub cannot decide (an older hub, or a
 * session that has not ended a turn) clears at once; the hub mark follows. It
 * marks only while the page is visible, so a pane restored in a background tab
 * marks nothing until you look at it. A row still loading is marked when it
 * arrives, and each turn end is marked once, however often the row is rebuilt
 * while the pane shows it. On a hub that tracks seen-through marks the mark
 * also covers the session's last motion, read once per opening; a read that
 * fails or can't be decoded falls back to the turn alone. The hub's mark is
 * idempotent (and also clears a "Mark as unread"), so each opening and each
 * new turn end sends its mark, and a failed one is left for the next. */
export function useMarkSessionSeenOnOpen(ref: string): void {
  useEffect(() => {
    let disposed = false;
    // The row state this stay in front has marked. Hiding the page clears it,
    // so showing it again marks the row afresh.
    let marked: string | undefined;
    const markOnce = () => {
      if (document.visibilityState !== "visible") return;
      const { client, state } = connectionStore.getState();
      if (state !== "ready" || !client) return;
      const summary = selectSessionSummary(ref, navigationStore.getState());
      if (!summary) return;
      const key = markedRowKey(summary);
      if (key === undefined || key === marked) return;
      marked = key;
      // The read can outlast the moment it was taken for: a page hidden
      // meanwhile marks nothing (showing it again marks afresh), and the mark
      // goes through the connection that is ready now, or waits for one.
      const send = (seenThrough: number | undefined) => {
        if (disposed || document.visibilityState !== "visible") {
          marked = undefined;
          return;
        }
        // The read is this browser's now; the hub's row catches up after.
        sessionReadProgress.markRead(summary, seenThrough);
        if (seenThrough === undefined) return;
        const current = connectionStore.getState();
        if (current.state !== "ready" || !current.client) {
          marked = undefined;
          return;
        }
        current.client
          .request("evener/session/seen/set", { sessions: [{ ref, seenThrough }] })
          .then(() => sessionReadProgress.acknowledge(ref, seenThrough))
          .catch(() => {});
      };
      // An older hub sends no seen_through, so its motion can't be compared.
      if (summary.seen_through === undefined) {
        send(seenThroughToMark(summary));
        return;
      }
      client
        .request("evener/activity/read", { refs: [ref] })
        .then((read) =>
          seenThroughWithMotion(
            summary,
            decodeActivityRead(read).find((activity) => activity.ref === ref)?.lastMovedAt,
          ),
        )
        .catch(() => {
          // A read that failed because its connection was replaced says
          // nothing about the session: read again through the new one rather
          // than fall back to the turn alone.
          if (!disposed && connectionStore.getState().client !== client) {
            marked = undefined;
            markOnce();
            return undefined;
          }
          return seenThroughToMark(summary);
        })
        .then(send);
    };
    const onVisibilityChange = () => {
      // Leaving the page unmarks: a mark taken for a hidden page marks
      // nothing, and showing it again marks the row afresh.
      marked = undefined;
      markOnce();
    };
    markOnce();
    const unsubscribeConnection = connectionStore.subscribe(markOnce);
    const unsubscribeNavigation = navigationStore.subscribe(markOnce);
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      disposed = true;
      unsubscribeConnection();
      unsubscribeNavigation();
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [ref]);
}
