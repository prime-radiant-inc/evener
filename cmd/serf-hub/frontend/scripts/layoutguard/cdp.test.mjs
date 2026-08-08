import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { test } from "node:test";
import { probeBrowserCapability } from "./cdp.mjs";

test("probeBrowserCapability includes Chrome binary path in error message on startup failure", async () => {
  // Mock a spawn that returns a process with stderr but never succeeds waitForCdp
  const mockSpawn = () => {
    const proc = new EventEmitter();
    proc.stderr = new EventEmitter();
    proc.kill = () => {};
    // Never emit a success signal; waitForCdp will timeout and throw
    return proc;
  };

  await assert.rejects(
    () => probeBrowserCapability(mockSpawn),
    (err) => {
      assert.ok(err.message.includes("Chrome startup failed"), "error should mention Chrome startup failed");
      assert.ok(err.message.includes("Chrome binary:"), "error should include Chrome binary path");
      return true;
    },
  );
});

test("probeBrowserCapability includes launch arguments in error message", async () => {
  const mockSpawn = () => {
    const proc = new EventEmitter();
    proc.stderr = new EventEmitter();
    proc.kill = () => {};
    return proc;
  };

  await assert.rejects(
    () => probeBrowserCapability(mockSpawn),
    (err) => {
      assert.ok(err.message.includes("Launch args:"), "error should include launch arguments");
      // Verify specific args are included
      assert.ok(err.message.includes("--headless=new"), "error should contain --headless=new");
      assert.ok(err.message.includes("--disable-gpu"), "error should contain --disable-gpu");
      return true;
    },
  );
});

test("probeBrowserCapability includes remediation guidance in error message", async () => {
  const mockSpawn = () => {
    const proc = new EventEmitter();
    proc.stderr = new EventEmitter();
    proc.kill = () => {};
    return proc;
  };

  await assert.rejects(
    () => probeBrowserCapability(mockSpawn),
    (err) => {
      assert.ok(
        err.message.includes("To remediate:"),
        "error should include remediation section",
      );
      assert.ok(
        err.message.includes("Verify Chrome/Chromium is installed"),
        "error should mention Chrome installation",
      );
      assert.ok(
        err.message.includes("Check system resources"),
        "error should mention system resources",
      );
      assert.ok(
        err.message.includes("Ensure no port conflict"),
        "error should mention port conflict check",
      );
      return true;
    },
  );
});

test("probeBrowserCapability captures and includes stderr in error message when Chrome fails", async () => {
  const mockSpawn = () => {
    const proc = new EventEmitter();
    proc.stderr = new EventEmitter();
    proc.kill = () => {};
    // Simulate Chrome emitting diagnostic stderr
    setImmediate(() => {
      proc.stderr.emit("data", Buffer.from("Chrome failed: binary not found"));
    });
    return proc;
  };

  await assert.rejects(
    () => probeBrowserCapability(mockSpawn),
    (err) => {
      assert.ok(
        err.message.includes("Chrome stderr:"),
        "error should include Chrome stderr section",
      );
      assert.ok(
        err.message.includes("binary not found"),
        "error should contain captured stderr output",
      );
      return true;
    },
  );
});

test("probeBrowserCapability includes fallback message when no stderr is captured", async () => {
  const mockSpawn = () => {
    const proc = new EventEmitter();
    proc.stderr = new EventEmitter();
    proc.kill = () => {};
    // Don't emit any stderr data
    return proc;
  };

  await assert.rejects(
    () => probeBrowserCapability(mockSpawn),
    (err) => {
      assert.ok(
        err.message.includes("no stderr captured"),
        "error should include fallback when no stderr",
      );
      assert.ok(
        err.message.includes("check Chrome binary permissions and system resources"),
        "error should provide guidance when stderr unavailable",
      );
      return true;
    },
  );
});

test("probeBrowserCapability distinguishes environment failure from test case failure in error", async () => {
  const mockSpawn = () => {
    const proc = new EventEmitter();
    proc.stderr = new EventEmitter();
    proc.kill = () => {};
    return proc;
  };

  await assert.rejects(
    () => probeBrowserCapability(mockSpawn),
    (err) => {
      assert.ok(
        err.message.includes("environment problem, not a test case failure"),
        "error must clearly indicate this is an environment issue",
      );
      return true;
    },
  );
});
