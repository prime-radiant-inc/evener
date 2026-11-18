import { DAEMONLESS_BOOT_GENERATION } from "./bootGeneration";
import { isModelControlAuthorityStale, mutationErrorData, WireError } from "./errors";
import type { ThreadModel } from "./model";
import { sameJsonValue } from "./plainObject";
import { SHUT_DOWN_STATUSES } from "./submitRouting";
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

// Whether a model pick can reach the wire on `model`. One predicate for every
// client, so the browser picker and the phone sheet cannot disagree about
// which sessions may offer a model change.
//
// The status gate is the shut-down set, which is exactly the set of sessions
// a resume can wake: the hub answers their read from pastThreadCapabilities
// with changeModel true and no model-control authority, and resumes the
// session behind the mutation. restartRequired is deliberately NOT here. It
// names a LIVE daemon on an incompatible protocol
// (appwire.ThreadStatusRestartRequired) whose read carries
// readablePastCapabilities - no changeModel - and whose thread/resume the hub
// refuses outright, so a pick on it has no resume path and must not be offered
// as one.
export function modelPickCanSend(model: Pick<ThreadModel, "capabilities" | "modelControlState" | "status">): boolean {
  if (model.capabilities?.changeModel !== true) return false;
  const kind = model.modelControlState.kind;
  if (kind === "ready") return true;
  // `ready` holds the authority; a cold session's `unsupported` is the one
  // state a pick resolves by resuming first. Everything else is held rather
  // than offered: `recovering`, whose replacement read the store owns and a
  // pick must not race; `uninitialized`, what a connection retire leaves until
  // a read lands; and a LIVE `unsupported`, a daemon whose envelope carries no
  // complete snapshot, which nothing resumes and which would fail after the
  // user chose.
  return kind === "unsupported" && coldSessionCanResume(model);
}

// Whether `model` names a session a model change would have to RESUME first: a
// shut-down thread the hub still advertises changeModel for. This is exactly
// the set the hub resumes behind a mutation (see modelPickCanSend above).
export function coldSessionCanResume(model: Pick<ThreadModel, "capabilities" | "status">): boolean {
  return model.capabilities?.changeModel === true && SHUT_DOWN_STATUSES.has(model.status.type);
}

// Whether a read's boot generation names a running daemon. A saved view is
// daemonless ("daemonless", or absent before any v6 read). But the hub also
// fills a LIVE daemon's empty window from the saved index — servedFrom
// "saved-index" with fallbackReason "live-read-empty" — and still stamps that
// daemon's own generation and live thread envelope. "No transcript items from
// a live daemon" is not "no authority from a live daemon": such a read carries
// model-control authority and must be evaluated from its own envelope, not
// skipped as a saved view.
export function readNamesLiveDaemon(bootGeneration: string | undefined): boolean {
  return bootGeneration !== undefined && bootGeneration !== "" && bootGeneration !== DAEMONLESS_BOOT_GENERATION;
}

// Whether a saved-view read leaves held model control alone. It normally does:
// a saved view authors no model-control authority of its own (a daemonless boot
// cannot speak for one), so a client that held authority keeps it, and a pane
// whose authority a reconnect retired stays retired until a LIVE read speaks
// again — the saved view must not upgrade a stale display into evidence.
//
// The exception is a read that identifies a session a pick can resume. A `ready`
// pane still holds real authority, so it keeps it. Every other held state
// carries none, and none of them offers the pick: `uninitialized` and
// `recovering` both leave the picker disabled. For those the cold `unsupported`
// the same read reduces to is strictly better — it is the one state a pick
// resolves by resuming first, and a pick is the only way the user gets that
// session running again. `recovering` is included on purpose: this branch only
// runs for a saved read (see the callers' historyFallback guard), so there is no
// live daemon left to answer the held recovery. Leaving the pane waiting would
// keep the picker disabled with nothing in flight to re-enable it.
export function savedReadKeepsHeldModelControl(
  held: Pick<ThreadModel, "modelControlState">,
  read: Pick<ThreadModel, "capabilities" | "status">,
): boolean {
  if (held.modelControlState.kind === "ready") return true;
  return !coldSessionCanResume(read);
}

// A read may report a null effort ladder for a model that has none. `null` is
// not a value the `string[]` snapshot type can carry: a caller reading
// `.length` or iterating the published ladder would throw. Normalization
// therefore happens where the snapshot ENTERS, not at each project/compare
// site, so the stored `ready` snapshot and every later revision comparison
// see the same `[]` a carrier sends explicitly — otherwise an empty-array
// carrier for the same revision would read as a conflict against a held null.
// Only the read path tolerates null; validateThreadModelSetResponse below
// still requires an array from a model-set reply.
function normalizedSnapshot(snapshot: ModelControlSnapshot): ModelControlSnapshot {
  const levels = snapshot.active.reasoningEffortLevels as unknown;
  if (Array.isArray(levels)) return snapshot;
  return { ...snapshot, active: { ...snapshot.active, reasoningEffortLevels: [] } };
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

export function restoreHeldModelControl(next: ThreadModel, held: ThreadModel): ThreadModel {
  return {
    ...next,
    modelControlState: held.modelControlState,
    modelProvider: held.modelProvider,
    model: held.model,
    reasoningEffortLevels: held.reasoningEffortLevels,
    supportsReasoning: held.supportsReasoning,
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
  if (input.kind === "readAccepted" && input.snapshot)
    input = { ...input, snapshot: normalizedSnapshot(input.snapshot) };
  if (input.kind === "snapshot") input = { ...input, snapshot: normalizedSnapshot(input.snapshot) };
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

// requireLevels says the caller cannot tolerate a null effort ladder. A read
// may report null for a model with no ladder and the client normalizes it
// before publishing a typed snapshot, but a model-set reply is the value a
// direct caller receives as `string[]`, so null there is an incomplete success.
function isCompleteSnapshot(value: unknown, requireLevels = false): value is ModelControlSnapshot {
  if (!isRecord(value) || !isRecord(value.authority) || !isRecord(value.active)) return false;
  if (!isNonEmptyString(value.authority.instanceId) || !isNonEmptyString(value.authority.bootGeneration)) return false;
  if (typeof value.revision !== "number" || !Number.isSafeInteger(value.revision) || value.revision <= 0) return false;
  if (!isNonEmptyString(value.active.modelProvider) || !isNonEmptyString(value.active.model)) return false;
  const levels = value.active.reasoningEffortLevels;
  const levelsValid = requireLevels
    ? Array.isArray(levels) && levels.every((entry) => typeof entry === "string")
    : isStringArrayOrNull(levels);
  if (!levelsValid || typeof value.active.supportsReasoning !== "boolean") return false;
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
    !isCompleteSnapshot(value.modelControl, true)
  ) {
    throw malformedModelSetResponse(clientMutationId);
  }
  return value as unknown as ThreadModelSetResponse;
}
