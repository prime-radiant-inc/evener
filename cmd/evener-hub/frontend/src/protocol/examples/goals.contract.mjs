import assert from "node:assert/strict";
import { test } from "node:test";
import { runGoals } from "./goals-logic.mjs";
import { ownedHub, scriptedHub, withMutationEnv } from "./management-contract-fixtures.mjs";

const ref = "local:goal",
  instanceId = "instance";
const goal = { objective: "before", status: "active", iterations: 2 };
const read = (evener = {}) => ({
  thread: {
    id: "thread",
    evener: { ref, instanceId, goal: structuredClone(goal), capabilities: { goal: true }, ...evener },
  },
});
const params = (action) => ({
  ref,
  expectedInstanceId: instanceId,
  reviewedGoal: structuredClone(goal),
  ...(action === "set" ? { objective: "  authored\nobjective  " } : {}),
});
const mutate = (run) => withMutationEnv("EVENER_GOAL_MUTATION", run);
const run = (script, action, input = params(action)) => runGoals(script.hub, { action, params: input, ownedHub });
const writes = (script) => script.calls.filter((c) => c.method === "goal/set");

test("goal list is readonly with absent or current state", async () => {
  for (const state of [undefined, null, goal]) {
    const snapshot = read({ goal: state }),
      script = scriptedHub([() => snapshot]);
    const result = await runGoals(script.hub, { params: { ref } });
    assert.deepEqual(result, { outcome: "read", execution: "unverified", readback: snapshot });
    assert.equal(writes(script).length, 0);
  }
});
test("set and clear send only generated wire fields and keep started distinct from execution", () =>
  mutate(async () => {
    for (const action of ["set", "clear"])
      for (const started of [false, true]) {
        const after = read({ goal: { objective: "another", status: "achieved", iterations: 3 } });
        const script = scriptedHub([() => read(), () => ({ started }), () => after]);
        const result = await run(script, action);
        assert.equal(result.outcome, "acknowledged");
        assert.equal(result.started, started);
        assert.equal(result.execution, "unverified");
        assert.deepEqual(result.readback, after);
        assert.deepEqual(writes(script), [
          { method: "goal/set", params: { ref, objective: action === "set" ? params(action).objective : "" } },
        ]);
      }
  }));
test("authored goal parameters and ownership are checked before connect", () =>
  mutate(async () => {
    for (const [action, input] of [
      ["set", { ...params("set"), objective: " " }],
      ["clear", { ...params("clear"), objective: "" }],
      ["set", { ...params("set"), expectedInstanceId: "" }],
      ["set", { ...params("set"), clientMutationId: "invented" }],
      ["set", { ...params("set"), reviewedGoal: undefined }],
      ["set", { ...params("set"), reviewedGoal: { status: "active" } }],
      ["set", { ...params("set"), reviewedGoal: { ...goal, iterations: -1 } }],
      ["set", { ...params("set"), reviewedGoal: { ...goal, unknown: true } }],
      [["set"], params("set")],
    ]) {
      const script = scriptedHub([]);
      await assert.rejects(run(script, action, input));
      assert.equal(script.calls.length, 0);
    }
    const missing = params("set");
    delete missing.reviewedGoal;
    const absent = scriptedHub([]);
    await assert.rejects(run(absent, "set", missing));
    assert.equal(absent.calls.length, 0);
    for (const owner of [undefined, "ws://other/rpc"]) {
      const script = scriptedHub([]);
      await assert.rejects(runGoals(script.hub, { action: "set", params: params("set"), ownedHub: owner }));
      assert.equal(script.calls.length, 0);
    }
    delete process.env.EVENER_GOAL_MUTATION;
    const script = scriptedHub([]);
    await assert.rejects(run(script, "clear"));
    assert.equal(script.calls.length, 0);
  }));
test("goal preflight refuses changed review, unavailable capability and replacement binding", () =>
  mutate(async () => {
    for (const evener of [
      { goal: undefined },
      { goal: { ...goal, iterations: 3 } },
      { goal: { ...goal, objective: "changed" } },
      { goal: { ...goal, status: "achieved" } },
      { capabilities: { goal: false } },
      { capabilities: undefined },
      { instanceId: "other" },
      { ref: "other" },
      { goal: { ...goal, iterations: 0.5 } },
      { goal: [] },
      { goal: { ...goal, objective: null } },
    ]) {
      const script = scriptedHub([() => read(evener)]);
      await assert.rejects(run(script, "set"));
      assert.equal(writes(script).length, 0);
    }
    const script = scriptedHub([
      () => read({ goal: undefined }),
      () => ({ started: false }),
      () => read({ goal: undefined }),
    ]);
    assert.equal((await run(script, "clear", { ...params("clear"), reviewedGoal: null })).outcome, "acknowledged");
  }));
test("goal decisions are cloned before connection and reviewed key order is irrelevant", () =>
  mutate(async () => {
    const input = params("set"),
      expected = input.objective;
    const script = scriptedHub([() => read(), () => ({ started: true }), () => read()]);
    const connect = script.hub.connect;
    script.hub.connect = async () => {
      input.objective = "changed";
      input.reviewedGoal.iterations = 99;
      await connect();
    };
    await run(script, "set", input);
    assert.equal(writes(script)[0].params.objective, expected);
    const reordered = { iterations: 2, status: "active", objective: "before" };
    const another = scriptedHub([() => read(), () => ({ started: false }), () => read()]);
    assert.equal(
      (await run(another, "clear", { ...params("clear"), reviewedGoal: reordered })).outcome,
      "acknowledged",
    );
  }));
test("malformed or lost goal acknowledgments stay uncertain even when desired state appears", () =>
  mutate(async () => {
    for (const reply of [
      () => null,
      () => undefined,
      () => ({}),
      () => [],
      () => ({ started: "true" }),
      () => {
        throw null;
      },
      () => {
        throw undefined;
      },
      () => {
        throw new Error("lost");
      },
    ]) {
      const script = scriptedHub([() => read(), reply, () => read({ goal: undefined })]);
      const result = await run(script, "clear");
      assert.equal(result.outcome, "uncertain");
      assert.equal(result.started, undefined);
      assert.equal(result.execution, "unverified");
      assert.equal(writes(script).length, 1);
    }
  }));
test("goal readback enforces binding and preserves ordered dual errors", () =>
  mutate(async () => {
    for (const after of [
      read({ instanceId: "other" }),
      read({ ref: "other" }),
      { thread: { ...read().thread, id: "other" } },
    ]) {
      const script = scriptedHub([() => read(), () => ({ started: true }), () => after]);
      await assert.rejects(run(script, "set"));
      assert.equal(writes(script).length, 1);
    }
    const error = new Error("read");
    const ack = scriptedHub([
      () => read(),
      () => ({ started: false }),
      () => {
        throw error;
      },
    ]);
    await assert.rejects(run(ack, "clear"), (e) => e === error);
    const both = scriptedHub([
      () => read(),
      () => {
        throw undefined;
      },
      () => {
        throw null;
      },
    ]);
    await assert.rejects(run(both, "clear"), (e) => {
      assert.ok(e instanceof AggregateError);
      assert.deepEqual(e.errors, [undefined, null]);
      return true;
    });
  }));
