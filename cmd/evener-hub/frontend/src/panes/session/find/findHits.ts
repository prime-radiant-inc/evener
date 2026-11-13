// Which transcript entries hold the words being looked for. A hit is one
// projected entry - a message, a thought, a tool call - so the count agrees
// with what the reader steps through, and an entry the current verbosity
// leaves out is not a hit.
import {
  entryDisplayKey,
  findNeedle,
  findReadable,
  type ItemModel,
  itemFindTexts,
  type ProjectedEntry,
  projectThread,
  type ThreadModel,
  type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import { type TranscriptBodyRow, transcriptRowsForProjection } from "../transcript/TranscriptBody";
import { foldTurnEntries, intentGroupId } from "../transcript/toolRuns";

export interface FindHit {
  /** The entry's display key: its identity across paging and re-projection,
   * and the id its transcript anchor carries. */
  readonly key: string;
  /** The source item, which is how a caller outside the transcript names it. */
  readonly itemId: string;
  /** The virtual row that renders it. */
  readonly rowIndex: number;
  /** The disclosures that stand between the reader and the matching words,
   * outermost first: the fold hiding the entry (a tool run or an action
   * group), then the entry's own disclosures when it is a tool call or a
   * thought.
   * A message has none. */
  readonly opens: readonly string[];
}

// An item's searchable text, normalised once. Settled items keep their
// identity across re-projection, so while a reply streams only the growing
// item is read again; every other entry is a substring test.
const itemHaystacks = new WeakMap<ItemModel, readonly string[]>();

function haystack(text: string): string {
  return findReadable(text).toLowerCase();
}

function itemHaystack(item: ItemModel): readonly string[] {
  let cached = itemHaystacks.get(item);
  if (cached === undefined) {
    cached = itemFindTexts(item).map(haystack);
    itemHaystacks.set(item, cached);
  }
  return cached;
}

function entryMatches(entry: ProjectedEntry, needle: string): boolean {
  if (entry.kind === "intent" && haystack(entry.rationale).includes(needle)) return true;
  if (entry.kind === "critical") {
    if (haystack(entry.summary).includes(needle)) return true;
    // A redacted thought never renders its text, so it cannot be found by it.
    if (entry.redacted) return false;
  }
  return itemHaystack(entry.item).some((text) => text.includes(needle));
}

function hit(entry: ProjectedEntry, rowIndex: number, fold?: string): FindHit {
  const key = entryDisplayKey(entry);
  const itemId = entry.kind === "intent" ? entry.sourceItemId : entry.item.id;
  const opens = fold === undefined ? [] : [fold];
  // A tool call keeps what it was asked behind its summary disclosure and
  // what came back behind its body; a thought's body goes by its display key.
  if (!(entry.kind === "item" && entry.isMessage)) opens.push(...new Set([itemId, key, `summary:${itemId}`]));
  return { key, itemId, rowIndex, opens };
}

/** The entries holding `query`, oldest first. Folds follow the one fold the
 * transcript renders from (toolRuns.ts), so a hit inside a closed run knows
 * which disclosure opens it. */
export function findHits(rows: readonly TranscriptBodyRow[], query: string): FindHit[] {
  const needle = findNeedle(query);
  if (needle === "") return [];
  const hits: FindHit[] = [];
  rows.forEach((row, rowIndex) => {
    if (row.kind === "intentGroup") {
      for (const entry of row.entries) if (entryMatches(entry, needle)) hits.push(hit(entry, rowIndex, row.id));
      return;
    }
    const laidOut = foldTurnEntries(row.turn);
    for (let index = 0; index < laidOut.length; index += 1) {
      const entry = laidOut[index];
      if (!entry) continue;
      if (entry.kind === "run") {
        for (const member of entry.entries)
          if (entryMatches(member, needle)) hits.push(hit(member, rowIndex, entry.id));
        continue;
      }
      if (entry.kind === "intent") {
        // TurnBlock groups consecutive intent entries under the first one's id.
        const group = intentGroupId(entry.id);
        let cursor = index;
        for (;;) {
          const member = laidOut[cursor];
          if (member?.kind !== "intent") break;
          if (entryMatches(member, needle)) hits.push(hit(member, rowIndex, group));
          cursor += 1;
        }
        index = cursor - 1;
        continue;
      }
      if (entryMatches(entry, needle)) hits.push(hit(entry, rowIndex));
    }
  });
  return hits;
}

/** The source items of `model` that hold `query` in an entry `config` shows:
 * the items a caller outside the transcript can send Find to. */
export function findableItemIds(model: ThreadModel, config: TranscriptDisplayConfigV1, query: string): Set<string> {
  const rows = transcriptRowsForProjection(projectThread(model, config));
  return new Set(findHits(rows, query).map((found) => found.itemId));
}
