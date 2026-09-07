import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";
import { isAbsolute } from "node:path";
import { clientFromEnvironment } from "./connection.mjs";
import { runGoals } from "./goals-logic.mjs";

let hub;
try {
  const action = process.env.EVENER_GOAL_ACTION ?? "list";
  const file = process.env.EVENER_GOAL_PARAMS_FILE;
  const params = file ? JSON.parse(await readFile(file, "utf8")) : {};
  const reviewFile = process.env.EVENER_GOAL_REVIEW_FILE;
  if (reviewFile)
    assert.ok(action === "list" && isAbsolute(reviewFile), "Use an absolute review output path in list mode.");
  ({ hub } = clientFromEnvironment());
  const result = await runGoals(hub, { action, params, ownedHub: process.env.EVENER_GOAL_OWNED_HUB });
  const { evener } = result.readback.thread;
  if (reviewFile) {
    // Authored objectives go only to an explicitly requested private review file.
    const review = { ref: evener.ref, expectedInstanceId: evener.instanceId, reviewedGoal: evener.goal ?? null };
    await writeFile(reviewFile, JSON.stringify(review, null, 2), { flag: "wx", mode: 0o600 });
  }
  console.log(
    JSON.stringify({
      outcome: result.outcome,
      started: result.started,
      execution: result.execution,
      hasGoal: evener.goal != null,
      reviewWritten: Boolean(reviewFile),
    }),
  );
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch {
  console.error("Goal operation failed.");
  process.exitCode = 1;
} finally {
  hub?.close();
}
