// Versioned history and the live overlay: history state, versioned item and
// turn merges, overlay display derivation, and read dispositions. Imports
// `turnHistoryMerge` and `transcriptItems`; the notification folds that drive
// these live in `reducer.ts`.

import { compareBootGeneration } from "./bootGeneration";
import type { HistoryState, ItemModel, ThreadModel, TurnModel } from "./model";
import { SYSTEM_PRELUDE_TURN_ID } from "./model";
import {
  copyItemTextPresence,
  imageSessionRouteForSession,
  itemIsIdentityOnly,
  itemTextPresence,
  markItemIdentityOnly,
  markItemTextOmitted,
  outputImagesToItemImages,
  threadFields,
  wireImageSessionId,
  wireItemToModel,
  wireToTurnModel,
  wireToTurnScalars,
} from "./transcriptItems";
import { mergeOlderItemPageWithFolds, mergeToolCallsByCallId, mergeTurnHistory } from "./turnHistoryMerge";
import type {
  OverlayItem,
  Thread,
  ThreadItem,
  ThreadItemPosition,
  ThreadReadResponse,
  ThreadTurnsListResponse,
  Turn,
} from "./types.gen";

// mergeOlderItemPage folds one thread/turns/list backfill page into the model.
// A v6 page into a model holding versioned history merges by version under
// the snapshot rules below; anything else takes the unversioned merge.
export function mergeOlderItemPage(model: ThreadModel, resp: ThreadTurnsListResponse): ThreadModel {
  if (model.history && resp.snapshot) return mergeVersionedPage(model, model.history, resp, true);
  return mergeOlderItemPageWithFolds(model, resp).model;
}

// ---------------------------------------------------------------------------
// Versioned history and the live overlay (evener-appwire-v6; spec
// docs/superpowers/specs/2026-09-25-transcript-read-model-design.md: "Recorded
// length", "Reads", "Live history notifications", "The live overlay").
//
// model.history.turns holds the recorded turns; model.overlay holds the live
// overlay; model.turns is derived from both (withDisplay). Recorded items
// merge by version and are identified by transcriptKey; a merge never removes
// one. Only a replacement does: another boot generation, a newer epoch or
// incarnation, a read after invalidation, or an authoritative daemonless read
// for the position range it returned.
// ---------------------------------------------------------------------------

export const EMPTY_HISTORY: HistoryState = {
  bootGeneration: "",
  epoch: 0,
  length: 0,
  appliedGeneration: 0,
  issuedGeneration: 0,
  deferredPages: [],
  turns: [],
};

type ReadIdentity = Pick<HistoryState, "bootGeneration" | "epoch" | "incarnation" | "length">;

export function readIdentity(resp: {
  bootGeneration?: string;
  epoch?: number;
  snapshot?: { incarnation: string; length: number };
}): ReadIdentity {
  return {
    bootGeneration: resp.bootGeneration ?? "",
    epoch: resp.epoch ?? 0,
    incarnation: resp.snapshot?.incarnation,
    length: resp.snapshot?.length ?? 0,
  };
}

// The running turn a read names. Only evener.activeTurnId counts: a turn the
// snapshot merely records as inProgress (a crash left it open) is not running.
export function runningTurn(thread: Thread): Pick<ThreadModel, "runningTurnId"> {
  return thread.evener.activeTurnId ? { runningTurnId: thread.evener.activeTurnId } : {};
}

export function overlayRecord(items: readonly OverlayItem[] | undefined): Record<string, OverlayItem> {
  const record: Record<string, OverlayItem> = {};
  for (const item of items ?? []) record[item.key] = item;
  return record;
}

function itemIdentity(item: Pick<ItemModel, "id" | "transcriptKey">): string {
  return item.transcriptKey ?? item.id;
}

// Payload fields whose ABSENCE on a wire item means "this entry said nothing
// new here", not "cleared" — appwire's own nil/non-nil-empty/non-empty rule
// (imagesToItemImagesForSession, outputImagesToItemImages): an omitted field
// (undefined) leaves the held value standing, an explicit empty list or
// string is authoritative like any other value. mergeReplacedItem applies it
// to a live history/updated frame, since mergeHistory otherwise replaces an
// item wholesale on every higher-version re-issue; the phone store's
// rehydrate reconciliation reads the same list for the fresh-read merge.
export const ITEM_OMISSION_TOLERANT_FIELDS = [
  "images",
  "outputImages",
  "output",
  "error",
  "raw",
  "prevalOnly",
  "exitCode",
  "argumentsJSON",
  "description",
  "toolName",
  "callId",
  "eventKind",
  "steeringKind",
  "source",
  "actor",
] as const;

// A version-superseding item replacement: incoming is authoritative for
// every field it explicitly carries, but an omitted (undefined)
// omission-tolerant field, or an omitted text field (itemTextPresence),
// falls back to the held item's value instead of clearing it.
function mergeReplacedItem(held: ItemModel | undefined, incoming: ItemModel): ItemModel {
  if (held === undefined) return incoming;
  let merged = incoming;
  for (const field of ITEM_OMISSION_TOLERANT_FIELDS) {
    const heldValue = (held as unknown as Record<string, unknown>)[field];
    if (heldValue === undefined || (incoming as unknown as Record<string, unknown>)[field] !== undefined) continue;
    if (merged === incoming) merged = { ...incoming };
    (merged as unknown as Record<string, unknown>)[field] = heldValue;
  }
  if (itemTextPresence(incoming) === "omitted" && itemTextPresence(held) === "provided") {
    merged = copyItemTextPresence(
      incoming,
      merged === incoming ? { ...incoming, text: held.text } : { ...merged, text: held.text },
    );
  }
  return merged;
}

/** Applies the recorded history version precedence to one keyed entry row. */
export function mergeTranscriptItemVersioned(
  held: ItemModel | undefined,
  heldVersion: number | undefined,
  incoming: ItemModel,
  incomingVersion: number,
): ItemModel {
  if (
    held !== undefined &&
    !supersedes(heldVersion, incomingVersion) &&
    !(heldVersion === incomingVersion && itemIsIdentityOnly(held) && !itemIsIdentityOnly(incoming))
  ) {
    return held;
  }
  return mergeReplacedItem(held, incoming);
}

/** Applies the recorded turn-version precedence while keeping hydrated items owned by history. */
export function mergeTranscriptTurnVersioned(held: TurnModel | undefined, incoming: TurnModel): TurnModel {
  if (held !== undefined && !supersedes(held.version, incoming.version)) return held;
  return { ...incoming, items: held?.items ?? [] };
}

/** Merges genuine local reader rows into versioned history using the reducer's normal fold rules. */
export function mergeTranscriptReaderWindow<M extends ThreadModel>(
  model: M,
  turns: readonly TurnModel[],
): ThreadModel & ModelExtras<M> {
  if (!model.history || turns.length === 0) return publicModel<M>(model);
  const fragment: HistoryFragment = {
    turns: turns.map(({ items: _items, ...turn }) => ({ ...turn, items: [] })),
    items: turns.flatMap((turn) => turn.items),
  };
  const history = { ...model.history, turns: mergeHistory(model.history.turns, fragment) };
  return publicModel<M>(withDisplay(model, history, model.overlay ?? {}));
}

// Orders by (entry, item, sub). An item with no position sorts after every
// positioned one. Exported for transcriptProjector.ts, which ranks items by
// real document position rather than turn-then-item array order (turns can
// interleave - see noticeHistoryTurn above).
export function comparePositions(left: ThreadItemPosition | undefined, right: ThreadItemPosition | undefined): number {
  if (!left || !right) return left ? -1 : right ? 1 : 0;
  return left.entry - right.entry || left.item - right.item || (left.sub ?? 0) - (right.sub ?? 0);
}

// Where a turn sits: its first item's position, or, for a turn that holds no
// item yet, the entry its version names.
function turnPosition(turn: TurnModel): ThreadItemPosition | undefined {
  return turn.items[0]?.position ?? (turn.version === undefined ? undefined : { entry: turn.version, item: 0 });
}

// The number of items that sort before position: the index a new item at
// position is inserted at, and one past the last item preceding it. An
// upper-bound search (<=, not <): a new arrival whose position TIES an
// already-held item's — including the common case of two items that both
// carry no position at all, where comparePositions reads every such pair as
// equal — inserts after every item already at that position, preserving
// arrival order, rather than jumping to the front of the tied run.
function itemsBefore(items: readonly ItemModel[], position: ThreadItemPosition | undefined): number {
  let low = 0;
  let high = items.length;
  while (low < high) {
    const mid = (low + high) >>> 1;
    if (comparePositions(items[mid]?.position, position) <= 0) low = mid + 1;
    else high = mid;
  }
  return low;
}

// The higher version wins; a held item or turn without a version (an older
// read's) always yields.
function supersedes(held: number | undefined, incoming: number | undefined): boolean {
  return held === undefined || (incoming !== undefined && incoming > held);
}

interface HistoryFragment {
  turns: TurnModel[]; // turn scalars, items empty
  items: ItemModel[];
}

// Flattens wire turns into turn scalars and the items they carry. An item
// without its own turnId belongs to the turn that carried it.
export function splitWireTurns(turns: readonly Turn[], imageSessionRoute: string | undefined): HistoryFragment {
  const fragment: HistoryFragment = { turns: [], items: [] };
  for (const turn of turns) {
    fragment.turns.push({ ...wireToTurnScalars(turn), items: [] });
    for (const wire of turn.items ?? []) {
      const item = wireItemToModel(wire, imageSessionRoute);
      if (item.turnId === "") item.turnId = turn.id;
      fragment.items.push(item);
    }
  }
  return fragment;
}

export function wireFragment(
  turns: readonly Turn[] | undefined,
  items: readonly ThreadItem[] | undefined,
  imageSessionRoute: string | undefined,
): HistoryFragment {
  return {
    turns: (turns ?? []).map((turn) => ({ ...wireToTurnScalars(turn), items: [] })),
    items: (items ?? []).map((item) => wireItemToModel(item, imageSessionRoute)),
  };
}

// Merges a fragment into recorded turns by version. A turn or item history
// does not hold is added, unless heldOnly (a daemonless read's `changes`,
// which refresh held items and never add pages the client did not load).
// Returns turns itself when nothing changed, and keeps every untouched turn
// by reference.
export function mergeHistory(turns: TurnModel[], fragment: HistoryFragment, heldOnly = false): TurnModel[] {
  const byId = new Map(turns.map((turn) => [turn.id, turn]));
  const ownedItems = new Set<string>();
  let changed = false;
  for (const incoming of fragment.turns) {
    if (incoming.id === "") continue;
    const held = byId.get(incoming.id);
    if (held ? !supersedes(held.version, incoming.version) : heldOnly) continue;
    byId.set(incoming.id, { ...incoming, items: held?.items ?? [] });
    changed = true;
  }
  for (const incoming of fragment.items) {
    // A recorded item always names its turn; one that does not has none to join.
    if (incoming.turnId === "") continue;
    const held = byId.get(incoming.turnId);
    if (!held && heldOnly) continue;
    const turn = held ?? { id: incoming.turnId, status: "inProgress", items: [] };
    const identity = itemIdentity(incoming);
    const index = turn.items.findIndex((item) => itemIdentity(item) === identity);
    const heldItem = turn.items[index];
    const identityUpgrade =
      heldItem !== undefined &&
      heldItem.version === incoming.version &&
      itemIsIdentityOnly(heldItem) &&
      !itemIsIdentityOnly(incoming);
    if (index === -1 ? heldOnly : !supersedes(heldItem?.version, incoming.version) && !identityUpgrade) continue;
    // Copy a turn's items once per merge, then edit the copy in place.
    const writable = ownedItems.has(turn.id) ? turn : { ...turn, items: [...turn.items] };
    ownedItems.add(turn.id);
    if (index === -1) writable.items.splice(itemsBefore(writable.items, incoming.position), 0, incoming);
    else {
      // A higher-version replacement is otherwise authoritative for every
      // field it carries, but an OMITTED text field is not a replacement —
      // main's landed text-presence rule (itemTextPresence/wireItemToModel):
      // an explicit empty string IS authoritative (a real settle with
      // nothing to say), while an absent one just means this entry said
      // nothing about text, and the prior provided value stands (e.g. a
      // reasoning item's seeded summary surviving a completion that omits
      // text of its own).
      writable.items[index] = mergeReplacedItem(turn.items[index], incoming);
    }
    byId.set(turn.id, writable);
    changed = true;
  }
  if (!changed) return turns;
  return [...byId.values()].sort((left, right) => comparePositions(turnPosition(left), turnPosition(right)));
}

// The earliest held item's position: turns are ordered by position, but a
// turn without items can come first, so every turn's first item counts.
function earliestPosition(turns: readonly TurnModel[]): ThreadItemPosition | undefined {
  let earliest: ThreadItemPosition | undefined;
  for (const turn of turns) {
    const position = turn.items[0]?.position;
    if (position && (!earliest || comparePositions(position, earliest) < 0)) earliest = position;
  }
  return earliest;
}

export function fragmentRange(items: readonly ItemModel[]): [ThreadItemPosition, ThreadItemPosition] | undefined {
  let first: ThreadItemPosition | undefined;
  let last: ThreadItemPosition | undefined;
  for (const item of items) {
    if (!item.position) continue;
    if (!first || comparePositions(item.position, first) < 0) first = item.position;
    if (!last || comparePositions(item.position, last) > 0) last = item.position;
  }
  return first && last ? [first, last] : undefined;
}

// An authoritative read replaces what it returned: held items in [from, to]
// (to undefined: through the end of history) are dropped before the read's
// own items merge in, and a turn left with no items goes with them.
function dropItemsInRange(turns: TurnModel[], from: ThreadItemPosition, to?: ThreadItemPosition): TurnModel[] {
  const inRange = (position: ThreadItemPosition | undefined) =>
    position !== undefined &&
    comparePositions(position, from) >= 0 &&
    (to === undefined || comparePositions(position, to) <= 0);
  let changed = false;
  const kept: TurnModel[] = [];
  for (const turn of turns) {
    const items = turn.items.filter((item) => !inRange(item.position));
    if (items.length === turn.items.length) {
      kept.push(turn);
      continue;
    }
    changed = true;
    if (items.length > 0) kept.push({ ...turn, items });
  }
  return changed ? kept : turns;
}

// Drops the overlay state history now covers: a stream once history holds an
// item of its round, a preview once history holds the agentMessage of its
// communicate call, and a tool's execution state once its history item carries
// the completing TOOL_RESULTS entry. A notice already laid into history owns
// its row there, so its live copy goes too. Returns overlay itself when
// nothing is covered.
export function pruneCoveredOverlay(
  turns: readonly TurnModel[],
  overlay: Record<string, OverlayItem>,
): Record<string, OverlayItem> {
  if (Object.keys(overlay).length === 0) return overlay;
  const rounds = new Set<string>();
  const communicated = new Set<string>();
  const completed = new Set<string>();
  const unrecordedNotices = new Set(
    Object.values(overlay)
      .filter((item) => item.kind === "notice")
      .map((item) => itemIdentity(item.item)),
  );
  for (const turn of turns) {
    for (const item of turn.items) {
      if (unrecordedNotices.size > 0) unrecordedNotices.delete(itemIdentity(item));
      if (item.roundId) rounds.add(item.roundId);
      if (item.type === "agentMessage" && item.callId) communicated.add(item.callId);
      if (item.completedAtEntry && item.transcriptKey) completed.add(item.transcriptKey);
    }
  }
  const covered = (item: OverlayItem): boolean => {
    switch (item.kind) {
      case "stream":
        return item.roundId !== undefined && rounds.has(item.roundId);
      case "preview":
        return item.callId !== undefined && communicated.has(item.callId);
      case "tool":
        return item.historyKey !== undefined && completed.has(item.historyKey);
      case "notice":
        return !unrecordedNotices.has(itemIdentity(item.item));
      default:
        return false;
    }
  };
  return filterOverlay(overlay, covered);
}

// Drops the overlay items drop names; returns overlay itself when none go.
export function filterOverlay(
  overlay: Record<string, OverlayItem>,
  drop: (item: OverlayItem) => boolean,
): Record<string, OverlayItem> {
  const entries = Object.entries(overlay);
  const kept = entries.filter(([, item]) => !drop(item));
  return kept.length === entries.length ? overlay : Object.fromEntries(kept);
}

// A display item the overlay contributes on its own: a stream, preview,
// notice, or a tool whose history item is not held.
function overlayDisplayItem(overlayItem: OverlayItem, imageSessionRoute: string | undefined): ItemModel {
  const item = wireItemToModel(overlayItem.item, imageSessionRoute);
  item.overlayKey = overlayItem.key;
  if (item.turnId === "" && overlayItem.turnId) item.turnId = overlayItem.turnId;
  if (overlayItem.anchor) item.position = { ...overlayItem.anchor };
  // A live (status inProgress) agentMessage/reasoning item renders only from
  // pendingText (AgentMessageItem.tsx, RawItemView.tsx) - pendingText has no
  // wire counterpart (types.gen.ts's ThreadItem carries none), so it never
  // survives wireItemToModel above and has to be minted here, from the
  // overlay's own accumulated text, every time this item is displayed.
  if (item.status === "inProgress" && item.text) item.pendingText = [item.text];
  return item;
}

// A tool's execution state laid over its in-progress history item: running
// output, status and held images. The item keeps its id and version.
function layOverHistoryItem(item: ItemModel, overlayItem: OverlayItem): ItemModel {
  return copyItemTextPresence(item, {
    ...item,
    output: overlayItem.item.output ?? item.output,
    status: overlayItem.item.status ?? item.status,
    outputImages: outputImagesToItemImages(overlayItem.item.outputImages) ?? item.outputImages,
    overlayKey: overlayItem.key,
  });
}

// What produced a display turn that carries overlay items: its recorded turn
// (undefined for a placeholder) and the overlay items laid into it. A display
// turn whose inputs are unchanged is reused, so a delta on one turn leaves
// every other turn's reference alone. A turn with no overlay item is its
// recorded turn itself and needs no entry.
interface DisplayTurnSource {
  recorded: TurnModel | undefined;
  overlay: readonly OverlayItem[];
}

export const displayTurnSources = new WeakMap<TurnModel, DisplayTurnSource>();

function sameOverlayItems(left: readonly OverlayItem[], right: readonly OverlayItem[]): boolean {
  return left.length === right.length && left.every((item, index) => item === right[index]);
}

// The turn holding the recorded item a tool's execution state lays over.
function toolHistoryTurn(overlayItem: OverlayItem, turns: readonly TurnModel[]): TurnModel | undefined {
  const holds = (turn: TurnModel) => turn.items.some((item) => item.transcriptKey === overlayItem.historyKey);
  const hinted = turns.find((turn) => turn.id === overlayItem.turnId);
  return hinted && holds(hinted) ? hinted : turns.find(holds);
}

// The turn holding the recorded item that precedes a notice's anchor across
// every turn (turns can interleave), or undefined when nothing precedes it.
function noticeHistoryTurn(anchor: ThreadItemPosition, turns: readonly TurnModel[]): TurnModel | undefined {
  let found: TurnModel | undefined;
  let preceding: ThreadItemPosition | undefined;
  for (const turn of turns) {
    const position = turn.items[itemsBefore(turn.items, anchor) - 1]?.position;
    if (position && (!preceding || comparePositions(position, preceding) > 0)) {
      found = turn;
      preceding = position;
    }
  }
  return found;
}

// The display turn that shows an overlay item: "" when it names no turn and
// none is running, and it is not shown.
function overlayTurnId(
  overlayItem: OverlayItem,
  turns: readonly TurnModel[],
  runningTurnId: string | undefined,
): string {
  const fallback = overlayItem.turnId || overlayItem.item.turnId || runningTurnId || "";
  if (overlayItem.kind === "tool" && overlayItem.historyKey) {
    return toolHistoryTurn(overlayItem, turns)?.id ?? fallback;
  }
  if (overlayItem.kind === "notice" && overlayItem.anchor) {
    return (noticeHistoryTurn(overlayItem.anchor, turns) ?? turns[0])?.id ?? (fallback || SYSTEM_PRELUDE_TURN_ID);
  }
  return fallback;
}

export function buildDisplayTurn(
  id: string,
  source: DisplayTurnSource,
  imageSessionRoute: string | undefined,
): TurnModel {
  const laid = new Map<string, OverlayItem>();
  const trailing: ItemModel[] = [];
  let items = source.recorded?.items ?? [];
  let noticeItems: ItemModel[] | undefined;
  for (const overlayItem of source.overlay) {
    if (overlayItem.kind === "notice") {
      noticeItems ??= [...items];
      const notice = overlayDisplayItem(overlayItem, imageSessionRoute);
      noticeItems.splice(itemsBefore(noticeItems, notice.position), 0, notice);
    } else if (
      overlayItem.kind === "tool" &&
      overlayItem.historyKey &&
      source.recorded?.items.some((item) => item.transcriptKey === overlayItem.historyKey)
    ) {
      laid.set(overlayItem.historyKey, overlayItem);
    } else {
      trailing.push(overlayDisplayItem(overlayItem, imageSessionRoute));
    }
  }
  items = noticeItems ?? items;
  if (laid.size > 0) {
    items = items.map((item) => {
      const overlayItem = item.transcriptKey === undefined ? undefined : laid.get(item.transcriptKey);
      return overlayItem && !item.completedAtEntry ? layOverHistoryItem(item, overlayItem) : item;
    });
  }
  const turn: TurnModel = {
    ...(source.recorded ?? { id, status: "inProgress" }),
    items: trailing.length > 0 ? [...items, ...trailing] : items,
  };
  displayTurnSources.set(turn, source);
  return turn;
}

// The display turns for recorded turns and an overlay: recorded turns in
// order, then a placeholder turn for each turn the overlay shows that history
// does not hold yet. previous is the model's current display, reused where a
// turn's inputs did not change.
function displayTurns(
  recorded: TurnModel[],
  overlay: Record<string, OverlayItem>,
  previous: readonly TurnModel[],
  imageSessionRoute: string | undefined,
  runningTurnId: string | undefined,
): TurnModel[] {
  const byTurn = new Map<string, OverlayItem[]>();
  for (const overlayItem of Object.values(overlay)) {
    const id = overlayTurnId(overlayItem, recorded, runningTurnId);
    if (id === "") continue;
    const list = byTurn.get(id);
    if (list) list.push(overlayItem);
    else byTurn.set(id, [overlayItem]);
  }
  if (byTurn.size === 0) return recorded;
  const previousById = new Map(previous.map((turn) => [turn.id, turn]));
  const display = (id: string, recordedTurn: TurnModel | undefined): TurnModel => {
    const overlayItems = byTurn.get(id);
    if (!overlayItems && recordedTurn) return recordedTurn;
    const source: DisplayTurnSource = { recorded: recordedTurn, overlay: overlayItems ?? [] };
    const prior = previousById.get(id);
    const priorSource = prior && displayTurnSources.get(prior);
    if (
      prior &&
      priorSource &&
      priorSource.recorded === recordedTurn &&
      sameOverlayItems(priorSource.overlay, source.overlay)
    ) {
      return prior;
    }
    return buildDisplayTurn(id, source, imageSessionRoute);
  };
  const turns = recorded.map((turn) => display(turn.id, turn));
  const held = new Set(recorded.map((turn) => turn.id));
  for (const id of byTurn.keys()) {
    if (!held.has(id)) turns.push(display(id, undefined));
  }
  return turns;
}

export function modelImageSessionRoute(model: ThreadModel): string | undefined {
  return imageSessionRouteForSession(model.imageSessionId ?? model.threadId);
}

// Sets a model's history and overlay and derives its display turns. The
// overlay first loses whatever the history now covers.
//
// Cost: coverage and placement scan every held item (and toolHistoryTurn once
// per tool overlay not laid over a held item), so each history/updated, read
// and overlay upsert is O(held items x tool overlays). Deltas skip this
// (applyOverlayDelta). If long transcripts get sluggish, start here.
export function withDisplay<M extends ThreadModel>(
  model: M,
  history: HistoryState,
  overlay: Record<string, OverlayItem>,
): M & { history: HistoryState } {
  const shown = pruneCoveredOverlay(history.turns, overlay);
  return {
    ...model,
    history,
    overlay: shown,
    turns: displayTurns(history.turns, shown, model.turns, modelImageSessionRoute(model), model.runningTurnId),
  };
}

/**
 * Drops completed item payloads from an idle background model while retaining
 * identities needed for versioned upserts and overlay handoff pruning.
 * Mounted readers keep their own full model; active overlay items are rebuilt
 * through the same display projection used by ordinary notifications.
 */
export function pruneBackgroundHistoryBodies<M extends ThreadModel>(model: M): ThreadModel & ModelExtras<M> {
  if (!model.history) return publicModel<M>(model);
  const history = {
    ...model.history,
    turns: model.history.turns.map((turn) => ({
      ...turn,
      items: turn.items.map((item) =>
        markItemIdentityOnly(
          markItemTextOmitted({
            id: item.id,
            turnId: item.turnId,
            type: item.type,
            text: "",
            // Pending-question owners still need resolution boundaries when
            // the body is absent, including the non-resolving human-note case.
            ...(item.type === "steering" && item.source !== undefined ? { source: item.source } : {}),
            ...(item.type === "steering" && item.steeringKind !== undefined ? { steeringKind: item.steeringKind } : {}),
            ...(item.transcriptKey === undefined ? {} : { transcriptKey: item.transcriptKey }),
            ...(item.position === undefined ? {} : { position: { ...item.position } }),
            ...(item.version === undefined ? {} : { version: item.version }),
            ...(item.roundId === undefined ? {} : { roundId: item.roundId }),
            ...(item.callId === undefined ? {} : { callId: item.callId }),
            ...(item.completedAtEntry === undefined ? {} : { completedAtEntry: item.completedAtEntry }),
            ...(item.transcriptEntryIndex === undefined ? {} : { transcriptEntryIndex: item.transcriptEntryIndex }),
          }),
        ),
      ),
    })),
  };
  const projected = withDisplay(model, history, model.overlay ?? {});
  return publicModel<M>({
    ...projected,
    turns: projected.turns.map((turn) => ({
      ...turn,
      items: turn.items.filter((item) => !itemIsIdentityOnly(item)),
    })),
  });
}

// The read identity a history update, resync push or page names. A resync the
// hub pushes itself names no epoch.
export interface HistorySignal {
  bootGeneration?: string;
  epoch?: number;
  incarnation?: string;
}

// What a signal does to held history. It compares against what an invalid
// thread awaits (the newest generation and epoch that invalidated it), so a
// signal re-arms an invalid thread only when it is newer still, and repeated
// updates from the awaited generation do not. "apply" on an invalid thread
// still means nothing merges.
export function classifySignal(held: HistoryState, signal: HistorySignal): "ignore" | "apply" | "invalidate" {
  const awaited = held.awaited ?? { bootGeneration: held.bootGeneration, epoch: held.epoch };
  const action =
    signal.bootGeneration === undefined
      ? "apply"
      : compareBootGeneration(awaited.bootGeneration, signal.bootGeneration);
  if (action === "ignore") return "ignore";
  if (action === "replace" || signal.epoch === undefined || signal.epoch > awaited.epoch) return "invalidate";
  if (signal.epoch < awaited.epoch) return "ignore";
  // Another incarnation invalidates a live thread. An invalid thread already
  // awaits a replacement, and pages from any incarnation but the pending one
  // are discarded.
  const live = held.invalidatedAtGeneration === undefined;
  return live && signal.incarnation !== undefined && signal.incarnation !== held.incarnation ? "invalidate" : "apply";
}

// Marks a thread invalid, or re-arms an invalid one: nothing merges until a
// latest-window response to a generation issued after this point replaces its
// whole history. Every invalidation moves the threshold to the newest issued
// generation, so a read in flight across a second resync is dropped too.
export function invalidated(history: HistoryState, signal: HistorySignal = {}): HistoryState {
  const awaited = history.awaited ?? { bootGeneration: history.bootGeneration, epoch: history.epoch };
  const bootGeneration = signal.bootGeneration ?? awaited.bootGeneration;
  // Epochs count again from zero in each boot generation.
  const sameBoot = compareBootGeneration(awaited.bootGeneration, bootGeneration) === "apply";
  const newIncarnation = signal.incarnation !== undefined && signal.incarnation !== history.incarnation;
  return {
    ...history,
    invalidatedAtGeneration: history.issuedGeneration,
    awaited: { bootGeneration, epoch: signal.epoch ?? (sameBoot ? awaited.epoch : 0) },
    ...(newIncarnation ? { pendingIncarnation: signal.incarnation } : {}),
  };
}

/**
 * Marks the thread invalid so the next latest-window response (one issued
 * after this call) replaces its whole history instead of merging. A store
 * calls it on reconnect, then issues that read. The model's own extras and any
 * deferred pages are kept.
 */
export function invalidateHistory<M extends ThreadModel>(model: M): ThreadModel & ModelExtras<M> {
  return publicModel<M>({ ...model, history: invalidated(model.history ?? EMPTY_HISTORY) });
}

export function historyIsLive(history: HistoryState | undefined): history is HistoryState {
  return history !== undefined && history.invalidatedAtGeneration === undefined;
}

/**
 * Records a new latest-window thread/read for the thread and returns the
 * request generation it carries (ThreadReadParams.requestGeneration). The
 * counter is per thread and never resets. A store keeps one latest-window read
 * per thread in flight; applyReadResponse drops any response to a generation
 * older than the newest issued, or issued before the thread was invalidated.
 * Backfill pages (thread/turns/list) carry no generation.
 */
export function issueLatestWindowRead<M extends ThreadModel>(
  model: M,
): { model: ThreadModel & ModelExtras<M>; requestGeneration: number } {
  const history = model.history ?? EMPTY_HISTORY;
  const requestGeneration = history.issuedGeneration + 1;
  return {
    model: publicModel<M>({ ...model, history: { ...history, issuedGeneration: requestGeneration } }),
    requestGeneration,
  };
}

/**
 * Records that the latest-window read issued as requestGeneration failed with
 * ErrorTranscriptHistoryFailed (errors.ts's isTranscriptHistoryFailedError).
 * The held history stays as it is and `history.failed` carries the one
 * diagnostic to show; nothing merges or replaces until a latest-window read
 * succeeds, while the overlay keeps updating. The error's generation and epoch
 * are never adopted. A failure that applies ends the thread's invalid state
 * (and what it awaited), so a store does not re-issue the read in a loop; the
 * next read comes from the next event (a resync, a reconnect, the user).
 * A failure of a superseded read (older than the newest issued, or issued
 * before the thread was last invalidated) is ignored and returns model itself,
 * so the read a later invalidation asked for is still issued.
 */
export function applyHistoryReadFailure<M extends ThreadModel>(
  model: M,
  diagnostic: string,
  requestGeneration: number,
): ThreadModel & ModelExtras<M> {
  const held = model.history ?? EMPTY_HISTORY;
  const superseded =
    requestGeneration < held.issuedGeneration ||
    (held.invalidatedAtGeneration !== undefined && requestGeneration <= held.invalidatedAtGeneration);
  if (superseded) return publicModel<M>(model);
  const { invalidatedAtGeneration: _ended, awaited: _awaited, ...history } = held;
  return publicModel<M>({ ...model, history: { ...history, failed: diagnostic } });
}

type ReadDisposition = "discard" | "replace" | "merge";

// The read identity fields readDisposition needs, structurally — satisfied by
// a wire ThreadReadResponse and, for applyReadModel below (a caller whose own
// service layer hydrates the wire response before the merge boundary), by a
// HistoryState the caller already holds.
interface ReadDispositionSignal {
  requestGeneration?: number;
  bootGeneration?: string;
  epoch?: number;
  snapshot?: { incarnation: string; length: number };
}

// The identity-only half of readDisposition: replace/merge/discard from boot
// generation, epoch, incarnation and length alone, with no request-generation
// ordering at all. Shared by readDisposition (which layers request-generation
// ordering on top, for a caller that tracks one — issueLatestWindowRead) and
// applyReadModel (whose caller has no such generation to supply, so this is
// its own disposition outright — see that function's own doc comment for why
// a generation gate would misfire there).
function identityDisposition(held: HistoryState, identity: ReadIdentity): ReadDisposition {
  if (held.invalidatedAtGeneration !== undefined) {
    const { bootGeneration, epoch } = identity;
    return classifySignal(held, { bootGeneration, epoch }) === "ignore" ? "discard" : "replace";
  }
  const action = compareBootGeneration(held.bootGeneration, identity.bootGeneration);
  if (action !== "apply") return action === "ignore" ? "discard" : "replace";
  if (identity.epoch < held.epoch) return "discard";
  if (identity.epoch > held.epoch || identity.incarnation !== held.incarnation) return "replace";
  return identity.length < held.length ? "discard" : "merge";
}

// What a latest-window response does to held history. Request generations
// decide whether it applies at all; then the generation token, the epoch and
// the incarnation decide between replacing the whole history and merging.
export function readDisposition(held: HistoryState, resp: ReadDispositionSignal): ReadDisposition {
  const generation = resp.requestGeneration ?? 0;
  if (generation < held.issuedGeneration) return "discard";
  if (held.invalidatedAtGeneration !== undefined && generation <= held.invalidatedAtGeneration) return "discard";
  return identityDisposition(held, readIdentity(resp));
}

/**
 * Applies a latest-window thread/read response (one issued through
 * issueLatestWindowRead) to a model holding versioned history:
 * - a response to an older request generation than the newest issued, or to one
 *   issued before the thread was invalidated, is discarded;
 * - a lower boot generation, an older epoch, or the same incarnation with a
 *   shorter length is discarded;
 * - an invalid thread, another generation token, a newer epoch or another
 *   incarnation replaces the whole history, then applies any pages deferred
 *   for that incarnation;
 * - otherwise it merges by version; an authoritative (daemonless) response
 *   first drops every held item from its first position on; `changes` (held
 *   items and turns outside the window whose version grew) merge into held
 *   history only.
 * The thread's own fields, the overlay (resp.overlay) and the running turn are
 * always the response's, and a successful read ends a failed-history state.
 * A discarded response returns model itself.
 */
export function applyReadResponse<M extends ThreadModel>(
  model: M,
  resp: ThreadReadResponse,
  now: number,
): ThreadModel & ModelExtras<M> {
  const held = model.history ?? EMPTY_HISTORY;
  const disposition = readDisposition(held, resp);
  if (disposition === "discard") return publicModel<M>(model);
  const fields = threadFields(resp, model.ref, now);
  const imageSessionRoute = imageSessionRouteForSession(fields.imageSessionId ?? fields.threadId);
  const fresh = splitWireTurns(resp.thread.turns ?? [], imageSessionRoute);
  let turns: TurnModel[];
  let olderCursor = resp.olderCursor;
  if (disposition === "replace") {
    turns = mergeHistory([], fresh);
  } else {
    const window = fragmentRange(fresh.items);
    turns = resp.authoritative ? dropItemsInRange(held.turns, window?.[0] ?? { entry: 0, item: 0 }) : held.turns;
    // Older pages the client holds keep their own cursor.
    const oldest = earliestPosition(turns);
    if (oldest && (!window || comparePositions(oldest, window[0]) < 0)) olderCursor = model.olderCursor;
    turns = mergeHistory(turns, fresh);
    if (resp.changes) {
      turns = mergeHistory(turns, wireFragment(resp.changes.turns, resp.changes.items, imageSessionRoute), true);
    }
  }
  const generation = resp.requestGeneration ?? 0;
  const history: HistoryState = {
    ...readIdentity(resp),
    appliedGeneration: generation,
    issuedGeneration: Math.max(held.issuedGeneration, generation),
    deferredPages: [],
    turns,
  };
  const { runningTurnId: _previous, ...base } = model;
  // The spread keeps any field this read omits. A snapshot that no longer
  // advertises an artifacts root must clear the previous one, or link actions
  // stay aimed at a directory the hub no longer serves.
  const clearedArtifacts = fields.artifactsDir === undefined ? { artifactsDir: undefined } : {};
  let next = withDisplay(
    { ...base, ...fields, ...clearedArtifacts, ...runningTurn(resp.thread), olderCursor },
    history,
    overlayRecord(resp.overlay),
  );
  for (const deferred of held.deferredPages) {
    if (deferred.snapshot?.incarnation === history.incarnation) {
      next = mergeVersionedPage(next, next.history, deferred, false);
    }
  }
  return publicModel<M>(next);
}

// Splits turns already merged into TurnModel[] back into a HistoryFragment
// (turn scalars, items) — the shape mergeHistory folds in applyReadModel
// below, mirroring splitWireTurns for a caller whose input is an
// already-hydrated model rather than a wire response.
function turnModelsToFragment(turns: readonly TurnModel[]): HistoryFragment {
  return {
    turns: turns.map((turn) => ({ ...turn, items: [] })),
    // mergeHistory joins each item to its turn by item.turnId alone (never by
    // array nesting) — splitWireTurns backfills a blank one from the
    // containing wire turn for exactly this reason. A model hydrated outside
    // this package's own versioned path (a caller's pre-v6 display model, or
    // a legacy hydrate with no snapshot) can carry items whose turnId was
    // never stamped at all — nesting was the only membership signal there —
    // so the same backfill runs here, or every such item joins the same
    // synthetic turnId-"" turn instead of the one that actually holds it.
    items: turns.flatMap((turn) => turn.items.map((item) => (item.turnId ? item : { ...item, turnId: turn.id }))),
  };
}

// readDisposition for a caller with no request-generation protocol of its
// own (see applyReadModel's doc comment: no issueLatestWindowRead, no
// requestGeneration on the wire request, so appliedGeneration is always the
// held value and never advances). readDisposition's own generation gates
// (`generation < held.issuedGeneration`, `generation <= held.invalidatedAtGeneration`)
// assume a caller that bumps issuedGeneration BEFORE every read and echoes it
// back — exactly the thing this caller cannot do, so gating discard/replace
// on it would silently freeze an invalidated thread forever (a resync sets
// invalidatedAtGeneration to whatever is currently held, and every later read
// arrives at that SAME value, never a higher one, so `generation <=
// invalidatedAtGeneration` never clears). Once invalidated, this instead
// classifies the fresh read's own identity directly: an ignorable boot
// generation/epoch (older or equal to what invalidated it) still discards,
// but anything else — the fresh read's own snapshot, whatever it is —
// resolves the invalidation, since it is the only signal this caller has.
function readModelDisposition(held: HistoryState, freshHistory: HistoryState | undefined): ReadDisposition {
  return freshHistory === undefined ? "merge" : identityDisposition(held, freshHistory);
}

/**
 * applyReadResponse for a caller whose own service layer hydrates the wire
 * ThreadReadResponse into a ThreadModel (hydrateThread) before the merge
 * boundary reaches this package — mobile's ConversationService is exactly
 * this shape, and its existing test suite scripts fixtures at the
 * hydrated-model level throughout, so requiring the raw wire response here
 * would force rewriting it wholesale. Disposition rules mirror
 * applyReadResponse (read from `fresh.history`, which hydrateThread already
 * stamps, instead of a wire response's own fields) with one difference:
 * this caller has no request-generation protocol of its own (see
 * readModelDisposition's doc comment) — invalidation recovery classifies the
 * fresh read's identity directly instead of gating on a generation this
 * caller cannot supply. A fresh read that carries no snapshot identity at
 * all (fresh.history undefined — a legacy hydrate, or a daemon that answered
 * with no history support) always merges by identity (a version-less item,
 * held or incoming, always yields — see `supersedes` — so this degrades to
 * the same content-wins-on-reissue behavior an unversioned merge always
 * had), never discarded or replaced outright: with no generation/epoch/
 * incarnation of its own to compare, discarding or replacing would be a
 * guess this entry point has no basis for. A service can carry the wire's
 * `changes` alongside its hydrated model, updating held content outside the
 * latest window after the same identity admission as applyReadResponse.
 * The wire-only `authoritative` range replacement policy does not apply here.
 */
export function applyReadModel<M extends ThreadModel>(
  model: M,
  fresh: ThreadModel,
  changes?: ThreadReadResponse["changes"],
): ThreadModel & ModelExtras<M> {
  const held = model.history ?? EMPTY_HISTORY;
  const freshHistory = fresh.history;
  const disposition = readModelDisposition(held, freshHistory);
  if (disposition === "discard") return publicModel<M>(model);
  const fragment = turnModelsToFragment(freshHistory?.turns ?? fresh.turns);
  let turns: TurnModel[];
  let olderCursor = fresh.olderCursor;
  if (disposition === "replace") {
    turns = mergeHistory([], fragment);
  } else {
    const window = fragmentRange(fragment.items);
    turns = held.turns;
    // Older pages the client holds keep their own cursor.
    const oldest = earliestPosition(turns);
    if (oldest && (!window || comparePositions(oldest, window[0]) < 0)) olderCursor = model.olderCursor;
    turns = mergeHistory(turns, fragment);
    if (changes) {
      const imageSessionRoute = imageSessionRouteForSession(fresh.imageSessionId ?? fresh.threadId);
      turns = mergeHistory(turns, wireFragment(changes.turns, changes.items, imageSessionRoute), true);
    }
  }
  // A caller here never tracks a request generation of its own (no
  // issueLatestWindowRead, no requestGeneration on the wire request — see
  // readModelDisposition's own doc comment), so there is no meaningful
  // appliedGeneration/issuedGeneration to advance past what is already
  // held; carrying held's forward (rather than always 0) at least survives
  // a real one arriving from elsewhere (a page response's own snapshot).
  const history: HistoryState = {
    // freshHistory undefined (a caller with no snapshot support, or one
    // hydrated on the legacy branch) has no identity of its own to
    // supersede held's with — keep held's exactly, or the next genuinely
    // versioned read reads a real incarnation against a blanked-out one and
    // replaces wholesale, discarding the very history this merge just
    // preserved. Only the four identity fields carry over — never
    // invalidatedAtGeneration/awaited/pendingIncarnation/failed, which this
    // merge (a non-discard disposition) already resolved.
    bootGeneration: freshHistory?.bootGeneration ?? held.bootGeneration,
    epoch: freshHistory?.epoch ?? held.epoch,
    incarnation: freshHistory?.incarnation ?? held.incarnation,
    length: freshHistory?.length ?? held.length,
    appliedGeneration: held.appliedGeneration,
    issuedGeneration: held.issuedGeneration,
    deferredPages: [],
    turns,
  };
  const {
    turns: _freshTurns,
    history: _freshHistoryField,
    overlay: freshOverlay,
    runningTurnId: freshRunningTurnId,
    ...fields
  } = fresh;
  const { runningTurnId: _previous, ...base } = model;
  // Same rule as applyReadResponse: a freshly hydrated model that carries no
  // artifacts root must clear the previous one, or the spread keeps it and
  // link actions stay aimed at a directory the snapshot no longer names.
  const clearedArtifacts =
    fresh.artifactsDir === undefined && model.artifactsDir !== undefined ? { artifactsDir: undefined } : {};
  let next = withDisplay(
    {
      ...base,
      ...fields,
      ...clearedArtifacts,
      ...(freshRunningTurnId ? { runningTurnId: freshRunningTurnId } : {}),
      olderCursor,
    },
    history,
    freshOverlay ?? {},
  );
  for (const deferred of held.deferredPages) {
    if (deferred.snapshot?.incarnation === history.incarnation) {
      next = mergeVersionedPage(next, next.history, deferred, false);
    }
  }
  return publicModel<M>(next);
}

type PageDisposition = "discard" | "defer" | "invalidate" | "merge";

// What a backfill page does to held history. Pages carry no request
// generation: they accumulate within their snapshot in any order, and never
// replace anything.
function pageDisposition(held: HistoryState, resp: ThreadTurnsListResponse): PageDisposition {
  if (held.failed !== undefined) return "discard";
  const identity = readIdentity(resp);
  const signal = classifySignal(held, identity);
  if (signal !== "apply") return signal === "ignore" ? "discard" : "invalidate";
  if (held.invalidatedAtGeneration !== undefined) {
    return identity.incarnation === held.pendingIncarnation ? "defer" : "discard";
  }
  return identity.length < held.length ? "discard" : "merge";
}

function mergeVersionedPage<M extends ThreadModel>(
  model: M,
  held: HistoryState,
  resp: ThreadTurnsListResponse,
  takeCursor: boolean,
): M {
  switch (pageDisposition(held, resp)) {
    case "discard":
      return model;
    case "defer":
      return { ...model, history: { ...held, deferredPages: [...held.deferredPages, resp] } };
    case "invalidate": {
      // A page of the incarnation it just announced waits for that
      // incarnation's latest window.
      const history = invalidated(held, readIdentity(resp));
      const deferred = history.pendingIncarnation === resp.snapshot?.incarnation;
      return {
        ...model,
        history: deferred ? { ...history, deferredPages: [...history.deferredPages, resp] } : history,
      };
    }
    case "merge": {
      const fresh = splitWireTurns(resp.data ?? [], modelImageSessionRoute(model));
      const range = resp.authoritative ? fragmentRange(fresh.items) : undefined;
      const turns = mergeHistory(range ? dropItemsInRange(held.turns, range[0], range[1]) : held.turns, fresh);
      const next = takeCursor ? { ...model, olderCursor: resp.nextCursor } : model;
      // The held length never advances from a page: a page's snapshot length
      // is the transcript length the index had at read time, not a promise
      // that everything through it was delivered - Before only returns items
      // older than the cursor, so a page can carry a longer length than held
      // without carrying the live items created in between. Advancing held
      // to it would make the next latest-window read's heldSnapshot claim
      // completeness through a length whose in-between changes it never
      // saw, and LatestSince's ChangedSince(held.length) would then skip
      // them silently. Only a latest-window read (and its accompanying
      // changes) verifies completeness up to a length, so only that advances
      // held. pageDisposition still requires a page's length to be at least
      // held's to merge, comparing against that one true, latest-window
      // watermark - never one a page pushed forward.
      return withDisplay(next, { ...held, turns }, model.overlay ?? {});
    }
  }
}

// The fields a caller adds on top of ThreadModel, and ONLY those. ThreadModel's
// own fields are deliberately taken from ThreadModel, not from M: the reducer
// owns and rewrites them (clearing modelRetry, restamping lastFrameAt,
// replacing status), so a caller that intersects one to a narrower type gets
// ThreadModel's type back rather than a contract the fold is about to break.
//
// The conditional distributes over a union M: a plain Omit collapses a union to
// its members' COMMON keys and drops each member's own extras, so a caller
// whose model is a union would lose the field it distinguishes the members by.
export type ModelExtras<M extends ThreadModel> = M extends unknown ? Omit<M, keyof ThreadModel> : never;

// Re-attaches ModelExtras at the exported boundary. The fold works on the
// concrete model M — every case spreads it, so the runtime value already
// carries the caller's extras — but TS cannot prove a bare generic M assignable
// to the conditional type above, so the public entry points convert here.
export function publicModel<M extends ThreadModel>(value: unknown): ThreadModel & ModelExtras<M> {
  return value as ThreadModel & ModelExtras<M>;
}

// hydrateThread builds a thread's whole model from one read. A v6 read (one
// that names its snapshot) builds versioned history and the overlay; a pre-v6
// read keeps the unversioned turns it always had.
export function hydrateThread(resp: ThreadReadResponse, ref: string, now: number): ThreadModel {
  const fields = threadFields(resp, ref, now);
  const imageSessionRoute = imageSessionRouteForSession(fields.imageSessionId ?? fields.threadId);
  if (!resp.snapshot) {
    return {
      ...fields,
      turns: mergeToolCallsByCallId((resp.thread.turns ?? []).map((turn) => wireToTurnModel(turn, imageSessionRoute))),
    };
  }
  const fresh = splitWireTurns(resp.thread.turns ?? [], imageSessionRoute);
  const generation = resp.requestGeneration ?? 0;
  const history: HistoryState = {
    ...readIdentity(resp),
    appliedGeneration: generation,
    issuedGeneration: generation,
    deferredPages: [],
    turns: mergeHistory([], fresh),
  };
  return withDisplay({ ...fields, turns: [], ...runningTurn(resp.thread) }, history, overlayRecord(resp.overlay));
}

/** The first and last item positions of a read response's fresh window: the
 * bounds the store's shell seam compares against the captured anchor and the
 * model's current newest (spec, "The two serving paths, the live gap, and
 * its rule"). Pure record data; no disposition logic lives here. */
export function readWindowBounds(resp: ThreadReadResponse): {
  start: ThreadItemPosition | undefined;
  end: ThreadItemPosition | undefined;
} {
  // threadFields' own session-id derivation (the wire sessionId, falling back
  // to the thread id, trimmed), shared here so the bounds read the same
  // image session route the read itself would.
  const fresh = splitWireTurns(resp.thread.turns ?? [], imageSessionRouteForSession(wireImageSessionId(resp.thread)));
  const range = fragmentRange(fresh.items);
  return { start: range?.[0], end: range?.[1] };
}

/** Merges a fold tail (the pre-replacement model's turns positioned above the
 * replacement window's end) onto a model whose history hydrateThread just
 * built. The replay's result is the window plus exactly the folds that
 * arrived (spec: "the rule therefore still replaces, then replays"). */
export function mergeTailTurns<M extends ThreadModel>(model: M, tail: TurnModel[]): ThreadModel & ModelExtras<M> {
  if (tail.length === 0 || model.history === undefined) return publicModel<M>(model);
  const merged = mergeTurnHistory(model.history.turns, tail);
  const history: HistoryState = { ...model.history, turns: merged.turns };
  // model.overlay is set on every model withDisplay built (hydrateThread's v6
  // branch included); the ?? {} is for the type, whose field is optional.
  return publicModel<M>(withDisplay({ ...model, history }, history, model.overlay ?? {}));
}

export function collectAuthoritativeMutationIds(resp: ThreadReadResponse): Set<string> {
  const identities = new Set<string>();
  for (const pending of resp.thread.evener.pendingMutations ?? []) identities.add(pending.clientMutationId);
  for (const clientMutationId of resp.thread.evener.queue.clientMutationIds ?? []) identities.add(clientMutationId);
  for (const turn of resp.thread.turns ?? []) {
    for (const item of turn.items ?? []) {
      if (item.clientMutationId) identities.add(item.clientMutationId);
    }
  }
  return identities;
}

export function prependOlderTurns(model: ThreadModel, resp: ThreadTurnsListResponse): ThreadModel {
  return mergeOlderItemPage(model, resp);
}
