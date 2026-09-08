import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { runHubSetup } from "./hub-setup-logic.mjs";

function fake(responses) {
  const calls = [];
  return {
    calls,
    hub: {
      connect: async () => calls.push("connect"),
      request: async (m, p) => {
        calls.push({ m, p });
        const r = responses.shift();
        if (r instanceof Error) throw r;
        return r;
      },
    },
  };
}
test("pairing sends origin and returns private response", async () => {
  const f = fake([{ authUrl: "http://hub/auth/fixture-token" }]);
  const r = await runHubSetup(f.hub, { action: "pairing", params: { origin: "http://192.168.1.2:9180" } });
  assert.equal(r.readback.authUrl, "http://hub/auth/fixture-token");
  assert.deepEqual(f.calls, ["connect", { m: "evener/mobile/pairing", p: { origin: "http://192.168.1.2:9180" } }]);
});
test("directory creation requires owned mutation opt-in and preserves captured input", async () => {
  const f = fake([{ path: "/tmp/new", created: true }]);
  const params = { path: "/tmp/new" };
  const r = await runHubSetup(f.hub, {
    action: "dirs",
    params,
    ownedHub: "http://hub",
    mutationOptIn: "1",
    rpcUrl: "http://hub",
  });
  assert.equal(r.readback.created, true);
  assert.deepEqual(f.calls[1].p, { path: "/tmp/new" });
});
test("invalid inputs fail before connection", async () => {
  const f = fake([]);
  await assert.rejects(runHubSetup(f.hub, { action: "pairing", params: { origin: "" } }));
  assert.deepEqual(f.calls, []);
});

test("directory failures and malformed replies retain uncertainty without replay", async () => {
  for (const response of [new Error("private server path"), { path: "/owned/path" }, { path: "", created: true }]) {
    const f = fake([response]);
    const result = await runHubSetup(f.hub, {
      action: "dirs",
      params: { path: "/owned/path" },
      ownedHub: "ws://owned/rpc",
      mutationOptIn: "1",
      rpcUrl: "ws://owned/rpc",
    });
    assert.equal(result.outcome, "uncertain");
    assert.equal(f.calls.filter((call) => call.m === "evener/dirs/create").length, 1);
    assert.equal(result.readback, undefined);
  }
});

test("directory parameters are captured before asynchronous connection", async () => {
  const params = { path: "~/owned" };
  let received;
  const result = await runHubSetup(
    {
      connect: async () => {
        params.path = "/different";
      },
      request: async (_, value) => {
        received = value;
        return { path: "/resolved/owned", created: false };
      },
    },
    { action: "dirs", params, ownedHub: "ws://owned/rpc", mutationOptIn: "1", rpcUrl: "ws://owned/rpc" },
  );
  assert.deepEqual(received, { path: "~/owned" });
  assert.equal(result.readback.created, false);
});

test("CLI requires and reserves private output before accessing pairing credentials", async () => {
  const { runHubSetupCLI } = await import("./hub-setup-cli.mjs");
  const root = await mkdtemp(join(tmpdir(), "hub-setup-contract-"));
  try {
    const params = join(root, "params.json");
    await writeFile(params, JSON.stringify({ origin: "https://hub.example" }));
    const output = join(root, "output.json");
    const environment = { EVENER_HUB_SETUP_ACTION: "pairing", EVENER_HUB_SETUP_PARAMS_FILE: params };
    const f = fake([{ authUrl: "https://hub.example/auth/fixture-token" }]);
    await assert.rejects(runHubSetupCLI(environment, f.hub));
    assert.deepEqual(f.calls, []);
    await writeFile(output, "keep");
    await assert.rejects(runHubSetupCLI({ ...environment, EVENER_HUB_SETUP_OUTPUT_FILE: output }, f.hub));
    assert.deepEqual(f.calls, []);
    assert.equal(await readFile(output, "utf8"), "keep");
    const privateOutput = join(root, "private.json");
    const stdout = [];
    await runHubSetupCLI({ ...environment, EVENER_HUB_SETUP_OUTPUT_FILE: privateOutput }, f.hub, {
      stdout: (value) => stdout.push(JSON.parse(value)),
    });
    assert.equal((await stat(privateOutput)).mode & 0o777, 0o600);
    assert.deepEqual(JSON.parse(await readFile(privateOutput, "utf8")), {
      authUrl: "https://hub.example/auth/fixture-token",
    });
    assert.deepEqual(stdout, [{ action: "pairing", outcome: "read", execution: "unverified", outputWritten: true }]);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("CLI records unavailable response privately and never prints error bodies", async () => {
  const { runHubSetupCLI } = await import("./hub-setup-cli.mjs");
  const root = await mkdtemp(join(tmpdir(), "hub-setup-contract-"));
  try {
    const params = join(root, "params.json");
    await writeFile(params, JSON.stringify({ path: "/owned/new" }));
    const output = join(root, "output.json");
    const stdout = [];
    const f = fake([new Error("private-token")]);
    const result = await runHubSetupCLI(
      {
        EVENER_HUB_SETUP_ACTION: "dirs",
        EVENER_HUB_SETUP_PARAMS_FILE: params,
        EVENER_HUB_SETUP_OUTPUT_FILE: output,
        EVENER_HUB_SETUP_MUTATION: "1",
        EVENER_HUB_SETUP_OWNED_HUB: "ws://owned/rpc",
        EVENER_RPC_URL: "ws://owned/rpc",
      },
      f.hub,
      { stdout: (value) => stdout.push(JSON.parse(value)) },
    );
    assert.equal(result.outcome, "uncertain");
    assert.deepEqual(JSON.parse(await readFile(output, "utf8")), {
      outcome: "uncertain",
      execution: "unverified",
      readback: "unavailable",
    });
    assert.deepEqual(stdout, [{ action: "dirs", outcome: "uncertain", execution: "unverified", outputWritten: true }]);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("directory ownership failures refuse before connection", async () => {
  for (const options of [
    { mutationOptIn: "0", rpcUrl: "ws://owned/rpc" },
    { mutationOptIn: "1", rpcUrl: "ws://different/rpc" },
  ]) {
    const f = fake([]);
    await assert.rejects(
      runHubSetup(f.hub, { action: "dirs", params: { path: "/owned/path" }, ownedHub: "ws://owned/rpc", ...options }),
    );
    assert.deepEqual(f.calls, []);
  }
});

test("pairing captures origin and rejects malformed bootstrap replies without replay", async () => {
  const params = { origin: "https://requested.example" };
  let received;
  const result = await runHubSetup(
    {
      connect: async () => {
        params.origin = "https://changed.example";
      },
      request: async (_, p) => {
        received = p;
        return { authUrl: "https://configured.example/auth/fixture-token" };
      },
    },
    { action: "pairing", params },
  );
  assert.equal(result.outcome, "read");
  assert.deepEqual(received, { origin: "https://requested.example" });
  for (const authUrl of [
    "invalid",
    "https://hub.example/auth?token=fixture",
    "https://user:fixture@hub.example/auth/fixture",
    "file:///auth/fixture",
  ]) {
    const f = fake([{ authUrl }]);
    const r = await runHubSetup(f.hub, { action: "pairing", params: { origin: "https://hub.example" } });
    assert.equal(r.outcome, "uncertain");
    assert.equal(r.readback, undefined);
    assert.equal(f.calls.filter((call) => call.m === "evener/mobile/pairing").length, 1);
  }
});
