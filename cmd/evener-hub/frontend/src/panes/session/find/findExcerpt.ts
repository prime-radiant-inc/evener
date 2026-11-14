// Cutting the stretch of a long text that holds the words Find landed on.

// The needle is whitespace-collapsed and lowercased (findNeedle); in the text
// its words may be separated by any run of whitespace.
function pattern(needle: string): RegExp {
  const words = needle.split(" ").map((word) => word.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  return new RegExp(words.join("\\s+"), "i");
}

/** Whether `text` holds the needle, by the rule excerpts are cut with. */
export function holdsNeedle(text: string, needle: string): boolean {
  return pattern(needle).test(text);
}

/** At most `budget` characters of `text` around the needle, under a line
 * saying the rest is not shown; the whole text when it fits. Null when the
 * text does not hold the needle. */
export function excerptAround(text: string, needle: string, budget: number): string | null {
  const match = pattern(needle).exec(text);
  if (match === null) return null;
  if (text.length <= budget) return text;
  const start = Math.max(0, Math.min(match.index - budget / 2, text.length - budget));
  const end = Math.min(text.length, start + budget);
  const note = `showing ${(end - start).toLocaleString("en-US")} chars around the match — the rest is not shown`;
  return `${note}\n${text.slice(start, end)}`;
}
