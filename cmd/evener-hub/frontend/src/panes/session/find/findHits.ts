// Which transcript entries hold the words being looked for. A hit is one
// projected entry - a message, a thought, a tool call - so the count agrees
// with what the reader steps through, and an entry the current verbosity
// leaves out is not a hit.
import {
  entryDisplayKey,
  findNeedle,
  findReadable,
  type ItemModel,
  isParentSteer,
  itemFindTexts,
  MEMORY_CONTEXT_EVENT_KIND,
  type ProjectedEntry,
  projectThread,
  steeringNotificationFragments,
  type ThreadModel,
  type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import { type TranscriptBodyRow, transcriptRowsForProjection } from "../transcript/TranscriptBody";
import { toolRendererFor } from "../transcript/toolRenderers";
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
   * A message has none. Each is a disclosure id within the transcript view. */
  readonly opens: readonly string[];
  /** The disclosures to open whose state belongs to the session rather than
   * to one view of it: the divider a steer, a note or a memory refresh keeps
   * its text behind. Each is the id that divider was given. */
  readonly sessionOpens: readonly string[];
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
  // A call whose descriptor suppresses its row has nothing to land on.
  if (entry.item.type === "commandExecution" && toolRendererFor(entry.item.toolName ?? "").suppress?.(entry.item)) {
    return false;
  }
  if (entry.kind === "intent" && haystack(entry.rationale).includes(needle)) return true;
  if (entry.kind === "critical") {
    if (haystack(entry.summary).includes(needle)) return true;
    // A redacted thought never renders its raw text; what it can unfold is
    // its summaries.
    if (entry.redacted) {
      return (entry.item.reasoningSummaries ?? []).some((summary) => haystack(summary.join("")).includes(needle));
    }
  }
  return itemHaystack(entry.item).some((text) => text.includes(needle));
}

// The dividers of a steer or a memory refresh that hold the words. A steer
// delivering notifications is cut into pieces, each stretch of text behind a
// divider of its own.
function dividersHolding(item: ItemModel, needle: string): string[] {
  if (item.type !== "steering") return item.eventKind === MEMORY_CONTEXT_EVENT_KIND ? [item.id] : [];
  const pieces = isParentSteer(item.source) ? null : steeringNotificationFragments(item.text ?? "");
  if (pieces === null) return [item.id];
  return pieces.flatMap((piece, index) =>
    piece.kind === "text" && haystack(piece.text).includes(needle) ? [`${item.id}:${index}`] : [],
  );
}

function hit(entry: ProjectedEntry, rowIndex: number, needle: string, fold?: string): FindHit {
  const key = entryDisplayKey(entry);
  const itemId = entry.kind === "intent" ? entry.sourceItemId : entry.item.id;
  const opens = fold === undefined ? [] : [fold];
  // A tool call keeps what it was asked behind its summary disclosure and
  // what came back behind its body; a thought's body goes by its display key.
  if (!(entry.kind === "item" && entry.isMessage)) opens.push(...new Set([itemId, key, `summary:${itemId}`]));
  return { key, itemId, rowIndex, opens, sessionOpens: dividersHolding(entry.item, needle) };
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
      for (const entry of row.entries) if (entryMatches(entry, needle)) hits.push(hit(entry, rowIndex, needle, row.id));
      return;
    }
    const laidOut = foldTurnEntries(row.turn);
    for (let index = 0; index < laidOut.length; index += 1) {
      const entry = laidOut[index];
      if (!entry) continue;
      if (entry.kind === "run") {
        for (const member of entry.entries)
          if (entryMatches(member, needle)) hits.push(hit(member, rowIndex, needle, entry.id));
        continue;
      }
      if (entry.kind === "intent") {
        // TurnBlock groups consecutive intent entries under the first one's id.
        const group = intentGroupId(entry.id);
        let cursor = index;
        for (;;) {
          const member = laidOut[cursor];
          if (member?.kind !== "intent") break;
          if (entryMatches(member, needle)) hits.push(hit(member, rowIndex, needle, group));
          cursor += 1;
        }
        index = cursor - 1;
        continue;
      }
      if (entryMatches(entry, needle)) hits.push(hit(entry, rowIndex, needle));
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
