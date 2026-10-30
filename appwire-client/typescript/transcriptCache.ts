// Cached session records and the current-transcript checkpoint: encode and
// decode between ThreadModel and its persisted form. Imports the validators,
// `transcriptHistory` and `transcriptItems`.

import type { HistoryState, ItemModel, ThreadModel } from "./model";
import { withDisplay } from "./transcriptHistory";
import {
  copyItemTextPresence,
  itemIsIdentityOnly,
  itemTextPresence,
  markItemIdentityOnly,
  markItemTextOmitted,
  normalizeAuthorship,
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
import type { OverlayItem, ThreadCapabilities } from "./types.gen";

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
  if (item.authorship !== undefined) item.authorship = normalizeAuthorship(item.authorship);
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
