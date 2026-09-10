import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { runMaintenanceChecksCLI } from "./maintenance-checks-cli.mjs";
import { runMaintenanceCheck, safeMaintenanceSummary } from "./maintenance-checks-logic.mjs";
import { withPrivateOutput } from "./private-output.mjs";

function fixture(responses = {}) {
  const calls = [];
  return {
    calls,
    hub: {
      async connect() {
        calls.push({ method: "connect" });
      },
      async request(method, params) {
        calls.push({ method, params });
        const response = responses[method];
        if (response instanceof Error) throw response;
        return response ?? {};
      },
    },
  };
}

test("auth test correlates the hub's trimmed instance name", async () => {
  const f = fixture({
    "evener/auth/test": { provider: "owned", status: "missing", message: "No credentials configured." },
  });
  const result = await runMaintenanceCheck(f.hub, {
    action: "auth/test",
    params: { provider: " owned " },
    ownedHub: "ws://fixture/rpc",
    rpcUrl: "ws://fixture/rpc",
    mutationOptIn: "1",
  });
  assert.equal(result.outcome, "read");
  assert.equal(f.calls[1].params.provider, "owned");
});

test("ping is read-only and validates the empty response", async () => {
  const f = fixture({ ping: {} });
  const result = await runMaintenanceCheck(f.hub, { action: "ping" });
  assert.deepEqual(f.calls, [{ method: "connect" }, { method: "ping", params: {} }]);
  assert.equal(result.outcome, "read");
  assert.deepEqual(result.readback, {});
  assert.deepEqual(safeMaintenanceSummary(result), { action: "ping", outcome: "read", execution: "unverified" });
});

test("auth test captures provider before connect and preserves structured status", async () => {
  const params = { provider: "anthropic" };
  process.env.EVENER_RPC_URL = "ws://127.0.0.1:1/rpc";
  process.env.EVENER_MAINTENANCE_MUTATION = "1";
  const f = fixture({
    "evener/auth/test": { provider: "anthropic", status: "success", message: "Credentials verified." },
  });
  const resultPromise = runMaintenanceCheck(f.hub, {
    action: "auth/test",
    params,
    ownedHub: "ws://127.0.0.1:1/rpc",
  });
  params.provider = "changed";
  const result = await resultPromise;
  assert.deepEqual(f.calls[1], { method: "evener/auth/test", params: { provider: "anthropic" } });
  assert.equal(result.outcome, "read");
  assert.equal(result.readback.status, "success");
  delete process.env.EVENER_MAINTENANCE_MUTATION;
  delete process.env.EVENER_RPC_URL;
});

test("auth test requires opt-in and matching ownership before connecting", async () => {
  for (const change of [
    { optIn: undefined, ownedHub: "ws://127.0.0.1:1/rpc" },
    { optIn: "1", ownedHub: "ws://127.0.0.1:2/rpc" },
  ]) {
    const f = fixture();
    process.env.EVENER_RPC_URL = "ws://127.0.0.1:1/rpc";
    if (change.optIn === undefined) delete process.env.EVENER_MAINTENANCE_MUTATION;
    else process.env.EVENER_MAINTENANCE_MUTATION = change.optIn;
    try {
      await assert.rejects(
        runMaintenanceCheck(f.hub, {
          action: "auth/test",
          params: { provider: "anthropic" },
          ownedHub: change.ownedHub,
        }),
      );
      assert.deepEqual(f.calls, []);
    } finally {
      delete process.env.EVENER_MAINTENANCE_MUTATION;
      delete process.env.EVENER_RPC_URL;
    }
  }
});

test("plugin check requires opt-in and ownership, then accepts updated and errors omissions", async () => {
  const f = fixture({ "evener/plugin/checkNow": { updated: ["demo@local"] } });
  await assert.rejects(
    runMaintenanceCheck(f.hub, { action: "plugin/checkNow", ownedHub: "ws://127.0.0.1:1/rpc" }),
    /opt-in/,
  );
  process.env.EVENER_RPC_URL = "ws://127.0.0.1:1/rpc";
  process.env.EVENER_MAINTENANCE_MUTATION = "1";
  try {
    const result = await runMaintenanceCheck(f.hub, { action: "plugin/checkNow", ownedHub: "ws://127.0.0.1:1/rpc" });
    assert.equal(result.outcome, "acknowledged");
    assert.deepEqual(result.readback, { updated: ["demo@local"] });
    assert.equal(safeMaintenanceSummary(result).updatedCount, 1);
    assert.equal(safeMaintenanceSummary(result).errorCount, 0);
  } finally {
    delete process.env.EVENER_MAINTENANCE_MUTATION;
    delete process.env.EVENER_RPC_URL;
  }
});

test("failed or malformed checks are uncertain and are never replayed", async () => {
  process.env.EVENER_RPC_URL = "ws://127.0.0.1:1/rpc";
  process.env.EVENER_MAINTENANCE_MUTATION = "1";
  try {
    for (const response of [new Error("private"), { updated: [7] }]) {
      const failed = fixture({ "evener/plugin/checkNow": response });
      const result = await runMaintenanceCheck(failed.hub, {
        action: "plugin/checkNow",
        ownedHub: "ws://127.0.0.1:1/rpc",
      });
      assert.equal(result.outcome, "uncertain");
      assert.equal(failed.calls.filter((call) => call.method === "evener/plugin/checkNow").length, 1);
    }
  } finally {
    delete process.env.EVENER_MAINTENANCE_MUTATION;
    delete process.env.EVENER_RPC_URL;
  }
});

test("private output is reserved before connection and summary omits response details", async () => {
  const dir = await mkdtemp(join(tmpdir(), "maintenance-checks-"));
  const output = join(dir, "result.json");
  let connected = false;
  try {
    await assert.rejects(
      withPrivateOutput(output, async () => {
        connected = true;
        throw new Error("private");
      }),
      /private/,
    );
    assert.equal(connected, true);
    await assert.rejects(readFile(output), /ENOENT/);
    assert.deepEqual(
      safeMaintenanceSummary({
        action: "plugin/checkNow",
        outcome: "uncertain",
        execution: "unverified",
        readback: { errors: ["secret provider detail"] },
      }),
      { action: "plugin/checkNow", outcome: "uncertain", execution: "unverified", updatedCount: 0, errorCount: 1 },
    );
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("CLI refuses an occupied private output before connecting", async () => {
  const dir = await mkdtemp(join(tmpdir(), "maintenance-checks-"));
  const output = join(dir, "occupied.json");
  await writeFile(output, "keep", { mode: 0o600 });
  let connects = 0;
  try {
    await assert.rejects(
      runMaintenanceChecksCLI({ EVENER_MAINTENANCE_OUTPUT_FILE: output }, { connect: async () => connects++ }),
      /EEXIST/,
    );
    assert.equal(connects, 0);
    assert.equal(await readFile(output, "utf8"), "keep");
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("CLI writes acknowledged output privately with mode 0600 and metadata-only stdout", async () => {
  const dir = await mkdtemp(join(tmpdir(), "maintenance-checks-"));
  const output = join(dir, "result.json");
  const lines = [];
  const f = fixture({ ping: {} });
  try {
    await runMaintenanceChecksCLI({ EVENER_MAINTENANCE_OUTPUT_FILE: output }, f.hub, {
      stdout: (line) => lines.push(line),
    });
    assert.deepEqual(JSON.parse(await readFile(output, "utf8")), {});
    assert.equal((await stat(output)).mode & 0o777, 0o600);
    assert.deepEqual(JSON.parse(lines[0]), {
      action: "ping",
      outcome: "read",
      execution: "unverified",
      outputWritten: true,
    });
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("CLI writes valid unavailable readback for uncertain failures and does not retry", async () => {
  const dir = await mkdtemp(join(tmpdir(), "maintenance-checks-"));
  const output = join(dir, "uncertain.json");
  const paramsFile = join(dir, "params.json");
  await writeFile(paramsFile, JSON.stringify({ provider: "anthropic" }), { mode: 0o600 });
  const lines = [];
  const f = fixture({ "evener/auth/test": new Error("private") });
  const environment = {
    EVENER_MAINTENANCE_ACTION: "auth/test",
    EVENER_MAINTENANCE_PARAMS_FILE: paramsFile,
    EVENER_MAINTENANCE_OUTPUT_FILE: output,
    EVENER_MAINTENANCE_MUTATION: "1",
    EVENER_RPC_URL: "ws://127.0.0.1:1/rpc",
    EVENER_MAINTENANCE_OWNED_HUB: "ws://127.0.0.1:1/rpc",
  };
  try {
    const result = await runMaintenanceChecksCLI(environment, f.hub, { stdout: (line) => lines.push(line) });
    assert.equal(result.outcome, "uncertain");
    assert.deepEqual(JSON.parse(await readFile(output, "utf8")), {
      outcome: "uncertain",
      execution: "unverified",
      readback: "unavailable",
    });
    assert.deepEqual(JSON.parse(lines[0]), {
      action: "auth/test",
      outcome: "uncertain",
      execution: "unverified",
      outputWritten: true,
    });
    assert.equal(f.calls.filter((call) => call.method === "evener/auth/test").length, 1);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("CLI rejects a directory output path before connecting", async () => {
  const dir = await mkdtemp(join(tmpdir(), "maintenance-checks-"));
  const f = fixture({ ping: {} });
  try {
    await assert.rejects(runMaintenanceChecksCLI({ EVENER_MAINTENANCE_OUTPUT_FILE: dir }, f.hub), /EEXIST/);
    assert.equal(f.calls.length, 0);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
