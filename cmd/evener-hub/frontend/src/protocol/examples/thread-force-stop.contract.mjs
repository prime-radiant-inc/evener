import assert from "node:assert/strict";
import test from "node:test";
import { runThreadForceStop } from "./thread-force-stop-logic.mjs";

const url = "ws://127.0.0.1:1/rpc";
function optedIn(run) {
  const old = process.env.EVENER_THREAD_FORCE_STOP_MUTATION;
  const oldURL = process.env.EVENER_RPC_URL;
  process.env.EVENER_THREAD_FORCE_STOP_MUTATION = "1";
  process.env.EVENER_RPC_URL = url;
  return Promise.resolve(run()).finally(() => {
    if (old === undefined) delete process.env.EVENER_THREAD_FORCE_STOP_MUTATION;
    else process.env.EVENER_THREAD_FORCE_STOP_MUTATION = old;
    if (oldURL === undefined) delete process.env.EVENER_RPC_URL;
    else process.env.EVENER_RPC_URL = oldURL;
  });
}

test("force stop uses the client's dedicated recovery operation", () =>
  optedIn(async () => {
    const calls = [];
    const hub = {
      async connect() {
        calls.push("connect");
      },
      async forceStop(ref) {
        calls.push(["forceStop", ref]);
      },
      async request() {
        throw new Error("ordinary RPC queue must not be used");
      },
    };
    assert.deepEqual(await runThreadForceStop(hub, { ref: "local:one", ownedHub: url }), {
      outcome: "acknowledged",
      execution: "unverified",
    });
    assert.deepEqual(calls, ["connect", ["forceStop", "local:one"]]);
  }));

test("force stop validates ownership and reference before connecting", async () => {
  await optedIn(async () => {
    const hub = {
      connect: async () => {
        throw new Error("must not connect");
      },
    };
    await assert.rejects(runThreadForceStop(hub, { ref: "", ownedHub: url }));
    await assert.rejects(runThreadForceStop(hub, { ref: "local:one", ownedHub: "ws://other/rpc" }));
  });
});
