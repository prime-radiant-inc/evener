// Pure reducer folding AppWire wire shapes into the UI-ready ThreadModel.
// hydrateThread REPLACES the model wholesale (snapshot recovery, e.g. on
// (re)subscribe); applyNotification folds one live notification at a time.
// Every function here is pure: given the same inputs, produces the same
// (possibly reference-equal, for no-op cases) output.

import type { HistoryState, ItemModel, ThreadModel, TurnModel } from "./model";
import {
  buildDisplayTurn,
  classifySignal,
  displayTurnSources,
  EMPTY_HISTORY,
  filterOverlay,
  fragmentRange,
  type HistorySignal,
  historyIsLive,
  invalidated,
  type ModelExtras,
  mergeHistory,
  mergeOlderItemPage,
  modelImageSessionRoute,
  overlayRecord,
  pruneCoveredOverlay,
  publicModel,
  readIdentity,
  runningTurn,
  splitWireTurns,
  wireFragment,
  withDisplay,
} from "./transcriptHistory";
import {
  copyItemTextPresence,
  imageSessionRouteForSession,
  itemIsIdentityOnly,
  itemTextPresence,
  markItemIdentityOnly,
  markItemTextOmitted,
  threadFields,
  upsertStableDelegate,
  wireImageSessionId,
  wireToTurnModel,
} from "./transcriptItems";
import {
  type CachedSessionHistory,
  type CachedSessionItem,
  type CurrentTranscriptFields,
  type CurrentTranscriptState,
  isRecord,
  validCurrentFields,
  validCurrentHistory,
  validItem,
  validOverlayItem,
} from "./transcriptValidators";
import { mergeToolCallsByCallId, mergeTurnHistory } from "./turnHistoryMerge";
import type {
  AnyNotification,
  HistoryUpdatedParams,
  OverlayDeltaParams,
  OverlayItem,
  SandboxEscalationRequested,
  ThreadCapabilities,
  ThreadItemPosition,
  ThreadReadResponse,
  ThreadResyncParams,
  ThreadTurnsListResponse,
} from "./types.gen";
import { foldWarningParams } from "./warningFold";

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

// ---------------------------------------------------------------------------
// Cached session records (web session-history cache spec, "The persist unit").
// A record is the recorded history plus the display fields a pane needs to
// render before the socket answers; nothing live is persisted, and the load
// path resets the transient invalidation fields so a reload starts a clean
// read. The encoder is the write seam's only input; the constructor is the
// load seam's. Both are pure so the mobile store can adopt them later.
// ---------------------------------------------------------------------------

export interface CachedSessionRecord {
  ref: string;
  threadId: string;
  name: string;
  modelProvider: string;
  model: string;
  imageSessionId?: string;
  olderCursor?: string;
  savedAt: number;
  history: CachedSessionHistory;
}

export type CurrentTranscriptAdmission = Pick<
  ThreadModel,
  "capabilities" | "capabilitySource" | "instanceId" | "resumeRequired" | "resumeOnlyFoldable"
>;

const CURRENT_TRANSCRIPT_FIELD_KEYS = [
  "ref",
  "parentRef",
  "threadId",
  "imageSessionId",
  "name",
  "status",
  "modelProvider",
  "model",
  "reasoningEffort",
  "visionModel",
  "askPending",
  "pendingEscalations",
  "activeTurnId",
  "runningTurnId",
  "queue",
  "tasks",
  "diagnostics",
  "delegates",
  "skills",
  "turnSlots",
  "jobsUpdatedAt",
  "jobsTreeRevision",
  "olderCursor",
  "lastFrameAt",
  "modelRetry",
  "goal",
  "humanNote",
  "agentNote",
  "sessionUrls",
  "contextUsed",
  "contextWindow",
  "contextPressure",
  "usage",
  "cost",
  "failedToolCalls",
  "access",
  "workMillis",
  "activeTurnStartedAt",
  "lastTurnEndedAt",
  "reasoningEffortLevels",
  "supportsReasoning",
  "cwd",
  "gitBranch",
  "projectPath",
  "createdAt",
  "updatedAt",
] as const satisfies readonly (keyof CurrentTranscriptFields)[];

function encodeCachedItem(item: ItemModel, includeIdentityOnly = false): CachedSessionItem {
  return {
    ...item,
    ...(item.pendingText === undefined ? {} : { pendingText: Array.from(item.pendingText) }),
    ...(item.reasoningSummaries === undefined
      ? {}
      : { reasoningSummaries: item.reasoningSummaries.map((chunks) => Array.from(chunks)) }),
    ...(itemTextPresence(item) === "omitted" ? { textOmitted: true } : {}),
    ...(includeIdentityOnly && itemIsIdentityOnly(item) ? { identityOnly: true } : {}),
  };
}

function decodeCachedItem(cached: CachedSessionItem): ItemModel {
  const { textOmitted, identityOnly, ...item } = cached;
  const decoded = textOmitted === true ? markItemTextOmitted(item) : copyItemTextPresence(cached, item);
  return identityOnly === true ? markItemIdentityOnly(decoded) : decoded;
}

/** Encodes one recorded item without changing omitted-text or identity-only semantics. */
export function encodeTranscriptItem(item: ItemModel): CachedSessionItem {
  return encodeCachedItem(item, true);
}

/** Decodes one stored item; malformed rows are rejected rather than cast into the model. */
export function decodeTranscriptItem(value: unknown): ItemModel | undefined {
  return validItem(value) ? decodeCachedItem(value) : undefined;
}

export function currentTranscriptState(model: ThreadModel): CurrentTranscriptState | undefined {
  const history = model.history;
  if (history?.incarnation === undefined) return undefined;
  const fields = {} as CurrentTranscriptFields;
  for (const key of CURRENT_TRANSCRIPT_FIELD_KEYS) {
    if (key in model) (fields as Record<string, unknown>)[key] = model[key];
  }
  return {
    version: 1,
    fields,
    history: {
      ...history,
      turns: history.turns.map((turn) => ({ ...turn, items: turn.items.map((item) => encodeCachedItem(item, true)) })),
    },
    overlay: Object.fromEntries(
      Object.entries(model.overlay ?? {}).map(([key, item]) => [key, { ...item, item: { ...item.item } }]),
    ),
  };
}

export function decodeCurrentTranscriptState(value: unknown): CurrentTranscriptState | undefined {
  if (
    !isRecord(value) ||
    value.version !== 1 ||
    !validCurrentFields(value.fields) ||
    !validCurrentHistory(value.history) ||
    !isRecord(value.overlay)
  )
    return undefined;
  const overlay: Record<string, OverlayItem> = {};
  for (const [key, item] of Object.entries(value.overlay)) {
    if (!validOverlayItem(item) || item.key !== key) return undefined;
    overlay[key] = { ...item, item: { ...item.item } };
  }
  const fields: Record<string, unknown> = {};
  for (const key of CURRENT_TRANSCRIPT_FIELD_KEYS) {
    if (key in value.fields) fields[key] = value.fields[key];
  }
  const history = value.history;
  const historyFields: Record<string, unknown> = {};
  for (const key of [
    "bootGeneration",
    "epoch",
    "incarnation",
    "length",
    "appliedGeneration",
    "issuedGeneration",
    "invalidatedAtGeneration",
    "awaited",
    "pendingIncarnation",
    "deferredPages",
    "failed",
  ]) {
    if (key in history) historyFields[key] = (history as unknown as Record<string, unknown>)[key];
  }
  return {
    version: 1,
    fields: fields as CurrentTranscriptFields,
    history: {
      ...(historyFields as Omit<HistoryState, "turns">),
      turns: history.turns.map((turn) => ({ ...turn, items: turn.items.map((item) => encodeCachedItem(item)) })),
    },
    overlay,
  };
}

export function threadModelFromCurrentState(
  state: CurrentTranscriptState,
  admission?: CurrentTranscriptAdmission,
): ThreadModel {
  const history: HistoryState = {
    ...state.history,
    turns: state.history.turns.map((turn) => ({ ...turn, items: turn.items.map(decodeCachedItem) })),
  };
  const model: ThreadModel = {
    ...state.fields,
    turns: [],
    capabilities: { ...EMPTY_CAPABILITIES },
    // The cached field set does not carry the context-assumption flag, and an
    // unrecorded flag is not an assumption (main's own rebuild default).
    contextAssumed: false,
    ...admission,
  };
  return withDisplay(model, history, state.overlay);
}

// The shell's admission state is new, not an existing one: capabilities are
// the empty set (all thirteen required flags false), so every capability-
// gated action refuses until the authoritative read lands.
const EMPTY_CAPABILITIES: ThreadCapabilities = {
  send: false,
  steer: false,
  interrupt: false,
  compact: false,
  clear: false,
  forkFromTurn: false,
  shutdown: false,
  changeModel: false,
  changeVisionModel: false,
  queue: false,
  goal: false,
  sharedNotes: false,
  rename: false,
};

export function threadModelFromCache(record: CachedSessionRecord, now: number): ThreadModel {
  const history: HistoryState = {
    bootGeneration: record.history.bootGeneration,
    epoch: record.history.epoch,
    ...(record.history.incarnation === undefined ? {} : { incarnation: record.history.incarnation }),
    length: record.history.length,
    appliedGeneration: record.history.appliedGeneration,
    issuedGeneration: record.history.issuedGeneration,
    deferredPages: [],
    turns: record.history.turns.map((turn) => ({
      ...turn,
      items: turn.items.map(decodeCachedItem),
    })),
  };
  const base: ThreadModel = {
    ref: record.ref,
    threadId: record.threadId,
    name: record.name,
    status: { type: "" },
    modelProvider: record.modelProvider,
    model: record.model,
    visionModel: "",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    jobsTreeRevision: null,
    ...(record.olderCursor === undefined ? {} : { olderCursor: record.olderCursor }),
    lastFrameAt: now,
    capabilities: EMPTY_CAPABILITIES,
    goal: null,
    humanNote: "",
    agentNote: "",
    sessionUrls: [],
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    contextAssumed: false,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "",
    ...(record.imageSessionId === undefined ? {} : { imageSessionId: record.imageSessionId }),
    history,
  };
  return withDisplay(base, history, {});
}

export function cachedSessionRecord(model: ThreadModel, now: number): CachedSessionRecord | undefined {
  const history = model.history;
  if (history?.incarnation === undefined) return undefined; // only a completed v6 content-bearing read
  return {
    ref: model.ref,
    threadId: model.threadId,
    name: model.name,
    modelProvider: model.modelProvider,
    model: model.model,
    ...(model.imageSessionId === undefined ? {} : { imageSessionId: model.imageSessionId }),
    ...(model.olderCursor === undefined ? {} : { olderCursor: model.olderCursor }),
    savedAt: now,
    history: {
      bootGeneration: history.bootGeneration,
      epoch: history.epoch,
      incarnation: history.incarnation,
      length: history.length,
      appliedGeneration: history.appliedGeneration,
      issuedGeneration: history.issuedGeneration,
      turns: history.turns.map((turn) => ({
        ...turn,
        items: turn.items.map((item) => encodeCachedItem(item)),
      })),
    },
  };
}

// stampThreadImageURLs (output_images.go) prefers the wire session id and
// falls back to the thread id, trimming both (strings.TrimSpace); the
// client-side rebuild matches it exactly — a whitespace-padded session id
// must not win the fallback and escape to a /s/%20.../images route the hub
// would 404 on while the trimmed thread id would have served.
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

// history/updated: the full current form of every recorded item and turn an
// entry changed, merged by version under the one generation state machine.
function applyHistoryUpdated<M extends ThreadModel>(model: M, params: HistoryUpdatedParams, now: number): M {
  const held = model.history ?? EMPTY_HISTORY;
  const live = { ...model, lastFrameAt: now };
  if (held.failed !== undefined) return live;
  const signal: HistorySignal = {
    bootGeneration: params.bootGeneration,
    epoch: params.epoch,
    incarnation: params.snapshot.incarnation,
  };
  const action = classifySignal(held, signal);
  if (action === "invalidate") return { ...live, history: invalidated(held, signal) };
  if (action === "ignore" || held.invalidatedAtGeneration !== undefined) return live;
  const turns = mergeHistory(held.turns, wireFragment(params.turns, params.items, modelImageSessionRoute(model)));
  if (turns === held.turns) return live;
  return withDisplay(live, { ...held, turns }, model.overlay ?? {});
}

// evener/thread/resync: the server bumped the thread's epoch (or a hub pushed
// a resync of its own, which names none). A newer epoch or another generation
// invalidates the thread; the store re-reads its latest window.
function applyResync<M extends ThreadModel>(model: M, params: ThreadResyncParams, now: number): M {
  const held = model.history;
  if (!held) return model;
  const live = { ...model, lastFrameAt: now };
  const signal: HistorySignal = { bootGeneration: params.bootGeneration || undefined, epoch: params.epoch };
  return classifySignal(held, signal) === "invalidate" ? { ...live, history: invalidated(held, signal) } : live;
}

// overlay/delta appends to one stream, preview or tool item. Only the display
// turn showing it is rebuilt. A delta for an item the overlay no longer holds
// (covered, reset or ended) is ignored.
function applyOverlayDelta<M extends ThreadModel>(model: M, params: OverlayDeltaParams, now: number): M {
  const held = model.overlay?.[params.key];
  const live = { ...model, lastFrameAt: now };
  if (!held || !historyIsLive(model.history)) return live;
  const item =
    params.field === "output"
      ? { ...held.item, output: (held.item.output ?? "") + params.delta }
      : { ...held.item, text: (held.item.text ?? "") + params.delta };
  const next: OverlayItem = { ...held, item };
  const overlay = { ...model.overlay, [params.key]: next };
  const index = model.turns.findIndex((turn) => displayTurnSources.get(turn)?.overlay.includes(held));
  const turn = model.turns[index];
  const source = turn && displayTurnSources.get(turn);
  // Every overlay item a display shows is in its turn's source; one that is not
  // shown yet (no turn found) takes the full derivation.
  if (!turn || !source) return withDisplay(live, model.history, overlay);
  const rebuilt = buildDisplayTurn(
    turn.id,
    { ...source, overlay: source.overlay.map((overlayItem) => (overlayItem === held ? next : overlayItem)) },
    modelImageSessionRoute(model),
  );
  return { ...live, overlay, turns: model.turns.map((shown, at) => (at === index ? rebuilt : shown)) };
}

// overlay/upserted, overlay/reset and overlay/end: replace the overlay and
// re-derive the display. Nothing applies while the thread is invalid.
function applyOverlayChange<M extends ThreadModel>(
  model: M,
  now: number,
  change: (overlay: Record<string, OverlayItem>) => Record<string, OverlayItem>,
): M {
  const live = { ...model, lastFrameAt: now };
  if (!historyIsLive(model.history)) return live;
  const current = model.overlay ?? {};
  const overlay = change(current);
  if (overlay === current) return live;
  return withDisplay(live, model.history, overlay);
}

// The fold's own form of resolvePendingEscalation: returns the concrete model,
// which is what applyNotificationToThread composes with. The exported wrapper
// below presents the distributive type.
function resolvePendingEscalationModel<M extends ThreadModel>(model: M, escalationId: string): M {
  if (!model.pendingEscalations.some((e) => e.escalationId === escalationId)) return model;
  return { ...model, pendingEscalations: model.pendingEscalations.filter((e) => e.escalationId !== escalationId) };
}

// Removes one pending escalation by id, returning the same reference when the
// id is absent (the no-op-same-reference idiom used throughout this file). Two
// callers reuse it: the threads store's resolveEscalation action, after this
// client's own successful evener/sandbox/escalation/resolve call, and the
// evener/sandbox/escalation/resolved notification case in applyNotification,
// which fires when another client's resolve — or a turn-interrupt or session
// close — retires the card. So a client merely watching the session drops its
// now-stale copy live off the broadcast instead of waiting for its next
// snapshot.
export function resolvePendingEscalation<M extends ThreadModel>(
  model: M,
  escalationId: string,
): ThreadModel & ModelExtras<M> {
  return publicModel<M>(resolvePendingEscalationModel(model, escalationId));
}

// notificationRoutingKey extracts the identity a frame routes by, from the
// frame's own params alone: ref before threadId, else nothing. It is the ONE
// source of truth for that precedence — notificationTargetsThread answers
// per-model matches from it, and the threads store's routing index keys off
// it directly — so the two layers cannot drift apart.
export type NotificationRoutingKey = { ref: string } | { threadId: string };

export function notificationRoutingKey(n: AnyNotification): NotificationRoutingKey | null {
  const params = n.params as { ref?: unknown; threadId?: unknown };
  if (typeof params.ref === "string") return { ref: params.ref };
  if (typeof params.threadId === "string") return { threadId: params.threadId };
  return null;
}

export function notificationTargetsThread(n: AnyNotification, model: ThreadModel): boolean {
  const key = notificationRoutingKey(n);
  if (!key) return false;
  if ("ref" in key) return key.ref === model.ref;
  return key.threadId === model.threadId;
}

// Replaces the turn identified by turnId with `fn(turn)`; turns not matching
// pass through unchanged (same reference). The whole ARRAY is also unchanged
// (same reference) when turnId names no turn in `turns` at all — every
// caller that needs to tell "the frame changed something" from "the frame
// named a turn outside this window" (a mobile store's gap detection, e.g.)
// reads that from array identity, and Array.prototype.map allocates a new
// array unconditionally, even when every mapped element came back identical.
function mapTurn(turns: TurnModel[], turnId: string, fn: (turn: TurnModel) => TurnModel): TurnModel[] {
  let changed = false;
  const mapped = turns.map((t) => {
    if (t.id !== turnId) return t;
    const next = fn(t);
    if (next !== t) changed = true;
    return next;
  });
  return changed ? mapped : turns;
}

// Appends `incoming` to pendingEscalations, or — if an entry with the same
// escalationId is already present — replaces it in place rather than
// growing the list. Dedup exists because hydration's surface-on-entry
// snapshot (thread.evener.pendingEscalations) and this live notification can
// legitimately race and both deliver the same card; last write wins.
function upsertPendingEscalation(
  escalations: SandboxEscalationRequested[],
  incoming: SandboxEscalationRequested,
): SandboxEscalationRequested[] {
  const idx = escalations.findIndex((e) => e.escalationId === incoming.escalationId);
  if (idx === -1) return [...escalations, incoming];
  return escalations.map((e, i) => (i === idx ? incoming : e));
}

// item/completed kinds that count as the model actually producing something
// (design doc Component 1): assistant message, reasoning, tool call. A
// systemMessage or userMessage item completing (a steer "are you stuck?"
// completing its own systemMessage item, say) arrives mid-grind and must not
// be mistaken for the retry's wait being over.
const MODEL_OUTPUT_ITEM_TYPES = new Set(["agentMessage", "reasoning", "commandExecution"]);

// Folds one live wire notification into model. Most notifications carry
// ref/threadId and are matched via notificationTargetsThread — routing those
// to the right ThreadModel is the caller's job (or not: a mismatch is a safe
// no-op here) either way. turn/completed splits on whether it names the
// model's active turn: the active turn's own settle ends the turn, while any
// other turn's is the no-active-turn announcement path and folds through
// foldNonActiveTurnCompleted instead.
//
// Generic over the model: every case builds its result by spreading `model`
// and overriding only the fields it owns, so a caller's extra fields (native's
// MobileConversation = ThreadModel & { items }, say) survive the fold at
// runtime AND in the return type — the caller needs no cast and no re-spread.
// The return is ThreadModel & ModelExtras<M>, so this holds for extra fields
// while the fields the fold rewrites keep ThreadModel's types.
export function applyNotification<M extends ThreadModel>(
  model: M,
  n: AnyNotification,
  now: number,
): ThreadModel & ModelExtras<M> {
  const next = applyNotificationToThread(model, n, now);
  if (!next.modelRetry || !notificationTargetsThread(n, model)) return publicModel<M>(next);
  // A pending retry is sticky (design doc Component 1): it survives deltas
  // and other mid-grind item completions, clearing only on a turn boundary or
  // the completion of the model's own output item — otherwise a provider
  // grinding through retries looks like the indicator vanished for no reason.
  // No read-model replacement exists for the old turn-boundary clearing rule
  // (a turn boundary carries no notification of its own any more), so only
  // the model-output-item-completed half survives.
  const modelOutputCompleted =
    n.method === "history/updated" &&
    next.history?.turns !== model.history?.turns &&
    (n.params.items ?? []).some((item) => MODEL_OUTPUT_ITEM_TYPES.has(item.type));
  if (!modelOutputCompleted) return publicModel<M>(next);
  const cleared = { ...next };
  delete cleared.modelRetry;
  return publicModel<M>(cleared);
}

/**
 * Ends one round for the entry-backed transcript handoff without dropping
 * overlay slots that the reducer's current history has not recorded yet.
 * Ordinary `applyNotification` keeps its established overlay/end semantics.
 */
export function applyOverlayEndForTranscriptHandoff<M extends ThreadModel>(
  model: M,
  notification: Extract<AnyNotification, { method: "overlay/end" }>,
  now: number,
): ThreadModel & ModelExtras<M> {
  if (!notificationTargetsThread(notification, model)) return publicModel<M>(model);
  const roundId = notification.params.roundId;
  const isEndedRound = (item: OverlayItem) => item.kind !== "notice" && item.roundId === roundId;
  const next = applyOverlayChange(model, now, (overlay) => {
    const remaining = filterOverlay(overlay, isEndedRound);
    if (remaining === overlay) return overlay;
    const ended = filterOverlay(overlay, (item) => !isEndedRound(item));
    return { ...remaining, ...pruneCoveredOverlay(model.history?.turns ?? [], ended) };
  });
  return publicModel<M>(next);
}

// The known thread-scalar notifications: applyNotificationToThread's own case
// for each just restamps one scalar field (and lastFrameAt), never `turns`.
// History/updated, the overlay/* cases (which do touch turns),
// evener/thread/resync (its own clause) and warning (its own drop rule) are
// not named. Everything else, including a forward-compat method this client
// has no case for, is transcript-shaped.
const THREAD_SCALAR_ONLY_METHODS: ReadonlySet<string> = new Set([
  "thread/queueChanged",
  "thread/status/changed",
  "thread/model/changed",
  "thread/reasoning-effort/changed",
  "evener/goal/updated",
  "evener/notes/updated",
  "evener/urls/updated",
  "thread/vision-model/changed",
  "evener/task/updated",
  "evener/thread/name/changed",
  "evener/sandbox/escalation/requested",
  "evener/sandbox/escalation/resolved",
  "evener/job/started",
  "evener/job/finished",
  "evener/delegate/updated",
  "evener/jobs/treeUpdated",
  "evener/thread/modelRetry",
]);

/** Whether a frame may change the thread's `turns`. A denylist on purpose: a
 * method nobody classified counts as transcript-shaped, so a consumer that
 * treats a frame leaving `turns` untouched as a gap errs toward refetching. */
export function notificationTouchesTranscript(method: string): boolean {
  return !THREAD_SCALAR_ONLY_METHODS.has(method);
}

function applyNotificationToThread<M extends ThreadModel>(model: M, n: AnyNotification, now: number): M {
  switch (n.method) {
    case "history/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      return applyHistoryUpdated(model, n.params, now);
    }

    case "evener/thread/resync": {
      if (!notificationTargetsThread(n, model)) return model;
      return applyResync(model, n.params, now);
    }

    case "overlay/upserted": {
      if (!notificationTargetsThread(n, model)) return model;
      const item = n.params.item;
      return applyOverlayChange(model, now, (overlay) => ({ ...overlay, [item.key]: item }));
    }

    case "overlay/delta": {
      if (!notificationTargetsThread(n, model)) return model;
      return applyOverlayDelta(model, n.params, now);
    }

    case "overlay/reset": {
      if (!notificationTargetsThread(n, model)) return model;
      const streamId = n.params.streamId;
      return applyOverlayChange(model, now, (overlay) => filterOverlay(overlay, (item) => item.streamId === streamId));
    }

    case "overlay/end": {
      if (!notificationTargetsThread(n, model)) return model;
      const roundId = n.params.roundId;
      return applyOverlayChange(model, now, (overlay) =>
        filterOverlay(overlay, (item) => item.kind !== "notice" && item.roundId === roundId),
      );
    }

    case "thread/queueChanged": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, queue: n.params.queue, lastFrameAt: now };
    }

    case "thread/status/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      const status = n.params.status;
      // The running turn is the frame's activeTurnId, and none when it names
      // none: an idle frame ends the running turn.
      const { runningTurnId: _previous, ...rest } = model;
      return {
        ...(rest as M),
        ...(n.params.activeTurnId ? { runningTurnId: n.params.activeTurnId } : {}),
        status,
        // The work-clock anchor (activeTurnStartedAt) has no live push to
        // refresh it, so a cold-hydrated live anchor would keep clocking
        // now-minus-anchor forever once the turn ends (StatusRow.tsx feeds it to
        // totalWorkMillis unconditionally). Drop it on any non-active
        // transition so the model never carries a live anchor while at rest.
        activeTurnStartedAt: status.type === "active" ? model.activeTurnStartedAt : undefined,
        // The failure count is otherwise snapshot-only, so a client that
        // attached while the session was clean would keep saying nothing
        // however many failures followed - the watcher the count exists for.
        // A status change is a turn boundary, the only moment it can have
        // moved. Absent here means "no update" (an old daemon omits it), never
        // "nobody counted": clearing it would blank a figure the hydrate
        // legitimately gave us. Absence at HYDRATE is where unknown lives.
        failedToolCalls: n.params.failedToolCalls ?? model.failedToolCalls,
        // askPending is snapshot-authoritative and this is the wire refreshing
        // it, not the reducer deriving it: the hub stamps the flag on the frame
        // that goes with every clear of the pending set (a resolving user turn,
        // an interrupt), so a client stops showing "question waiting" without a
        // reread. Same absent-means-no-update rule as the count above; the ask
        // dock's own in-tool signal is still separate and still not this.
        askPending: n.params.askPending ?? model.askPending,
        // Capabilities are snapshot-only too, and two of them (send, queue)
        // are defined BY this very transition: the hub gates send on "no turn
        // in flight" and queue on "a turn in flight"
        // (server/appwire_thread_projection.go's appCapabilities; steer is harness
        // support alone). A set cut before the
        // turn therefore describes the wrong session by the time the composer
        // reads it back, which is how a running session came to show no Steer,
        // no Stop and a dead Send until the page was reloaded (kata 06t8).
        // Same absent-means-no-update rule as the count above: a source that
        // omits the capability sends none, and clearing on absence would strip
        // the session of every action it advertised.
        capabilities: n.params.capabilities ?? model.capabilities,
        capabilitySource: n.params.capabilities ? "statusFrame" : model.capabilitySource,
        lastFrameAt: now,
      };
    }

    case "thread/model/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      // ThreadModelChangedParams (appwire/types_turn.go) carries
      // reasoningEffortLevels/supportsReasoning alongside modelProvider/
      // model, describing the NEW model's full reasoning profile - not a
      // partial patch, so an omitted/empty ladder on the new payload
      // replaces (not preserves) whatever the old model's picker showed.
      return {
        ...model,
        modelProvider: n.params.modelProvider,
        model: n.params.model,
        reasoningEffortLevels: n.params.reasoningEffortLevels ?? [],
        supportsReasoning: n.params.supportsReasoning ?? false,
        lastFrameAt: now,
      };
    }

    case "thread/reasoning-effort/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, reasoningEffort: n.params.reasoningEffort, lastFrameAt: now };
    }

    case "evener/goal/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, goal: n.params.goal ?? null, lastFrameAt: now };
    }

    case "evener/notes/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, humanNote: n.params.humanNote ?? "", agentNote: n.params.agentNote ?? "", lastFrameAt: now };
    }

    case "evener/urls/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, sessionUrls: n.params.urls ?? [], lastFrameAt: now };
    }

    case "thread/vision-model/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, visionModel: n.params.visionModel, lastFrameAt: now };
    }

    case "evener/task/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      return {
        ...model,
        tasks: {
          total: n.params.total,
          done: n.params.done,
          ...(n.params.cancelled === undefined ? {} : { cancelled: n.params.cancelled }),
          ...(n.params.remaining === undefined
            ? n.params.cancelled === undefined
              ? {}
              : { remaining: Math.max(0, n.params.total - n.params.done - n.params.cancelled) }
            : { remaining: n.params.remaining }),
          ...(n.params.current ? { current: n.params.current } : {}),
        },
        lastFrameAt: now,
      };
    }

    case "evener/thread/name/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, name: n.params.name, lastFrameAt: now };
    }

    // PendingEscalations is THREAD-level human-client state, never a
    // transcript item (appwire/types_thread.go's ThreadEvener.PendingEscalations doc
    // comment: "a HUMAN-CLIENT field only ... never part of the model's
    // transcript or any model-visible projection"). The catalog entry
    // (appwire/protocol.go:185) declares this notification with its real
    // generated payload type (SandboxEscalationRequested), used verbatim
    // here rather than a local "(inline)" interface. See
    // upsertPendingEscalation for the dedup rationale.
    case "evener/sandbox/escalation/requested": {
      if (!notificationTargetsThread(n, model)) return model;
      return {
        ...model,
        pendingEscalations: upsertPendingEscalation(model.pendingEscalations, n.params),
        lastFrameAt: now,
      };
    }

    // The resolved twin of requested (wire-honesty spec Part B): a previously-
    // raised escalation left the pending set — resolved, turn-interrupted, or
    // cleared by session close. The payload carries no outcome (see
    // SandboxEscalationResolved's Go doc); this client clears its own copy by
    // id via the same helper the local resolve action reuses. resolvePending-
    // Escalation is a same-reference no-op for an id we never held, but a
    // targeted live frame still stamps lastFrameAt like every other case here.
    case "evener/sandbox/escalation/resolved": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...resolvePendingEscalationModel(model, n.params.escalationId), lastFrameAt: now };
    }

    // The model holds no job LIST at this layer, so the lifecycle pair leaves
    // its liveness signal (lastFrameAt) and a timestamp the jobs panel watches
    // to know its list went stale — the whole reason the panel need not poll.
    // Both ends bump it: starting and finishing both change what
    // evener/jobs/list returns. Live steering (below) is handled separately:
    // unlike jobs, it becomes a transcript item.
    case "evener/job/started":
    case "evener/job/finished": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, jobsUpdatedAt: now, lastFrameAt: now };
    }

    case "evener/delegate/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      const delegates = upsertStableDelegate(model.delegates ?? [], n.params.delegate);
      if (delegates === model.delegates) return { ...model, jobsUpdatedAt: now, lastFrameAt: now };
      return { ...model, delegates, jobsUpdatedAt: now, lastFrameAt: now };
    }

    case "evener/jobs/treeUpdated": {
      if (!notificationTargetsThread(n, model)) return model;
      if (model.jobsTreeRevision !== null && n.params.revision <= model.jobsTreeRevision) return model;
      return { ...model, jobsTreeRevision: n.params.revision, jobsUpdatedAt: now };
    }

    case "warning": {
      if (!notificationTargetsThread(n, model)) return model;
      const activeTurnId = model.activeTurnId;
      // No active turn: there is nowhere wire-true to put it, and — unlike
      // evener/steering/injected's race window — warnings are not transcript-
      // persisted at all (internal/apptranscript has no warning-item
      // conversion), so the next snapshot would not carry it either. Drop
      // it client-side; only the liveness signal survives.
      // A v6 model shows warnings as overlay notices, and its display turns
      // are derived, so a minted item would vanish at the next derivation. The
      // hub's own relay-attach warning (cmd/evener-hub/app_rpc.go) still comes
      // as this notification and needs a v6 home before Task 20 deletes it.
      // bootGeneration "" (EMPTY_HISTORY's own spelling) means no real read
      // ever stamped one — applyReadModel's caller (a service layer that
      // hydrates outside this package's own snapshot branch) can mint a
      // `.history` of its own for merge continuity alone (applyReadModel's
      // doc comment) without that model ever having gone through a real v6
      // read, so a genuine bootGeneration is what actually distinguishes the
      // two here, not `.history`'s mere presence.
      if (!activeTurnId || model.history?.bootGeneration) return { ...model, lastFrameAt: now };
      const params = n.params;
      const folded = foldWarningParams(params);
      return {
        ...model,
        turns: mapTurn(model.turns, activeTurnId, (turn) => {
          // Same collision-proofing as evener/steering/injected: count what's
          // already there rather than a global counter, so multiple
          // warnings in one turn land with distinct, order-preserving ids.
          const warningCount = turn.items.filter((it) => it.type === "warning").length;
          const item: ItemModel = {
            id: `item_warning_live_${activeTurnId}_${warningCount}`,
            turnId: activeTurnId,
            type: "warning",
            text: folded.text,
            status: "completed",
            warning: { source: folded.source, title: folded.title, hint: folded.hint, code: folded.code },
          };
          return { ...turn, items: [...turn.items, item] };
        }),
        lastFrameAt: now,
      };
    }

    case "evener/thread/modelRetry": {
      if (!notificationTargetsThread(n, model)) return model;
      const params = n.params;
      // No lastFrameAt restamp on purpose — see ThreadModel.modelRetry. The
      // quiet/stall clock is measuring a real silence; this explains it rather
      // than resetting it.
      return {
        ...model,
        modelRetry: {
          attempt: params.attempt,
          maxAttempts: params.maxAttempts,
          delayMs: params.delayMs,
          ...(params.errorClass ? { errorClass: params.errorClass } : {}),
          ...(params.statusCode ? { statusCode: params.statusCode } : {}),
          ...(params.turnId ? { turnId: params.turnId } : {}),
          ...(params.model ? { model: params.model } : {}),
          groupElapsedMs: params.groupElapsedMs,
          attemptCap: params.attemptCap,
          receivedAt: now,
        },
      };
    }

    default:
      return model;
  }
}
