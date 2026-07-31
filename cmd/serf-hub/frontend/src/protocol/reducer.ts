// Pure reducer folding AppWire wire shapes into the UI-ready ThreadModel.
// hydrateThread REPLACES the model wholesale (snapshot recovery, e.g. on
// (re)subscribe); applyNotification folds one live notification at a time.
// Every function here is pure: given the same inputs, produces the same
// (possibly reference-equal, for no-op cases) output.

import { type ItemImage, type ItemModel, SYSTEM_PRELUDE_TURN_ID, type ThreadModel, type TurnModel } from "./model";
import type {
  AnyNotification,
  InputItem,
  OutputImage,
  SandboxEscalationRequested,
  Thread,
  ThreadItem,
  ThreadReadResponse,
  ThreadTurnsListResponse,
  Turn,
} from "./types.gen";

function epochMsToISO(ms: number | undefined): string | undefined {
  // Go's zero value leaks through the wire as 0: a non-positive (or NaN)
  // anchor means "absent", never the 1970 epoch - downstream duration math
  // must never clock against it. statusFormat.ts rejects bad anchors too,
  // as defense-in-depth for its own callers.
  return ms === undefined || Number.isNaN(ms) || ms <= 0 ? undefined : new Date(ms).toISOString();
}

// Thread.createdAt/updatedAt are the wire's only Unix-SECONDS stamps
// (cmd/serf-hub's hubcore.UnixSeconds for a past session,
// appsource/local_daemon.go's entry.StartedAt.Unix() for a live one), unlike
// every millisecond stamp epochMsToISO handles. Same absent-means-absent rule.
function epochSecondsToISO(seconds: number | undefined): string | undefined {
  return epochMsToISO(seconds === undefined || Number.isNaN(seconds) ? seconds : seconds * 1000);
}

// ItemModel.images/outputImages carry a resolved src alongside the wire's own
// name/path/(source) fields (ItemImage, model.ts) rather than collapsing to
// just src. src keeps preferring url — the field the legacy web client
// (cmd/serf-hub/assets/renderer.js: imagesForUserItem, renderToolOutputImages)
// treats as the <img src> — falling back to path or name, exactly as before;
// name/path/source ride alongside it unresolved so a renderer can caption the
// image instead of losing everything but whichever field happened to win
// that fallback (kata byq2).
function imagesToItemImages(images: InputItem[] | undefined): ItemImage[] | undefined {
  if (!images || images.length === 0) return undefined;
  // A composer-attached image reaches the wire as inline bytes (mediaType +
  // data, no url/path — appwire_projection.go's projectUserInputImages), so
  // when no server route is named, the bytes themselves are the src. Falling
  // through to the bare name gave the browser a relative URL that 404s, and
  // ImageGallery drops an unloadable src — no thumbnail at all (kata w53n).
  return images.map((img) => ({
    src: img.url ?? inlineImageSrc(img) ?? img.path ?? img.name ?? "",
    name: img.name,
    path: img.path,
  }));
}

function inlineImageSrc(img: InputItem): string | undefined {
  if (img.data === undefined || img.data === "" || img.mediaType === undefined || img.mediaType === "") {
    return undefined;
  }
  return `data:${img.mediaType};base64,${img.data}`;
}

function outputImagesToItemImages(images: OutputImage[] | undefined): ItemImage[] | undefined {
  if (!images || images.length === 0) return undefined;
  return images.map((img) => ({
    src: img.url ?? img.path ?? img.name ?? img.source,
    name: img.name,
    path: img.path,
    source: img.source,
  }));
}

// Maps a wire ThreadItem to a settled ItemModel (no pendingText — that only
// exists for an item currently streaming). A reasoning item that already
// carries flattened text (e.g. replayed from a persisted transcript on
// hydrate) is seeded as a single chunk so display-time joining still works;
// live in-flight chunks accumulated via item/reasoning/summaryTextDelta are
// preserved separately by the item/completed and turn/completed handlers
// (mergeReasoning), since they are more complete than this seed.
function wireItemToModel(item: ThreadItem): ItemModel {
  const model: ItemModel & { clientMutationId?: string } = {
    id: item.id,
    turnId: item.turnId ?? "",
    type: item.type,
    text: item.text ?? "",
    toolName: item.toolName,
    callId: item.callId,
    argumentsJSON: item.argumentsJson,
    description: item.description,
    eventKind: item.eventKind,
    raw: item.raw,
    output: item.output,
    error: item.error,
    prevalOnly: item.prevalOnly,
    exitCode: item.exitCode,
    images: imagesToItemImages(item.images),
    outputImages: outputImagesToItemImages(item.outputImages),
    status: item.status,
    source: item.source,
    steeringKind: item.steeringKind,
    startedAt: epochMsToISO(item.startedAt),
    completedAt: epochMsToISO(item.completedAt),
  };
  if (item.clientMutationId) model.clientMutationId = item.clientMutationId;
  if (item.type === "reasoning" && item.text) {
    model.reasoningSummaries = [[item.text]];
  }
  return model;
}

// The model "keeps chunks": reasoningSummaries accumulated from
// item/reasoning/summaryTextDelta are never discarded on settlement (only
// joined for display, by the consumer). Wins over whatever wireItemToModel
// seeded from the settled wire item's own (usually empty) text.
function mergeReasoning(settled: ItemModel, existing: ItemModel | undefined): ItemModel {
  if (existing?.reasoningSummaries) {
    return { ...settled, reasoningSummaries: existing.reasoningSummaries };
  }
  return settled;
}

// item/completed's settled wire item never carries observedStartedAt/
// observedCompletedAt — those are model-only client observations (see
// ItemModel's doc comment in model.ts), never present on a wire ThreadItem,
// so wireItemToModel never sets them and a fresh `settled` object has
// already lost whatever appendReasoningDelta stamped. Carries the existing
// item's observedStartedAt forward, and — if observation began but never
// got a completion stamp — stamps observedCompletedAt from `now` (purity:
// only ever from the now argument, never a clock read).
function mergeObservedTiming(settled: ItemModel, existing: ItemModel | undefined, now: number): ItemModel {
  if (existing?.observedStartedAt === undefined) return settled;
  return {
    ...settled,
    observedStartedAt: existing.observedStartedAt,
    observedCompletedAt: existing.observedCompletedAt ?? epochMsToISO(now),
  };
}

// The live tool-settle site drops ArgumentsJSON: EventToolCallEnd
// (internal/appprojector/appwire_projection.go:414-442) resolves it into
// argsJSON at :424-427 but uses that only to derive Description, never
// attaching it to the emitted ThreadItem — so the settled wire item's
// argumentsJson is empty even though the streamed item/started item (:373)
// had it. Historical items don't lose it
// (internal/apptranscript/apptranscript.go:284,312), so this is a
// live-settle-only gap the model corrects: keep the existing item's
// argumentsJSON when the settled payload didn't bring its own. A settled
// payload that DOES carry argumentsJson wins — wire truth over memory.
function mergeArguments(settled: ItemModel, existing: ItemModel | undefined): ItemModel {
  if (settled.argumentsJSON !== undefined) return settled;
  if (existing?.argumentsJSON === undefined) return settled;
  return { ...settled, argumentsJSON: existing.argumentsJSON };
}

// Folds a PRESERVED item (one carried over from before settlement, not
// replaced by wire-authoritative data — see the "turn/completed" case) into
// its settled shape. The live wire's settle stamp carries no items at all,
// so there is no authoritative text to adopt the way item/completed would;
// any pendingText chunks still sitting on the item are joined into text
// exactly as item/completed would eventually have finalized them (mirrors
// item/agentMessage/delta's own chunk accumulation). An item still marked
// inProgress inside a settled turn is stale — a turn cannot complete with
// one of its own items unfinished (e.g. an interrupt or session-end cut a
// stream short before its own item/completed arrived) — so its status is
// promoted to completed. reasoningSummaries pass through untouched (they are
// already the model's own accumulated chunks, not wire data to merge). An
// item still under reasoning-timing observation (observedStartedAt set, no
// observedCompletedAt yet) gets observedCompletedAt stamped from `now` — the
// turn ending is the honest end of observation (see ItemModel's doc comment
// in model.ts).
function settleItem(item: ItemModel, now: number): ItemModel {
  const pending = item.pendingText;
  const stale = item.status === "inProgress";
  const needsObservedCompletion = item.observedStartedAt !== undefined && item.observedCompletedAt === undefined;
  if (pending === undefined && !stale && !needsObservedCompletion) return item;
  return {
    ...item,
    text: pending === undefined ? item.text : item.text + pending.join(""),
    pendingText: undefined,
    status: stale ? "completed" : item.status,
    observedCompletedAt: needsObservedCompletion ? epochMsToISO(now) : item.observedCompletedAt,
  };
}

// The turn-level (non-items) fields wireToTurnModel maps — split out so the
// "turn/completed" bare-stamp path (which has real turn fields but no items
// worth trusting) can reuse the exact same field mapping without also
// pulling in wireToTurnModel's item conversion.
function wireToTurnScalars(turn: Turn): Omit<TurnModel, "items"> {
  return {
    id: turn.id,
    status: turn.status,
    startedAt: epochMsToISO(turn.startedAt),
    completedAt: epochMsToISO(turn.completedAt),
    durationMs: turn.durationMs,
    usage: turn.usage,
    cost: turn.cost,
    error: turn.error,
  };
}

function wireToTurnModel(turn: Turn): TurnModel {
  return { ...wireToTurnScalars(turn), items: (turn.items ?? []).map(wireItemToModel) };
}

// serf.activeTurnId is the primary signal; a turn already marked inProgress
// in the snapshot is the fallback for daemons/sources that don't populate it
// (mirrors activeTurnIDFromThread in cmd/serf-hub/assets/appwire.js).
function activeTurnIdFromThread(thread: Thread): string | undefined {
  if (thread.serf.activeTurnId) return thread.serf.activeTurnId;
  return thread.turns?.find((t) => t.status === "inProgress")?.id;
}

const isToolResultId = (id: string) => id.startsWith("item_tool_result_");
const isToolCallId = (id: string) => id.startsWith("item_tool_") && !isToolResultId(id);

// Reload projects a tool CALL and its RESULT as two items sharing a callId, in
// separate wire turns (apptranscript.TurnsFromFile mints one turn per transcript
// entry). Collapse them the way the live path already produces a single item:
// the call supplies id + argumentsJSON + startedAt, the result supplies output +
// error + exitCode + completedAt + settled status. A turn emptied by the merge is
// dropped so its TurnSeparator does not survive. (zrzr)
function mergeToolCallsByCallId(turns: TurnModel[]): TurnModel[] {
  const resultByCallId = new Map<string, ItemModel>();
  for (const turn of turns) {
    for (const item of turn.items) {
      if (item.callId && isToolResultId(item.id)) resultByCallId.set(item.callId, item);
    }
  }
  if (resultByCallId.size === 0) return turns;

  const merged: TurnModel[] = [];
  for (const turn of turns) {
    const items: ItemModel[] = [];
    for (const item of turn.items) {
      if (item.callId && isToolResultId(item.id)) continue; // folded into its call
      if (item.callId && isToolCallId(item.id)) {
        const result = resultByCallId.get(item.callId);
        if (result) {
          items.push({
            ...item,
            output: result.output,
            error: result.error,
            prevalOnly: result.prevalOnly,
            exitCode: result.exitCode,
            completedAt: result.completedAt,
            status: result.status,
            outputImages: result.outputImages ?? item.outputImages,
          });
          continue;
        }
      }
      items.push(item);
    }
    if (items.length > 0) merged.push({ ...turn, items });
  }
  return merged;
}

export function hydrateThread(resp: ThreadReadResponse, ref: string, now: number): ThreadModel {
  const thread = resp.thread;
  return {
    ref,
    threadId: thread.id,
    name: thread.name ?? "",
    status: thread.status,
    modelProvider: thread.modelProvider,
    // Thread has no separate "model id" field on the wire snapshot — only
    // ModelProvider, which appwire/types.go documents as overloaded to
    // "stay[ing] the model field" for this exact reason. thread/model/changed
    // (below) is what later splits provider and model id apart properly.
    model: thread.modelProvider,
    reasoningEffort: thread.serf.reasoningEffort,
    askPending: thread.serf.askPending ?? false,
    // Go wire-nullable-array rule: omitempty absent means empty, not missing.
    pendingEscalations: thread.serf.pendingEscalations ?? [],
    turns: mergeToolCallsByCallId((thread.turns ?? []).map(wireToTurnModel)),
    activeTurnId: activeTurnIdFromThread(thread),
    queue: thread.serf.queue,
    pendingMutations: thread.serf.pendingMutations ?? [],
    // An absent aggregate means the daemon could not authoritatively read
    // tasks; preserve a present zero so an empty task list stays distinct.
    tasks: thread.serf.tasks ?? null,
    jobsUpdatedAt: null,
    olderCursor: resp.olderCursor,
    lastFrameAt: now,
    capabilities: thread.serf.capabilities,
    goal: thread.serf.goal ?? null,
    contextUsed: thread.serf.contextUsed ?? 0,
    contextWindow: thread.serf.contextWindow ?? 0,
    contextPressure: thread.serf.contextPressure ?? 0,
    usage: thread.serf.usage ?? null,
    cost: thread.serf.cost ?? null,
    // Passed straight through, undefined and all: absent is "nobody counted"
    // and must not become a 0 that reads as "nothing failed".
    failedToolCalls: thread.serf.failedToolCalls,
    workMillis: thread.serf.workMillis ?? 0,
    activeTurnStartedAt: epochMsToISO(thread.serf.activeTurnStartedAt),
    reasoningEffortLevels: thread.serf.reasoningEffortLevels ?? [],
    supportsReasoning: thread.serf.supportsReasoning ?? false,
    cwd: thread.cwd,
    gitBranch: thread.gitInfo?.branch,
    projectPath: thread.projectPath,
    createdAt: epochSecondsToISO(thread.createdAt),
    updatedAt: epochSecondsToISO(thread.updatedAt),
  };
}

export function collectAuthoritativeMutationIds(resp: ThreadReadResponse): Set<string> {
  const identities = new Set<string>();
  for (const pending of resp.thread.serf.pendingMutations ?? []) identities.add(pending.clientMutationId);
  for (const clientMutationId of resp.thread.serf.queue.clientMutationIds ?? []) identities.add(clientMutationId);
  for (const turn of resp.thread.turns ?? []) {
    for (const item of turn.items ?? []) {
      if (item.clientMutationId) identities.add(item.clientMutationId);
    }
  }
  return identities;
}

export function prependOlderTurns(model: ThreadModel, resp: ThreadTurnsListResponse): ThreadModel {
  const older = mergeToolCallsByCallId((resp.data ?? []).map(wireToTurnModel));
  return {
    ...model,
    turns: [...older, ...model.turns],
    olderCursor: resp.nextCursor,
  };
}

// Removes one pending escalation by id, returning the same reference when the
// id is absent (the no-op-same-reference idiom used throughout this file). Two
// callers reuse it: the threads store's resolveEscalation action, after this
// client's own successful serf/sandbox/escalation/resolve call, and the
// serf/sandbox/escalation/resolved notification case in applyNotification,
// which fires when another client's resolve — or a turn-interrupt or session
// close — retires the card. So a client merely watching the session drops its
// now-stale copy live off the broadcast instead of waiting for its next
// snapshot.
export function resolvePendingEscalation(model: ThreadModel, escalationId: string): ThreadModel {
  if (!model.pendingEscalations.some((e) => e.escalationId === escalationId)) return model;
  return { ...model, pendingEscalations: model.pendingEscalations.filter((e) => e.escalationId !== escalationId) };
}

export function notificationTargetsThread(n: AnyNotification, model: ThreadModel): boolean {
  const params = n.params as { ref?: string; threadId?: string };
  if (params.ref !== undefined) return params.ref === model.ref;
  if (params.threadId !== undefined) return params.threadId === model.threadId;
  return false;
}

// Replaces the turn identified by turnId with `fn(turn)`; turns not matching
// pass through unchanged (same reference).
function mapTurn(turns: TurnModel[], turnId: string, fn: (turn: TurnModel) => TurnModel): TurnModel[] {
  return turns.map((t) => (t.id === turnId ? fn(t) : t));
}

// Replaces the item identified by itemId with `fn(item)`; items not matching
// pass through unchanged (same reference).
function mapItem(items: ItemModel[], itemId: string, fn: (item: ItemModel) => ItemModel): ItemModel[] {
  return items.map((it) => (it.id === itemId ? fn(it) : it));
}

// Finds which turn currently holds itemId, preferring the notification's own
// turnId hint, then the model's active turn, then a full scan (defensive —
// in practice the hint and activeTurnId always agree, since only one turn is
// ever in flight at a time).
function findItemTurnId(model: ThreadModel, turnIdHint: string | undefined, itemId: string): string | undefined {
  const turnHasItem = (turn: TurnModel) => turn.items.some((it) => it.id === itemId);
  if (turnIdHint) {
    const turn = model.turns.find((t) => t.id === turnIdHint);
    if (turn && turnHasItem(turn)) return turnIdHint;
  }
  if (model.activeTurnId) {
    const turn = model.turns.find((t) => t.id === model.activeTurnId);
    if (turn && turnHasItem(turn)) return model.activeTurnId;
  }
  return model.turns.find(turnHasItem)?.id;
}

// Resolves which turn a brand-new item belongs to (turnId hint, then the
// item's own turnId, then the model's active turn), verifying that turn
// actually exists in the model.
function resolveInsertTurnId(
  model: ThreadModel,
  turnIdHint: string | undefined,
  itemTurnId: string | undefined,
): string | undefined {
  const candidate = turnIdHint ?? itemTurnId ?? model.activeTurnId;
  return candidate !== undefined && model.turns.some((t) => t.id === candidate) ? candidate : undefined;
}

// Replaces the FIRST turn matching turnId with `settled`, reporting loudly
// when more than one row shares the id. model.turns is presented everywhere
// else as if ids are unique — a duplicate should never happen (see the
// "turn/started" case's comment) — but replacing EVERY entry sharing turnId
// would overwrite an unrelated turn's content with this settle's, silently,
// the exact corruption this reducer must not produce.
function settleFirstMatchingTurn(turns: TurnModel[], turnId: string, settled: TurnModel): TurnModel[] {
  const duplicateCount = turns.reduce((count, t) => (t.id === turnId ? count + 1 : count), 0);
  if (duplicateCount > 1) {
    console.error(
      `applyNotification: turn/completed turnId ${turnId} matches ${duplicateCount} turns in model.turns — settling only the first match (turn-id-uniqueness invariant violated)`,
    );
  }
  let settledFirstMatch = false;
  return turns.map((t) => {
    if (t.id !== turnId || settledFirstMatch) return t;
    settledFirstMatch = true;
    return settled;
  });
}

// Folds a settle stamp's own items into a turn's item list BY ID: an item
// already there is merged exactly the way item/completed merges its settled
// payload (the three helpers read/write disjoint fields off the same `old`),
// a new one is appended. Appending — not replacing, which is what the active
// turn's "full" branch does — is what the announcement path needs: the daemon
// sends one turn/completed per announcement, all naming the same synthetic
// turn, so replacing would leave a startup burst showing only its last line
// where the snapshot shows every one (server/appwire_turns.go's upsertItem).
function upsertTurnItems(items: ItemModel[], incoming: ThreadItem[], now: number): ItemModel[] {
  let next = items;
  for (const wire of incoming) {
    const settled = wireItemToModel(wire);
    const present = next.some((it) => it.id === settled.id);
    next = present
      ? mapItem(next, settled.id, (old) =>
          mergeObservedTiming(mergeArguments(mergeReasoning(settled, old), old), old, now),
        )
      : [...next, settled];
  }
  return next;
}

// Merges a completion stamp onto the turn it names, from an existing turn or
// from nothing when the model has never seen that turn. Only the fields the
// stamp actually carries are taken, mirroring the hub's own snapshot
// reduction (server/appwire_turns.go's NotifyTurnCompleted case): an
// announcement stamp carries id/status/items and nothing else, so a turn's
// already-known startedAt, usage and cost survive it.
function mergeTurnCompletionStamp(
  existing: TurnModel | undefined,
  turnId: string,
  stamp: Turn,
  now: number,
): TurnModel {
  const base: TurnModel = existing ?? { id: turnId, status: "", items: [] };
  return {
    ...base,
    id: turnId,
    status: stamp.status || base.status,
    completedAt: epochMsToISO(stamp.completedAt) ?? base.completedAt,
    durationMs: stamp.durationMs ?? base.durationMs,
    error: stamp.error,
    items: upsertTurnItems(base.items, stamp.items ?? [], now),
  };
}

// The prelude is the one turn whose id fixes its POSITION: it holds content
// from before the session's first real turn by definition, so it belongs at
// the front however late its first frame arrives — nothing orders a session's
// first turn-starting request behind its startup announcements. Every other
// turn is placed where it arrived, which for a between-turns announcement gap
// is after the real turn it followed. Same rule, same reasons, as the hub's
// snapshot reduction (server/appwire_turns.go's ensureTurn).
function placeNewTurn(turns: TurnModel[], turn: TurnModel): TurnModel[] {
  return turn.id === SYSTEM_PRELUDE_TURN_ID ? [turn, ...turns] : [...turns, turn];
}

// Folds a turn/completed that names a turn OTHER than the model's active one.
// That is the daemon's no-active-turn announcement path
// (internal/appprojector/appwire_projection.go's systemAnnouncementItem): a
// session's startup burst, or a burst between two real turns, arrives as one
// turn/completed per announcement, each carrying a single item with itemsView
// "full", all sharing one synthetic turn id — SYSTEM_PRELUDE_TURN_ID before
// the first real turn, a minted "turn_N" gap id after one (kata 9ekv).
//
// activeTurnId and activeTurnStartedAt are deliberately left alone. This
// settle is not the active turn's, and a real turn can be streaming while an
// announcement's frames land, so clearing them here would stop the work clock
// and lose the item-routing anchor for a turn that is still in flight. The
// snapshot reduction clears its own active turn only on an id match, for the
// same reason.
function foldNonActiveTurnCompleted(model: ThreadModel, turnId: string, stamp: Turn, now: number): ThreadModel {
  const existing = model.turns.find((t) => t.id === turnId);
  const settled = mergeTurnCompletionStamp(existing, turnId, stamp, now);
  return {
    ...model,
    turns: existing ? settleFirstMatchingTurn(model.turns, turnId, settled) : placeNewTurn(model.turns, settled),
    lastFrameAt: now,
  };
}

// Accumulates one reasoning delta chunk and, first delta only, stamps
// observedStartedAt as a client observation of when reasoning began (the
// wire carries no reasoning timestamps at all — see ItemModel's doc comment
// in model.ts). `now` is the reducer's own now parameter, never a clock
// read (purity).
function appendReasoningDelta(item: ItemModel, summaryIndex: number, delta: string, now: number): ItemModel {
  const summaries = item.reasoningSummaries ? item.reasoningSummaries.slice() : [];
  while (summaries.length <= summaryIndex) summaries.push([]);
  const chunks = summaries[summaryIndex] ?? [];
  summaries[summaryIndex] = [...chunks, delta];
  return { ...item, reasoningSummaries: summaries, observedStartedAt: item.observedStartedAt ?? epochMsToISO(now) };
}

// Appends `incoming` to pendingEscalations, or — if an entry with the same
// escalationId is already present — replaces it in place rather than
// growing the list. Dedup exists because hydration's surface-on-entry
// snapshot (thread.serf.pendingEscalations) and this live notification can
// legitimately race and both deliver the same card; last write wins.
function upsertPendingEscalation(
  escalations: SandboxEscalationRequested[],
  incoming: SandboxEscalationRequested,
): SandboxEscalationRequested[] {
  const idx = escalations.findIndex((e) => e.escalationId === incoming.escalationId);
  if (idx === -1) return [...escalations, incoming];
  return escalations.map((e, i) => (i === idx ? incoming : e));
}

// Folds one live wire notification into model. Most notifications carry
// ref/threadId and are matched via notificationTargetsThread — routing those
// to the right ThreadModel is the caller's job (or not: a mismatch is a safe
// no-op here) either way. turn/completed splits on whether it names the
// model's active turn: the active turn's own settle ends the turn, while any
// other turn's is the no-active-turn announcement path and folds through
// foldNonActiveTurnCompleted instead.
export function applyNotification(model: ThreadModel, n: AnyNotification, now: number): ThreadModel {
  const next = applyNotificationToThread(model, n, now);
  // A real frame supersedes a pending model-call retry: the model produced
  // something, so the wait the retry described is over. Keyed on lastFrameAt
  // advancing rather than on a list of "real" methods, so a frame-bearing
  // notification added later cannot forget to clear it. serf/thread/modelRetry
  // itself deliberately does not restamp, so it survives its own dispatch.
  if (next.modelRetry && next.lastFrameAt !== model.lastFrameAt) {
    const { modelRetry: _superseded, ...cleared } = next;
    return cleared;
  }
  return next;
}

function applyNotificationToThread(model: ThreadModel, n: AnyNotification, now: number): ThreadModel {
  switch (n.method) {
    case "turn/started": {
      if (!notificationTargetsThread(n, model)) return model;
      const { turn } = n.params;
      // turns is presented everywhere else (mapTurn, findItemTurnId) as if
      // ids are unique. A duplicate here should never happen — the two known
      // ways it could (eptj, bz2z) are both fixed server-side — but blindly
      // appending would grow a second row sharing an id, silently setting up
      // turn/completed's same-id-replaces-both hazard below. Report loudly
      // (a reducer is a bad place to throw) and replace the existing row in
      // place instead of duplicating it.
      const existingIndex = model.turns.findIndex((t) => t.id === turn.id);
      if (existingIndex !== -1) {
        console.error(
          `applyNotification: turn/started turnId ${turn.id} already exists in model.turns — replacing it in place instead of appending a duplicate row (turn-id-uniqueness invariant violated)`,
        );
        return {
          ...model,
          turns: model.turns.map((t, i) => (i === existingIndex ? wireToTurnModel(turn) : t)),
          activeTurnId: turn.id,
          lastFrameAt: now,
        };
      }
      return {
        ...model,
        turns: [...model.turns, wireToTurnModel(turn)],
        activeTurnId: turn.id,
        lastFrameAt: now,
      };
    }

    case "turn/completed": {
      const params = n.params;
      const turnId = params.turnId || params.turn.id;
      if (!notificationTargetsThread(n, model)) return model;
      if (model.activeTurnId !== turnId) return foldNonActiveTurnCompleted(model, turnId, params.turn, now);
      const oldTurn = model.turns.find((t) => t.id === turnId);
      const stamp = params.turn;
      let settledTurn: TurnModel;
      if (stamp.itemsView === "full") {
        settledTurn = wireToTurnModel(stamp);
        // Same three-helper composition as item/completed's existing-item
        // branch below (mergeReasoning/mergeArguments/mergeObservedTiming
        // read/write disjoint fields off the same `old` reference, so
        // composition order is free) — this branch has its own settled
        // items rather than item/completed's single one, so it maps instead
        // of a single mapItem call.
        settledTurn.items = settledTurn.items.map((item) => {
          const old = oldTurn?.items.find((o) => o.id === item.id);
          return mergeObservedTiming(mergeArguments(mergeReasoning(item, old), old), old, now);
        });
      } else {
        // The live wire's settle stamp never carries items — every live
        // settle site (EventUserInput, EventGoalContinuation, EventError,
        // EventSessionEnd in internal/appprojector/appwire_projection.go)
        // emits a bare Turn{ID,Status[,Error]} with Items nil, ItemsView "".
        // itemsView !== "full" means "this payload has nothing to say about
        // items," not "the turn has no items" — keep whatever the model
        // already accumulated via item/started + deltas + item/completed,
        // folding any item still mid-stream through settleItem (a just-
        // settled turn cannot legitimately still have a pending item).
        settledTurn = {
          ...wireToTurnScalars(stamp),
          items: (oldTurn?.items ?? []).map((item) => settleItem(item, now)),
        };
      }
      return {
        ...model,
        turns: settleFirstMatchingTurn(model.turns, turnId, settledTurn),
        activeTurnId: undefined,
        // The active turn just ended: its start anchor is now stale (there is
        // no live push to refresh it), so clear it in lockstep with activeTurnId
        // to stop the work-clock ticking against a completed turn.
        activeTurnStartedAt: undefined,
        lastFrameAt: now,
      };
    }

    case "item/started": {
      if (!notificationTargetsThread(n, model)) return model;
      const { turnId, item } = n.params;
      const targetTurnId = resolveInsertTurnId(model, turnId, item.turnId);
      if (!targetTurnId) return { ...model, lastFrameAt: now };
      return {
        ...model,
        turns: mapTurn(model.turns, targetTurnId, (turn) => ({
          ...turn,
          items: [...turn.items, wireItemToModel(item)],
        })),
        lastFrameAt: now,
      };
    }

    case "item/completed": {
      if (!notificationTargetsThread(n, model)) return model;
      const { turnId, item } = n.params;
      // A live watcher on a long turn sees nothing move on thread/status/
      // changed until the turn ends, however many tool calls fail inside it
      // (kata 895d) — item/completed is the finer-grained carrier, stamped
      // by the server only on the item whose completion actually moved the
      // count. Applied exactly like thread/status/changed's: absent means
      // "no change", never "nobody counted".
      const failedToolCalls = n.params.failedToolCalls ?? model.failedToolCalls;
      const existingTurnId = findItemTurnId(model, turnId, item.id);
      if (existingTurnId) {
        return {
          ...model,
          turns: mapTurn(model.turns, existingTurnId, (turn) => ({
            ...turn,
            items: mapItem(turn.items, item.id, (old) =>
              mergeObservedTiming(mergeArguments(mergeReasoning(wireItemToModel(item), old), old), old, now),
            ),
          })),
          failedToolCalls,
          lastFrameAt: now,
        };
      }
      // Some item types (userMessage, systemMessage) go straight to
      // item/completed with no preceding item/started — see
      // internal/appprojector/appwire_projection.go (EventUserInput,
      // EventGoalContinuation): a new turn opens via turn/started with an
      // empty turn, then item/completed alone carries the item. Insert
      // rather than drop it.
      const insertTurnId = resolveInsertTurnId(model, turnId, item.turnId);
      if (!insertTurnId) return { ...model, failedToolCalls, lastFrameAt: now };
      return {
        ...model,
        turns: mapTurn(model.turns, insertTurnId, (turn) => ({
          ...turn,
          items: [...turn.items, wireItemToModel(item)],
        })),
        failedToolCalls,
        lastFrameAt: now,
      };
    }

    case "item/agentMessage/delta": {
      if (!notificationTargetsThread(n, model)) return model;
      const params = n.params;
      const targetTurnId = findItemTurnId(model, params.turnId, params.itemId);
      if (!targetTurnId) return { ...model, lastFrameAt: now };
      return {
        ...model,
        turns: mapTurn(model.turns, targetTurnId, (turn) => ({
          ...turn,
          items: mapItem(turn.items, params.itemId, (item) => ({
            ...item,
            pendingText: [...(item.pendingText ?? []), params.delta],
          })),
        })),
        lastFrameAt: now,
      };
    }

    case "item/agentMessage/reset": {
      if (!notificationTargetsThread(n, model)) return model;
      const params = n.params;
      const targetTurnId = findItemTurnId(model, params.turnId, params.itemId);
      if (!targetTurnId) return { ...model, lastFrameAt: now };
      return {
        ...model,
        turns: mapTurn(model.turns, targetTurnId, (turn) => ({
          ...turn,
          items: turn.items.filter((it) => it.id !== params.itemId),
        })),
        lastFrameAt: now,
      };
    }

    case "item/reasoning/summaryTextDelta": {
      if (!notificationTargetsThread(n, model)) return model;
      const params = n.params;
      const targetTurnId = findItemTurnId(model, params.turnId, params.itemId);
      if (!targetTurnId) return { ...model, lastFrameAt: now };
      return {
        ...model,
        turns: mapTurn(model.turns, targetTurnId, (turn) => ({
          ...turn,
          items: mapItem(turn.items, params.itemId, (item) =>
            appendReasoningDelta(item, params.summaryIndex, params.delta, now),
          ),
        })),
        lastFrameAt: now,
      };
    }

    case "item/toolOutput/delta": {
      if (!notificationTargetsThread(n, model)) return model;
      const params = n.params;
      const targetTurnId = findItemTurnId(model, params.turnId, params.itemId);
      if (!targetTurnId) return { ...model, lastFrameAt: now };
      return {
        ...model,
        turns: mapTurn(model.turns, targetTurnId, (turn) => ({
          ...turn,
          items: mapItem(turn.items, params.itemId, (item) => ({
            ...item,
            output: (item.output ?? "") + params.delta,
          })),
        })),
        lastFrameAt: now,
      };
    }

    case "thread/queueChanged": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, queue: n.params.queue, lastFrameAt: now };
    }

    case "thread/status/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      const status = n.params.status;
      return {
        ...model,
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
        // Capabilities are snapshot-only too, and three of them (send, steer,
        // queue) are defined BY this very transition: the hub gates send on
        // "no turn in flight" and steer/queue on "a turn in flight"
        // (server/appwire_runtime.go's appCapabilities). A set cut before the
        // turn therefore describes the wrong session by the time the composer
        // reads it back, which is how a running session came to show no Steer,
        // no Stop and a dead Send until the page was reloaded (kata 06t8).
        // Same absent-means-no-update rule as the count above: a source that
        // state-gates nothing (the Codex bridge) sends none, and clearing on
        // absence would strip the session of every action it advertised.
        capabilities: n.params.capabilities ?? model.capabilities,
        lastFrameAt: now,
      };
    }

    case "thread/model/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      // ThreadModelChangedParams (appwire/types.go:867-874) carries
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

    case "serf/task/updated": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, tasks: { total: n.params.total, done: n.params.done }, lastFrameAt: now };
    }

    case "serf/thread/name/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, name: n.params.name, lastFrameAt: now };
    }

    // PendingEscalations is THREAD-level human-client state, never a
    // transcript item (appwire/types.go's ThreadSerf.PendingEscalations doc
    // comment: "a HUMAN-CLIENT field only ... never part of the model's
    // transcript or any model-visible projection"). The catalog entry
    // (appwire/protocol.go:185) declares this notification with its real
    // generated payload type (SandboxEscalationRequested), used verbatim
    // here rather than a local "(inline)" interface. See
    // upsertPendingEscalation for the dedup rationale.
    case "serf/sandbox/escalation/requested": {
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
    case "serf/sandbox/escalation/resolved": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...resolvePendingEscalation(model, n.params.escalationId), lastFrameAt: now };
    }

    // The model holds no job LIST at this layer, so the lifecycle pair leaves
    // its liveness signal (lastFrameAt) and a timestamp the jobs panel watches
    // to know its list went stale — the whole reason the panel need not poll.
    // Both ends bump it: starting and finishing both change what
    // serf/jobs/list returns. Live steering (below) is handled separately:
    // unlike jobs, it becomes a transcript item.
    case "serf/job/started":
    case "serf/job/finished": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, jobsUpdatedAt: now, lastFrameAt: now };
    }

    case "warning": {
      if (!notificationTargetsThread(n, model)) return model;
      const activeTurnId = model.activeTurnId;
      // No active turn: there is nowhere wire-true to put it, and — unlike
      // serf/steering/injected's race window — warnings are not transcript-
      // persisted at all (internal/apptranscript has no warning-item
      // conversion), so the next snapshot would not carry it either. Drop
      // it client-side; only the liveness signal survives.
      if (!activeTurnId) return { ...model, lastFrameAt: now };
      const params = n.params;
      return {
        ...model,
        turns: mapTurn(model.turns, activeTurnId, (turn) => {
          // Same collision-proofing as serf/steering/injected: count what's
          // already there rather than a global counter, so multiple
          // warnings in one turn land with distinct, order-preserving ids.
          const warningCount = turn.items.filter((it) => it.type === "warning").length;
          const item: ItemModel = {
            id: `item_warning_live_${activeTurnId}_${warningCount}`,
            turnId: activeTurnId,
            type: "warning",
            text: params.message ?? "",
            status: "completed",
            warning: { source: params.source, title: params.title, hint: params.hint },
          };
          return { ...turn, items: [...turn.items, item] };
        }),
        lastFrameAt: now,
      };
    }

    case "serf/thread/modelRetry": {
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
        },
      };
    }

    case "serf/steering/injected": {
      if (!notificationTargetsThread(n, model)) return model;
      const activeTurnId = model.activeTurnId;
      // The server only injects steering into an in-flight turn; if the
      // model has none (e.g. this arrived just after the turn's own settle),
      // there is nowhere wire-true to put it — a race recovered by the next
      // snapshot, not a turn to fabricate client-side.
      if (!activeTurnId) return { ...model, lastFrameAt: now };
      const params = n.params;
      return {
        ...model,
        turns: mapTurn(model.turns, activeTurnId, (turn) => {
          // id must be unique across multiple steers landing in the same
          // turn; count what's already there rather than a global counter,
          // mirroring the historical reload shape's per-turn indexing
          // (internal/apptranscript/apptranscript.go:211-229, item_steering_<n>).
          const steeringCount = turn.items.filter((it) => it.type === "steering").length;
          const item: ItemModel & { clientMutationId?: string } = {
            id: `item_steering_live_${activeTurnId}_${steeringCount}`,
            turnId: activeTurnId,
            type: "steering",
            text: params.text ?? "",
            images: imagesToItemImages(params.images),
            status: "completed",
            source: params.source,
            steeringKind: params.kind,
          };
          if (params.clientMutationId) item.clientMutationId = params.clientMutationId;
          return { ...turn, items: [...turn.items, item] };
        }),
        lastFrameAt: now,
      };
    }

    default:
      return model;
  }
}
