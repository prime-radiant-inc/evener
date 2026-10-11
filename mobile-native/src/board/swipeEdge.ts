/** A swipe that begins this close to the screen's left edge belongs to the
 * system's back gesture and never acts on a row (spec 7.3). */
export const EDGE_ZONE_PT = 24;

export function startsInEdgeZone(pageX: number): boolean {
	return pageX < EDGE_ZONE_PT;
}
