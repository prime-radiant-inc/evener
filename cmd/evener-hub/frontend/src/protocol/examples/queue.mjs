import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";
import { isAbsolute } from "node:path";
import { clientFromEnvironment } from "./connection.mjs";
import { runQueue } from "./queue-logic.mjs";

let hub;
try {
  const action = process.env.EVENER_QUEUE_ACTION ?? "list";
  const file = process.env.EVENER_QUEUE_PARAMS_FILE;
  const params = file ? JSON.parse(await readFile(file, "utf8")) : {};
  const reviewFile = process.env.EVENER_QUEUE_REVIEW_FILE;
  if (reviewFile)
    assert.ok(action === "list" && isAbsolute(reviewFile), "Use an absolute review output path in list mode.");
  ({ hub } = clientFromEnvironment());
  const result = await runQueue(hub, { action, params, ownedHub: process.env.EVENER_QUEUE_OWNED_HUB });
  const { evener } = result.readback.thread;
  if (reviewFile) {
    // Full queued text goes only to an explicitly requested private review file.
    await writeFile(
      reviewFile,
      `${JSON.stringify(
        {
          ref: evener.ref,
          expectedInstanceId: evener.instanceId,
          queue: evener.queue,
        },
        null,
        2,
      )}\n`,
      { flag: "wx", mode: 0o600 },
    );
  }
  console.log(
    JSON.stringify({
      outcome: result.outcome,
      execution: result.execution,
      depth: evener.queue.depth ?? 0,
      reviewWritten: Boolean(reviewFile),
    }),
  );
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch {
  console.error("Queue operation failed.");
  process.exitCode = 1;
} finally {
  hub?.close();
}
