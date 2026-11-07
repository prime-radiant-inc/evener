/** The shortest side an iPhone Duo inner display offers, in points. */
export const DUO_INNER_MIN_POINTS = 600;

/** Whether a window is the Duo's inner display: both sides at least 600
 * points. The outer display is 466x678 and a regular iPhone in landscape is
 * shorter than 600 points, so neither becomes a split. */
export function isDuoInner(width: number, height: number): boolean {
	return width >= DUO_INNER_MIN_POINTS && height >= DUO_INNER_MIN_POINTS;
}
