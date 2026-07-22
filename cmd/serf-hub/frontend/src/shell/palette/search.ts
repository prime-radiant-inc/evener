// Search mode: the palette's default mode (query is empty or doesn't start
// with "/"). Three concerns, all ported from search.js's search-mode block:
//   - fetchSearch: the backend session search. REST GET /api/search?q= only -
//     there is NO appwire `search` method (parity-m6-surfaces.md §2.3,
//     verified against the wire catalog). Wire shape is pinned against
//     cmd/serf-hub/web_api.go handleApiSearch + web_types.go searchResponse.
//   - findInSessionMatches: the "In session · N" section. The legacy scanned
//     #conversation's DOM (search.js:961-982); the new transcript is
//     virtualized, so this scans the focused session's ThreadModel directly
//     (turns -> items -> text) - the plan-resolved successor.
//   - buildSnippet / highlightParts: the <mark>-highlighted context strings.
//     Returned as structured parts (not HTML) so the React overlay renders
//     <mark> without dangerouslySetInnerHTML - React escapes for us, so the
//     legacy's escapeHtml has no successor here.

import type { ItemModel, ThreadModel } from "../../protocol/model";

export interface SearchResult {
  id: string;
  title: string;
  project: string;
  state: string;
  age: string;
}

export interface SearchResponse {
  live: SearchResult[];
  past: SearchResult[];
}

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

// The daemon auth-guards the whole mux (see auth.ts) and reads a same-origin
// cookie/bearer, so this rides same-origin credentials like every other REST
// call in this app (stores/tree.ts FETCH_INIT, shell/rail/actions.ts).
export async function fetchSearch(query: string): Promise<SearchResponse> {
  const res = await fetch(`/api/search?q=${encodeURIComponent(query)}`, { credentials: "same-origin" });
  if (!res.ok) throw new Error(`search failed: HTTP ${res.status}`);
  const data = (await res.json()) as { live?: SearchResult[] | null; past?: SearchResult[] | null };
  return {
    // Go encodes an empty result slice as JSON null, not [] - normalize both
    // (and a missing field) to an empty array.
    live: Array.isArray(data.live) ? data.live : [],
    past: Array.isArray(data.past) ? data.past : [],
  };
}

// normalizeWhitespace collapses runs of whitespace to a single space and
// trims, matching the legacy's `(el.textContent||"").replace(/\s+/g," ").trim()`
// so a match spanning a newline in the model's item text still resolves.
function normalizeWhitespace(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}

// itemSearchTexts gathers every user-VISIBLE text an item renders in the
// transcript, so search finds what the user can see (reviewer adjudication
// I2), not just the settled message text: an item's `text`, a tool call's
// `output` and `error`, and each live reasoning summary (its streamed chunks
// joined). Each source that matches produces its own hit, all sharing the
// item's turn label.
function itemSearchTexts(item: ItemModel): string[] {
  const texts: string[] = [];
  if (item.text) texts.push(item.text);
  if (item.output) texts.push(item.output);
  if (item.error) texts.push(item.error);
  for (const summary of item.reasoningSummaries ?? []) {
    const joined = summary.join("");
    if (joined) texts.push(joined);
  }
  return texts;
}

export function findInSessionMatches(model: ThreadModel, query: string): InSessionMatch[] {
  const q = query.toLowerCase();
  if (!q) return [];
  const out: InSessionMatch[] = [];
  let turnNumber = 0;
  for (const turn of model.turns) {
    turnNumber += 1;
    for (const item of turn.items) {
      for (const raw of itemSearchTexts(item)) {
        const text = normalizeWhitespace(raw);
        if (!text) continue;
        const hit = text.toLowerCase().indexOf(q);
        if (hit < 0) continue;
        out.push({ turn: turnNumber, itemId: item.id, snippet: buildSnippet(text, hit, query.length) });
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
