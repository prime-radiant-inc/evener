export interface MeasuredAnchor {
  readonly key: string;
  readonly offsetPx: number;
  readonly priorIndex: number;
}

export interface MeasuredRow {
  readonly key: string | number | bigint;
  readonly index: number;
  readonly start: number;
  readonly end: number;
}

export const ANCHOR_TOLERANCE_PX = 2;

/** Capture the first measured row whose bottom edge intersects the viewport. */
export function captureMeasuredAnchor(
  rows: readonly MeasuredRow[],
  scrollOffset: number,
): MeasuredAnchor | null {
  const row = rows.find((candidate) => candidate.end > scrollOffset);
  if (row === undefined) return null;
  return {
    key: String(row.key),
    offsetPx: row.start - scrollOffset,
    priorIndex: row.index,
  };
}

/**
 * Resolve an anchor through an authoritative item snapshot. Stable identity
 * wins, followed by the next survivor, previous survivor, and finally tail.
 */
export function resolveReconciledAnchor(
  previousKeys: readonly string[],
  nextKeys: readonly string[],
  anchor: MeasuredAnchor,
): string | null {
  if (nextKeys.includes(anchor.key)) return anchor.key;
  for (let index = anchor.priorIndex; index < previousKeys.length; index += 1) {
    const key = previousKeys[index];
    if (key !== undefined && nextKeys.includes(key)) return key;
  }
  for (let index = anchor.priorIndex - 1; index >= 0; index -= 1) {
    const key = previousKeys[index];
    if (key !== undefined && nextKeys.includes(key)) return key;
  }
  return nextKeys.at(-1) ?? null;
}
