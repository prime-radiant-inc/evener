// Which transcript entries hold the words being looked for. Find searches
// what the reader can read: a message's words, a thought's summaries and an
// action row's rationale. A hit is one projected entry, so the count agrees
// with what the reader steps through, and every hit has its words on screen
// once its row mounts: a message never folds, and a thought's or an action's
// words are behind a disclosure Find opens before it lands. Tool calls,
// steers and system notices are not searched.
import {
  entryDisplayKey,
  findNeedle,
  findReadable,
  hidesThinkingTraces,
  type ItemModel,
  type ProjectedEntry,
  projectThread,
  type ThreadModel,
  type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import { mermaidRenderVersion } from "../../../widgets/mermaid/renderState";
import { type TranscriptBodyRow, transcriptRowsForProjection } from "../transcript/TranscriptBody";
import { foldTurnEntries, intentGroupId, intentRunAt } from "../transcript/toolRuns";
import { markdownAsRead, messageAsShown, reasoningSummariesAsRead } from "./messageText";

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
  /** Disclosures that hide the matching words, outermost first. Empty for a
   * message (omitted there); a thought names its own disclosure and an action
   * row its group. */
  readonly opens?: readonly string[];
}

/** What the transcript view shows, which decides what Find counts: a view
 * that hides thinking traces shows no thought summary to land on. */
export interface FindView {
  readonly config?: TranscriptDisplayConfigV1;
  /** The diagram render state the text is read at. */
  readonly version?: number;
}

// A reply still arriving is an overlay item until it is recorded: a streamed
// reply under "stream:<id>:<type>", one being said through a call under
// "preview:<callId>" (the wire's overlay slot keys).
const STREAM = "stream:";
const PREVIEW = "preview:";

/** Whether an item id names an overlay slot: live state no refresh can bring
 * back, because it was never recorded. */
export function isOverlay(itemId: string): boolean {
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
const haystacks = new WeakMap<ItemModel, { version: number; text: string | null }>();
// A thought's summaries are immutable per item, so they are read once.
const thoughtHaystacks = new WeakMap<ItemModel, readonly string[]>();

function readable(text: string): string {
  return findReadable(text).toLowerCase();
}

function haystack(item: ItemModel, version: number): string | null {
  // A diagram renders asynchronously, so one can fail after this message was
  // first read; the version changes then and the text is read again.
  const cached = haystacks.get(item);
  if (cached === undefined || cached.version !== version) {
    const shown = messageAsShown(item);
    const text = shown === null ? null : readable(shown);
    haystacks.set(item, { version, text });
    return text;
  }
  return cached.text;
}

function thoughtHaystack(item: ItemModel): readonly string[] {
  let cached = thoughtHaystacks.get(item);
  if (cached === undefined) {
    cached = reasoningSummariesAsRead(item).map(readable);
    thoughtHaystacks.set(item, cached);
  }
  return cached;
}

// A redacted thought's label is read once per item, like its summaries.
const redactedLabels = new WeakMap<ItemModel, string>();

function redactedLabel(entry: Extract<ProjectedEntry, { kind: "critical" }>): string {
  let cached = redactedLabels.get(entry.item);
  if (cached === undefined) {
    cached = readable(markdownAsRead(entry.summary));
    redactedLabels.set(entry.item, cached);
  }
  return cached;
}

function matches(entry: ProjectedEntry, needle: string, view: FindView, version: number): boolean {
  if (entry.kind === "intent") return readable(entry.rationale).includes(needle);
  if (entry.item.type === "reasoning") {
    // A chat view shows only the placeholder before a live thought: its
    // summaries are not on screen, so they are not a match there.
    if (entry.kind === "thinking" && view.config !== undefined && hidesThinkingTraces(view.config.content))
      return false;
    // A redacted thought shows its neutral label; what it can unfold is its
    // summaries, never its raw text.
    if (entry.kind === "critical" && entry.redacted && redactedLabel(entry).includes(needle)) return true;
    return thoughtHaystack(entry.item).some((text) => text.includes(needle));
  }
  return entry.kind === "item" && entry.isMessage && (haystack(entry.item, version)?.includes(needle) ?? false);
}

/** The entries holding `query`, oldest first. `view.version` is the diagram
 * render state the text is read at: a caller that must react when a diagram
 * fails passes the version it subscribed to, so the search is redone then. */
export function findHits(rows: readonly TranscriptBodyRow[], query: string, view: FindView = {}): FindHit[] {
  const needle = findNeedle(query);
  if (needle === "") return [];
  const version = view.version ?? mermaidRenderVersion();
  const hits: FindHit[] = [];
  const add = (entry: ProjectedEntry, rowIndex: number, opens?: readonly string[]) => {
    if (!matches(entry, needle, view, version)) return;
    const { id, callId, roundId } = entry.item;
    const itemId = entry.kind === "intent" ? entry.sourceItemId : id;
    hits.push({
      key: entryDisplayKey(entry),
      itemId,
      rowIndex,
      ...(callId ? { callId } : {}),
      ...(roundId ? { roundId } : {}),
      ...(opens ? { opens } : {}),
    });
  };
  rows.forEach((row, rowIndex) => {
    if (row.kind === "intentGroup") {
      for (const entry of row.entries) add(entry, rowIndex, [row.id]);
      return;
    }
    const laidOut = foldTurnEntries(row.turn);
    for (let index = 0; index < laidOut.length; index += 1) {
      const entry = laidOut[index];
      if (entry === undefined) continue;
      if (entry.kind === "intent") {
        // TurnBlock groups consecutive action rows under the first one's id.
        const run = intentRunAt(laidOut, index);
        const group = intentGroupId(run.entries[0]?.id);
        for (const member of run.entries) add(member, rowIndex, [group]);
        index = run.end - 1;
        continue;
      }
      if (entry.kind === "run") continue;
      add(entry, rowIndex, entry.item.type === "reasoning" ? [entryDisplayKey(entry)] : undefined);
    }
  });
  return hits;
}

/** The source items of `model` that hold `query` in an entry `config` shows:
 * the items a caller outside the transcript can send Find to. */
export function findableItemIds(model: ThreadModel, config: TranscriptDisplayConfigV1, query: string): Set<string> {
  const rows = transcriptRowsForProjection(projectThread(model, config));
  return new Set(findHits(rows, query, { config }).map((found) => found.itemId));
}
