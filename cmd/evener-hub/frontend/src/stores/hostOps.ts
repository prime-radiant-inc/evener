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
// `operations` polling (S15) and the remnant/orphan affordances (S16) are NOT
// here: a started operation's record is retained under `operations` as the seed
// S15's polling consumes, and the fenced/busy refusals render their concrete
// message with no bare retry (the open/wait and teardown-retry affordances are
// later slices').

import type { AppwireClientLike, HostPlan, HostPlanStaleFacts } from "@evener/appwire-client";
import { friendlyErrorMessage, RequestTimeoutError, WireError } from "@evener/appwire-client";
import { create, useStore } from "zustand";
import { connectedClientPort, connectionStore } from "./connection";
import { HOST_GATE_TIMEOUT_MS, type HostMutationPair, hostsStore } from "./hosts";
import { createSecureUUID } from "./secureUUID";

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
 * differs per call: only deploy/restart carry an operation ID. */
export type HostOpRequestContext = "plan" | "operation" | "connect";

function timeoutMessage(context: HostOpRequestContext): string {
  switch (context) {
    case "plan":
      return "The hub did not answer before the plan request timed out; retry planning.";
    case "connect":
      return "The hub did not answer before the Connect request timed out; retry.";
    case "operation":
      return "The hub did not answer before the request timed out; retry — a retry repeats the same operation ID.";
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
    case "host-detached":
      return "connect";
    case "remnant-open":
    case "host-busy-operation":
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

/** HostOperationRef is one started operation's record: the seed S15's
 * `operations` polling consumes (registry spec 08 §13's progress rendering). */
export interface HostOperationRef {
  id: string;
  clientOperationId: string;
  state: string;
}

interface HostOpsStoreState {
  plans: Record<string, HostPlanPhase>;
  restarts: Record<string, HostRestartAttempt>;
  operations: Record<string, HostOperationRef>;
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
  discardRestart: (name: string) => void;
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
function planSequence(name: string): number {
  return planSequences.get(name) ?? 0;
}
function restartSequence(name: string): number {
  return restartSequences.get(name) ?? 0;
}

function planIsCurrent(name: string, sequence: number, client: AppwireClientLike | null): boolean {
  return planSequence(name) === sequence && connectionStore.getState().client === client;
}
/** planSequenceSuperseded answers whether a newer plan call (or a discard)
 * owns name's state, so the older request must publish nothing. */
function planSequenceSuperseded(name: string, sequence: number): boolean {
  return planSequence(name) !== sequence;
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

function operationRef(result: { id: string; clientOperationId: string; state: string }): HostOperationRef {
  return { id: result.id, clientOperationId: result.clientOperationId, state: result.state };
}

export const hostOpsStore = create<HostOpsStoreState>((set, get) => ({
  plans: {},
  restarts: {},
  operations: {},

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
      // The generated union carries `outcome` as `string`, so the arms are
      // narrowed by the fields only one of them declares - the same structural
      // discriminator stores/hosts.ts reads for the mutation-result union.
      if ("plan" in result && "token" in result) {
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
        set((previous) => ({ operations: { ...previous.operations, [name]: operationRef(result) } }));
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
      const result = await send(attempt.pair, operationId);
      if (connectionStore.getState().client === client) {
        set((previous) => ({ operations: { ...previous.operations, [name]: operationRef(result) } }));
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
        const result = await send(current, retryId);
        if (connectionStore.getState().client === client) {
          set((previous) => ({ operations: { ...previous.operations, [name]: operationRef(result) } }));
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

  resetForTests: () => {
    planSequences.clear();
    restartSequences.clear();
    set({ plans: {}, restarts: {}, operations: {} });
  },
}));

export function useHostOpsStore<T>(selector: (state: HostOpsStoreState) => T): T {
  return useStore(hostOpsStore, selector);
}
