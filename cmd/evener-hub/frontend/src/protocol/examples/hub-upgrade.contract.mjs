import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { runHubUpgrade } from "./hub-upgrade-logic.mjs";

const overview = (version = "v1", commit = "abc") => ({ hub: { version, commit } });
const response = () => ({
  release: "v2",
  channel: "stable",
  url: "https://updates.example/v2",
  archive: "evener-v2.tar.gz",
  prefix: "/opt/evener",
  binDir: "/opt/evener/bin",
  shareBinDir: "/opt/evener/share",
  installed: ["evener-hub"],
  restartMessage: "Restart the hub.",
});
function fake(values, onConnect) {
  const calls = [];
  return {
    calls,
    hub: {
      connect: async () => {
        calls.push("connect");
        onConnect?.();
      },
      request: async (method, params) => {
        calls.push({ method, params });
        const value = values.shift();
        if (value instanceof Error) throw value;
        return value;
      },
    },
  };
}
const owned = { ownedHub: "ws://owned/rpc", mutationOptIn: "1", rpcUrl: "ws://owned/rpc" };

test("review reads running identity", async () => {
  const f = fake([overview()]);
  const result = await runHubUpgrade(f.hub, { action: "review" });
  assert.equal(result.outcome, "read");
  assert.deepEqual(result.identity, { version: "v1", commit: "abc" });
  assert.deepEqual(f.calls, ["connect", { method: "evener/settings/overview", params: {} }]);
});

test("review accepts the generated omitted commit field", async () => {
  const f = fake([{ hub: { version: "dev" } }]);
  const result = await runHubUpgrade(f.hub, { action: "review" });
  assert.deepEqual(result.identity, { version: "dev", commit: "" });
});

test("malformed present commit values fail before connecting", async () => {
  for (const commit of [null, 42, {}]) {
    const f = fake([]);
    await assert.rejects(
      runHubUpgrade(f.hub, { action: "upgrade", params: { reviewed: { version: "dev", commit } }, ...owned }),
    );
    assert.deepEqual(f.calls, []);
  }
});

test("upgrade requires ownership before connect", async () => {
  const f = fake([]);
  await assert.rejects(
    runHubUpgrade(f.hub, {
      action: "upgrade",
      params: { requested: "latest", reviewed: { version: "v1", commit: "abc" } },
    }),
  );
  assert.deepEqual(f.calls, []);
});

test("upgrade rechecks identity and sends generated wire params", async () => {
  const params = { requested: "latest", reviewed: { version: "v1", commit: "abc" } };
  const f = fake([overview(), response()], () => {
    params.requested = "other";
  });
  const result = await runHubUpgrade(f.hub, { action: "upgrade", params, ...owned });
  assert.equal(result.outcome, "acknowledged");
  assert.deepEqual(f.calls[2], { method: "evener/upgrade", params: { requested: "latest" } });
});

test("changed identity refuses dispatch", async () => {
  const f = fake([overview("v9", "changed")]);
  await assert.rejects(
    runHubUpgrade(f.hub, {
      action: "upgrade",
      params: { requested: "latest", reviewed: { version: "v1", commit: "abc" } },
      ...owned,
    }),
  );
  assert.equal(f.calls.filter((v) => v.method === "evener/upgrade").length, 0);
});

test("lost or malformed reply is uncertain without replay", async () => {
  for (const value of [new Error("lost"), { release: "v2" }]) {
    const f = fake([overview(), value]);
    const result = await runHubUpgrade(f.hub, {
      action: "upgrade",
      params: { reviewed: { version: "v1", commit: "abc" } },
      ...owned,
    });
    assert.equal(result.outcome, "uncertain");
    assert.equal(f.calls.filter((v) => v.method === "evener/upgrade").length, 1);
  }
});

test("CLI reserves mode-0600 output and keeps stdout metadata-only", async () => {
  const { runHubUpgradeCLI } = await import("./hub-upgrade-cli.mjs");
  const root = await mkdtemp(join(tmpdir(), "hub-upgrade-contract-"));
  try {
    const paramsFile = join(root, "params.json");
    await writeFile(paramsFile, JSON.stringify({ requested: "latest", reviewed: { version: "v1", commit: "abc" } }));
    const output = join(root, "result.json");
    const stdout = [];
    const f = fake([overview(), response()]);
    await runHubUpgradeCLI(
      {
        EVENER_HUB_UPGRADE_ACTION: "upgrade",
        EVENER_HUB_UPGRADE_PARAMS_FILE: paramsFile,
        EVENER_HUB_UPGRADE_OUTPUT_FILE: output,
        EVENER_HUB_UPGRADE_MUTATION: "1",
        EVENER_HUB_UPGRADE_OWNED_HUB: "ws://owned/rpc",
        EVENER_RPC_URL: "ws://owned/rpc",
      },
      f.hub,
      { stdout: (value) => stdout.push(JSON.parse(value)) },
    );
    assert.equal((await stat(output)).mode & 0o777, 0o600);
    assert.deepEqual(stdout, [
      { action: "upgrade", outcome: "acknowledged", execution: "unverified", installedCount: 1, outputWritten: true },
    ]);
    assert.equal(JSON.parse(await readFile(output, "utf8")).release, "v2");
    const occupied = join(root, "occupied.json");
    await writeFile(occupied, "keep");
    const blocked = fake([]);
    await assert.rejects(
      runHubUpgradeCLI({ EVENER_HUB_UPGRADE_ACTION: "review", EVENER_HUB_UPGRADE_OUTPUT_FILE: occupied }, blocked.hub),
    );
    assert.deepEqual(blocked.calls, []);
    assert.equal(await readFile(occupied, "utf8"), "keep");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
