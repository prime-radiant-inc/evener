/** Whether an upward drag, released, dismisses the banner: past 30pt (the
 * prototype's threshold) or flicked upward. */
export function swipeDismisses(dy: number, vy: number): boolean {
	return dy < -30 || (dy < 0 && vy < -0.5);
}
