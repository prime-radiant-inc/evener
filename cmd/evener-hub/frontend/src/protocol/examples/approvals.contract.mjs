import assert from "node:assert/strict";
import test from "node:test";
import { runApprovals } from "./approvals-logic.mjs";
import { ownedHub, scriptedHub, withMutationEnv } from "./management-contract-fixtures.mjs";
import { AcknowledgedReadbackError } from "./management-recovery.mjs";

const card = {
  threadId: "thread-1",
  ref: "local:session-1",
  escalationId: "approval-1",
  mode: "workspace-write",
  tool: "shell",
  kind: "command",
  deniedPath: "",
  command: "owned-fixture-command",
  outputSoFar: "",
  partiallyRan: false,
};
const readMethod = "thread/read";
const resolve = "evener/sandbox/escalation/resolve";
const readParams = { ref: card.ref, includeTurns: false, subscribe: false };
const read = (pending = [card], overrides = {}) => ({
  thread: {
    id: card.threadId,
    evener: { ref: card.ref, instanceId: "instance-1", pendingEscalations: pending, ...overrides },
  },
});
const params = { ref: card.ref, expectedInstanceId: "instance-1", escalation: card, approve: false };
const options = { action: "resolve", params, ownedHub };
const optedIn = (run) => withMutationEnv("EVENER_APPROVAL_MUTATION", run);

test("approval read connects without subscribing and accepts omitted pending approvals", async () => {
  const omitted = read();
  delete omitted.thread.evener.pendingEscalations;
  const f = scriptedHub([() => omitted]);
  const result = await runApprovals(f.hub, { params: { ref: card.ref } });
  assert.equal(result.outcome, "read");
  assert.equal(result.execution, "unverified");
  assert.equal(result.readback.thread.evener.pendingEscalations?.length ?? 0, 0);
  assert.deepEqual(f.calls, [{ method: "connect" }, { method: readMethod, params: readParams }]);
});
test("approval input, action and ownership guards reject before connect", async () =>
  optedIn(async () => {
    const f = scriptedHub([]);
    delete process.env.EVENER_APPROVAL_MUTATION;
    await assert.rejects(runApprovals(f.hub, options));
    process.env.EVENER_APPROVAL_MUTATION = "1";
    for (const change of [
      { ownedHub: undefined },
      { ownedHub: "ws://other/rpc" },
      { action: "toString" },
      { action: "" },
      { params: null },
      { params: { ...params, unknown: true } },
      { params: { ...params, approve: "false" } },
      { params: { ...params, expectedInstanceId: "" } },
      { params: { ...params, ref: " " } },
      { params: { ...params, escalation: { escalationId: card.escalationId, ref: card.ref } } },
      { params: { ...params, escalation: { ...card, ref: "different" } } },
      { params: { ...params, escalation: { ...card, partiallyRan: "false" } } },
      { action: "list", params },
    ])
      await assert.rejects(runApprovals(f.hub, { ...options, ...change }));
    assert.deepEqual(f.calls, []);
  }));
test("approval preflight rejects changed binding, missing or changed full card", async () =>
  optedIn(async () => {
    for (const before of [
      read([card], { ref: "other" }),
      read([card], { instanceId: "replaced" }),
      read([]),
      read([{ ...card, command: "changed-command" }]),
      read([{ ...card, partiallyRan: true }]),
      read([null]),
      read(null),
    ]) {
      const f = scriptedHub([() => before]);
      await assert.rejects(runApprovals(f.hub, options));
      assert.deepEqual(
        f.calls.map((c) => c.method),
        ["connect", readMethod],
      );
    }
  }));
for (const approve of [false, true])
  test(`approval sends ${approve} once with exact card binding`, async () =>
    optedIn(async () => {
      const reordered = Object.fromEntries(Object.entries(card).reverse());
      const f = scriptedHub([() => read([reordered]), () => ({}), () => read([])]);
      const result = await runApprovals(f.hub, { ...options, params: { ...params, approve } });
      assert.equal(result.outcome, "acknowledged");
      assert.equal(result.execution, "unverified");
      assert.deepEqual(f.calls, [
        { method: "connect" },
        { method: readMethod, params: readParams },
        { method: resolve, params: { ref: card.ref, escalationId: card.escalationId, approve } },
        { method: readMethod, params: readParams },
      ]);
    }));
test("approval captures reviewed input before awaiting connection", async () =>
  optedIn(async () => {
    const input = structuredClone(params);
    const f = scriptedHub([() => read(), () => ({}), () => read([])]);
    const connect = f.hub.connect;
    f.hub.connect = async () => {
      await connect();
      input.approve = true;
      input.escalation.command = "unreviewed-change";
    };
    await runApprovals(f.hub, { ...options, params: input });
    assert.equal(f.calls.find((c) => c.method === resolve).params.approve, false);
  }));
for (const cause of [new Error("lost reply"), undefined, null])
  test(`approval lost reply stays uncertain with absent card: ${String(cause)}`, async () =>
    optedIn(async () => {
      const f = scriptedHub([
        () => read(),
        () => {
          throw cause;
        },
        () => read([]),
      ]);
      const result = await runApprovals(f.hub, options);
      assert.equal(result.outcome, "uncertain");
      assert.equal(result.execution, "unverified");
      assert.deepEqual(
        f.calls.map((c) => c.method),
        ["connect", readMethod, resolve, readMethod],
      );
    }));
test("approval invalid ACK triggers readback and never implies execution", async () =>
  optedIn(async () => {
    for (const ack of [null, undefined, []]) {
      const f = scriptedHub([() => read(), () => ack, () => read([])]);
      assert.equal((await runApprovals(f.hub, options)).outcome, "uncertain");
    }
  }));
test("approval readback failure preserves read cause or both ordered causes", async () =>
  optedIn(async () => {
    const readError = new Error("private read");
    const f = scriptedHub([
      () => read(),
      () => ({}),
      () => {
        throw readError;
      },
    ]);
    await assert.rejects(
      runApprovals(f.hub, options),
      (e) => e instanceof AcknowledgedReadbackError && e.cause === readError,
    );
    for (const cause of [new Error("private mutation"), undefined, null]) {
      const bad = scriptedHub([
        () => read(),
        () => {
          throw cause;
        },
        () => {
          throw readError;
        },
      ]);
      await assert.rejects(
        runApprovals(bad.hub, options),
        (e) => e instanceof AggregateError && e.errors[0] === cause && e.errors[1] === readError,
      );
      assert.equal(bad.calls.filter((c) => c.method === resolve).length, 1);
    }
  }));
test("approval replacement readback is rejected including after mutation failure", async () =>
  optedIn(async () => {
    for (const changed of [read([], { instanceId: "replacement" }), read([], { ref: "other" })]) {
      const f = scriptedHub([() => read(), () => ({}), () => changed]);
      await assert.rejects(runApprovals(f.hub, options));
      const lost = new Error("lost");
      const bad = scriptedHub([
        () => read(),
        () => {
          throw lost;
        },
        () => changed,
      ]);
      await assert.rejects(
        runApprovals(bad.hub, options),
        (e) => e instanceof AggregateError && e.errors[0] === lost && e.errors[1] instanceof Error,
      );
    }
  }));
test("readonly approval response must belong to requested ref", async () => {
  const f = scriptedHub([() => read([], { ref: "other" })]);
  await assert.rejects(runApprovals(f.hub, { params: { ref: card.ref } }));
});
