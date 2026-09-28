// A horizontal pan on the Session's title moves to the next or previous
// session in Live order (spec 6): to the left for the next one, as a page
// turns, and to the right for the one before.

/** How far, in points, or how fast, in points a second, a pan must go. */
const DISTANCE_PT = 60;
const SPEED_PT_PER_S = 500;

/** 1 for the next session, -1 for the previous one, null for no move. A
 * pan that went one way and was flicked back the other is no move. */
export function titleSwipeDirection(translationX: number, velocityX: number): 1 | -1 | null {
	const left = translationX < -DISTANCE_PT || velocityX < -SPEED_PT_PER_S;
	const right = translationX > DISTANCE_PT || velocityX > SPEED_PT_PER_S;
	if (left === right) return null;
	return left ? 1 : -1;
}

/** How a session that replaced another slides in (the Conversation route's
 * animationTypeForReplace): the previous one in Live order from the left, as
 * a pop does, and any other from the right, as a push does. */
export function replaceAnimation(slideFrom: "left" | undefined): "pop" | "push" {
	return slideFrom === "left" ? "pop" : "push";
}
