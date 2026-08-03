import assert from "node:assert/strict";
import { test } from "node:test";

import { findAvailablePort, startBrowserGuard } from "./browserGuardProcess.mjs";

test("allocates distinct local ports", async () => {
  const first = await findAvailablePort();
  const second = await findAvailablePort([first]);
  assert.notEqual(first, second);
  assert.ok(first > 0);
  assert.ok(second > 0);
});

test("cleans Vite when Chrome startup throws", async () => {
  const killed = [];
  let calls = 0;
  const spawnProcess = (command) => {
    calls++;
    if (calls === 1) {
      return {
        stderr: { on() {} },
        kill: () => killed.push(command),
      };
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
  assert.deepEqual(killed, ["./node_modules/.bin/vite"]);
});
