import { expect, test } from "vitest";
import { classifySteering, type ParsedNotification, type SteeringClass, steeringTreatment } from "./steeringClassify";

// Non-undefined nth-notification accessor (noUncheckedIndexedAccess makes a
// bare [i] possibly-undefined); throws with a clear message when absent.
function notif(c: SteeringClass, i: number): ParsedNotification {
  const n = c.notifications?.[i];
  if (!n) throw new Error(`expected a notification at index ${i}`);
  return n;
}

// Classification is CONTENT-pattern-based (legacy renderer-format.js:414-494's
// classifySteering, driven from renderer.js:4706-4756) - a pure frontend
// decision off the steering text, so no wire-kind field is needed and nothing
// escalates to the reducer.

test("a <CURRENT-TASK> reminder classifies as current-task and is suppressed", () => {
  const c = classifySteering(
    '<SYSTEM-REMINDER><CURRENT-TASK id="3"><TITLE>Build it</TITLE></CURRENT-TASK></SYSTEM-REMINDER>',
  );
  expect(c.kind).toBe("current-task");
  expect(steeringTreatment(c.kind)).toBe("suppress");
});

test("a full task list classifies as full-list and is suppressed", () => {
  const c = classifySteering("Task list:\n[open] #1: do a thing");
  expect(c.kind).toBe("full-list");
  expect(steeringTreatment(c.kind)).toBe("suppress");
});

test('a "task_list tool available" nudge classifies as task-nudge and is suppressed', () => {
  const c = classifySteering("Reminder: the task_list tool available for planning.");
  expect(c.kind).toBe("task-nudge");
  expect(steeringTreatment(c.kind)).toBe("suppress");
});

test('"completed all tasks" classifies as tasks-done and keeps a divider', () => {
  const c = classifySteering("You have completed all tasks on the list.");
  expect(c.kind).toBe("tasks-done");
  expect(c.label).toBe("tasks done");
  expect(steeringTreatment(c.kind)).toBe("divider");
});

test("loop detection classifies as loop with its own divider label", () => {
  const c = classifySteering("You appear to be stuck in a loop.");
  expect(c.kind).toBe("loop");
  expect(c.label).toBe("loop detection");
  expect(steeringTreatment(c.kind)).toBe("divider");
});

test("a read-only nudge classifies as read-only", () => {
  const c = classifySteering("You have been reading without writing for 8 turns.");
  expect(c.kind).toBe("read-only");
  expect(c.label).toBe("read-only nudge");
});

test("a transcript pointer classifies as transcript", () => {
  const c = classifySteering("Your pre-compaction transcript is available.");
  expect(c.kind).toBe("transcript");
  expect(c.label).toBe("transcript pointer");
});

test("an unrecognized steer falls through to unknown with the generic label", () => {
  const c = classifySteering("Some one-off system note.");
  expect(c.kind).toBe("unknown");
  expect(c.label).toBe("steering injected");
  expect(steeringTreatment(c.kind)).toBe("divider");
});

// --- notification parsing (contracts §17) --------------------------------

const oneBlock = `<job-notification job_id="job_42" event="completed" job_type="delegate" status="completed" reason="" output_bytes="12" transcript_ref="ref_a">
Job job_42 completed. Output is available through read_transcript(transcript_ref="ref_a") if needed.
excerpt:
did the thing
</job-notification>`;

test("a delegate-completion job-notification classifies as notification and parses one block", () => {
  const c = classifySteering(oneBlock);
  expect(c.kind).toBe("notification");
  expect(steeringTreatment(c.kind)).toBe("card");
  expect(c.notifications).toHaveLength(1);
  const n = notif(c, 0);
  expect(n.title).toBe("Job completed");
  expect(n.tone).toBe("success");
  expect(n.excerpt).toBe("did the thing");
  expect(n.secondary).toContain("delegate");
});

test("several job-notification blocks each parse individually (no greedy aggregation across blocks)", () => {
  const two = `${oneBlock}\n<job-notification job_id="job_43" event="failed" job_type="shell" status="failed" reason="nonzero exit" output_bytes="4" exit_code="2">
Job job_43 failed.
excerpt:
boom
</job-notification>`;
  const c = classifySteering(two);
  expect(c.notifications).toHaveLength(2);
  expect(notif(c, 1).tone).toBe("error");
  // Each card's raw text is only its own block, never bleeding across boundaries.
  expect(notif(c, 0).rawText).not.toContain("job_43");
  expect(notif(c, 1).rawText).not.toContain("job_42");
});

test("an exhausted notification is a terminal, non-success (error) tone", () => {
  const block = `<job-notification job_id="j" event="exhausted" job_type="delegate" status="exhausted" reason="budget" output_bytes="0" budget="10" limit="10" resumable="false">
Job j exhausted.
</job-notification>`;
  expect(notif(classifySteering(block), 0).tone).toBe("error");
});

test("a nonzero exit code forces error tone even when the status is otherwise clean", () => {
  const block = `<job-notification job_id="j" event="completed" job_type="shell" status="completed" reason="" output_bytes="0" exit_code="1">
Job j completed.
</job-notification>`;
  expect(notif(classifySteering(block), 0).tone).toBe("error");
});

test("a job-less watch event classifies as a watch notification", () => {
  const block = `<job-notification job_id="" event="watch" job_type="" status="watch" reason="file changed" output_bytes="0">
Watch event triggered: file changed.
</job-notification>`;
  const n = notif(classifySteering(block), 0);
  expect(n.type).toBe("watch");
  expect(n.title).toBe("Watch triggered");
});

test("an Observer callback classifies as a notification", () => {
  const c = classifySteering("Observer callback:\nmessage: something happened\noutput: details here");
  expect(c.kind).toBe("notification");
  expect(notif(c, 0).title).toBe("Observer callback");
});

test("an Observer callback with no output surfaces its message prose (not just the raw disclosure)", () => {
  // The daemon emits `Observer callback:\nmessage: X` with no `\noutput:` when
  // the callback carries no tool output (agent/session_tools_communicate.go:117).
  // The prose is then the ONLY content, so it must reach the card body (floor
  // parity-m4 §8:239 "body = observer-callback prose"), not be dropped to the
  // raw disclosure alone.
  const c = classifySteering("Observer callback:\nmessage: the sidecar noticed the build broke");
  expect(c.kind).toBe("notification");
  const n = notif(c, 0);
  expect(n.title).toBe("Observer callback");
  expect(n.excerpt).toBe("the sidecar noticed the build broke");
});

test("leftover text around a notification block is preserved for a trailing divider", () => {
  const c = classifySteering(`some preface\n${oneBlock}\nsome epilogue`);
  expect(c.kind).toBe("notification");
  expect(c.leftover).toContain("some preface");
  expect(c.leftover).toContain("some epilogue");
});

test("a communicate envelope inside a notification exposes its message for markdown rendering", () => {
  const block = `<job-notification job_id="j" event="completed" job_type="delegate" status="completed" reason="" output_bytes="0">
Job j completed.
excerpt:
{"message":"**done** with the work","data":{"concerns":["watch the edge case"]}}
</job-notification>`;
  const n = notif(classifySteering(block), 0);
  expect(n.message).toBe("**done** with the work");
  expect(n.concerns).toEqual(["watch the edge case"]);
});
