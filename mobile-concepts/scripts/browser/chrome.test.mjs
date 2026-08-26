import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  activePort,
  bounded,
  closeBrowserProcess,
  closePageTarget,
  exitEvent,
  finalizeChromeProfile,
  runOwnedCleanup,
} from "./chrome.mjs";

test("exit event preserves the exact Node code and signal shape", async () => {
  const child = new EventEmitter();
  child.exitCode = null;
  child.signalCode = null;
  const observed = exitEvent(child);
  child.emit("exit", 0, null);
  assert.deepEqual(await observed, { code: 0, signal: null });
});

test("already-exited child preserves code and signal shape", async () => {
  assert.deepEqual(
    await exitEvent({ exitCode: 0, signalCode: null }),
    { code: 0, signal: null },
  );
  assert.deepEqual(
    await exitEvent({ exitCode: null, signalCode: "SIGKILL" }),
    { code: null, signal: "SIGKILL" },
  );
});

test("only clean zero-exit shutdown removes the owned profile", async () => {
  const removed = [];
  const removeProfile = async (profile) => removed.push(profile);
  assert.equal(
    await finalizeChromeProfile(
      "/clean-profile",
      { retainProfile: false, shutdownFailed: false },
      removeProfile,
    ),
    false,
  );
  assert.deepEqual(removed, ["/clean-profile"]);

  for (const [profile, state] of [
    ["/nonzero-profile", { retainProfile: false, shutdownFailed: true }],
    ["/matrix-red-profile", { retainProfile: true, shutdownFailed: false }],
    ["/forced-red-profile", { retainProfile: true, shutdownFailed: true }],
  ]) {
    assert.equal(
      await finalizeChromeProfile(profile, state, removeProfile),
      true,
    );
  }
  assert.deepEqual(removed, ["/clean-profile"]);
});

test("bounded rejects a hung Browser.close request", async () => {
  await assert.rejects(
    bounded(new Promise(() => {}), 5, "Browser.close request"),
    /Browser\.close request exceeded 5ms/,
  );
});

test("Chrome shutdown bounds the actual Browser.close request and escalates", async () => {
  const child = new EventEmitter();
  child.exitCode = null;
  child.signals = [];
  child.kill = (signal) => {
    child.signals.push(signal);
    child.exitCode = 0;
    child.emit("exit", 0, signal);
  };
  const browser = { send: () => new Promise(() => {}) };
  assert.equal(await closeBrowserProcess(browser, child, 5), true);
  assert.deepEqual(child.signals, ["SIGTERM"]);
});

test("expected protocol disconnect plus observed child exit is a clean shutdown", async () => {
  const child = new EventEmitter();
  child.exitCode = null;
  child.signals = [];
  child.kill = (signal) => child.signals.push(signal);
  const browser = {
    async send(method) {
      assert.equal(method, "Browser.close");
      queueMicrotask(() => {
        child.exitCode = 0;
        child.emit("exit", 0, null);
      });
      throw new Error("CDP WebSocket closed");
    },
  };
  assert.equal(await closeBrowserProcess(browser, child, 50), false);
  assert.deepEqual(child.signals, []);
});

test("nonzero child exit after protocol close retains shutdown diagnostics", async () => {
  const child = new EventEmitter();
  child.exitCode = null;
  child.kill = () => assert.fail("already observed exit must not be signaled");
  const browser = {
    async send() {
      queueMicrotask(() => {
        child.exitCode = 9;
        child.emit("exit", 9, null);
      });
      throw new Error("CDP WebSocket closed");
    },
  };
  assert.equal(await closeBrowserProcess(browser, child, 50), true);
});

test("readiness rejects child error instead of waiting for the port file", async () => {
  const profile = await mkdtemp(path.join(os.tmpdir(), "chrome-ready-test-"));
  const child = new EventEmitter();
  try {
    const ready = activePort(profile, child, () => "spawn diagnostics");
    child.emit("error", new Error("exec failed"));
    await assert.rejects(ready, /Chrome process error: exec failed/);
  } finally {
    await rm(profile, { recursive: true, force: true });
  }
});

test("owned cleanup attempts every release and preserves primary errors", async () => {
  const calls = [];
  const primary = new Error("case failed");
  await assert.rejects(
    runOwnedCleanup(primary, [
      async () => {
        calls.push("target");
        throw new Error("target close failed");
      },
      async () => {
        calls.push("listener");
        throw new Error("listener close failed");
      },
    ]),
    (error) => {
      assert(error instanceof AggregateError);
      assert.deepEqual(
        error.errors.map(({ message }) => message),
        ["case failed", "target close failed", "listener close failed"],
      );
      return true;
    },
  );
  assert.deepEqual(calls, ["target", "listener"]);
});

test("actual target cleanup releases WebSocket after Target.closeTarget rejects", async () => {
  const calls = [];
  const browser = {
    async send(method, params) {
      calls.push([method, params]);
      throw new Error("target close rejected");
    },
  };
  const page = {
    async close() {
      calls.push(["WebSocket.close"]);
      throw new Error("websocket close rejected");
    },
  };
  await assert.rejects(
    closePageTarget(browser, page, "target-1", new Error("case failed")),
    (error) => {
      assert(error instanceof AggregateError);
      assert.deepEqual(
        error.errors.map(({ message }) => message),
        ["case failed", "target close rejected", "websocket close rejected"],
      );
      return true;
    },
  );
  assert.deepEqual(calls, [
    ["Target.closeTarget", { targetId: "target-1" }],
    ["WebSocket.close"],
  ]);
});
