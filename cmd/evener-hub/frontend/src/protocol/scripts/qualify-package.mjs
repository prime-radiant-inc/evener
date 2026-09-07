import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const packageDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const fixtureDir = mkdtempSync(join(tmpdir(), "evener-appwire-package-"));
let completed = false;
process.on("exit", (code) => {
  if (code === 0 && completed) rmSync(fixtureDir, { force: true, recursive: true });
  else console.error(`qualification fixture retained at ${fixtureDir}`);
});

function run(command, args, cwd) {
  try {
    return execFileSync(command, args, {
      cwd,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    });
  } catch (error) {
    const stdout = error.stdout?.toString();
    const stderr = error.stderr?.toString();
    if (stdout) process.stderr.write(stdout);
    if (stderr) process.stderr.write(stderr);
    throw error;
  }
}

const packed = JSON.parse(run("npm", ["pack", "--json", "--pack-destination", fixtureDir], packageDir))[0];
const tarball = join(fixtureDir, packed.filename);
run(
  "npm",
  ["install", "--offline", "--ignore-scripts", "--no-audit", "--no-fund", "--package-lock=false", tarball],
  fixtureDir,
);

writeFileSync(
  join(fixtureDir, "esm.mts"),
  `import { AppwireClient, APPWIRE_PROTOCOL_VERSION, WireError } from "@evener/appwire-client";
const client: AppwireClient = new AppwireClient({ url: "ws://127.0.0.1:1/rpc" });
const version: string = APPWIRE_PROTOCOL_VERSION;
const error: WireError | undefined = undefined;
void client; void version; void error;
`,
);
writeFileSync(
  join(fixtureDir, "commonjs.cts"),
  `import client = require("@evener/appwire-client");
const app: client.AppwireClient = new client.AppwireClient({ url: "ws://127.0.0.1:1/rpc" });
const version: string = client.APPWIRE_PROTOCOL_VERSION;
void app; void version;
`,
);
const tsc = resolve(packageDir, "node_modules/.bin/tsc");
run(
  tsc,
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
  fixtureDir,
);

writeFileSync(
  join(fixtureDir, "esm-runtime.mjs"),
  `import { AppwireClient, APPWIRE_PROTOCOL_VERSION } from "@evener/appwire-client";
if (typeof AppwireClient !== "function" || typeof APPWIRE_PROTOCOL_VERSION !== "string") process.exit(1);
`,
);
writeFileSync(
  join(fixtureDir, "commonjs-runtime.cjs"),
  `const { AppwireClient, APPWIRE_PROTOCOL_VERSION } = require("@evener/appwire-client");
if (typeof AppwireClient !== "function" || typeof APPWIRE_PROTOCOL_VERSION !== "string") process.exit(1);
`,
);
run(process.execPath, [join(fixtureDir, "esm-runtime.mjs")], fixtureDir);
run(process.execPath, [join(fixtureDir, "commonjs-runtime.cjs")], fixtureDir);

run(
  process.execPath,
  [
    "--test",
    join(fixtureDir, "node_modules/@evener/appwire-client/examples/streaming-rejoin.contract.mjs"),
    join(fixtureDir, "node_modules/@evener/appwire-client/examples/preferences.contract.mjs"),
    join(fixtureDir, "node_modules/@evener/appwire-client/examples/organization.contract.mjs"),
  ],
  fixtureDir,
);

const listing = run("tar", ["-tzf", tarball], fixtureDir);
for (const expected of [
  "package/dist/index.js",
  "package/dist/index.d.ts",
  "package/README.md",
  "package/examples/connection.mjs",
]) {
  assert(listing.includes(`${expected}\n`), `packed package is missing ${expected}`);
}
for (const forbidden of ["package/client.ts", "package/src/", "package/node_modules/"]) {
  assert(!listing.includes(forbidden), `packed package contains ${forbidden}`);
}

console.log(`qualified ${packed.name}@${packed.version} outside ${packageDir}`);
completed = true;
