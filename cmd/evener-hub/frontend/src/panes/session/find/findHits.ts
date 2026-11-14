// Which transcript entries hold the words being looked for. A hit is one
// projected entry - a message, a thought, a tool call - so the count agrees
// with what the reader steps through, and an entry the current verbosity
// leaves out is not a hit.
import {
  entryDisplayKey,
  findNeedle,
  findReadable,
  hidesThinkingTraces,
  type ItemModel,
  isParentSteer,
  itemFindTexts,
  joinedReasoningParagraphs,
  MEMORY_CONTEXT_EVENT_KIND,
  type ProjectedEntry,
  type ProjectedTurn,
  projectThread,
  steeringNotificationFragments,
  type ThreadModel,
  type TranscriptDisplayConfigV1,
  type TranscriptMetadataVisibility,
} from "@evener/appwire-client";
import { notificationDisclosureId } from "../transcript/messages/NotificationCard";
import { rendersSteer } from "../transcript/messages/SteeringItem";
import { renderSystemRunFor } from "../transcript/messages/SystemNoticeItem";
import { shouldGroup } from "../transcript/messages/systemGrouping";
import { type TranscriptBodyRow, transcriptRowsForProjection } from "../transcript/TranscriptBody";
import { toolRendererFor } from "../transcript/toolRenderers";
import { foldTurnEntries, intentGroupId } from "../transcript/toolRuns";
import { markdownAsRead } from "./markdownText";

/** The transcript view the hits are for. What a renderer shows depends on
 * it, so what Find counts does too. Left out, nothing is hidden by it. */
export interface FindContext {
  readonly sessionRef?: string;
  readonly config?: TranscriptDisplayConfigV1;
  readonly metadata?: Pick<TranscriptMetadataVisibility, "hookExits">;
}

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
  /** The display key of the entry this one renders under, when it has no
   * place of its own: a notice folded into a group sits under the group's
   * first member. */
  readonly anchor?: string;
}

// An item's searchable text, normalised once. Settled items keep their
// identity across re-projection, so while a reply streams only the growing
// item is read again; every other entry is a substring test.
const itemHaystacks = new WeakMap<ItemModel, readonly string[]>();

function haystack(text: string): string {
  return findReadable(text).toLowerCase();
}

// A reply and a thought render as Markdown, so they are read as the page
// shows them; a thought shows only its summaries. Everything else renders
// its text as written.
function shownTexts(item: ItemModel): string[] {
  if (item.type === "reasoning") return joinedReasoningParagraphs(item.reasoningSummaries).map(markdownAsRead);
  if (item.type === "agentMessage") return itemFindTexts(item).map(markdownAsRead);
  return itemFindTexts(item);
}

function itemHaystack(item: ItemModel): readonly string[] {
  let cached = itemHaystacks.get(item);
  if (cached === undefined) {
    cached = shownTexts(item).map(haystack);
    itemHaystacks.set(item, cached);
  }
  return cached;
}

function entryMatches(entry: ProjectedEntry, needle: string, view: FindContext): boolean {
  const item = entry.item;
  // An entry whose renderer shows nothing has nothing to land on: a call
  // whose descriptor suppresses its row, a steer the transcript leaves out,
  // and a thought where thinking traces are hidden.
  if (item.type === "commandExecution" && toolRendererFor(item.toolName ?? "").suppress?.(item)) return false;
  if (item.type === "steering" && !rendersSteer(item)) return false;
  if (entry.kind === "thinking" && view.config !== undefined && hidesThinkingTraces(view.config.content)) {
    return false;
  }
  if (entry.kind === "intent" && haystack(entry.rationale).includes(needle)) return true;
  if (entry.kind === "critical") {
    if (haystack(entry.summary).includes(needle)) return true;
    // A redacted thought never renders its raw text; what it can unfold is
    // its summaries.
    if (entry.redacted) {
      return (item.reasoningSummaries ?? []).some((summary) => haystack(summary.join("")).includes(needle));
    }
  }
  return itemHaystack(item).some((text) => text.includes(needle));
}

// Where the words of a steer or a memory refresh sit. Its text is behind a
// divider keyed by session; a steer delivering notifications is cut into
// pieces, each stretch of text behind a divider of its own and each
// notification in a card with a disclosure in the view.
function behindDividers(item: ItemModel, needle: string, view: FindContext): { session: string[]; inView: string[] } {
  if (item.type !== "steering") {
    return { session: item.eventKind === MEMORY_CONTEXT_EVENT_KIND ? [item.id] : [], inView: [] };
  }
  const pieces = isParentSteer(item.source) ? null : steeringNotificationFragments(item.text ?? "");
  if (pieces === null) return { session: [item.id], inView: [] };
  const session: string[] = [];
  const inView: string[] = [];
  pieces.forEach((piece, index) => {
    const delivery = `${item.id}:${index}`;
    if (piece.kind === "text") {
      if (haystack(piece.text).includes(needle)) session.push(delivery);
    } else if (haystack(piece.notification.rawText).includes(needle)) {
      inView.push(notificationDisclosureId(piece.notification, view.sessionRef, delivery));
    }
  });
  return { session, inView };
}

// The first member of the group a system notice is folded into, when it is
// not that member itself.
function groupLeader(item: ItemModel, turn: ProjectedTurn | undefined, view: FindContext): ItemModel | undefined {
  if (item.type !== "systemMessage" || turn === undefined) return undefined;
  const run = renderSystemRunFor(turn.source.items, item.id, view.metadata ?? { hookExits: "none" });
  if (run === undefined || run.isFirst || !shouldGroup(run)) return undefined;
  return run.items[0];
}

function hit(
  entry: ProjectedEntry,
  rowIndex: number,
  needle: string,
  view: FindContext,
  turn?: ProjectedTurn,
  fold?: string,
): FindHit | null {
  const key = entryDisplayKey(entry);
  const itemId = entry.kind === "intent" ? entry.sourceItemId : entry.item.id;
  const opens = fold === undefined ? [] : [fold];
  let anchor: string | undefined;
  const leader = groupLeader(entry.item, turn, view);
  if (leader !== undefined) {
    const under = turn?.entries.find((other) => other.kind === "item" && other.item === leader);
    // A group whose first member the view leaves out is never rendered.
    if (under === undefined) return null;
    anchor = entryDisplayKey(under);
    opens.push(leader.id);
  }
  const dividers = behindDividers(entry.item, needle, view);
  // A tool call keeps what it was asked behind its summary disclosure and
  // what came back behind its body; a thought's body goes by its display key.
  if (!(entry.kind === "item" && entry.isMessage)) opens.push(...new Set([itemId, key, `summary:${itemId}`]));
  opens.push(...dividers.inView);
  return { key, itemId, rowIndex, opens, sessionOpens: dividers.session, anchor };
}

/** The entries holding `query`, oldest first. Folds follow the one fold the
 * transcript renders from (toolRuns.ts), so a hit inside a closed run knows
 * which disclosure opens it. */
export function findHits(rows: readonly TranscriptBodyRow[], query: string, view: FindContext = {}): FindHit[] {
  const needle = findNeedle(query);
  if (needle === "") return [];
  const hits: FindHit[] = [];
  const add = (entry: ProjectedEntry, rowIndex: number, turn?: ProjectedTurn, fold?: string) => {
    if (!entryMatches(entry, needle, view)) return;
    const found = hit(entry, rowIndex, needle, view, turn, fold);
    if (found !== null) hits.push(found);
  };
  rows.forEach((row, rowIndex) => {
    if (row.kind === "intentGroup") {
      for (const entry of row.entries) add(entry, rowIndex, undefined, row.id);
      return;
    }
    const laidOut = foldTurnEntries(row.turn);
    for (let index = 0; index < laidOut.length; index += 1) {
      const entry = laidOut[index];
      if (!entry) continue;
      if (entry.kind === "run") {
        for (const member of entry.entries) add(member, rowIndex, row.turn, entry.id);
        continue;
      }
      if (entry.kind === "intent") {
        // TurnBlock groups consecutive intent entries under the first one's id.
        const group = intentGroupId(entry.id);
        let cursor = index;
        for (;;) {
          const member = laidOut[cursor];
          if (member?.kind !== "intent") break;
          add(member, rowIndex, row.turn, group);
          cursor += 1;
        }
        index = cursor - 1;
        continue;
      }
      add(entry, rowIndex, row.turn);
    }
  });
  return hits;
}

/** The source items of `model` that hold `query` in an entry `config` shows:
 * the items a caller outside the transcript can send Find to. */
export function findableItemIds(model: ThreadModel, config: TranscriptDisplayConfigV1, query: string): Set<string> {
  const projection = projectThread(model, config);
  const rows = transcriptRowsForProjection(projection);
  const view = { sessionRef: model.ref, config, metadata: projection.metadata };
  return new Set(findHits(rows, query, view).map((found) => found.itemId));
}
