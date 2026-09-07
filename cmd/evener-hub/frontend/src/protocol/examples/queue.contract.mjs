import assert from "node:assert/strict";
import { test } from "node:test";
import { ownedHub, scriptedHub, withMutationEnv } from "./management-contract-fixtures.mjs";
import { runQueue } from "./queue-logic.mjs";

const ref = "local:queue";
const instanceId = "instance";
const clientMutationId = "queue-action-once";
const actions = ["queue", "cancel", "promote", "drain"];
const methods = {
  queue: "turn/queue",
  cancel: "turn/cancelQueued",
  promote: "turn/promoteQueuedAsSteer",
  drain: "turn/drainAsSteer",
};
const queue = () => ({ revision: 7, depth: 2, ids: ["a", "b"], texts: ["alpha", "beta"] });
const snapshot = (evener = {}) => ({
  thread: {
    id: "queue-thread",
    evener: {
      ref,
      instanceId,
      queue: queue(),
      capabilities: { queue: true, send: false, steer: true },
      ...evener,
    },
  },
});
const params = (action) => ({
  ref,
  expectedInstanceId: instanceId,
  clientMutationId,
  ...(action === "queue" ? { input: [{ type: "text", text: "sentinel" }] } : {}),
  ...(["cancel", "promote"].includes(action) ? { index: 1, expectedEntryId: "b" } : {}),
  ...(action === "drain" ? { expectedQueueRevision: 7 } : {}),
});
const response = (action, change = {}) => ({
  ...(action === "cancel" ? { removedText: "beta", removedImages: 1 } : {}),
  receipt: {
    clientMutationId,
    instanceId,
    threadId: "queue-thread",
    disposition: "applied",
    projectionState: action === "cancel" ? "removed" : "pending",
    queueEntryIds: action === "queue" ? ["c"] : action === "drain" ? ["a", "b"] : ["b"],
    ...(["promote", "drain"].includes(action) ? { turnId: "turn-2" } : {}),
    ...change,
  },
});
const mutate = (run) => withMutationEnv("EVENER_QUEUE_MUTATION", run);
const run = (script, action, input = params(action)) => runQueue(script.hub, { action, params: input, ownedHub });
const writes = (script) => script.calls.filter((call) => call.method.startsWith("turn/"));

test("queue list is read-only and keeps omitted empty queue fields", async () => {
  const script = scriptedHub([() => snapshot({ queue: { revision: 0 } })]);
  const result = await runQueue(script.hub, { params: { ref } });
  assert.equal(result.outcome, "read");
  assert.equal(result.execution, "unverified");
  assert.deepEqual(result.readback.thread.evener.queue, { revision: 0 });
  assert.deepEqual(script.calls, [
    { method: "connect" },
    { method: "thread/read", params: { ref, includeTurns: false, subscribe: false } },
  ]);
});

test("all queue actions use guarded wire parameters once and validate their receipt", () =>
  mutate(async () => {
    for (const action of actions) {
      for (const disposition of ["applied", "replayed"]) {
        const ack = response(action, { disposition });
        if (action === "cancel" && disposition === "replayed") delete ack.removedImages;
        const after = snapshot({ queue: { revision: 8 } });
        const script = scriptedHub([() => snapshot(), () => ack, () => after]);
        const result = await run(script, action);
        assert.equal(result.outcome, "acknowledged");
        assert.equal(result.execution, "unverified");
        assert.deepEqual(result.receipt, ack.receipt);
        assert.deepEqual(result.readback, after);
        assert.deepEqual(writes(script), [{ method: methods[action], params: params(action) }]);
        assert.equal(script.calls.filter((c) => c.method === "thread/read").length, 2);
      }
    }
  }));

test("queue ownership and invalid authored parameters refuse before connecting", () =>
  mutate(async () => {
    const bad = [
      ["queue", { ...params("queue"), input: "" }],
      ["queue", { ...params("queue"), input: [] }],
      ["queue", { ...params("queue"), input: [{ type: "text", text: " " }] }],
      ["queue", { ...params("queue"), input: [{ type: "image", url: "x" }] }],
      ["queue", { ...params("queue"), input: [{ type: "text", text: "x", unknown: true }] }],
      ["cancel", { ...params("cancel"), index: -1 }],
      ["cancel", { ...params("cancel"), expectedEntryId: "" }],
      ["drain", { ...params("drain"), expectedQueueRevision: 0.5 }],
      ["drain", { ...params("drain"), input: [] }],
      ["queue", { ...params("queue"), clientMutationId: " padded " }],
      ["queue", { ...params("queue"), expectedInstanceId: "" }],
      [["queue"], params("queue")],
    ];
    for (const [action, input] of bad) {
      const script = scriptedHub([]);
      await assert.rejects(run(script, action, input));
      assert.equal(script.calls.length, 0);
    }
    for (const owner of [undefined, "ws://other/rpc"]) {
      const script = scriptedHub([]);
      await assert.rejects(runQueue(script.hub, { action: "queue", params: params("queue"), ownedHub: owner }));
      assert.equal(script.calls.length, 0);
    }
    delete process.env.EVENER_QUEUE_MUTATION;
    const script = scriptedHub([]);
    await assert.rejects(run(script, "cancel"));
    assert.equal(script.calls.length, 0);
  }));

test("queue preflight refuses shifted entries, stale revisions, missing capabilities and bindings", () =>
  mutate(async () => {
    const cases = [
      ["cancel", { queue: { ...queue(), ids: ["b", "a"] } }],
      ["promote", { queue: { ...queue(), ids: ["b", "a"] } }],
      ["cancel", { queue: { revision: 9 } }],
      ["drain", { queue: { ...queue(), revision: 8 } }],
      ["drain", { queue: { revision: 7 } }],
      ["drain", { queue: { revision: 7, depth: 2 } }],
      ["queue", { capabilities: { queue: false, send: true, steer: true } }],
      ["promote", { capabilities: { send: false, steer: false } }],
      ["drain", { capabilities: { send: false, steer: false } }],
      ["queue", { instanceId: "replacement" }],
      ["queue", { ref: "local:other" }],
    ];
    for (const [action, evener] of cases) {
      const script = scriptedHub([() => snapshot(evener)]);
      await assert.rejects(run(script, action));
      assert.equal(writes(script).length, 0);
    }
  }));

test("held queues can be cancelled without capabilities or resumed with send", () =>
  mutate(async () => {
    for (const action of ["cancel", "promote", "drain"]) {
      const caps = { queue: false, steer: false, send: action !== "cancel" };
      const script = scriptedHub([() => snapshot({ capabilities: caps }), () => response(action), () => snapshot()]);
      assert.equal((await run(script, action)).outcome, "acknowledged");
    }
  }));

test("malformed queue snapshots do not become writable reviews", () =>
  mutate(async () => {
    for (const state of [
      null,
      [],
      { revision: -1 },
      { revision: 1, depth: -1 },
      { revision: 1, depth: null },
      { revision: 1, depth: 2, ids: ["a"] },
      { revision: 1, depth: 2, ids: ["a", "a"] },
      { revision: 1, depth: 1, ids: [""] },
      { revision: 1, depth: 1, texts: [null] },
      { revision: 1, depth: 2, preview: ["a"] },
      { revision: 1, depth: 1, clientMutationIds: [false] },
    ]) {
      const script = scriptedHub([() => snapshot({ queue: state })]);
      await assert.rejects(run(script, "queue"));
      assert.equal(writes(script).length, 0);
    }
  }));

test("malformed queue receipts remain uncertain, refresh once, and expose no receipt", () =>
  mutate(async () => {
    for (const action of actions) {
      const invalid = [
        null,
        {},
        { receipt: null },
        response(action, { clientMutationId: "other" }),
        response(action, { threadId: "other" }),
        response(action, { instanceId: undefined }),
        response(action, { instanceId: "other" }),
        response(action, { disposition: "unknown" }),
        response(action, { projectionState: "reflected" }),
        response(action, { queueEntryIds: [] }),
        response(action, { queueEntryIds: ["a", "a"] }),
        response(action, { queueEntryIds: [""] }),
      ];
      if (action !== "queue") invalid.push(response(action, { queueEntryIds: ["unknown"] }));
      if (action === "queue") invalid.push(response(action, { queueEntryIds: ["c", "d"] }));
      if (action === "drain")
        invalid.push(response(action, { queueEntryIds: ["a"] }), response(action, { queueEntryIds: ["b", "a"] }));
      if (["promote", "drain"].includes(action)) invalid.push(response(action, { turnId: "" }));
      else invalid.push(response(action, { turnId: "unexpected" }));
      if (action === "cancel")
        invalid.push({ ...response(action), removedText: null }, { ...response(action), removedImages: -1 });
      for (const ack of invalid) {
        const script = scriptedHub([() => snapshot(), () => ack, () => snapshot({ queue: { revision: 8 } })]);
        const result = await run(script, action);
        assert.equal(result.outcome, "uncertain");
        assert.equal(result.receipt, undefined);
        assert.equal(writes(script).length, 1);
        assert.equal(script.calls.filter((c) => c.method === "thread/read").length, 2);
      }
    }
  }));

test("lost queue responses never replay even when state changed", () =>
  mutate(async () => {
    for (const failure of [new Error("lost"), null, undefined]) {
      const script = scriptedHub([
        () => snapshot(),
        () => {
          throw failure;
        },
        () => snapshot({ queue: { revision: 9 } }),
      ]);
      const result = await run(script, "drain");
      assert.equal(result.outcome, "uncertain");
      assert.equal(result.execution, "unverified");
      assert.equal(writes(script).length, 1);
    }
  }));

test("readback rejects replacement bindings and retains dual failures", () =>
  mutate(async () => {
    for (const action of actions) {
      for (const replacement of [
        snapshot({ instanceId: "replacement" }),
        snapshot({ ref: "other" }),
        { ...snapshot(), thread: { ...snapshot().thread, id: "other" } },
      ]) {
        const script = scriptedHub([() => snapshot(), () => response(action), () => replacement]);
        await assert.rejects(run(script, action));
        assert.equal(writes(script).length, 1);
      }
    }
    const readError = new Error("read unavailable");
    const ack = scriptedHub([
      () => snapshot(),
      () => response("queue"),
      () => {
        throw readError;
      },
    ]);
    await assert.rejects(run(ack, "queue"), (error) => error === readError);
    const both = scriptedHub([
      () => snapshot(),
      () => {
        throw undefined;
      },
      () => {
        throw readError;
      },
    ]);
    await assert.rejects(run(both, "queue"), (error) => {
      assert.ok(error instanceof AggregateError);
      assert.deepEqual(error.errors, [undefined, readError]);
      return true;
    });
  }));

test("caller changes during connection cannot change the reviewed queue action", () =>
  mutate(async () => {
    const input = params("queue");
    const original = structuredClone(input);
    const script = scriptedHub([() => snapshot(), () => response("queue"), () => snapshot()]);
    const connect = script.hub.connect;
    script.hub.connect = async () => {
      input.ref = "other";
      input.input[0].text = "different";
      await connect();
    };
    await run(script, "queue", input);
    assert.deepEqual(writes(script), [{ method: "turn/queue", params: original }]);
  }));
