import { isModelControlAuthorityStale, mutationErrorData, WireError } from "./errors";
import type { ThreadModel } from "./model";
import { sameJsonValue } from "./plainObject";
import type { ModelControlAuthority, ModelControlSnapshot, ThreadModelSetResponse } from "./types.gen";

export interface ModelControlDisplay {
  modelProvider: string;
  model: string;
}

export type ModelControlClientState =
  | { kind: "uninitialized"; canonicalDisplay: ModelControlDisplay }
  | { kind: "ready"; snapshot: ModelControlSnapshot }
  | { kind: "recovering"; lastSafeDisplay: ModelControlDisplay }
  | { kind: "unsupported"; canonicalDisplay: ModelControlDisplay; observedVersion?: number };

export type ModelControlInput =
  | { kind: "connectionRetired" }
  | {
      kind: "readAccepted";
      version?: number;
      snapshot?: ModelControlSnapshot;
      authority: ModelControlAuthority;
    }
  | { kind: "snapshot"; source: "response" | "notification"; snapshot: ModelControlSnapshot }
  | { kind: "authorityStale" }
  | { kind: "postDispatchFailure" };

export type ModelControlRecoveryInput = Extract<ModelControlInput, { kind: "authorityStale" | "postDispatchFailure" }>;

export type ModelControlDecision = "applied" | "duplicate" | "ignored" | "recovering" | "unsupported";

export interface ModelControlRequestFence {
  authority: ModelControlAuthority;
  clientGeneration: number;
  readGeneration: number;
}

export interface ModelControlReduction {
  model: ThreadModel;
  decision: ModelControlDecision;
  replacementReadRequired: boolean;
}

export function modelControlRecoveryInput(error: unknown): ModelControlRecoveryInput | undefined {
  if (isModelControlAuthorityStale(error)) return { kind: "authorityStale" };
  const failure = mutationErrorData(error);
  if (
    !(error instanceof WireError) ||
    failure?.mutationOutcome === "unknown" ||
    failure?.retryDisposition === "blocked"
  ) {
    return { kind: "postDispatchFailure" };
  }
  return undefined;
}

export function sameModelControlAuthority(
  left: ModelControlAuthority | undefined,
  right: ModelControlAuthority | undefined,
): boolean {
  return (
    left !== undefined &&
    right !== undefined &&
    left.instanceId === right.instanceId &&
    left.bootGeneration === right.bootGeneration
  );
}

function canonicalDisplay(model: ThreadModel): ModelControlDisplay {
  return { modelProvider: model.modelProvider, model: model.model };
}

export function modelControlDisplay(model: ThreadModel): ModelControlDisplay {
  const state = model.modelControlState;
  if (state.kind === "ready") {
    const desired = state.snapshot.desired;
    if (desired?.lifecycle === "pending" || desired?.lifecycle === "applied") {
      return { modelProvider: desired.modelProvider, model: desired.model };
    }
    return {
      modelProvider: state.snapshot.active.modelProvider,
      model: state.snapshot.active.model,
    };
  }
  return state.kind === "recovering" ? state.lastSafeDisplay : state.canonicalDisplay;
}

export function projectModelControlSnapshot(model: ThreadModel, snapshot: ModelControlSnapshot): ThreadModel {
  return {
    ...model,
    modelControlState: { kind: "ready", snapshot },
    modelProvider: snapshot.active.modelProvider,
    model: snapshot.active.model,
    reasoningEffortLevels: [...(snapshot.active.reasoningEffortLevels ?? [])],
    supportsReasoning: snapshot.active.supportsReasoning,
  };
}

function recover(model: ThreadModel, replacementReadRequired: boolean): ModelControlReduction {
  if (model.modelControlState.kind === "recovering") {
    return { model, decision: "ignored", replacementReadRequired: false };
  }
  return {
    model: {
      ...model,
      modelControlState: { kind: "recovering", lastSafeDisplay: modelControlDisplay(model) },
    },
    decision: "recovering",
    replacementReadRequired,
  };
}

function reduceRevision(
  model: ThreadModel,
  held: ModelControlSnapshot,
  incoming: ModelControlSnapshot,
  conflictStartsRead: boolean,
): ModelControlReduction {
  if (incoming.revision < held.revision) {
    return { model, decision: "ignored", replacementReadRequired: false };
  }
  if (incoming.revision === held.revision) {
    if (sameJsonValue(held, incoming)) {
      return { model, decision: "duplicate", replacementReadRequired: false };
    }
    return recover(model, conflictStartsRead);
  }
  return {
    model: projectModelControlSnapshot(model, incoming),
    decision: "applied",
    replacementReadRequired: false,
  };
}

export function reduceModelControl(model: ThreadModel, input: ModelControlInput): ModelControlReduction {
  if (input.kind === "connectionRetired") {
    const display = canonicalDisplay(model);
    return {
      model: {
        ...model,
        modelProvider: display.modelProvider,
        model: display.model,
        modelControlState: { kind: "uninitialized", canonicalDisplay: display },
      },
      decision: "applied",
      replacementReadRequired: false,
    };
  }

  if (input.kind === "readAccepted") {
    if (input.version !== 1) {
      const display = canonicalDisplay(model);
      return {
        model: {
          ...model,
          modelControlState: {
            kind: "unsupported",
            canonicalDisplay: display,
            ...(input.version === undefined ? {} : { observedVersion: input.version }),
          },
        },
        decision: "unsupported",
        replacementReadRequired: false,
      };
    }
    if (!isCompleteSnapshot(input.snapshot) || !sameModelControlAuthority(input.snapshot.authority, input.authority)) {
      if (model.modelControlState.kind === "recovering") {
        return { model, decision: "recovering", replacementReadRequired: false };
      }
      return recover(model, false);
    }
    if (model.modelControlState.kind === "ready") {
      const held = model.modelControlState.snapshot;
      if (sameModelControlAuthority(held.authority, input.snapshot.authority)) {
        return reduceRevision(model, held, input.snapshot, false);
      }
    }
    return {
      model: projectModelControlSnapshot(model, input.snapshot),
      decision: "applied",
      replacementReadRequired: false,
    };
  }

  if (model.modelControlState.kind !== "ready") {
    return { model, decision: "ignored", replacementReadRequired: false };
  }

  if (input.kind === "authorityStale" || input.kind === "postDispatchFailure") {
    return recover(model, true);
  }

  const held = model.modelControlState.snapshot;
  if (!isCompleteSnapshot(input.snapshot) || !sameModelControlAuthority(held.authority, input.snapshot.authority)) {
    return recover(model, true);
  }
  return reduceRevision(model, held, input.snapshot, true);
}

export function modelControlAuthority(model: ThreadModel): ModelControlAuthority | undefined {
  return model.modelControlState.kind === "ready" ? model.modelControlState.snapshot.authority : undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.length > 0;
}

function isStringArrayOrNull(value: unknown): value is string[] | null {
  return value === null || (Array.isArray(value) && value.every((entry) => typeof entry === "string"));
}

function isCompleteSnapshot(value: unknown): value is ModelControlSnapshot {
  if (!isRecord(value) || !isRecord(value.authority) || !isRecord(value.active)) return false;
  if (!isNonEmptyString(value.authority.instanceId) || !isNonEmptyString(value.authority.bootGeneration)) return false;
  if (typeof value.revision !== "number" || !Number.isSafeInteger(value.revision) || value.revision <= 0) return false;
  if (!isNonEmptyString(value.active.modelProvider) || !isNonEmptyString(value.active.model)) return false;
  if (!isStringArrayOrNull(value.active.reasoningEffortLevels) || typeof value.active.supportsReasoning !== "boolean") {
    return false;
  }
  if (value.desired === undefined) return true;
  if (!isRecord(value.desired)) return false;
  return (
    typeof value.desired.requestSequence === "number" &&
    Number.isSafeInteger(value.desired.requestSequence) &&
    value.desired.requestSequence > 0 &&
    isNonEmptyString(value.desired.clientMutationId) &&
    isNonEmptyString(value.desired.modelProvider) &&
    isNonEmptyString(value.desired.model) &&
    (value.desired.lifecycle === "pending" ||
      value.desired.lifecycle === "applied" ||
      value.desired.lifecycle === "rejected")
  );
}

function malformedModelSetResponse(clientMutationId: string): WireError {
  return new WireError("thread/model/set returned an incomplete success response", -32603, {
    evenerErrorInfo: "mutationOutcomeUnknown",
    clientMutationId,
    mutationOutcome: "unknown",
    retryDisposition: "blocked",
  });
}

export function validateThreadModelSetResponse(value: unknown, clientMutationId: string): ThreadModelSetResponse {
  if (
    !isRecord(value) ||
    value.modelControlVersion !== 1 ||
    (value.disposition !== "accepted" && value.disposition !== "superseded") ||
    !isCompleteSnapshot(value.modelControl)
  ) {
    throw malformedModelSetResponse(clientMutationId);
  }
  return value as unknown as ThreadModelSetResponse;
}
