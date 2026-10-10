// Following the transcript's live end (spec 8.2). While following, new rows
// scroll into view as they land. A drag stops it; ending a drag, or its
// momentum, at the end starts it again, so a row that lands mid-drag or
// mid-coast never pulls the list from under the finger. Away from the end,
// the rows that were there when you left are what "↓ N new" counts against.
import { useCallback, useRef, useState } from "react";

/** How close to the end still counts as at the end, in points. */
export const AT_END_PT = 48;

/** How close to the top loads the page of older history above, in points. */
export const PAGE_OLDER_PT = 800;

export interface ScrollGeometry {
	contentOffset: { y: number };
	contentSize: { height: number };
	layoutMeasurement: { height: number };
	/** iOS: a bar the list runs under, as a bottom inset past its content. */
	contentInset?: { bottom: number };
}

/** Whether a scroll position is at the transcript's end: within AT_END_PT of
 * it. The end is past any bottom inset, so a list that fits in the viewport
 * above its inset is always at its end. */
export function atEnd({ contentOffset, contentSize, layoutMeasurement, contentInset }: ScrollGeometry): boolean {
	return contentOffset.y + layoutMeasurement.height >= contentSize.height + (contentInset?.bottom ?? 0) - AT_END_PT;
}

export interface LiveEndFollow {
	/** New rows scroll into view as they land. */
	following: boolean;
	/** What is moving the list: a finger, its momentum, or neither (the app). */
	touch: "none" | "dragging" | "momentum";
	/** The rows there when you left the end, or null at the end. */
	away: ReadonlySet<string> | null;
	/** You have moved this session's list yourself: by a drag, its coast, or
	 * an assistive scroll (VoiceOver, Switch Control, a keyboard), which sends
	 * neither but moves the list with no finger while the app neither follows
	 * the end nor restores a position. */
	dragged: boolean;
}

export type FollowEvent =
	| { type: "dragBegin" }
	| { type: "dragEnd"; atEnd: boolean }
	| { type: "momentumBegin" }
	/** The list moved with no finger on it while the app was neither
	 * following the end nor restoring a position: an assistive scroll. */
	| { type: "assistiveScroll" }
	| { type: "momentumEnd"; atEnd: boolean }
	/** Any scroll, yours or the app's; `keys` names the rows on screen now. */
	| { type: "scroll"; atEnd: boolean; keys: () => ReadonlySet<string> }
	/** Jump to live, or opening at the live end. */
	| { type: "follow" }
	/** A restore to a reading position, or a jump to a find match. */
	| { type: "unfollow" }
	/** A new session on the screen. */
	| { type: "reset"; following: boolean };

/** Whether the list at offset `y` loads the page of older history above:
 * near the top, once you have moved the list yourself. Opening at a reading
 * position near the top, or at the live end of a page shorter than the
 * screen, sits near the top with no drag; paging there chained every older
 * page in on open, each prepend landing the list near the top again. A short
 * page you drag still pages, though letting go there follows the end. */
export function pagesOlder(state: LiveEndFollow, y: number): boolean {
	return state.dragged && y < PAGE_OLDER_PT;
}

export function nextFollow(state: LiveEndFollow, event: FollowEvent): LiveEndFollow {
	switch (event.type) {
		case "dragBegin":
			return { ...state, following: false, touch: "dragging", dragged: true };
		case "assistiveScroll":
			return state.dragged ? state : { ...state, dragged: true };
		case "momentumBegin":
			// A release inside the end band may still coast away from it: follow
			// only once the momentum settles there.
			return { ...state, following: false, touch: "momentum", dragged: true };
		case "dragEnd":
		case "momentumEnd":
			return event.atEnd
				? { ...state, following: true, touch: "none", away: null }
				: { ...state, following: false, touch: "none" };
		case "scroll":
			if (event.atEnd) return state.away === null ? state : { ...state, away: null };
			// While following, only the app scrolls the list, toward the end:
			// that is no leaving it.
			if (state.away !== null || (state.following && state.touch === "none")) return state;
			return { ...state, away: event.keys() };
		case "follow":
			return { ...state, following: true, touch: "none", away: null };
		case "unfollow":
			return { ...state, following: false, away: null };
		case "reset":
			return { following: event.following, touch: "none", away: null, dragged: false };
	}
}

/** The follow state for a screen: `state.current` for handlers to read at
 * once, and `away` as React state, since the "↓ new" pill renders from it.
 * `onFollowing` hears whether the reader follows the live end whenever it
 * changes, and on every reset: a reset opens a session, whose store may be new
 * and hasn't heard. */
export function useLiveEndFollow(onFollowing?: (following: boolean) => void) {
	const state = useRef<LiveEndFollow>({ following: false, touch: "none", away: null, dragged: false });
	const [away, setAway] = useState<ReadonlySet<string> | null>(null);
	const reported = useRef<boolean | null>(null);
	const listener = useRef(onFollowing);
	listener.current = onFollowing;
	const dispatch = useCallback((event: FollowEvent) => {
		state.current = nextFollow(state.current, event);
		setAway(state.current.away);
		if (event.type === "reset" || reported.current !== state.current.following) {
			reported.current = state.current.following;
			listener.current?.(state.current.following);
		}
	}, []);
	return { state, away, dispatch };
}
