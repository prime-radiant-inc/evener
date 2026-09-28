// Moving between sessions from inside one (spec 6, 8.1, 8.3, 13.2): who else
// needs you (the Back count and Next's destination), and the Live order the
// title bar's swipes walk. Both come from the Board's own bands
// (src/board/attention.ts), so the Session and the Board never disagree about
// who needs you.
import type { NavigationSessionSummary } from "@evener/appwire-client";
import type { LiveBands } from "../board/attention";

export function othersNeedingYou(bands: LiveBands, currentRef: string): NavigationSessionSummary[] {
	return bands.needsYou.map((item) => item.row).filter((row) => row.ref !== currentRef);
}

/** Next's destination, in Needs you order (ruling 11). From a session in
 * Needs you it is the one after it, wrapping around at the end, so Next
 * walks through everyone who needs you instead of bouncing between two.
 * From any other session it is the head of that order. Phase 6's alerts put
 * "whichever session alerted you most recently" at the head. */
export function nextSession(bands: LiveBands, currentRef: string): NavigationSessionSummary | null {
	const order = bands.needsYou.map((item) => item.row);
	const index = order.findIndex((row) => row.ref === currentRef);
	if (index === -1) return order[0] ?? null;
	return order.length > 1 ? order[(index + 1) % order.length] : null;
}

export function liveOrder(bands: LiveBands): NavigationSessionSummary[] {
	return [...bands.needsYou, ...bands.finished, ...bands.working, ...bands.idle].map((item) => item.row);
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
