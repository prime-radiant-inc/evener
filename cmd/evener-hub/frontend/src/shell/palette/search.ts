// Search mode: the palette's default mode (query is empty or doesn't start
// with "/"). Three concerns, all ported from search.js's search-mode block:
//   - fetchSearch: the backend session search over the typed AppWire method.
//   - findInSessionMatches: the "In session · N" section, scanning the focused
//     session's loaded ThreadModel (turns -> items -> text). Choosing a row
//     opens the session's own Find on it (panes/session/find).
//   - buildSnippet / highlightParts: the <mark>-highlighted context strings.
//     Returned as structured parts (not HTML) so the React overlay renders
//     <mark> without dangerouslySetInnerHTML - React escapes for us, so the
//     legacy's escapeHtml has no successor here.

import {
  type AppwireClientLike,
  findIndexIn,
  findNeedle,
  findReadable,
  itemFindTexts,
  type SearchResponse,
  type ThreadModel,
} from "@evener/appwire-client";

// One text run of a highlighted string: `mark` true means it is the matched
// substring (rendered inside <mark>), false means surrounding context.
export interface HighlightPart {
  text: string;
  mark: boolean;
}

export interface InSessionMatch {
  turn: number; // 1-based turn number, for the "turn N" row label
  itemId: string;
  snippet: HighlightPart[];
}

export function fetchSearch(query: string, client: AppwireClientLike): Promise<SearchResponse> {
  return client.request("evener/search", { query });
}

// What an item is searched by, and what counts as a match, are the shared
// find rules (@evener/appwire-client's findInSession): the palette lists the
// same matches the session's own Find steps through. Each matching text of an
// item is its own hit, all sharing the item's turn label.
export function findInSessionMatches(model: ThreadModel, query: string): InSessionMatch[] {
  const needle = findNeedle(query);
  if (needle === "") return [];
  const out: InSessionMatch[] = [];
  let turnNumber = 0;
  for (const turn of model.turns) {
    turnNumber += 1;
    for (const item of turn.items) {
      for (const raw of itemFindTexts(item)) {
        const text = findReadable(raw);
        const hit = findIndexIn(text, needle);
        if (hit < 0) continue;
        out.push({ turn: turnNumber, itemId: item.id, snippet: buildSnippet(text, hit, needle.length) });
      }
    }
  }
  return out;
}

const SNIPPET_CONTEXT = 40;

export function buildSnippet(text: string, hit: number, len: number): HighlightPart[] {
  const start = Math.max(0, hit - SNIPPET_CONTEXT);
  const end = Math.min(text.length, hit + len + SNIPPET_CONTEXT);
  const before = (start > 0 ? "…" : "") + text.slice(start, hit);
  const match = text.slice(hit, hit + len);
  const after = text.slice(hit + len, end) + (end < text.length ? "…" : "");
  return [
    { text: before, mark: false },
    { text: match, mark: true },
    { text: after, mark: false },
  ];
}

export function highlightParts(text: string, query: string): HighlightPart[] {
  if (!query) return [{ text, mark: false }];
  const i = text.toLowerCase().indexOf(query.toLowerCase());
  if (i < 0) return [{ text, mark: false }];
  return [
    { text: text.slice(0, i), mark: false },
    { text: text.slice(i, i + query.length), mark: true },
    { text: text.slice(i + query.length), mark: false },
  ];
}
