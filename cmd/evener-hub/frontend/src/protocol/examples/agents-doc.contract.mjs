import assert from "node:assert/strict";
import test from "node:test";
import { runAgentsDoc } from "./agents-doc-logic.mjs";

const url = "ws://127.0.0.1:1/rpc";
const before = { path: "/config/AGENTS.md", exists: true, content: "old" },
  after = { ...before, content: "new" };
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
        return replies.shift();
      },
    },
  };
}
test("agents document get is read-only", async () => {
  const f = fixture([before]);
  assert.deepEqual(await runAgentsDoc(f.hub), { outcome: "read", readback: before });
});
test("agents document set reviews and reads back the whole document", async () => {
  const old = process.env.EVENER_AGENTS_DOC_MUTATION;
  const oldURL = process.env.EVENER_RPC_URL;
  process.env.EVENER_AGENTS_DOC_MUTATION = "1";
  process.env.EVENER_RPC_URL = url;
  try {
    const f = fixture([before, after, after]);
    const result = await runAgentsDoc(f.hub, {
      action: "set",
      params: { content: "new", reviewed: before },
      ownedHub: url,
    });
    assert.equal(result.outcome, "acknowledged");
    assert.equal(result.execution, "unverified");
    assert.deepEqual(
      f.calls.map((x) => (typeof x === "string" ? x : x.method)),
      ["connect", "evener/settings/agentsDoc/get", "evener/settings/agentsDoc/set", "evener/settings/agentsDoc/get"],
    );
    assert.deepEqual(f.calls[2].params, { content: "new" });
  } finally {
    if (old === undefined) delete process.env.EVENER_AGENTS_DOC_MUTATION;
    else process.env.EVENER_AGENTS_DOC_MUTATION = old;
    if (oldURL === undefined) delete process.env.EVENER_RPC_URL;
    else process.env.EVENER_RPC_URL = oldURL;
  }
});
