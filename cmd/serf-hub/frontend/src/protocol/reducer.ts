// Pure reducer folding AppWire wire shapes into the UI-ready ThreadModel.
// hydrateThread REPLACES the model wholesale (snapshot recovery, e.g. on
// (re)subscribe); applyNotification folds one live notification at a time.
// Every function here is pure: given the same inputs, produces the same
// (possibly reference-equal, for no-op cases) output.

import type { ItemModel, ThreadModel, TurnModel } from "./model";
import type { AnyNotification, InputItem, OutputImage, Thread, ThreadItem, ThreadReadResponse, ThreadTurnsListResponse, Turn } from "./types.gen";

// The following notification param types are "(inline)" in the AppWire
// catalog (appwire/protocol.go / docs/appwire-protocol.md): the codegen
// leaves them as an empty placeholder interface (e.g. `ItemStartedPayload {}`)
// because their shape only exists in prose, not a named Go struct. These
// local interfaces capture that documented prose shape so the fields can be
// read with the type-checker's help; each is verified against the actual
// notification construction site in internal/appprojector/appwire_projection.go.
interface TurnStartedInline {
  threadId?: string;
  ref?: string;
  turn: Turn;
}

interface ItemLifecycleInline {
  threadId?: string;
  ref?: string;
  turnId?: string;
  item: ThreadItem;
}

function epochMsToISO(ms: number | undefined): string | undefined {
  return ms === undefined ? undefined : new Date(ms).toISOString();
}

// ItemModel.images/outputImages are display-ready string[], but the wire
// carries structured InputItem/OutputImage objects. Take each image's
// renderable handle, preferring url — the field the legacy web client
// (cmd/serf-hub/assets/renderer.js: imagesForUserItem, renderToolOutputImages)
// treats as the <img src> — falling back to path or name.
function imagesToStrings(images: InputItem[] | undefined): string[] | undefined {
  if (!images || images.length === 0) return undefined;
  return images.map((img) => img.url ?? img.path ?? img.name ?? "");
}

function outputImagesToStrings(images: OutputImage[] | undefined): string[] | undefined {
  if (!images || images.length === 0) return undefined;
  return images.map((img) => img.url ?? img.path ?? img.name ?? img.source);
}

// Maps a wire ThreadItem to a settled ItemModel (no pendingText — that only
// exists for an item currently streaming). A reasoning item that already
// carries flattened text (e.g. replayed from a persisted transcript on
// hydrate) is seeded as a single chunk so display-time joining still works;
// live in-flight chunks accumulated via item/reasoning/summaryTextDelta are
// preserved separately by the item/completed and turn/completed handlers
// (mergeReasoning), since they are more complete than this seed.
function wireItemToModel(item: ThreadItem): ItemModel {
  const model: ItemModel = {
    id: item.id,
    turnId: item.turnId ?? "",
    type: item.type,
    text: item.text ?? "",
    toolName: item.toolName,
    callId: item.callId,
    argumentsJSON: item.argumentsJson,
    output: item.output,
    images: imagesToStrings(item.images),
    outputImages: outputImagesToStrings(item.outputImages),
    status: item.status,
    source: item.source,
    startedAt: epochMsToISO(item.startedAt),
    completedAt: epochMsToISO(item.completedAt),
  };
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

function wireToTurnModel(turn: Turn): TurnModel {
  return {
    id: turn.id,
    status: turn.status,
    items: (turn.items ?? []).map(wireItemToModel),
    startedAt: epochMsToISO(turn.startedAt),
    completedAt: epochMsToISO(turn.completedAt),
    durationMs: turn.durationMs,
    usage: turn.usage,
    cost: turn.cost,
    error: turn.error,
  };
}

// serf.activeTurnId is the primary signal; a turn already marked inProgress
// in the snapshot is the fallback for daemons/sources that don't populate it
// (mirrors activeTurnIDFromThread in cmd/serf-hub/assets/appwire.js).
function activeTurnIdFromThread(thread: Thread): string | undefined {
  if (thread.serf.activeTurnId) return thread.serf.activeTurnId;
  return thread.turns?.find((t) => t.status === "inProgress")?.id;
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
    turns: (thread.turns ?? []).map(wireToTurnModel),
    activeTurnId: activeTurnIdFromThread(thread),
    queue: thread.serf.queue,
    tasks: null,
    olderCursor: resp.olderCursor,
    lastFrameAt: now,
  };
}

export function prependOlderTurns(model: ThreadModel, resp: ThreadTurnsListResponse): ThreadModel {
  const older = (resp.data ?? []).map(wireToTurnModel);
  return {
    ...model,
    turns: [...older, ...model.turns],
    olderCursor: resp.nextCursor,
  };
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
function resolveInsertTurnId(model: ThreadModel, turnIdHint: string | undefined, itemTurnId: string | undefined): string | undefined {
  const candidate = turnIdHint ?? itemTurnId ?? model.activeTurnId;
  return candidate !== undefined && model.turns.some((t) => t.id === candidate) ? candidate : undefined;
}

function appendReasoningDelta(item: ItemModel, summaryIndex: number, delta: string): ItemModel {
  const summaries = item.reasoningSummaries ? item.reasoningSummaries.slice() : [];
  while (summaries.length <= summaryIndex) summaries.push([]);
  const chunks = summaries[summaryIndex] ?? [];
  summaries[summaryIndex] = [...chunks, delta];
  return { ...item, reasoningSummaries: summaries };
}

// True for the tool call the daemon tracks as StatusInfo.PendingAsk
// (agent/session_tools_ask.go, appwire/types.go SerfThread.AskPending): the
// built-in ask_user tool, wire-projected like any other tool call
// (type "commandExecution", toolName "ask_user" — confirmed against
// internal/appprojector/appwire_projection.go and internal/apptranscript).
function isAskUserItem(item: ThreadItem): boolean {
  return item.type === "commandExecution" && item.toolName === "ask_user";
}

// Folds one live wire notification into model. Most notifications carry
// ref/threadId and are matched via notificationTargetsThread — routing those
// to the right ThreadModel is the caller's job (or not: a mismatch is a safe
// no-op here) either way. turn/completed is the one exception worth calling
// out: it carries no thread identifier at all, and turn IDs are per-thread
// sequential, so the same turnId legitimately exists on multiple threads at
// once. The store MUST deliver turn/completed only to the model whose
// activeTurnId matches turnId; this function enforces that match
// independently as a second line of defense (see the "turn/completed" case).
export function applyNotification(model: ThreadModel, n: AnyNotification, now: number): ThreadModel {
  switch (n.method) {
    case "turn/started": {
      if (!notificationTargetsThread(n, model)) return model;
      const { turn } = n.params as TurnStartedInline;
      return {
        ...model,
        turns: [...model.turns, wireToTurnModel(turn)],
        activeTurnId: turn.id,
        lastFrameAt: now,
      };
    }

    case "turn/completed": {
      // Routing contract (store-layer requirement): TurnCompletedParams is
      // {turnId, turn} on the wire — it carries neither ref nor threadId
      // (confirmed against appwire/types.go and types.gen.ts), and turn IDs
      // are per-thread sequential ("turn_%d", internal/appprojector), so the
      // SAME turnId routinely exists on more than one thread at once. A
      // multiplexed store MUST NOT broadcast this notification to every
      // subscribed ThreadModel — it must deliver it only to the model whose
      // activeTurnId matches turnId. This reducer independently enforces
      // that as a second line of defense (below), so a store bug or a
      // notification delivered to the wrong model degrades to a same-
      // reference no-op instead of corrupting an unrelated thread's history.
      // One case is genuinely unroutable from the payload alone — two panes
      // simultaneously mid-turn on the exact same numbered turn_N — and is
      // left to correct store-side subscription routing; this gate cannot
      // resolve it because both models would pass the check.
      const params = n.params;
      const turnId = params.turnId || params.turn.id;
      if (model.activeTurnId !== turnId) return model;
      const oldTurn = model.turns.find((t) => t.id === turnId);
      const settledTurn = wireToTurnModel(params.turn);
      settledTurn.items = settledTurn.items.map((item) => mergeReasoning(item, oldTurn?.items.find((old) => old.id === item.id)));
      return {
        ...model,
        turns: model.turns.map((t) => (t.id === turnId ? settledTurn : t)),
        activeTurnId: undefined,
        lastFrameAt: now,
      };
    }

    case "item/started": {
      if (!notificationTargetsThread(n, model)) return model;
      const { turnId, item } = n.params as ItemLifecycleInline;
      const targetTurnId = resolveInsertTurnId(model, turnId, item.turnId);
      if (!targetTurnId) return { ...model, lastFrameAt: now };
      return {
        ...model,
        turns: mapTurn(model.turns, targetTurnId, (turn) => ({ ...turn, items: [...turn.items, wireItemToModel(item)] })),
        askPending: isAskUserItem(item) ? true : model.askPending,
        lastFrameAt: now,
      };
    }

    case "item/completed": {
      if (!notificationTargetsThread(n, model)) return model;
      const { turnId, item } = n.params as ItemLifecycleInline;
      const askPending = isAskUserItem(item) ? false : model.askPending;
      const existingTurnId = findItemTurnId(model, turnId, item.id);
      if (existingTurnId) {
        return {
          ...model,
          turns: mapTurn(model.turns, existingTurnId, (turn) => ({
            ...turn,
            items: mapItem(turn.items, item.id, (old) => mergeReasoning(wireItemToModel(item), old)),
          })),
          askPending,
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
      if (!insertTurnId) return { ...model, lastFrameAt: now };
      return {
        ...model,
        turns: mapTurn(model.turns, insertTurnId, (turn) => ({ ...turn, items: [...turn.items, wireItemToModel(item)] })),
        askPending,
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
          items: mapItem(turn.items, params.itemId, (item) => ({ ...item, pendingText: [...(item.pendingText ?? []), params.delta] })),
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
        turns: mapTurn(model.turns, targetTurnId, (turn) => ({ ...turn, items: turn.items.filter((it) => it.id !== params.itemId) })),
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
          items: mapItem(turn.items, params.itemId, (item) => appendReasoningDelta(item, params.summaryIndex, params.delta)),
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
          items: mapItem(turn.items, params.itemId, (item) => ({ ...item, output: (item.output ?? "") + params.delta })),
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
      return { ...model, status: n.params.status, lastFrameAt: now };
    }

    case "thread/model/changed": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, modelProvider: n.params.modelProvider, model: n.params.model, lastFrameAt: now };
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

    // These carry no ThreadModel-tracked state (no job list, no steering
    // log at this layer) — only their liveness signal (lastFrameAt) applies.
    case "serf/job/started":
    case "serf/job/finished":
    case "serf/steering/injected": {
      if (!notificationTargetsThread(n, model)) return model;
      return { ...model, lastFrameAt: now };
    }

    default:
      return model;
  }
}
