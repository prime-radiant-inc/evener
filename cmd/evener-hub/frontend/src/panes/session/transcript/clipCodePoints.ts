// Truncates text to a code-point budget. Counting code points rather than
// UTF-16 units keeps an emoji or other astral character whole; the appended
// ellipsis is the only mark that the text was clipped. Callers that clip a
// single line of prose pass trimEnd so the ellipsis never follows a space.
export function clipCodePoints(text: string, max: number, { trimEnd = false }: { trimEnd?: boolean } = {}): string {
  const codePoints = Array.from(text);
  if (codePoints.length <= max) return text;
  const clipped = codePoints.slice(0, max).join("");
  return `${trimEnd ? clipped.trimEnd() : clipped}…`;
}
