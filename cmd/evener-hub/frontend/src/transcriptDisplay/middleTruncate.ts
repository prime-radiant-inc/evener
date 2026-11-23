/** The head's share of a middle-truncation cut, in code points. */
export const MIDDLE_TRUNCATION_HEAD_SHARE = 0.6;

/** Walks a cut left off boundary whitespace and atomic spans: head and tail are
 * separate flex items, so whitespace must stay interior and an id must not split. */
export function walkTruncationCut(
  chars: readonly string[],
  cut: number,
  atomicStart?: (position: number) => number | undefined,
): number {
  let position = cut;
  while (position > 0 && position < chars.length) {
    const start = atomicStart?.(position);
    if (start !== undefined) position = start;
    else if (/\s/.test(chars[position - 1] ?? "") || /\s/.test(chars[position] ?? "")) position -= 1;
    else break;
  }
  return position;
}

/** Splits `text` at `cutOf`'s cut into head and tail, or null when one side would empty. */
export function middleTruncate(text: string, cutOf: (chars: readonly string[]) => number): [string, string] | null {
  const chars = Array.from(text);
  const position = walkTruncationCut(chars, cutOf(chars));
  if (position <= 0 || position >= chars.length) return null;
  return [chars.slice(0, position).join(""), chars.slice(position).join("")];
}
