#!/usr/bin/env node
// preflight repairs the environment hub-mcp needs before tsc runs, the same
// job web-preflight.sh does for the frontend:
//
//  1. hub-mcp's own node_modules — installed from the committed lockfile when
//     missing or older than it.
//  2. @evener/appwire-client's dist/ — built from the sibling checkout when
//     missing or older than its sources, because hub-mcp consumes the SDK the
//     way an installed consumer does (through the package's exports map, not
//     the sources).
//
// It refuses — loudly, naming the fix — the one hazard AGENTS.md calls out for
// every npm install in this repo: a node_modules that is a symlink to a shared
// install. npm ci (and any install) through such a link empties the shared
// install for every other worktree using it, so a symlink is never repaired
// here; the operator refreshes the shared install or gives this checkout a
// real node_modules of its own.

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const packageRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const sdkRoot = path.resolve(packageRoot, "..", "appwire-client", "typescript");

function isSymlink(p) {
  try {
    return fs.lstatSync(p).isSymbolicLink();
  } catch {
    return false;
  }
}

function exists(p) {
  return fs.existsSync(p);
}

// newestMtime walks dir recursively and returns the newest mtime among files
// whose name ends with one of extensions, or 0 when nothing matches.
function newestMtime(dir, extensions) {
  let newest = 0;
  const walk = (d) => {
    let entries;
    try {
      entries = fs.readdirSync(d, { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries) {
      const full = path.join(d, entry.name);
      if (entry.isDirectory()) {
        if (entry.name === "node_modules" || entry.name === "dist") continue;
        walk(full);
      } else if (extensions.some((ext) => entry.name.endsWith(ext))) {
        try {
          const mtime = fs.statSync(full).mtimeMs;
          if (mtime > newest) newest = mtime;
        } catch {
          /* raced away; the next check decides */
        }
      }
    }
  };
  walk(dir);
  return newest;
}

function log(message) {
  process.stdout.write(`hub-mcp preflight: ${message}\n`);
}

function die(message) {
  process.stderr.write(`hub-mcp preflight: ${message}\n`);
  process.exit(1);
}

// ensureInstall repairs one package's node_modules from its committed
// lockfile. A symlinked node_modules is never repaired here (see header).
function ensureInstall(root, label) {
  const nodeModules = path.join(root, "node_modules");
  const lockfile = path.join(root, "package-lock.json");
  if (isSymlink(nodeModules)) {
    die(
      `${label} node_modules is a symlink to a shared install (${fs.readlinkSync(nodeModules)}). ` +
        `Installing through it would empty that shared install for every other checkout using it. ` +
        `Refresh the shared install where it lives, or remove the link and give this checkout a real node_modules.`,
    );
  }
  if (!exists(lockfile)) {
    die(`${label} has no package-lock.json; run npm install there once and commit the lockfile.`);
  }
  const lockMtime = fs.statSync(lockfile).mtimeMs;
  const installedMtime = exists(nodeModules) ? fs.statSync(nodeModules).mtimeMs : 0;
  if (exists(nodeModules) && installedMtime >= lockMtime) return;
  log(`${label}: installing from package-lock.json`);
  execFileSync("npm", ["ci", "--no-audit", "--no-fund"], { cwd: root, stdio: "inherit" });
}

// ensureSdkDist builds the SDK's dist when missing or stale. hub-mcp resolves
// @evener/appwire-client through the package's exports map, which points at
// dist/, so the dist must exist and be at least as new as the sources.
function ensureSdkDist() {
  if (!exists(path.join(sdkRoot, "package.json"))) {
    die(`no package.json at ${sdkRoot}; is hub-mcp still a sibling of appwire-client/typescript?`);
  }
  ensureInstall(sdkRoot, "appwire-client/typescript");
  const distIndex = path.join(sdkRoot, "dist", "index.js");
  const sources = newestMtime(sdkRoot, [".ts"]);
  if (exists(distIndex) && fs.statSync(distIndex).mtimeMs >= sources) return;
  log("appwire-client/typescript: building dist (stale or missing)");
  execFileSync("npm", ["run", "build"], { cwd: sdkRoot, stdio: "inherit" });
  if (!exists(distIndex)) {
    die("appwire-client/typescript build finished without dist/index.js");
  }
}

ensureInstall(packageRoot, "hub-mcp");
ensureSdkDist();
log("environment ready");
