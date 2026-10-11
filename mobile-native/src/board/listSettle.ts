// When the Board's list may change (spec 7.3: it "never reorders while a
// finger is on it or it is scrolling"). A state machine over the list's touch
// and scroll events; the list is held in every state but idle. React Native
// never says "momentum will not start", and the touch cancel a scroll view
// sends as it takes a touch over can come before its drag begins, so a lifted
// finger waits a moment before the list settles. A glide or an app-driven
// scroll whose end event never arrives is let go by a deadline.

export type SettleState = "idle" | "touching" | "dragging" | "lifted" | "momentum" | "appScrolling";
export type SettleEvent =
	| "touchStart"
	| "touchEnd"
	| "scrollBeginDrag"
	| "scrollEndDrag"
	| "momentumBegin"
	| "momentumEnd"
	| "appScrollStart"
	| "deadline"
	| "reset";

/** How long each waiting state lasts before it settles on its own. lifted:
 * a drag or momentum that begins within 100ms keeps the list held. momentum:
 * past the longest iOS deceleration (about 5 seconds from the fastest
 * fling). appScrolling: a scroll to the offset the list already has sends no
 * end event. */
export const SETTLE_DEADLINE_MS: Readonly<Partial<Record<SettleState, number>>> = {
	lifted: 100,
	momentum: 6000,
	appScrolling: 1000,
};

export function nextSettleState(state: SettleState, event: SettleEvent): SettleState {
	switch (event) {
		case "reset":
			return "idle";
		case "touchStart":
			// A finger stops a glide or an app scroll; a second finger during a
			// drag changes nothing.
			return state === "dragging" ? "dragging" : "touching";
		case "touchEnd":
			// A drag ends with its own event.
			return state === "touching" ? "lifted" : state;
		case "scrollBeginDrag":
			return "dragging";
		case "scrollEndDrag":
			return state === "dragging" ? "lifted" : state;
		case "momentumBegin":
			// Ignored while a finger is down or the app is scrolling.
			return state === "touching" || state === "appScrolling" ? state : "momentum";
		case "momentumEnd":
			return state === "momentum" || state === "appScrolling" || state === "lifted" ? "idle" : state;
		case "appScrollStart":
			return state === "touching" || state === "dragging" ? state : "appScrolling";
		case "deadline":
			return SETTLE_DEADLINE_MS[state] === undefined ? state : "idle";
	}
}
