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
  /** The call that delivered the message, for one said through a tool call. */
  readonly callId?: string;
  /** The model round the message came from, when it has one. */
  readonly roundId?: string;
}

// A reply still arriving is an overlay item until it is recorded: a streamed
// reply under "stream:<id>:<type>", one being said through a call under
// "preview:<callId>" (the wire's overlay slot keys).
const STREAM = "stream:";
const PREVIEW = "preview:";

function isOverlay(itemId: string): boolean {
  return itemId.startsWith(STREAM) || itemId.startsWith(PREVIEW);
}

/** Where the chosen match is among `hits`, or -1. A match is a message, named
 * by its source item; its display key is not its identity, because a round's
 * first loaded reply takes the round's key and so the key passes between
 * replies as the loaded window changes. The one time the item itself changes
 * is a reply being recorded: a streamed reply keeps its key, or, for a later
 * text run of the stream, only its round; a previewed one is the recorded
 * message of the same call. */
export function indexOfMatch(
  hits: readonly FindHit[],
  chosen: { key: string; itemId: string; roundId?: string | null },
): number {
  const same = hits.findIndex((hit) => hit.itemId === chosen.itemId);
  if (same >= 0) return same;
  if (chosen.itemId.startsWith(STREAM)) {
    const keyed = hits.findIndex((hit) => hit.key === chosen.key && !isOverlay(hit.itemId));
    if (keyed >= 0 || !chosen.roundId) return keyed;
    return hits.findIndex((hit) => hit.roundId === chosen.roundId && !isOverlay(hit.itemId));
  }
  if (chosen.itemId.startsWith(PREVIEW)) {
    const callId = chosen.itemId.slice(PREVIEW.length);
    return hits.findIndex((hit) => hit.callId === callId && !isOverlay(hit.itemId));
  }
  return -1;
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
      if (!matches(entry, needle)) continue;
      const { id, callId, roundId } = entry.item;
      hits.push({
        key: entryDisplayKey(entry),
        itemId: id,
        rowIndex,
        ...(callId ? { callId } : {}),
        ...(roundId ? { roundId } : {}),
      });
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
