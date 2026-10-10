import { decodeActivityRead, type NavigationSessionSummary } from "@evener/appwire-client";
import { useEffect } from "react";
import { connectionStore } from "../../stores/connection";
import { selectSessionSummary } from "../../stores/navigation/selectors";
import { navigationStore } from "../../stores/navigation/store";

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

/** Marks a session seen on the hub when its pane opens, and again when the
 * page becomes visible with the pane still open, so its blue dot clears on
 * every device (spec 18, S4). It marks only while the page is visible, so a
 * pane restored in a background tab marks nothing until you look at it. Each
 * open marks the row as the pane first finds it, once: a turn that ends while
 * the pane stays open is not "opened since". A row still loading is marked
 * when it arrives. On a hub that tracks seen-through marks the mark also
 * covers the session's last motion, read once per open; a read that fails or
 * can't be decoded falls back to the turn alone. The hub's mark is idempotent
 * (and also clears a "Mark as unread"), so each open sends its mark, and a
 * failed one is left for the next open. */
export function useMarkSessionSeenOnOpen(ref: string): void {
  useEffect(() => {
    let awaitingRow = true;
    let disposed = false;
    const markOnce = () => {
      if (!awaitingRow || document.visibilityState !== "visible") return;
      const { client, state } = connectionStore.getState();
      if (state !== "ready" || !client) return;
      const summary = selectSessionSummary(ref, navigationStore.getState());
      if (!summary) return;
      awaitingRow = false;
      // The read can outlast the moment it was taken for: a page hidden
      // meanwhile marks nothing (showing it again marks afresh), and the mark
      // goes through the connection that is ready now, or waits for one.
      const send = (seenThrough: number | undefined) => {
        if (disposed || seenThrough === undefined) return;
        if (document.visibilityState !== "visible") return;
        const current = connectionStore.getState();
        if (current.state !== "ready" || !current.client) {
          awaitingRow = true;
          return;
        }
        current.client.request("evener/session/seen/set", { sessions: [{ ref, seenThrough }] }).catch(() => {});
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
        .catch(() => seenThroughToMark(summary))
        .then(send);
    };
    const onVisibilityChange = () => {
      if (document.visibilityState !== "visible") return;
      awaitingRow = true;
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
