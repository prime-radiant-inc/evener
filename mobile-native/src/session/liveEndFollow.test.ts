// Following the transcript's live end (spec 8.2), as a pure state machine:
// whether new rows scroll into view, whether a finger or its momentum is
// moving the list, and the rows that were there when you left the end (what
// "↓ N new" counts against).
import { describe, expect, it } from "vitest";
import { AT_END_PT, atEnd, type LiveEndFollow, nextFollow, PAGE_OLDER_PT, pagesOlder } from "./liveEndFollow";

const scrolled = (y: number, content = 4_000, viewport = 600) => ({
	contentOffset: { y },
	contentSize: { height: content },
	layoutMeasurement: { height: viewport },
});

const rows = new Set(["a", "b"]);
const keys = () => rows;
const following: LiveEndFollow = { following: true, touch: "none", away: null, dragged: false };

describe("atEnd", () => {
	it("is true within the end band and false above it", () => {
		expect(atEnd(scrolled(4_000 - 600))).toBe(true);
		expect(atEnd(scrolled(4_000 - 600 - AT_END_PT))).toBe(true);
		expect(atEnd(scrolled(4_000 - 600 - AT_END_PT - 1))).toBe(false);
	});

	it("counts a bottom content inset (a bar the list runs under) as part of the end", () => {
		const underBar = { ...scrolled(4_000 - 600 + 180), contentInset: { bottom: 180 } };
		expect(atEnd(underBar)).toBe(true);
		expect(atEnd({ ...scrolled(4_000 - 600), contentInset: { bottom: 180 } })).toBe(false);
	});

	it("is true when everything fits in the viewport", () => {
		expect(atEnd(scrolled(0, 300, 600))).toBe(true);
		expect(atEnd({ ...scrolled(0, 420, 600), contentInset: { bottom: 180 } })).toBe(true);
		// It would fit without the inset; the inset leaves it scrolled short of its end.
		expect(atEnd(scrolled(0, 500, 600))).toBe(true);
		expect(atEnd({ ...scrolled(0, 500, 600), contentInset: { bottom: 180 } })).toBe(false);
	});
});

describe("nextFollow", () => {
	it("stops following the moment a drag begins", () => {
		expect(nextFollow(following, { type: "dragBegin" })).toEqual({
			following: false,
			touch: "dragging",
			away: null,
			dragged: true,
		});
	});

	it("follows again when the drag ends at the end, and not before", () => {
		let state = nextFollow(following, { type: "dragBegin" });
		state = nextFollow(state, { type: "scroll", atEnd: true, keys });
		expect(state.following).toBe(false);
		state = nextFollow(state, { type: "dragEnd", atEnd: true });
		expect(state).toEqual({ following: true, touch: "none", away: null, dragged: true });
	});

	it("doesn't follow after a drag that reached the end and came back up", () => {
		let state = nextFollow(following, { type: "dragBegin" });
		state = nextFollow(state, { type: "scroll", atEnd: true, keys });
		state = nextFollow(state, { type: "scroll", atEnd: false, keys });
		state = nextFollow(state, { type: "dragEnd", atEnd: false });
		expect(state).toEqual({ following: false, touch: "none", away: rows, dragged: true });
	});

	it("follows again when a flick's momentum settles at the end", () => {
		let state = nextFollow(following, { type: "dragBegin" });
		state = nextFollow(state, { type: "scroll", atEnd: false, keys });
		state = nextFollow(state, { type: "dragEnd", atEnd: false });
		state = nextFollow(state, { type: "momentumBegin" });
		expect(state.touch).toBe("momentum");
		state = nextFollow(state, { type: "momentumEnd", atEnd: true });
		expect(state).toEqual({ following: true, touch: "none", away: null, dragged: true });
	});

	it("holds off following while momentum moves the list, even from a release at the end", () => {
		// Let go inside the end band with an upward flick: a row landing while
		// the list coasts must not yank it back down.
		let state = nextFollow(following, { type: "dragBegin" });
		state = nextFollow(state, { type: "dragEnd", atEnd: true });
		state = nextFollow(state, { type: "momentumBegin" });
		expect(state).toEqual({ following: false, touch: "momentum", away: null, dragged: true });
	});

	it("stops following when momentum carries you off the end", () => {
		let state = nextFollow(following, { type: "dragBegin" });
		state = nextFollow(state, { type: "dragEnd", atEnd: true });
		state = nextFollow(state, { type: "momentumBegin" });
		state = nextFollow(state, { type: "momentumEnd", atEnd: false });
		expect(state.following).toBe(false);
	});

	it("remembers the rows there when you leave the end, once, and forgets them at the end", () => {
		let state = nextFollow(following, { type: "dragBegin" });
		state = nextFollow(state, { type: "scroll", atEnd: false, keys });
		expect(state.away).toBe(rows);
		state = nextFollow(state, { type: "scroll", atEnd: false, keys: () => new Set(["c"]) });
		expect(state.away).toBe(rows);
		state = nextFollow(state, { type: "scroll", atEnd: true, keys });
		expect(state.away).toBeNull();
	});

	it("leaves nothing away while the app scrolls a followed list itself", () => {
		// Following, a row landing scrolls the list for you; the scroll events
		// on the way pass above the end band and mustn't flash the pill.
		expect(nextFollow(following, { type: "scroll", atEnd: false, keys })).toBe(following);
	});

	it("stays unfollowed when the app scrolls to the end on its own", () => {
		const reading: LiveEndFollow = { following: false, touch: "none", away: rows, dragged: false };
		expect(nextFollow(reading, { type: "scroll", atEnd: true, keys })).toEqual({
			following: false,
			touch: "none",
			away: null,
			dragged: false,
		});
	});

	it("follows on Jump to live, and stops for a restore or a find", () => {
		const reading: LiveEndFollow = { following: false, touch: "none", away: rows, dragged: false };
		expect(nextFollow(reading, { type: "follow" })).toEqual({
			following: true,
			touch: "none",
			away: null,
			dragged: false,
		});
		expect(nextFollow(following, { type: "unfollow" })).toEqual({
			following: false,
			touch: "none",
			away: null,
			dragged: false,
		});
	});

	it("starts over for a new session", () => {
		const dragging: LiveEndFollow = { following: false, touch: "dragging", away: rows, dragged: true };
		expect(nextFollow(dragging, { type: "reset", following: true })).toEqual(following);
		expect(nextFollow(dragging, { type: "reset", following: false })).toEqual({
			following: false,
			touch: "none",
			away: null,
			dragged: false,
		});
	});
});

describe("pagesOlder", () => {
	const reading: LiveEndFollow = { following: false, touch: "none", away: null, dragged: false };

	it("pages near the top once you have moved the list yourself", () => {
		const moved = nextFollow(reading, { type: "dragBegin" });
		expect(pagesOlder(moved, PAGE_OLDER_PT - 1)).toBe(true);
		expect(pagesOlder(moved, PAGE_OLDER_PT)).toBe(false);
	});

	it("doesn't page before you have, even near the top", () => {
		// Opening at a reading position near the top, or at the live end of a
		// page shorter than the screen, puts the list near its top with no drag.
		expect(pagesOlder(reading, 0)).toBe(false);
		expect(pagesOlder(following, 0)).toBe(false);
	});

	it("pages on a short page you drag, even though letting go there follows the end", () => {
		let state = nextFollow(following, { type: "dragBegin" });
		state = nextFollow(state, { type: "dragEnd", atEnd: true });
		expect(state.following).toBe(true);
		expect(pagesOlder(state, 0)).toBe(true);
	});

	it("counts the list moving with no finger and no app scroll, as VoiceOver moves it", () => {
		const moved = nextFollow(reading, { type: "assistiveScroll" });
		expect(moved.dragged).toBe(true);
		expect(pagesOlder(moved, 0)).toBe(true);
	});

	it("counts a coast as moving the list, as a flick's momentum is", () => {
		expect(nextFollow(reading, { type: "momentumBegin" }).dragged).toBe(true);
	});
});
