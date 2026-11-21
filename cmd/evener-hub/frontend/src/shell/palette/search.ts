// Search mode: the palette's default mode (query is empty or doesn't start
// with "/"). Three concerns, all ported from search.js's search-mode block:
//   - fetchSearch: the backend session search over the typed AppWire method.
//   - findInSessionMatches: the "In session · N" section, scanning the focused
//     session's loaded ThreadModel (turns -> items -> text). Choosing a
//     message, thought or action row opens the session's own Find on it
//     (panes/session/find).
//   - buildSnippet / highlightParts: the <mark>-highlighted context strings.
//     Returned as structured parts (not HTML) so the React overlay renders
//     <mark> without dangerouslySetInnerHTML - React escapes for us, so the
//     legacy's escapeHtml has no successor here.

import {
  type AppwireClientLike,
  findIndexIn,
  findNeedle,
  type ItemModel,
  MEMORY_CONTEXT_EVENT_KIND,
  type SearchResponse,
  type ThreadModel,
  type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import { projectedSearchTexts } from "../../panes/session/find/findHits";
import { messageAsShown } from "../../panes/session/find/messageText";

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
function itemSearchTexts(item: ItemModel, projected: ReadonlyMap<string, readonly string[]>): string[] {
  const texts: string[] = [];
  // A message is read as its row shows it (a reply's Markdown rendered), the
  // way the session's own Find reads it, so a message row here is one Find
  // can land on.
  const message = messageAsShown(item);
  // A system notice or warning is read the way its row shows it (projected
  // below), not from the raw wire text: a scaffold's raw Markdown and a
  // failure's echoed line are not what is on screen. A memory refresh stays on
  // its raw text until its own slice.
  const projectedByNotice =
    item.type === "warning" || (item.type === "systemMessage" && item.eventKind !== MEMORY_CONTEXT_EVENT_KIND);
  // A thought's `text` is its trace, which no row shows, so only the
  // summaries below are read for it.
  if (message !== null) texts.push(message);
  else if (item.type !== "reasoning" && !projectedByNotice && item.text) {
    texts.push(item.text);
  }
  // What the view shows this item as - an action row's rationale, a thought's
  // summaries where the view shows the thought. Find's own rule, shared, so a
  // hit here is always one Find can land on.
  const shown = projected.get(item.id);
  if (shown !== undefined) texts.push(...shown);
  if (item.output) texts.push(item.output);
  if (item.error) texts.push(item.error);
  return texts;
}

export function findInSessionMatches(
  model: ThreadModel,
  query: string,
  config: TranscriptDisplayConfigV1,
): InSessionMatch[] {
  // The shared find needle: case and the shape of whitespace never decide a
  // match, here or in the session's Find.
  const q = findNeedle(query);
  if (!q) return [];
  const projected = projectedSearchTexts(model, config);
  const out: InSessionMatch[] = [];
  let turnNumber = 0;
  for (const turn of model.turns) {
    turnNumber += 1;
    for (const item of turn.items) {
      for (const raw of new Set(itemSearchTexts(item, projected))) {
        const text = normalizeWhitespace(raw);
        if (!text) continue;
        const hit = findIndexIn(text, q);
        if (hit < 0) continue;
        out.push({ turn: turnNumber, itemId: item.id, snippet: buildSnippet(text, hit, q.length) });
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
  const needle = findNeedle(query);
  const i = findIndexIn(text, needle);
  if (i < 0) return [{ text, mark: false }];
  return [
    { text: text.slice(0, i), mark: false },
    { text: text.slice(i, i + needle.length), mark: true },
    { text: text.slice(i + needle.length), mark: false },
  ];
}
