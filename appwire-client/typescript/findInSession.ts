// Find in session, the part every client shares: what counts as a match,
// which loaded rows hold one, and stepping between them. Each client supplies
// the text of its own rows; older-history demand stays with HistoryPaging.
function collapseWhitespace(text: string): string {
  return text.replace(/\s+/g, " ");
}

/** What a typed query looks for. Case and runs of whitespace never decide a
 * match, so a phrase is found across a line break. Empty means no search. */
export function findNeedle(query: string): string {
  return collapseWhitespace(query).trim().toLowerCase();
}

/** Text as find reads it: whitespace collapsed, case kept. An index into it
 * addresses the same characters a lower-cased copy holds. */
export function findReadable(text: string): string {
  return collapseWhitespace(text).trim();
}

/** Where `needle` (from findNeedle) first sits in findReadable(text), or -1.
 *
 * A caller slices the readable text at the result, so the index counts
 * characters of `readable`, never of its lower-cased copy. Lower-casing can
 * make one character into two units ("İ" becomes "i" plus a combining dot),
 * which shifts every offset after it: the table below keeps the two in step.
 * Such an expansion can also place a copy offset where no character begins, so
 * an occurrence starting there is skipped in favor of the next one. */
export function findIndexIn(readable: string, needle: string): number {
  if (needle === "") return -1;
  const loweredText = readable.toLowerCase();
  // Where each offset of the lower-cased copy begins in `readable`. An offset
  // inside a character's expansion has no entry: no character starts there.
  const starts: number[] = [];
  let lowered = 0;
  let index = 0;
  for (const char of readable) {
    starts[lowered] = index;
    lowered += char.toLowerCase().length;
    index += char.length;
  }
  starts[lowered] = index;
  // A match can begin inside an expansion, so try each occurrence until one
  // starts on a character.
  for (let at = loweredText.indexOf(needle); at >= 0; at = loweredText.indexOf(needle, at + 1)) {
    const start = starts[at];
    if (start !== undefined) return start;
  }
  return -1;
}

export function textMatchesFind(text: string, needle: string): boolean {
  return findIndexIn(findReadable(text), needle) >= 0;
}

/** The indexes of the rows holding `query`, oldest first. */
export function findMatches<Row>(rows: readonly Row[], query: string, textOf: (row: Row) => string): number[] {
  const needle = findNeedle(query);
  if (needle === "") return [];
  const matches: number[] = [];
  rows.forEach((row, index) => {
    if (textMatchesFind(textOf(row), needle)) matches.push(index);
  });
  return matches;
}

/** The match to show next. With none shown yet it starts at the newest, since
 * a reader is usually at the end. Stepping back (-1) goes older and forward
 * (1) newer. Null means there is none that way: stepping back, the client
 * then asks for older history and tries again. */
export function stepMatch(matches: readonly number[], current: number | null, direction: 1 | -1): number | null {
  if (matches.length === 0) return null;
  if (current === null) return matches[matches.length - 1] ?? null;
  if (direction === 1) return matches.find((index) => index > current) ?? null;
  let older: number | null = null;
  for (const index of matches) {
    if (index >= current) break;
    older = index;
  }
  return older;
}

/** Where a reader is among the matches: which one (1-based, 0 with none) of
 * how many, counted oldest first. With no current match it sits on the
 * newest. */
export interface MatchPosition {
  position: number;
  count: number;
}

export function matchPosition(matches: readonly number[], current: number | null): MatchPosition {
  if (matches.length === 0) return { position: 0, count: 0 };
  return { position: current === null ? matches.length : matches.indexOf(current) + 1, count: matches.length };
}
