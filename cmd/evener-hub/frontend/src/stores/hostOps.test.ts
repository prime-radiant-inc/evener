// hostOps.test.ts — store-level unit tests for the deploy pipeline's Hosts UI
// state (stores/hostOps.ts): the plan -> confirm -> deploy flow, the no-token
// branch, the token-refusal re-plan, and the §11 refusal vocabulary's concrete
// rendering. Component-level rendering tests live in
// panes/settings/sections/hosts.test.tsx.
//
// Pattern mirrors hosts.test.ts: each test resets the stores and connection in
// beforeEach, and a FakeClient answers the wire methods.

import { type HostPlan, type HostRow, RequestTimeoutError, WireError } from "@evener/appwire-client";
import { FakeClient, gateSettlements } from "@evener/appwire-client/testing/fakeClient";
import { beforeEach, describe, expect, test } from "vitest";
import { connectionStore } from "./connection";
import {
  deployRefusalAction,
  hostOpRefusal,
  hostOpRefusalBlocksRetry,
  hostOpsStore,
  planNoTokenAction,
  planRefusalAction,
  restartRefusalAction,
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
      { info: "stale-entry", want: "The host changed since this plan was made." },
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
      state: "pending",
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
      state: "pending",
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
    expect(state.notice).toContain("The host changed since this plan was made.");
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
      state: "pending",
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
