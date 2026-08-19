import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { spawn } from "node:child_process";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test } from "node:test";

import { createBrowserProcessCleanup } from "./browserGuardProcess.mjs";

// Issue #119: Chrome spawns `chrome_crashpad_handler` in its OWN process
// group (POSIX_SPAWN_SETEXGROUP on macOS), so the browser guard's
// `process.kill(-chromePgid)` group teardown never reaches it. The guard's
// `waitForProfileProcessExit` (browserGuardProcess.mjs:297-357) then only
// POLLS the orphan for 2s and rejects, never sending it a signal — so the
// helper escapes cleanup and the private profile is retained.
//
// This is a RED test: it stands up the real shape (a "chrome" process in its
// own process group that spawns a "crashpad helper" grandchild in a SEPARATE
// process group) and runs the guard's real cleanup against real process
// groups. The guard must reap the orphan; today it does not, so this fails.

function isProcessAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    if (error.code === "ESRCH") return false;
    if (error.code === "EPERM") return true;
    throw error;
  }
}

function isProcessGroupRunning(pgid) {
  try {
    process.kill(-pgid, 0);
    return true;
  } catch (error) {
    if (error.code === "ESRCH") return false;
    if (error.code === "EPERM") return true;
    throw error;
  }
}

function signalProcessGroup(pgid, signal) {
  try {
    process.kill(-pgid, signal);
    return true;
  } catch (error) {
    if (error.code === "ESRCH") return false;
    throw error;
  }
}

function readPgid(pid) {
  try {
    return Number(
      execFileSync("/bin/ps", ["-p", String(pid), "-o", "pgid="], { encoding: "utf8" }).trim(),
    );
  } catch {
    return null;
  }
}

// The "chrome" stand-in: a node process (its own process group via detached:
// true) that spawns a "crashpad helper" grandchild in its OWN process group
// (detached: true -> setsid, the Node analog of POSIX_SPAWN_SETEXGROUP),
// records the grandchild's pid, and stays alive until it is signaled. This
// is the structure Chrome uses for chrome_crashpad_handler.
const CHROME_STAND_IN = `
import { spawn } from "node:child_process";
import { writeFileSync } from "node:fs";

const grandchild = spawn("/bin/sleep", ["300"], { detached: true, stdio: "ignore" });
grandchild.unref();
writeFileSync(process.argv[2], String(grandchild.pid));

process.on("SIGTERM", () => process.exit(0));
process.on("SIGINT", () => process.exit(0));
`;

async function waitForFile(file, { timeoutMs = 5_000 } = {}) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const value = Number(readFileSync(file, "utf8").trim());
      if (value > 0) return value;
    } catch {
      // not yet written
    }
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  return null;
}

async function waitForExit(pid, { timeoutMs = 2_000 } = {}) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (!isProcessAlive(pid)) return true;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  return !isProcessAlive(pid);
}

test("cleanup reaps a Chrome helper spawned in its own process group (issue #119)", async (context) => {
  const workDir = mkdtempSync(path.join(tmpdir(), "browser-guard-crashpad-leak-"));
  context.after(() => rmSync(workDir, { recursive: true, force: true }));
  const profileDir = mkdtempSync(path.join(tmpdir(), "browser-guard-crashpad-leak-profile-"));
  context.after(() => rmSync(profileDir, { recursive: true, force: true }));

  const standInPath = path.join(workDir, "chrome-stand-in.mjs");
  const grandchildPidFile = path.join(workDir, "grandchild.pid");
  writeFileSync(standInPath, CHROME_STAND_IN);

  // Spawn "chrome" as its own process group (matches startBrowserGuard's
  // useProcessGroups on non-win32: detached: true -> chrome.pid == chrome.pgid).
  const chrome = spawn(process.execPath, [standInPath, grandchildPidFile], {
    detached: true,
    stdio: "ignore",
  });
  chrome.unref();
  context.after(() => {
    try {
      process.kill(-chrome.pid, "SIGKILL");
    } catch {
      // already gone
    }
  });

  const chromePgid = chrome.pid;
  const grandchildPid = await waitForFile(grandchildPidFile);
  assert.ok(grandchildPid > 0, "chrome stand-in never spawned the crashpad grandchild");
  context.after(() => {
    try {
      process.kill(grandchildPid, "SIGKILL");
    } catch {
      // already gone
    }
  });

  // Sanity: the grandchild is alive and in a DIFFERENT process group than
  // chrome — this is the POSIX_SPAWN_SETEXGROUP condition the bug is about.
  // If the grandchild were in chrome's group, the existing teardown would
  // already reach it via `process.kill(-chromePgid, SIGTERM)`.
  assert.equal(isProcessAlive(grandchildPid), true, "grandchild should be alive before cleanup");
  const grandchildPgid = readPgid(grandchildPid);
  assert.ok(
    grandchildPgid !== null && grandchildPgid !== chromePgid,
    `grandchild (pgid=${grandchildPgid}) must be in a different process group than chrome (pgid=${chromePgid})`,
  );

  // Fake processTarget so the guard does not register signal handlers on the
  // real test-runner process.
  const processTarget = new EventEmitter();
  processTarget.exit = () => {};

  const lifecycle = createBrowserProcessCleanup({
    profileDir,
    processTarget,
    processGroupRunning: isProcessGroupRunning,
    signalProcessGroup,
    scheduleGroupCheck: setImmediate,
    cancelGroupCheck: clearImmediate,
    // The guard discovers the escaped helper via `ps` in production; inject
    // that discovery so the test points at the real grandchild we spawned.
    findProfileProcesses: () => [{ pid: grandchildPid }],
    profileProcessRunning: (identity) => isProcessAlive(identity.pid),
    scheduleProfileProcessCheck: setImmediate,
    cancelProfileProcessCheck: clearImmediate,
  });
  lifecycle.addChild(chrome, {
    processGroupId: chromePgid,
    // No graceful CDP close for the stand-in; go straight to signaling the
    // chrome process group (the path that misses the separate-pgroup helper).
    gracefulClose: null,
  });

  // The guard's cleanup must reap the orphaned helper. Today it does not:
  // `process.kill(-chromePgid)` misses the separate-pgroup grandchild, and
  // `waitForProfileProcessExit` only polls then rejects without signaling.
  let cleanupError = null;
  try {
    await lifecycle.cleanup();
  } catch (error) {
    cleanupError = error;
  }

  // Give the kernel a moment to reap any signal that a fixed cleanup would
  // deliver, then require the orphan to be gone.
  const reaped = await waitForExit(grandchildPid, { timeoutMs: 1_000 });
  assert.equal(
    reaped,
    true,
    `crashpad helper ${grandchildPid} (separate process group ${grandchildPgid}, chrome pgid ${chromePgid}) escaped cleanup; cleanupError=${cleanupError?.message ?? "none"}`,
  );
});
