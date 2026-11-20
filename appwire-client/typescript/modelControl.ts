import { DAEMONLESS_BOOT_GENERATION } from "./bootGeneration";
import { isModelControlAuthorityStale, mutationErrorData, WireError } from "./errors";
import type { ThreadModel } from "./model";
import { sameJsonValue } from "./plainObject";
import { SHUT_DOWN_STATUSES } from "./submitRouting";
import type {
  ModelControlAuthority,
  ModelControlSnapshot,
  ThreadCapabilities,
  ThreadModelSetResponse,
} from "./types.gen";

export interface ModelControlDisplay {
  modelProvider: string;
  model: string;
}

export type ModelControlClientState =
  | { kind: "uninitialized"; canonicalDisplay: ModelControlDisplay }
  | { kind: "ready"; snapshot: ModelControlSnapshot }
  | { kind: "recovering"; lastSafeDisplay: ModelControlDisplay }
  | { kind: "unsupported"; canonicalDisplay: ModelControlDisplay };

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

// The slice of a session every model-pick decision reads. Capabilities may be
// partial: only changeModel is consulted, and a caller that describes a session
// by the capability it cares about must still be able to ask.
export interface ModelPickTarget {
  capabilities?: Partial<ThreadCapabilities> | undefined;
  modelControlState: ModelControlClientState;
  status: { type: string };
  // Whether the connection this session was read over can carry
  // thread/model/set at all. A hub that negotiated below this client's v8
  // refuses the mutation pre-send (client.ts's request), because v8 is what
  // added the client mutation id and the exact authority a daemon fences a
  // pick on.
  //
  // Every surface that publishes a session supplies this from the connection
  // that read it (the browser's store and the phone's conversation service).
  // Absent means the caller speaks for no connection - a hand-built fixture, a
  // unit test of another rule - and keeps the pre-connection answer rather than
  // disabling the picker on a fact nobody supplied.
  modelControlSupported?: boolean | undefined;
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
export function modelPickCanSend(model: ModelPickTarget): boolean {
  // Nothing can reach the wire on a connection below v8, whatever the session
  // offers: a cold pick would resume the daemon and then fail after the user
  // chose, so the pick must not be offered at all.
  if (model.modelControlSupported === false) return false;
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
export function coldSessionCanResume(model: Pick<ModelPickTarget, "capabilities" | "status">): boolean {
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
//
// Only the four lanes below normalizes. An OMITTED or non-array ladder is not
// the tolerated null: it is an incomplete snapshot, and rewriting it to `[]`
// here would turn one into a `ready` authority. So this runs strictly AFTER
// isCompleteSnapshot has validated the shape, and it touches nothing when the
// ladder is any value but null. validateThreadModelSetResponse below still
// requires an array from a model-set reply.
function normalizedSnapshot(snapshot: ModelControlSnapshot): ModelControlSnapshot {
  if (!isRecord(snapshot.active)) return snapshot;
  const levels = snapshot.active.reasoningEffortLevels as unknown;
  if (levels !== null) return snapshot;
  return { ...snapshot, active: { ...snapshot.active, reasoningEffortLevels: [] } };
}

// Sets or clears the provider instance named beside a model id, keeping the
// key's established shape: present only when some carrier named one, so a
// thread that never carried the key does not gain it with an undefined value
// (hydrateThread and the thread/model/changed branch keep the same rule).
function modelProfilePatch(
  previous: string | undefined,
  next: string | undefined,
): Partial<Pick<ThreadModel, "modelProfile">> {
  if (next !== undefined) return { modelProfile: next };
  if (previous === undefined) return {};
  return { modelProfile: undefined };
}

function projectModelControlSnapshot(model: ThreadModel, snapshot: ModelControlSnapshot): ThreadModel {
  // The snapshot names the running model the way a read does: the provider
  // instance in active.modelProvider with the bare id beside it, or one whole
  // identity in both fields when no instance is named apart. modelProfile is
  // the client's name for that instance (hydrateThread's Thread.evener.profile),
  // and qualifiedModelIdentity prefers it when qualifying the id, so it must
  // follow the snapshot: a cross-provider switch that left the previous read's
  // instance in place would qualify the new provider's model as the old one's.
  const profile = snapshot.active.modelProvider === snapshot.active.model ? undefined : snapshot.active.modelProvider;
  return {
    ...model,
    modelControlState: { kind: "ready", snapshot },
    modelProvider: snapshot.active.modelProvider,
    model: snapshot.active.model,
    ...modelProfilePatch(model.modelProfile, profile),
    reasoningEffortLevels: [...(snapshot.active.reasoningEffortLevels ?? [])],
    supportsReasoning: snapshot.active.supportsReasoning,
  };
}

export function restoreHeldModelControl(next: ThreadModel, held: ThreadModel): ThreadModel {
  // The held model's identity is a triple: the two fields above and the
  // provider instance named beside them. Restoring the first two without the
  // third would pair the held id with the fresh body's stale instance, so the
  // instance rides with the other held fields - and one the held model does
  // name cannot survive beside an id it does not qualify.
  return {
    ...next,
    modelControlState: held.modelControlState,
    modelProvider: held.modelProvider,
    model: held.model,
    ...modelProfilePatch(next.modelProfile, held.modelProfile),
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
          modelControlState: { kind: "unsupported", canonicalDisplay: display },
        },
        decision: "unsupported",
        replacementReadRequired: false,
      };
    }
    // Shape first, normalization second: a snapshot this authority cannot
    // speak for (or one whose own fields are missing) leaves model changes
    // unavailable, and must not be repaired into a `ready` authority here.
    if (!isCompleteSnapshot(input.snapshot) || !sameModelControlAuthority(input.snapshot.authority, input.authority)) {
      if (model.modelControlState.kind === "recovering") {
        return { model, decision: "recovering", replacementReadRequired: false };
      }
      return recover(model, false);
    }
    const incoming = normalizedSnapshot(input.snapshot);
    if (model.modelControlState.kind === "ready") {
      const held = model.modelControlState.snapshot;
      if (sameModelControlAuthority(held.authority, incoming.authority)) {
        return reduceRevision(model, held, incoming, false);
      }
    }
    return {
      model: projectModelControlSnapshot(model, incoming),
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
  return reduceRevision(model, held, normalizedSnapshot(input.snapshot), true);
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

/**
 * The reply that never arrived for a dispatched model switch. The daemon may
 * have applied the pick before the timeout or the socket close, so this is
 * `mutationOutcome=unknown` with `retryDisposition=blocked`, carrying the
 * mutation the caller actually sent. It is the same envelope a malformed
 * success produces, because it is the same fact: this call cannot say what
 * happened, and must not be replayed. Known pre-send failures and the peer's
 * own refusals are never converted into it.
 */
export function modelSetReplyLostError(clientMutationId: string, cause: "timeout" | "socketClosed"): WireError {
  return new WireError(
    cause === "timeout"
      ? "thread/model/set was dispatched but its reply timed out"
      : "thread/model/set was dispatched but the socket closed before its reply",
    -32603,
    {
      evenerErrorInfo: "mutationOutcomeUnknown",
      clientMutationId,
      mutationOutcome: "unknown",
      retryDisposition: "blocked",
      cause,
    },
  );
}

/**
 * The refusal for a model change on a connection that negotiated an older
 * protocol than this client speaks (client.ts's dialAndHandshake). v8 added
 * the client mutation id and the exact authority a daemon fences a pick on, so
 * an older hub would apply the pick unchecked rather than refuse it. Nothing
 * was dispatched, so this answer is notAccepted and never the outcome-unknown
 * envelope a lost reply produces: the picker must not offer a change this
 * connection cannot send. retryDisposition "none" is the wire's own word for
 * the same fact -- sending the identical pick again can only be refused again
 * (appwire.MutationOutcomeNotAccepted).
 */
export function modelSetOlderHubError(): WireError {
  return new WireError("This hub is older than this app, so it cannot change a session's model", -32600, {
    evenerErrorInfo: "olderHubProtocol",
    mutationOutcome: "notAccepted",
    retryDisposition: "none",
  });
}

/**
 * Whether a reply's own desired record is this mutation, already settled as
 * rejected. A reply can arrive with an accepted disposition and a rejected
 * lifecycle for the very mutation it answers: the pick passed admission, then
 * failed deferred validation (a boundary check, a quota retry) before the
 * daemon refreshed the reply's snapshot. Adopting that snapshot and treating
 * the call as a success confirms a switch the daemon has already given up on.
 * A pick still `pending` is not this: it is queued and may yet apply.
 */
export function modelSetReplyRejected(
  response: Pick<ThreadModelSetResponse, "modelControl">,
  clientMutationId: string,
): boolean {
  const desired = response.modelControl?.desired;
  return desired?.clientMutationId === clientMutationId && desired.lifecycle === "rejected";
}

export function validateThreadModelSetResponse(value: unknown, clientMutationId: string): ThreadModelSetResponse {
  if (
    !isRecord(value) ||
    (value.disposition !== "accepted" && value.disposition !== "superseded") ||
    !isCompleteSnapshot(value.modelControl, true)
  ) {
    throw malformedModelSetResponse(clientMutationId);
  }
  return value as unknown as ThreadModelSetResponse;
}
