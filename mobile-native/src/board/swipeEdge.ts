/** A swipe that begins this close to the screen's left edge belongs to the
 * system's back gesture and never acts on a row (spec 7.3). */
export const EDGE_ZONE_PT = 24;

export function startsInEdgeZone(pageX: number): boolean {
	return pageX < EDGE_ZONE_PT;
}

/** The left hit slop that keeps the screen's edge band out of a row whose
 * frame starts `inset` points from the screen's. hitSlop measures from the
 * row's own edge, so an indented row needs a smaller trim, and a row already
 * at or past the band needs none (undefined). */
export function edgeBandHitSlop(inset: number): { left: number } | undefined {
	if (inset >= EDGE_ZONE_PT) return undefined;
	return { left: inset - EDGE_ZONE_PT };
}
