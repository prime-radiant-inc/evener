import assert from "node:assert/strict";
import { execFile, execFileSync } from "node:child_process";
import { once } from "node:events";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { WebSocketServer } from "ws";
import { runInstalledDiscoveryContracts } from "./discovery-contracts.mjs";

const packageDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const consumerDir = mkdtempSync(join(tmpdir(), "evener-appwire-package-"));
const run = (command, args, cwd) =>
  execFileSync(command, args, {
    cwd,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });
async function qualify() {
  const packed = JSON.parse(run("npm", ["pack", "--json", "--pack-destination", consumerDir], packageDir))[0];
  const tarball = join(consumerDir, packed.filename);
  run(
    "npm",
    ["install", "--offline", "--ignore-scripts", "--no-audit", "--no-fund", "--package-lock=false", tarball],
    consumerDir,
  );
  // Every module the package ships, and the one it deliberately does not.
  // docContent.ts calls global fetch against a same-origin hub URL
  // ("docContent.ts:77"), so it cannot run in a bare Node consumer; it ships
  // once it takes a base-URL and fetch port.
  const shippedModules = [
    "index",
    "client",
    "errors",
    "transport",
    "types.gen",
    "askAnswers",
    "activityData",
    "activityList",
    "activityMerge",
    "itemFailure",
    "jobOutput",
    "model",
    "reducer",
    "sendQueueAvailability",
    "sessionErrors",
    "stableDelegate",
  ];
  const heldBackModules = ["docContent"];
  // Every runtime export of the package entry point. All four generated
  // consumer programs are built from this one list, so a module that compiles
  // in the checkout but is not packed, or is packed but unreachable from the
  // entry point, fails here instead of in somebody's consumer.
  const runtimeExports = [
    "AppwireClient",
    "APPWIRE_PROTOCOL_VERSION",
    "ConnectionClosedError",
    "RequestTimeoutError",
    "WireError",
    "rpcURLFromLocation",
    "composeAskAnswers",
    "METHOD_NAMES",
    "NOTIFICATION_NAMES",
    "STEERING_KINDS",
    "THREAD_ITEM_EVENT_KINDS",
    "isFailedJobOutcome",
    "isFailedDelegateOutcome",
    "isActivityFailure",
    "isTurnContainer",
    "parseActivityTree",
    "activityNodeID",
    "delegateHasActiveWork",
    "defaultExpandedIDs",
    "reconcileActivityState",
    "ActivityList",
    "fenceRootSession",
    "graftContinuationTree",
    "hasItemFailure",
    "hasErrorText",
    "hasFailureStatus",
    "isNonZeroExit",
    "isInProgressStatus",
    "parseJobLogTail",
    "SYSTEM_PRELUDE_TURN_ID",
    "pendingTextJoined",
    "imageSessionRouteForSession",
    "hydrateThread",
    "collectAuthoritativeMutationIds",
    "prependOlderTurns",
    "mergeOlderItemPage",
    "resolvePendingEscalation",
    "notificationRoutingKey",
    "notificationTargetsThread",
    "applyNotification",
    "deriveSendQueueAvailability",
    "isActionUnavailable",
    "isThreadNotFound",
    "stableDelegateDisplayStatus",
  ];
  // One exported type per shipped module that declares any, so the declaration
  // check covers each module's packed .d.ts and not just its runtime half.
  const typeExports = [
    "AppwireClientOptions",
    "WebSocketLike",
    "AskAnswerItem",
    "ActivityTree",
    "ActivityState",
    "ItemFailureSignals",
    "JobLogTail",
    "ThreadModel",
    "NotificationRoutingKey",
    "SendQueueAvailability",
    "StableDelegateState",
  ];
  // One call per shipped module, with a trivial input. Importing alone would
  // pass for a module that needs a browser global at load time; calling proves
  // each module actually evaluates and runs inside a bare Node consumer.
  const smokeCalls = `assert.equal(typeof client.AppwireClient, "function");
assert.equal(client.rpcURLFromLocation({ protocol: "https:", host: "hub.example:9180" }), "wss://hub.example:9180/rpc");
assert.equal(client.composeAskAnswers([]), "[answers]");
assert.equal(new client.WireError("nope", -32000).code, -32000);
assert(client.METHOD_NAMES.length > 0);
const session = {
  kind: "session", sessionId: "thread", ref: "ref", label: "label", aggregate: "idle",
  counts: { active: 0, failed: 0, completed: 0, complete: true }, entries: [], branch: {},
};
const tree = { revision: 1, root: session };
assert.equal(client.parseActivityTree(null), null);
assert.equal(client.activityNodeID(session), "session:thread");
assert(Array.isArray(client.defaultExpandedIDs(tree)));
assert.equal(client.isActivityFailure("failure", undefined), true);
assert.equal(client.fenceRootSession(session, session).sessionId, "thread");
assert.equal(client.graftContinuationTree(tree, "session:thread", tree).revision, 1);
assert.equal(client.hasItemFailure({ status: "completed", exitCode: 1 }), true);
assert.equal(client.hasFailureStatus({ status: "interrupted" }), true);
assert.equal(client.hasErrorText({ error: "  " }), false);
assert.equal(client.isNonZeroExit({ exitCode: 0 }), false);
assert.equal(client.isInProgressStatus("inProgress"), true);
assert.equal(client.parseJobLogTail(null), null);
assert.equal(client.SYSTEM_PRELUDE_TURN_ID, "turn_system");
assert.equal(client.pendingTextJoined(["a", "b"]), "ab");
assert.equal(client.notificationRoutingKey({ method: "evener/x", params: {} }), null);
assert.equal(client.deriveSendQueueAvailability({ statusType: "restartRequired", capabilities: {} }).canSend, false);
assert.equal(client.isActionUnavailable(new Error("not a wire error")), false);
assert.equal(client.isThreadNotFound(new Error("not a wire error")), false);
assert.equal(client.stableDelegateDisplayStatus({ status: "running" }), "running");
const activity = new client.ActivityList({ request: async () => ({}), onNotification: () => () => {} }, "ref", "thread");
assert.equal(activity.getSnapshot().tree, null);
`;
  const presenceLoop = `for (const name of ${JSON.stringify(runtimeExports)}) assert(name in client, \`missing export \${name}\`);\n`;
  writeFileSync(
    join(consumerDir, "esm.mts"),
    `import {
${runtimeExports.map((name) => `  ${name},`).join("\n")}
} from "@evener/appwire-client";
import type {
${typeExports.map((name) => `  ${name},`).join("\n")}
} from "@evener/appwire-client";
const client: AppwireClient = new AppwireClient({ url: "ws://127.0.0.1:1/rpc" });
const version: string = APPWIRE_PROTOCOL_VERSION; void client; void version;
declare const shipped: [${typeExports.join(", ")}]; void shipped;
${runtimeExports.map((name) => `void ${name};`).join("\n")}
`,
  );
  writeFileSync(
    join(consumerDir, "commonjs.cts"),
    `import client = require("@evener/appwire-client");
const app: client.AppwireClient = new client.AppwireClient({ url: "ws://127.0.0.1:1/rpc" }); void app;
declare const shipped: [${typeExports.map((name) => `client.${name}`).join(", ")}]; void shipped;
${runtimeExports.map((name) => `void client.${name};`).join("\n")}
`,
  );
  run(
    resolve(packageDir, "node_modules/.bin/tsc"),
    [
      "--strict",
      "--noEmit",
      "--module",
      "NodeNext",
      "--moduleResolution",
      "NodeNext",
      "--target",
      "ES2022",
      "esm.mts",
      "commonjs.cts",
    ],
    consumerDir,
  );
  writeFileSync(
    join(consumerDir, "esm-runtime.mjs"),
    `import assert from "node:assert/strict";
import * as client from "@evener/appwire-client";
${presenceLoop}${smokeCalls}`,
  );
  writeFileSync(
    join(consumerDir, "commonjs-runtime.cjs"),
    `const assert = require("node:assert/strict");
const client = require("@evener/appwire-client");
${presenceLoop}${smokeCalls}`,
  );
  run(process.execPath, [join(consumerDir, "esm-runtime.mjs")], consumerDir);
  run(process.execPath, [join(consumerDir, "commonjs-runtime.cjs")], consumerDir);
  const listing = run("tar", ["-tzf", tarball], consumerDir);
  for (const expected of [
    ...shippedModules.flatMap((module) => [`package/dist/${module}.js`, `package/dist/${module}.d.ts`]),
    "package/README.md",
    "package/examples/connection.mjs",
    "package/examples/inspect.mjs",
    "package/examples/discovery.mjs",
    "package/examples/discovery-cli.mjs",
    "package/examples/discovery-logic.mjs",
    "package/examples/private-output.mjs",
  ])
    assert(listing.includes(`${expected}\n`), `missing ${expected}`);
  for (const entry of listing.trim().split("\n")) {
    assert(
      entry === "package/package.json" ||
        entry === "package/README.md" ||
        entry.startsWith("package/dist/") ||
        entry.startsWith("package/examples/"),
      `unexpected shipped path ${entry}`,
    );
    assert(!entry.endsWith(".ts") || entry.endsWith(".d.ts"), `source leak ${entry}`);
  }
  for (const module of heldBackModules)
    assert(!listing.includes(`package/dist/${module}.`), `held-back module shipped: ${module}`);
  // Run the shipped program from the installed tarball. Only the remote server
  // is scripted; imports, sockets, handshake, client requests and output are real.
  const serverProtocolVersion = "evener-appwire-v5";
  const fixtureCwd = "/fixture/project";
  const responses = new Map([
    ["model/list", { params: { cwd: fixtureCwd }, result: { data: [] } }],
    ["thread/list", { params: { limit: 20 }, result: { data: [], nextCursor: "next-page" } }],
    ["evener/launch/schema", { params: {}, result: { options: [] } }],
    [
      "evener/launch/resolve",
      { params: { cwd: fixtureCwd }, result: { effective: {}, layers: {}, provenance: {}, diagnostics: [] } },
    ],
  ]);
  let discoveryMode;
  const observedMethods = [];
  const controller = new AbortController();
  let serverError;
  const server = new WebSocketServer({ host: "127.0.0.1", port: 0 });
  server.on("connection", (socket) => {
    socket.on("message", async (data) => {
      try {
        const request = JSON.parse(data.toString());
        if (request.method === "initialized" && request.id === undefined) return;
        observedMethods.push(request.method);
        let result;
        if (request.method === "initialize") {
          assert.equal(request.params.clientInfo.name, "appwire-reference");
          assert.equal(request.params.protocolVersion, serverProtocolVersion);
          result = {
            serverInfo: { name: "package-qualification", version: "0.0.0" },
            protocolVersion: serverProtocolVersion,
            sourceId: "qualification-source",
            features: {
              threadList: true,
              threadTurnsList: true,
              turnStart: false,
              turnSteer: false,
              threadClear: false,
              threadShutdown: false,
              forkFromTurn: false,
              tasks: false,
              transcriptList: true,
              modelList: true,
              directoryComplete: true,
              auth: false,
            },
          };
        } else if (discoveryMode) {
          assert.equal(request.method, discoveryMode.method);
          assert.deepEqual(request.params, discoveryMode.params);
          await discoveryMode.beforeResponse?.();
          if (discoveryMode.error) {
            socket.send(JSON.stringify({ jsonrpc: "2.0", id: request.id, error: discoveryMode.error }));
            return;
          }
          if (discoveryMode.close) {
            socket.close();
            return;
          }
          result = discoveryMode.response;
        } else {
          const response = responses.get(request.method);
          assert(response, `unexpected method ${request.method}`);
          assert.deepEqual(request.params, response.params);
          result = response.result;
        }
        socket.send(JSON.stringify({ jsonrpc: "2.0", id: request.id, result }));
      } catch (error) {
        serverError ??= error;
        controller.abort(error);
      }
    });
  });
  server.on("error", (error) => {
    serverError ??= error;
    controller.abort(error);
  });
  try {
    await once(server, "listening", { signal: controller.signal });
    const address = server.address();
    assert(address && typeof address !== "string");
    const { stdout } = await promisify(execFile)(
      process.execPath,
      [join(consumerDir, "node_modules/@evener/appwire-client/examples/inspect.mjs")],
      {
        cwd: consumerDir,
        env: { ...process.env, EVENER_RPC_URL: `ws://127.0.0.1:${address.port}/rpc`, EVENER_CWD: fixtureCwd },
        signal: controller.signal,
        timeout: 15000,
        encoding: "utf8",
      },
    );
    assert.deepEqual(observedMethods, ["initialize", ...responses.keys()]);
    assert.deepEqual(JSON.parse(stdout), {
      protocolVersion: serverProtocolVersion,
      sourceId: "qualification-source",
      models: 0,
      sessionsOnFirstPage: 0,
      hasMoreSessions: true,
      launchOptions: 0,
      resolvedLayers: [],
      repositoryTrust: "absent",
      diagnostics: 0,
    });

    await runInstalledDiscoveryContracts({
      consumerDir,
      rpcURL: `ws://127.0.0.1:${address.port}/rpc`,
      fixtureCwd,
      observedMethods,
      signal: controller.signal,
      setMode: (mode) => {
        discoveryMode = mode;
      },
    });
  } catch (error) {
    throw serverError ?? error;
  } finally {
    for (const socket of server.clients) socket.terminate();
    await new Promise((resolveClose) => server.close(resolveClose));
  }
  console.log(`qualified ${packed.name}@${packed.version}: installed imports, declarations and read-only example`);
}

try {
  await qualify();
  // Only this invocation's privately created consumer is reclaimed.
  rmSync(consumerDir, { recursive: true });
} catch (error) {
  if (error?.stdout) process.stderr.write(error.stdout);
  if (error?.stderr) process.stderr.write(error.stderr);
  console.error(`Package qualification fixture retained at ${consumerDir}`);
  throw error;
}
