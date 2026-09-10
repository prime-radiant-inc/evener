import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { withPrivateOutput } from "./private-output.mjs";
import { runSessionManagement, safeSessionManagementSummary } from "./session-management-logic.mjs";

export async function runSessionManagementCLI(environment, hub) {
  const action = environment.EVENER_SESSION_MANAGEMENT_ACTION ?? "list";
  const file = environment.EVENER_SESSION_MANAGEMENT_PARAMS_FILE;
  const params = file ? JSON.parse(await readFile(file, "utf8")) : {};
  const reviewFile = environment.EVENER_SESSION_MANAGEMENT_REVIEW_FILE;
  assert.ok(reviewFile === undefined || action === "list", "Review output is only available in list mode.");
  const result = await withPrivateOutput(reviewFile, async (output) => {
    const result = await runSessionManagement(hub, {
      action,
      params,
      ownedHub: environment.EVENER_SESSION_MANAGEMENT_OWNED_HUB,
    });
    if (output) {
      assert.ok(result.review, "Session has no controllable instance to review.");
      await output.writeFile(
        JSON.stringify(
          { ref: params.ref, expectedInstanceId: result.review.instanceId, reviewed: result.review },
          null,
          2,
        ),
      );
    }
    return result;
  });
  console.log(
    JSON.stringify({ action, ...safeSessionManagementSummary(result), reviewWritten: reviewFile !== undefined }),
  );
  return result;
}
