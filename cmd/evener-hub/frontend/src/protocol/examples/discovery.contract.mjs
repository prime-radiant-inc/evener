import assert from "node:assert/strict";
import { access, mkdtemp, readFile, rm, stat, unlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { runDiscoveryCLI } from "./discovery-cli.mjs";
import { runDiscovery, summarizeDiscovery } from "./discovery-logic.mjs";

function fixture(response) {
  const calls = [];
  return {
    calls,
    hub: {
      connect: async () => calls.push("connect"),
      request: async (method, params) => {
        calls.push({ method, params });
        return response;
      },
    },
  };
}
test("discovery dispatches one selected read with captured params and preserves future fields", async () => {
  const params = { prefix: "/tmp", limit: 2, includeFiles: true };
  const source = { data: ["/tmp/a"], future: { value: 1 } };
  const f = fixture(source);
  const pending = runDiscovery(f.hub, { action: "paths", params });
  params.prefix = "/changed";
  const result = await pending;
  assert.deepEqual(f.calls, [
    "connect",
    { method: "evener/paths/complete", params: { prefix: "/tmp", limit: 2, includeFiles: true } },
  ]);
  assert.deepEqual(result.readback, source);
  assert.deepEqual(summarizeDiscovery(result), { outcome: "read", action: "paths", count: 1 });
});
test("discovery validates real response contracts and does not call on bad input", async () => {
  const f = fixture({ data: [] });
  await assert.rejects(runDiscovery(f.hub, { action: "paths", params: { prefix: "", extra: true } }));
  assert.deepEqual(f.calls, []);
  for (const [action, response] of [
    ["gitHead", { head: 4 }],
    ["search", { live: [], past: [{}] }],
    ["harnesses", { data: [{ id: "x" }] }],
  ]) {
    const bad = fixture(response);
    await assert.rejects(runDiscovery(bad.hub, { action, params: action === "gitHead" ? { cwd: "/tmp" } : {} }));
  }
});
test("discovery propagates transport errors", async () => {
  const error = new Error("transport");
  const f = fixture(error);
  f.hub.request = async () => {
    throw error;
  };
  await assert.rejects(runDiscovery(f.hub, { action: "settings", params: {} }), (actual) => actual === error);
});
test("paths accepts omitted and empty prefixes, and path validation reports empty input", async () => {
  await runDiscovery(fixture({ data: [] }).hub, { action: "paths" });
  await runDiscovery(fixture({ data: [] }).hub, { action: "paths", params: { prefix: "" } });
  const result = await runDiscovery(fixture({ path: "", valid: false, error: "path is required" }).hub, {
    action: "validatePath",
    params: { path: "" },
  });
  assert.equal(result.readback.valid, false);
});
test("harness optional descriptor strings are type checked", async () => {
  for (const key of ["kind", "emptyTaskUnsupportedReason", "emptyTaskUnsupportedNextAction"])
    await assert.rejects(
      runDiscovery(fixture({ data: [{ id: "x", label: "X", [key]: 1 }] }).hub, { action: "harnesses" }),
    );
});
test("CLI keeps stdout metadata-only and writes private full output exclusively", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "evener-discovery-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const output = join(directory, "readback.json");
  const params = join(directory, "params.json");
  await writeFile(params, JSON.stringify({ query: "needle" }));
  const f = fixture({
    live: [],
    past: [{ id: "id", title: "private", project: "/secret", state: "ended", age: "now", ref: "local:id" }],
  });
  const lines = [];
  const originalLog = console.log;
  console.log = (line) => lines.push(line);
  try {
    await runDiscoveryCLI(
      { EVENER_DISCOVERY_ACTION: "search", EVENER_DISCOVERY_PARAMS_FILE: params, EVENER_DISCOVERY_OUTPUT_FILE: output },
      f.hub,
    );
  } finally {
    console.log = originalLog;
  }
  assert.deepEqual(JSON.parse(lines[0]), { outcome: "read", action: "search", count: 1 });
  assert.match(await readFile(output, "utf8"), /private/);
  assert.equal((await stat(output)).mode & 0o777, 0o600);
  await assert.rejects(
    runDiscoveryCLI({ EVENER_DISCOVERY_ACTION: "search", EVENER_DISCOVERY_OUTPUT_FILE: output }, f.hub),
  );
  await assert.rejects(
    runDiscoveryCLI({ EVENER_DISCOVERY_ACTION: "search", EVENER_DISCOVERY_OUTPUT_FILE: "relative.json" }, f.hub),
  );
});
test("CLI removes only its incomplete private output after a failed read", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "evener-discovery-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const output = join(directory, "failed.json");
  const f = fixture(new Error("read failed"));
  f.hub.request = async () => {
    throw new Error("read failed");
  };
  await assert.rejects(
    runDiscoveryCLI({ EVENER_DISCOVERY_ACTION: "settings", EVENER_DISCOVERY_OUTPUT_FILE: output }, f.hub),
  );
  await assert.rejects(access(output));
});
test("CLI does not remove a replacement at the reserved output path", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "evener-discovery-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const output = join(directory, "replaced.json");
  const f = fixture({});
  f.hub.request = async () => {
    await unlink(output);
    await writeFile(output, "replacement");
    throw new Error("read failed");
  };
  await assert.rejects(
    runDiscoveryCLI({ EVENER_DISCOVERY_ACTION: "settings", EVENER_DISCOVERY_OUTPUT_FILE: output }, f.hub),
  );
  assert.equal(await readFile(output, "utf8"), "replacement");
});

test("settings validates known optional fields while preserving omissions and future data", async () => {
  const valid = {
    hub: { version: "0.1.0", pastIndex: {} },
    storage: {},
    agents: [{ name: "default", editPath: "", future: 1 }],
    mcpDiscovered: {},
    future: { opaque: true },
  };
  const result = await runDiscovery(fixture(valid).hub, { action: "settings" });
  assert.deepEqual(result.readback, valid);
  for (const response of [
    { agents: [{ name: "default", editPath: 4 }] },
    { hub: { commit: 4 } },
    { hub: { pastIndex: { count: "1" } } },
    { mcpDiscovered: { servers: [{ name: "x", transport: 4 }] } },
  ]) {
    await assert.rejects(runDiscovery(fixture(response).hub, { action: "settings" }));
  }
});

