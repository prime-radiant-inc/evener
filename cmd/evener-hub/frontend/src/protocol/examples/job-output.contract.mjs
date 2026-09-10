import assert from "node:assert/strict";
import test from "node:test";
import { runJobOutput, summarizeJobOutput } from "./job-output-logic.mjs";

const page = { tail: "日本語", totalBytes: 100, retainedStart: 91, truncated: true, hasEarlier: true };
const input = { ref: "local:owner", jobId: "job_one" };
function fixture(response = { data: page }, onConnect = () => {}) {
  const calls = [];
  return {
    calls,
    hub: {
      async connect() {
        calls.push({ method: "connect" });
        await onConnect();
      },
      async request(method, params) {
        calls.push({ method, params });
        return response;
      },
    },
  };
}
test("job output captures the owning session and server byte cursor before connecting", async () => {
  const params = { ...input, maxBytes: 65536, beforeBytes: 0 };
  const f = fixture(undefined, () => {
    params.ref = "local:other";
    params.beforeBytes = 12;
  });
  assert.deepEqual(await runJobOutput(f.hub, params), { outcome: "read", readback: page });
  assert.deepEqual(f.calls, [
    { method: "connect" },
    { method: "evener/jobs/output", params: { ...input, maxBytes: 65536, beforeBytes: 0 } },
  ]);
});
test("job output omits default options and normalizes the absent false flag", async () => {
  const { hasEarlier: _, ...withoutEarlier } = page;
  const f = fixture({ data: withoutEarlier });
  const result = await runJobOutput(f.hub, input);
  assert.deepEqual(f.calls[1], { method: "evener/jobs/output", params: input });
  assert.deepEqual(result.readback, { ...page, hasEarlier: false });
});
test("job output rejects invalid parameters before connecting", async () => {
  const f = fixture();
  for (const params of [
    null,
    [],
    {},
    { ...input, ref: " " },
    { ...input, jobId: "" },
    { ...input, action: "stop" },
    ...[0, -1, 65537, 1.5, NaN, Infinity].map((maxBytes) => ({ ...input, maxBytes })),
    ...[-1, 1.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1].map((beforeBytes) => ({ ...input, beforeBytes })),
  ])
    await assert.rejects(runJobOutput(f.hub, params));
  assert.deepEqual(f.calls, []);
});
test("job output rejects invalid envelopes and unsafe byte bookkeeping", async () => {
  for (const response of [
    undefined,
    null,
    [],
    {},
    { data: null },
    { data: [] },
    ...[
      { tail: 1 },
      { totalBytes: -1 },
      { totalBytes: Number.MAX_SAFE_INTEGER + 1 },
      { retainedStart: 101 },
      { retainedStart: NaN },
      { truncated: undefined },
      { truncated: 0 },
      { hasEarlier: "true" },
    ].map((change) => ({ data: { ...page, ...change } })),
  ]) {
    const f = fixture();
    f.hub.request = async () => response;
    await assert.rejects(runJobOutput(f.hub, input));
  }
});
test("job output retains independent copies of authored content and future fields", async () => {
  const data = { ...page, future: { values: ["private"] } };
  const result = await runJobOutput(fixture({ data }).hub, input);
  data.future.values.push("changed");
  assert.deepEqual(result.readback, { ...page, future: { values: ["private"] } });
});
test("job output propagates request failures without retrying", async () => {
  let calls = 0;
  const failure = new Error("private request failure");
  const hub = {
    connect: async () => {},
    request: async () => {
      calls++;
      throw failure;
    },
  };
  await assert.rejects(runJobOutput(hub, input), (error) => error === failure);
  assert.equal(calls, 1);
});
test("job output summary excludes log content and future private fields", async () => {
  const result = await runJobOutput(fixture({ data: { ...page, privateRef: "private" } }).hub, input);
  assert.deepEqual(summarizeJobOutput(result), {
    outcome: "read",
    totalBytes: 100,
    retainedStart: 91,
    truncated: true,
    hasEarlier: true,
  });
});
