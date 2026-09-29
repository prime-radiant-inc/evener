// Following the transcript's live end (spec 8.2). While following, new rows
// scroll into view as they land. A drag stops it; ending a drag, or its
// momentum, at the end starts it again, so a row that lands mid-drag or
// mid-coast never pulls the list from under the finger. Away from the end,
// the rows that were there when you left are what "↓ N new" counts against.
import { useCallback, useRef, useState } from "react";

/** How close to the end still counts as at the end, in points. */
export const AT_END_PT = 48;

export interface ScrollGeometry {
	contentOffset: { y: number };
	contentSize: { height: number };
	layoutMeasurement: { height: number };
}

/** Whether a scroll position is at the transcript's end: within AT_END_PT of
 * it, or everything fits in the viewport. */
export function atEnd({ contentOffset, contentSize, layoutMeasurement }: ScrollGeometry): boolean {
	return contentOffset.y + layoutMeasurement.height >= contentSize.height - AT_END_PT;
}

export interface LiveEndFollow {
	/** New rows scroll into view as they land. */
	following: boolean;
	/** What is moving the list: a finger, its momentum, or neither (the app). */
	touch: "none" | "dragging" | "momentum";
	/** The rows there when you left the end, or null at the end. */
	away: ReadonlySet<string> | null;
}

export type FollowEvent =
	| { type: "dragBegin" }
	| { type: "dragEnd"; atEnd: boolean }
	| { type: "momentumBegin" }
	| { type: "momentumEnd"; atEnd: boolean }
	/** Any scroll, yours or the app's; `keys` names the rows on screen now. */
	| { type: "scroll"; atEnd: boolean; keys: () => ReadonlySet<string> }
	/** Jump to live, or opening at the live end. */
	| { type: "follow" }
	/** A restore to a reading position, or a jump to a find match. */
	| { type: "unfollow" }
	/** A new session on the screen. */
	| { type: "reset"; following: boolean };

export function nextFollow(state: LiveEndFollow, event: FollowEvent): LiveEndFollow {
	switch (event.type) {
		case "dragBegin":
			return { ...state, following: false, touch: "dragging" };
		case "momentumBegin":
			// A release inside the end band may still coast away from it: follow
			// only once the momentum settles there.
			return { ...state, following: false, touch: "momentum" };
		case "dragEnd":
		case "momentumEnd":
			return event.atEnd
				? { following: true, touch: "none", away: null }
				: { ...state, following: false, touch: "none" };
		case "scroll":
			if (event.atEnd) return state.away === null ? state : { ...state, away: null };
			// While following, only the app scrolls the list, toward the end:
			// that is no leaving it.
			if (state.away !== null || (state.following && state.touch === "none")) return state;
			return { ...state, away: event.keys() };
		case "follow":
			return { following: true, touch: "none", away: null };
		case "unfollow":
			return { ...state, following: false, away: null };
		case "reset":
			return { following: event.following, touch: "none", away: null };
	}
}

/** The follow state for a screen: `state.current` for handlers to read at
 * once, and `away` as React state, since the "↓ new" pill renders from it. */
export function useLiveEndFollow() {
	const state = useRef<LiveEndFollow>({ following: false, touch: "none", away: null });
	const [away, setAway] = useState<ReadonlySet<string> | null>(null);
	const dispatch = useCallback((event: FollowEvent) => {
		state.current = nextFollow(state.current, event);
		setAway(state.current.away);
	}, []);
	return { state, away, dispatch };
}
