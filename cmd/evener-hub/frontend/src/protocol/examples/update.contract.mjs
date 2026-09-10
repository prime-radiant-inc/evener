import assert from "node:assert/strict";
import test from "node:test";
import { runUpdate } from "./update-logic.mjs";

const url = "ws://127.0.0.1:1/rpc";
const check = {
  channel: "stable",
  buildChannel: "stable",
  currentVersion: "dev",
  currentCommit: "",
  updateAvailable: false,
  applicable: false,
};
const apply = { release: "", channel: "stable", installed: [], restarting: false };
function fixture(replies) {
  const calls = [];
  return {
    calls,
    hub: {
      async connect() {
        calls.push("connect");
      },
      async request(method, params) {
        calls.push({ method, params });
        const next = replies.shift();
        return typeof next === "function" ? next() : next;
      },
    },
  };
}
async function optedIn(run) {
  const old = process.env.EVENER_UPDATE_MUTATION;
  const oldURL = process.env.EVENER_RPC_URL;
  process.env.EVENER_UPDATE_MUTATION = "1";
  process.env.EVENER_RPC_URL = url;
  try {
    return await run();
  } finally {
    if (old === undefined) delete process.env.EVENER_UPDATE_MUTATION;
    else process.env.EVENER_UPDATE_MUTATION = old;
    if (oldURL === undefined) delete process.env.EVENER_RPC_URL;
    else process.env.EVENER_RPC_URL = oldURL;
  }
}
test("update check is read-only and preserves the channel", async () => {
  const f = fixture([check]);
  assert.deepEqual(await runUpdate(f.hub, { params: { channel: "stable" } }), { outcome: "read", readback: check });
  assert.deepEqual(f.calls, ["connect", { method: "evener/update/check", params: { channel: "stable" } }]);
});
test("update apply requires ownership and reconciles with a fresh check", () =>
  optedIn(async () => {
    const f = fixture([apply, check]);
    const result = await runUpdate(f.hub, { action: "apply", params: { channel: "stable" }, ownedHub: url });
    assert.equal(result.outcome, "acknowledged");
    assert.equal(result.execution, "unverified");
    assert.deepEqual(
      f.calls.map((call) => (typeof call === "string" ? call : call.method)),
      ["connect", "evener/update/apply", "evener/update/check"],
    );
  }));
test("update mutations reject malformed parameters before connection", async () => {
  await optedIn(async () => {
    const f = fixture([]);
    await assert.rejects(runUpdate(f.hub, { action: "apply", params: { channel: 3 }, ownedHub: url }));
    assert.deepEqual(f.calls, []);
  });
});
