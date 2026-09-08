import assert from "node:assert/strict";
import { access, mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { ownedHub, scriptedHub, withMutationEnv } from "./management-contract-fixtures.mjs";
import { runSessionManagementCLI } from "./session-management-cli.mjs";
import { runSessionManagement } from "./session-management-logic.mjs";

const ref = "local:session";
const snapshot = (overrides = {}) => ({
  thread: {
    id: "thread-1",
    name: "Current",
    evener: {
      ref,
      instanceId: "instance-1",
      capabilities: { rename: true, compact: true, clear: true, shutdown: true },
      ...overrides,
    },
  },
});
const reviewed = { threadId: "thread-1", instanceId: "instance-1", name: "Current" };
const params = { ref, expectedInstanceId: "instance-1", reviewed };

test("list is bounded and returns safe review metadata", async () => {
  const script = scriptedHub([() => snapshot()]);
  const result = await runSessionManagement(script.hub, { params: { ref } });
  assert.deepEqual(result.review, reviewed);
  assert.deepEqual(
    script.calls.find((call) => call.method === "thread/read"),
    { method: "thread/read", params: { ref, includeTurns: false, subscribe: false } },
  );
});

test("rename and compact mutate once and read back", async () => {
  await withMutationEnv("EVENER_SESSION_MANAGEMENT_MUTATION", async () => {
    for (const [action, input] of [
      ["rename", { ...params, name: "Renamed" }],
      ["compact", params],
    ]) {
      const script = scriptedHub([() => snapshot(), () => ({}), () => snapshot()]);
      const result = await runSessionManagement(script.hub, { action, params: input, ownedHub });
      assert.equal(result.outcome, "acknowledged");
      assert.equal(script.calls.filter((call) => call.method === "thread/read").length, 2);
    }
  });
});

test("clear uses generated atomic fence and response snapshot", async () => {
  await withMutationEnv("EVENER_SESSION_MANAGEMENT_MUTATION", async () => {
    const replacement = snapshot({ instanceId: "instance-2" });
    const script = scriptedHub([
      () => snapshot(),
      (_method, clearParams) => ({
        ...replacement,
        ref,
        receipt: {
          clientMutationId: clearParams.clientMutationId,
          disposition: "applied",
          threadId: "thread-1",
          instanceId: "instance-2",
          projectionState: "reflected",
        },
      }),
    ]);
    const result = await runSessionManagement(script.hub, { action: "clear", params, ownedHub });
    assert.equal(result.outcome, "acknowledged");
    const clearCall = script.calls.find((call) => call.method === "thread/clear");
    assert.equal(clearCall.method, "thread/clear");
    assert.equal(clearCall.params.expectedInstanceId, "instance-1");
    assert.match(clearCall.params.clientMutationId, /^[0-9a-f-]{36}$/);
    assert.equal(script.calls.filter((call) => call.method === "thread/read").length, 1);
  });
});

test("shutdown does not read after asynchronous stop and malformed replies are uncertain", async () => {
  await withMutationEnv("EVENER_SESSION_MANAGEMENT_MUTATION", async () => {
    for (const reply of [{}, null, undefined]) {
      const script = scriptedHub([() => snapshot(), () => reply]);
      const result = await runSessionManagement(script.hub, { action: "shutdown", params, ownedHub });
      assert.equal(result.outcome, reply && Object.keys(reply).length === 0 ? "acknowledged" : "uncertain");
      assert.equal(script.calls.filter((call) => call.method === "thread/read").length, 1);
    }
  });
});

test("malformed and mismatched clear receipts stay uncertain with one dispatch", async () => {
  await withMutationEnv("EVENER_SESSION_MANAGEMENT_MUTATION", async () => {
    const receipts = [
      () => ({}),
      (valid) => ({ ...valid, clientMutationId: "wrong" }),
      (valid) => ({ ...valid, threadId: "other" }),
      (valid) => ({ ...valid, instanceId: "other" }),
      (valid) => ({ ...valid, projectionState: "pending" }),
      (valid) => ({ ...valid, disposition: "unknown" }),
    ];
    for (const receipt of receipts) {
      const script = scriptedHub([
        () => snapshot(),
        (_method, clearParams) => ({
          ...snapshot({ instanceId: "instance-2" }),
          ref,
          receipt: receipt({
            clientMutationId: clearParams.clientMutationId,
            disposition: "applied",
            threadId: "thread-1",
            instanceId: "instance-2",
            projectionState: "reflected",
          }),
        }),
      ]);
      const result = await runSessionManagement(script.hub, { action: "clear", params, ownedHub });
      assert.equal(result.outcome, "uncertain");
      assert.equal(script.calls.filter((call) => call.method === "thread/clear").length, 1);
    }
  });
});

test("authored control input is captured before connect", async () => {
  await withMutationEnv("EVENER_SESSION_MANAGEMENT_MUTATION", async () => {
    const input = { ...structuredClone(params), name: "Authored" };
    const script = scriptedHub([() => snapshot(), () => ({}), () => snapshot()]);
    const connect = script.hub.connect;
    script.hub.connect = async () => {
      input.name = "Changed after review";
      input.reviewed.name = "Changed after review";
      await connect();
    };
    await runSessionManagement(script.hub, { action: "rename", params: input, ownedHub });
    assert.deepEqual(script.calls.find((call) => call.method === "evener/thread/name/set").params, {
      ref,
      name: "Authored",
    });
  });
});

test("invalid authored state and unavailable capability stop before mutation", async () => {
  await withMutationEnv("EVENER_SESSION_MANAGEMENT_MUTATION", async () => {
    for (const [action, input, before, expected] of [
      ["rename", { ...params, name: 1 }, snapshot(), /Session name must be text/],
      ["compact", params, snapshot({ capabilities: { compact: false } }), /Session control is unavailable/],
      ["clear", { ...params, reviewed: { ...reviewed, name: "changed" } }, snapshot(), /Session state changed/],
    ]) {
      const script = scriptedHub([() => before]);
      await assert.rejects(runSessionManagement(script.hub, { action, params: input, ownedHub }), expected);
      assert.equal(
        script.calls.filter((call) =>
          ["evener/thread/name/set", "thread/compact/start", "thread/clear"].includes(call.method),
        ).length,
        0,
      );
    }
  });
});

test("clear cannot acknowledge absent or malformed replacement identity", async () => {
  await withMutationEnv("EVENER_SESSION_MANAGEMENT_MUTATION", async () => {
    for (const instanceId of [undefined, "", "   ", 12]) {
      const script = scriptedHub([
        () => snapshot(),
        (_method, clearParams) => ({
          ...snapshot({ instanceId }),
          ref,
          receipt: {
            clientMutationId: clearParams.clientMutationId,
            disposition: "applied",
            threadId: "thread-1",
            instanceId,
            projectionState: "reflected",
          },
        }),
      ]);
      const result = await runSessionManagement(script.hub, { action: "clear", params, ownedHub });
      assert.equal(result.outcome, "uncertain");
      assert.equal(script.calls.filter((call) => call.method === "thread/clear").length, 1);
      assert.equal(result.readback, undefined);
    }
  });
});

test("session review rejects malformed identities before producing a review", async () => {
  for (const instanceId of ["", "   ", 12]) {
    const script = scriptedHub([() => snapshot({ instanceId })]);
    await assert.rejects(runSessionManagement(script.hub, { params: { ref } }));
  }
});

test("session read keeps unavailable source-backed controls unavailable", async () => {
  const value = snapshot();
  delete value.thread.evener.instanceId;
  delete value.thread.evener.capabilities;
  const script = scriptedHub([() => value]);
  const result = await runSessionManagement(script.hub, { params: { ref } });
  assert.deepEqual(result.readback, value);
  assert.equal(result.review, undefined);
});

test("CLI writes a private review and rejects existing destinations before connecting", async (t) => {
  const dir = await mkdtemp(join(tmpdir(), "evener-session-review-"));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const input = join(dir, "params.json"),
    output = join(dir, "review.json");
  await writeFile(input, JSON.stringify({ ref }));
  const env = { EVENER_SESSION_MANAGEMENT_PARAMS_FILE: input, EVENER_SESSION_MANAGEMENT_REVIEW_FILE: output };
  const lines = [];
  t.mock.method(console, "log", (line) => lines.push(JSON.parse(line)));
  const script = scriptedHub([() => snapshot()]);
  await runSessionManagementCLI(env, script.hub);
  assert.deepEqual(JSON.parse(await readFile(output, "utf8")), params);
  assert.equal((await stat(output)).mode & 0o777, 0o600);
  assert.deepEqual(lines, [
    {
      action: "list",
      outcome: "read",
      execution: "unverified",
      readback: "present",
      review: "present",
      reviewWritten: true,
    },
  ]);
  const duplicate = scriptedHub([]);
  await assert.rejects(runSessionManagementCLI(env, duplicate.hub), { code: "EEXIST" });
  assert.deepEqual(duplicate.calls, []);
  assert.deepEqual(JSON.parse(await readFile(output, "utf8")), params);
});

test("CLI cannot leave an unusable review for a source without a session instance", async (t) => {
  const dir = await mkdtemp(join(tmpdir(), "evener-session-review-"));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const input = join(dir, "params.json"),
    output = join(dir, "review.json");
  await writeFile(input, JSON.stringify({ ref }));
  const value = snapshot();
  delete value.thread.evener.instanceId;
  const script = scriptedHub([() => value]);
  await assert.rejects(
    runSessionManagementCLI(
      { EVENER_SESSION_MANAGEMENT_PARAMS_FILE: input, EVENER_SESSION_MANAGEMENT_REVIEW_FILE: output },
      script.hub,
    ),
    /no controllable instance/,
  );
  await assert.rejects(access(output), { code: "ENOENT" });
});
