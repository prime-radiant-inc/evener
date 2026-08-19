import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { EventEmitter } from "node:events";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test } from "node:test";

import { createBrowserProcessCleanup, findMacOSProfileProcesses } from "./browserGuardProcess.mjs";

// Issue #119: Chrome's Crashpad handler escapes the browser guard's group
// teardown. Measured topology on real macOS Chrome (PR #240 review, 4/4
// launches): Crashpad DOUBLE-SPAWNS. An intermediate process becomes a new
// process-group leader, spawns `chrome_crashpad_handler` into that group, and
// exits. The surviving handler therefore has:
//   - pgid == the DEAD intermediate's pid (never the handler's own pid)
//   - ppid == 1 (orphaned to launchd)
//   - a group that is neither Chrome's nor its own leadership
// So `process.kill(-chromePgid)` never reaches it, and `kill(-handlerPid)` is
// always ESRCH. The only signal that reliably lands is `kill(-pgid)` with the
// pgid captured at discovery time.
//
// This test stands up that TRUE topology with real processes and real process
// groups, runs the guard's real cleanup with the REAL default
// signalProfileProcess, and requires the orphan dead and an out-of-scope decoy
// alive. Against the pre-fix code (`kill(-handlerPid)`) this test fails: the
// ESRCH is swallowed as "already gone" and the helper survives.

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
    return Number(execFileSync("/bin/ps", ["-p", String(pid), "-o", "pgid="], { encoding: "utf8" }).trim());
  } catch {
    return null;
  }
}

// The "chrome" stand-in reproduces Crashpad's double-spawn: it spawns an
// INTERMEDIATE (detached: true -> new group leader), the intermediate spawns
// the "helper" (/bin/sleep) WITHOUT detaching (so the helper inherits the
// intermediate's group), records both pids, and exits immediately. The helper
// survives with pgid == the dead intermediate's pid — the real handler's
// shape. "chrome" itself stays alive until signaled, like the browser.
const CHROME_STAND_IN = `
import { spawn } from "node:child_process";

const intermediate = spawn(process.execPath, [process.argv[2]], { detached: true, stdio: "ignore" });
intermediate.unref();

process.on("SIGTERM", () => process.exit(0));
process.on("SIGINT", () => process.exit(0));
setInterval(() => {}, 1000);
`;

const INTERMEDIATE_STAND_IN = `
import { spawn } from "node:child_process";
import { writeFileSync } from "node:fs";

// NOT detached: the helper inherits THIS process's group, and this process
// (the group leader) exits immediately — the Crashpad double-spawn.
const helper = spawn("/bin/sleep", ["300"], { stdio: "ignore" });
helper.unref();
writeFileSync(process.env.HELPER_PID_FILE, JSON.stringify({ helperPid: helper.pid, intermediatePid: process.pid }));
process.exit(0);
`;

async function waitForFile(file, { timeoutMs = 5_000 } = {}) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const value = JSON.parse(readFileSync(file, "utf8"));
      if (value.helperPid > 0 && value.intermediatePid > 0) return value;
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

test("cleanup reaps a Crashpad helper with the real double-spawn topology (issue #119)", async (context) => {
  const workDir = mkdtempSync(path.join(tmpdir(), "browser-guard-crashpad-leak-"));
  context.after(() => rmSync(workDir, { recursive: true, force: true }));
  const profileDir = mkdtempSync(path.join(tmpdir(), "browser-guard-crashpad-leak-profile-"));
  context.after(() => rmSync(profileDir, { recursive: true, force: true }));

  const chromeStandInPath = path.join(workDir, "chrome-stand-in.mjs");
  const intermediateStandInPath = path.join(workDir, "intermediate-stand-in.mjs");
  const helperPidFile = path.join(workDir, "helper.pid");
  writeFileSync(chromeStandInPath, CHROME_STAND_IN);
  writeFileSync(intermediateStandInPath, INTERMEDIATE_STAND_IN);

  // A decoy in its own process group, outside the profileDir scope: cleanup
  // must not signal it. It stands in for "whatever else is running on the
  // machine" — the reason the kill must target only the pgid captured for
  // this run's discovered helper.
  const decoy = spawn("/bin/sleep", ["300"], { detached: true, stdio: "ignore" });
  decoy.unref();
  context.after(() => {
    try {
      process.kill(-decoy.pid, "SIGKILL");
    } catch {
      // already gone
    }
  });

  // Spawn "chrome" as its own process group (matches startBrowserGuard's
  // useProcessGroups on non-win32: detached: true -> chrome.pid == chrome.pgid).
  const chrome = spawn(process.execPath, [chromeStandInPath, intermediateStandInPath], {
    detached: true,
    stdio: "ignore",
    env: { ...process.env, HELPER_PID_FILE: helperPidFile },
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
  const spawned = await waitForFile(helperPidFile);
  assert.ok(spawned, "chrome stand-in never spawned the crashpad helper");
  const { helperPid, intermediatePid } = spawned;
  context.after(() => {
    try {
      process.kill(helperPid, "SIGKILL");
    } catch {
      // already gone
    }
  });

  // Sanity: this is the measured Crashpad topology. The helper is alive, its
  // group leader (the intermediate) is dead, and its pgid is neither its own
  // pid nor chrome's group — so neither `kill(-chromePgid)` nor
  // `kill(-helperPid)` can reach it.
  assert.equal(isProcessAlive(helperPid), true, "helper should be alive before cleanup");
  assert.equal(await waitForExit(intermediatePid), true, "the intermediate group leader must be dead");
  const helperPgid = readPgid(helperPid);
  assert.equal(
    helperPgid,
    intermediatePid,
    `helper (pgid=${helperPgid}) must sit in the dead intermediate's group (${intermediatePid})`,
  );
  assert.notEqual(helperPgid, helperPid, "helper must not lead its own group");
  assert.notEqual(helperPgid, chromePgid, "helper must not be in chrome's group");

  // The decoy must be invisible to the real discovery: it is not a
  // chrome_crashpad_handler and carries no --database under this profileDir.
  if (process.platform === "darwin") {
    const discovered = findMacOSProfileProcesses(profileDir);
    assert.ok(
      !discovered.some((identity) => identity.pid === decoy.pid),
      "real discovery must not match the out-of-scope decoy",
    );
  }

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
    // that discovery so the test points at the real helper we spawned,
    // carrying the pgid the way findMacOSProfileProcesses now does. The
    // SIGNALING path is NOT injected: the real signalProfileProcess must kill
    // this real topology.
    findProfileProcesses: () => [{ pid: helperPid, pgid: helperPgid }],
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

  let cleanupError = null;
  try {
    await lifecycle.cleanup();
  } catch (error) {
    cleanupError = error;
  }

  // Give the kernel a moment to deliver the signal, then require the orphan
  // dead and the out-of-scope decoy untouched.
  const reaped = await waitForExit(helperPid, { timeoutMs: 1_000 });
  assert.equal(
    reaped,
    true,
    `crashpad helper ${helperPid} (pgid ${helperPgid}, dead leader ${intermediatePid}, chrome pgid ${chromePgid}) escaped cleanup; cleanupError=${cleanupError?.message ?? "none"}`,
  );
  assert.equal(cleanupError, null, `cleanup must succeed once the helper is reaped: ${cleanupError?.message}`);
  assert.equal(isProcessAlive(decoy.pid), true, "the out-of-scope decoy must survive cleanup");
});
