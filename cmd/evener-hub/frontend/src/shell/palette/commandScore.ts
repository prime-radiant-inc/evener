// Fuzzy command ranking, ported verbatim from search.js:590-617. commandScore
// takes the best of an exact substring hit (worth 200 - matchIndex, so an
// earlier match ranks higher) or a fuzzy subsequence score across the
// command's id + title + keywords. fuzzyScore rewards matches that start at a
// word boundary and runs of consecutive characters, and penalizes gaps; a
// needle that is not a subsequence at all scores -1, which commandScore's
// callers treat as "exclude entirely".
//
// The query `q` is expected pre-lowercased and pre-trimmed by the caller
// (renderCommands lowercases the filter before scoring), matching the legacy
// contract exactly - commandScore lowercases each FIELD but never q.

export interface ScorableCommand {
  id: string;
  title: string;
  keywords: string[];
}

export function fuzzyScore(needle: string, haystack: string): number {
  if (!needle) return 0;
  let score = 0;
  let pos = -1;
  let streak = 0;
  for (const ch of needle) {
    const next = haystack.indexOf(ch, pos + 1);
    if (next < 0) return -1;
    streak = next === pos + 1 ? streak + 1 : 0;
    const boundary = next === 0 || /[\s/_-]/.test(haystack.charAt(next - 1));
    score += 10 + (boundary ? 8 : 0) + streak * 4 - Math.min(next - pos - 1, 8);
    pos = next;
  }
  return score;
}

export function commandScore(command: ScorableCommand, q: string): number {
  if (!q) return 0;
  const fields = [command.id, command.title, ...command.keywords];
  let best = -1;
  for (const raw of fields) {
    const text = raw.toLowerCase();
    const exact = text.indexOf(q);
    if (exact >= 0) best = Math.max(best, 200 - exact);
    best = Math.max(best, fuzzyScore(q, text));
  }
  return best;
}
