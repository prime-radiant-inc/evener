// hostOps.test.ts — store-level unit tests for the deploy pipeline's Hosts UI
// state (stores/hostOps.ts): the plan -> confirm -> deploy flow, the no-token
// branch, the token-refusal re-plan, and the §11 refusal vocabulary's concrete
// rendering. Component-level rendering tests live in
// panes/settings/sections/hosts.test.tsx.
//
// Pattern mirrors hosts.test.ts: each test resets the stores and connection in
// beforeEach, and a FakeClient answers the wire methods.

import {
  type HostOperationsResponse,
  type HostPlan,
  type HostRow,
  type OperationRecord,
  type RemovedRow,
  RequestTimeoutError,
  WireError,
} from "@evener/appwire-client";
import { FakeClient, gateSettlements } from "@evener/appwire-client/testing/fakeClient";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import {
  clearedOutcomeLine,
  deployRefusalAction,
  type HostOperationRef,
  hostOperationView,
  hostOpRefusal,
  hostOpRefusalBlocksRetry,
  hostOpsStore,
  operationNeedsRead,
  operationReadPending,
  operationShownOnHost,
  operationStateSettled,
  planNoTokenAction,
  planRefusalAction,
  restartRefusalAction,
  retryOutcomeLine,
  TEARDOWN_RECOVER_STATEMENT,
  teardownRecoverRefusalAction,
  teardownRetryRefusalAction,
} from "./hostOps";
import { hostsStore } from "./hosts";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function row(name: string): HostRow {
  return {
    name,
    generation: 3,
    incarnationId: "inc-3",
    origin: "hub.toml",
    attached: true,
    midAttach: false,
    removed: false,
  };
}

function plan(overrides: Partial<HostPlan> = {}): HostPlan {
  return {
    host: "beta",
    generation: 3,
    targetPath: "/srv/evener/evener",
    controllerRevision: "controller-rev-9",
    restartFollows: true,
    factsRevision: "facts-rev-1",
    hubTomlFingerprint: "fp-1",
    factsCapturedAt: "2026-09-28T07:59:00Z",
    factsAgeSec: 42,
    runningVersion: "1.4.2",
    runningHealthy: true,
    ...overrides,
  };
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  hostsStore.getState().resetForTests();
  hostOpsStore.getState().resetForTests();
});

describe("hostOpRefusal", () => {
  test("the §11 vocabulary renders concrete copy, never the generic failure", () => {
    const cases: Array<{ info: string; data?: Record<string, unknown>; want: string }> = [
      { info: "token-missing", want: "This confirmation's plan token is no longer on the server." },
      { info: "token-mismatched", want: "This confirmation does not match the server's plan token." },
      { info: "token-superseded", want: "A newer plan has superseded this confirmation." },
      { info: "token-expired", want: "This plan expired before the deploy was submitted." },
      { info: "stale-entry", want: "The host changed since this operation was prepared." },
      { info: "conflicting-operation-id", want: "This client operation ID is already used by another operation." },
      { info: "host-detached", want: "The host has no live attached channel." },
      { info: "probe-failed", want: "Probing the host's running state failed." },
      { info: "host-busy-transient", want: "The host is busy right now." },
    ];
    for (const { info, data, want } of cases) {
      const err = new WireError(`hub says: ${info}`, -32013, { evenerErrorInfo: info, ...data });
      const refusal = hostOpRefusal(err);
      expect(refusal.kind).toBe(info);
      expect(refusal.message).toContain(want);
      // The hub's own prose rides along as the detail.
      expect(refusal.message).toContain(`hub says: ${info}`);
      expect(refusal.message).not.toContain("Something went wrong.");
    }
  });

  test("remnant-open names the blocking remnant and blocks retry", () => {
    const err = new WireError('host "beta": fenced', -32013, {
      evenerErrorInfo: "remnant-open",
      remnantId: "remnant-7",
    });
    const refusal = hostOpRefusal(err);
    expect(refusal.kind).toBe("remnant-open");
    expect(refusal.remnantId).toBe("remnant-7");
    expect(refusal.message).toContain("An open teardown remnant is blocking this host (remnant-7).");
    expect(hostOpRefusalBlocksRetry(refusal.kind)).toBe(true);
  });

  test("host-busy-operation names the running operation and blocks retry", () => {
    const err = new WireError("busy", -32013, {
      evenerErrorInfo: "host-busy-operation",
      operationId: "op-42",
    });
    const refusal = hostOpRefusal(err);
    expect(refusal.kind).toBe("host-busy-operation");
    expect(refusal.operationId).toBe("op-42");
    expect(refusal.message).toContain("Another operation is running on this host (op-42).");
    expect(hostOpRefusalBlocksRetry(refusal.kind)).toBe(true);
  });

  test("a timeout says the retry repeats the same operation ID", () => {
    const refusal = hostOpRefusal(new RequestTimeoutError("deploy timed out"));
    expect(refusal.kind).toBe("unknown");
    expect(refusal.message).toContain("did not answer before the request timed out");
    expect(refusal.message).toContain("repeats the same operation ID");
  });

  test("a plan timeout never claims an operation ID", () => {
    // plan mints a token and carries no operation ID: the timeout sentence is
    // per call, so the plan arm says to retry planning instead.
    const refusal = hostOpRefusal(new RequestTimeoutError("plan timed out"), "plan");
    expect(refusal.kind).toBe("unknown");
    expect(refusal.message).toContain("did not answer before the plan request timed out");
    expect(refusal.message).toContain("retry planning");
    expect(refusal.message).not.toContain("operation ID");
  });

  test("a Connect timeout names the Connect step", () => {
    const refusal = hostOpRefusal(new RequestTimeoutError("connect timed out"), "connect");
    expect(refusal.message).toContain("did not answer before the Connect request timed out");
    expect(refusal.message).not.toContain("operation ID");
  });

  test("a hub-unreachable rejection keeps the client's own sentence", () => {
    const refusal = hostOpRefusal(new Error('FakeClient: cannot call "evener/host/deploy" while state is "closed"'));
    expect(refusal.message).toBe("Can't reach the hub right now.");
  });
});

describe("refusal recovery actions", () => {
  test("deploy recovery: re-plan on token/stale drift, retry on conflicts, connect on detach, none on fenced/busy", () => {
    for (const kind of [
      "token-missing",
      "token-mismatched",
      "token-superseded",
      "token-expired",
      "stale-entry",
      "probe-failed",
    ] as const) {
      expect(deployRefusalAction(kind)).toBe("replan");
    }
    expect(deployRefusalAction("conflicting-operation-id")).toBe("retry");
    expect(deployRefusalAction("host-busy-transient")).toBe("retry");
    expect(deployRefusalAction("unknown")).toBe("retry");
    expect(deployRefusalAction("host-detached")).toBe("connect");
    expect(deployRefusalAction("remnant-open")).toBe("none");
    expect(deployRefusalAction("host-busy-operation")).toBe("none");
  });

  test("plan recovery: re-plan for everything retryable, none for the fenced/busy arms", () => {
    expect(planRefusalAction("stale-entry")).toBe("replan");
    expect(planRefusalAction("host-busy-transient")).toBe("replan");
    expect(planRefusalAction("unknown")).toBe("replan");
    expect(planRefusalAction("host-busy-operation")).toBe("none");
    expect(planRefusalAction("remnant-open")).toBe("none");
  });

  test("restart recovery: Connect for a detached host, none for fenced/busy, retry otherwise", () => {
    // A detached host's restart refusal has a supported way out (Connect),
    // and the bare repeat must not stay enabled (the refusal would repeat).
    expect(restartRefusalAction("host-detached")).toBe("connect");
    expect(restartRefusalAction("host-busy-operation")).toBe("none");
    expect(restartRefusalAction("remnant-open")).toBe("none");
    expect(restartRefusalAction("stale-entry")).toBe("retry");
    expect(restartRefusalAction("host-busy-transient")).toBe("retry");
    expect(restartRefusalAction("conflicting-operation-id")).toBe("retry");
    expect(restartRefusalAction("probe-failed")).toBe("retry");
    expect(restartRefusalAction("unknown")).toBe("retry");
  });

  test("no-token recovery: unattached connects, the retry arms re-plan, terminal arms offer nothing", () => {
    expect(planNoTokenAction("unattached", false)).toBe("connect");
    expect(planNoTokenAction("refresh-failed", false)).toBe("replan");
    expect(planNoTokenAction("probe-failed", false)).toBe("replan");
    expect(planNoTokenAction("handler-absent", false)).toBe("replan");
    // remnant-open never re-plans and never connects: a re-plan mints nothing
    // while the remnant is open (registry spec 08 §13).
    expect(planNoTokenAction("remnant-open", false)).toBe("none");
    expect(planNoTokenAction("controller-dirty", true)).toBe("none");
    expect(planNoTokenAction("target-unwritable", true)).toBe("none");
    expect(planNoTokenAction("target-missing-prereq", true)).toBe("none");
    expect(planNoTokenAction("target-unit-findings", true)).toBe("none");
    // An unknown non-terminal reason is never a dead end: it re-plans, the
    // safe recovery planRefusalAction's own default uses. Only remnant-open is
    // explicitly affordance-free.
    expect(planNoTokenAction("some-future-reason", false)).toBe("replan");
  });
});

describe("plan", () => {
  test("publishes the planned arm with its token and a client operation ID", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    await hostOpsStore.getState().plan("beta");

    const state = hostOpsStore.getState().plans.beta;
    expect(state?.phase).toBe("planned");
    if (state?.phase !== "planned") throw new Error("unreachable");
    expect(state.plan.targetPath).toBe("/srv/evener/evener");
    expect(state.token).toBe("tok-1");
    expect(state.operationId).not.toBe("");
    expect(state.notice).toBeNull();
    expect(state.refusal).toBeNull();
    expect(fake.calls.find((c) => c.method === "evener/host/plan")?.params).toEqual({ name: "beta" });
  });

  test("publishes the no-token arm without inventing a plan", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({
      outcome: "no-token",
      staleFacts: {
        message: 'host "beta" is not attached; connect it and plan again',
        attached: false,
        reason: "unattached",
      },
      terminal: false,
    }));
    await hostOpsStore.getState().plan("beta");

    const state = hostOpsStore.getState().plans.beta;
    expect(state?.phase).toBe("no-token");
    if (state?.phase !== "no-token") throw new Error("unreachable");
    expect(state.staleFacts.reason).toBe("unattached");
    expect(state.terminal).toBe(false);
    expect(state.remnantId).toBeNull();
  });

  test("an envelope refusal publishes the classified refusal", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => {
      throw new WireError('host "beta": busy', -32013, { evenerErrorInfo: "host-busy-transient" });
    });
    await hostOpsStore.getState().plan("beta");

    const state = hostOpsStore.getState().plans.beta;
    expect(state?.phase).toBe("error");
    if (state?.phase !== "error") throw new Error("unreachable");
    expect(state.refusal.kind).toBe("host-busy-transient");
    expect(state.refusal.message).toContain("The host is busy right now.");
  });

  test("a response for a discarded plan cannot resurrect its state", async () => {
    const fake = connectFakeClient();
    const settlements = gateSettlements(fake, "evener/host/plan");
    const pending = hostOpsStore.getState().plan("beta");
    await Promise.resolve();
    hostOpsStore.getState().discardPlan("beta");
    settlements[0]!.resolve({ outcome: "planned", plan: plan(), token: "tok-stale" });
    await pending;

    expect(hostOpsStore.getState().plans.beta).toBeUndefined();
  });

  test("an older plan response cannot overwrite a newer one", async () => {
    const fake = connectFakeClient();
    const settlements = gateSettlements(fake, "evener/host/plan");
    const first = hostOpsStore.getState().plan("beta");
    await Promise.resolve();
    const second = hostOpsStore.getState().plan("beta");
    await Promise.resolve();
    settlements[1]!.resolve({ outcome: "planned", plan: plan({ targetPath: "/new" }), token: "tok-2" });
    await second;
    settlements[0]!.resolve({ outcome: "planned", plan: plan({ targetPath: "/old" }), token: "tok-1" });
    await first;

    const state = hostOpsStore.getState().plans.beta;
    if (state?.phase !== "planned") throw new Error("unreachable");
    expect(state.token).toBe("tok-2");
    expect(state.plan.targetPath).toBe("/new");
  });

  test("a response landing after a client replacement publishes the connection-changed arm", async () => {
    const fake = connectFakeClient();
    const settlements = gateSettlements(fake, "evener/host/plan");
    const pending = hostOpsStore.getState().plan("beta");
    await Promise.resolve();
    // The connection is replaced while the plan is out: the response describes
    // the hub that was, so no writer for the old client remains - the planning
    // state must not be left spinning forever.
    connectionStore.getState().connect(new FakeClient("ready"));
    settlements[0]!.resolve({ outcome: "planned", plan: plan(), token: "tok-1" });
    await pending;

    const state = hostOpsStore.getState().plans.beta;
    expect(state?.phase).toBe("error");
    if (state?.phase !== "error") throw new Error("unreachable");
    expect(state.refusal.message).toContain("connection changed while this plan was being built");
    expect(state.recovery).toBe("replan");
  });

  test("a failure landing after a client replacement publishes the same arm", async () => {
    const fake = connectFakeClient();
    const settlements = gateSettlements(fake, "evener/host/plan");
    const pending = hostOpsStore.getState().plan("beta");
    await Promise.resolve();
    connectionStore.getState().connect(new FakeClient("ready"));
    settlements[0]!.reject(new WireError("probe died", -32014, { evenerErrorInfo: "probe-failed" }));
    await pending;

    const state = hostOpsStore.getState().plans.beta;
    expect(state?.phase).toBe("error");
    if (state?.phase !== "error") throw new Error("unreachable");
    // The replacement is the honest answer, not the dead request's own
    // classification: re-planning against the new connection is the way out.
    expect(state.refusal.message).toContain("connection changed while this plan was being built");
    expect(state.recovery).toBe("replan");
  });
});

describe("deploy", () => {
  test("submits exactly the displayed plan's token plus the client operation ID", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    fake.on("evener/host/deploy", () => ({ id: "op-1", clientOperationId: "client-op-1", state: "pending" }));
    await hostOpsStore.getState().plan("beta");
    const planned = hostOpsStore.getState().plans.beta;
    if (planned?.phase !== "planned") throw new Error("unreachable");

    await hostOpsStore.getState().deploy("beta");

    expect(fake.calls.find((c) => c.method === "evener/host/deploy")?.params).toEqual({
      name: "beta",
      token: "tok-1",
      operationId: planned.operationId,
    });
    expect(hostOpsStore.getState().plans.beta?.phase).toBe("started");
    // The record is retained as the seed S15's operations polling consumes.
    expect(hostOpsStore.getState().operations.beta).toEqual({
      id: "op-1",
      clientOperationId: "client-op-1",
      kind: "deploy",
      progress: [],
      state: "pending",
      host: "beta",
      generation: 3,
    });
  });

  test("a missing client attaches the refusal to the current confirmation, never a stale snapshot", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    await hostOpsStore.getState().plan("beta");
    const planned = hostOpsStore.getState().plans.beta;
    if (planned?.phase !== "planned") throw new Error("unreachable");

    connectionStore.setState({ client: null });
    await hostOpsStore.getState().deploy("beta");

    const state = hostOpsStore.getState().plans.beta;
    if (state?.phase !== "planned") throw new Error("unreachable");
    // The refusal lands on the confirmed plan itself, whose token and
    // operation ID survive for the retry.
    expect(state.token).toBe("tok-1");
    expect(state.operationId).toBe(planned.operationId);
    expect(state.refusal).not.toBeNull();
    expect(fake.calls.filter((c) => c.method === "evener/host/deploy")).toHaveLength(0);
  });

  test("an operation record is not published when the connection was replaced mid-request", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    const settlements = gateSettlements(fake, "evener/host/deploy");
    await hostOpsStore.getState().plan("beta");
    const pending = hostOpsStore.getState().deploy("beta");
    await Promise.resolve();
    // The record came back through a connection that has since been replaced:
    // it describes the previous hub, so it must not seed S15's polling for
    // this host name.
    connectionStore.getState().connect(new FakeClient("ready"));
    settlements[0]!.resolve({ id: "op-old", clientOperationId: "client-op-old", state: "pending" });
    await pending;

    expect(hostOpsStore.getState().operations.beta).toBeUndefined();
  });

  test("a superseded confirmation still publishes its record under the same client", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    const settlements = gateSettlements(fake, "evener/host/deploy");
    await hostOpsStore.getState().plan("beta");
    const pending = hostOpsStore.getState().deploy("beta");
    await Promise.resolve();
    // The dialog closed while the request was out (a superseded sequence), but
    // the connection is the same one: the started record is real and S15 needs
    // its id.
    hostOpsStore.getState().discardPlan("beta");
    settlements[0]!.resolve({ id: "op-keep", clientOperationId: "client-op-keep", state: "pending" });
    await pending;

    expect(hostOpsStore.getState().plans.beta).toBeUndefined();
    expect(hostOpsStore.getState().operations.beta).toEqual({
      id: "op-keep",
      clientOperationId: "client-op-keep",
      kind: "deploy",
      progress: [],
      state: "pending",
      host: "beta",
      generation: 3,
    });
  });

  test("a stale token re-plans and re-renders before any retry", async () => {
    const fake = connectFakeClient();
    let plans = 0;
    fake.on("evener/host/plan", () => {
      plans += 1;
      return { outcome: "planned", plan: plan({ targetPath: `/t${plans}` }), token: `tok-${plans}` };
    });
    fake.on("evener/host/deploy", () => {
      throw new WireError('host "beta": the confirmation token expired', -32013, { evenerErrorInfo: "token-expired" });
    });
    await hostOpsStore.getState().plan("beta");
    const before = hostOpsStore.getState().plans.beta;
    if (before?.phase !== "planned") throw new Error("unreachable");

    await hostOpsStore.getState().deploy("beta");

    // §13: deploy rejected the token as stale, so the UI re-plans and
    // re-renders the confirmation from the new response before any retry.
    expect(plans).toBe(2);
    const after = hostOpsStore.getState().plans.beta;
    expect(after?.phase).toBe("planned");
    if (after?.phase !== "planned") throw new Error("unreachable");
    expect(after.token).toBe("tok-2");
    expect(after.plan.targetPath).toBe("/t2");
    expect(after.operationId).not.toBe(before.operationId);
    expect(after.notice).toContain("This plan expired before the deploy was submitted.");
    expect(after.notice).toContain("fresh plan");
    // Nothing was deployed under the dead token.
    expect(fake.calls.filter((c) => c.method === "evener/host/deploy")).toHaveLength(1);
  });

  test("a hub.toml fingerprint drift re-plans like any other stale-entry refusal", async () => {
    const fake = connectFakeClient();
    let plans = 0;
    fake.on("evener/host/plan", () => {
      plans += 1;
      return { outcome: "planned", plan: plan({ targetPath: `/t${plans}` }), token: `tok-${plans}` };
    });
    fake.on("evener/host/deploy", () => {
      // 08b §11: a manual hub.toml edit between plan and deploy is a
      // stale-entry refusal with the hub.toml-fingerprint binding.
      throw new WireError('host "beta": the host entry changed', -32013, {
        evenerErrorInfo: "stale-entry",
        binding: "hub.toml-fingerprint",
      });
    });
    await hostOpsStore.getState().plan("beta");
    await hostOpsStore.getState().deploy("beta");

    expect(plans).toBe(2);
    const state = hostOpsStore.getState().plans.beta;
    if (state?.phase !== "planned") throw new Error("unreachable");
    expect(state.token).toBe("tok-2");
    expect(state.notice).toContain("The host changed since this operation was prepared.");
  });

  test("a lost response retries with the SAME operation ID, never a fresh operation", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    let deploys = 0;
    fake.on("evener/host/deploy", () => {
      deploys += 1;
      if (deploys === 1) throw new RequestTimeoutError("no response");
      return { id: "op-1", clientOperationId: "client-op-1", state: "pending" };
    });
    await hostOpsStore.getState().plan("beta");
    const planned = hostOpsStore.getState().plans.beta;
    if (planned?.phase !== "planned") throw new Error("unreachable");

    await hostOpsStore.getState().deploy("beta");
    const state = hostOpsStore.getState().plans.beta;
    if (state?.phase !== "planned") throw new Error("unreachable");
    expect(state.refusal?.message).toContain("did not answer before the request timed out");
    expect(state.refusal !== null && deployRefusalAction(state.refusal.kind)).toBe("retry");

    await hostOpsStore.getState().deploy("beta");
    const calls = fake.calls.filter((c) => c.method === "evener/host/deploy");
    expect(calls).toHaveLength(2);
    expect((calls[0]!.params as { operationId: string }).operationId).toBe(
      (calls[1]!.params as { operationId: string }).operationId,
    );
    expect(hostOpsStore.getState().plans.beta?.phase).toBe("started");
  });

  test("a conflicting operation ID rotates it for the retry", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    let deploys = 0;
    fake.on("evener/host/deploy", () => {
      deploys += 1;
      if (deploys === 1) {
        throw new WireError("used up", -32013, { evenerErrorInfo: "conflicting-operation-id" });
      }
      return { id: "op-2", clientOperationId: "client-op-2", state: "pending" };
    });
    await hostOpsStore.getState().plan("beta");

    await hostOpsStore.getState().deploy("beta");
    await hostOpsStore.getState().deploy("beta");
    const calls = fake.calls.filter((c) => c.method === "evener/host/deploy");
    expect(calls).toHaveLength(2);
    expect((calls[0]!.params as { operationId: string }).operationId).not.toBe(
      (calls[1]!.params as { operationId: string }).operationId,
    );
    expect(hostOpsStore.getState().plans.beta?.phase).toBe("started");
  });

  test("host-detached offers Connect, not a bare retry", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    fake.on("evener/host/deploy", () => {
      throw new WireError("no channel", -32014, { evenerErrorInfo: "host-detached" });
    });
    fake.on("evener/host/attach", () => ({ attached: true, host: "beta" }));
    await hostOpsStore.getState().plan("beta");
    await hostOpsStore.getState().deploy("beta");

    const state = hostOpsStore.getState().plans.beta;
    if (state?.phase !== "planned") throw new Error("unreachable");
    expect(state.refusal?.kind).toBe("host-detached");
    if (state.refusal === null) throw new Error("unreachable");
    expect(deployRefusalAction(state.refusal.kind)).toBe("connect");

    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-2" }));
    await hostOpsStore.getState().connectAndPlan("beta");
    expect(fake.calls.filter((c) => c.method === "evener/host/attach")).toHaveLength(1);
    expect(fake.calls.filter((c) => c.method === "evener/host/plan")).toHaveLength(2);
  });
});

describe("restart", () => {
  test("submits the intended pair and the client operation ID", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/restart", () => ({ id: "op-9", clientOperationId: "client-op-9", state: "pending" }));
    hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });
    const attempt = hostOpsStore.getState().restarts.beta;
    if (attempt === undefined) throw new Error("unreachable");

    await hostOpsStore.getState().restart("beta");

    expect(fake.calls.find((c) => c.method === "evener/host/restart")?.params).toEqual({
      name: "beta",
      operationId: attempt.operationId,
      generation: 3,
      incarnationId: "inc-3",
    });
    expect(hostOpsStore.getState().restarts.beta?.phase).toBe("started");
    expect(hostOpsStore.getState().operations.beta).toEqual({
      id: "op-9",
      clientOperationId: "client-op-9",
      kind: "restart",
      progress: [],
      state: "pending",
      host: "beta",
      generation: 3,
      incarnationId: "inc-3",
    });
  });

  test("a stale-entry refusal re-reads the pair and retries once with the fresh one", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({
      hosts: [row("beta"), { ...row("beta"), generation: 4, incarnationId: "inc-4" }].slice(1),
    }));
    let restarts = 0;
    fake.on("evener/host/restart", () => {
      restarts += 1;
      if (restarts === 1) {
        throw new WireError('host "beta": registration moved; retry', -32013, {
          evenerErrorInfo: "stale-entry",
          binding: "generation",
        });
      }
      return { id: "op-10", clientOperationId: "client-op-10", state: "pending" };
    });
    hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });

    await hostOpsStore.getState().restart("beta");

    const calls = fake.calls.filter((c) => c.method === "evener/host/restart");
    expect(calls).toHaveLength(2);
    expect((calls[0]!.params as { generation: number }).generation).toBe(3);
    // The retry echoes the pair the re-read answered, mirroring hosts.ts's
    // guardedMutation retry shape.
    expect(calls[1]!.params as { generation: number; incarnationId: string }).toMatchObject({
      generation: 4,
      incarnationId: "inc-4",
    });
    expect(hostOpsStore.getState().restarts.beta?.phase).toBe("started");
  });

  test("a second stale-entry refusal surfaces instead of looping", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("beta")] }));
    fake.on("evener/host/restart", () => {
      throw new WireError('host "beta": registration moved; retry', -32013, {
        evenerErrorInfo: "stale-entry",
        binding: "generation",
      });
    });
    hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });

    await hostOpsStore.getState().restart("beta");

    expect(fake.calls.filter((c) => c.method === "evener/host/restart")).toHaveLength(2);
    const state = hostOpsStore.getState().restarts.beta;
    expect(state?.phase).toBe("failed");
    expect(state?.refusal?.kind).toBe("stale-entry");
  });

  test("a failed stale-entry retry persists the fresh pair and operation ID", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [{ ...row("beta"), generation: 4, incarnationId: "inc-4" }] }));
    let restarts = 0;
    fake.on("evener/host/restart", () => {
      restarts += 1;
      if (restarts === 1) {
        throw new WireError('host "beta": registration moved; retry', -32013, { evenerErrorInfo: "stale-entry" });
      }
      if (restarts === 2) {
        throw new WireError("busy", -32013, { evenerErrorInfo: "host-busy-transient" });
      }
      return { id: "op-11", clientOperationId: "client-op-11", state: "pending" };
    });
    hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });

    // First attempt: stale-entry re-reads and retries once; that retry fails,
    // leaving the attempt failed.
    await hostOpsStore.getState().restart("beta");
    expect(hostOpsStore.getState().restarts.beta?.phase).toBe("failed");

    // The operator retries: the attempt must resubmit the pair the re-read
    // answered and the operation ID the retry used - never the stale
    // coordinates and retired key the first attempt carried.
    await hostOpsStore.getState().restart("beta");
    const calls = fake.calls.filter((c) => c.method === "evener/host/restart");
    expect(calls).toHaveLength(3);
    expect(calls[1]!.params as { generation: number; incarnationId: string }).toMatchObject({
      generation: 4,
      incarnationId: "inc-4",
    });
    expect(calls[2]!.params as { generation: number; incarnationId: string }).toMatchObject({
      generation: 4,
      incarnationId: "inc-4",
    });
    expect((calls[2]!.params as { operationId: string }).operationId).toBe(
      (calls[1]!.params as { operationId: string }).operationId,
    );
    expect(hostOpsStore.getState().restarts.beta?.phase).toBe("started");
  });

  test("a host-detached restart offers Connect and re-seeds the confirmation on the fresh pair", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [{ ...row("beta"), generation: 4, incarnationId: "inc-4" }] }));
    let restarts = 0;
    fake.on("evener/host/restart", () => {
      restarts += 1;
      if (restarts === 1) {
        throw new WireError("no live attached channel", -32014, { evenerErrorInfo: "host-detached" });
      }
      return { id: "op-13", clientOperationId: "client-op-13", state: "pending" };
    });
    fake.on("evener/host/attach", () => ({ attached: true, host: "beta" }));
    hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });

    await hostOpsStore.getState().restart("beta");
    const failed = hostOpsStore.getState().restarts.beta;
    expect(failed?.phase).toBe("failed");
    if (failed === undefined || failed.refusal === null) throw new Error("unreachable");
    expect(restartRefusalAction(failed.refusal.kind)).toBe("connect");

    await hostOpsStore.getState().connectAndRestart("beta");

    // Connect succeeded and the forced read answered the current pair: the
    // attempt is a fresh confirmation against it, never a replay of the stale
    // pair under the retired key.
    const reseeded = hostOpsStore.getState().restarts.beta;
    expect(reseeded?.phase).toBe("confirm");
    expect(reseeded?.pair).toEqual({ generation: 4, incarnationId: "inc-4" });
    expect(reseeded?.operationId).not.toBe(failed.operationId);
    expect(reseeded?.refusal).toBeNull();
    expect(fake.calls.filter((c) => c.method === "evener/host/attach")).toHaveLength(1);
    if (reseeded === undefined) throw new Error("unreachable");

    // Confirming (or the dialog's Restart affordance) opens under the fresh pair.
    await hostOpsStore.getState().restart("beta");
    const calls = fake.calls.filter((c) => c.method === "evener/host/restart");
    expect(calls).toHaveLength(2);
    expect(calls[1]!.params as { generation: number; incarnationId: string }).toMatchObject({
      generation: 4,
      incarnationId: "inc-4",
    });
    expect((calls[1]!.params as { operationId: string }).operationId).toBe(reseeded.operationId);
    expect(hostOpsStore.getState().restarts.beta?.phase).toBe("started");
  });

  test("a failed Connect during connectAndRestart records the connect refusal with the attempt intact", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("beta")] }));
    fake.on("evener/host/restart", () => {
      throw new WireError("no live attached channel", -32014, { evenerErrorInfo: "host-detached" });
    });
    fake.on("evener/host/attach", () => {
      throw new Error("dial tcp: connection refused");
    });
    hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });
    await hostOpsStore.getState().restart("beta");

    await hostOpsStore.getState().connectAndRestart("beta");

    const state = hostOpsStore.getState().restarts.beta;
    expect(state?.phase).toBe("failed");
    expect(state?.refusal).not.toBeNull();
    expect(state?.pair).toEqual({ generation: 3, incarnationId: "inc-3" });
    expect(fake.calls.filter((c) => c.method === "evener/host/restart")).toHaveLength(1);
  });

  test("the stale-entry retry takes a forced read past an in-flight refresh", async () => {
    const fake = connectFakeClient();
    const lists = gateSettlements(fake, "evener/host/list");
    // A background poll is in flight when the restart refuses stale-entry.
    // Its response was captured before the refusal proved the snapshot stale,
    // so it must not feed the single automatic retry.
    const poll = hostsStore.getState().refresh();
    await Promise.resolve();

    let restarts = 0;
    fake.on("evener/host/restart", () => {
      restarts += 1;
      if (restarts === 1) {
        throw new WireError('host "beta": registration moved; retry', -32013, { evenerErrorInfo: "stale-entry" });
      }
      return { id: "op-14", clientOperationId: "client-op-14", state: "pending" };
    });
    hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });
    const pending = hostOpsStore.getState().restart("beta");

    // The retry issues its OWN list read; waiting on the coalesced refresh
    // would echo the pre-move snapshot.
    for (let i = 0; i < 20 && lists.length < 2; i += 1) await Promise.resolve();
    expect(lists).toHaveLength(2);

    // The in-flight poll answers the pre-move snapshot...
    lists[0]!.resolve({ hosts: [row("beta")] });
    await poll;
    // ...and the forced read answers the current pair.
    lists[1]!.resolve({ hosts: [{ ...row("beta"), generation: 4, incarnationId: "inc-4" }] });
    await pending;

    const calls = fake.calls.filter((c) => c.method === "evener/host/restart");
    expect(calls).toHaveLength(2);
    expect(calls[1]!.params as { generation: number; incarnationId: string }).toMatchObject({
      generation: 4,
      incarnationId: "inc-4",
    });
  });

  test("a failed forced read fails the attempt with the unknown-pair refusal and no second request", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("beta")] }));
    // A ready pre-move snapshot stands...
    await hostsStore.getState().fetch();
    // ...and the forced read fails, so the snapshot on hand is the one the
    // refusal just proved stale: the retry must not replay it.
    fake.on("evener/host/list", () => {
      throw new Error("registry unavailable");
    });
    fake.on("evener/host/restart", () => {
      throw new WireError('host "beta": registration moved; retry', -32013, { evenerErrorInfo: "stale-entry" });
    });
    hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });

    await hostOpsStore.getState().restart("beta");

    const state = hostOpsStore.getState().restarts.beta;
    expect(state?.phase).toBe("failed");
    expect(state?.refusal?.message).toContain("answered no current pair");
    expect(fake.calls.filter((c) => c.method === "evener/host/restart")).toHaveLength(1);
  });

  test("a forced read discarded by a mutation fence answers false, so the retry never replays the stale pair", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("beta")] }));
    // A ready pre-move snapshot stands.
    await hostsStore.getState().fetch();
    const lists = gateSettlements(fake, "evener/host/list");

    fake.on("evener/host/restart", () => {
      throw new WireError('host "beta": registration moved; retry', -32013, { evenerErrorInfo: "stale-entry" });
    });
    fake.on("evener/host/attach", () => ({ attached: true, host: "beta" }));
    hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });
    const restartPending = hostOpsStore.getState().restart("beta");
    for (let i = 0; i < 20 && lists.length < 1; i += 1) await Promise.resolve();
    expect(lists).toHaveLength(1);

    // A mutation lands while the forced read is in flight: its re-read fence
    // marks the forced read's generation as already published.
    const mutation = hostsStore.getState().connect("beta");
    for (let i = 0; i < 20 && lists.length < 2; i += 1) await Promise.resolve();
    expect(lists).toHaveLength(2);

    // The forced read's own response is discarded by that fence even though
    // its generation is the marker's value...
    lists[0]!.resolve({ hosts: [{ ...row("beta"), generation: 4, incarnationId: "inc-4" }] });
    // ...and the mutation's re-read answers the pre-move snapshot.
    lists[1]!.resolve({ hosts: [row("beta")] });
    await mutation;
    await restartPending;

    const state = hostOpsStore.getState().restarts.beta;
    expect(state?.phase).toBe("failed");
    expect(state?.refusal?.message).toContain("answered no current pair");
    expect(fake.calls.filter((c) => c.method === "evener/host/restart")).toHaveLength(1);
  });

  test("a recovery completing after a close-and-reopen publishes nothing", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [{ ...row("beta"), generation: 4, incarnationId: "inc-4" }] }));
    fake.on("evener/host/restart", () => {
      throw new WireError("no live attached channel", -32014, { evenerErrorInfo: "host-detached" });
    });
    const attaches = gateSettlements(fake, "evener/host/attach");
    hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });
    await hostOpsStore.getState().restart("beta");
    const pending = hostOpsStore.getState().connectAndRestart("beta");
    await Promise.resolve();

    // The operator closes and reopens the dialog while Connect is in flight:
    // a newer attempt owns the state, so the stale recovery publishes nothing.
    hostOpsStore.getState().discardRestart("beta");
    hostOpsStore.getState().beginRestart("beta", { generation: 9, incarnationId: "inc-9" });
    attaches[0]!.resolve({ attached: true });
    await pending;

    const state = hostOpsStore.getState().restarts.beta;
    expect(state?.phase).toBe("confirm");
    expect(state?.pair).toEqual({ generation: 9, incarnationId: "inc-9" });
  });

  test("a failed connect recovery completing after a close-and-reopen publishes nothing", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    const attaches = gateSettlements(fake, "evener/host/attach");
    const pending = hostOpsStore.getState().connectAndPlan("beta");
    await Promise.resolve();

    hostOpsStore.getState().discardPlan("beta");
    await hostOpsStore.getState().plan("beta");
    attaches[0]!.reject(new Error("dial refused"));
    await pending;

    // The newer plan owns the state; the stale Connect failure never overwrites it.
    const state = hostOpsStore.getState().plans.beta;
    expect(state?.phase).toBe("planned");
  });

  test("a connect recovery superseded before Connect resolves issues no plan", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("beta")] }));
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    const attaches = gateSettlements(fake, "evener/host/attach");
    const pending = hostOpsStore.getState().connectAndPlan("beta");
    await Promise.resolve();

    // The operator closes and reopens the dialog while Connect is in flight:
    // the reopened dialog's plan owns the state, and the stale recovery must
    // not mint another plan over it.
    hostOpsStore.getState().discardPlan("beta");
    await hostOpsStore.getState().plan("beta");
    attaches[0]!.resolve({ attached: true, host: "beta" });
    await pending;

    expect(fake.calls.filter((c) => c.method === "evener/host/plan")).toHaveLength(1);
  });

  test("host-busy-operation names the running operation and blocks a bare retry", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/restart", () => {
      throw new WireError("busy", -32013, { evenerErrorInfo: "host-busy-operation", operationId: "op-42" });
    });
    hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });

    await hostOpsStore.getState().restart("beta");

    const state = hostOpsStore.getState().restarts.beta;
    expect(state?.phase).toBe("failed");
    if (state === undefined || state.refusal === null) throw new Error("unreachable");
    expect(state.refusal.message).toContain("Another operation is running on this host (op-42).");
    expect(hostOpRefusalBlocksRetry(state.refusal.kind)).toBe(true);
  });

  test("remnant-open surfaces the blocking remnant with no retry", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/restart", () => {
      throw new WireError("fenced", -32013, { evenerErrorInfo: "remnant-open", remnantId: "remnant-7" });
    });
    hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });

    await hostOpsStore.getState().restart("beta");

    const state = hostOpsStore.getState().restarts.beta;
    if (state === undefined || state.refusal === null) throw new Error("unreachable");
    expect(state.refusal.message).toContain("remnant-7");
    expect(hostOpRefusalBlocksRetry(state.refusal.kind)).toBe(true);
  });

  test("discardRestart clears the attempt", () => {
    hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });
    expect(hostOpsStore.getState().restarts.beta).toBeDefined();
    hostOpsStore.getState().discardRestart("beta");
    expect(hostOpsStore.getState().restarts.beta).toBeUndefined();
  });
});

// --- S15: operations polling, progress/terminal render, replay ---------------

// operationRecord is one wire record as `evener/host/operations` answers it
// (deploy-pipeline spec 08b §10).
function operationRecord(overrides: Partial<OperationRecord> = {}): OperationRecord {
  return {
    id: "op-1",
    clientOperationId: "client-op-1",
    host: "beta",
    generation: 3,
    incarnationId: "inc-3",
    kind: "deploy",
    state: "running",
    progress: [],
    createdAt: "2026-09-28T08:00:00Z",
    updatedAt: "2026-09-28T08:00:05Z",
    hostRemoved: false,
    ...overrides,
  };
}

// startedDeploy drives one plan -> deploy submission and scripts the
// operations read the polling then issues.
async function startedDeploy(fake: FakeClient, operations: () => HostOperationsResponse): Promise<void> {
  fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
  fake.on("evener/host/deploy", () => ({ id: "op-1", clientOperationId: "client-op-1", state: "pending" }));
  fake.on("evener/host/operations", operations);
  await hostOpsStore.getState().plan("beta");
  await hostOpsStore.getState().deploy("beta");
}

describe("operations polling (S15)", () => {
  test("polls the started operation by host name and controller-assigned id", async () => {
    const fake = connectFakeClient();
    await startedDeploy(fake, () => ({
      operations: [
        operationRecord({ state: "running", progress: [{ ts: "2026-09-28T08:00:05Z", message: "pushing evener" }] }),
      ],
    }));

    await hostOpsStore.getState().pollOperation("beta");

    // 08b §10: `id` is the detail filter for the controller-assigned record id,
    // and `name` pins the page to this host — the narrowest exact read, never a
    // broad unfiltered page.
    const read = fake.calls.find((c) => c.method === "evener/host/operations");
    expect(read?.params).toEqual({ name: "beta", id: "op-1" });
    const ref = hostOpsStore.getState().operations.beta;
    if (ref === undefined) throw new Error("unreachable");
    expect(ref.state).toBe("running");
    expect(ref.progress).toEqual([{ ts: "2026-09-28T08:00:05Z", message: "pushing evener" }]);
    expect(ref.fetched).toBe(true);
    expect(hostOperationView(ref)).toEqual({
      tone: "attention",
      label: "Deploying…",
      line: "pushing evener",
      replay: null,
      unavailable: null,
    });
  });

  test("a terminal success renders the completed outcome", async () => {
    const fake = connectFakeClient();
    await startedDeploy(fake, () => ({
      operations: [operationRecord({ state: "complete", result: { ok: true, message: "deployed controller-rev-9" } })],
    }));

    await hostOpsStore.getState().pollOperation("beta");

    const ref = hostOpsStore.getState().operations.beta;
    if (ref === undefined) throw new Error("unreachable");
    expect(hostOperationView(ref)).toMatchObject({
      tone: "alive",
      label: "Deploy complete",
      line: "deployed controller-rev-9",
    });
  });

  test("a terminal failure renders the record's result message verbatim", async () => {
    const fake = connectFakeClient();
    const verbatim = "deploy failed: the remote evener service exited with status 1 after the swap";
    await startedDeploy(fake, () => ({
      operations: [operationRecord({ state: "failed", result: { ok: false, message: verbatim } })],
    }));

    await hostOpsStore.getState().pollOperation("beta");

    const ref = hostOpsStore.getState().operations.beta;
    if (ref === undefined) throw new Error("unreachable");
    expect(ref.result).toEqual({ ok: false, message: verbatim });
    const view = hostOperationView(ref);
    expect(view.label).toBe("Deploy failed");
    expect(view.tone).toBe("danger");
    // Exact equality: §13's verbatim 04b error, never rewritten, prefixed, or
    // generalised into a generic failure.
    expect(view.line).toBe(verbatim);
  });

  test("a terminal read ends the loop: no further reads, and the rows refresh", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("beta")] }));
    await startedDeploy(fake, () => ({
      operations: [operationRecord({ state: "complete", result: { ok: true, message: "deployed 1.5.0" } })],
    }));

    await hostOpsStore.getState().pollOperation("beta");
    await hostOpsStore.getState().pollOperation("beta");

    const reads = () => fake.calls.filter((c) => c.method === "evener/host/operations").length;
    expect(reads()).toBe(1);
    const ref = hostOpsStore.getState().operations.beta;
    if (ref === undefined) throw new Error("unreachable");
    expect(operationNeedsRead(ref)).toBe(false);
    // 08b §13 item 1: the worker publishes the verified post-operation facts
    // before the record reads `complete`; the terminal read refreshes the rows
    // so the row's version signal is current without waiting for the pane poll.
    await vi.waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/host/list").length).toBe(1));
  });

  test("a late read cannot overwrite a terminal record or strand a stopped-progress error", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    fake.on("evener/host/deploy", () => ({ id: "op-1", clientOperationId: "client-op-1", state: "pending" }));
    const settlements = gateSettlements(fake, "evener/host/operations");
    await hostOpsStore.getState().plan("beta");
    await hostOpsStore.getState().deploy("beta");

    // Two reads issue before either answers (a slow read overlaps the next
    // tick): only the newest may publish, whatever order they settle in.
    const older = hostOpsStore.getState().pollOperation("beta");
    const newer = hostOpsStore.getState().pollOperation("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(2));
    settlements[1]!.resolve({
      operations: [operationRecord({ state: "complete", result: { ok: true, message: "deployed 1.5.0" } })],
    });
    await newer;
    settlements[0]!.reject(new Error("late read timed out"));
    await older;

    const ref = hostOpsStore.getState().operations.beta;
    if (ref === undefined) throw new Error("unreachable");
    // The late read's failure must not resurrect the running state or attach a
    // "progress stopped" error to the finished record — nothing would ever
    // clear it, because a settled record owes no further read.
    expect(ref.state).toBe("complete");
    expect(ref.readRefusal).toBeUndefined();
    expect(hostOperationView(ref).unavailable).toBeNull();
    expect(hostOperationView(ref).label).toBe("Deploy complete");
  });

  test("a read counts as pending while in flight and is released when it settles", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    fake.on("evener/host/deploy", () => ({ id: "op-1", clientOperationId: "client-op-1", state: "pending" }));
    const settlements = gateSettlements(fake, "evener/host/operations");
    await hostOpsStore.getState().plan("beta");
    await hostOpsStore.getState().deploy("beta");

    const pending = hostOpsStore.getState().pollOperation("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    expect(operationReadPending("beta", "op-1")).toBe(true);

    settlements[0]!.resolve({
      operations: [operationRecord({ state: "complete", result: { ok: true, message: "deployed 1.5.0" } })],
    });
    await pending;
    // Released in pollOperation's finally, so the tick resumes after a slow or
    // failed read.
    expect(operationReadPending("beta", "op-1")).toBe(false);
  });

  test("a superseded operation's pending read does not block the new operation", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    fake.on("evener/host/deploy", () => ({ id: "op-1", clientOperationId: "client-op-1", state: "pending" }));
    const settlements = gateSettlements(fake, "evener/host/operations");
    await hostOpsStore.getState().plan("beta");
    await hostOpsStore.getState().deploy("beta");
    const hung = hostOpsStore.getState().pollOperation("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    expect(operationReadPending("beta", "op-1")).toBe(true);

    // A new operation replaces the tracked ref while the old read still hangs:
    // the pending read belongs to the superseded operation, so it must not
    // block the new one's progress (up to the old read's 15s timeout).
    fake.on("evener/host/restart", () => ({ id: "op-2", clientOperationId: "client-op-2", state: "pending" }));
    hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });
    await hostOpsStore.getState().restart("beta");
    expect(hostOpsStore.getState().operations.beta?.id).toBe("op-2");
    expect(operationReadPending("beta", "op-2")).toBe(false);

    const fresh = hostOpsStore.getState().pollOperation("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(2));
    settlements[0]!.resolve({ operations: [] });
    await hung;
    settlements[1]!.resolve({ operations: [operationRecord({ id: "op-2", clientOperationId: "client-op-2" })] });
    await fresh;
  });

  test("a read that finds no record marks the operation gone and stops the loop", async () => {
    const fake = connectFakeClient();
    await startedDeploy(fake, () => ({ operations: [] }));

    await hostOpsStore.getState().pollOperation("beta");

    const ref = hostOpsStore.getState().operations.beta;
    if (ref === undefined) throw new Error("unreachable");
    expect(ref.gone).toBe(true);
    // Nothing left to read: the outcome is unshowable, so the loop stops.
    expect(operationNeedsRead(ref)).toBe(false);
    expect(hostOperationView(ref).unavailable).toContain("no longer retains this operation's record");
  });

  test("a repeated identical read failure is published once, not every tick", async () => {
    const fake = connectFakeClient();
    await startedDeploy(fake, () => ({ operations: [operationRecord({ state: "running" })] }));
    await hostOpsStore.getState().pollOperation("beta");

    connectionStore.setState({ client: null });
    await hostOpsStore.getState().pollOperation("beta");
    const first = hostOpsStore.getState().operations.beta;
    if (first === undefined) throw new Error("unreachable");
    expect(first.readRefusal).toBeDefined();

    // The same refusal is not republished: the ref keeps its identity, so the
    // row's alert is not re-rendered and re-announced every second.
    await hostOpsStore.getState().pollOperation("beta");
    expect(hostOpsStore.getState().operations.beta).toBe(first);
  });

  test("a deploy seed carries the current row's pair, so a re-created name cannot show it", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/list", () => ({ hosts: [row("beta")] }));
    await hostsStore.getState().fetch();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    fake.on("evener/host/deploy", () => ({ id: "op-1", clientOperationId: "client-op-1", state: "pending" }));

    await hostOpsStore.getState().plan("beta");
    await hostOpsStore.getState().deploy("beta");

    const ref = hostOpsStore.getState().operations.beta;
    if (ref === undefined) throw new Error("unreachable");
    expect(ref.generation).toBe(3);
    expect(ref.incarnationId).toBe("inc-3");
  });

  test("a late response from a superseded deploy cannot replace the newer operation", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    const settlements = gateSettlements(fake, "evener/host/deploy");
    await hostOpsStore.getState().plan("beta");
    const first = hostOpsStore.getState().deploy("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(1));

    // The confirmation is superseded: the dialog closes, a fresh plan mints a
    // new operation ID, and a second deploy goes out while the first hangs.
    hostOpsStore.getState().discardPlan("beta");
    await hostOpsStore.getState().plan("beta");
    const second = hostOpsStore.getState().deploy("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(2));
    settlements[1]!.resolve({ id: "op-2", clientOperationId: "client-op-2", state: "pending" });
    await second;
    expect(hostOpsStore.getState().operations.beta?.id).toBe("op-2");

    // The late first response answers an operation the newer request already
    // superseded: adopting it would flip the row onto stale progress and bump
    // the polling sequence out from under the newer record.
    settlements[0]!.resolve({ id: "op-1", clientOperationId: "client-op-1", state: "pending" });
    await first;
    expect(hostOpsStore.getState().operations.beta?.id).toBe("op-2");

    // The row's polling still reads the newer operation's record.
    fake.on("evener/host/operations", () => ({
      operations: [operationRecord({ id: "op-2", clientOperationId: "client-op-2" })],
    }));
    await hostOpsStore.getState().pollOperation("beta");
    const reads = fake.calls.filter((c) => c.method === "evener/host/operations");
    expect(reads[reads.length - 1]?.params).toEqual({ name: "beta", id: "op-2" });
  });

  test("a late response after only a dialog close still seeds polling", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    const settlements = gateSettlements(fake, "evener/host/deploy");
    await hostOpsStore.getState().plan("beta");
    const deploy = hostOpsStore.getState().deploy("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    // Only the dialog closed: no newer operation exists, so the response is a
    // real operation whose record must still seed polling (S14's rule).
    hostOpsStore.getState().discardPlan("beta");
    settlements[0]!.resolve({ id: "op-1", clientOperationId: "client-op-1", state: "pending" });
    await deploy;
    expect(hostOpsStore.getState().operations.beta?.id).toBe("op-1");
  });

  test("a late restart response cannot replace the newer restart operation", async () => {
    const fake = connectFakeClient();
    const settlements = gateSettlements(fake, "evener/host/restart");
    hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });
    const first = hostOpsStore.getState().restart("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    // A second attempt (a re-opened dialog) supersedes the first while it hangs.
    hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });
    const second = hostOpsStore.getState().restart("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(2));
    settlements[1]!.resolve({ id: "op-2", clientOperationId: "client-op-2", state: "pending" });
    await second;
    expect(hostOpsStore.getState().operations.beta?.id).toBe("op-2");

    settlements[0]!.resolve({ id: "op-1", clientOperationId: "client-op-1", state: "pending" });
    await first;
    expect(hostOpsStore.getState().operations.beta?.id).toBe("op-2");
  });

  test("a late restart response after only a dialog close still seeds polling", async () => {
    const fake = connectFakeClient();
    const settlements = gateSettlements(fake, "evener/host/restart");
    hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });
    const restart = hostOpsStore.getState().restart("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    hostOpsStore.getState().discardRestart("beta");
    settlements[0]!.resolve({ id: "op-1", clientOperationId: "client-op-1", state: "pending" });
    await restart;
    expect(hostOpsStore.getState().operations.beta?.id).toBe("op-1");
  });

  test("a deploy seed keeps the pair captured at issue, not the re-created row's", async () => {
    const fake = connectFakeClient();
    let reCreated = false;
    fake.on("evener/host/list", () => ({
      hosts: [reCreated ? { ...row("beta"), generation: 9, incarnationId: "inc-9" } : row("beta")],
    }));
    await hostsStore.getState().fetch();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    const settlements = gateSettlements(fake, "evener/host/deploy");
    await hostOpsStore.getState().plan("beta");
    const deploy = hostOpsStore.getState().deploy("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(1));

    // The host is removed and re-created while the request is in flight: the
    // operation belongs to the incarnation the request was issued against, so
    // the seed must pin that pair and stay suppressed on the new row until the
    // first read reconciles identity.
    reCreated = true;
    await hostsStore.getState().reReadForced();
    settlements[0]!.resolve({ id: "op-1", clientOperationId: "client-op-1", state: "pending" });
    await deploy;

    const seed = hostOpsStore.getState().operations.beta;
    if (seed === undefined) throw new Error("unreachable");
    expect(seed.generation).toBe(3);
    expect(seed.incarnationId).toBe("inc-3");
    expect(operationShownOnHost(seed, { generation: 9, incarnationId: "inc-9" })).toBe(false);
  });

  test("an older valid response publishes while a newer request is still in flight, and the newer replaces it", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    const settlements = gateSettlements(fake, "evener/host/deploy");
    await hostOpsStore.getState().plan("beta");
    const first = hostOpsStore.getState().deploy("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    hostOpsStore.getState().discardPlan("beta");
    await hostOpsStore.getState().plan("beta");
    const second = hostOpsStore.getState().deploy("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(2));

    // S16 (S15 follow-up): publish ordering keys off the newest request that
    // actually PUBLISHED, so a newer request still in flight must not suppress
    // an older valid response — it seeds its operation (S14's designed case).
    settlements[0]!.resolve({ id: "op-1", clientOperationId: "client-op-1", state: "pending" });
    await first;
    expect(hostOpsStore.getState().operations.beta?.id).toBe("op-1");

    // The newer request, once it publishes, still wins.
    settlements[1]!.resolve({ id: "op-2", clientOperationId: "client-op-2", state: "pending" });
    await second;
    expect(hostOpsStore.getState().operations.beta?.id).toBe("op-2");
  });

  test("a lost connection is a visible progress state that a good read clears", async () => {
    const fake = connectFakeClient();
    await startedDeploy(fake, () => ({
      operations: [
        operationRecord({ state: "running", progress: [{ ts: "2026-09-28T08:00:05Z", message: "waiting healthy" }] }),
      ],
    }));
    await hostOpsStore.getState().pollOperation("beta");

    connectionStore.setState({ client: null });
    await hostOpsStore.getState().pollOperation("beta");

    let ref = hostOpsStore.getState().operations.beta;
    if (ref === undefined) throw new Error("unreachable");
    // The last-known state stays rendered; the stopped read is explicit — a
    // lost connection is never a silent stall.
    expect(ref.state).toBe("running");
    expect(ref.readRefusal?.kind).toBe("unknown");
    expect(hostOperationView(ref).unavailable).toContain("Progress updates stopped");
    // The loop keeps wanting a read: a transient failure never ends polling.
    expect(operationNeedsRead(ref)).toBe(true);

    const recovered = new FakeClient("ready");
    connectionStore.getState().connect(recovered);
    recovered.on("evener/host/operations", () => ({
      operations: [operationRecord({ state: "complete", result: { ok: true, message: "deployed 1.5.0" } })],
    }));
    await hostOpsStore.getState().pollOperation("beta");

    ref = hostOpsStore.getState().operations.beta;
    if (ref === undefined) throw new Error("unreachable");
    expect(ref.readRefusal).toBeUndefined();
    expect(hostOperationView(ref).unavailable).toBeNull();
    expect(ref.state).toBe("complete");
  });

  test("a retry after a lost response reuses the same operation ID and adopts the returned record", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    let deploys = 0;
    fake.on("evener/host/deploy", () => {
      deploys += 1;
      if (deploys === 1) throw new RequestTimeoutError("no response");
      // The dedup hit answers the existing record (08b §10): same controller id,
      // the record's actual state — never a fresh operation.
      return { id: "op-1", clientOperationId: "client-op-1", state: "failed" };
    });
    await hostOpsStore.getState().plan("beta");
    const planned = hostOpsStore.getState().plans.beta;
    if (planned?.phase !== "planned") throw new Error("unreachable");

    await hostOpsStore.getState().deploy("beta");
    const afterLostResponse = hostOpsStore.getState().plans.beta;
    if (afterLostResponse?.phase !== "planned") throw new Error("unreachable");
    await hostOpsStore.getState().deploy("beta");

    const calls = fake.calls.filter((c) => c.method === "evener/host/deploy");
    // §13: a retry after a lost response re-submits the SAME client operation ID.
    expect((calls[0]!.params as { operationId: string }).operationId).toBe(
      (calls[1]!.params as { operationId: string }).operationId,
    );
    // The returned record is what the store tracks, by the id it answered.
    expect(hostOpsStore.getState().operations.beta?.id).toBe("op-1");
    expect(hostOpsStore.getState().operations.beta?.state).toBe("failed");
    expect(deploys).toBe(2);
  });

  test("a replay past compaction renders the tombstoned terminal outcome, never a fresh operation", async () => {
    const fake = connectFakeClient();
    const verbatim = "restart failed: the post-restart probe still reports the old process instance";
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    fake.on("evener/host/deploy", () => ({ id: "op-1", clientOperationId: "client-op-1", state: "failed" }));
    // S6's `compacted` marker is not in the generated base types yet
    // (app_host_ops.go's operationRecordWire leaves it absent until S6 registers
    // it); the store reads it structurally, so a tombstoned replay renders as
    // the operation's past outcome the moment the marker lands.
    const tombstone = {
      ...operationRecord({ state: "failed", result: { ok: false, message: verbatim } }),
      compacted: true,
    } as unknown as OperationRecord;
    fake.on("evener/host/operations", () => ({ operations: [tombstone] }));
    fake.on("evener/host/list", () => ({ hosts: [row("beta")] }));

    await hostOpsStore.getState().plan("beta");
    await hostOpsStore.getState().deploy("beta");

    // The seed is already terminal but carries no body, so the loop still owes
    // it one read for the retained outcome.
    const seed = hostOpsStore.getState().operations.beta;
    if (seed === undefined) throw new Error("unreachable");
    expect(operationNeedsRead(seed)).toBe(true);
    await hostOpsStore.getState().pollOperation("beta");

    const ref = hostOpsStore.getState().operations.beta;
    if (ref === undefined) throw new Error("unreachable");
    const view = hostOperationView(ref);
    expect(view.label).toBe("Deploy failed");
    expect(view.line).toBe(verbatim);
    expect(view.replay).toContain("compacted");
    expect(operationNeedsRead(ref)).toBe(false);
    // Nothing opened a fresh operation.
    expect(fake.calls.filter((c) => c.method === "evener/host/deploy")).toHaveLength(1);
    // The replay's first full read is terminal too, so the rows refresh here
    // exactly as they do for a live transition: a deduplicated successful
    // deploy must not leave the row on stale post-operation facts.
    await vi.waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/host/list").length).toBe(1));
  });

  test("a stale-entry read refusal is visible, and polling continues", async () => {
    const fake = connectFakeClient();
    await startedDeploy(fake, () => {
      throw new WireError("the pinned boundary moved", -32013, { evenerErrorInfo: "stale-entry" });
    });

    await hostOpsStore.getState().pollOperation("beta");

    const ref = hostOpsStore.getState().operations.beta;
    if (ref === undefined) throw new Error("unreachable");
    expect(ref.readRefusal?.kind).toBe("stale-entry");
    expect(hostOperationView(ref).unavailable).toContain("The host changed since this operation was prepared.");
    expect(operationNeedsRead(ref)).toBe(true);
  });

  test("a mid-poll cursor-invalidated restarts from one fresh read, not a loop", async () => {
    const fake = connectFakeClient();
    let reads = 0;
    await startedDeploy(fake, () => {
      reads += 1;
      if (reads === 1) {
        throw new WireError("compaction removed the row this cursor resumed after", -32013, {
          evenerErrorInfo: "cursor-invalidated",
        });
      }
      return { operations: [operationRecord({ state: "complete", result: { ok: true, message: "deployed 1.5.0" } })] };
    });

    await hostOpsStore.getState().pollOperation("beta");

    // Exactly one immediate fresh read: the invalidated position is dropped,
    // and a repeating refusal would surface rather than loop.
    expect(reads).toBe(2);
    const ref = hostOpsStore.getState().operations.beta;
    if (ref === undefined) throw new Error("unreachable");
    expect(ref.state).toBe("complete");
    expect(ref.readRefusal).toBeUndefined();
    expect(hostOperationView(ref).line).toBe("deployed 1.5.0");
  });

  test("a cursor-too-large read refusal renders its concrete headline", async () => {
    const fake = connectFakeClient();
    await startedDeploy(fake, () => {
      throw new WireError("the cursor would be 9100 bytes, over the 8192-byte encoded cap", -32013, {
        evenerErrorInfo: "cursor-too-large",
        capBytes: 8192,
      });
    });

    await hostOpsStore.getState().pollOperation("beta");

    const ref = hostOpsStore.getState().operations.beta;
    if (ref === undefined) throw new Error("unreachable");
    expect(ref.readRefusal?.kind).toBe("cursor-too-large");
    const unavailable = hostOperationView(ref).unavailable;
    expect(unavailable).toContain("too large to page through");
    // The hub's own sentence rides along as the detail.
    expect(unavailable).toContain("8192-byte encoded cap");
  });

  test("settled states end the read loop; orphan-unverified keeps owing reads", () => {
    for (const state of ["complete", "failed", "interrupted"]) {
      expect(operationStateSettled(state)).toBe(true);
    }
    // S16 (S15 follow-up): `orphan-unverified` is durable but NOT settled — an
    // out-of-band transition can still move it, so the read loop must keep
    // polling until a terminal state rather than freeze the record behind a
    // settled state.
    expect(operationStateSettled("orphan-unverified")).toBe(false);
    for (const state of ["pending", "running"]) {
      expect(operationStateSettled(state)).toBe(false);
    }
    const seed: HostOperationRef = {
      id: "op-1",
      clientOperationId: "client-op-1",
      kind: "deploy",
      state: "failed",
      progress: [],
    };
    // A seed (deploy/restart's response) carries no result/progress: one read
    // fills the retained body, then the terminal state ends the loop.
    expect(operationNeedsRead(seed)).toBe(true);
    expect(operationNeedsRead({ ...seed, fetched: true })).toBe(false);
    expect(operationNeedsRead({ ...seed, fetched: true, gone: true })).toBe(false);
    expect(operationNeedsRead({ ...seed, state: "running" })).toBe(true);
    // An orphan-unverified body is durable: it still owes reads.
    expect(operationNeedsRead({ ...seed, state: "orphan-unverified", fetched: true })).toBe(true);
  });

  test("the view names the operation's kind and state, and never invents a message", () => {
    const base: HostOperationRef = {
      id: "op-1",
      clientOperationId: "client-op-1",
      kind: "restart",
      state: "running",
      progress: [{ ts: "t", message: "reconnecting the channel" }],
    };
    expect(hostOperationView(base)).toMatchObject({
      tone: "attention",
      label: "Restarting…",
      line: "reconnecting the channel",
    });
    expect(
      hostOperationView({ ...base, state: "interrupted", result: { ok: false, message: "controller shutdown" } }),
    ).toMatchObject({ tone: "danger", label: "Restart interrupted", line: "controller shutdown" });
    // `orphan-unverified` is durable: render the state truthfully, nothing more.
    expect(hostOperationView({ ...base, state: "orphan-unverified" })).toMatchObject({
      tone: "attention",
      label: "Restart orphan-unverified",
      line: null,
    });
    // A failure with no result message renders the chip alone rather than a
    // generic sentence.
    expect(hostOperationView({ ...base, state: "failed" })).toMatchObject({ label: "Restart failed", line: null });
    // A blank progress message is no line either: never an empty detail span.
    expect(hostOperationView({ ...base, progress: [{ ts: "t", message: "   " }] })).toMatchObject({ line: null });
    // An unknown future state renders its raw value, never a fabricated success.
    expect(hostOperationView({ ...base, state: "paused" })).toMatchObject({ tone: "neutral", label: "Restart paused" });
    // ...and still shows the record's own prose when it has any: the newest
    // progress entry first, the result message otherwise.
    expect(
      hostOperationView({ ...base, state: "paused", progress: [{ ts: "t", message: "still working" }] }),
    ).toMatchObject({ label: "Restart paused", line: "still working" });
    expect(
      hostOperationView({
        ...base,
        state: "paused",
        progress: [],
        result: { ok: false, message: "paused by the operator" },
      }),
    ).toMatchObject({ label: "Restart paused", line: "paused by the operator" });
  });

  test("an operation tracks its host identity, and only renders on the incarnation it belongs to", () => {
    const identity = { host: "beta", generation: 1, incarnationId: "inc-1" };
    const base: HostOperationRef = {
      id: "op-1",
      clientOperationId: "client-op-1",
      kind: "deploy",
      state: "running",
      progress: [],
    };
    // A seed that has not read its record renders on the row it was started
    // from; once a read answers, the record's pair decides.
    expect(operationShownOnHost(base, identity)).toBe(true);
    // A seed already carrying the row's pair is identified too: a re-created
    // name with the same generation but a new incarnation is suppressed even
    // before the first poll reveals the record.
    const seed: HostOperationRef = { ...base, ...identity };
    expect(operationShownOnHost(seed, { generation: 1, incarnationId: "inc-2" })).toBe(false);
    const read: HostOperationRef = { ...base, ...identity, fetched: true };
    expect(operationShownOnHost(read, identity)).toBe(true);
    // A re-created same-name host (a new generation or incarnation) must not
    // display the previous incarnation's operation.
    expect(operationShownOnHost(read, { generation: 2, incarnationId: "inc-1" })).toBe(false);
    expect(operationShownOnHost(read, { generation: 1, incarnationId: "inc-2" })).toBe(false);
    // A ref pinned to a newer generation than the row's is current and the row
    // snapshot is merely stale (the pane poll converges it): it still renders.
    const newer: HostOperationRef = { ...base, generation: 4, incarnationId: "inc-4", fetched: true };
    expect(operationShownOnHost(newer, { generation: 3, incarnationId: "inc-3" })).toBe(true);
  });

  // --- S16: the S15 follow-ups, RED-first ---------------------------------------

  test("an orphan-unverified record keeps polling until a terminal state", async () => {
    const fake = connectFakeClient();
    let reads = 0;
    await startedDeploy(fake, () => {
      reads += 1;
      if (reads === 1) return { operations: [operationRecord({ state: "orphan-unverified" })] };
      return {
        operations: [operationRecord({ state: "interrupted", result: { ok: false, message: "controller shutdown" } })],
      };
    });

    await hostOpsStore.getState().pollOperation("beta");
    let ref = hostOpsStore.getState().operations.beta;
    if (ref === undefined) throw new Error("unreachable");
    expect(ref.state).toBe("orphan-unverified");
    // The state is durable: the loop keeps owing a read instead of freezing
    // on it.
    expect(operationNeedsRead(ref)).toBe(true);

    await hostOpsStore.getState().pollOperation("beta");
    ref = hostOpsStore.getState().operations.beta;
    if (ref === undefined) throw new Error("unreachable");
    expect(ref.state).toBe("interrupted");
    expect(operationNeedsRead(ref)).toBe(false);
  });

  test("a newer request that never publishes cannot suppress an older valid response", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    const settlements = gateSettlements(fake, "evener/host/deploy");
    await hostOpsStore.getState().plan("beta");
    const first = hostOpsStore.getState().deploy("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    hostOpsStore.getState().discardPlan("beta");
    await hostOpsStore.getState().plan("beta");
    const second = hostOpsStore.getState().deploy("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(2));

    // The newer request is REFUSED (a held gate): it never publishes, so it must
    // not suppress the older valid response waiting behind it.
    settlements[1]!.reject(
      new WireError("busy", -32013, { evenerErrorInfo: "host-busy-operation", operationId: "op-42" }),
    );
    await second;
    expect(hostOpsStore.getState().operations.beta).toBeUndefined();

    settlements[0]!.resolve({ id: "op-1", clientOperationId: "client-op-1", state: "pending" });
    await first;
    expect(hostOpsStore.getState().operations.beta?.id).toBe("op-1");
  });

  test("stopOperationPoll releases the name's pending reads so a remount is not skipped", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    fake.on("evener/host/deploy", () => ({ id: "op-1", clientOperationId: "client-op-1", state: "pending" }));
    const settlements = gateSettlements(fake, "evener/host/operations");
    await hostOpsStore.getState().plan("beta");
    await hostOpsStore.getState().deploy("beta");

    const hung = hostOpsStore.getState().pollOperation("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    expect(operationReadPending("beta", "op-1")).toBe(true);

    // The section unmounts while the read hangs: the pending entry is released
    // now, so a remount's first tick is not skipped for up to the read timeout.
    hostOpsStore.getState().stopOperationPoll("beta");
    expect(operationReadPending("beta", "op-1")).toBe(false);

    // The hung read settling later publishes nothing and is harmless.
    settlements[0]!.resolve({ operations: [] });
    await hung;
    expect(operationReadPending("beta", "op-1")).toBe(false);
  });

  test("a stale read settling after the poll stopped cannot release a new read's pending count", async () => {
    const fake = connectFakeClient();
    fake.on("evener/host/plan", () => ({ outcome: "planned", plan: plan(), token: "tok-1" }));
    fake.on("evener/host/deploy", () => ({ id: "op-1", clientOperationId: "client-op-1", state: "pending" }));
    const settlements = gateSettlements(fake, "evener/host/operations");
    await hostOpsStore.getState().plan("beta");
    await hostOpsStore.getState().deploy("beta");

    const stale = hostOpsStore.getState().pollOperation("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(1));
    hostOpsStore.getState().stopOperationPoll("beta");

    // A remount reads the same operation again while the stale read still hangs.
    const fresh = hostOpsStore.getState().pollOperation("beta");
    await vi.waitFor(() => expect(settlements).toHaveLength(2));
    expect(operationReadPending("beta", "op-1")).toBe(true);

    // The stale read's settle must not release the fresh read's pending count.
    settlements[0]!.resolve({ operations: [] });
    await stale;
    expect(operationReadPending("beta", "op-1")).toBe(true);

    settlements[1]!.resolve({
      operations: [operationRecord({ state: "complete", result: { ok: true, message: "deployed 1.5.0" } })],
    });
    await fresh;
    expect(operationReadPending("beta", "op-1")).toBe(false);
  });

  // --- S16: the remnant teardown retry/recover store ----------------------------

  /** removedRow is the tombstone-shaped arm a `hostKind: "removed"` teardown
   * response carries (registry spec 08 §11's RemovedRow). */
  function removedRow(name: string): RemovedRow {
    return {
      name,
      origin: "hub.toml",
      generation: 3,
      incarnationId: "inc-3",
      removed: true,
      attached: false,
      midEnsure: false,
    };
  }

  describe("remnant teardown retry/recover (S16)", () => {
    test("teardownRetry submits exactly the remnantId and projects the teardown-complete arm", async () => {
      const fake = connectFakeClient();
      fake.on("evener/host/list", () => ({ hosts: [row("beta")] }));
      await hostsStore.getState().fetch();
      fake.on("evener/host/teardown-retry", () => ({
        outcome: "teardown-complete",
        hostKind: "live",
        host: row("beta"),
        remnantId: "remnant-7",
      }));

      await hostOpsStore.getState().teardownRetry("beta", "remnant-7");

      // Registry spec 08 §11: params are exactly {remnantId}.
      const call = fake.calls.find((candidate) => candidate.method === "evener/host/teardown-retry");
      expect(call?.params).toEqual({ remnantId: "remnant-7" });
      const repair = hostOpsStore.getState().repairs.beta;
      if (repair?.phase !== "retried") throw new Error("unreachable");
      expect(repair.result).toEqual({
        outcome: "teardown-complete",
        remnantId: "remnant-7",
        hostKind: "live",
        hostName: "beta",
        hostRemoved: false,
      });
      // A resolved remnant converges the rows without waiting for the pane poll.
      await vi.waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/host/list").length).toBe(2));
    });

    test("a failed arm carries the seam and the still-open remnant, never an aggregate success", async () => {
      const fake = connectFakeClient();
      fake.on("evener/host/teardown-retry", () => ({
        outcome: "committed-with-teardown-failure",
        hostKind: "removed",
        host: removedRow("beta"),
        remnantId: "remnant-7",
        seam: "remove-host",
        escalationAgeSec: 7200,
      }));

      await hostOpsStore.getState().teardownRetry("beta", "remnant-7");

      const repair = hostOpsStore.getState().repairs.beta;
      if (repair?.phase !== "retried") throw new Error("unreachable");
      expect(repair.result).toEqual({
        outcome: "committed-with-teardown-failure",
        remnantId: "remnant-7",
        hostKind: "removed",
        hostName: "beta",
        hostRemoved: true,
        seam: "remove-host",
        escalationAgeSec: 7200,
      });
      expect(retryOutcomeLine(repair.result)).toContain("still open");
      expect(retryOutcomeLine(repair.result)).toContain("remove-host");
    });

    test("already-cleared renders as the idempotent resolved arm", async () => {
      const fake = connectFakeClient();
      fake.on("evener/host/teardown-retry", () => ({
        outcome: "already-cleared",
        hostKind: "removed",
        host: removedRow("beta"),
        remnantId: "remnant-7",
      }));

      await hostOpsStore.getState().teardownRetry("beta", "remnant-7");

      const repair = hostOpsStore.getState().repairs.beta;
      if (repair?.phase !== "retried") throw new Error("unreachable");
      expect(repair.result.outcome).toBe("already-cleared");
      expect(retryOutcomeLine(repair.result)).toContain("already cleared");
    });

    test("a live-attempt busy refusal renders the typed busy, never a silent no-op", async () => {
      const fake = connectFakeClient();
      // 08b §6/§11: a live teardown attempt holds the gate, so the retry refuses
      // with the typed busy error (transient here — the holder is the attempt,
      // not an operation record) and the operator may retry later.
      fake.on("evener/host/teardown-retry", () => {
        throw new WireError('host "beta" is busy: a live teardown attempt owns the remnant', -32014, {
          evenerErrorInfo: "host-busy-transient",
        });
      });

      await hostOpsStore.getState().teardownRetry("beta", "remnant-7");

      const repair = hostOpsStore.getState().repairs.beta;
      if (repair?.phase !== "refused") throw new Error("unreachable");
      expect(repair.action).toBe("retry");
      expect(repair.refusal.kind).toBe("host-busy-transient");
      expect(repair.refusal.message).toContain("The host is busy right now.");
      expect(repair.refusal.message).toContain("a live teardown attempt owns the remnant");
      expect(teardownRetryRefusalAction(repair.refusal.kind)).toBe("retry");
    });

    test("teardown-unknown-key names the unknown remnant and never bare-retries it", async () => {
      const fake = connectFakeClient();
      fake.on("evener/host/teardown-retry", () => {
        throw new WireError('teardown remnant "remnant-7" is unknown or purged', -32001, {
          evenerErrorInfo: "teardown-unknown-key",
          remnantId: "remnant-7",
        });
      });

      await hostOpsStore.getState().teardownRetry("beta", "remnant-7");

      const repair = hostOpsStore.getState().repairs.beta;
      if (repair?.phase !== "refused") throw new Error("unreachable");
      expect(repair.refusal.kind).toBe("teardown-unknown-key");
      expect(repair.refusal.remnantId).toBe("remnant-7");
      expect(repair.refusal.message).toContain("remnant-7");
      // A retry naming an id the hub does not know refuses again: no bare retry.
      expect(teardownRetryRefusalAction(repair.refusal.kind)).toBe("none");
    });

    test("teardownRecover submits the wire's attestation fields and projects recovered-cleared", async () => {
      const fake = connectFakeClient();
      fake.on("evener/host/list", () => ({ hosts: [row("beta")] }));
      await hostsStore.getState().fetch();
      fake.on("evener/host/teardown-recover", (params) => ({
        outcome: "recovered-cleared",
        remnantId: params.remnantId,
        clearedName: "beta",
        clearedAt: "2026-09-28T09:30:00Z",
        hostKind: "live",
      }));

      await hostOpsStore.getState().teardownRecover("beta", "remnant-7", {
        operator: "operator-1",
        statement: TEARDOWN_RECOVER_STATEMENT,
        observedAt: "2026-09-28T09:29:00Z",
      });

      const call = fake.calls.find((candidate) => candidate.method === "evener/host/teardown-recover");
      expect(call?.params).toEqual({
        remnantId: "remnant-7",
        attestation: {
          operator: "operator-1",
          statement: "teardown-verified-absent",
          observedAt: "2026-09-28T09:29:00Z",
        },
      });
      const repair = hostOpsStore.getState().repairs.beta;
      if (repair?.phase !== "cleared") throw new Error("unreachable");
      expect(repair.result).toEqual({
        remnantId: "remnant-7",
        clearedName: "beta",
        clearedAt: "2026-09-28T09:30:00Z",
        hostKind: "live",
      });
      expect(clearedOutcomeLine(repair.result)).toContain("beta");
      await vi.waitFor(() => expect(fake.calls.filter((c) => c.method === "evener/host/list").length).toBe(2));
    });

    test("a response landing after a client replacement publishes the connection-changed refusal", async () => {
      const fake = connectFakeClient();
      const settlements = gateSettlements(fake, "evener/host/teardown-retry");
      const pending = hostOpsStore.getState().teardownRetry("beta", "remnant-7");
      await vi.waitFor(() => expect(settlements).toHaveLength(1));

      const replacement = new FakeClient("ready");
      connectionStore.getState().connect(replacement);
      settlements[0]!.resolve({
        outcome: "teardown-complete",
        hostKind: "live",
        host: row("beta"),
        remnantId: "remnant-7",
      });
      await pending;

      // The response describes the hub that was: it must not publish as the new
      // connection's repair state, and the arm is never a silent stall.
      const repair = hostOpsStore.getState().repairs.beta;
      if (repair?.phase !== "refused") throw new Error("unreachable");
      expect(repair.refusal.message).toContain("connection changed");
    });

    test("a superseded repair response publishes nothing", async () => {
      const fake = connectFakeClient();
      const settlements = gateSettlements(fake, "evener/host/teardown-retry");
      const first = hostOpsStore.getState().teardownRetry("beta", "remnant-7");
      await vi.waitFor(() => expect(settlements).toHaveLength(1));
      const second = hostOpsStore.getState().teardownRetry("beta", "remnant-8");
      await vi.waitFor(() => expect(settlements).toHaveLength(2));

      settlements[1]!.resolve({
        outcome: "teardown-complete",
        hostKind: "live",
        host: row("beta"),
        remnantId: "remnant-8",
      });
      await second;
      settlements[0]!.resolve({
        outcome: "teardown-complete",
        hostKind: "live",
        host: row("beta"),
        remnantId: "remnant-7",
      });
      await first;

      const repair = hostOpsStore.getState().repairs.beta;
      if (repair?.phase !== "retried") throw new Error("unreachable");
      expect(repair.result.remnantId).toBe("remnant-8");
    });

    test("clearRepair drops the name's repair state", async () => {
      const fake = connectFakeClient();
      fake.on("evener/host/teardown-retry", () => ({
        outcome: "teardown-complete",
        hostKind: "live",
        host: row("beta"),
        remnantId: "remnant-7",
      }));

      await hostOpsStore.getState().teardownRetry("beta", "remnant-7");
      hostOpsStore.getState().clearRepair("beta");
      expect(hostOpsStore.getState().repairs.beta).toBeUndefined();
    });

    test("the repair outcome lines render each arm honestly, never an aggregate success", () => {
      const base = { remnantId: "remnant-7", hostKind: "live", hostName: "beta", hostRemoved: false };
      expect(retryOutcomeLine({ ...base, outcome: "teardown-complete" })).toContain("remnant-7");
      expect(retryOutcomeLine({ ...base, outcome: "already-cleared" })).toContain("already cleared");
      expect(
        retryOutcomeLine({
          ...base,
          hostKind: "removed",
          hostRemoved: true,
          outcome: "committed-with-teardown-failure",
          seam: "remove-host",
        }),
      ).toContain("still open");
      // An unrecognized future arm renders its raw outcome, never a success claim.
      expect(retryOutcomeLine({ ...base, outcome: "teardown-deferred" })).toContain("teardown-deferred");
    });

    test("an empty remnant id publishes a refusal instead of a silent no-op", async () => {
      const fake = connectFakeClient();
      await hostOpsStore.getState().teardownRetry("beta", "  ");
      expect(fake.calls.filter((c) => c.method === "evener/host/teardown-retry")).toHaveLength(0);
      let repair = hostOpsStore.getState().repairs.beta;
      if (repair?.phase !== "refused") throw new Error("unreachable");
      expect(repair.action).toBe("retry");
      expect(repair.refusal.message).toContain("No remnant id");

      await hostOpsStore.getState().teardownRecover("beta", "", {
        operator: "operator-1",
        statement: TEARDOWN_RECOVER_STATEMENT,
        observedAt: "2026-09-28T09:29:00Z",
      });
      expect(fake.calls.filter((c) => c.method === "evener/host/teardown-recover")).toHaveLength(0);
      repair = hostOpsStore.getState().repairs.beta;
      if (repair?.phase !== "refused") throw new Error("unreachable");
      expect(repair.action).toBe("recover");
      expect(repair.refusal.message).toContain("No remnant id");
    });

    test("a committed-with-teardown-failure arm still converges the host rows", async () => {
      const fake = connectFakeClient();
      let escalation: number | undefined;
      fake.on("evener/host/list", () => ({
        hosts: [
          {
            ...row("beta"),
            openRemnantId: "remnant-7",
            ...(escalation === undefined ? {} : { escalationAgeSec: escalation }),
          },
        ],
      }));
      await hostsStore.getState().fetch();
      fake.on("evener/host/teardown-retry", () => ({
        outcome: "committed-with-teardown-failure",
        hostKind: "live",
        host: { ...row("beta"), openRemnantId: "remnant-7" },
        remnantId: "remnant-7",
        seam: "update-host",
      }));

      // The retry ran past the escalation bound, so the hub stamps the row the
      // next list read answers: the failure arm must converge the rows, or the
      // row-level recover affordance never appears.
      escalation = 7200;
      await hostOpsStore.getState().teardownRetry("beta", "remnant-7");

      await vi.waitFor(() => {
        const load = hostsStore.getState().load;
        if (load.phase !== "ready") throw new Error("unreachable");
        expect(load.hosts[0]?.escalationAgeSec).toBe(7200);
      });
    });

    test("reSeedRestart re-reads the pair and re-seeds the confirmation under a fresh operation ID", async () => {
      const fake = connectFakeClient();
      let generation = 3;
      fake.on("evener/host/list", () => ({
        hosts: [{ ...row("beta"), generation, incarnationId: `inc-${generation}` }],
      }));
      await hostsStore.getState().fetch();
      hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });
      const before = hostOpsStore.getState().restarts.beta;
      if (before === undefined) throw new Error("unreachable");

      generation = 9;
      await hostOpsStore.getState().reSeedRestart("beta");

      const after = hostOpsStore.getState().restarts.beta;
      if (after === undefined) throw new Error("unreachable");
      expect(after.phase).toBe("confirm");
      // The pair the refusal proved stale is never resubmitted: the confirmation
      // is seeded against the freshly read row, under a fresh operation ID.
      expect(after.pair).toEqual({ generation: 9, incarnationId: "inc-9" });
      expect(after.operationId).not.toBe(before.operationId);
    });

    test("clearRepairRefusal drops a refusal but never cancels an in-flight repair", async () => {
      const fake = connectFakeClient();
      // A refused entry is dropped: the fence re-check must be able to clear it.
      fake.on("evener/host/teardown-retry", () => {
        throw new WireError("busy", -32014, { evenerErrorInfo: "host-busy-transient" });
      });
      await hostOpsStore.getState().teardownRetry("beta", "remnant-7");
      expect(hostOpsStore.getState().repairs.beta?.phase).toBe("refused");
      hostOpsStore.getState().clearRepairRefusal("beta");
      expect(hostOpsStore.getState().repairs.beta).toBeUndefined();

      // An IN-FLIGHT entry is untouched: clearing it would drop the arm the
      // live request is about to publish.
      const settlements = gateSettlements(fake, "evener/host/teardown-retry");
      const pending = hostOpsStore.getState().teardownRetry("beta", "remnant-7");
      await vi.waitFor(() => expect(settlements).toHaveLength(1));
      hostOpsStore.getState().clearRepairRefusal("beta");
      expect(hostOpsStore.getState().repairs.beta?.phase).toBe("retrying");
      settlements[0]!.resolve({
        outcome: "teardown-complete",
        hostKind: "live",
        host: row("beta"),
        remnantId: "remnant-7",
      });
      await pending;
      const repair = hostOpsStore.getState().repairs.beta;
      if (repair?.phase !== "retried") throw new Error("unreachable");
      expect(repair.result.outcome).toBe("teardown-complete");
    });

    test("a re-seed landing after a newer restart attempt publishes nothing", async () => {
      const fake = connectFakeClient();
      fake.on("evener/host/list", () => ({ hosts: [row("beta")] }));
      await hostsStore.getState().fetch();
      hostOpsStore.getState().beginRestart("beta", { generation: 3, incarnationId: "inc-3" });

      // The re-seed's forced read hangs while a NEWER attempt (a re-opened
      // dialog) supersedes the one the continuation was started for.
      const settlements = gateSettlements(fake, "evener/host/list");
      const reseed = hostOpsStore.getState().reSeedRestart("beta");
      await vi.waitFor(() => expect(settlements).toHaveLength(1));
      hostOpsStore.getState().beginRestart("beta", { generation: 9, incarnationId: "inc-9" });
      const newer = hostOpsStore.getState().restarts.beta;
      if (newer === undefined) throw new Error("unreachable");

      settlements[0]!.resolve({ hosts: [{ ...row("beta"), generation: 3, incarnationId: "inc-3" }] });
      await reseed;

      // The stale continuation must not overwrite the newer attempt's pair or
      // operation ID.
      const after = hostOpsStore.getState().restarts.beta;
      if (after === undefined) throw new Error("unreachable");
      expect(after.pair).toEqual(newer.pair);
      expect(after.operationId).toBe(newer.operationId);
    });

    test("teardown recovery actions: the transient busy retries; occupied gate and unknown key never bare-retry", () => {
      expect(teardownRetryRefusalAction("host-busy-transient")).toBe("retry");
      expect(teardownRetryRefusalAction("host-busy-operation")).toBe("none");
      expect(teardownRetryRefusalAction("teardown-unknown-key")).toBe("none");
      expect(teardownRecoverRefusalAction("host-busy-transient")).toBe("retry");
      expect(teardownRecoverRefusalAction("invalid-params")).toBe("none");
    });
  });
});
