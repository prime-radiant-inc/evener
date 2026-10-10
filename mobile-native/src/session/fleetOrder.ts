// Moving between sessions from inside one (spec 6, 8.1, 8.3, 13.2): who else
// needs you (the Back count and Next's destination), and the Live order the
// title bar's swipes walk. Both come from the Board's own bands
// (src/board/attention.ts), so the Session and the Board never disagree about
// who needs you.
import type { NavigationSessionSummary } from "@evener/appwire-client";
import { type LiveBands, liveRows } from "../board/attention";

export function othersNeedingYou(bands: LiveBands, currentRef: string): NavigationSessionSummary[] {
	return bands.needsYou.map((item) => item.row).filter((row) => row.ref !== currentRef);
}

/** The order Next serves (spec 8.3): whichever session alerted you most
 * recently, shown or held, then Needs you order. The prototype's nextQueue.
 * `recent` is the alert center's, most recent first. */
export function servedFirst<T extends { ref: string }>(needsYou: readonly T[], recent: readonly string[]): T[] {
	const rank = (row: T) => {
		const index = recent.indexOf(row.ref);
		return index === -1 ? recent.length : index;
	};
	return needsYou
		.map((row, order) => ({ row, order }))
		.sort((a, b) => rank(a.row) - rank(b.row) || a.order - b.order)
		.map(({ row }) => row);
}

/** The order Next serves, its destination first, which its touch-and-hold
 * list shows too: what alerted you most recently, then Needs you order
 * (ruling 11). From a session in Needs you, that order starts at the one
 * after it and wraps around, so Next walks through everyone who needs you
 * instead of bouncing between two. From any other session it starts at the
 * head. */
export function nextQueue(bands: LiveBands, currentRef: string, recent: readonly string[]): NavigationSessionSummary[] {
	const order = bands.needsYou.map((item) => item.row);
	const index = order.findIndex((row) => row.ref === currentRef);
	const after = index === -1 ? order : [...order.slice(index + 1), ...order.slice(0, index)];
	return servedFirst(after, recent);
}

export function liveOrder(bands: LiveBands): NavigationSessionSummary[] {
	return liveRows(bands).map((item) => item.row);
}

/** The session before or after this one in Live order; null at either end, or
 * when this one isn't in Live. */
export function neighbor(
	order: readonly NavigationSessionSummary[],
	currentRef: string,
	direction: 1 | -1,
): NavigationSessionSummary | null {
	const index = order.findIndex((row) => row.ref === currentRef);
	return index === -1 ? null : (order[index + direction] ?? null);
}

/** Next pushes, so Back returns to where you were. From a session Next itself
 * opened, it replaces, so working through the queue stays one step deep and
 * Back still lands where you started (spec 8.3, ruling 2). */
export function nextNavigation(openedBy: "next" | undefined): "push" | "replace" {
	return openedBy === "next" ? "replace" : "push";
}
