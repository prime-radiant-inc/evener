import assert from "node:assert/strict";
import { execFile, execFileSync } from "node:child_process";
import { once } from "node:events";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { WebSocketServer } from "ws";

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
  writeFileSync(
    join(consumerDir, "esm.mts"),
    `import { AppwireClient, APPWIRE_PROTOCOL_VERSION } from "@evener/appwire-client";
const client: AppwireClient = new AppwireClient({ url: "ws://127.0.0.1:1/rpc" });
const version: string = APPWIRE_PROTOCOL_VERSION; void client; void version;
`,
  );
  writeFileSync(
    join(consumerDir, "commonjs.cts"),
    `import client = require("@evener/appwire-client");
const app: client.AppwireClient = new client.AppwireClient({ url: "ws://127.0.0.1:1/rpc" }); void app;
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
    `import { AppwireClient } from "@evener/appwire-client"; if (typeof AppwireClient !== "function") process.exit(1);`,
  );
  writeFileSync(
    join(consumerDir, "commonjs-runtime.cjs"),
    `const { AppwireClient } = require("@evener/appwire-client"); if (typeof AppwireClient !== "function") process.exit(1);`,
  );
  run(process.execPath, [join(consumerDir, "esm-runtime.mjs")], consumerDir);
  run(process.execPath, [join(consumerDir, "commonjs-runtime.cjs")], consumerDir);
  const listing = run("tar", ["-tzf", tarball], consumerDir);
  for (const expected of [
    "package/dist/index.js",
    "package/dist/index.d.ts",
    "package/README.md",
    "package/examples/connection.mjs",
    "package/examples/inspect.mjs",
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
    assert(!entry.endsWith(".ts") || entry.startsWith("package/dist/"), `source leak ${entry}`);
  }
  // Run the shipped program from the installed tarball. Only the remote server
  // is scripted; imports, sockets, handshake, client requests and output are real.
  const installedRequire = createRequire(join(consumerDir, "package.json"));
  const { APPWIRE_PROTOCOL_VERSION } = installedRequire("@evener/appwire-client");
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
  const observedMethods = [];
  const controller = new AbortController();
  const server = new WebSocketServer({ host: "127.0.0.1", port: 0 });
  server.on("connection", (socket) => {
    socket.on("message", (data) => {
      try {
        const request = JSON.parse(data.toString());
        if (request.method === "initialized" && request.id === undefined) return;
        observedMethods.push(request.method);
        let result;
        if (request.method === "initialize") {
          assert.equal(request.params.clientInfo.name, "appwire-reference");
          result = {
            serverInfo: { name: "package-qualification", version: "0.0.0" },
            protocolVersion: APPWIRE_PROTOCOL_VERSION,
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
        } else {
          const response = responses.get(request.method);
          assert(response, `unexpected method ${request.method}`);
          assert.deepEqual(request.params, response.params);
          result = response.result;
        }
        socket.send(JSON.stringify({ jsonrpc: "2.0", id: request.id, result }));
      } catch (error) {
        controller.abort(error);
      }
    });
  });
  server.on("error", (error) => controller.abort(error));
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
      protocolVersion: APPWIRE_PROTOCOL_VERSION,
      sourceId: "qualification-source",
      models: 0,
      sessionsOnFirstPage: 0,
      hasMoreSessions: true,
      launchOptions: 0,
      resolvedLayers: [],
      repositoryTrust: "absent",
      diagnostics: 0,
    });
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
