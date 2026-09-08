import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { withMutationEnv } from "./management-contract-fixtures.mjs";
import { runSessionLineageCLI } from "./session-lineage-cli.mjs";
import { runSessionLineage } from "./session-lineage-logic.mjs";

const ref = "local:parent";
const thread = (extra = {}) => ({
  thread: {
    id: "thread-1",
    name: "Parent",
    evener: { ref, instanceId: "instance-1", capabilities: { forkFromTurn: true }, ...extra },
  },
});
const scripted = (responses) => {
  const calls = [];
  return {
    calls,
    hub: {
      connect: async () => calls.push({ method: "connect" }),
      request: async (method, params) => {
        calls.push({ method, params });
        const value = responses.shift();
        if (value instanceof Error) throw value;
        return value;
      },
    },
  };
};
test("lineage reads validate source-backed responses", async () => {
  const f = scripted([{ data: [{ ref, title: "Parent", kind: "session" }] }, { ref, items: [], truncated: false }]);
  assert.equal((await runSessionLineage(f.hub, { action: "transcripts", params: { ref } })).outcome, "read");
  assert.equal((await runSessionLineage(f.hub, { action: "preview", params: { ref, limit: 2 } })).outcome, "read");
  assert.deepEqual(
    f.calls.map((x) => x.method),
    ["connect", "evener/thread/transcripts/list", "connect", "evener/subagentPreview"],
  );
});
test("fork is reviewed, owned, and uncertain on a lost reply", async () => {
  const reviewed = { threadId: "thread-1", instanceId: "instance-1", name: "Parent" };
  const f = scripted([thread(), new Error("reply lost"), thread()]);
  const result = await withMutationEnv("EVENER_SESSION_LINEAGE_MUTATION", () =>
    runSessionLineage(f.hub, {
      action: "fork",
      params: { ref, expectedInstanceId: "instance-1", reviewed, sourceTurnId: "3", deferInput: true },
      ownedHub: process.env.EVENER_RPC_URL,
    }),
  );
  assert.equal(result.outcome, "uncertain");
  assert.equal(f.calls.filter((x) => x.method === "thread/fork").length, 1);
});
test("stale review and missing fork capability stop before dispatch", async () => {
  await withMutationEnv("EVENER_SESSION_LINEAGE_MUTATION", async () => {
    const renamed = thread();
    renamed.thread.name = "Changed";
    for (const current of [renamed, thread({ capabilities: { forkFromTurn: false } })]) {
      const f = scripted([current]);
      await assert.rejects(
        runSessionLineage(f.hub, {
          action: "fork",
          params: {
            ref,
            expectedInstanceId: "instance-1",
            reviewed: { threadId: "thread-1", instanceId: "instance-1", name: "Parent" },
            sourceTurnId: "3",
            deferInput: true,
          },
          ownedHub: process.env.EVENER_RPC_URL,
        }),
      );
      assert.equal(f.calls.filter((x) => x.method === "thread/fork").length, 0);
    }
  });
});

const review = { threadId: "thread-1", instanceId: "instance-1", name: "Parent" };
const forkParams = { ref, expectedInstanceId: "instance-1", reviewed: review, sourceTurnId: "3", deferInput: true };
const authored = (f, action, params) =>
  withMutationEnv("EVENER_SESSION_LINEAGE_MUTATION", () =>
    runSessionLineage(f.hub, { action, params, ownedHub: process.env.EVENER_RPC_URL }),
  );
const child = () => ({
  thread: { id: "child", evener: { ref: "local:child" } },
  originalInput: "private original input",
});

test("acknowledged fork reads the returned child and preserves original input for editing", async () => {
  const f = scripted([thread(), child(), child()]);
  const result = await authored(f, "fork", forkParams);
  assert.equal(result.outcome, "acknowledged");
  assert.equal(result.response.originalInput, "private original input");
  assert.equal(result.readback.thread.evener.ref, "local:child");
  assert.equal(f.calls.at(-1).params.ref, "local:child");
});

test("lost fork reply reads only the known parent and never invents a child", async () => {
  const f = scripted([thread(), new Error("lost"), thread()]);
  const result = await authored(f, "fork", forkParams);
  assert.equal(result.outcome, "uncertain");
  assert.equal(result.readbackTarget, "parent");
  assert.equal(result.response, undefined);
  assert.equal(f.calls.at(-1).params.ref, ref);
});

test("acknowledged fork with failed child readback remains acknowledged", async () => {
  const f = scripted([thread(), child(), new Error("unavailable")]);
  const result = await authored(f, "fork", forkParams);
  assert.equal(result.outcome, "acknowledged");
  assert.equal(result.readback, undefined);
  assert.equal(result.response.thread.evener.ref, "local:child");
  assert.equal(f.calls.filter((x) => x.method === "thread/fork").length, 1);
});

test("lineage CLI reserves private output before connecting and preserves fork recovery", async () => {
  const dir = await mkdtemp(join(tmpdir(), "sdk-lineage-"));
  const file = join(dir, "params.json");
  const output = join(dir, "result.json");
  const env = {
    EVENER_SESSION_LINEAGE_ACTION: "fork",
    EVENER_SESSION_LINEAGE_PARAMS_FILE: file,
    EVENER_SESSION_LINEAGE_OUTPUT_FILE: output,
    EVENER_SESSION_LINEAGE_OWNED_HUB: "ws://127.0.0.1:1/rpc",
  };
  const summaries = [];
  try {
    await writeFile(file, JSON.stringify(forkParams));
    await writeFile(output, "existing file");
    const blocked = scripted([]);
    await assert.rejects(runSessionLineageCLI(env, blocked.hub, { stdout: (x) => summaries.push(x) }), {
      code: "EEXIST",
    });
    assert.deepEqual(blocked.calls, []);
    assert.equal(await readFile(output, "utf8"), "existing file");
    await rm(output);
    const f = scripted([thread(), child(), new Error("private read error")]);
    const result = await withMutationEnv("EVENER_SESSION_LINEAGE_MUTATION", () =>
      runSessionLineageCLI(env, f.hub, { stdout: (x) => summaries.push(x) }),
    );
    assert.equal(result.outcome, "acknowledged");
    const saved = JSON.parse(await readFile(output, "utf8"));
    assert.equal(saved.response.thread.evener.ref, "local:child");
    assert.equal(saved.response.originalInput, "private original input");
    assert.equal(saved.readbackAvailable, false);
    assert.equal((await stat(output)).mode & 0o777, 0o600);
    assert.equal(summaries.length, 1);
    assert.equal(summaries[0].includes("private"), false);
    assert.equal(summaries[0].includes("local:child"), false);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("malformed fork receipt stays uncertain and uses parent reconciliation", async () => {
  for (const reply of [thread(), { thread: { id: "child", evener: {} } }, { ...child(), originalInput: 42 }]) {
    const f = scripted([thread(), reply, thread()]);
    const result = await authored(f, "fork", forkParams);
    assert.equal(result.outcome, "uncertain");
    assert.equal(result.readbackTarget, "parent");
    assert.equal(f.calls.at(-1).params.ref, ref);
  }
});

test("source-owned whole-thread fork omits turn fields without claiming turn capability", async () => {
  const parent = thread({ ref: "remote:parent", capabilities: undefined });
  const f = scripted([parent, child(), child()]);
  const result = await authored(f, "fork", { ref: "remote:parent", reviewed: review });
  assert.equal(result.outcome, "acknowledged");
  assert.deepEqual(f.calls[2].params, { ref: "remote:parent" });
});

test("review and resume accept an ended source with omitted instance and capabilities", async () => {
  const ended = thread({ instanceId: undefined, capabilities: undefined });
  const read = scripted([ended]);
  const reviewed = await runSessionLineage(read.hub, { action: "review", params: { ref } });
  assert.deepEqual(reviewed.review, { threadId: "thread-1", name: "Parent" });
  const f = scripted([ended, thread({ instanceId: "new" }), thread({ instanceId: "new" })]);
  const result = await authored(f, "resume", { ref, reviewed: reviewed.review });
  assert.equal(result.outcome, "acknowledged");
  assert.deepEqual(f.calls[2], { method: "thread/resume", params: { ref } });
});

test("captured review and authored input survive caller edits during connect", async () => {
  const f = scripted([thread(), child(), child()]);
  const params = structuredClone(forkParams);
  await withMutationEnv("EVENER_SESSION_LINEAGE_MUTATION", async () => {
    const promise = runSessionLineage(f.hub, { action: "fork", params, ownedHub: process.env.EVENER_RPC_URL });
    params.ref = "other:thread";
    params.reviewed.name = "changed";
    params.sourceTurnId = "9";
    await promise;
  });
  assert.equal(f.calls[2].params.ref, ref);
  assert.equal(f.calls[2].params.sourceTurnId, "3");
});

test("invalid inputs fail before connecting", async () => {
  for (const [action, params] of [
    ["unknown", forkParams],
    ["preview", {}],
    ["preview", { ref, limit: 1.5 }],
    ["fork", { ...forkParams, deferInput: "yes" }],
    ["fork", { ...forkParams, aside: "yes" }],
    ["fork", { ...forkParams, sourceTurnId: "turn_live_9" }],
    ["fork", { ...forkParams, deferInput: false }],
    ["resume", { ref, reviewed: review, expectedInstanceId: "other" }],
  ]) {
    const f = scripted([]);
    await assert.rejects(authored(f, action, params));
    assert.equal(f.calls.length, 0, action);
  }
});

test("aside omits divergence fields and does not fabricate turn-fork permission", async () => {
  const f = scripted([thread({ capabilities: undefined }), child(), child()]);
  const result = await authored(f, "fork", { ref, reviewed: review, aside: true });
  assert.equal(result.outcome, "acknowledged");
  assert.deepEqual(f.calls[2].params, { ref, aside: true });
});

test("preview preserves empty and populated current server projections", async () => {
  for (const items of [[], [{ id: "", type: "agentMessage", text: "private", future: true }]]) {
    const f = scripted([{ ref, items, truncated: false }]);
    const result = await runSessionLineage(f.hub, { action: "preview", params: { ref } });
    assert.deepEqual(result.readback.items, items);
    assert.deepEqual(f.calls[1].params, { ref });
  }
  for (const items of [null, [42]]) {
    const f = scripted([{ ref, items, truncated: false }]);
    await assert.rejects(runSessionLineage(f.hub, { action: "preview", params: { ref } }));
  }
});
