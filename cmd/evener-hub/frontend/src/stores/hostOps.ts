// hostOps.ts is the Hosts settings section's deploy-pipeline store (component
// 08 slice S14): the per-host plan/deploy state behind the section's Deploy
// action and its plan confirmation dialog, plus the Restart attempt state.
// Plan/deploy/restart are the deploy-pipeline spec's methods (08b §6, §10):
// `plan` mints the single-use confirmation token from fresh facts, `deploy`
// consumes exactly that displayed token with a client operation ID, and
// `restart` opens a durable operation for the intended (generation,
// incarnation id) pair.
//
// The registry contract this renders lives in registry spec 08 §13: opening the
// Deploy dialog calls `evener/host/plan` and renders the controller-minted plan;
// the confirmation submits exactly the displayed plan; a token refusal (expired,
// superseded, or binding-mismatched) re-plans and re-renders before any retry;
// and the no-token arm branches on `reason` with a per-reason affordance.
//
// A started operation's record is retained under `operations`; S15's polling
// reads it by (name, controller-assigned id) and the row renders progress
// through its terminal state (registry spec 08 §13, deploy-pipeline spec 08b
// §6/§8/§10). S16 adds the remnant repair affordances on top: `teardownRetry`
// resumes one named remnant through `evener/host/teardown-retry`, and the
// escalated `teardownRecover` clears an unresolvable one through
// `evener/host/teardown-recover` with the audited operator attestation
// (registry spec 08 §6/§11).

import type {
  AppwireClientLike,
  HostPlan,
  HostPlanStaleFacts,
  HostTeardownAttestation,
  HostTeardownRecoverResult,
  MethodTypes,
  OperationProgressEntry,
  OperationRecord,
} from "@evener/appwire-client";
import { friendlyErrorMessage, RequestTimeoutError, WireError } from "@evener/appwire-client";
import { create, useStore } from "zustand";
import { connectedClientPort, connectionStore } from "./connection";
import { HOST_GATE_TIMEOUT_MS, type HostMutationPair, hostsStore } from "./hosts";
import { createSecureUUID } from "./secureUUID";

type HostTeardownRetryResult = MethodTypes["evener/host/teardown-retry"]["result"];

// --- the §11 refusal vocabulary ----------------------------------------------
//
// Every deploy-pipeline refusal rides the AppWire error envelope with its
// stable `evenerErrorInfo` discriminator (deploy-pipeline spec 08b §11), never
// the shared numeric code. This module is the one place the UI classifies that
// vocabulary, so every surface renders the same concrete sentence and offers
// the same recovery instead of falling through to a generic failure.

export type HostOpRefusalKind =
  | "token-missing"
  | "token-mismatched"
  | "token-superseded"
  | "token-expired"
  | "stale-entry"
  | "conflicting-operation-id"
  | "host-detached"
  | "probe-failed"
  | "remnant-open"
  | "host-busy-operation"
  | "host-busy-transient"
  | "teardown-unknown-key"
  | "invalid-params"
  | "cursor-invalidated"
  | "cursor-too-large"
  | "unknown";

export interface HostOpRefusal {
  kind: HostOpRefusalKind;
  /** Concrete UI copy: the stable headline plus the hub's own prose, so no
   * vocabulary member ever renders as a generic failure. */
  message: string;
  /** Present exactly on `remnant-open` (08b §11: the blocking remnant's id). */
  remnantId?: string;
  /** Present exactly on `host-busy-operation` (08b §11: the running record id). */
  operationId?: string;
}

/** stringData reads one string-valued data field off a wire error, or
 * undefined for any other shape. */
function stringData(error: unknown, key: string): string | undefined {
  if (!(error instanceof WireError) || !error.data || typeof error.data !== "object") return undefined;
  const value = (error.data as Record<string, unknown>)[key];
  return typeof value === "string" && value !== "" ? value : undefined;
}

function withDetail(headline: string, detail: string): string {
  const trimmed = detail.trim();
  return trimmed === "" ? headline : `${headline} ${trimmed}`;
}

/** HostOpRequestContext names the call a refusal came from, for the copy that
 * differs per call: only deploy/restart carry an operation ID, the operations
 * read reports progress rather than starting work, and the teardown calls are
 * the remnant repair. */
export type HostOpRequestContext = "plan" | "operation" | "connect" | "progress" | "teardown";

function timeoutMessage(context: HostOpRequestContext): string {
  switch (context) {
    case "plan":
      return "The hub did not answer before the plan request timed out; retry planning.";
    case "connect":
      return "The hub did not answer before the Connect request timed out; retry.";
    case "operation":
      return "The hub did not answer before the request timed out; retry — a retry repeats the same operation ID.";
    case "progress":
      return "The hub did not answer before the operation-progress read timed out; progress will retry.";
    case "teardown":
      return "The hub did not answer before the teardown request timed out; retry the repair.";
  }
}

/** hostOpRefusal classifies a rejection into the vocabulary the surfaces
 * render. Known discriminators get a stable, concrete headline with the hub's
 * own message appended as the detail; anything else is `unknown` — a hub
 * message when the hub composed one, otherwise the client's own sentence for
 * the unreachable family, and a timeout keeps the one instruction the lost-
 * response contract promises for THAT call (only an operation retry repeats
 * the same operation ID; a plan has nothing to repeat but itself). */
export function hostOpRefusal(error: unknown, context: HostOpRequestContext = "operation"): HostOpRefusal {
  const info = error instanceof WireError ? error.evenerErrorInfo : undefined;
  const detail = friendlyErrorMessage(error);
  switch (info) {
    case "token-missing":
      return { kind: info, message: withDetail("This confirmation's plan token is no longer on the server.", detail) };
    case "token-mismatched":
      return { kind: info, message: withDetail("This confirmation does not match the server's plan token.", detail) };
    case "token-superseded":
      return { kind: info, message: withDetail("A newer plan has superseded this confirmation.", detail) };
    case "token-expired":
      return { kind: info, message: withDetail("This plan expired before the deploy was submitted.", detail) };
    case "stale-entry":
      // Action-agnostic: the same refusal surfaces in the deploy dialog and
      // the restart dialog, and only one of them has a plan.
      return { kind: info, message: withDetail("The host changed since this operation was prepared.", detail) };
    case "conflicting-operation-id":
      return {
        kind: info,
        message: withDetail("This client operation ID is already used by another operation.", detail),
      };
    case "host-detached":
      return { kind: info, message: withDetail("The host has no live attached channel.", detail) };
    case "probe-failed":
      return { kind: info, message: withDetail("Probing the host's running state failed.", detail) };
    case "remnant-open": {
      const remnantId = stringData(error, "remnantId");
      const headline =
        remnantId === undefined
          ? "An open teardown remnant is blocking this host."
          : `An open teardown remnant is blocking this host (${remnantId}).`;
      return { kind: info, message: withDetail(headline, detail), ...(remnantId === undefined ? {} : { remnantId }) };
    }
    case "host-busy-operation": {
      const operationId = stringData(error, "operationId");
      const headline =
        operationId === undefined
          ? "Another operation is running on this host."
          : `Another operation is running on this host (${operationId}).`;
      return {
        kind: info,
        message: withDetail(headline, detail),
        ...(operationId === undefined ? {} : { operationId }),
      };
    }
    case "host-busy-transient":
      return { kind: info, message: withDetail("The host is busy right now.", detail) };
    case "teardown-unknown-key": {
      const remnantId = stringData(error, "remnantId");
      const headline =
        remnantId === undefined
          ? "The hub does not know this teardown remnant."
          : `The hub does not know teardown remnant ${remnantId}.`;
      return { kind: info, message: withDetail(headline, detail), ...(remnantId === undefined ? {} : { remnantId }) };
    }
    case "invalidParams":
      // The recover attestation's validation refusals (shape, operator
      // mismatch) ride this discriminator; the hub's own sentence names the
      // blocking check, so render it concretely instead of generically.
      return { kind: "invalid-params", message: withDetail("The hub refused this request as invalid.", detail) };
    case "cursor-invalidated":
      // 08b §8: a compaction removed rows at or before the position this read
      // resumed from; the client restarts from the first page. The poll never
      // presents a cursor, so this arm is unreachable with its read shape — it
      // exists so any occurrence renders concretely instead of generically.
      return { kind: info, message: withDetail("The operation list was compacted under this read.", detail) };
    case "cursor-too-large":
      // 08b §8: the first page's boundary map would overrun the 8 KiB encoded
      // cap ({capBytes: 8192}), so no cursor was minted.
      return { kind: info, message: withDetail("The host operation list is too large to page through.", detail) };
    default:
      if (error instanceof RequestTimeoutError) {
        return { kind: "unknown", message: timeoutMessage(context) };
      }
      return { kind: "unknown", message: detail };
  }
}

/** HostOpRecovery is the affordance a refusal earns on the surface that
 * rendered it: mint a fresh plan, repeat the failed request, Connect first, or
 * nothing at all (the open/wait and teardown-retry affordances are S15/S16). */
export type HostOpRecovery = "replan" | "retry" | "connect" | "none";

/** deployRefusalAction maps an `evener/host/deploy` refusal onto its recovery
 * (08b §6, §11, registry spec 08 §13): a stale token re-plans; an operation-ID
 * collision, transient busy, or an unclassified/transport failure retries the
 * deploy; a gone channel Connects; and the fenced/busy arms never bare-retry. */
export function deployRefusalAction(kind: HostOpRefusalKind): HostOpRecovery {
  switch (kind) {
    case "token-missing":
    case "token-mismatched":
    case "token-superseded":
    case "token-expired":
    case "stale-entry":
    case "probe-failed":
      return "replan";
    case "conflicting-operation-id":
    case "host-busy-transient":
    case "unknown":
      return "retry";
    // Read-side refusals (the operations poll) the deploy path never emits; a
    // repeat is the safe recovery for the same reason it is for `unknown`.
    case "cursor-invalidated":
    case "cursor-too-large":
      return "retry";
    case "host-detached":
      return "connect";
    case "remnant-open":
    case "host-busy-operation":
      return "none";
    // The S16 repair refusals (and the recover attestation's validation
    // refusal) never arise from deploy; if one ever did, a bare repeat is not
    // obviously safe, so none is the honest answer.
    case "teardown-unknown-key":
    case "invalid-params":
      return "none";
  }
}

/** planRefusalAction maps an `evener/host/plan` envelope refusal onto its
 * recovery: planning again is safe for everything a plan can refuse with,
 * except the fenced/busy arms, which must not be bare-retried (08b §11). */
export function planRefusalAction(kind: HostOpRefusalKind): HostOpRecovery {
  switch (kind) {
    case "host-busy-operation":
    case "remnant-open":
      return "none";
    default:
      return "replan";
  }
}

/** planNoTokenAction maps the no-token arm's `reason` onto its affordance
 * (registry spec 08 §13): `unattached` directs the UI to Connect first;
 * `refresh-failed`, `probe-failed`, and `handler-absent` re-plan (never a
 * Connect loop); `remnant-open` offers nothing this slice — it never re-plans
 * and never Connects, because a re-plan mints nothing while the remnant is
 * open (its teardown-retry affordance is S16); the terminal arms disable the
 * confirmation with no retry. */
export function planNoTokenAction(reason: string, terminal: boolean): HostOpRecovery {
  if (terminal) return "none";
  switch (reason) {
    case "unattached":
      return "connect";
    case "refresh-failed":
    case "probe-failed":
    case "handler-absent":
      return "replan";
    case "remnant-open":
      // Explicitly affordance-free: it never re-plans and never Connects (a
      // re-plan mints nothing while the remnant is open; its teardown-retry
      // affordance is S16's).
      return "none";
    default:
      // An unrecognized non-terminal reason is never a dead end: re-planning
      // is the safe recovery, matching planRefusalAction's own default.
      return "replan";
  }
}

/** hostOpRefusalBlocksRetry reports whether a refusal's kind forbids a bare
 * retry: a held gate's operation (open/wait is S15's affordance) and an open
 * teardown remnant (teardown-retry is S16's) both refuse a plain repeat. */
export function hostOpRefusalBlocksRetry(kind: HostOpRefusalKind): boolean {
  return kind === "host-busy-operation" || kind === "remnant-open";
}

/** teardownRetryRefusalAction maps a `teardown-retry` refusal onto its recovery
 * (08b §6, registry spec 08 §11). A live attempt holding the gate refuses with
 * the typed transient busy — the gate holder owns the attempt, so the operator
 * retries later, never silently. An operation-held gate, an unknown/purged
 * remnant id, and a validation refusal all refuse a bare repeat: each names its
 * own next step instead (the unknown key names a re-read). */
export function teardownRetryRefusalAction(kind: HostOpRefusalKind): HostOpRecovery {
  switch (kind) {
    case "host-busy-transient":
      return "retry";
    case "host-busy-operation":
    case "teardown-unknown-key":
    case "invalid-params":
      return "none";
    default:
      return "retry";
  }
}

/** teardownRecoverRefusalAction maps a `teardown-recover` refusal onto its
 * recovery with the same arms as the retry's (the recover also try-acquires
 * the gate, so a live attempt is the typed transient busy). */
export function teardownRecoverRefusalAction(kind: HostOpRefusalKind): HostOpRecovery {
  return teardownRetryRefusalAction(kind);
}

// --- the remnant repair state (S16) ------------------------------------------

/** TEARDOWN_RECOVER_STATEMENT is the only `statement` value
 * `evener/host/teardown-recover` accepts (registry spec 08 §11: the audited
 * operator recovery's attestation). */
export const TEARDOWN_RECOVER_STATEMENT = "teardown-verified-absent";

/** HostTeardownRetryView projects one `teardown-retry` result arm onto what
 * the surfaces render: the arm's own outcome discriminator, the remnant, and
 * the row shape it paired with (`hostKind: "live"` with HostRow, "removed"
 * with the tombstone RemovedRow). Never an aggregate success: `seam` stays
 * present exactly on the failure arm, which leaves the remnant open. */
export interface HostTeardownRetryView {
  outcome: string;
  remnantId: string;
  hostKind: string;
  hostName: string;
  hostRemoved: boolean;
  escalationAgeSec?: number;
  seam?: string;
}

/** HostTeardownClearView is the `teardown-recover` response's single
 * `recovered-cleared` arm: clearedName/clearedAt/hostKind, no live row. */
export interface HostTeardownClearView {
  remnantId: string;
  clearedName: string;
  clearedAt: string;
  hostKind: string;
}

/** HostRemnantRepair is the per-name repair state every remnant surface
 * renders: the in-flight submission, the concrete refusal, or the arm the hub
 * answered. */
export type HostRemnantRepair =
  | { phase: "retrying"; remnantId: string }
  | { phase: "recovering"; remnantId: string }
  | { phase: "refused"; remnantId: string; action: "retry" | "recover"; refusal: HostOpRefusal }
  | { phase: "retried"; remnantId: string; result: HostTeardownRetryView }
  | { phase: "cleared"; remnantId: string; result: HostTeardownClearView };

/** retryOutcomeLine renders one retry arm's own outcome: the resolved arms say
 * what resolved, the failure arm names the seam and the still-open remnant,
 * and an unrecognized arm shows its raw outcome rather than claiming
 * success. */
export function retryOutcomeLine(result: HostTeardownRetryView): string {
  switch (result.outcome) {
    case "teardown-complete":
      return `Teardown completed; remnant ${result.remnantId} is resolved.`;
    case "already-cleared":
      return `Remnant ${result.remnantId} was already cleared.`;
    case "committed-with-teardown-failure":
      return `Teardown failed again at ${result.seam ?? "the teardown"}; remnant ${result.remnantId} is still open.`;
    default:
      return `The hub answered ${result.outcome} for remnant ${result.remnantId}.`;
  }
}

/** clearedOutcomeLine renders the recover's `recovered-cleared` arm. */
export function clearedOutcomeLine(result: HostTeardownClearView): string {
  return `Recovered: cleared remnant ${result.remnantId} for ${result.clearedName} at ${result.clearedAt}.`;
}

/** retryArmResolved reports whether a retry arm resolved the remnant (the two
 * success outcomes), so a surface may offer the next step (re-plan); the
 * failure arm leaves it open. */
export function retryArmResolved(result: HostTeardownRetryView): boolean {
  return result.outcome === "teardown-complete" || result.outcome === "already-cleared";
}

/** restartRefusalAction maps an `evener/host/restart` refusal onto its
 * recovery: a gone channel Connects and re-seeds the confirmation against the
 * registry's current pair; the fenced/busy arms never bare-retry; everything
 * else may repeat the request (a stale-entry re-reads and re-peeks first, and
 * a conflicting-operation-id rotates the key). */
export function restartRefusalAction(kind: HostOpRefusalKind): HostOpRecovery {
  switch (kind) {
    case "host-detached":
      return "connect";
    case "remnant-open":
    case "host-busy-operation":
      return "none";
    default:
      return "retry";
  }
}

// --- per-host state ----------------------------------------------------------

export type HostPlanPhase =
  | { phase: "planning" }
  | {
      phase: "planned";
      plan: HostPlan;
      /** The single-use confirmation token this confirmation submits. */
      token: string;
      /** The client operation ID this confirmation submits; a lost-response
       * retry of the same confirmation reuses it (08b §6). */
      operationId: string;
      /** Set when a deploy refusal re-planned the confirmation, so the
       * operator knows why the displayed plan changed. */
      notice: string | null;
      refusal: HostOpRefusal | null;
    }
  | { phase: "no-token"; staleFacts: HostPlanStaleFacts; terminal: boolean; remnantId: string | null }
  | { phase: "error"; refusal: HostOpRefusal; recovery: HostOpRecovery }
  | { phase: "started" };

export interface HostRestartAttempt {
  phase: "confirm" | "failed" | "started";
  /** The intended (generation, incarnation id) pair: the row the operator saw.
   * A lost-response retry repeats it and replays (08b §10). */
  pair: HostMutationPair;
  operationId: string;
  refusal: HostOpRefusal | null;
}

/** HostOperationRef is one started operation as the UI tracks it: the
 * deploy/restart response's seed ({id, clientOperationId, state}, 08b §10)
 * plus whatever the `operations` poll has read from the record. */
export interface HostOperationRef {
  /** The controller-assigned record id: the `id` detail filter the poll reads
   * by, and the key a replay re-answers under (08b §8, §10). */
  id: string;
  /** The client operation ID this operation was submitted under. A retry after
   * a lost response re-submits exactly this ID, and the hub's dedup answers the
   * existing record — never a fresh operation (08b §6, §10; registry spec 08
   * §13). */
  clientOperationId: string;
  /** The record's kind ("deploy"/"restart"): seeded by the call that started
   * the operation, refreshed from the record. */
  kind: string;
  state: string;
  /** The record's host and pinned (generation, incarnation id) pair: the seed
   * carries what the starting call knew (the plan's generation, the restart's
   * pair), and the first read replaces them with the record's own. They are
   * what lets a row refuse to display another incarnation's operation after a
   * same-name remove/re-add. */
  host?: string;
  generation?: number;
  incarnationId?: string;
  /** The record's progress entries, newest last (08b §10). Empty until the
   * first read answers. */
  progress: OperationProgressEntry[];
  /** The record's terminal result; absent until a read answers one. The poll
   * renders a failure's `message` VERBATIM (registry spec 08 §13). */
  result?: { ok: boolean; message: string };
  /** S6's tombstone-replay marker. The generated base types do not name it yet
   * (app_host_ops.go's operationRecordWire leaves `compacted` absent until S6
   * registers it), so the poll reads it structurally: a replayed record renders
   * as that operation's past terminal outcome, never a fresh operation. */
  compacted?: true;
  /** True once a read answered this record's full body; the deploy/restart
   * response is only the seed. */
  fetched?: true;
  /** The newest read's refusal, when it rejected: progress updates stopped and
   * the last-known state/progress stay rendered underneath. Cleared by a good
   * read. */
  readRefusal?: HostOpRefusal;
  /** True when a read answered no record for this id (past the tombstone
   * retention horizon): the outcome cannot be shown, so the loop stops. */
  gone?: true;
}

/** Settled states end the read loop (08b §10's closed state set): the three
 * terminal states. `orphan-unverified` is deliberately NOT settled: it is
 * durable but an out-of-band transition can still move it, so the loop keeps
 * polling it until the record reaches a terminal state — treating it as settled
 * would freeze the moved record behind a stale state until a page reload. */
const SETTLED_OPERATION_STATES: ReadonlySet<string> = new Set(["complete", "failed", "interrupted"]);

export function operationStateSettled(state: string): boolean {
  return SETTLED_OPERATION_STATES.has(state);
}

/** operationNeedsRead reports whether this ref still owes a `operations` read:
 * a seed that has never been read (a replayed terminal seed still owes one read
 * for its retained outcome), or a non-settled state. A gone record owes
 * nothing — no read can bring its outcome back. */
export function operationNeedsRead(ref: HostOperationRef): boolean {
  if (ref.gone === true) return false;
  if (ref.fetched !== true) return true;
  return !operationStateSettled(ref.state);
}

/** operationShownOnHost reports whether a tracked operation still belongs to
 * the host incarnation the row displays. Generations are strictly monotonic
 * (registry spec 08 §1), so the comparison is directional:
 *
 * - A ref pinned to an OLDER generation than the row's belongs to a previous
 *   incarnation (a same-name remove/re-add) and is suppressed — the previous
 *   incarnation's record is not this host's work. Equal generations compare
 *   further: a differing incarnation id suppresses too.
 * - A ref pinned to a NEWER generation than the row's is current and the ROW is
 *   merely stale (the pane's list poll converges it): the operation stays.
 * - A ref that carries no pair was seeded by the call that just ran on this
 *   row, so it renders.
 *
 * Suppression is render-only: the poll still drives a suppressed ref to its
 * terminal state (pollOperation never consults this), so no ref stays
 * non-terminal forever and the store can still retain the record's outcome. */
export function operationShownOnHost(
  operation: HostOperationRef,
  row: { generation: number; incarnationId: string },
): boolean {
  if (operation.generation !== undefined && operation.generation < row.generation) return false;
  if (
    operation.generation === row.generation &&
    operation.incarnationId !== undefined &&
    operation.incarnationId !== row.incarnationId
  ) {
    return false;
  }
  return true;
}

/** HostOperationView is one operation ref as a host row renders it. */
export interface HostOperationView {
  tone: "neutral" | "attention" | "alive" | "danger";
  /** "Deploying…", "Restart complete", "Deploy failed", ... */
  label: string;
  /** The record's own prose in one line: the newest progress entry while the
   * operation runs, or the terminal result's message — verbatim, never
   * rewritten into a generic failure (§13). */
  line: string | null;
  /** The compacted-tombstone note, separate from `line` so the retained result
   * stays verbatim. */
  replay: string | null;
  /** Why progress updates stopped (a failed or answerless read), or null. */
  unavailable: string | null;
}

function operationKindLabel(kind: string): string {
  switch (kind) {
    case "deploy":
      return "Deploy";
    case "restart":
      return "Restart";
    default:
      return kind === "" ? "Operation" : kind;
  }
}

function operationInFlightLabel(kind: string): string {
  switch (kind) {
    case "deploy":
      return "Deploying…";
    case "restart":
      return "Restarting…";
    default:
      return `${operationKindLabel(kind)} in progress`;
  }
}

/** hostOperationView renders one operation ref for its host row: the state
 * chip, the record's own one-line prose (a failure's 04b message verbatim), the
 * compacted-replay note, and the visible stopped-progress reason. */
export function hostOperationView(ref: HostOperationRef): HostOperationView {
  const kind = operationKindLabel(ref.kind);
  const lastEntry = ref.progress[ref.progress.length - 1];
  // A blank progress message is no line, exactly like an empty result message:
  // never an empty detail span.
  const latest = lastEntry === undefined || lastEntry.message.trim() === "" ? null : lastEntry.message;
  // A record with no result (or an empty message) renders the state chip alone
  // rather than an invented sentence.
  const outcome = ref.result === undefined || ref.result.message === "" ? null : ref.result.message;
  let tone: HostOperationView["tone"] = "neutral";
  let label = `${kind} ${ref.state}`;
  // A state outside the wire's closed set still shows the record's own prose:
  // the newest progress entry, falling back to the result message.
  let line: string | null = latest ?? outcome;
  switch (ref.state) {
    case "pending":
    case "running":
      tone = "attention";
      label = operationInFlightLabel(ref.kind);
      line = latest;
      break;
    case "complete":
      tone = "alive";
      label = `${kind} complete`;
      line = outcome;
      break;
    case "failed":
      tone = "danger";
      label = `${kind} failed`;
      line = outcome;
      break;
    case "interrupted":
      tone = "danger";
      label = `${kind} interrupted`;
      line = outcome;
      break;
    case "orphan-unverified":
      tone = "attention";
      label = `${kind} orphan-unverified`;
      line = outcome;
      break;
  }
  return {
    tone,
    label,
    line,
    replay:
      ref.compacted === true
        ? "This record was replayed from a compacted operation; the result shown is its retained terminal outcome."
        : null,
    unavailable:
      ref.readRefusal !== undefined
        ? `Progress updates stopped: ${ref.readRefusal.message}`
        : ref.gone === true
          ? "The hub no longer retains this operation's record, so its outcome cannot be shown."
          : null,
  };
}

interface HostOpsStoreState {
  plans: Record<string, HostPlanPhase>;
  restarts: Record<string, HostRestartAttempt>;
  operations: Record<string, HostOperationRef>;
  /** The remnant repair state per host name (S16): the teardown-retry or
   * teardown-recover submission and the arm/refusal it answered. */
  repairs: Record<string, HostRemnantRepair>;
  /** Opens the confirmation: calls `evener/host/plan` and publishes the arm it
   * answers (§13). A `notice` is only ever set by an automatic re-plan. */
  plan: (name: string, opts?: { notice?: string }) => Promise<void>;
  /** Submits exactly the displayed plan's token plus the client operation ID.
   * Resolves with the state advanced (started, re-planned, or refused). */
  deploy: (name: string) => Promise<void>;
  /** The `unattached`/`host-detached` recovery: Connect, then re-plan. */
  connectAndPlan: (name: string) => Promise<void>;
  discardPlan: (name: string) => void;
  beginRestart: (name: string, pair: HostMutationPair) => void;
  restart: (name: string) => Promise<void>;
  /** The `host-detached` restart recovery: Connect, then re-seed the
   * confirmation against the registry's current pair, never the refused
   * attempt's pair. */
  connectAndRestart: (name: string) => Promise<void>;
  /** The resolved-remnant continuation: re-reads the registry and re-seeds the
   * restart confirmation against the pair it answers, under a fresh operation
   * ID — the refused attempt's pair is never resubmitted. */
  reSeedRestart: (name: string) => Promise<void>;
  discardRestart: (name: string) => void;
  /** Reads the tracked operation's record once (08b §10's detail read) and
   * publishes what it answers: progress, the terminal outcome, or the visible
   * refusal. The caller owns the cadence; the section's mounted interval drives
   * this and stops when its target is settled. */
  pollOperation: (name: string) => Promise<void>;
  /** Submits `evener/host/teardown-retry` for exactly this remnant (registry
   * spec 08 §6/§11) and publishes the arm it answers. */
  teardownRetry: (name: string, remnantId: string) => Promise<void>;
  /** Submits `evener/host/teardown-recover` with the audited operator
   * attestation (08 §6/§11) and publishes the `recovered-cleared` result. */
  teardownRecover: (name: string, remnantId: string, attestation: HostTeardownAttestation) => Promise<void>;
  /** Drops the name's repair state (a dialog close): a later response for it
   * publishes nothing. */
  clearRepair: (name: string) => void;
  /** Drops a REFUSED repair entry, leaving any in-flight submission
   * untouched: the fence re-check never cancels the arm a live request is
   * about to publish. */
  clearRepairRefusal: (name: string) => void;
  /** Makes any in-flight read for this name publish nothing (the caller owns
   * the interval; the section calls this on unmount). It also releases the
   * name's pending-read entries, so a remount's first tick is not skipped. */
  stopOperationPoll: (name: string) => void;
  resetForTests: () => void;
}

// requireClient resolves connectionStore's CURRENT client, labelled by this
// store - the shared port (stores/connection.ts), not a hand-rolled twin.
const { requireClient } = connectedClientPort("hostOps");

// Per-host request sequences: a response may only publish while it is still
// the newest request issued for that host, and a discarded dialog's response
// publishes nowhere (the guard stores/hosts.ts uses for its list reads).
const planSequences = new Map<string, number>();
const restartSequences = new Map<string, number>();
const operationSequences = new Map<string, number>();
const repairSequences = new Map<string, number>();
// The deploy/restart REQUEST tokens issued per host name, bumped when a
// request is sent (never by a publish, a dialog close, or unmount), plus the
// newest token that actually PUBLISHED. A response may publish unless a
// strictly newer request already published: two issued requests are ordered by
// the published token, not by arrival, and a newer request that never
// publishes (refused, or still in flight) cannot suppress an older valid
// response.
const operationRequestSequences = new Map<string, number>();
const operationPublishedRequestSeqs = new Map<string, number>();
// Outstanding operation reads, keyed by host name and the record id the read
// is for, as per-read tokens (a direct caller and the section's tick can
// overlap). The tick skips a pending (name, id), so the interval keeps at most
// one read per operation in flight — and a superseded operation's lingering
// read never blocks its replacement. A token is released in pollOperation's
// finally, or wholesale by stopOperationPoll on unmount; a released token can
// no longer release a newer read's entry.
const operationReadsPending = new Map<string, Map<string, Set<object>>>();
function planSequence(name: string): number {
  return planSequences.get(name) ?? 0;
}
function restartSequence(name: string): number {
  return restartSequences.get(name) ?? 0;
}
function operationSequence(name: string): number {
  return operationSequences.get(name) ?? 0;
}
function repairSequence(name: string): number {
  return repairSequences.get(name) ?? 0;
}

function nextOperationRequestSeq(name: string): number {
  const seq = (operationRequestSequences.get(name) ?? 0) + 1;
  operationRequestSequences.set(name, seq);
  return seq;
}

/** operationRequestPublished answers whether a response bearing `requestSeq`
 * may still publish: it must not be older than the newest request that
 * actually published. */
function operationRequestPublished(name: string, requestSeq: number): boolean {
  return requestSeq >= (operationPublishedRequestSeqs.get(name) ?? 0);
}

function markOperationRequestPublished(name: string, requestSeq: number): void {
  const published = operationPublishedRequestSeqs.get(name) ?? 0;
  if (requestSeq > published) operationPublishedRequestSeqs.set(name, requestSeq);
}

/** operationReadPending reports whether a read for this exact operation is
 * still in flight: the section's tick skips a pending operation instead of
 * piling another read on top of a slow one. Keyed by the record id, so a
 * superseded operation's lingering read never stalls its replacement. */
export function operationReadPending(name: string, id: string): boolean {
  return (operationReadsPending.get(name)?.get(id)?.size ?? 0) > 0;
}

function beginOperationRead(name: string, id: string): object {
  const token = {};
  const byId = operationReadsPending.get(name) ?? new Map<string, Set<object>>();
  const tokens = byId.get(id) ?? new Set<object>();
  tokens.add(token);
  byId.set(id, tokens);
  operationReadsPending.set(name, byId);
  return token;
}

function endOperationRead(name: string, id: string, token: object): void {
  const byId = operationReadsPending.get(name);
  const tokens = byId?.get(id);
  if (byId === undefined || tokens === undefined) return;
  tokens.delete(token);
  if (tokens.size === 0) byId.delete(id);
  if (byId.size === 0) operationReadsPending.delete(name);
}

/** releaseOperationReads drops every pending read for the name: unmount
 * cancels the poll, so its skip entries must not survive into a remount's
 * first tick and block it for up to the read timeout. The released tokens can
 * no longer release anything when their reads settle. */
function releaseOperationReads(name: string): void {
  operationReadsPending.delete(name);
}

function planIsCurrent(name: string, sequence: number, client: AppwireClientLike | null): boolean {
  return planSequence(name) === sequence && connectionStore.getState().client === client;
}
/** planSequenceSuperseded answers whether a newer plan call (or a discard)
 * owns name's state, so the older request must publish nothing. */
function planSequenceSuperseded(name: string, sequence: number): boolean {
  return planSequence(name) !== sequence;
}

/** operationPollIsCurrent answers whether a poll read may publish: it is still
 * the newest read issued for the name, and it came through the connection that
 * is current now (a replaced connection's answer describes the hub that was). */
function operationPollIsCurrent(name: string, sequence: number, client: AppwireClientLike | null): boolean {
  return operationSequence(name) === sequence && connectionStore.getState().client === client;
}

/** The terminal arm a plan publishes when its connection was replaced while
 * the request was out. The response describes the hub that was and no writer
 * for the old client remains, so leaving the planning state up would spin
 * forever; re-planning against the new connection is the way out. */
const PLAN_CONNECTION_CHANGED_MESSAGE = "The hub connection changed while this plan was being built; plan again.";

function planConnectionChangedArm(): HostPlanPhase {
  return { phase: "error", refusal: { kind: "unknown", message: PLAN_CONNECTION_CHANGED_MESSAGE }, recovery: "replan" };
}

/** The refusal a restart records when the registry answers no current pair for
 * the host - a failed forced read, or a name no longer listed: there is
 * nothing to confirm against until the registry answers. */
const RESTART_PAIR_UNKNOWN_REFUSAL: HostOpRefusal = {
  kind: "unknown",
  message: "The host registry answered no current pair for this host; re-read the host list and retry.",
};
function restartIsCurrent(name: string, sequence: number, client: AppwireClientLike | null): boolean {
  return restartSequence(name) === sequence && connectionStore.getState().client === client;
}

/** currentPairFor reads name's pair from the registry's current ready
 * snapshot, for the restart stale-entry retry - the pair comes from a row the
 * registry answered, never a fabricated one (stores/hosts.ts's rule). */
function currentPairFor(name: string): HostMutationPair | undefined {
  const load = hostsStore.getState().load;
  if (load.phase !== "ready") return undefined;
  const row = load.hosts.find((candidate) => candidate.name === name);
  if (row === undefined) return undefined;
  return { generation: row.generation, incarnationId: row.incarnationId };
}

/** operationRef is the seed the deploy/restart response leaves (08b §10: the
 * response carries only the id, client operation ID, and state): the kind comes
 * from the call that started the operation, the identity from what that call
 * knew (the plan's generation, the restart's intended pair), and the poll fills
 * the body. */
function operationRef(
  result: { id: string; clientOperationId: string; state: string },
  kind: string,
  identity: { host: string; generation?: number; incarnationId?: string },
): HostOperationRef {
  return {
    id: result.id,
    clientOperationId: result.clientOperationId,
    kind,
    state: result.state,
    progress: [],
    host: identity.host,
    ...(identity.generation === undefined ? {} : { generation: identity.generation }),
    ...(identity.incarnationId === undefined ? {} : { incarnationId: identity.incarnationId }),
  };
}

/** OPERATION_READ_TIMEOUT_MS bounds one `evener/host/operations` read. The call
 * is a cheap controller-local store read (08b §6: it never dials), so a short
 * bound surfaces a stalled connection as the visible stopped-progress state
 * instead of hanging the poll. */
export const OPERATION_READ_TIMEOUT_MS = 15_000;

/** readOperationRecord reads one operation record by its controller-assigned
 * id, host-pinned: `{name, id}` is the narrowest exact read 08b §10 offers
 * (the `id` detail filter resolves the record directly, and `name` pins the
 * page's window to this host so no cross-host boundary map is minted). */
async function readOperationRecord(
  client: AppwireClientLike,
  name: string,
  id: string,
): Promise<OperationRecord | undefined> {
  const response = await client.request(
    "evener/host/operations",
    { name, id },
    { timeoutMs: OPERATION_READ_TIMEOUT_MS },
  );
  return response.operations.find((candidate) => candidate.id === id);
}

/** recordToOperationRef projects a wire record onto the ref the row renders.
 * `compacted` is read structurally: the generated base types do not name S6's
 * tombstone-replay marker yet, and the projection must keep it the moment the
 * marker lands (a replay renders as the operation's past outcome). */
function recordToOperationRef(record: OperationRecord): HostOperationRef {
  const ref: HostOperationRef = {
    id: record.id,
    clientOperationId: record.clientOperationId,
    kind: record.kind,
    state: record.state,
    progress: record.progress ?? [],
    host: record.host,
    generation: record.generation,
    incarnationId: record.incarnationId,
    fetched: true,
  };
  if (record.result !== undefined) ref.result = { ok: record.result.ok, message: record.result.message };
  if ((record as { compacted?: unknown }).compacted === true) ref.compacted = true;
  return ref;
}

// OperationSet/OperationGet are the slices of the store's accessors the
// operation publish helpers take; the store passes its own set/get in.
type OperationSet = (updater: (previous: HostOpsStoreState) => Partial<HostOpsStoreState>) => void;
type OperationGet = () => HostOpsStoreState;

/** publishStartedOperation seeds one started operation — but only while no
 * STRICTLY NEWER deploy/restart request has already PUBLISHED for the name.
 * `requestSeq` is that request's token from nextOperationRequestSeq (per host
 * name, bumped at issue): two requests that both publish are ordered by the
 * published token whatever the arrival order, so the newer response wins; but
 * a newer request that never publishes — refused, or still in flight — must
 * not suppress an older valid response, whose operation exists server-side
 * and whose record must be tracked. A dialog close alone does not bump the
 * token, so a superseded CONFIRMATION with no newer published request still
 * publishes (S14's designed case), and neither does unmount (the section's
 * stopOperationPoll bumps the read sequence, not this one). On a real publish
 * it supersedes any in-flight read for the name: the previous operation's poll
 * must not publish onto the record this one just answered. */
function publishStartedOperation(set: OperationSet, name: string, ref: HostOperationRef, requestSeq: number): void {
  let published = false;
  set((previous) => {
    if (!operationRequestPublished(name, requestSeq)) return previous;
    published = true;
    return { operations: { ...previous.operations, [name]: ref } };
  });
  if (published) {
    markOperationRequestPublished(name, requestSeq);
    operationSequences.set(name, operationSequence(name) + 1);
  }
}

/** publishOperationRecord merges one read's record onto the ref the row
 * renders — only while the same (name, id) is still the tracked one. A first
 * observation of a settled state nudges the host rows: 08b §6's worker
 * publishes the verified post-operation facts before marking the record
 * `complete`, so the row's version signal can be current now instead of
 * waiting for the pane's own poll. */
function publishOperationRecord(
  set: OperationSet,
  get: OperationGet,
  name: string,
  id: string,
  record: OperationRecord,
): void {
  const held = get().operations[name];
  if (held === undefined || held.id !== id) return;
  const wasSettled = operationStateSettled(held.state);
  // A seed (the deploy/restart response) carries no result/progress: its first
  // full read is a refresh-worthy terminal observation too, so a replayed
  // (deduplicated) operation does not leave the row on stale facts.
  const firstBody = held.fetched !== true;
  set((previous) => {
    const current = previous.operations[name];
    if (current === undefined || current.id !== id) return previous;
    return { operations: { ...previous.operations, [name]: recordToOperationRef(record) } };
  });
  if ((!wasSettled || firstBody) && operationStateSettled(record.state)) {
    // A failed refresh is the background poll's own business (it keeps the
    // last rows); it must never surface as an unhandled rejection here.
    void hostsStore
      .getState()
      .refresh()
      .catch(() => undefined);
  }
}

/** publishOperationReadFailure records why progress updates stopped, leaving
 * the last-known state and progress rendered underneath (the loop keeps
 * retrying: a transient failure never ends polling). */
function publishOperationReadFailure(set: OperationSet, name: string, id: string, refusal: HostOpRefusal): void {
  set((previous) => {
    const current = previous.operations[name];
    if (current === undefined || current.id !== id) return previous;
    // A read that is no longer the newest must not strand a stopped-progress
    // error on a record whose body was already read and settled: nothing would
    // ever clear it, because a settled record owes no further read.
    if (current.fetched === true && operationStateSettled(current.state)) return previous;
    // The same refusal seen again is not a new fact: republishing it every tick
    // would re-render and re-announce the row's alert every second while the
    // hub stays away.
    if (
      current.readRefusal !== undefined &&
      current.readRefusal.kind === refusal.kind &&
      current.readRefusal.message === refusal.message
    ) {
      return previous;
    }
    return { operations: { ...previous.operations, [name]: { ...current, readRefusal: refusal } } };
  });
}

/** markOperationGone records that the hub retains no record for this id: the
 * outcome cannot be shown now, and no further read can change that, so the
 * loop stops. */
function markOperationGone(set: OperationSet, name: string, id: string): void {
  set((previous) => {
    const current = previous.operations[name];
    if (current === undefined || current.id !== id) return previous;
    return { operations: { ...previous.operations, [name]: { ...current, readRefusal: undefined, gone: true } } };
  });
}

/** retryViewOf projects one `teardown-retry` arm (registry spec 08 §11's six
 * outcome x hostKind arms) onto the view the surfaces render. */
function retryViewOf(result: HostTeardownRetryResult, fallbackRemnantId: string): HostTeardownRetryView {
  const view: HostTeardownRetryView = {
    outcome: result.outcome,
    remnantId: result.remnantId === "" ? fallbackRemnantId : result.remnantId,
    hostKind: result.hostKind,
    hostName: result.host.name,
    hostRemoved: result.host.removed === true,
  };
  if (result.escalationAgeSec !== undefined) view.escalationAgeSec = result.escalationAgeSec;
  if ("seam" in result && result.seam !== undefined) view.seam = result.seam;
  return view;
}

/** clearViewOf projects `teardown-recover`'s single `recovered-cleared` arm. */
function clearViewOf(result: HostTeardownRecoverResult, fallbackRemnantId: string): HostTeardownClearView {
  return {
    remnantId: result.remnantId === "" ? fallbackRemnantId : result.remnantId,
    clearedName: result.clearedName,
    clearedAt: result.clearedAt,
    hostKind: result.hostKind,
  };
}

/** repairConnectionChangedRefusal is the refusal a repair records when its
 * connection was replaced while the request was out: the response describes
 * the hub that was, and the arm is never a silent stall. */
function repairConnectionChangedRefusal(call: "teardown-retry" | "teardown-recover"): HostOpRefusal {
  return {
    kind: "unknown",
    message: `The hub connection changed while the ${call} request was in flight; retry the repair.`,
  };
}

function repairRefusalState(remnantId: string, action: "retry" | "recover", refusal: HostOpRefusal): HostRemnantRepair {
  return { phase: "refused", remnantId, action, refusal };
}

/** The refusal both repair actions publish when called with no remnant id:
 * nothing was submitted, and saying so beats a silent no-op. */
function missingRemnantIdRefusal(action: "retry" | "recover"): HostOpRefusal {
  return {
    kind: "unknown",
    message: `No remnant id is named for this repair; re-read the host list and ${action === "retry" ? "retry" : "recover"} again.`,
  };
}

export const hostOpsStore = create<HostOpsStoreState>((set, get) => ({
  plans: {},
  restarts: {},
  operations: {},
  repairs: {},

  plan: async (name, opts) => {
    let client: AppwireClientLike;
    try {
      client = requireClient();
    } catch (error) {
      // Attach the refusal to the CURRENT entry: a plan already in flight owns
      // the planning state and must not be clobbered by this pre-request
      // failure, while any settled state is the honest target.
      set((previous) => {
        const current = previous.plans[name];
        if (current?.phase === "planning") return previous;
        return {
          plans: {
            ...previous.plans,
            [name]: { phase: "error", refusal: hostOpRefusal(error, "plan"), recovery: "replan" },
          },
        };
      });
      return;
    }
    const sequence = planSequence(name) + 1;
    planSequences.set(name, sequence);
    set((previous) => ({ plans: { ...previous.plans, [name]: { phase: "planning" } } }));
    try {
      const result = await client.request("evener/host/plan", { name }, { timeoutMs: HOST_GATE_TIMEOUT_MS });
      if (planSequenceSuperseded(name, sequence)) return;
      if (connectionStore.getState().client !== client) {
        set((previous) => ({ plans: { ...previous.plans, [name]: planConnectionChangedArm() } }));
        return;
      }
      if (result.outcome === "planned") {
        set((previous) => ({
          plans: {
            ...previous.plans,
            [name]: {
              phase: "planned",
              plan: result.plan,
              token: result.token,
              operationId: createSecureUUID(),
              notice: opts?.notice ?? null,
              refusal: null,
            },
          },
        }));
      } else {
        set((previous) => ({
          plans: {
            ...previous.plans,
            [name]: {
              phase: "no-token",
              staleFacts: result.staleFacts,
              terminal: result.terminal,
              remnantId: result.remnantId ?? null,
            },
          },
        }));
      }
    } catch (error) {
      if (planSequenceSuperseded(name, sequence)) return;
      if (connectionStore.getState().client !== client) {
        set((previous) => ({ plans: { ...previous.plans, [name]: planConnectionChangedArm() } }));
        return;
      }
      const refusal = hostOpRefusal(error, "plan");
      set((previous) => ({
        plans: { ...previous.plans, [name]: { phase: "error", refusal, recovery: planRefusalAction(refusal.kind) } },
      }));
    }
  },

  deploy: async (name) => {
    const state = get().plans[name];
    if (state === undefined || state.phase !== "planned") return;
    let client: AppwireClientLike;
    try {
      client = requireClient();
    } catch (error) {
      // Read the entry inside the setter and attach the refusal only while the
      // confirmation this call intended is still the one displayed - a newer
      // plan/token/ID minted meanwhile is never overwritten by a stale
      // snapshot.
      set((previous) => {
        const current = previous.plans[name];
        if (current === undefined || current.phase !== "planned") return previous;
        return { plans: { ...previous.plans, [name]: { ...current, refusal: hostOpRefusal(error) } } };
      });
      return;
    }
    const sequence = planSequence(name);
    const token = state.token;
    // A conflicting-operation-id refusal means this client's operation ID is
    // already used up by another operation: the retry starts fresh under a new
    // ID, and that ID is persisted before the request so a following
    // lost-response retry reuses the same one (08b §6 step 1).
    let operationId = state.operationId;
    if (state.refusal?.kind === "conflicting-operation-id") {
      operationId = createSecureUUID();
      set((previous) => ({
        plans: { ...previous.plans, [name]: { ...state, operationId, refusal: null } },
      }));
    }
    // The request-currency token this response must still match at publish
    // time (see publishStartedOperation): captured when the request is issued.
    const requestSeq = nextOperationRequestSeq(name);
    // The identity this operation is submitted against, captured at ISSUE
    // time: a remove/re-add while the request is in flight must not tag the
    // operation with the new incarnation's pair. The row is authoritative for
    // identity; the plan's generation is the fallback when no row is loaded.
    // The first read replaces both with the record's own pair.
    const issuedPair = currentPairFor(name);
    try {
      const result = await client.request(
        "evener/host/deploy",
        { name, token, operationId },
        { timeoutMs: HOST_GATE_TIMEOUT_MS },
      );
      // The record is published even if the confirmation was superseded while
      // the request was out (the operation exists server-side and S15's polling
      // needs its id), but only while the connection that sent it is still
      // current: a record from a replaced connection describes the hub that
      // was and must not seed this name's polling.
      if (connectionStore.getState().client === client) {
        publishStartedOperation(
          set,
          name,
          operationRef(result, "deploy", {
            host: name,
            generation: issuedPair?.generation ?? state.plan.generation,
            ...(issuedPair === undefined ? {} : { incarnationId: issuedPair.incarnationId }),
          }),
          requestSeq,
        );
      }
      if (!planIsCurrent(name, sequence, client)) return;
      set((previous) => {
        const current = previous.plans[name];
        if (current === undefined || current.phase !== "planned" || current.operationId !== operationId)
          return previous;
        return { plans: { ...previous.plans, [name]: { phase: "started" } } };
      });
    } catch (error) {
      if (!planIsCurrent(name, sequence, client)) return;
      const refusal = hostOpRefusal(error);
      if (deployRefusalAction(refusal.kind) === "replan") {
        // §13: deploy rejected the token as stale, so the UI re-plans and
        // re-renders the confirmation from the new response BEFORE any retry.
        await get().plan(name, {
          notice: `${refusal.message} A fresh plan is shown below; review it and deploy again.`,
        });
        return;
      }
      set((previous) => {
        const current = previous.plans[name];
        if (current === undefined || current.phase !== "planned") return previous;
        return { plans: { ...previous.plans, [name]: { ...current, refusal } } };
      });
    }
  },

  connectAndPlan: async (name) => {
    const sequence = planSequence(name);
    const client = connectionStore.getState().client;
    try {
      await hostsStore.getState().connect(name);
    } catch (error) {
      // A close-and-reopen (a newer plan) or a replaced connection owns the
      // state now; a stale recovery never overwrites it.
      if (!planIsCurrent(name, sequence, client)) return;
      set((previous) => ({
        plans: {
          ...previous.plans,
          [name]: { phase: "error", refusal: hostOpRefusal(error, "connect"), recovery: "connect" },
        },
      }));
      return;
    }
    // Connect resolved, but a close-and-reopen may have superseded this
    // recovery while it was in flight: the newer state owns the sequence, and
    // the stale recovery must not mint a plan over it.
    if (!planIsCurrent(name, sequence, client)) return;
    await get().plan(name);
  },

  discardPlan: (name) => {
    planSequences.set(name, planSequence(name) + 1);
    set((previous) => {
      const plans = { ...previous.plans };
      delete plans[name];
      return { plans };
    });
  },

  beginRestart: (name, pair) => {
    restartSequences.set(name, restartSequence(name) + 1);
    set((previous) => ({
      restarts: {
        ...previous.restarts,
        [name]: { phase: "confirm", pair, operationId: createSecureUUID(), refusal: null },
      },
    }));
  },

  restart: async (name) => {
    const attempt = get().restarts[name];
    if (attempt === undefined || attempt.phase === "started") return;
    let client: AppwireClientLike;
    try {
      client = requireClient();
    } catch (error) {
      // Read the entry inside the setter: only the attempt still displayed is
      // failed, never a newer pair/ID a concurrent action minted.
      set((previous) => {
        const current = previous.restarts[name];
        if (current === undefined || current.phase === "started") return previous;
        return {
          restarts: { ...previous.restarts, [name]: { ...current, phase: "failed", refusal: hostOpRefusal(error) } },
        };
      });
      return;
    }
    const sequence = restartSequence(name);
    // A conflicting-operation-id retry starts under a fresh ID, persisted
    // before the retry so a following lost-response retry reuses it.
    let operationId = attempt.operationId;
    if (attempt.refusal?.kind === "conflicting-operation-id") {
      operationId = createSecureUUID();
      set((previous) => ({
        restarts: { ...previous.restarts, [name]: { ...attempt, operationId, refusal: null } },
      }));
    }
    const send = (pair: HostMutationPair, id: string) =>
      client.request(
        "evener/host/restart",
        { name, operationId: id, generation: pair.generation, incarnationId: pair.incarnationId },
        { timeoutMs: HOST_GATE_TIMEOUT_MS },
      );
    try {
      const requestSeq = nextOperationRequestSeq(name);
      const result = await send(attempt.pair, operationId);
      if (connectionStore.getState().client === client) {
        publishStartedOperation(
          set,
          name,
          operationRef(result, "restart", {
            host: name,
            generation: attempt.pair.generation,
            incarnationId: attempt.pair.incarnationId,
          }),
          requestSeq,
        );
      }
      if (!restartIsCurrent(name, sequence, client)) return;
      set((previous) => {
        const current = previous.restarts[name];
        if (current === undefined || current.phase === "started") return previous;
        return {
          restarts: {
            ...previous.restarts,
            [name]: { phase: "started", pair: attempt.pair, operationId, refusal: null },
          },
        };
      });
      return;
    } catch (error) {
      if (!restartIsCurrent(name, sequence, client)) return;
      const refusal = hostOpRefusal(error);
      if (refusal.kind !== "stale-entry") {
        set((previous) => {
          const current = previous.restarts[name];
          if (current === undefined || current.phase === "started") return previous;
          return { restarts: { ...previous.restarts, [name]: { ...current, phase: "failed", refusal } } };
        });
        return;
      }
      // The pair the operator saw moved: take a FORCED registry read and retry
      // ONCE with the pair it now answers, under a fresh operation ID - the
      // retry shape stores/hosts.ts's guardedMutation uses (its own quietReRead
      // reads past the coalescing refresh the same way; a poll already in
      // flight carries the pre-move snapshot and would consume the one retry
      // without moving). A second stale-entry surfaces instead of looping
      // (only a person can decide what is next).
      const published = await hostsStore.getState().reReadForced();
      if (!restartIsCurrent(name, sequence, client)) return;
      if (!published) {
        // The forced read failed (or a newer response owned the publish): the
        // snapshot on hand is the one this refusal proved stale, so there is
        // no pair to retry against - fail rather than replay the stale pair.
        set((previous) => {
          const held = previous.restarts[name];
          if (held === undefined || held.phase === "started") return previous;
          return {
            restarts: {
              ...previous.restarts,
              [name]: { ...held, phase: "failed", refusal: RESTART_PAIR_UNKNOWN_REFUSAL },
            },
          };
        });
        return;
      }
      const current = currentPairFor(name);
      if (current === undefined) {
        set((previous) => {
          const held = previous.restarts[name];
          if (held === undefined || held.phase === "started") return previous;
          return {
            restarts: {
              ...previous.restarts,
              [name]: { ...held, phase: "failed", refusal: RESTART_PAIR_UNKNOWN_REFUSAL },
            },
          };
        });
        return;
      }
      const retryId = createSecureUUID();
      // Persist the refreshed coordinates BEFORE the retry: if it fails, the
      // attempt must name the pair and operation ID it actually used, so the
      // operator's next attempt never resubmits the stale pair under the
      // retired key - the same discipline the conflicting-operation-id path
      // uses above.
      set((previous) => {
        const held = previous.restarts[name];
        if (held === undefined || held.phase === "started") return previous;
        return { restarts: { ...previous.restarts, [name]: { ...held, pair: current, operationId: retryId } } };
      });
      try {
        const requestSeq = nextOperationRequestSeq(name);
        const result = await send(current, retryId);
        if (connectionStore.getState().client === client) {
          publishStartedOperation(
            set,
            name,
            operationRef(result, "restart", {
              host: name,
              generation: current.generation,
              incarnationId: current.incarnationId,
            }),
            requestSeq,
          );
        }
        if (!restartIsCurrent(name, sequence, client)) return;
        set((previous) => {
          const held = previous.restarts[name];
          if (held === undefined || held.phase === "started") return previous;
          return {
            restarts: {
              ...previous.restarts,
              [name]: { phase: "started", pair: current, operationId: retryId, refusal: null },
            },
          };
        });
      } catch (retryError) {
        if (!restartIsCurrent(name, sequence, client)) return;
        const retryRefusal = hostOpRefusal(retryError);
        set((previous) => {
          const held = previous.restarts[name];
          if (held === undefined || held.phase === "started") return previous;
          return { restarts: { ...previous.restarts, [name]: { ...held, phase: "failed", refusal: retryRefusal } } };
        });
      }
    }
  },

  connectAndRestart: async (name) => {
    const sequence = restartSequence(name);
    const client = connectionStore.getState().client;
    try {
      await hostsStore.getState().connect(name);
    } catch (error) {
      // A close-and-reopen (or a replaced connection) owns the state now; a
      // stale recovery never overwrites it.
      if (!restartIsCurrent(name, sequence, client)) return;
      const refusal = hostOpRefusal(error, "connect");
      set((previous) => {
        const current = previous.restarts[name];
        if (current === undefined || current.phase === "started") return previous;
        return { restarts: { ...previous.restarts, [name]: { ...current, phase: "failed", refusal } } };
      });
      return;
    }
    // Connect succeeded: re-seed the confirmation against what the registry NOW
    // answers. The refused attempt's pair is exactly what must not be resubmitted
    // blindly, so the operator confirms against the current pair instead.
    const published = await hostsStore.getState().reReadForced();
    if (!restartIsCurrent(name, sequence, client)) return;
    const pair = published ? currentPairFor(name) : undefined;
    set((previous) => {
      const current = previous.restarts[name];
      if (current === undefined || current.phase === "started") return previous;
      if (pair === undefined) {
        return {
          restarts: {
            ...previous.restarts,
            [name]: { ...current, phase: "failed", refusal: RESTART_PAIR_UNKNOWN_REFUSAL },
          },
        };
      }
      return {
        restarts: {
          ...previous.restarts,
          [name]: { phase: "confirm", pair, operationId: createSecureUUID(), refusal: null },
        },
      };
    });
  },

  discardRestart: (name) => {
    restartSequences.set(name, restartSequence(name) + 1);
    set((previous) => {
      const restarts = { ...previous.restarts };
      delete restarts[name];
      return { restarts };
    });
  },

  reSeedRestart: async (name) => {
    // The continuation a resolved remnant earns on the restart surface: the
    // pair the refusal was made against may have moved while the repair ran
    // (and the remnant may have belonged to a remove), so re-read first and
    // never replay the old pair. reReadForced issues a new read rather than
    // joining an in-flight poll that predates the resolution.
    //
    // Identity captured BEFORE the read: a newer attempt (a re-opened dialog)
    // or a replaced connection owns the state if either moved while it was
    // out, and this continuation then publishes nothing.
    const sequence = restartSequence(name);
    const client = connectionStore.getState().client;
    const published = await hostsStore.getState().reReadForced();
    if (!restartIsCurrent(name, sequence, client)) return;
    const pair = published ? currentPairFor(name) : undefined;
    set((previous) => {
      const current = previous.restarts[name];
      if (current === undefined || current.phase === "started") return previous;
      if (pair === undefined) {
        return {
          restarts: {
            ...previous.restarts,
            [name]: { ...current, phase: "failed", refusal: RESTART_PAIR_UNKNOWN_REFUSAL },
          },
        };
      }
      return {
        restarts: {
          ...previous.restarts,
          [name]: { phase: "confirm", pair, operationId: createSecureUUID(), refusal: null },
        },
      };
    });
  },

  pollOperation: async (name) => {
    const ref = get().operations[name];
    if (ref === undefined || !operationNeedsRead(ref)) return;
    let client: AppwireClientLike;
    try {
      client = requireClient();
    } catch (error) {
      publishOperationReadFailure(set, name, ref.id, hostOpRefusal(error, "progress"));
      return;
    }
    // Per-issue sequence: this read owns the newest position, so any earlier
    // read (a direct caller's overlap) is dropped whole — its record and its
    // refusal alike. The tick's own skip keeps the interval at one read per
    // name, so this never starves a publish.
    const sequence = operationSequence(name) + 1;
    operationSequences.set(name, sequence);
    const readToken = beginOperationRead(name, ref.id);
    try {
      try {
        const record = await readOperationRecord(client, name, ref.id);
        if (!operationPollIsCurrent(name, sequence, client)) return;
        if (record === undefined) {
          markOperationGone(set, name, ref.id);
          return;
        }
        publishOperationRecord(set, get, name, ref.id, record);
      } catch (error) {
        if (!operationPollIsCurrent(name, sequence, client)) return;
        const refusal = hostOpRefusal(error, "progress");
        if (refusal.kind !== "cursor-invalidated") {
          publishOperationReadFailure(set, name, ref.id, refusal);
          return;
        }
        // 08b §8: compaction invalidated the position this read resumed from,
        // and the fix is a fresh first-page read. This poll never presents a
        // cursor, so the arm cannot arise with its shape; if it ever does, retry
        // ONCE from scratch rather than surfacing the refusal, and never loop on
        // the invalidated position.
        try {
          const record = await readOperationRecord(client, name, ref.id);
          if (!operationPollIsCurrent(name, sequence, client)) return;
          if (record === undefined) {
            markOperationGone(set, name, ref.id);
            return;
          }
          publishOperationRecord(set, get, name, ref.id, record);
        } catch (retryError) {
          if (!operationPollIsCurrent(name, sequence, client)) return;
          publishOperationReadFailure(set, name, ref.id, hostOpRefusal(retryError, "progress"));
        }
      }
    } finally {
      endOperationRead(name, ref.id, readToken);
    }
  },

  teardownRetry: async (name, remnantId) => {
    const id = remnantId.trim();
    if (id === "") {
      // Nothing to submit and nothing to name: publish the refusal instead of
      // a silent no-op the operator cannot see. (The dialog is gated on a
      // non-empty id too; this is the store's own guard.)
      set((previous) => ({
        repairs: { ...previous.repairs, [name]: repairRefusalState(id, "retry", missingRemnantIdRefusal("retry")) },
      }));
      return;
    }
    let client: AppwireClientLike;
    try {
      client = requireClient();
    } catch (error) {
      // Attach the refusal only while no repair submission is in flight: an
      // in-flight request owns the displayed state and publishes its own arm.
      set((previous) => {
        const current = previous.repairs[name];
        if (current?.phase === "retrying" || current?.phase === "recovering") return previous;
        return {
          repairs: { ...previous.repairs, [name]: repairRefusalState(id, "retry", hostOpRefusal(error, "teardown")) },
        };
      });
      return;
    }
    const sequence = repairSequence(name) + 1;
    repairSequences.set(name, sequence);
    set((previous) => ({ repairs: { ...previous.repairs, [name]: { phase: "retrying", remnantId: id } } }));
    try {
      const result = await client.request(
        "evener/host/teardown-retry",
        { remnantId: id },
        { timeoutMs: HOST_GATE_TIMEOUT_MS },
      );
      // A newer repair request owns the state now: this response publishes
      // nothing. A REPLACED connection is next: the response describes the hub
      // that was, so record that instead of silently stalling the panel.
      if (repairSequence(name) !== sequence) return;
      if (connectionStore.getState().client !== client) {
        set((previous) => ({
          repairs: {
            ...previous.repairs,
            [name]: repairRefusalState(id, "retry", repairConnectionChangedRefusal("teardown-retry")),
          },
        }));
        return;
      }
      const view = retryViewOf(result, id);
      set((previous) => ({
        repairs: { ...previous.repairs, [name]: { phase: "retried", remnantId: id, result: view } },
      }));
      // EVERY accepted arm converges the rows, the failure arm included: a
      // committed-with-teardown-failure response can still have moved the row
      // (a tombstone, updated escalation data), and the row-level affordance
      // must reflect it. reReadForced issues a NEW read rather than joining an
      // in-flight poll that predates this response; best-effort, like S15's
      // post-operation refresh.
      void hostsStore
        .getState()
        .reReadForced()
        .catch(() => undefined);
    } catch (error) {
      if (repairSequence(name) !== sequence) return;
      const refusal =
        connectionStore.getState().client === client
          ? hostOpRefusal(error, "teardown")
          : repairConnectionChangedRefusal("teardown-retry");
      set((previous) => ({ repairs: { ...previous.repairs, [name]: repairRefusalState(id, "retry", refusal) } }));
    }
  },

  teardownRecover: async (name, remnantId, attestation) => {
    const id = remnantId.trim();
    if (id === "") {
      set((previous) => ({
        repairs: { ...previous.repairs, [name]: repairRefusalState(id, "recover", missingRemnantIdRefusal("recover")) },
      }));
      return;
    }
    let client: AppwireClientLike;
    try {
      client = requireClient();
    } catch (error) {
      set((previous) => {
        const current = previous.repairs[name];
        if (current?.phase === "retrying" || current?.phase === "recovering") return previous;
        return {
          repairs: { ...previous.repairs, [name]: repairRefusalState(id, "recover", hostOpRefusal(error, "teardown")) },
        };
      });
      return;
    }
    const sequence = repairSequence(name) + 1;
    repairSequences.set(name, sequence);
    set((previous) => ({ repairs: { ...previous.repairs, [name]: { phase: "recovering", remnantId: id } } }));
    try {
      const result = await client.request(
        "evener/host/teardown-recover",
        {
          remnantId: id,
          attestation: {
            operator: attestation.operator,
            statement: attestation.statement,
            observedAt: attestation.observedAt,
          },
        },
        { timeoutMs: HOST_GATE_TIMEOUT_MS },
      );
      if (repairSequence(name) !== sequence) return;
      if (connectionStore.getState().client !== client) {
        set((previous) => ({
          repairs: {
            ...previous.repairs,
            [name]: repairRefusalState(id, "recover", repairConnectionChangedRefusal("teardown-recover")),
          },
        }));
        return;
      }
      set((previous) => ({
        repairs: { ...previous.repairs, [name]: { phase: "cleared", remnantId: id, result: clearViewOf(result, id) } },
      }));
      // The cleared remnant changes the row (it disappears from the list):
      // converge it now rather than waiting for the pane's own poll.
      void hostsStore
        .getState()
        .reReadForced()
        .catch(() => undefined);
    } catch (error) {
      if (repairSequence(name) !== sequence) return;
      const refusal =
        connectionStore.getState().client === client
          ? hostOpRefusal(error, "teardown")
          : repairConnectionChangedRefusal("teardown-recover");
      set((previous) => ({ repairs: { ...previous.repairs, [name]: repairRefusalState(id, "recover", refusal) } }));
    }
  },

  clearRepair: (name) => {
    repairSequences.set(name, repairSequence(name) + 1);
    set((previous) => {
      const repairs = { ...previous.repairs };
      delete repairs[name];
      return { repairs };
    });
  },

  clearRepairRefusal: (name) => {
    // Fence-only clear: a live retry/recover keeps its sequence, so its arm
    // still publishes when it lands. Only a settled refusal is dropped.
    set((previous) => {
      const current = previous.repairs[name];
      if (current === undefined || current.phase !== "refused") return previous;
      const repairs = { ...previous.repairs };
      delete repairs[name];
      return { repairs };
    });
  },

  stopOperationPoll: (name) => {
    operationSequences.set(name, operationSequence(name) + 1);
    releaseOperationReads(name);
  },

  resetForTests: () => {
    planSequences.clear();
    restartSequences.clear();
    operationSequences.clear();
    operationRequestSequences.clear();
    operationPublishedRequestSeqs.clear();
    repairSequences.clear();
    operationReadsPending.clear();
    set({ plans: {}, restarts: {}, operations: {}, repairs: {} });
  },
}));

export function useHostOpsStore<T>(selector: (state: HostOpsStoreState) => T): T {
  return useStore(hostOpsStore, selector);
}
