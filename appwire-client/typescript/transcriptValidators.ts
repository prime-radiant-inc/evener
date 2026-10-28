// Shape validators for persisted transcript state: they read only `unknown`
// and generated wire types, so a stored record is never cast into the model
// unchecked.

import type { HistoryState, ItemModel, ThreadModel, TurnModel } from "./model";
import type { OverlayItem, ThreadItem, ThreadItemPosition } from "./types.gen";
import { THREAD_ITEM_EVENT_KINDS, WIRE_FIELD_KEYS } from "./types.gen";

export interface CachedSessionItem extends ItemModel {
  // JSON drops ITEM_TEXT_PRESENCE. Only omitted text needs an enumerable
  // marker; legacy records without it keep their provided-text semantics.
  textOmitted?: true;
  // Identity-only is a fold input property, not visible item data. Checkpoints
  // persist it explicitly because a JSON round trip loses the private symbol.
  identityOnly?: true;
}

export interface CachedSessionTurn extends TurnModel {
  items: CachedSessionItem[];
}

export interface CachedSessionHistory {
  bootGeneration: string;
  epoch: number;
  incarnation?: string;
  length: number;
  appliedGeneration: number;
  issuedGeneration: number;
  turns: CachedSessionTurn[];
}

export type CurrentTranscriptFields = Pick<
  ThreadModel,
  | "ref"
  | "parentRef"
  | "threadId"
  | "imageSessionId"
  | "name"
  | "status"
  | "modelProvider"
  | "model"
  | "reasoningEffort"
  | "visionModel"
  | "askPending"
  | "pendingEscalations"
  | "activeTurnId"
  | "runningTurnId"
  | "queue"
  | "tasks"
  | "diagnostics"
  | "delegates"
  | "skills"
  | "turnSlots"
  | "jobsUpdatedAt"
  | "jobsTreeRevision"
  | "olderCursor"
  | "lastFrameAt"
  | "modelRetry"
  | "goal"
  | "humanNote"
  | "agentNote"
  | "sessionUrls"
  | "contextUsed"
  | "contextWindow"
  | "contextPressure"
  | "usage"
  | "cost"
  | "failedToolCalls"
  | "access"
  | "workMillis"
  | "activeTurnStartedAt"
  | "lastTurnEndedAt"
  | "reasoningEffortLevels"
  | "supportsReasoning"
  | "cwd"
  | "gitBranch"
  | "projectPath"
  | "createdAt"
  | "updatedAt"
>;

export interface CurrentTranscriptState {
  version: 1;
  fields: CurrentTranscriptFields;
  history: Omit<HistoryState, "turns"> & Pick<CachedSessionHistory, "turns">;
  overlay: Record<string, OverlayItem>;
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value);
}

function isNonNegativeInteger(value: unknown): value is number {
  return isFiniteNumber(value) && value >= 0 && Number.isInteger(value);
}

function validPosition(value: unknown): value is ThreadItemPosition {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, WIRE_FIELD_KEYS.ThreadItemPosition) &&
    isNonNegativeInteger(value.entry) &&
    isNonNegativeInteger(value.item) &&
    (value.sub === undefined || isNonNegativeInteger(value.sub))
  );
}

const ITEM_MODEL_KEYS = new Set([
  "authorship",
  "id",
  "turnId",
  "transcriptKey",
  "position",
  "transcriptEntryIndex",
  "clientMutationId",
  "type",
  "text",
  "pendingText",
  "toolName",
  "callId",
  "argumentsJSON",
  "description",
  "eventKind",
  "steeringKind",
  "raw",
  "output",
  "error",
  "prevalOnly",
  "exitCode",
  "images",
  "outputImages",
  "status",
  "source",
  "reasoningSummaries",
  "startedAt",
  "completedAt",
  "observedStartedAt",
  "observedCompletedAt",
  "warning",
  "version",
  "roundId",
  "completedAtEntry",
  "overlayKey",
  "textOmitted",
  "identityOnly",
]);

export function validItem(value: unknown): value is CachedSessionItem {
  if (!isRecord(value) || Object.keys(value).some((key) => !ITEM_MODEL_KEYS.has(key))) return false;
  if (
    typeof value.id !== "string" ||
    typeof value.turnId !== "string" ||
    typeof value.type !== "string" ||
    typeof value.text !== "string"
  )
    return false;
  if (value.position !== undefined && !validPosition(value.position)) return false;
  for (const key of [
    "transcriptKey",
    "clientMutationId",
    "toolName",
    "callId",
    "argumentsJSON",
    "description",
    "eventKind",
    "steeringKind",
    "output",
    "error",
    "status",
    "source",
    "startedAt",
    "completedAt",
    "observedStartedAt",
    "observedCompletedAt",
    "roundId",
    "overlayKey",
  ]) {
    if (value[key] !== undefined && typeof value[key] !== "string") return false;
  }
  for (const key of ["transcriptEntryIndex", "exitCode", "version", "completedAtEntry"]) {
    if (value[key] !== undefined && !isFiniteNumber(value[key])) return false;
  }
  if (value.prevalOnly !== undefined && typeof value.prevalOnly !== "boolean") return false;
  if (
    value.warning !== undefined &&
    (!isRecord(value.warning) ||
      !hasOnlyKeys(value.warning, ["source", "title", "hint", "code"]) ||
      ["source", "title", "hint", "code"].some(
        (key) =>
          value.warning &&
          (value.warning as Record<string, unknown>)[key] !== undefined &&
          typeof (value.warning as Record<string, unknown>)[key] !== "string",
      ))
  )
    return false;
  const validImages = (images: unknown): boolean =>
    Array.isArray(images) &&
    images.every(
      (image) =>
        isRecord(image) &&
        hasOnlyKeys(image, ["src", "name", "path", "source"]) &&
        typeof image.src === "string" &&
        ["name", "path", "source"].every((key) => image[key] === undefined || typeof image[key] === "string"),
    );
  if (value.images !== undefined && !validImages(value.images)) return false;
  if (value.outputImages !== undefined && !validImages(value.outputImages)) return false;
  if (
    value.pendingText !== undefined &&
    (!Array.isArray(value.pendingText) || !value.pendingText.every((part) => typeof part === "string"))
  )
    return false;
  if (
    value.reasoningSummaries !== undefined &&
    (!Array.isArray(value.reasoningSummaries) ||
      !value.reasoningSummaries.every(
        (parts) => Array.isArray(parts) && parts.every((part) => typeof part === "string"),
      ))
  )
    return false;
  if (value.textOmitted !== undefined && value.textOmitted !== true) return false;
  if (value.identityOnly !== undefined && value.identityOnly !== true) return false;
  return true;
}

function validTurn(value: unknown): value is CachedSessionTurn {
  const keys = new Set([
    "id",
    "status",
    "items",
    "startedAt",
    "completedAt",
    "durationMs",
    "usage",
    "cost",
    "error",
    "version",
  ]);
  return (
    isRecord(value) &&
    Object.keys(value).every((key) => keys.has(key)) &&
    typeof value.id === "string" &&
    typeof value.status === "string" &&
    Array.isArray(value.items) &&
    value.items.every(validItem) &&
    (value.startedAt === undefined || typeof value.startedAt === "string") &&
    (value.completedAt === undefined || typeof value.completedAt === "string") &&
    (value.durationMs === undefined || (isFiniteNumber(value.durationMs) && value.durationMs >= 0)) &&
    (value.usage === undefined || validUsage(value.usage)) &&
    (value.cost === undefined || value.cost === null || typeof value.cost === "string") &&
    (value.error === undefined || value.error === null || validTurnError(value.error)) &&
    (value.version === undefined || isNonNegativeInteger(value.version))
  );
}

function validTurnError(value: unknown): boolean {
  if (!isRecord(value)) return false;
  if (!hasOnlyKeys(value, WIRE_FIELD_KEYS.TurnError) || typeof value.message !== "string") return false;
  return (
    ["additionalDetails", "source", "title", "hint"].every(
      (key) => value[key] === undefined || typeof value[key] === "string",
    ) &&
    (value.cause === undefined || validDiagnosticCause(value.cause))
  );
}

function validDiagnosticCause(value: unknown): boolean {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, WIRE_FIELD_KEYS.DiagnosticCause) &&
    typeof value.kind === "string" &&
    ["provider", "model"].every((key) => value[key] === undefined || typeof value[key] === "string") &&
    (value.status === undefined || isFiniteNumber(value.status))
  );
}

function hasOnlyKeys(value: Record<string, unknown>, allowed: readonly string[]): boolean {
  return Object.keys(value).every((key) => allowed.includes(key));
}

function validWireItem(value: unknown): value is ThreadItem {
  if (
    !isRecord(value) ||
    !hasOnlyKeys(value, WIRE_FIELD_KEYS.ThreadItem) ||
    typeof value.type !== "string" ||
    typeof value.id !== "string"
  )
    return false;
  for (const key of [
    "transcriptKey",
    "turnId",
    "text",
    "delta",
    "toolName",
    "callId",
    "argumentsJson",
    "description",
    "output",
    "error",
    "status",
    "source",
    "steeringKind",
    "clientMutationId",
    "roundId",
  ]) {
    if (value[key] !== undefined && typeof value[key] !== "string") return false;
  }
  if (
    value.eventKind !== undefined &&
    !THREAD_ITEM_EVENT_KINDS.includes(value.eventKind as (typeof THREAD_ITEM_EVENT_KINDS)[number])
  )
    return false;
  if (value.position !== undefined && !validPosition(value.position)) return false;
  for (const key of [
    "transcriptEntryIndex",
    "startedAt",
    "completedAt",
    "durationMs",
    "exitCode",
    "version",
    "completedAtEntry",
  ]) {
    if (value[key] !== undefined && !isFiniteNumber(value[key])) return false;
  }
  if (value.prevalOnly !== undefined && typeof value.prevalOnly !== "boolean") return false;
  const validInputImage = (image: unknown): boolean => {
    if (!isRecord(image) || !hasOnlyKeys(image, WIRE_FIELD_KEYS.InputItem)) return false;
    if (typeof image.type !== "string") return false;
    for (const key of ["text", "url", "mediaType", "data", "name", "path"]) {
      if (image[key] !== undefined && typeof image[key] !== "string") return false;
    }
    if (
      image.metadata !== undefined &&
      (!isRecord(image.metadata) || Object.values(image.metadata).some((entry) => typeof entry !== "string"))
    )
      return false;
    return (
      image.mentions === undefined ||
      (Array.isArray(image.mentions) &&
        image.mentions.every(
          (mention) =>
            isRecord(mention) &&
            hasOnlyKeys(mention, WIRE_FIELD_KEYS.InputMention) &&
            typeof mention.kind === "string" &&
            typeof mention.name === "string" &&
            isNonNegativeInteger(mention.offset),
        ))
    );
  };
  const validOutputImage = (image: unknown): boolean =>
    isRecord(image) &&
    hasOnlyKeys(image, WIRE_FIELD_KEYS.OutputImage) &&
    typeof image.source === "string" &&
    ["name", "mediaType", "url", "sha", "path"].every(
      (key) => image[key] === undefined || typeof image[key] === "string",
    ) &&
    (image.size === undefined || isNonNegativeInteger(image.size));
  if (value.images !== undefined && (!Array.isArray(value.images) || !value.images.every(validInputImage)))
    return false;
  if (
    value.outputImages !== undefined &&
    (!Array.isArray(value.outputImages) || !value.outputImages.every(validOutputImage))
  )
    return false;
  return true;
}

function validQueue(value: unknown): boolean {
  if (!isRecord(value) || !hasOnlyKeys(value, WIRE_FIELD_KEYS.QueueState)) return false;
  if (!isNonNegativeInteger(value.revision)) return false;
  if (value.depth !== undefined && !isNonNegativeInteger(value.depth)) return false;
  for (const key of ["preview", "ids", "clientMutationIds", "texts"]) {
    if (
      value[key] !== undefined &&
      (!Array.isArray(value[key]) || value[key].some((entry) => typeof entry !== "string"))
    )
      return false;
  }
  for (const key of ["skillNames", "commandNames"]) {
    if (
      value[key] !== undefined &&
      (!Array.isArray(value[key]) ||
        value[key].some((list) => !Array.isArray(list) || list.some((entry) => typeof entry !== "string")))
    )
      return false;
  }
  if (
    value.mentions !== undefined &&
    (!Array.isArray(value.mentions) ||
      value.mentions.some(
        (list) =>
          !Array.isArray(list) ||
          list.some(
            (mention) =>
              !isRecord(mention) ||
              !hasOnlyKeys(mention, WIRE_FIELD_KEYS.InputMention) ||
              typeof mention.kind !== "string" ||
              typeof mention.name !== "string" ||
              !isNonNegativeInteger(mention.offset),
          ),
      ))
  )
    return false;
  return true;
}

function validUsage(value: unknown): boolean {
  return (
    isRecord(value) && hasOnlyKeys(value, WIRE_FIELD_KEYS.EvenerUsage) && Object.values(value).every(isFiniteNumber)
  );
}

function validStableDelegate(value: unknown): boolean {
  const stringFields = [
    "logicalOwnerSessionId",
    "parentDelegateId",
    "outcome",
    "reason",
    "error",
    "notResumableReason",
    "reportPreview",
    "task",
    "description",
    "agentType",
    "requestedModel",
    "resolvedProfileId",
    "resolvedModel",
    "model",
    "reasoningEffort",
    "originTurnId",
    "originToolCallId",
    "originItemId",
    "runStartedAt",
    "runEndedAt",
    "latestActivityAt",
    "latestIntent",
    "packetKind",
    "structuredResultReason",
    "exhaustionBudget",
  ];
  const booleanFields = [
    "terminal",
    "reportPreviewTruncated",
    "structuredResultValid",
    "exhaustionResumable",
    "parentWatchGranted",
  ];
  const numberFields = ["runningForMs", "quietForMs", "durationMs", "exhaustionLimit", "delegationAllowance"];
  return (
    isRecord(value) &&
    hasOnlyKeys(value, WIRE_FIELD_KEYS.EvenerDelegateInfo) &&
    isNonNegativeInteger(value.runGeneration) &&
    isNonNegativeInteger(value.projectionRevision) &&
    [
      "delegateId",
      "ownerSessionId",
      "rootSessionId",
      "childSessionId",
      "transcriptRef",
      "type",
      "lifecycle",
      "phase",
      "status",
    ].every((key) => typeof value[key] === "string") &&
    ["resumable", "needsAttention"].every((key) => typeof value[key] === "boolean") &&
    stringFields.every((key) => value[key] === undefined || typeof value[key] === "string") &&
    booleanFields.every((key) => value[key] === undefined || typeof value[key] === "boolean") &&
    numberFields.every((key) => value[key] === undefined || isFiniteNumber(value[key])) &&
    (value.warnings === undefined || validStringArray(value.warnings)) &&
    (value.diagnostics === undefined || validStringArray(value.diagnostics)) &&
    (value.usage === undefined || validUsage(value.usage)) &&
    (value.worktree === undefined || validWorktree(value.worktree))
  );
}

function validStringArray(value: unknown): boolean {
  return Array.isArray(value) && value.every((item) => typeof item === "string");
}

function validWorktree(value: unknown): boolean {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, WIRE_FIELD_KEYS.JobActivityWorktree) &&
    typeof value.path === "string" &&
    typeof value.branch === "string" &&
    typeof value.headSha === "string" &&
    isFiniteNumber(value.ahead) &&
    typeof value.dirty === "boolean"
  );
}

function validDiagnostics(value: unknown): boolean {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, ["commands", "plugins"]) &&
    (value.commands === undefined ||
      (Array.isArray(value.commands) &&
        value.commands.every(
          (command) =>
            isRecord(command) &&
            hasOnlyKeys(command, WIRE_FIELD_KEYS.CommandDescriptor) &&
            typeof command.name === "string" &&
            ["pluginName", "description", "argumentHint", "source"].every(
              (key) => command[key] === undefined || typeof command[key] === "string",
            ),
        ))) &&
    (value.plugins === undefined ||
      (Array.isArray(value.plugins) &&
        value.plugins.every(
          (plugin) => isRecord(plugin) && hasOnlyKeys(plugin, ["name"]) && typeof plugin.name === "string",
        )))
  );
}

function validSkill(value: unknown): boolean {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, WIRE_FIELD_KEYS.EvenerSkillInfo) &&
    typeof value.name === "string" &&
    ["disableModelInvocation", "userInvocable", "available"].every((key) => typeof value[key] === "boolean") &&
    (value.description === undefined || typeof value.description === "string") &&
    (value.allowedTools === undefined || validStringArray(value.allowedTools))
  );
}

function validEscalation(value: unknown): boolean {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, WIRE_FIELD_KEYS.SandboxEscalationRequested) &&
    ["threadId", "ref", "escalationId", "mode", "tool", "kind", "deniedPath"].every(
      (key) => typeof value[key] === "string",
    ) &&
    ["command", "outputSoFar"].every((key) => value[key] === undefined || typeof value[key] === "string") &&
    (value.partiallyRan === undefined || typeof value.partiallyRan === "boolean")
  );
}

function validSessionURL(value: unknown): boolean {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, WIRE_FIELD_KEYS.SessionURL) &&
    typeof value.id === "string" &&
    typeof value.url === "string" &&
    ["label", "addedBy"].every((key) => value[key] === undefined || typeof value[key] === "string") &&
    (value.addedAt === undefined || isFiniteNumber(value.addedAt))
  );
}

function validModelRetry(value: unknown): boolean {
  return (
    isRecord(value) &&
    hasOnlyKeys(value, [
      "attempt",
      "maxAttempts",
      "delayMs",
      "errorClass",
      "statusCode",
      "turnId",
      "model",
      "groupElapsedMs",
      "attemptCap",
      "receivedAt",
    ]) &&
    ["attempt", "maxAttempts", "delayMs", "groupElapsedMs", "attemptCap", "receivedAt"].every((key) =>
      isFiniteNumber(value[key]),
    ) &&
    ["statusCode"].every((key) => value[key] === undefined || isFiniteNumber(value[key])) &&
    ["errorClass", "turnId", "model"].every((key) => value[key] === undefined || typeof value[key] === "string")
  );
}

function validCurrentFieldShapes(value: Record<string, unknown>): boolean {
  const status = value.status as Record<string, unknown>;
  if (!hasOnlyKeys(status, WIRE_FIELD_KEYS.ThreadStatus)) return false;
  if (
    status.activeFlags !== undefined &&
    (!Array.isArray(status.activeFlags) || status.activeFlags.some((flag: unknown) => typeof flag !== "string"))
  )
    return false;
  if (
    value.tasks !== null &&
    (!isRecord(value.tasks) ||
      !hasOnlyKeys(value.tasks, WIRE_FIELD_KEYS.TaskAggregate) ||
      !isNonNegativeInteger(value.tasks.total) ||
      !isNonNegativeInteger(value.tasks.done) ||
      ["cancelled", "remaining"].some(
        (key) =>
          value.tasks &&
          (value.tasks as Record<string, unknown>)[key] !== undefined &&
          !isNonNegativeInteger((value.tasks as Record<string, unknown>)[key]),
      ) ||
      (value.tasks.current !== undefined &&
        (!isRecord(value.tasks.current) ||
          !hasOnlyKeys(value.tasks.current, WIRE_FIELD_KEYS.TaskSummary) ||
          !isNonNegativeInteger(value.tasks.current.id) ||
          typeof value.tasks.current.description !== "string")))
  )
    return false;
  if (value.usage !== null && !validUsage(value.usage)) return false;
  if (
    value.access !== undefined &&
    (!isRecord(value.access) ||
      !hasOnlyKeys(value.access, WIRE_FIELD_KEYS.ThreadAccess) ||
      typeof value.access.sandbox !== "string" ||
      typeof value.access.network !== "boolean")
  )
    return false;
  if (
    value.goal !== null &&
    (!isRecord(value.goal) ||
      !hasOnlyKeys(value.goal, WIRE_FIELD_KEYS.GoalState) ||
      (value.goal.objective !== undefined && typeof value.goal.objective !== "string") ||
      typeof value.goal.status !== "string" ||
      !isNonNegativeInteger(value.goal.iterations))
  )
    return false;
  if (value.delegates !== undefined && (!Array.isArray(value.delegates) || !value.delegates.every(validStableDelegate)))
    return false;
  if (value.diagnostics !== undefined && !validDiagnostics(value.diagnostics)) return false;
  if (value.skills !== undefined && (!Array.isArray(value.skills) || !value.skills.every(validSkill))) return false;
  if (!Array.isArray(value.pendingEscalations) || !value.pendingEscalations.every(validEscalation)) return false;
  if (!Array.isArray(value.sessionUrls) || !value.sessionUrls.every(validSessionURL)) return false;
  if (value.modelRetry !== undefined && !validModelRetry(value.modelRetry)) return false;
  if (
    value.turnSlots !== undefined &&
    value.turnSlots !== null &&
    (!isRecord(value.turnSlots) ||
      !hasOnlyKeys(value.turnSlots, WIRE_FIELD_KEYS.EvenerTurnSlots) ||
      ["inUse", "cap", "jobs", "driveTurns"].some(
        (key) => !isNonNegativeInteger((value.turnSlots as Record<string, unknown>)[key]),
      ))
  )
    return false;
  return true;
}

function validDeferredPage(value: unknown): boolean {
  if (
    !isRecord(value) ||
    !hasOnlyKeys(value, WIRE_FIELD_KEYS.ThreadTurnsListResponse) ||
    !Array.isArray(value.data) ||
    !value.data.every(validWireTurn)
  )
    return false;
  if (value.nextCursor !== undefined && typeof value.nextCursor !== "string") return false;
  if (value.bootGeneration !== undefined && typeof value.bootGeneration !== "string") return false;
  if (value.epoch !== undefined && !isNonNegativeInteger(value.epoch)) return false;
  if (value.authoritative !== undefined && typeof value.authoritative !== "boolean") return false;
  return (
    value.snapshot === undefined ||
    (isRecord(value.snapshot) &&
      hasOnlyKeys(value.snapshot, WIRE_FIELD_KEYS.SnapshotIdentity) &&
      typeof value.snapshot.incarnation === "string" &&
      isNonNegativeInteger(value.snapshot.length))
  );
}

function validWireTurn(value: unknown): boolean {
  if (!isRecord(value) || !hasOnlyKeys(value, WIRE_FIELD_KEYS.Turn)) return false;
  if (typeof value.id !== "string" || typeof value.itemsView !== "string" || typeof value.status !== "string")
    return false;
  if (value.items !== undefined && (!Array.isArray(value.items) || !value.items.every(validWireItem))) return false;
  if (value.error !== undefined && !validTurnError(value.error)) return false;
  if (value.version !== undefined && !isNonNegativeInteger(value.version)) return false;
  if (value.hasEarlierItems !== undefined && typeof value.hasEarlierItems !== "boolean") return false;
  if (value.hasLaterItems !== undefined && typeof value.hasLaterItems !== "boolean") return false;
  for (const key of ["startedAt", "completedAt", "durationMs"]) {
    if (value[key] !== undefined && !isFiniteNumber(value[key])) return false;
  }
  if (value.cost !== undefined && typeof value.cost !== "string") return false;
  return (
    value.usage === undefined ||
    (isRecord(value.usage) &&
      hasOnlyKeys(value.usage, WIRE_FIELD_KEYS.EvenerUsage) &&
      Object.values(value.usage).every(isFiniteNumber))
  );
}

export function validOverlayItem(value: unknown): value is OverlayItem {
  const keys = new Set(["key", "kind", "turnId", "roundId", "streamId", "callId", "historyKey", "anchor", "item"]);
  if (
    !isRecord(value) ||
    Object.keys(value).some((key) => !keys.has(key)) ||
    typeof value.key !== "string" ||
    typeof value.kind !== "string" ||
    !isRecord(value.item)
  )
    return false;
  return (
    ["turnId", "roundId", "streamId", "callId", "historyKey"].every(
      (key) => value[key] === undefined || typeof value[key] === "string",
    ) &&
    validWireItem(value.item) &&
    (value.anchor === undefined || validPosition(value.anchor))
  );
}

export function validCurrentFields(value: unknown): value is CurrentTranscriptFields {
  if (!isRecord(value)) return false;
  const strings = ["ref", "threadId", "name", "modelProvider", "model", "visionModel", "humanNote", "agentNote", "cwd"];
  if (strings.some((key) => typeof value[key] !== "string")) return false;
  const numbers = ["lastFrameAt", "contextUsed", "contextWindow", "contextPressure", "workMillis"];
  if (numbers.some((key) => !isFiniteNumber(value[key]))) return false;
  if (
    typeof value.askPending !== "boolean" ||
    typeof value.supportsReasoning !== "boolean" ||
    !isRecord(value.status) ||
    typeof value.status.type !== "string"
  )
    return false;
  if (
    !Array.isArray(value.pendingEscalations) ||
    !Array.isArray(value.sessionUrls) ||
    !Array.isArray(value.reasoningEffortLevels)
  )
    return false;
  if (value.reasoningEffortLevels.some((item) => typeof item !== "string")) return false;
  if (!validCurrentFieldShapes(value)) return false;
  if (value.queue !== null && !validQueue(value.queue)) return false;
  if (value.jobsUpdatedAt !== null && !isFiniteNumber(value.jobsUpdatedAt)) return false;
  if (value.jobsTreeRevision !== null && !isFiniteNumber(value.jobsTreeRevision)) return false;
  if (value.parentRef !== undefined && typeof value.parentRef !== "string") return false;
  if (value.imageSessionId !== undefined && typeof value.imageSessionId !== "string") return false;
  if (value.olderCursor !== undefined && typeof value.olderCursor !== "string") return false;
  if (value.cost !== undefined && value.cost !== null && typeof value.cost !== "string") return false;
  if (value.failedToolCalls !== undefined && !isFiniteNumber(value.failedToolCalls)) return false;
  if (value.activeTurnId !== undefined && typeof value.activeTurnId !== "string") return false;
  if (value.runningTurnId !== undefined && typeof value.runningTurnId !== "string") return false;
  for (const key of [
    "reasoningEffort",
    "activeTurnStartedAt",
    "lastTurnEndedAt",
    "gitBranch",
    "projectPath",
    "createdAt",
    "updatedAt",
  ]) {
    if (value[key] !== undefined && typeof value[key] !== "string") return false;
  }
  if (
    value.capabilities !== undefined ||
    value.pendingMutations !== undefined ||
    value.instanceId !== undefined ||
    value.resumeRequired !== undefined ||
    value.resumeOnlyFoldable !== undefined ||
    value.turns !== undefined ||
    value.history !== undefined ||
    value.overlay !== undefined
  )
    return false;
  return true;
}

export function validCurrentHistory(value: unknown): value is CurrentTranscriptState["history"] {
  const keys = new Set([
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
    "turns",
  ]);
  return (
    isRecord(value) &&
    Object.keys(value).every((key) => keys.has(key)) &&
    typeof value.bootGeneration === "string" &&
    isNonNegativeInteger(value.epoch) &&
    isNonNegativeInteger(value.length) &&
    isNonNegativeInteger(value.appliedGeneration) &&
    isNonNegativeInteger(value.issuedGeneration) &&
    Array.isArray(value.deferredPages) &&
    value.deferredPages.every(validDeferredPage) &&
    Array.isArray(value.turns) &&
    value.turns.every(validTurn) &&
    (value.incarnation === undefined || typeof value.incarnation === "string") &&
    (value.invalidatedAtGeneration === undefined || isNonNegativeInteger(value.invalidatedAtGeneration)) &&
    (value.failed === undefined || typeof value.failed === "string") &&
    (value.pendingIncarnation === undefined || typeof value.pendingIncarnation === "string") &&
    (value.awaited === undefined ||
      (isRecord(value.awaited) &&
        hasOnlyKeys(value.awaited, ["bootGeneration", "epoch"]) &&
        typeof value.awaited.bootGeneration === "string" &&
        isNonNegativeInteger(value.awaited.epoch)))
  );
}
