import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const packageDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const consumerDir = mkdtempSync(join(tmpdir(), "evener-appwire-package-"));
const run = (command, args, cwd) =>
  execFileSync(command, args, {
    cwd,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });
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
for (const forbidden of ["package/client.ts", "package/src/", "package/node_modules/"])
  assert(!listing.includes(forbidden), `contains ${forbidden}`);
console.log(`qualified ${packed.name}@${packed.version} in installed consumer`);
