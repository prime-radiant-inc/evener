// @vitest-environment node

import { expect, test } from "vitest";
import { jobWatchEvidence } from "./jobWatchSteps";
import { toolWireStep } from "./testing/toolWireFixtures";

// What each recorded job_watch step shows when opened, in words: a create's
// note, a list's rows, an inspected watch's trigger and note; nothing more
// for a clear, whose line says it all.
test("words what each recorded job_watch step shows when opened", () => {
  expect(jobWatchEvidence(toolWireStep("call_watch_timer"))).toBe("Check the deploy finished.");
  expect(jobWatchEvidence(toolWireStep("call_watch_repeat"))).toBe("Look over the open PRs.");
  expect(jobWatchEvidence(toolWireStep("call_watch_list"))).toBe(
    "watching  watch_fixture_1  in 5m · this session\nwatching  watch_fixture_2  every 10m · this session",
  );
  expect(jobWatchEvidence(toolWireStep("call_watch_inspect"))).toBe("in 5m · this session\nCheck the deploy finished.");
  expect(jobWatchEvidence(toolWireStep("call_watch_clear"))).toBe("");
});

test("shows nothing more for a create with no note or a terminal catch-up, and can't read a shape it doesn't know", () => {
  const create = (raw: unknown) =>
    jobWatchEvidence({ toolName: "job_watch", argumentsJSON: '{"operation":"create"}', raw });
  expect(create({ watch_id: "watch_x", source: "self", watching: true, after_seconds: 300, fired: false })).toBe("");
  expect(
    create({ watch_id: "watch_x", source: "job_a", watching: false, terminal_catchup: true, note: "n", fired: false }),
  ).toBe("");
  expect(create({})).toBeUndefined();
  expect(jobWatchEvidence({ toolName: "job_watch", argumentsJSON: '{"operation":"list"}', raw: { watches: [] } })).toBe(
    "No watches.",
  );
});

// An inspect names what the watch watched for in every state, and its note
// once: a note-only watch's trigger phrase already is its note.
test("words an inspected watch's trigger in every state, and its note once", () => {
  const inspect = (raw: Record<string, unknown>) =>
    jobWatchEvidence({ toolName: "job_watch", argumentsJSON: '{"operation":"inspect"}', raw });
  const timer = { watch_id: "watch_x", source: "self", condition: "after_seconds: 300; note: n", note: "n" };
  expect(inspect({ ...timer, watching: false, end_reason: "fired" })).toBe("ended: fired\nin 5m · this session\nn");
  expect(inspect({ ...timer, watching: false })).toBe("pending\nin 5m · this session\nn");
  expect(
    inspect({ watch_id: "watch_x", source: "self", watching: true, condition: "note: Check CI", note: "Check CI" }),
  ).toBe("Check CI · this session");
  expect(inspect({ watch_id: "watch_x", watching: false })).toBe("");
});
