import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { access, readFile, stat, unlink, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { promisify } from "node:util";

const exec = promisify(execFile);

export async function runInstalledDiscoveryContracts({
  consumerDir,
  rpcURL,
  fixtureCwd,
  observedMethods,
  setMode,
  signal,
}) {
  const environment = { ...process.env, EVENER_RPC_URL: rpcURL, EVENER_CWD: fixtureCwd };
  for (const key of ["EVENER_DISCOVERY_ACTION", "EVENER_DISCOVERY_PARAMS_FILE", "EVENER_DISCOVERY_OUTPUT_FILE"])
    delete environment[key];
  const run = (overrides) =>
    exec(process.execPath, [join(consumerDir, "node_modules/@evener/appwire-client/examples/discovery.mjs")], {
      cwd: consumerDir,
      env: { ...environment, ...overrides },
      encoding: "utf8",
      timeout: 15000,
      signal,
    });
  const fails = (overrides) =>
    assert.rejects(run(overrides), (error) => {
      assert.equal(error.code, 1, "example must reject normally, not time out or be killed");
      assert.equal(error.signal, null);
      assert.equal(error.stdout, "", "failed read must not publish partial or private output");
      assert.equal(error.stderr.trim(), "Discovery could not be read.");
      return true;
    });
  const cases = [
    {
      action: "paths",
      method: "evener/paths/complete",
      params: { prefix: fixtureCwd, limit: 2, includeFiles: true },
      response: { data: [fixtureCwd], future: { opaque: true } },
      count: 1,
    },
    {
      action: "projects",
      method: "evener/projects/recent",
      params: { limit: 2 },
      response: { data: [fixtureCwd] },
      count: 1,
    },
    {
      action: "validatePath",
      method: "evener/path/validate",
      params: { path: fixtureCwd, kind: "dir" },
      response: { path: fixtureCwd, valid: true },
    },
    { action: "gitHead", method: "evener/git/head", params: { cwd: fixtureCwd }, response: { head: "fixture-branch" } },
    {
      action: "search",
      method: "evener/search",
      params: { query: "private-query" },
      response: {
        live: [],
        past: [
          {
            id: "session-1",
            title: "private-title",
            project: fixtureCwd,
            state: "ended",
            age: "now",
            ref: "local:session-1",
          },
        ],
      },
      count: 1,
    },
    {
      action: "harnesses",
      method: "evener/harnesses/list",
      params: {},
      response: { data: [{ id: "fixture", label: "Fixture", kind: "test" }] },
      count: 1,
    },
    {
      action: "settings",
      method: "evener/settings/overview",
      params: {},
      response: { hub: { version: "0.1.0", buildChannel: "dev" }, storage: {}, agents: [] },
      count: 3,
    },
  ];
  for (const { action, method, params, response, count } of cases) {
    setMode({ method, params, response });
    observedMethods.length = 0;
    const output = join(consumerDir, `discovery-${action}.json`);
    const paramsFile = join(consumerDir, `discovery-${action}-params.json`);
    await writeFile(paramsFile, JSON.stringify(params));
    const { stdout, stderr } = await run({
      EVENER_DISCOVERY_ACTION: action,
      EVENER_DISCOVERY_PARAMS_FILE: paramsFile,
      EVENER_DISCOVERY_OUTPUT_FILE: output,
    });
    assert.deepEqual(JSON.parse(stdout), { outcome: "read", action, ...(count === undefined ? {} : { count }) });
    assert.equal(stderr, "");
    assert.deepEqual(JSON.parse(await readFile(output, "utf8")), response);
    assert.equal((await stat(output)).mode & 0o777, 0o600);
    assert.deepEqual(observedMethods, ["initialize", method]);
  }

  const invalidParams = join(consumerDir, "discovery-invalid-params.json");
  await writeFile(invalidParams, JSON.stringify({ cwd: fixtureCwd, unknown: true }));
  observedMethods.length = 0;
  await fails({ EVENER_DISCOVERY_ACTION: "gitHead", EVENER_DISCOVERY_PARAMS_FILE: invalidParams });
  assert.deepEqual(observedMethods, [], "invalid input must fail before connecting");

  for (const { name, action, method, response, close, error } of [
    { name: "malformed-paths", action: "paths", method: "evener/paths/complete", response: { invalid: true } },
    {
      name: "malformed-settings",
      action: "settings",
      method: "evener/settings/overview",
      response: { hub: { buildChannel: 4 } },
    },
    { name: "transport-close", action: "settings", method: "evener/settings/overview", close: true },
    {
      name: "wire-error",
      action: "settings",
      method: "evener/settings/overview",
      error: { code: -32603, message: "private-server-error" },
    },
  ]) {
    setMode({ method, params: {}, response, close, error });
    observedMethods.length = 0;
    const output = join(consumerDir, `discovery-${name}.json`);
    await fails({ EVENER_DISCOVERY_ACTION: action, EVENER_DISCOVERY_OUTPUT_FILE: output });
    assert.deepEqual(observedMethods, ["initialize", method]);
    await assert.rejects(access(output), { code: "ENOENT" });
  }

  const existing = join(consumerDir, "discovery-existing.json");
  await writeFile(existing, "keep");
  observedMethods.length = 0;
  await fails({ EVENER_DISCOVERY_ACTION: "settings", EVENER_DISCOVERY_OUTPUT_FILE: existing });
  assert.equal(await readFile(existing, "utf8"), "keep");
  assert.deepEqual(observedMethods, [], "failed output reservation must not connect");
  await fails({ EVENER_DISCOVERY_ACTION: "settings", EVENER_DISCOVERY_OUTPUT_FILE: "relative.json" });
  assert.deepEqual(observedMethods, []);

  // The wire request proves the file was reserved. Replace it before sending
  // failure so cleanup has to distinguish the replacement from its own inode.
  const replacement = join(consumerDir, "discovery-replacement.json");
  setMode({
    method: "evener/settings/overview",
    params: {},
    error: { code: -32603, message: "fixture failure" },
    beforeResponse: async () => {
      await unlink(replacement);
      await writeFile(replacement, "replacement");
    },
  });
  await fails({ EVENER_DISCOVERY_ACTION: "settings", EVENER_DISCOVERY_OUTPUT_FILE: replacement });
  assert.equal(await readFile(replacement, "utf8"), "replacement");
}
