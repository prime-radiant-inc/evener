import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { existsSync, mkdtempSync, realpathSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test } from "node:test";

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
    return true;
  }

  exit(signal = "SIGTERM") {
    this.signalCode = signal;
    this.emit("exit", null, signal);
  }
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
