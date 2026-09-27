import type { NavigationSessionSummary } from "@evener/appwire-client";
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

/** Marks a session seen on the hub when its pane opens, and again when the
 * page becomes visible with the pane still open, so its blue dot clears on
 * every device (spec 18, S4). It marks only while the page is visible, so a
 * pane restored in a background tab marks nothing until you look at it. Each
 * open marks the row as the pane first finds it, once: a turn that ends while
 * the pane stays open is not "opened since". A row still loading is marked
 * when it arrives. The hub's mark is idempotent, so a failed one is left for
 * the next open. */
export function useMarkSessionSeenOnOpen(ref: string): void {
  useEffect(() => {
    let awaitingRow = true;
    const markOnce = () => {
      if (!awaitingRow || document.visibilityState !== "visible") return;
      const { client, state } = connectionStore.getState();
      if (state !== "ready" || !client) return;
      const summary = selectSessionSummary(ref, navigationStore.getState());
      if (!summary) return;
      awaitingRow = false;
      const seenThrough = seenThroughToMark(summary);
      if (seenThrough === undefined) return;
      client.request("evener/session/seen/set", { sessions: [{ ref, seenThrough }] }).catch(() => {});
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
      unsubscribeConnection();
      unsubscribeNavigation();
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [ref]);
}
