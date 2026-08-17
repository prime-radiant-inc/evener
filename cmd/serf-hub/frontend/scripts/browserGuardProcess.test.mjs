import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { EventEmitter } from "node:events";
import { existsSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test } from "node:test";

import { waitForHttp } from "./browserGuardCdp.mjs";
import * as browserGuardProcess from "./browserGuardProcess.mjs";

const { createBrowserProcessCleanup, findAvailablePort, startBrowserGuard } = browserGuardProcess;

class FakeChild extends EventEmitter {
  constructor(command, args = [], options = {}) {
    super();
    this.command = command;
    this.args = args;
    this.options = options;
    this.exitCode = null;
    this.signalCode = null;
    this.signals = [];
    this.stderr = new EventEmitter();
  }

  kill(signal) {
    this.signals.push(signal);
    return this.launchFailed !== true;
  }

  exit(signal = "SIGTERM") {
    this.signalCode = signal;
    this.emit("exit", null, signal);
  }

  // Model what Node does when spawn() itself fails, measured against a real
  // non-executable binary on this platform: the ChildProcess is returned with
  // no pid, then asynchronously sets exitCode to the negative errno, emits
  // "error" (which THROWS if nothing is listening), never emits "exit", and
  // answers false from kill() forever after.
  failLaunch(error) {
    this.launchFailed = true;
    this.exitCode = -(error.errno ?? 13);
    this.emit("error", error);
  }
}

function launchError(code, message) {
  return Object.assign(new Error(message), { code, errno: code === "ENOENT" ? 2 : 13, syscall: "spawn" });
}

/**
 * Register a guard's cleanup as TEARDOWN rather than leaving it as trailing
 * code at the end of a test body.
 *
 * A failing assertion skips everything after it, so a body-tail cleanup leaks
 * exactly what these tests exist to prove gets reaped: a detached process group
 * and a private profile directory. Leaking that from the launch-failure tests
 * would be its own joke. (Measured: forcing one assertion in the real-spawn
 * test to fail took the tmp profile count from 8 to 9, and a run of stale
 * browser-guard-test-* directories is what put this here.)
 *
 * FakeChild never exits on its own, so teardown has to release the children the
 * same way each success path does. Both halves are safe to repeat: cleanup()
 * memoizes its promise, and a second exit() emits to listeners that have already
 * been removed - so this is a no-op after a body that cleaned up for itself.
 */
function reapOnTeardown(context, guard, children = []) {
  context.after(async () => {
    const cleanup = guard.cleanup();
    for (const child of children) child.exit();
    await cleanup;
  });
}

async function startFakeGuard(options = {}) {
  const children = [];
  const guard = await startBrowserGuard({
    frontend: process.cwd(),
    profilePrefix: "browser-guard-test-",
    chromeBinary: "/fake/chrome",
    closeBrowser() {
      throw new Error("fake Chrome has no CDP endpoint");
    },
    spawnProcess(command, args, spawnOptions) {
      const child = new FakeChild(command, args, spawnOptions);
      children.push(child);
      return child;
    },
    ...options,
  });
  return { guard, children };
}

test("allocates distinct local ports", async () => {
  const first = await findAvailablePort();
  const second = await findAvailablePort([first]);
  assert.notEqual(first, second);
  assert.ok(first > 0);
  assert.ok(second > 0);
});

test("disables Chrome crash reporting outside the private browser profile", async () => {
  const { guard, children } = await startFakeGuard();
  try {
    assert.equal(children[1].command, "/fake/chrome");
    assert.ok(children[1].args.includes("--disable-crash-reporter"));
  } finally {
    const cleanup = guard.cleanup();
    for (const child of children) child.exit();
    await cleanup;
  }
});

test("uses a mock keychain for a fresh macOS browser profile", async () => {
  const { guard, children } = await startFakeGuard({ platform: "darwin" });
  try {
    assert.equal(children[1].command, "/fake/chrome");
    assert.ok(children[1].args.includes("--use-mock-keychain"));
  } finally {
    const cleanup = guard.cleanup();
    for (const child of children) child.exit();
    await cleanup;
  }
});

test("stores Chrome crash metadata inside the private browser profile", async () => {
  const { guard, children } = await startFakeGuard();
  try {
    assert.equal(children[1].options.env?.BREAKPAD_DUMP_LOCATION, path.join(guard.profileDir, "Crashpad"));
  } finally {
    const cleanup = guard.cleanup();
    for (const child of children) child.exit();
    await cleanup;
  }
});

test("lets Chrome exit after graceful close before falling back to a signal", async () => {
  let finishClose;
  const closePending = new Promise((resolve) => {
    finishClose = resolve;
  });
  const { guard, children } = await startFakeGuard({
    closeBrowser: () => closePending,
  });

  const cleanup = guard.cleanup();
  assert.deepEqual(children[0].signals, ["SIGTERM"]);
  assert.deepEqual(children[1].signals, []);
  assert.equal(existsSync(guard.profileDir), true);

  finishClose();
  await Promise.resolve();
  await Promise.resolve();
  assert.deepEqual(children[1].signals, []);

  for (const child of children) child.exit();
  await cleanup;
  assert.equal(existsSync(guard.profileDir), false);
});

test("falls back from a pending graceful close through TERM and KILL", async () => {
  const scheduled = [];
  const { guard, children } = await startFakeGuard({
    closeBrowser: () => new Promise(() => {}),
    scheduleEscalation(callback) {
      const task = { callback, cancelled: false };
      scheduled.push(task);
      return task;
    },
    cancelEscalation(task) {
      task.cancelled = true;
    },
  });

  const cleanup = guard.cleanup();
  children[0].exit();
  assert.deepEqual(children[1].signals, []);
  assert.equal(scheduled.length, 2);

  scheduled[1].callback();
  assert.deepEqual(children[1].signals, ["SIGTERM"]);
  assert.equal(scheduled.length, 3);
  scheduled[2].callback();
  assert.deepEqual(children[1].signals, ["SIGTERM", "SIGKILL"]);

  children[1].exit("SIGKILL");
  await cleanup;
  assert.equal(existsSync(guard.profileDir), false);
});

test("falls back to TERM when graceful Chrome close fails", async () => {
  const events = [];
  const { guard, children } = await startFakeGuard({
    closeBrowser() {
      events.push("close");
      return Promise.reject(new Error("CDP unavailable"));
    },
  });
  children[1].kill = (signal) => {
    events.push(signal);
    children[1].signals.push(signal);
    return true;
  };

  const cleanup = guard.cleanup();
  await Promise.resolve();
  await Promise.resolve();
  assert.deepEqual(events, ["close", "SIGTERM"]);

  for (const child of children) child.exit();
  await cleanup;
});

test("keeps the profile until a detached browser process group disappears", async () => {
  const profileDir = mkdtempSync(path.join(tmpdir(), "browser-group-test-"));
  const groupChecks = [];
  let groupRunning = true;
  const child = new FakeChild("/fake/chrome");
  child.pid = 4242;
  const lifecycle = createBrowserProcessCleanup({
    profileDir,
    processGroupRunning: () => groupRunning,
    signalProcessGroup() {
      return true;
    },
    scheduleGroupCheck(callback) {
      groupChecks.push(callback);
      return callback;
    },
    cancelGroupCheck() {},
  });
  lifecycle.addChild(child, {
    processGroupId: child.pid,
    gracefulClose: () => Promise.resolve(),
  });

  const cleanup = lifecycle.cleanup();
  child.exit();
  await Promise.resolve();
  assert.equal(existsSync(profileDir), true);

  groupRunning = false;
  groupChecks.at(-1)();
  await cleanup;
  assert.equal(existsSync(profileDir), false);
});

test("keeps the profile until a captured escaped Chrome helper exits", async (context) => {
  const profileDir = mkdtempSync(path.join(tmpdir(), "browser-helper-test-"));
  context.after(() => rmSync(profileDir, { recursive: true, force: true }));
  const helperChecks = [];
  let announceHelperCheck;
  const helperCheckScheduled = new Promise((resolve) => {
    announceHelperCheck = resolve;
  });
  let helperRunning = true;
  const child = new FakeChild("/fake/chrome");
  child.exit();
  const lifecycle = createBrowserProcessCleanup({
    profileDir,
    findProfileProcesses: () => [{ pid: 4444 }],
    profileProcessRunning: () => helperRunning,
    scheduleProfileProcessCheck(callback) {
      helperChecks.push(callback);
      announceHelperCheck();
      return callback;
    },
    cancelProfileProcessCheck() {},
  });
  lifecycle.addChild(child);

  const cleanup = lifecycle.cleanup();
  await helperCheckScheduled;
  assert.equal(existsSync(profileDir), true);
  assert.equal(helperChecks.length, 1);

  helperRunning = false;
  helperChecks.at(-1)();
  await cleanup;
  assert.equal(existsSync(profileDir), false);
});

test("retains the profile when an escaped Chrome helper outlives cleanup", async (context) => {
  const profileDir = mkdtempSync(path.join(tmpdir(), "browser-helper-timeout-test-"));
  context.after(() => rmSync(profileDir, { recursive: true, force: true }));
  const deadlines = [];
  let announceHelperCheck;
  const helperCheckScheduled = new Promise((resolve) => {
    announceHelperCheck = resolve;
  });
  const child = new FakeChild("/fake/chrome");
  child.exit();
  const lifecycle = createBrowserProcessCleanup({
    profileDir,
    findProfileProcesses: () => [{ pid: 4555 }],
    profileProcessRunning: () => true,
    scheduleEscalation(callback) {
      const deadline = { callback, cancelled: false };
      deadlines.push(deadline);
      return deadline;
    },
    cancelEscalation(deadline) {
      deadline.cancelled = true;
    },
    scheduleProfileProcessCheck(callback) {
      announceHelperCheck();
      return callback;
    },
    cancelProfileProcessCheck() {},
  });
  lifecycle.addChild(child);

  const cleanup = lifecycle.cleanup();
  await helperCheckScheduled;
  deadlines[0].callback();

  await assert.rejects(cleanup, /escaped Chrome helper 4555.*retained/);
  assert.equal(existsSync(profileDir), true);
});

test("reports a retained-profile cleanup failure before interrupted exit", async (context) => {
  const profileDir = mkdtempSync(path.join(tmpdir(), "browser-helper-signal-timeout-test-"));
  context.after(() => rmSync(profileDir, { recursive: true, force: true }));
  const processTarget = new EventEmitter();
  const deadlines = [];
  const events = [];
  let announceHelperCheck;
  const helperCheckScheduled = new Promise((resolve) => {
    announceHelperCheck = resolve;
  });
  processTarget.exit = (code) => events.push({ exit: code });
  const child = new FakeChild("/fake/chrome");
  child.exit();
  const lifecycle = createBrowserProcessCleanup({
    profileDir,
    processTarget,
    findProfileProcesses: () => [{ pid: 4566 }],
    profileProcessRunning: () => true,
    reportCleanupFailure: (error) => events.push({ diagnostic: error.message }),
    scheduleEscalation(callback) {
      deadlines.push(callback);
      return callback;
    },
    cancelEscalation() {},
    scheduleProfileProcessCheck(callback) {
      announceHelperCheck();
      return callback;
    },
    cancelProfileProcessCheck() {},
  });
  lifecycle.addChild(child);

  processTarget.emit("SIGTERM");
  await helperCheckScheduled;
  deadlines[0]();
  await assert.rejects(lifecycle.cleanup(), /escaped Chrome helper 4566/);
  await Promise.resolve();

  assert.deepEqual(events, [
    { diagnostic: `escaped Chrome helper 4566 did not exit; private profile retained at ${profileDir}` },
    { exit: 143 },
  ]);
  assert.equal(existsSync(profileDir), true);
});

test("captures a Chrome helper that escapes while the browser group closes", async (context) => {
  const profileDir = mkdtempSync(path.join(tmpdir(), "browser-late-helper-test-"));
  context.after(() => rmSync(profileDir, { recursive: true, force: true }));
  const helperChecks = [];
  let findCalls = 0;
  let announceRescan;
  const rescanned = new Promise((resolve) => {
    announceRescan = resolve;
  });
  let helperRunning = true;
  const child = new FakeChild("/fake/chrome");
  child.exit();
  const lifecycle = createBrowserProcessCleanup({
    profileDir,
    findProfileProcesses() {
      findCalls++;
      if (findCalls === 1) return [];
      announceRescan();
      return [{ pid: 4666, databaseArg: "--database=/owned/Crashpad" }];
    },
    profileProcessRunning: () => helperRunning,
    scheduleProfileProcessCheck(callback) {
      helperChecks.push(callback);
      return callback;
    },
    cancelProfileProcessCheck() {},
  });
  lifecycle.addChild(child);

  const cleanup = lifecycle.cleanup();
  const firstCompletion = await Promise.race([rescanned.then(() => "rescan"), cleanup.then(() => "cleanup")]);
  assert.equal(firstCompletion, "rescan");
  assert.equal(findCalls, 2);
  assert.equal(existsSync(profileDir), true);

  helperRunning = false;
  helperChecks.at(-1)();
  await cleanup;
  assert.equal(existsSync(profileDir), false);
});

test("discovers only the Crashpad helper for the exact canonical private profile", (context) => {
  assert.equal(typeof browserGuardProcess.findMacOSProfileProcesses, "function");
  const profileDir = mkdtempSync(path.join(tmpdir(), "browser-helper-discovery-test-"));
  context.after(() => rmSync(profileDir, { recursive: true, force: true }));
  const crashpadDir = path.join(realpathSync(profileDir), "Crashpad");
  const exactDatabase = `--database=${crashpadDir}`;
  const processList = [
    `  510 /Applications/Google Chrome/chrome_crashpad_handler --monitor-self ${exactDatabase} --url=`,
    `  511 /Applications/Google Chrome/chrome_crashpad_handler --database=${crashpadDir}-neighbor --url=`,
    `  512 /Applications/Google Chrome/chrome_crashpad_handler --database=${crashpadDir}/nested --url=`,
    `  513 /usr/bin/unrelated ${exactDatabase} --url=`,
  ].join("\n");

  assert.deepEqual(
    browserGuardProcess.findMacOSProfileProcesses(profileDir, {
      platform: "darwin",
      listProcesses: () => processList,
    }),
    [{ pid: 510, databaseArg: exactDatabase }],
  );
});

test("does not treat an unrelated process that reused a captured Crashpad PID as owned", () => {
  assert.equal(typeof browserGuardProcess.profileProcessIdentityRunning, "function");
  const identity = {
    pid: 510,
    databaseArg: "--database=/private/tmp/browser-profile/Crashpad",
  };
  const reusedProcess =
    "  510 /Applications/Google Chrome/chrome_crashpad_handler --database=/private/tmp/other-profile/Crashpad";
  const options = {
    platform: "darwin",
    listProcesses: () => reusedProcess,
  };

  assert.equal(browserGuardProcess.profileProcessIdentityRunning(identity, options), false);
});

test("targets a lingering browser process group with TERM then KILL", async () => {
  const profileDir = mkdtempSync(path.join(tmpdir(), "browser-group-test-"));
  const groupChecks = [];
  const escalations = [];
  const signals = [];
  let groupRunning = true;
  const child = new FakeChild("/fake/chrome");
  child.pid = 4343;
  const lifecycle = createBrowserProcessCleanup({
    profileDir,
    processGroupRunning: () => groupRunning,
    signalProcessGroup(processGroupId, signal) {
      signals.push([processGroupId, signal]);
      return true;
    },
    scheduleGroupCheck(callback) {
      groupChecks.push(callback);
      return callback;
    },
    cancelGroupCheck() {},
    scheduleEscalation(callback) {
      escalations.push(callback);
      return callback;
    },
    cancelEscalation() {},
  });
  lifecycle.addChild(child, { processGroupId: child.pid });

  const cleanup = lifecycle.cleanup();
  assert.deepEqual(signals, [[4343, "SIGTERM"]]);
  escalations[0]();
  assert.deepEqual(signals, [
    [4343, "SIGTERM"],
    [4343, "SIGKILL"],
  ]);

  groupRunning = false;
  groupChecks.at(-1)();
  await cleanup;
  assert.equal(existsSync(profileDir), false);
});

test("cleans Vite when Chrome startup throws", async () => {
  const killed = [];
  let calls = 0;
  const spawnProcess = (command) => {
    calls++;
    if (calls === 1) {
      const child = new FakeChild(command);
      child.kill = (signal) => {
        killed.push([command, signal]);
        queueMicrotask(() => child.exit(signal));
        return true;
      };
      return child;
    }
    throw new Error("chrome startup failed");
  };

  await assert.rejects(
    startBrowserGuard({
      frontend: process.cwd(),
      profilePrefix: "browser-guard-test-",
      chromeBinary: "/fake/chrome",
      spawnProcess,
    }),
    /chrome startup failed/,
  );
  assert.deepEqual(killed, [["./node_modules/.bin/vite", "SIGTERM"]]);
});

test("waits for every child to exit before removing the browser profile", async () => {
  const { guard, children } = await startFakeGuard();
  const cleanup = guard.cleanup();

  assert.equal(existsSync(guard.profileDir), true);
  children[0].exit();
  await Promise.resolve();
  assert.equal(existsSync(guard.profileDir), true);
  children[1].exit();

  await cleanup;
  assert.equal(existsSync(guard.profileDir), false);
});

test("escalates a child that does not exit after SIGTERM", async () => {
  const escalations = [];
  const { guard, children } = await startFakeGuard({
    scheduleEscalation(callback) {
      escalations.push(callback);
      return callback;
    },
    cancelEscalation() {},
  });

  const cleanup = guard.cleanup();
  assert.deepEqual(
    children.map((child) => child.signals),
    [["SIGTERM"], ["SIGTERM"]],
  );
  for (const escalate of escalations) escalate();
  assert.deepEqual(
    children.map((child) => child.signals),
    [
      ["SIGTERM", "SIGKILL"],
      ["SIGTERM", "SIGKILL"],
    ],
  );
  for (const child of children) child.exit("SIGKILL");
  await cleanup;
});

test("cleanup is idempotent while child exit is pending", async () => {
  const { guard, children } = await startFakeGuard();

  const first = guard.cleanup();
  const second = guard.cleanup();
  assert.strictEqual(second, first);
  assert.deepEqual(
    children.map((child) => child.signals),
    [["SIGTERM"], ["SIGTERM"]],
  );

  for (const child of children) child.exit();
  await first;
});

for (const { signal, exitCode } of [
  { signal: "SIGINT", exitCode: 130 },
  { signal: "SIGTERM", exitCode: 143 },
]) {
  test(`${signal} awaits cleanup and removes temporary signal listeners`, async () => {
    const processTarget = new EventEmitter();
    const exits = [];
    let guard = null;
    processTarget.exit = (code) => {
      exits.push({ code, profilePresent: existsSync(guard.profileDir) });
    };
    const started = await startFakeGuard({ processTarget });
    guard = started.guard;
    const { children } = started;

    assert.equal(processTarget.listenerCount("SIGINT"), 1);
    assert.equal(processTarget.listenerCount("SIGTERM"), 1);
    processTarget.emit(signal);
    assert.deepEqual(
      children.map((child) => child.signals),
      [["SIGTERM"], ["SIGTERM"]],
    );
    assert.deepEqual(exits, []);

    for (const child of children) child.exit();
    await guard.cleanup();
    await Promise.resolve();

    assert.deepEqual(exits, [{ code: exitCode, profilePresent: false }]);
    assert.equal(processTarget.listenerCount("SIGINT"), 0);
    assert.equal(processTarget.listenerCount("SIGTERM"), 0);
  });
}

// The diagnostic 45adecf57 retired with layoutguard/cdp.mjs's
// probeBrowserCapability. Its claim was that "environment failure named as
// such, with diagnostics" survived in run.mjs's startup error; what survived
// was that phrase plus VITE's stderr. Chrome's own stderr was not available at
// all -- it was spawned with stdio "ignore" -- so a Chrome that will not start
// produced a 30-second waitForHttp timeout and an irrelevant Vite log (3htx).
test("the browser startup diagnostic names the binary, the argv and Chrome's own stderr", () => {
  const message = browserGuardProcess.describeBrowserStartupFailure({
    error: new Error("chrome never came up at http://127.0.0.1:9222/json/version"),
    chromeBinary: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    chromeArgv: ["--headless=new", "--remote-debugging-port=9222"],
    chromeStderr: "dyld: Library not loaded: @rpath/libfoo.dylib",
    viteStderr: "",
  });

  assert.match(message, /environment problem, not a test case failure/);
  assert.match(message, /chrome never came up/);
  assert.match(message, /Chrome binary: .*Google Chrome/);
  assert.match(message, /--remote-debugging-port=9222/);
  assert.match(message, /dyld: Library not loaded/);
  assert.match(message, /1\./);
  assert.match(message, /2\./);
  assert.match(message, /3\./);
});

test("the startup diagnostic says so explicitly when Chrome produced no stderr", () => {
  const message = browserGuardProcess.describeBrowserStartupFailure({
    error: new Error("chrome never came up"),
    chromeBinary: "/usr/bin/chromium",
    chromeArgv: ["--headless=new"],
    chromeStderr: "",
    viteStderr: "",
  });

  assert.match(message, /chrome stderr: \(none\)/);
});

// The behaviour the diagnostic commit actually changed was Chrome's stderr going
// from discarded to captured. That is the regression net; the formatter tests
// above only cover a pure function written in the same commit. FakeChild already
// exposes a stderr EventEmitter and startBrowserGuard already takes a
// spawnProcess injection, so this needs no browser.
test("chrome is spawned with a piped stderr and the guard surfaces what it wrote", async () => {
  const { guard, children } = await startFakeGuard();
  const chrome = children[1];

  assert.deepEqual(chrome.options.stdio, ["ignore", "ignore", "pipe"]);
  assert.equal(guard.chromeBinary, "/fake/chrome");
  assert.ok(guard.getChromeArgv().includes("--headless=new"));

  chrome.stderr.emit("data", "dyld: Library not loaded: @rpath/libfoo.dylib");
  assert.match(guard.getChromeError(), /dyld: Library not loaded/);

  const cleanup = guard.cleanup();
  for (const child of children) child.exit();
  await cleanup;
});

test("the diagnostic blames vite for a vite failure and does not send the reader after Chrome", () => {
  const message = browserGuardProcess.describeBrowserStartupFailure({
    error: new Error("vite dev server never came up"),
    subsystem: "vite",
    viteStderr: "Error: Port 5173 is already in use",
  });

  assert.match(message, /Port 5173 is already in use/);
  assert.match(message, /Chrome is not implicated/);
  assert.doesNotMatch(message, /install Chrome/);
});

test("a browser binary that could not be resolved says nothing was spawned", () => {
  const message = browserGuardProcess.describeBrowserStartupFailure({
    error: new Error("no Chrome/Chromium found (looked at: /a, /b)"),
    subsystem: "launch",
  });

  assert.match(message, /no Chrome\/Chromium found/);
  assert.match(message, /nothing was spawned and there is no stderr to read/);
});

// kata ssca. spawn() reports a failed LAUNCH asynchronously on the child's
// "error" event - EACCES for a Chrome that is not executable, ENOENT for one
// that vanished between findChrome() and the spawn - and returns a ChildProcess
// either way, so the try/catch around startBrowserGuard's spawns can never see
// it. Unhandled, that event throws out of the event loop: past
// describeBrowserStartupFailure and through lifecycle.cleanup(), which is what
// reaps the Vite server and the private profile. These three cases pin the
// listener, the attribution, and that cleanup still finishes.
//
// "The readiness wait" is waitForHttp, which polls for 30 seconds. A launch
// that already failed has nothing to poll FOR, so the wait aborts with the
// launch error itself rather than reporting a timeout against a subsystem that
// was never running.
const UNREACHABLE = "http://127.0.0.1:1/json/version";

test("a Chrome that never launched aborts the readiness wait naming the launch error", async (context) => {
  const { guard, children } = await startFakeGuard();
  reapOnTeardown(context, guard, children);
  const failure = launchError("EACCES", "spawn /fake/chrome EACCES");

  children[1].failLaunch(failure);

  await assert.rejects(
    waitForHttp(UNREACHABLE, "chrome devtools endpoint", guard.getChromeLaunchError),
    /spawn \/fake\/chrome EACCES/,
  );
  const message = browserGuardProcess.describeBrowserStartupFailure({
    error: guard.getChromeLaunchError(),
    subsystem: "chrome",
    chromeBinary: guard.chromeBinary,
    chromeArgv: guard.getChromeArgv(),
    chromeStderr: guard.getChromeError(),
    viteStderr: guard.getViteError(),
  });
  assert.match(message, /environment problem, not a test case failure/);
  assert.match(message, /spawn \/fake\/chrome EACCES/);

  // The failed child never exits on its own; cleanup has to finish anyway.
  const cleanup = guard.cleanup();
  children[0].exit();
  await cleanup;
  assert.equal(existsSync(guard.profileDir), false);
});

test("a Vite that never launched aborts its own wait and is not blamed on Chrome", async (context) => {
  const { guard, children } = await startFakeGuard();
  reapOnTeardown(context, guard, children);
  const failure = launchError("ENOENT", "spawn ./node_modules/.bin/vite ENOENT");

  children[0].failLaunch(failure);

  await assert.rejects(
    waitForHttp(UNREACHABLE, "vite dev server", guard.getViteLaunchError),
    /spawn \.\/node_modules\/\.bin\/vite ENOENT/,
  );
  assert.equal(guard.getChromeLaunchError(), null);
  const message = browserGuardProcess.describeBrowserStartupFailure({
    error: guard.getViteLaunchError(),
    subsystem: "vite",
    viteStderr: guard.getViteError(),
  });
  assert.match(message, /spawn \.\/node_modules\/\.bin\/vite ENOENT/);
  assert.match(message, /Chrome is not implicated/);

  const cleanup = guard.cleanup();
  children[1].exit();
  await cleanup;
  assert.equal(existsSync(guard.profileDir), false);
});

// FakeChild.failLaunch above is a MODEL of Node's failed-spawn behaviour, and a
// model that cannot happen in production is a green test that guards nothing.
// This runs the real thing: real spawn(), a real non-executable file for Chrome
// (the EACCES case the kata names), a real child process standing in for Vite,
// and real cleanup. No Vite boot and no browser, so it stays cheap.
test("a real non-executable Chrome is named by the diagnostic and still gets cleaned up", async (context) => {
  const binDir = mkdtempSync(path.join(tmpdir(), "browser-guard-noexec-"));
  context.after(() => rmSync(binDir, { recursive: true, force: true }));
  const notExecutable = path.join(binDir, "chrome");
  writeFileSync(notExecutable, "#!/bin/sh\nexit 0\n", { mode: 0o644 });

  let calls = 0;
  const viteStandIn = [];
  const { guard } = await startFakeGuard({
    chromeBinary: notExecutable,
    spawnProcess(command, args, options) {
      calls++;
      // Vite is stood in for by a real, harmless long-lived process: this test
      // is about Chrome's launch, and booting a dev server to prove it would
      // cost seconds and prove nothing extra.
      if (calls === 1) {
        const child = spawn("/bin/sleep", ["30"], options);
        viteStandIn.push(child);
        return child;
      }
      return spawn(command, args, options);
    },
  });
  // The children here are REAL - a detached `sleep` and a Chrome that never
  // launched - so a skipped cleanup leaks an actual process group, not a fake.
  reapOnTeardown(context, guard);

  // The 'error' event is asynchronous, so the wait is what observes it - and it
  // must observe it in well under the 30 seconds the poll would otherwise take.
  const started = Date.now();
  await assert.rejects(
    waitForHttp(
      `http://127.0.0.1:${guard.cdpPort}/json/version`,
      "chrome devtools endpoint",
      guard.getChromeLaunchError,
    ),
    /EACCES/,
  );
  assert.ok(Date.now() - started < 5_000, "the wait must abort on the launch error, not poll to its timeout");

  const message = browserGuardProcess.describeBrowserStartupFailure({
    error: guard.getChromeLaunchError(),
    subsystem: "chrome",
    chromeBinary: guard.chromeBinary,
    chromeArgv: guard.getChromeArgv(),
    chromeStderr: guard.getChromeError(),
    viteStderr: guard.getViteError(),
  });
  assert.match(message, /environment problem, not a test case failure/);
  assert.match(message, /EACCES/);
  assert.match(message, /Chrome binary: .*chrome/);

  await guard.cleanup();
  assert.equal(existsSync(guard.profileDir), false);
  assert.equal(viteStandIn[0].killed || viteStandIn[0].exitCode !== null || viteStandIn[0].signalCode !== null, true);
});

test("a Chrome launch failure does not abort the wait for a Vite that is still coming up", async (context) => {
  const { guard, children } = await startFakeGuard();
  reapOnTeardown(context, guard, children);
  children[1].failLaunch(launchError("EACCES", "spawn /fake/chrome EACCES"));

  // The vite wait polls its own subsystem only; blaming a live Vite for
  // Chrome's failure would send the reader after the wrong remediation.
  assert.equal(guard.getViteLaunchError(), null);

  const cleanup = guard.cleanup();
  children[0].exit();
  await cleanup;
  assert.equal(existsSync(guard.profileDir), false);
});
