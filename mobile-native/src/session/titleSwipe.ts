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
 * animationTypeForReplace): one the title's swipe opened from the side it
 * came from, the previous one from the left as a pop does and the next from
 * the right as a push does, and one Next opened from the right. Any other
 * replace, such as a new session's or a fork's, keeps the library's own. */
export function replaceAnimation(params: {
	slideFrom?: "left" | "right";
	openedBy?: "next";
}): "pop" | "push" | undefined {
	if (params.slideFrom) return params.slideFrom === "left" ? "pop" : "push";
	return params.openedBy === "next" ? "push" : undefined;
}
