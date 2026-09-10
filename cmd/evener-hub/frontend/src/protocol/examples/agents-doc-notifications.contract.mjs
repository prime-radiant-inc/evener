import assert from "node:assert/strict";
import test from "node:test";
import { runAgentsDocNotifications } from "./agents-doc-notifications-logic.mjs";

const before = { path: "/fixture/AGENTS.md", exists: true, content: "before" };
const after = { ...before, content: "after" };
function boundary(read) {
  let notification;
  let state;
  let removals = 0;
  let reads = 0;
  return {
    emit: (params) => notification({ method: "evener/settings/agentsDoc/changed", params }),
    interrupt: () => state("reconnecting"),
    removals: () => removals,
    reads: () => reads,
    hub: {
      onNotification: (listener) => {
        notification = listener;
        return () => {
          removals++;
        };
      },
      onStateChange: (listener) => {
        state = listener;
        return () => {
          removals++;
        };
      },
      connect: async () => {
        assert.equal(typeof notification, "function");
      },
      request: async (method, params) => {
        assert.equal(method, "evener/settings/agentsDoc/get");
        assert.deepEqual(params, {});
        return read(++reads);
      },
    },
  };
}
test("observes a document change and reads the final document without a mutation", async () => {
  const io = boundary((read) => (read === 1 ? before : after));
  const result = await runAgentsDocNotifications(io.hub, { observe: async () => io.emit(after) });
  assert.equal(result.outcome, "read");
  assert.deepEqual(result.initial, before);
  assert.deepEqual(result.readback, after);
  assert.deepEqual(result.events, [{ method: "evener/settings/agentsDoc/changed", ...after }]);
  assert.equal(io.reads(), 2);
  assert.equal(io.removals(), 2);
});
test("a change during final readback or a disconnect keeps observation uncertain", async () => {
  for (const interrupted of [false, true]) {
    const io = boundary((read) => {
      if (read === 2 && !interrupted) io.emit(after);
      return after;
    });
    const result = await runAgentsDocNotifications(io.hub, {
      observe: async () => {
        if (interrupted) io.interrupt();
      },
    });
    assert.equal(result.outcome, "uncertain");
    assert.equal(result.connectionInterrupted, interrupted);
    assert.equal(result.changedDuringReadback, !interrupted);
    assert.equal(io.removals(), 2);
  }
});
test("malformed matching document events fail and detach both listeners", async () => {
  const io = boundary(() => before);
  await assert.rejects(runAgentsDocNotifications(io.hub, { observe: async () => io.emit({ ...after, content: 12 }) }));
  assert.equal(io.removals(), 2);
});
