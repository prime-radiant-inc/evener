import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { access, readFile, stat, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { promisify } from "node:util";

const exec = promisify(execFile);

async function runExample(consumerDir, environment) {
  return exec(process.execPath, [join(consumerDir, "node_modules/@evener/appwire-client/examples/discovery.mjs")], {
    cwd: consumerDir,
    env: { ...process.env, ...environment },
    encoding: "utf8",
    timeout: 15000,
  });
}

export async function runInstalledDiscoveryContracts({ consumerDir, rpcURL, fixtureCwd, observedMethods, setMode }) {
  const cases = [
    ["paths", { prefix: fixtureCwd, limit: 2, includeFiles: true }, { data: [fixtureCwd] }],
    ["projects", { limit: 2 }, { data: [fixtureCwd] }],
    ["validatePath", { path: fixtureCwd, kind: "project" }, { path: fixtureCwd, valid: true }],
    ["gitHead", { cwd: fixtureCwd }, { head: "0123456789abcdef" }],
    [
      "search",
      { query: "needle" },
      {
        live: [],
        past: [
          {
            id: "session-1",
            title: "fixture",
            project: fixtureCwd,
            state: "ended",
            age: "now",
            ref: "local:session-1",
          },
        ],
      },
    ],
    ["harnesses", {}, { data: [{ id: "fixture", label: "Fixture", kind: "test" }] }],
    ["settings", {}, { hub: { version: "0.1.0", buildChannel: "dev" }, storage: {}, agents: [] }],
  ];
  for (const [action, params, response] of cases) {
    setMode({ action, response });
    observedMethods.length = 0;
    const output = join(consumerDir, `discovery-${action}.json`);
    const paramsFile = join(consumerDir, `discovery-${action}-params.json`);
    await writeFile(paramsFile, JSON.stringify(params));
    const { stdout } = await runExample(consumerDir, {
      EVENER_RPC_URL: rpcURL,
      EVENER_CWD: fixtureCwd,
      EVENER_DISCOVERY_ACTION: action,
      EVENER_DISCOVERY_PARAMS_FILE: paramsFile,
      EVENER_DISCOVERY_OUTPUT_FILE: output,
    });
    assert.match(stdout, new RegExp(`"action":"${action}"`));
    assert.deepEqual(JSON.parse(await readFile(output, "utf8")), response);
    assert.equal((await stat(output)).mode & 0o777, 0o600);
    assert.equal(observedMethods.filter((method) => method.startsWith("evener/")).length, 1);
  }

  setMode({ action: "paths", response: { data: [] }, malformed: true });
  const malformedOutput = join(consumerDir, "discovery-malformed.json");
  await assert.rejects(
    runExample(consumerDir, {
      EVENER_RPC_URL: rpcURL,
      EVENER_CWD: fixtureCwd,
      EVENER_DISCOVERY_ACTION: "paths",
      EVENER_DISCOVERY_OUTPUT_FILE: malformedOutput,
    }),
  );
  await assert.rejects(access(malformedOutput));

  setMode({ action: "settings", response: { hub: { buildChannel: 4 } } });
  const malformedSettingsOutput = join(consumerDir, "discovery-malformed-settings.json");
  await assert.rejects(
    runExample(consumerDir, {
      EVENER_RPC_URL: rpcURL,
      EVENER_CWD: fixtureCwd,
      EVENER_DISCOVERY_ACTION: "settings",
      EVENER_DISCOVERY_OUTPUT_FILE: malformedSettingsOutput,
    }),
  );
  await assert.rejects(access(malformedSettingsOutput));

  setMode({ action: "settings", response: {}, close: true });
  const failedOutput = join(consumerDir, "discovery-transport-failure.json");
  await assert.rejects(
    runExample(consumerDir, {
      EVENER_RPC_URL: rpcURL,
      EVENER_CWD: fixtureCwd,
      EVENER_DISCOVERY_ACTION: "settings",
      EVENER_DISCOVERY_OUTPUT_FILE: failedOutput,
    }),
  );
  await assert.rejects(access(failedOutput));

  setMode({ action: "settings", response: { hub: {} } });
  const existingOutput = join(consumerDir, "discovery-existing.json");
  await writeFile(existingOutput, "keep");
  await assert.rejects(
    runExample(consumerDir, {
      EVENER_RPC_URL: rpcURL,
      EVENER_CWD: fixtureCwd,
      EVENER_DISCOVERY_ACTION: "settings",
      EVENER_DISCOVERY_OUTPUT_FILE: existingOutput,
    }),
  );
  assert.equal(await readFile(existingOutput, "utf8"), "keep");

  setMode({ action: "settings", response: { hub: {} } });
  await assert.rejects(
    runExample(consumerDir, {
      EVENER_RPC_URL: rpcURL,
      EVENER_CWD: fixtureCwd,
      EVENER_DISCOVERY_ACTION: "settings",
      EVENER_DISCOVERY_OUTPUT_FILE: "relative.json",
    }),
  );
}
