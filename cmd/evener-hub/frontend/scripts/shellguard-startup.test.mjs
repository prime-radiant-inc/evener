import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { test } from "node:test";

test("startup cancellation stays observed until setup reaches its cancellation wait", () => {
  const moduleURL = new URL("./shellguard/startup.mjs", import.meta.url).href;
  const child = spawnSync(process.execPath, ["--unhandled-rejections=strict", "--input-type=module", "--eval", `
    import assert from "node:assert/strict";
    import { setImmediate } from "node:timers/promises";
    const { startupCancellation } = await import(${JSON.stringify(moduleURL)});
    const controller = new AbortController();
    const reason = new Error("startup cancelled during setup");
    const aborted = startupCancellation(controller.signal);
    let cleaned = false;
    try {
      controller.abort(reason);
      // Setup can cross a Node turn before its first cancellation wait.
      await setImmediate();
      await assert.rejects(Promise.race([aborted]), error => error === reason);
    } finally {
      cleaned = true;
    }
    process.stdout.write(JSON.stringify({ cleaned }));
  `], { encoding: "utf8", timeout: 30000 });

  assert.equal(child.error, undefined);
  assert.equal(child.signal, null);
  assert.equal(child.status, 0, child.stderr);
  assert.equal(child.stderr, "");
  assert.deepEqual(JSON.parse(child.stdout), { cleaned: true });
});
