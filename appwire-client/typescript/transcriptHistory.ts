// Versioned history and the live overlay: history state, versioned item and
// turn merges, overlay display derivation, and read dispositions. Imports
// `turnHistoryMerge` and `transcriptItems`; the notification folds that drive
// these live in `reducer.ts`.

import { compareBootGeneration } from "./bootGeneration";
import { historyFallback, historyReplacesWindow, historyServingFacts } from "./historyServing";
import type { HistoryState, ItemModel, OlderFrontier, ThreadModel, TurnModel } from "./model";
import { comparePositions, SYSTEM_PRELUDE_TURN_ID } from "./model";
import { frontierFromCursor, olderFrontierOf, withOlderFrontier } from "./olderFrontier";

export { comparePositions } from "./model";
export { frontierFromCursor, olderFrontierOf, olderHistoryExhausted, withOlderFrontier } from "./olderFrontier";

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

// A page from the saved projection merges its content into the held
// transcript without speaking for the live history: it cannot replace or
// remove held turns, advance the identity, or adopt its own continuation. The
// live continuation stays available for recovery while the saved view pages.
export function mergeSavedViewPage(model: ThreadModel, resp: ThreadTurnsListResponse): ThreadModel {
  const fragment = splitWireTurns(resp.data ?? [], imageSessionRouteForSession(model.imageSessionId ?? model.threadId));
  // A saved page may not resurrect an identity a newer observation omitted.
  const kept = withoutNewerPageOmissions(model.history ?? EMPTY_HISTORY, fragment, resp.snapshot?.length ?? 0);
  const turns = mergeHistory(model.history?.turns ?? model.turns, kept);
  return model.history ? withDisplay(model, { ...model.history, turns }, model.overlay ?? {}) : { ...model, turns };
}

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

// Carry only active identity, never recovery state a successful read resolved.
function retainedReadIdentity(identity: ReadIdentity | undefined, held: ReadIdentity): ReadIdentity {
  return {
    bootGeneration: identity?.bootGeneration ?? held.bootGeneration,
    epoch: identity?.epoch ?? held.epoch,
    incarnation: identity?.incarnation ?? held.incarnation,
    length: identity?.length ?? held.length,
  };
}

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
    if (heldVersion === incomingVersion && fillsAskEvidence(held, incoming)) {
      return withAskEvidence(held, incoming);
    }
    return held;
  }
  return mergeReplacedItem(held, incoming);
}

// fillsAskEvidence reports whether incoming supplies an ask-resolution field
// the held row lacks. The two fields arrive independently — a bounded read can
// carry the legacy count while omitting the call list — so each is tested on
// its own. Gating on both being absent let a held count block the call list
// that a later refresh of the SAME item version supplies (#6323).
function fillsAskEvidence(held: ItemModel, incoming: ItemModel): boolean {
  const missingCalls = held.resolvedAskCalls === undefined && incoming.resolvedAskCalls !== undefined;
  const missingCount = held.resolvedAskLegacyCount === undefined && incoming.resolvedAskLegacyCount !== undefined;
  return missingCalls || missingCount;
}

// withAskEvidence returns the held row plus only the evidence it lacks. Text,
// identity and every other field stay the held row's: this exists to fill an
// absence, not to replace a row the version rule chose to keep. The text
// presence marker is non-enumerable, so a spread drops it; without
// copyItemTextPresence the held row would come back reading as an
// authoritative empty settle and block the local text reconciliation that is
// still owed for it.
function withAskEvidence(held: ItemModel, incoming: ItemModel): ItemModel {
  const filled: ItemModel = copyItemTextPresence(held, { ...held });
  if (held.resolvedAskCalls === undefined && incoming.resolvedAskCalls !== undefined) {
    filled.resolvedAskCalls = incoming.resolvedAskCalls.map((call) => ({ ...call }));
  }
  if (held.resolvedAskLegacyCount === undefined && incoming.resolvedAskLegacyCount !== undefined) {
    filled.resolvedAskLegacyCount = incoming.resolvedAskLegacyCount;
  }
  // The identity-only marker is non-enumerable too, so the spread drops it.
  // A placeholder that gains evidence must stay a placeholder: without the
  // marker a later same-version refresh can no longer take the upgrade path
  // that would restore its body, so the fill would destroy the evidence that
  // fixes the row.
  return itemIsIdentityOnly(held) ? markItemIdentityOnly(filled) : filled;
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
    // One decision, not two. The same function the entry-cache merge uses
    // resolves version precedence, an identity upgrade and the same-version
    // ask-evidence fill (#6323) together; re-deriving any of them here is how
    // the upgrade and the fill came to disagree, with a pruned placeholder
    // gaining evidence but keeping an empty body the refresh supplied.
    // A held row with no recorded version must still pass through the merge:
    // `supersedes` already gives the incoming row precedence there, and the
    // omission-tolerant merge is what stops a sparse tool update from erasing
    // output or images the reader already has, or a text-omitting update from
    // blanking the row.
    const merged =
      index === -1 || heldItem === undefined
        ? incoming
        : mergeTranscriptItemVersioned(heldItem, heldItem.version, incoming, incoming.version ?? 0);
    if (index === -1 ? heldOnly : merged === heldItem) continue;
    // Copy a turn's items once per merge, then edit the copy in place.
    const writable = ownedItems.has(turn.id) ? turn : { ...turn, items: [...turn.items] };
    ownedItems.add(turn.id);
    if (index === -1) writable.items.splice(itemsBefore(writable.items, incoming.position), 0, incoming);
    else writable.items[index] = merged;
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
function dropItemsInRange(
  turns: TurnModel[],
  from: ThreadItemPosition,
  to?: ThreadItemPosition,
  keep?: (item: ItemModel) => boolean,
): TurnModel[] {
  const inRange = (position: ThreadItemPosition | undefined) =>
    position !== undefined &&
    comparePositions(position, from) >= 0 &&
    (to === undefined || comparePositions(position, to) <= 0);
  let changed = false;
  const kept: TurnModel[] = [];
  for (const turn of turns) {
    const items = turn.items.filter((item) => !inRange(item.position) || keep?.(item));
    if (items.length === turn.items.length) {
      kept.push(turn);
      continue;
    }
    changed = true;
    if (items.length > 0) kept.push({ ...turn, items });
  }
  return changed ? kept : turns;
}

function newerPageObservation(
  history: HistoryState,
  position: ThreadItemPosition | undefined,
  snapshot: number,
): boolean {
  return (
    position !== undefined &&
    (history.replacementScopes ?? []).some(
      (scope) =>
        scope.representedSnapshot > snapshot &&
        comparePositions(position, scope.from) >= 0 &&
        comparePositions(position, scope.to) <= 0,
    )
  );
}

// The highest entry ordinal a response reported: the boundary of the entries
// its replacement can have omitted. Item and turn versions use that ordinal
// scale, while a snapshot length counts transcript bytes.
function servedCutOrdinal(fresh: HistoryFragment): number {
  let highest = 0;
  for (const item of fresh.items) highest = Math.max(highest, item.version ?? 0);
  for (const turn of fresh.turns) highest = Math.max(highest, turn.version ?? 0);
  return highest;
}

// A response or notification that is not authoritative for the range it
// covers cannot re-add an identity a newer page observation has already
// omitted.
export function withoutNewerPageOmissions(
  history: HistoryState,
  fragment: HistoryFragment,
  snapshot: number,
): HistoryFragment {
  const items = fragment.items.filter((item) => !newerPageObservation(history, item.position, snapshot));
  return items.length === fragment.items.length ? fragment : { ...fragment, items };
}

// Replacement removes omitted identities, not the held version of an identity
// the response still names, nor an item recorded after the entries it
// returned. An older observation cannot undo a newer range.
function replaceObservedWindow(
  history: HistoryState,
  fresh: HistoryFragment,
  from: ThreadItemPosition,
  to: ThreadItemPosition | undefined,
  snapshot: number,
): TurnModel[] {
  // Whatever the shape, the highest ordinal the response reported bounds what
  // its replacement can have omitted: a held item recorded after it survives.
  const cutOrdinal = servedCutOrdinal(fresh);
  const key = (item: ItemModel) => JSON.stringify([item.turnId, itemIdentity(item)]);
  const heldKeys = new Set(history.turns.flatMap((turn) => turn.items.map(key)));
  const items = fresh.items.filter(
    (item) => heldKeys.has(key(item)) || !newerPageObservation(history, item.position, snapshot),
  );
  const returnedKeys = new Set(items.map(key));
  const returnedTurns = new Set(items.map((item) => item.turnId));
  const originalTurns = new Set(fresh.items.map((item) => item.turnId));
  const heldTurns = new Set(history.turns.map((turn) => turn.id));
  const turns = dropItemsInRange(
    history.turns,
    from,
    to,
    (item) =>
      returnedKeys.has(key(item)) ||
      (item.version ?? 0) > cutOrdinal ||
      newerPageObservation(history, item.position, snapshot),
  );
  return mergeHistory(turns, {
    items,
    turns: fresh.turns.filter(
      (turn) => !originalTurns.has(turn.id) || returnedTurns.has(turn.id) || heldTurns.has(turn.id),
    ),
  });
}

function observePageRange(
  scopes: HistoryState["replacementScopes"],
  from: ThreadItemPosition,
  to: ThreadItemPosition,
  representedSnapshot: number,
  representedOrdinal: number,
): NonNullable<HistoryState["replacementScopes"]> {
  const held = scopes ?? [];
  if (
    held.some(
      (scope) =>
        scope.representedSnapshot >= representedSnapshot &&
        comparePositions(scope.from, from) <= 0 &&
        comparePositions(scope.to, to) >= 0,
    )
  )
    return held;
  return [
    ...held.filter(
      (scope) =>
        !(
          representedSnapshot >= scope.representedSnapshot &&
          comparePositions(from, scope.from) <= 0 &&
          comparePositions(to, scope.to) >= 0
        ),
    ),
    { from, to, representedSnapshot, representedOrdinal },
  ];
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
  if (historyFallback(resp)) return "merge";
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
// quotaWaitStampMoved reports whether the wait a read names is not the wait a
// retained quota retry announced, by the daemon-issued WaitStartedAt stamps
// both routes carry. A MATCHING stamp is decisive - the same wait, whatever
// the delivery delay did to the browser-computed deadline - so the caller
// must skip its deadline comparison entirely when this returns false with
// both stamps present; the comparison is only the fallback for a side that
// predates the stamp (an older daemon).
function quotaWaitStampMoved(
  read: Pick<ThreadModel, "quotaResumesAt" | "quotaWaitStartedAt">,
  model: Pick<ThreadModel, "modelRetry">,
): boolean {
  const retryStamp = model.modelRetry?.waitStartedAt;
  const readStamp = read.quotaWaitStartedAt;
  if (retryStamp === undefined || readStamp === undefined) return false;
  return retryStamp !== readStamp;
}

// quotaWaitStampsCorrelate reports whether both routes carry wait stamps at
// all: when they do, the stamp comparison above is the whole correlation and
// the deadline fallback must not also run (a delivery-delayed deadline would
// discard a matching wait's elapsed time).
function quotaWaitStampsCorrelate(
  read: Pick<ThreadModel, "quotaResumesAt" | "quotaWaitStartedAt">,
  model: Pick<ThreadModel, "modelRetry">,
): boolean {
  return model.modelRetry?.waitStartedAt !== undefined && read.quotaWaitStartedAt !== undefined;
}

// quotaReadNamesOlderWait reports whether the read's summary describes a wait
// that began before the wait the pane holds. Both instants are daemon-issued
// WaitStartedAt stamps, and each stamp is unique to one wait, so the earlier
// stamp is the older wait whatever either side's delay or delivery did.
//
// A read that names an older wait is a snapshot of a state the pane has
// already left. Its deadline, its stamp and the retry metadata the pane holds
// belong to one wait and move together: this verdict is what keeps the older
// read from spreading its deadline and stamp over the newer wait while the
// merge preserves that newer wait's retry, which would render the first reset
// beside the second call's elapsed time. A read naming a NEWER wait is not
// this case: it replaces the deadline and stamp, and the moved stamp retires
// the retry that belonged to the wait it supersedes.
function quotaReadNamesOlderWait(
  read: Pick<ThreadModel, "quotaWaitStartedAt">,
  model: Pick<ThreadModel, "quotaWaitStartedAt" | "modelRetry">,
): boolean {
  const readStamp = read.quotaWaitStartedAt;
  const heldStamp = model.quotaWaitStartedAt ?? model.modelRetry?.waitStartedAt;
  if (readStamp === undefined || heldStamp === undefined) return false;
  return readStamp < heldStamp;
}

// quotaReadPredatesWait reports whether the read cannot speak against the wait
// the pane holds: its cut predates the wait, or the source DECLARED that it
// supplies no cut at all. Either way its absent failure summary is silence
// rather than a statement the wait ended.
//
// UNKNOWN is not ABSENT, and the wire already carries the difference. A source
// that cannot stamp a cut says so - servingFactsUnavailableReason, which the
// versioned read fills in as "older-source" when it had to fall back, and which
// a peer hub propagates for a source older than itself. That is a read with no
// ordering evidence, so it cannot end a live wait: a remote pane refreshing
// while a wait begins would otherwise erase a countdown the daemon had just
// published. Facts that are simply MISSING are a different thing - the legacy
// methods predate serving facts entirely, and their silence is the shipped
// authority rule those routes have always had, so they keep it.
//
// Both instants are the DAEMON's: the cut is the source's own read-cut stamp
// (server/appwire_read_handlers.go writes it from time.Now), and the wait's
// start is the daemon-issued WaitStartedAt. The pane's receipt time is
// deliberately NOT part of the comparison. It is the browser's clock, and
// nothing in this client may assume it agrees with the daemon's: a reader
// whose clock runs fast would make every fresh cut look older than the wait,
// keep a countdown the read had already ended, and leave "Waiting for quota"
// on a turn that resumed - the failure this comparison exists to prevent,
// inverted.
//
// The bound is the wait's START, not its reset, and that is what the
// daemon's own publication order buys. The bridge refreshes the thread
// envelope with the wait before it publishes the wait's event
// (server/bridge.go), so the summary is in every read cut at or after the
// instant the wait began. A read cut after that instant therefore carries
// the summary whenever the wait still stands; its silence means the wait
// ended - a steer or a model switch resumed the call on another client while
// this pane missed the wake. Treating the whole span up to the scheduled
// reset as "too early to tell" is what kept such an ended countdown on
// screen, and what kept a second wait's retry metadata alive under a fresh
// read's deadline.
//
// A read with no cut instant, or a model holding no wait stamp, keeps the
// existing rules.
function quotaReadPredatesWait(
  read: Pick<ThreadModel, "servingFacts">,
  model: Pick<ThreadModel, "quotaWaitStartedAt" | "modelRetry">,
): boolean {
  const asOf = read.servingFacts?.asOf;
  const waitStartedAt = model.quotaWaitStartedAt ?? model.modelRetry?.waitStartedAt;
  if (waitStartedAt === undefined) return false;
  if (asOf === undefined) return read.servingFacts?.servingFactsUnavailableReason !== undefined;
  const cutMs = Date.parse(asOf);
  if (!Number.isFinite(cutMs)) return false;
  return cutMs < waitStartedAt;
}

export interface QuotaReadEvidence {
  // The instant this pane SENT the read.
  //
  // It is not the read's serving cut: the cut is the daemon's clock, and
  // comparing it against anything this browser stamped is the cross-clock error
  // the countdown rules must never make. This instant is compared only against
  // other browser instants - another read's, or a frame's receipt - which is
  // what makes the comparisons below exact under any skew.
  issuedAt?: number;
  // When this pane RECEIVED the quota wait's announcement, when the merge base
  // does not carry it. A read is usually merged against the model it was
  // issued from, and that model predates the announcement by construction -
  // exactly the race the announcement's receipt has to order. A caller holding
  // the live model supplies it here. Absent means the base itself is the live
  // model, whose own modelRetry carries the receipt.
  announcedAt?: number;
}

// quotaReadNewerThanWait reports whether the read can be ordered against the
// wait the pane holds. It answers with ORDER, never with a duration: a read
// issued before this pane received the wait's announcement cannot be newer than
// the wait's publication, whatever either clock says.
//
// Why the receipt instant bounds the publication. The daemon stamps the wait
// into the thread projection BEFORE it emits the announcement that publishes it
// (server/bridge.go refreshes the envelope, then records the event), and the
// announcement reaches this pane after that. So the projection knew the wait
// before the pane did. A read issued at or after the pane received the
// announcement therefore reached the daemon after the wait was projected, and
// its absent summary is a statement about a published wait rather than the
// silence of a cut taken in the gap between the two.
//
// A wait this pane learned ONLY from a read has no announcement to order
// against: the read is that wait's sole route, and its absent summary is the
// only evidence the pane will ever get. That case keeps the shipped rule.
//
// This is the bound quotaReadPredatesWait cannot supply on its own. A cut
// stamped between the session's wait and the bridge's projection refresh has an
// asOf LATER than waitStartedAt and carries no summary, so the cut comparison
// reads it as a wait that ended and erases a live countdown for the rest of the
// wait.
//
// WHOSE receipt, though - that is the whole rule. The receipt must belong to
// the wait being reconciled, and the model's own retry is the only thing that
// names one. A read can be issued during wait A and return after the pane
// received wait B's announcement; its caller captured A's receipt, and letting
// that stand for B would order the read against the wrong publication and clear
// a countdown B had only just started. So the model's receipt wins whenever the
// model holds a quota retry - it carries B, the wait this merge is deciding.
// Caller-supplied evidence is the fallback for the case the model holds NO
// retry, which is the announcement withheld from the model during a pending
// hydration: then there is no wait identity in the model to match, and the
// caller's receipt is the only evidence the pane received the announcement.
function quotaReadNewerThanWait(model: Pick<ThreadModel, "modelRetry">, evidence: QuotaReadEvidence): boolean {
  const heldRetry = model.modelRetry?.errorClass === "quota_exceeded" ? model.modelRetry : undefined;
  const announcedAt = heldRetry?.receivedAt ?? evidence.announcedAt;
  if (announcedAt === undefined) return true;
  return evidence.issuedAt !== undefined && evidence.issuedAt >= announcedAt;
}

// quotaReadMayEndWait is the ONE predicate every quota read merge asks before
// it retires a held wait. Its rule, in the reviewer's words: RETIRE ON
// ESTABLISHED REPLACEMENT, PRESERVE WHEN EVIDENCE IS MERELY ABSENT.
//
// "Established replacement" is turn ownership - the read names a different
// running turn than the wait's, which is the read's own fact and needs no
// serving cut. An established replacement outranks the cut comparison: a
// response that declares its cut unavailable still proves the wait's turn
// ended, and letting the unavailable cut veto that retired nothing while the
// pane counted down a canceled wait. "Merely absent" is everything else: a cut
// in the projection's publication gap, a declared-unavailable cut, or a read
// issued before this pane heard of the wait at all.
function quotaReadMayEndWait(
  read: Pick<ThreadModel, "servingFacts">,
  model: Pick<ThreadModel, "quotaWaitStartedAt" | "modelRetry">,
  evidence: QuotaReadEvidence,
  waitTurnReplaced: boolean,
): boolean {
  if (waitTurnReplaced) return true;
  // A caller that supplies no issue instant is a caller with no announcement
  // to order against: it keeps the shipped cut-only rule rather than being
  // read as "unordered", which would preserve every wait it ever saw.
  if (evidence.issuedAt === undefined) return !quotaReadPredatesWait(read, model);
  return quotaReadNewerThanWait(model, evidence) && !quotaReadPredatesWait(read, model);
}

// quotaReadIssuedAfterRetirement reports whether the read can end a turn's
// retirement. Both instants are this client's: the boundary's receipt stamp on
// the model, and the read's own issue instant. A read issued before the
// boundary captured a state in which the turn still ran, so its "running"
// verdict is the snapshot's age rather than news that the turn came back.
//
// A model carrying no retirement stamp predates this rule; it keeps the
// shipped revival, which is what lets an already-saved model recover.
function quotaReadIssuedAfterRetirement(
  model: Pick<ThreadModel, "quotaWaitRetiredAt">,
  evidence: QuotaReadEvidence,
): boolean {
  if (model.quotaWaitRetiredAt === undefined) return true;
  // Same convention as quotaReadMayEndWait: a caller that supplies no issue
  // instant has no client-clock evidence to order with, so it keeps the
  // shipped revival. Every production read supplies one, so the ordering binds
  // where it can be checked and nothing is silently refused where it cannot.
  if (evidence.issuedAt === undefined) return true;
  return evidence.issuedAt > model.quotaWaitRetiredAt;
}

// quotaWaitEndedOnModel reports whether the wait the read names by stamp is
// one the pane watched end, or an EARLIER wait of the same turn: the wake
// recorded the last ended wait's stamp independently of the chip, and every
// stamp at or before it belongs to a wait the pane already left. The read's
// deadline for such a wait never installs - not over the ordinary backoff the
// resumed request can carry, not over anything else. A read naming a LATER
// wait (the call hit the limit again) keeps the existing rules.
//
// Ordering rather than equality is what a pane living through two waits of one
// turn needs. A delayed read that captured the FIRST wait can land after both
// wakes; compared only against the second wait's stamp it would look like a
// new wait and restore a countdown for a request that is gone. Every stamp is
// the daemon's own, so the comparison needs no client clock.
function quotaWaitEndedOnModel(
  read: Pick<ThreadModel, "quotaWaitStartedAt">,
  model: Pick<ThreadModel, "quotaWaitEndedAt">,
): boolean {
  if (read.quotaWaitStartedAt === undefined) return false;
  return model.quotaWaitEndedAt !== undefined && read.quotaWaitStartedAt <= model.quotaWaitEndedAt;
}

// quotaWaitWakeRecorded reports whether the model carries the zero-delay
// wake of the wait the read names by stamp: the pane saw the wait end (a
// steer woke the call ahead of its scheduled reset) and a read whose
// snapshot was cut BEFORE the wake still carries the wait's summary. The
// wake is the newer fact about the same stamped wait, so its deadline never
// reinstalls - the pane would resume a countdown over a request already
// out, and the pending deadline would keep later model output from
// clearing the wait.
function quotaWaitWakeRecorded(
  read: Pick<ThreadModel, "quotaWaitStartedAt">,
  model: Pick<ThreadModel, "modelRetry">,
): boolean {
  return (
    model.modelRetry?.errorClass === "quota_exceeded" &&
    model.modelRetry.delayMs === 0 &&
    model.modelRetry.waitStartedAt !== undefined &&
    model.modelRetry.waitStartedAt === read.quotaWaitStartedAt
  );
}

// isDaemonSessionRead names the read's route by the thread's own shape. A
// root's kind is "session" (the hub stamps it on indexed roots) or unset
// with no parentRef; a fork names itself with kind "fork". Both are
// sessions served by their own daemon, whose reads attached the wait
// summary on every revision. A delegate names itself with kind "subagent" -
// the in-process route, whose legacy reads predate the summary entirely.
function isDaemonSessionRead(thread: Pick<ThreadModel, "kind" | "parentRef">): boolean {
  if (thread.kind === "fork") return true;
  return thread.parentRef === undefined && (thread.kind === undefined || thread.kind === "session");
}

export function applyReadResponse<M extends ThreadModel>(
  model: M,
  resp: ThreadReadResponse,
  now: number,
  // The caller's frontier verdict. A read that did not ask for history (the
  // hub omits OlderCursor and Snapshot for includeTurns:false) must pass
  // "unknown": its absent cursor is not a source-named end of history.
  frontier: OlderFrontier = frontierFromCursor(resp),
  // The ordering evidence the quota rules need, all of it this client's own.
  // A caller that does not supply it gets the shipped cut-only ordering.
  evidence: QuotaReadEvidence = {},
): ThreadModel & ModelExtras<M> {
  const held = model.history ?? EMPTY_HISTORY;
  const disposition = readDisposition(held, resp);
  if (disposition === "discard") return publicModel<M>(model);
  const fallback = historyFallback(resp);
  const fields = threadFields(resp, model.ref, now);
  const imageSessionRoute = imageSessionRouteForSession(fields.imageSessionId ?? fields.threadId);
  const fresh = splitWireTurns(resp.thread.turns ?? [], imageSessionRoute);
  let turns: TurnModel[];
  let olderCursor = resp.olderCursor;
  let olderFrontier = frontier;
  if (disposition === "replace") {
    turns = mergeHistory([], fresh);
  } else {
    const window = fragmentRange(fresh.items);
    const observed = historyReplacesWindow(resp) && historyServingFacts(resp)?.servedFrom !== undefined;
    turns = observed
      ? replaceObservedWindow(held, fresh, window?.[0] ?? { entry: 0, item: 0 }, undefined, resp.snapshot?.length ?? 0)
      : historyReplacesWindow(resp)
        ? dropItemsInRange(held.turns, window?.[0] ?? { entry: 0, item: 0 })
        : held.turns;
    // Older pages the client holds keep their own cursor.
    const oldest = earliestPosition(turns);
    if (fallback && olderFrontierOf(model) !== "unknown" && model.olderCursor !== undefined) {
      // A saved fallback window speaks for the saved index, never for the live
      // continuation: a disjoint window it reaches further back with must not
      // replace a live cursor the held history still names.
      olderCursor = model.olderCursor;
      olderFrontier = olderFrontierOf(model);
    } else if (oldest && (!window || comparePositions(oldest, window[0]) < 0)) {
      // Held older pages keep their own cursor only when they recorded a
      // provenance. A seed's unknown frontier is not evidence of a held
      // continuation: keeping it would leave a cursorless recovery read with
      // an unchanged frontier, and older-demand would retry forever. Taking
      // the response's cursor just re-merges rows already held.
      if (olderFrontierOf(model) !== "unknown") {
        olderCursor = model.olderCursor;
        olderFrontier = olderFrontierOf(model);
      }
    }
    if (!observed)
      turns = mergeHistory(
        turns,
        fallback ? withoutNewerPageOmissions(held, fresh, resp.snapshot?.length ?? 0) : fresh,
      );
    if (resp.changes) {
      turns = mergeHistory(turns, wireFragment(resp.changes.turns, resp.changes.items, imageSessionRoute), true);
    }
  }
  const generation = resp.requestGeneration ?? 0;
  const history: HistoryState = {
    ...(historyFallback(resp) ? retainedReadIdentity(undefined, held) : readIdentity(resp)),
    appliedGeneration: generation,
    issuedGeneration: Math.max(held.issuedGeneration, generation),
    deferredPages: [],
    ...(disposition === "merge" && held.replacementScopes ? { replacementScopes: held.replacementScopes } : {}),
    turns,
  };
  const { runningTurnId: _previous, servingFacts: _previousFacts, ...base } = model;
  // The spread keeps any field this read omits. A snapshot that no longer
  // advertises a served root (artifacts, scratch) must clear the previous
  // one, or link actions stay aimed at a directory the hub no longer serves.
  const clearedArtifacts = fields.artifactsDir === undefined ? { artifactsDir: undefined } : {};
  const clearedScratch = fields.scratchDir === undefined ? { scratchDir: undefined } : {};
  // A snapshot from a source that cannot report the quota wait (an older
  // daemon: no quotaWaitSnapshot capability, so evener.failure is absent by
  // design, not authoritatively empty) never clears a known wait - the live
  // announcement is the only wait evidence there is, and a refresh during a
  // multi-hour wait would otherwise erase the countdown and report a stall.
  // Two facts prove a source CAN speak for the wait despite a missing bit:
  // the capability itself, and the model's own quotaReadReportsWaits - set
  // only by a prior READ's failure summary, so its presence means this
  // source's reads already reported a wait once (a base-revision daemon
  // predating the bit). The one further exception is turn ownership: a read
  // that names a DIFFERENT running turn than the wait's is proof the wait's
  // turn ended, on any source - the running turn is the read's own fact, not
  // the quota field's - so the wait and its chip clear together there.
  // A ROOT read on a legacy daemon (no bit) is also authoritative: the base
  // revision's root reads attached the failure summary unconditionally, so
  // their absent field speaks for the wait. A legacy DELEGATE's read is not -
  // the delegate route predates the summary entirely - which is what the
  // thread's own shape names: a root's kind is "session" (the hub stamps it
  // on indexed roots) or unset, a fork names itself with kind fork (its own
  // daemon-served session, same summary rule), a delegate with kind subagent.
  const quotaSnapshotAuthoritative =
    fields.capabilities?.quotaWaitSnapshot === true ||
    base.quotaReadReportsWaits === true ||
    (base.modelRetry?.errorClass === "quota_exceeded" &&
      base.quotaReadReportsWaits === undefined &&
      isDaemonSessionRead(fields));
  const readRunningTurnId = runningTurn(resp.thread).runningTurnId;
  // A read that shows no wait is authoritative that the wait ended: the chip
  // goes with the deadline, or the canceled turn's "waiting for quota" reads
  // over whatever runs next. The wake's own zero-delay announcement already
  // cleared a wait that survived into the same turn; this is the reconnect
  // half, where the snapshot speaks for a turn the client never watched end.
  const waitTurnReplaced =
    base.modelRetry?.errorClass === "quota_exceeded" &&
    base.modelRetry.turnId !== undefined &&
    readRunningTurnId !== undefined &&
    readRunningTurnId !== base.modelRetry.turnId;
  // A retired turn's retirement ends when the read names that turn running
  // again (the remote-relay recovery read) AND the read was issued after the
  // boundary that retired it. Without the ordering leg a read captured while
  // the turn was still waiting - the read in flight when the Stop landed -
  // revives a turn the pane watched end merely by arriving late: Stop emits no
  // zero-delay wake, so there is no ended-wait stamp to reject it, and the
  // read reinstalls the deadline over the stop that canceled it. A fresh
  // recovery read after the relay's synthetic idle is issued after the
  // boundary, so the revival the relay path needs still happens.
  //
  // Decided HERE, before the quota block, because an accepted revival also
  // retracts the boundary's verdict on the wait it brings back: the boundary
  // recorded that wait as ended, and the ended-wait check below must not use
  // that record to reject the very countdown the revival restores.
  const retiredTurnNamedRunning =
    readRunningTurnId !== undefined &&
    readRunningTurnId === model.retiredQuotaTurn &&
    quotaReadIssuedAfterRetirement(model, evidence);
  // A recorded wake suppresses the deadline a pre-wake snapshot carries for
  // the same stamped wait: the read's summary is a stale cut, the wake the
  // newer fact. A read that cannot be ordered against the wait is the same
  // race from the other side: its absent field is stale or unordered silence
  // rather than a statement the wait ended, so it clears neither the deadline
  // nor the retry the live announcement recorded.
  const readMayEndWait = quotaReadMayEndWait(fields, model, evidence, waitTurnReplaced);
  const wakeSuppressesDeadline = quotaWaitWakeRecorded(fields, model);
  const readNamesEndedWait = quotaWaitEndedOnModel(fields, model);
  const clearedQuotaWait =
    (fields.quotaResumesAt === undefined && (quotaSnapshotAuthoritative || waitTurnReplaced) && readMayEndWait) ||
    (fields.quotaResumesAt !== undefined && (wakeSuppressesDeadline || readNamesEndedWait))
      ? { quotaResumesAt: undefined }
      : {};
  // The report-capability evidence is sticky: a source that once reported a
  // wait keeps reporting waits (its base revision attached the summary
  // unconditionally), so a later no-wait read does not unsay it.
  const keptQuotaReadReportsWaits =
    base.quotaReadReportsWaits || fields.quotaReadReportsWaits ? { quotaReadReportsWaits: true } : {};
  const clearedRetiredQuotaTurn = retiredTurnNamedRunning ? { retiredQuotaTurn: undefined } : {};
  // And until the retirement ends, the read's own deadline for that turn never
  // installs. A snapshot captured while the turn still waited carries the
  // wait's summary, and the fields spread would put it back over the Stop that
  // canceled it - the deadline and the retired marker disagreeing about a turn
  // the pane watched end. Re-spreading the held pair (absence included) keeps
  // the Stop's verdict whole; the ordering gate above is what lets a genuinely
  // newer read revive the turn and bring its deadline with it.
  const retiredTurnStillEnded =
    model.retiredQuotaTurn !== undefined && readRunningTurnId === model.retiredQuotaTurn && !retiredTurnNamedRunning;
  const heldRetiredWaitFields: Partial<ThreadModel> = retiredTurnStillEnded
    ? { quotaResumesAt: model.quotaResumesAt, quotaWaitStartedAt: model.quotaWaitStartedAt }
    : {};
  // The retirement protects more than the quota pair. A snapshot captured
  // before the Stop also carries the turn it saw running and the working status
  // that went with it; spreading those over a held idle model puts a stopped
  // session back to work, with no countdown and nothing saying the Stop took.
  // Turn ownership, status and the quota pair are one fact here.
  const heldRetiredTurnFields: Partial<ThreadModel> = retiredTurnStillEnded
    ? { status: model.status, runningTurnId: model.runningTurnId }
    : {};
  // A read that names a DIFFERENT running turn retires the old turn's retry
  // metadata even while the new turn waits its own wait: the snapshot's new
  // deadline is real, but the retained retry (its elapsed time, its model)
  // belongs to the old turn's call and would dress the new wait in the old
  // call's history. The new wait renders from its deadline alone until a
  // matching live announcement arrives.
  // So does a read that names a DIFFERENT WAIT within the same turn: the
  // daemon stamps each wait with the instant it began (WaitStartedAt),
  // shared by the announcement and the snapshot, so a retry whose stamp the
  // read's does not match belongs to a wait that ended - a steer ended the
  // first, the retry hit the limit again - and its elapsed time must not
  // dress the new wait. The stamp, not the reset instant, is the correlation:
  // two waits can share a reset, a stamp is unique to one. Where either side
  // predates the stamp (an older daemon) the moved-deadline comparison is the
  // fallback, gated on the capability as before.
  const quotaRetryFromAnotherCall =
    base.modelRetry?.errorClass === "quota_exceeded" &&
    base.modelRetry.turnId !== undefined &&
    (waitTurnReplaced ||
      quotaWaitStampMoved(fields, model) ||
      (!quotaWaitStampsCorrelate(fields, model) &&
        quotaSnapshotAuthoritative &&
        fields.quotaResumesAt !== model.quotaResumesAt));
  const clearedQuotaRetry =
    readMayEndWait &&
    (quotaSnapshotAuthoritative || waitTurnReplaced) &&
    (fields.quotaResumesAt === undefined || quotaRetryFromAnotherCall) &&
    base.modelRetry?.errorClass === "quota_exceeded"
      ? { modelRetry: undefined }
      : {};
  // A read naming an OLDER wait is a snapshot of a state the pane left: its
  // deadline and stamp must not spread over the newer wait whose retry the
  // merge preserves above. Deadline, stamp and retry move together, so the
  // held wait's pair is re-spread here - after the fields - as one fact.
  //
  // Re-spread UNCONDITIONALLY, absence included. A wake clears the pair while
  // the resumed request runs, so a held wait can legitimately have no deadline
  // at all; skipping the key then let the stale read's deadline install over a
  // wait that had already ended, which is a countdown for a request that is
  // gone. Writing undefined keeps "no wait" a fact rather than a gap the
  // snapshot fills.
  const heldOlderWaitFields: Partial<ThreadModel> = quotaReadNamesOlderWait(fields, model)
    ? { quotaResumesAt: model.quotaResumesAt, quotaWaitStartedAt: model.quotaWaitStartedAt }
    : {};
  let next = withDisplay(
    {
      ...base,
      ...fields,
      ...clearedArtifacts,
      ...clearedScratch,
      ...clearedQuotaWait,
      ...clearedQuotaRetry,
      ...heldOlderWaitFields,
      ...heldRetiredWaitFields,
      ...keptQuotaReadReportsWaits,
      ...clearedRetiredQuotaTurn,
      ...runningTurn(resp.thread),
      ...heldRetiredTurnFields,
      olderCursor,
      olderFrontier,
    },
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
  // The ordering evidence the quota rules need, all of it this client's own.
  // A caller that does not supply it gets the shipped cut-only ordering.
  evidence: QuotaReadEvidence = {},
): ThreadModel & ModelExtras<M> {
  const held = model.history ?? EMPTY_HISTORY;
  const freshHistory = fresh.history;
  const fallback = historyFallback(fresh.servingFacts);
  const disposition = fallback ? "merge" : readModelDisposition(held, freshHistory);
  if (disposition === "discard") return publicModel<M>(model);
  const fragment = turnModelsToFragment(freshHistory?.turns ?? fresh.turns);
  let turns: TurnModel[];
  let olderCursor = fresh.olderCursor;
  let olderFrontier = olderFrontierOf(fresh);
  if (disposition === "replace") {
    turns = mergeHistory([], fragment);
  } else {
    const window = fragmentRange(fragment.items);
    const observed = fresh.servingFacts && historyReplacesWindow(fresh.servingFacts);
    turns = observed
      ? replaceObservedWindow(
          held,
          fragment,
          window?.[0] ?? { entry: 0, item: 0 },
          undefined,
          freshHistory?.length ?? 0,
        )
      : held.turns;
    // Older pages the client holds keep their own cursor.
    const oldest = earliestPosition(turns);
    if (fallback && olderFrontierOf(model) !== "unknown" && model.olderCursor !== undefined) {
      // Same fallback rule as applyReadResponse: the saved window never
      // replaces a live continuation the held history names.
      olderCursor = model.olderCursor;
      olderFrontier = olderFrontierOf(model);
    } else if (oldest && (!window || comparePositions(oldest, window[0]) < 0)) {
      // Same rule as applyReadResponse: only a KNOWN held frontier outlives
      // the read's own.
      if (olderFrontierOf(model) !== "unknown") {
        olderCursor = model.olderCursor;
        olderFrontier = olderFrontierOf(model);
      }
    }
    if (!observed)
      turns = mergeHistory(
        turns,
        fallback ? withoutNewerPageOmissions(held, fragment, freshHistory?.length ?? 0) : fragment,
      );
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
  const identity = fallback ? held : freshHistory;
  const history: HistoryState = {
    // freshHistory undefined (a caller with no snapshot support, or one
    // hydrated on the legacy branch) has no identity of its own to
    // supersede held's with — keep held's exactly, or the next genuinely
    // versioned read reads a real incarnation against a blanked-out one and
    // replaces wholesale, discarding the very history this merge just
    // preserved. Only the four identity fields carry over — never
    // invalidatedAtGeneration/awaited/pendingIncarnation/failed, which this
    // merge (a non-discard disposition) already resolved.
    ...retainedReadIdentity(identity, held),
    appliedGeneration: held.appliedGeneration,
    issuedGeneration: held.issuedGeneration,
    deferredPages: [],
    ...(disposition === "merge" && held.replacementScopes ? { replacementScopes: held.replacementScopes } : {}),
    turns,
  };
  const {
    turns: _freshTurns,
    history: _freshHistoryField,
    overlay: freshOverlay,
    runningTurnId: freshRunningTurnId,
    ...fields
  } = fresh;
  const { runningTurnId: _previous, servingFacts: _previousFacts, ...base } = model;
  // Same rule as applyReadResponse: a freshly hydrated model that carries no
  // served root (artifacts, scratch) must clear the previous one, or the
  // spread keeps it and link actions stay aimed at a directory the snapshot
  // no longer names.
  const clearedArtifacts =
    fresh.artifactsDir === undefined && model.artifactsDir !== undefined ? { artifactsDir: undefined } : {};
  const clearedScratch =
    fresh.scratchDir === undefined && model.scratchDir !== undefined ? { scratchDir: undefined } : {};
  // Same snapshot-support rule as applyReadResponse: a source without
  // quotaWaitSnapshot cannot speak for the wait, so its read never clears
  // one the live announcements recorded - except by the capability, by the
  // model's own quotaReadReportsWaits (only a prior READ's failure summary
  // sets it, so its presence means this source's reads already reported a
  // wait: a base-revision daemon predating the bit), by the daemon-session
  // evidence (a base-revision ROOT or FORK read attached the summary
  // unconditionally; a delegate read names itself with kind subagent), or by
  // turn ownership, the read's own fact: a read naming a DIFFERENT running
  // turn than the wait's is proof the wait's turn ended on any source, and
  // clears wait and chip together.
  const quotaSnapshotAuthoritative =
    fresh.capabilities?.quotaWaitSnapshot === true ||
    base.quotaReadReportsWaits === true ||
    (base.modelRetry?.errorClass === "quota_exceeded" &&
      base.quotaReadReportsWaits === undefined &&
      isDaemonSessionRead(fresh));
  const waitTurnReplaced =
    base.modelRetry?.errorClass === "quota_exceeded" &&
    base.modelRetry.turnId !== undefined &&
    freshRunningTurnId !== undefined &&
    freshRunningTurnId !== base.modelRetry.turnId;
  // Same rule and same ordering as applyReadResponse: the retirement ends only
  // for a read issued after the boundary that made it, and an accepted revival
  // retracts that boundary's verdict on the wait it brings back so the
  // ended-wait check below cannot use it to reject the revived countdown.
  const retiredTurnNamedRunning =
    freshRunningTurnId !== undefined &&
    freshRunningTurnId === model.retiredQuotaTurn &&
    quotaReadIssuedAfterRetirement(model, evidence);
  // Same rule as applyReadResponse: a recorded wake suppresses the deadline a
  // pre-wake snapshot carries for the same stamped wait. The suppression
  // must fire even though the wake already cleared the model's own deadline:
  // the read's fields spread reinstalls the stale cut's, and only this
  // override drops it again. And a read that cannot be ordered against the
  // wait is the same race from the other side: its absent field is stale or
  // unordered silence, so it clears neither the deadline nor the retry the
  // live announcement recorded.
  const readMayEndWait = quotaReadMayEndWait(fresh, model, evidence, waitTurnReplaced);
  const wakeSuppressesDeadline = quotaWaitWakeRecorded(fresh, model);
  const readNamesEndedWait = quotaWaitEndedOnModel(fresh, model);
  const clearedQuotaWait =
    (fresh.quotaResumesAt === undefined &&
      (quotaSnapshotAuthoritative || waitTurnReplaced) &&
      model.quotaResumesAt !== undefined &&
      readMayEndWait) ||
    (fresh.quotaResumesAt !== undefined && (wakeSuppressesDeadline || readNamesEndedWait))
      ? { quotaResumesAt: undefined }
      : {};
  // Same cross-turn, wait-stamp and moved-deadline rules as applyReadResponse:
  // a read naming a different running turn, or a different wait within the
  // same turn, retires the retained retry metadata - the new deadline renders
  // alone until a matching live announcement arrives.
  const quotaRetryFromAnotherCall =
    base.modelRetry?.errorClass === "quota_exceeded" &&
    base.modelRetry.turnId !== undefined &&
    (waitTurnReplaced ||
      quotaWaitStampMoved(fresh, model) ||
      (!quotaWaitStampsCorrelate(fresh, model) &&
        quotaSnapshotAuthoritative &&
        fresh.quotaResumesAt !== model.quotaResumesAt));
  const clearedQuotaRetry =
    readMayEndWait &&
    (quotaSnapshotAuthoritative || waitTurnReplaced) &&
    (fresh.quotaResumesAt === undefined || quotaRetryFromAnotherCall) &&
    model.modelRetry?.errorClass === "quota_exceeded"
      ? { modelRetry: undefined }
      : {};
  // Same rule as applyReadResponse, absence included: an older wait's deadline
  // and stamp do not spread over the newer wait whose retry the merge
  // preserves, and a wait the pane watched end stays ended rather than being
  // restored by a snapshot that captured it.
  const heldOlderWaitFields: Partial<ThreadModel> = quotaReadNamesOlderWait(fresh, model)
    ? { quotaResumesAt: model.quotaResumesAt, quotaWaitStartedAt: model.quotaWaitStartedAt }
    : {};
  // Sticky, as in applyReadResponse: a source that once reported a wait keeps
  // reporting waits, so a later no-wait read does not unsay it.
  const keptQuotaReadReportsWaits =
    base.quotaReadReportsWaits || fresh.quotaReadReportsWaits ? { quotaReadReportsWaits: true } : {};
  const clearedRetiredQuotaTurn = retiredTurnNamedRunning ? { retiredQuotaTurn: undefined } : {};
  // Same rule as applyReadResponse: until the retirement ends, the read's own
  // deadline for that turn never installs. A snapshot captured while the turn
  // still waited carries the wait's summary, and spreading it would put a
  // countdown back over the Stop that canceled it.
  const retiredTurnStillEnded =
    model.retiredQuotaTurn !== undefined && freshRunningTurnId === model.retiredQuotaTurn && !retiredTurnNamedRunning;
  const heldRetiredWaitFields: Partial<ThreadModel> = retiredTurnStillEnded
    ? { quotaResumesAt: model.quotaResumesAt, quotaWaitStartedAt: model.quotaWaitStartedAt }
    : {};
  // Same rule as applyReadResponse: a pre-Stop snapshot's working status and
  // running turn are as stale as its deadline, and spreading them revives a
  // session the pane watched stop.
  const heldRetiredTurnFields: Partial<ThreadModel> = retiredTurnStillEnded
    ? { status: model.status, runningTurnId: model.runningTurnId }
    : {};
  let next = withDisplay(
    {
      ...base,
      ...fields,
      ...clearedArtifacts,
      ...clearedScratch,
      ...clearedQuotaWait,
      ...clearedQuotaRetry,
      ...heldOlderWaitFields,
      ...heldRetiredWaitFields,
      ...keptQuotaReadReportsWaits,
      ...clearedRetiredQuotaTurn,
      ...(freshRunningTurnId ? { runningTurnId: freshRunningTurnId } : {}),
      ...heldRetiredTurnFields,
      olderCursor,
      olderFrontier,
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
  if (historyFallback(resp)) return "merge";
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
      const range = historyReplacesWindow(resp) ? fragmentRange(fresh.items) : undefined;
      const servingFacts = historyServingFacts(resp);
      const snapshot = resp.snapshot?.length ?? held.length;
      const observed = range !== undefined && servingFacts !== undefined;
      const turns = observed
        ? replaceObservedWindow(held, fresh, range[0], range[1], snapshot)
        : mergeHistory(
            range ? dropItemsInRange(held.turns, range[0], range[1]) : held.turns,
            withoutNewerPageOmissions(held, fresh, snapshot),
          );
      // A page that names no successor cursor names the end of older history
      // for the range it serves (#5585).
      const next = {
        ...(takeCursor ? withOlderFrontier(model, resp.nextCursor) : model),
        ...(servingFacts ? { servingFacts } : {}),
      };
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
      return withDisplay(
        next,
        {
          ...held,
          ...(observed
            ? {
                replacementScopes: observePageRange(
                  held.replacementScopes,
                  range[0],
                  range[1],
                  snapshot,
                  servedCutOrdinal(fresh),
                ),
              }
            : {}),
          turns,
        },
        model.overlay ?? {},
      );
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
export function hydrateThread(
  resp: ThreadReadResponse,
  ref: string,
  now: number,
  frontier: OlderFrontier = frontierFromCursor(resp),
): ThreadModel {
  const fields = threadFields(resp, ref, now);
  const olderFrontier = frontier;
  const imageSessionRoute = imageSessionRouteForSession(fields.imageSessionId ?? fields.threadId);
  if (!resp.snapshot) {
    return {
      ...fields,
      olderFrontier,
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
  return withDisplay(
    { ...fields, olderFrontier, turns: [], ...runningTurn(resp.thread) },
    history,
    overlayRecord(resp.overlay),
  );
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
