import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test, { after } from "node:test";
import { fileURLToPath } from "node:url";
import { runOAuthCLI } from "./oauth-cli.mjs";

const directories = [];
async function temporaryDirectory(prefix) {
  const directory = await mkdtemp(join(tmpdir(), prefix));
  directories.push(directory);
  return directory;
}
after(async () => {
  await Promise.all(directories.map((directory) => rm(directory, { recursive: true, force: true })));
});

import { AcknowledgedReadbackError, runOAuth } from "./oauth-logic.mjs";

const endpoint = "ws://127.0.0.1:4310/rpc";
const status = {
  provider: "openai-codex",
  supported: true,
  signedIn: false,
  activeSource: "none",
  authModes: ["oauth"],
  hasStoredOAuth: false,
};
const authorized = { ...status, signedIn: true, activeSource: "store", hasStoredOAuth: true };
const deviceFlow = {
  rpcUrl: endpoint,
  provider: "openai-codex",
  kind: "device",
  flowId: "device-1",
  url: "https://example.test/device",
  userCode: "ABCD",
  intervalSeconds: 5,
};

function fixture({ reply = { state: "pending" }, before = status, readback = authorized, start, loginStart } = {}) {
  const calls = [];
  let statusReads = 0;
  return {
    calls,
    hub: {
      close() {},
      async connect() {
        calls.push("connect");
      },
      async request(method, params) {
        calls.push({ method, params });
        if (method === "evener/auth/status") return statusReads++ === 0 ? before : readback;
        if (method === "evener/auth/device/start")
          return (
            start ?? {
              provider: "openai-codex",
              flowId: "device-1",
              userCode: "ABCD",
              verificationUrl: "https://example.test/device",
              intervalSeconds: 5,
            }
          );
        if (method === "evener/auth/device/poll") return reply;
        if (method === "evener/auth/login/start")
          return (
            loginStart ?? {
              provider: "openai-codex",
              flowId: "login-1",
              url: "https://example.test/login",
            }
          );
        return { status: authorized };
      },
    },
  };
}

async function withOptIn(callback) {
  const previous = {
    mutation: process.env.EVENER_OAUTH_MUTATION,
    endpoint: process.env.EVENER_RPC_URL,
  };
  process.env.EVENER_OAUTH_MUTATION = "1";
  process.env.EVENER_RPC_URL = endpoint;
  try {
    return await callback();
  } finally {
    if (previous.mutation === undefined) delete process.env.EVENER_OAUTH_MUTATION;
    else process.env.EVENER_OAUTH_MUTATION = previous.mutation;
    if (previous.endpoint === undefined) delete process.env.EVENER_RPC_URL;
    else process.env.EVENER_RPC_URL = previous.endpoint;
  }
}

test("start captures endpoint and caller inputs before deferred connect", async () =>
  withOptIn(async () => {
    const params = { provider: "openai-codex", reviewed: structuredClone(status) };
    const fixtureValue = fixture();
    fixtureValue.hub.connect = async () => {
      params.provider = "other";
      params.reviewed.signedIn = true;
      process.env.EVENER_RPC_URL = "ws://changed";
      fixtureValue.calls.push("connect");
    };
    const result = await runOAuth(fixtureValue.hub, { action: "device/start", params, ownedHub: endpoint });
    assert.equal(result.rpcUrl, endpoint);
    assert.deepEqual(fixtureValue.calls.at(-1).params, { provider: "openai-codex" });
  }));

test("start malformed reply performs one status readback and never replays", async () =>
  withOptIn(async () => {
    const f = fixture({ start: {} });
    const result = await runOAuth(f.hub, {
      action: "device/start",
      params: { provider: "openai-codex", reviewed: status },
      ownedHub: endpoint,
    });
    assert.equal(result.outcome, "uncertain");
    assert.equal(f.calls.filter((call) => call.method?.endsWith("device/start")).length, 1);
    assert.equal(f.calls.filter((call) => call.method === "evener/auth/status").length, 2);
  }));

test("poll and completion accept only fully authorized inner statuses", async () =>
  withOptIn(async () => {
    for (const field of ["supported", "signedIn", "hasStoredOAuth"]) {
      const invalid = { ...authorized, [field]: false };
      const f = fixture({ reply: { state: "authorized", status: invalid } });
      const result = await runOAuth(f.hub, {
        action: "device/poll",
        params: { provider: "openai-codex", flow: deviceFlow },
        ownedHub: endpoint,
      });
      assert.equal(result.outcome, "uncertain");
      assert.equal(f.calls.filter((call) => call.method === "evener/auth/status").length, 1);
    }
    for (const field of ["supported", "signedIn", "hasStoredOAuth"]) {
      const invalid = { ...authorized };
      delete invalid[field];
      const f = fixture({ reply: { state: "authorized", status: invalid } });
      const result = await runOAuth(f.hub, {
        action: "device/poll",
        params: { provider: "openai-codex", flow: deviceFlow },
        ownedHub: endpoint,
      });
      assert.equal(result.outcome, "uncertain");
    }
  }));

test("malformed optional status fields are uncertain after one readback", async () =>
  withOptIn(async () => {
    const f = fixture({ reply: { state: "authorized", status: { ...authorized, email: 42 } } });
    const result = await runOAuth(f.hub, {
      action: "device/poll",
      params: { provider: "openai-codex", flow: deviceFlow },
      ownedHub: endpoint,
    });
    assert.equal(result.outcome, "uncertain");
  }));

test("acknowledged status and concurrent logout are both preserved", async () =>
  withOptIn(async () => {
    const loggedOut = { ...authorized, signedIn: false, hasStoredOAuth: true };
    const f = fixture({ reply: { state: "authorized", status: authorized }, before: loggedOut });
    const result = await runOAuth(f.hub, {
      action: "device/poll",
      params: { provider: "openai-codex", flow: deviceFlow },
      ownedHub: endpoint,
    });
    assert.equal(result.outcome, "authorized");
    assert.equal(result.acknowledged.signedIn, true);
    assert.equal(result.readback.signedIn, false);
  }));

test("both mutation and readback failures retain both error identities", async () =>
  withOptIn(async () => {
    const mutationError = new Error("mutation sentinel");
    const readbackError = new Error("readback sentinel");
    const f = fixture();
    f.hub.request = async (method) => {
      if (method === "evener/auth/status") throw readbackError;
      throw mutationError;
    };
    await assert.rejects(
      runOAuth(f.hub, {
        action: "device/poll",
        params: { provider: "openai-codex", flow: deviceFlow },
        ownedHub: endpoint,
      }),
      (error) =>
        error instanceof AggregateError && error.errors.includes(mutationError) && error.errors.includes(readbackError),
    );
  }));

test("acknowledged reply with failed readback carries the acknowledged state", async () =>
  withOptIn(async () => {
    const readbackError = new Error("readback sentinel");
    const f = fixture({ reply: { state: "authorized", status: authorized } });
    f.hub.request = async (method) => {
      if (method === "evener/auth/status") throw readbackError;
      return { state: "authorized", status: authorized };
    };
    await assert.rejects(
      runOAuth(f.hub, {
        action: "device/poll",
        params: { provider: "openai-codex", flow: deviceFlow },
        ownedHub: endpoint,
      }),
      (error) =>
        error instanceof AcknowledgedReadbackError &&
        error.acknowledged.signedIn === true &&
        error.cause === readbackError,
    );
  }));

test("CLI redacts invalid private JSON and decoder/provider failures", async () => {
  const cli = fileURLToPath(new URL("./oauth.mjs", import.meta.url));
  const directory = await temporaryDirectory("evener-oauth-cli-");
  for (const payload of [
    '{"sentinel":"private-json-sentinel"',
    JSON.stringify({ provider: "private-provider-sentinel", reviewed: status }),
  ]) {
    const paramsFile = join(directory, "params.json");
    await writeFile(paramsFile, payload);
    const child = spawn(process.execPath, [cli], {
      env: {
        ...process.env,
        EVENER_OAUTH_ACTION: "device/start",
        EVENER_OAUTH_PARAMS_FILE: paramsFile,
        EVENER_OAUTH_MUTATION: "1",
        EVENER_RPC_URL: endpoint,
        EVENER_OAUTH_OWNED_HUB: endpoint,
        EVENER_OAUTH_OUTPUT_FILE: join(directory, "challenge.json"),
      },
    });
    let output = "";
    child.stdout.on("data", (chunk) => {
      output += chunk;
    });
    child.stderr.on("data", (chunk) => {
      output += chunk;
    });
    const exitCode = await new Promise((resolve) => child.on("close", resolve));
    assert.equal(exitCode, 1);
    assert.ok(output.includes("OAuth operation could not be confirmed."));
    assert.equal(output.includes("private-json-sentinel"), false);
    assert.equal(output.includes("private-provider-sentinel"), false);
  }
});

test("private challenge output is exclusive mode 0600", async () => {
  const directory = await temporaryDirectory("evener-oauth-file-");
  const path = join(directory, "challenge.json");
  const { runOAuthCLI } = await import("./oauth-cli.mjs");
  const env = {
    EVENER_OAUTH_ACTION: "device/start",
    EVENER_OAUTH_PARAMS_FILE: join(directory, "params.json"),
    EVENER_OAUTH_OUTPUT_FILE: path,
    EVENER_OAUTH_OWNED_HUB: endpoint,
    EVENER_RPC_URL: endpoint,
    EVENER_OAUTH_MUTATION: "1",
  };
  await writeFile(env.EVENER_OAUTH_PARAMS_FILE, JSON.stringify({ provider: "openai-codex", reviewed: status }));
  const output = [];
  const code = await runOAuthCLI({
    env,
    connect: () => ({ hub: fixture().hub }),
    stdout: (value) => output.push(value),
    stderr: (value) => output.push(value),
  });
  assert.equal(code, 0);
  assert.equal((await stat(path)).mode & 0o777, 0o600);
  assert.equal(JSON.parse(await readFile(path, "utf8")).flowId, "device-1");
  assert.equal(output.join("").includes("example.test"), false);
});

test("continuations capture caller-owned input and reject different flow ownership", async () =>
  withOptIn(async () => {
    for (const change of [{ rpcUrl: "ws://other/rpc" }, { provider: "another" }, { kind: "login" }]) {
      const f = fixture();
      await assert.rejects(
        runOAuth(f.hub, {
          action: "device/poll",
          params: { provider: status.provider, flow: { ...deviceFlow, ...change } },
          ownedHub: endpoint,
        }),
      );
      assert.deepEqual(f.calls, []);
    }
    const f = fixture();
    const params = { provider: status.provider, flow: { ...deviceFlow } };
    let release;
    f.hub.connect = () =>
      new Promise((resolve) => {
        release = resolve;
      });
    const operation = runOAuth(f.hub, { action: "device/poll", params, ownedHub: endpoint });
    params.provider = "another";
    params.flow.flowId = "another-flow";
    release();
    assert.equal((await operation).outcome, "pending");
    assert.deepEqual(f.calls, [
      { method: "evener/auth/device/poll", params: { provider: status.provider, flowId: deviceFlow.flowId } },
    ]);
  }));

test("reviewed capability and opt-in checks reject before OAuth dispatch", async () =>
  withOptIn(async () => {
    for (const before of [
      { ...status, supported: false },
      { ...status, authModes: [] },
      { ...status, signedIn: true },
    ]) {
      const f = fixture({ before });
      await assert.rejects(
        runOAuth(f.hub, {
          action: "device/start",
          params: { provider: status.provider, reviewed: status },
          ownedHub: endpoint,
        }),
      );
      assert.equal(f.calls.filter((call) => call.method?.endsWith("/start")).length, 0);
    }
    for (const environment of [
      { EVENER_RPC_URL: endpoint },
      { EVENER_RPC_URL: "ws://other", EVENER_OAUTH_MUTATION: "1" },
    ]) {
      const f = fixture();
      await assert.rejects(
        runOAuth(f.hub, {
          action: "device/poll",
          params: { provider: status.provider, flow: deviceFlow },
          ownedHub: endpoint,
          environment,
        }),
      );
      assert.deepEqual(f.calls, []);
    }
  }));

test("start validates challenge metadata and only device start permits fallback", async () =>
  withOptIn(async () => {
    const challenge = {
      provider: status.provider,
      flowId: "flow",
      userCode: "CODE",
      verificationUrl: "https://example.test/device",
      intervalSeconds: 0,
    };
    for (const change of [
      { provider: "other" },
      { flowId: " " },
      { userCode: "" },
      { intervalSeconds: -1 },
      { intervalSeconds: 0.5 },
      { verificationUrl: "file:///private" },
      { verificationUrl: "https://u:p@example.test" },
      { fallback: "true" },
    ]) {
      const f = fixture({ start: { ...challenge, ...change } });
      const result = await runOAuth(f.hub, {
        action: "device/start",
        params: { provider: status.provider, reviewed: status },
        ownedHub: endpoint,
      });
      assert.equal(result.outcome, "uncertain");
      assert.equal(f.calls.filter((call) => call.method === "evener/auth/status").length, 2);
      assert.equal(f.calls.filter((call) => call.method?.endsWith("/start")).length, 1);
    }
    const fallback = fixture({ start: { provider: status.provider, fallback: true } });
    assert.equal(
      (
        await runOAuth(fallback.hub, {
          action: "device/start",
          params: { provider: status.provider, reviewed: status },
          ownedHub: endpoint,
        })
      ).outcome,
      "fallback",
    );
    assert.equal(fallback.calls.filter((call) => call.method?.endsWith("login/start")).length, 0);
    for (const reply of [
      { provider: status.provider, fallback: true },
      { provider: status.provider, flowId: "" },
      { provider: status.provider, flowId: "b", url: "javascript:void(0)" },
    ]) {
      const f = fixture({ loginStart: reply });
      assert.equal(
        (
          await runOAuth(f.hub, {
            action: "login/start",
            params: { provider: status.provider, reviewed: status },
            ownedHub: endpoint,
          })
        ).outcome,
        "uncertain",
      );
      assert.equal(f.calls.filter((call) => call.method === "evener/auth/status").length, 2);
    }
  }));

test("browser completion validates inner status and performs no replay", async () =>
  withOptIn(async () => {
    for (const reply of [
      null,
      {},
      { status: { ...authorized, provider: "other" } },
      { status: { ...authorized, supported: false } },
      { status: { ...authorized, hasStoredOAuth: false } },
      { status: { ...authorized, signedIn: false } },
      { status: authorized },
    ]) {
      const calls = [];
      const hub = {
        connect: async () => {},
        request: async (method, params) => {
          calls.push({ method, params });
          return method === "evener/auth/status" ? status : reply;
        },
      };
      const result = await runOAuth(hub, {
        action: "login/complete",
        params: {
          provider: status.provider,
          flow: { ...deviceFlow, kind: "login" },
          redirectUrl: "http://localhost:1455/auth/callback?code=fixture",
        },
        ownedHub: endpoint,
      });
      assert.equal(result.outcome, reply?.status === authorized ? "authorized" : "uncertain");
      assert.deepEqual(
        calls.map((call) => call.method),
        ["evener/auth/login/complete", "evener/auth/status"],
      );
    }
  }));

test("private output is reserved before connecting and existing files survive failure", async () => {
  const directory = await temporaryDirectory("evener-oauth-reserve-");
  const outputPath = join(directory, "challenge.json");
  const paramsPath = join(directory, "params.json");
  await writeFile(paramsPath, JSON.stringify({ provider: status.provider, reviewed: status }));
  const env = {
    EVENER_OAUTH_ACTION: "device/start",
    EVENER_OAUTH_PARAMS_FILE: paramsPath,
    EVENER_OAUTH_OUTPUT_FILE: outputPath,
    EVENER_OAUTH_MUTATION: "1",
    EVENER_OAUTH_OWNED_HUB: endpoint,
    EVENER_RPC_URL: endpoint,
  };
  await writeFile(outputPath, "preserved-file");
  let connections = 0;
  const run = (connect) => runOAuthCLI({ env, connect, stdout: () => {}, stderr: () => {} });
  assert.equal(
    await run(() => {
      connections++;
      throw new Error();
    }),
    1,
  );
  assert.equal(connections, 0);
  assert.equal(await readFile(outputPath, "utf8"), "preserved-file");
  await rm(outputPath);
  const hub = fixture().hub;
  hub.connect = async () => {
    assert.equal((await stat(outputPath)).mode & 0o777, 0o600);
    assert.equal(await readFile(outputPath, "utf8"), "");
    throw new Error("connect failed");
  };
  assert.equal(await run(() => ({ hub })), 1);
  await assert.rejects(stat(outputPath), { code: "ENOENT" });
  hub.connect = async () => {
    await rm(outputPath);
    await writeFile(outputPath, "replacement-file");
    throw new Error("connect failed");
  };
  assert.equal(await run(() => ({ hub })), 1);
  assert.equal(await readFile(outputPath, "utf8"), "replacement-file");
});
