// Which entries hold the words being looked for: a message's words, a
// thought's summaries, an action row's rationale, a steer's visible text, a
// memory refresh's index and a tool call's own lines (other system notices are
// not searched by this slice). A hit is one projected entry, and its words are
// on screen once its row mounts: a message never folds, and whatever hides the
// matching words - a thought's or an action group's disclosure, a steer's or a
// refresh's divider, a tool run, a call's summary line or body - Find opens
// before it lands. Text a tool body folds or clips away is shown by the row's
// own excerpt (toolCallHits.ts), never counted and left unseen.
import {
  entryDisplayKey,
  findNeedle,
  findReadable,
  hidesThinkingTraces,
  type ItemModel,
  isLiveCurrentReasoning,
  isParentSteer,
  isSuppressedSteeringKind,
  MEMORY_CONTEXT_EVENT_KIND,
  memoryContextEmptyText,
  type ProjectedEntry,
  parseMemoryContext,
  projectThread,
  steeringNotificationFragments,
  stripSystemReminder,
  type ThreadModel,
  type TranscriptDisplayConfigV1,
  type TurnModel,
} from "@evener/appwire-client";
import { mermaidRenderVersion } from "../../../widgets/mermaid/renderState";
import { displayedIntentOf } from "../transcript/ToolCallItem";
import { type TranscriptBodyRow, transcriptRowsForProjection } from "../transcript/TranscriptBody";
import { toolRendererFor } from "../transcript/toolRenderers";
import { foldTurnEntries, intentGroupId, intentRunAt } from "../transcript/toolRuns";
import { markdownAsRead, messageAsShown, reasoningSummariesAsRead } from "./messageText";
import { holdsNeedle, toolCallTexts } from "./toolCallHits";

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
  /** The disclosures that stand between the reader and the matching words,
   * outermost first: the fold hiding the entry (a tool run or an action
   * group), then the call's own summary line or body. A message has none. */
  readonly opens?: readonly string[];
  /** The session-scoped dividers that hide the matching words: a steer's and a
   * memory refresh's body folds by the session ref plus its own id
   * (SteeringDivider's key), not by the view's disclosure scope. */
  readonly opensSession?: readonly string[];
}

/** What the view shows, which decides what Find counts: one that hides
 * thinking traces has no thought summary to land on. */
export interface FindView {
  readonly config?: TranscriptDisplayConfigV1;
  /** The diagram render state the text is read at. */
  readonly version?: number;
  /** The session's working directory, which a tool call's summary reads at. */
  readonly cwd?: string;
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

// A message's searchable text, rendered once. Settled items keep their
// identity across re-projection, so while a reply streams only the growing
// item is rendered again.
const haystacks = new WeakMap<ItemModel, { version: number; text: string | null }>();
// A thought's summaries are immutable per item, so they are rendered once per
// reading: a live stream closes an unclosed marker at its tail, a settled one
// keeps it literal. A diagram inside the Markdown can fail after the first
// read and show its source, so the render version is part of the key.
const thoughtHaystacks = new WeakMap<ItemModel, { version: number; live: boolean; text: readonly string[] }>();

function readable(text: string): string {
  return findReadable(text).toLowerCase();
}

function haystack(item: ItemModel, version: number): string | null {
  // A diagram renders asynchronously, so one can fail after this message was
  // first read; the version changes then and the text is read again.
  const cached = haystacks.get(item);
  if (cached === undefined || cached.version !== version) {
    const shown = messageAsShown(item);
    haystacks.set(item, { version, text: shown });
    return shown;
  }
  return cached.text;
}

function thoughtHaystack(item: ItemModel, version: number, live: boolean): readonly string[] {
  const cached = thoughtHaystacks.get(item);
  if (cached !== undefined && cached.version === version && cached.live === live) return cached.text;
  const text = reasoningSummariesAsRead(item, live);
  thoughtHaystacks.set(item, { version, live, text });
  return text;
}

// A tool call's own lines, as its row paints them: the intent line, the
// descriptor's summary, and what only the body holds. The intent is the same
// rule ToolCallItem paints (a delegate's clipped brief included).
function toolCall(entry: Extract<ProjectedEntry, { kind: "item" }>, cwd: string | undefined) {
  return toolCallTexts(entry.item, { intent: displayedIntentOf(entry.item), cwd });
}

/** One span of a steer's or a memory refresh's row: the exact words it puts on
 * screen, and the session-scoped divider that folds them (absent for a steer
 * the human typed, which is a prompt bubble). */
interface ShownSpan {
  readonly text: string;
  readonly divider?: string;
}

// What a steer's or a memory refresh's row shows, read through the same
// helpers its renderer routes with (SteeringItem's source/kind order and
// MemoryContextItem's body), so Find, the palette and the row cannot disagree.
// Null for any other entry.
function dividerSpans(item: ItemModel): readonly ShownSpan[] | null {
  if (item.type === "steering") {
    // The human-note kind routes before source: it renders the labeled divider.
    if (item.steeringKind === "human-note") return item.text ? [{ text: item.text, divider: item.id }] : [];
    // A steer the human typed is a prompt bubble: plain visible text.
    if (item.source === "user") return item.text ? [{ text: item.text }] : [];
    if (!item.text || isSuppressedSteeringKind(item.steeringKind)) return [];
    // A parent's message is conversation: quoted notification tags in it are
    // text, never cards. Any other steer renders one divider per interstitial
    // text span, and its cards render no words of their own.
    const parent = isParentSteer(item.source);
    const fragments = parent ? null : steeringNotificationFragments(item.text);
    if (fragments === null) {
      return [{ text: parent ? item.text : stripSystemReminder(item.text), divider: item.id }];
    }
    return fragments.flatMap((fragment, index) =>
      fragment.kind === "notification" ? [] : [{ text: fragment.text, divider: `${item.id}:${index}` }],
    );
  }
  if (item.type === "systemMessage" && item.eventKind === MEMORY_CONTEXT_EVENT_KIND) {
    const observation = parseMemoryContext(item.raw);
    // A raw that did not decode shows its complete original Text; a decoded one
    // shows its index as Markdown, or the state's own note for an empty one.
    // The meta line and the folded Source are chrome the row keeps out of the
    // body, so neither is text a match could land on.
    if (observation === undefined) return item.text === "" ? [] : [{ text: item.text, divider: item.id }];
    const text =
      observation.content.trim() === ""
        ? memoryContextEmptyText(observation.state)
        : markdownAsRead(observation.content);
    return text === "" ? [] : [{ text, divider: item.id }];
  }
  return null;
}

/** Whether a steer's, a memory refresh's or a tool call's row decides what its
 * words are, so its raw `text` is never one of them. Other rows show their own
 * text. */
export function rowShowsOwnText(item: ItemModel): boolean {
  return (
    item.type === "steering" ||
    item.type === "commandExecution" ||
    (item.type === "systemMessage" && item.eventKind === MEMORY_CONTEXT_EVENT_KIND)
  );
}

// What an action row actually says: the same intent line ToolCallItem paints
// (a delegate's clipped brief included), and nothing where the row renders
// none. The projector's neutral rationale is a fallback for rows whose
// descriptor has no summary of its own, and a suppressed descriptor renders no
// row at all, so neither is text a match could land on.
function intentText(entry: Extract<ProjectedEntry, { kind: "intent" }>): string | null {
  const descriptor = toolRendererFor(entry.item.toolName ?? "");
  if (descriptor.suppress?.(entry.item)) return null;
  return displayedIntentOf(entry.item) ?? null;
}

/** Where a tool call holds the needle, and what hides that text - or null
 * where it holds none. A summary-line match opens that line; a body match
 * opens the body, and the row shows an excerpt of it where the body's own fold
 * or clip keeps the words off screen. */
function toolCallOpens(
  entry: Extract<ProjectedEntry, { kind: "item" }>,
  needle: string,
  view: FindView,
): readonly string[] | null {
  const texts = toolCall(entry, view.cwd);
  if (texts === null) return null;
  if (holdsNeedle(texts.intent, needle)) return [];
  if (holdsNeedle(texts.summary, needle)) return [`summary:${entry.item.id}`];
  if (texts.body.some((text) => holdsNeedle(text, needle))) return [entry.item.id];
  return null;
}

/** The text an entry shows that Find searches, as written: empty where the view
 * shows none of it. Find and the palette read every entry through this one
 * rule, so neither can offer text the other would not land on. `turn` is
 * absent for a row that only groups action entries, which carry no thought. */
function shownTexts(
  entry: ProjectedEntry,
  turn: TurnModel | undefined,
  config: TranscriptDisplayConfigV1 | undefined,
  version: number,
  cwd: string | undefined,
): readonly string[] {
  if (entry.kind === "intent") {
    const text = intentText(entry);
    return text === null ? [] : [text];
  }
  const spans = dividerSpans(entry.item);
  if (spans !== null) return spans.map((span) => span.text);
  if (entry.kind === "item" && entry.item.type === "commandExecution") {
    const texts = toolCall(entry, cwd);
    return texts === null ? [] : [texts.intent, texts.summary, ...texts.body].filter((text) => text !== "");
  }
  if (entry.item.type === "reasoning") {
    // A chat view shows only the placeholder before a live thought: its
    // summaries are not on screen, so they are not a match there.
    if (entry.kind === "thinking" && config !== undefined && hidesThinkingTraces(config.content)) return [];
    const texts: string[] = [];
    // A redacted thought shows its neutral label verbatim; what it can unfold
    // is its Markdown summaries, never its raw text.
    if (entry.kind === "critical" && entry.redacted) texts.push(entry.summary);
    // A thought reads live only while it is the turn's current tail, as
    // ThinkBlock decides it: the wire never completes a reasoning item, so a
    // later item in the same in-progress turn settles the thought.
    const live = turn !== undefined && isLiveCurrentReasoning(entry.item, turn);
    texts.push(...thoughtHaystack(entry.item, version, live));
    return texts;
  }
  const text = entry.kind === "item" && entry.isMessage ? haystack(entry.item, version) : null;
  return text === null ? [] : [text];
}

/** What `config` shows, per source item: the texts Find searches there. The
 * palette lists only these, so every row it offers is one Find can land on. */
export function projectedSearchTexts(
  model: ThreadModel,
  config: TranscriptDisplayConfigV1,
): Map<string, readonly string[]> {
  const version = mermaidRenderVersion();
  const shown = new Map<string, readonly string[]>();
  for (const row of transcriptRowsForProjection(projectThread(model, config))) {
    const turn = row.kind === "intentGroup" ? undefined : row.turn.source;
    for (const entry of row.kind === "intentGroup" ? row.entries : row.turn.entries) {
      const texts = shownTexts(entry, turn, config, version, model.cwd);
      if (texts.length > 0) shown.set(entry.item.id, texts);
    }
  }
  return shown;
}

/** The entries holding `query`, oldest first. `view.version` is the diagram
 * render state the text is read at: a caller that must react when a diagram
 * fails passes the version it subscribed to, so the search is redone then. */
export function findHits(rows: readonly TranscriptBodyRow[], query: string, view: FindView = {}): FindHit[] {
  const needle = findNeedle(query);
  if (needle === "") return [];
  const version = view.version ?? mermaidRenderVersion();
  const hits: FindHit[] = [];
  const push = (
    entry: ProjectedEntry,
    rowIndex: number,
    opens: readonly string[],
    opensSession: readonly string[] = [],
  ) => {
    const { id, callId, roundId } = entry.item;
    const itemId = entry.kind === "intent" ? entry.sourceItemId : id;
    hits.push({
      key: entryDisplayKey(entry),
      itemId,
      rowIndex,
      ...(callId ? { callId } : {}),
      ...(roundId ? { roundId } : {}),
      ...(opens.length > 0 ? { opens } : {}),
      ...(opensSession.length > 0 ? { opensSession } : {}),
    });
  };
  // A tool call's own lines are classified by where they sit (its intent line,
  // its summary line, its body); every other entry is a single test over the
  // texts the view shows.
  const add = (entry: ProjectedEntry, rowIndex: number, turn: TurnModel | undefined, outer: readonly string[]) => {
    if (entry.kind === "item" && entry.item.type === "commandExecution") {
      const own = toolCallOpens(entry, needle, view);
      if (own !== null) push(entry, rowIndex, [...outer, ...own]);
      return;
    }
    const spans = dividerSpans(entry.item);
    const texts =
      spans === null ? shownTexts(entry, turn, view.config, version, view.cwd) : spans.map((span) => span.text);
    if (!texts.some((text) => readable(text).includes(needle))) return;
    const opensSession = spans?.flatMap((span) =>
      span.divider !== undefined && readable(span.text).includes(needle) ? [span.divider] : [],
    );
    push(entry, rowIndex, outer, opensSession);
  };
  rows.forEach((row, rowIndex) => {
    if (row.kind === "intentGroup") {
      for (const entry of row.entries) add(entry, rowIndex, undefined, [row.id]);
      return;
    }
    // The turn's own entries, laid out the one way the transcript renders them
    // (toolRuns.ts): a folded run is one row, so a hit inside it knows which
    // disclosure opens it.
    const laidOut = foldTurnEntries(row.turn);
    for (let index = 0; index < laidOut.length; index += 1) {
      const entry = laidOut[index];
      if (entry === undefined) continue;
      if (entry.kind === "run") {
        for (const member of entry.entries) add(member, rowIndex, row.turn.source, [entry.id]);
        continue;
      }
      if (entry.kind === "intent") {
        // TurnBlock groups consecutive action rows under the first one's id.
        const run = intentRunAt(laidOut, index);
        const group = intentGroupId(run.entries[0]?.id);
        for (const member of run.entries) add(member, rowIndex, row.turn.source, [group]);
        index = run.end - 1;
        continue;
      }
      add(entry, rowIndex, row.turn.source, entry.item.type === "reasoning" ? [entryDisplayKey(entry)] : []);
    }
  });
  return hits;
}

/** The source items of `model` that hold `query` in an entry `config` shows:
 * the items a caller outside the transcript can send Find to. */
export function findableItemIds(model: ThreadModel, config: TranscriptDisplayConfigV1, query: string): Set<string> {
  const rows = transcriptRowsForProjection(projectThread(model, config));
  return new Set(findHits(rows, query, { config, cwd: model.cwd }).map((found) => found.itemId));
}
