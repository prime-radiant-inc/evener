// Which transcript entries hold the words being looked for. Find searches
// the conversation itself: what the person typed and what the agent replied.
// A hit is one projected message, so the count agrees with what the reader
// steps through, and every hit has its words on screen once its row mounts:
// a message is never folded, cut short or hidden behind a disclosure. Tool
// calls, thoughts, steers and system notices are not searched.
import {
  entryDisplayKey,
  findNeedle,
  findReadable,
  type ItemModel,
  type ProjectedEntry,
  projectThread,
  type ThreadModel,
  type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import { type TranscriptBodyRow, transcriptRowsForProjection } from "../transcript/TranscriptBody";
import { messageAsShown } from "./messageText";

export interface FindHit {
  /** The entry's display key: its identity across paging and re-projection,
   * and the id its transcript anchor carries. */
  readonly key: string;
  /** The source item, which is how a caller outside the transcript names it. */
  readonly itemId: string;
  /** The virtual row that renders it. */
  readonly rowIndex: number;
}

// A message's searchable text, normalised once. Settled items keep their
// identity across re-projection, so while a reply streams only the growing
// item is read again; every other entry is a substring test.
const haystacks = new WeakMap<ItemModel, string | null>();

function haystack(item: ItemModel): string | null {
  let cached = haystacks.get(item);
  if (cached === undefined) {
    const shown = messageAsShown(item);
    cached = shown === null ? null : findReadable(shown).toLowerCase();
    haystacks.set(item, cached);
  }
  return cached;
}

function matches(entry: ProjectedEntry, needle: string): boolean {
  return entry.kind === "item" && entry.isMessage && (haystack(entry.item)?.includes(needle) ?? false);
}

/** The messages holding `query`, oldest first. */
export function findHits(rows: readonly TranscriptBodyRow[], query: string): FindHit[] {
  const needle = findNeedle(query);
  if (needle === "") return [];
  const hits: FindHit[] = [];
  rows.forEach((row, rowIndex) => {
    if (row.kind !== "turn") return;
    for (const entry of row.turn.entries) {
      if (matches(entry, needle)) hits.push({ key: entryDisplayKey(entry), itemId: entry.item.id, rowIndex });
    }
  });
  return hits;
}

/** The source items of `model` that hold `query` in a message `config`
 * shows: the items a caller outside the transcript can send Find to. */
export function findableItemIds(model: ThreadModel, config: TranscriptDisplayConfigV1, query: string): Set<string> {
  const rows = transcriptRowsForProjection(projectThread(model, config));
  return new Set(findHits(rows, query).map((found) => found.itemId));
}
