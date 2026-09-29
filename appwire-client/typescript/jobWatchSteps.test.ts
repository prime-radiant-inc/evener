// @vitest-environment node

import { expect, test } from "vitest";
import { createdWatchNote } from "./jobWatchSteps";
import { toolWireStep } from "./testing/toolWireFixtures";

// A create's note is what the watch says when it fires; a list, an inspect or
// a clear armed nothing.
test("reads the note a recorded job_watch create armed, and none from any other operation", () => {
  expect(createdWatchNote(toolWireStep("call_watch_timer"))).toBe("Check the deploy finished.");
  expect(createdWatchNote(toolWireStep("call_watch_repeat"))).toBe("Look over the open PRs.");
  for (const call of ["call_watch_list", "call_watch_inspect", "call_watch_clear"] as const)
    expect(createdWatchNote(toolWireStep(call))).toBeUndefined();
  expect(createdWatchNote({ toolName: "job_watch", argumentsJSON: '{"operation":"create"}', raw: {} })).toBeUndefined();
});
