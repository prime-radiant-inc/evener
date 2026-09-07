import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";
import { isAbsolute } from "node:path";
import { clientFromEnvironment } from "./connection.mjs";
import { runQuestions } from "./questions-logic.mjs";

let hub;
try {
  const action = process.env.EVENER_QUESTION_ACTION ?? "list";
  const file = process.env.EVENER_QUESTION_PARAMS_FILE;
  const params = file ? JSON.parse(await readFile(file, "utf8")) : {};
  const reviewFile = process.env.EVENER_QUESTION_REVIEW_FILE;
  if (reviewFile)
    assert.ok(action === "list" && isAbsolute(reviewFile), "Use an absolute review output path in list mode.");
  ({ hub } = clientFromEnvironment());
  const result = await runQuestions(hub, {
    action,
    params,
    ownedHub: process.env.EVENER_QUESTION_OWNED_HUB,
  });
  if (reviewFile) {
    // Question text is written only to this explicitly requested private file.
    // Exclusive creation preserves any previous reviewed decision.
    await writeFile(
      reviewFile,
      `${JSON.stringify(
        {
          ref: result.thread.evener.ref,
          expectedInstanceId: result.thread.evener.instanceId,
          reviewedCalls: result.calls,
          questions: result.questions,
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
      ...(action === "list"
        ? { questionCount: result.questions.length, reviewWritten: Boolean(reviewFile) }
        : { pending: result.readback.thread.evener.askPending === true }),
    }),
  );
  if (result.outcome === "uncertain") process.exitCode = 2;
} catch {
  console.error("Question operation failed.");
  process.exitCode = 1;
} finally {
  hub?.close();
}
