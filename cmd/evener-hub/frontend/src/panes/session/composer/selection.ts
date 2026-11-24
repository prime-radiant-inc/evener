import type { ComposerMention, ImageMarkerRange } from "@evener/appwire-client";

/** Whether two string lists name the same values in the same order. */
export function sameStrings(left: readonly string[], right: readonly string[]): boolean {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

/** Whether two range lists name the same occurrences: same marker and position,
 * with order and repeats carrying no meaning. */
export function sameImageRanges(left: readonly ImageMarkerRange[], right: readonly ImageMarkerRange[]): boolean {
  const key = (range: ImageMarkerRange) => `${range.marker}:${range.start}:${range.end}`;
  return sameStrings(sortedKeys(left, key), sortedKeys(right, key));
}

/** Whether two marker lists name the same numbers, with order and repeats
 * carrying no meaning. */
export function sameMarkers(left: readonly number[], right: readonly number[]): boolean {
  const markers = new Set(left);
  return right.every((marker) => markers.has(marker)) && markers.size === new Set(right).size;
}

/** Whether two mention lists describe the same atoms in the same order. */
export function sameMentions(left: readonly ComposerMention[], right: readonly ComposerMention[]): boolean {
  const key = (mention: ComposerMention) => `${mention.kind}\u0000${mention.name}\u0000${mention.offset}`;
  return sameStrings(left.map(key), right.map(key));
}

function sortedKeys<T>(values: readonly T[], key: (value: T) => string): string[] {
  return [...new Set(values.map(key))].sort();
}
