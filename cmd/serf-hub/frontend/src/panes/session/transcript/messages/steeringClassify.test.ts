// @vitest-environment node

import { expect, test } from "vitest";
import { type ParsedNotification, parseSteeringNotifications } from "./steeringClassify";

// Non-undefined nth-notification accessor (noUncheckedIndexedAccess makes a
// bare [i] possibly-undefined); throws with a clear message when absent.
function notif(notifications: ParsedNotification[], i: number): ParsedNotification {
  const n = notifications[i];
  if (!n) throw new Error(`expected a notification at index ${i}`);
  return n;
}

// steeringClassify.ts now parses only STRUCTURED notification payloads -
// <job-notification> markup and the fixed "Observer callback:\n" header
// (agent/session_tools_communicate.go). The prose classifier that used to
// guess a steering "kind" from wording is gone (SteeringItem.tsx routes on
// ItemModel.steeringKind instead); this pins that its exports stay gone.

test("no longer exports a prose classifier", async () => {
  const mod = await import("./steeringClassify");
  expect("classifySteering" in mod).toBe(false);
  expect("steeringTreatment" in mod).toBe(false);
});

// --- notification parsing (contracts §17) --------------------------------

const oneBlock = `<job-notification job_id="job_42" event="completed" job_type="delegate" status="completed" reason="" output_bytes="12" transcript_ref="ref_a">
Job job_42 completed. Output is available through read_transcript(transcript_ref="ref_a") if needed.
excerpt:
did the thing
</job-notification>`;

test("a delegate-completion job-notification parses one block", () => {
  const { notifications } = parseSteeringNotifications(oneBlock);
  expect(notifications).toHaveLength(1);
  const n = notif(notifications, 0);
  expect(n.title).toBe("Job completed");
  expect(n.tone).toBe("success");
  expect(n.excerpt).toBe("did the thing");
  expect(n.secondary).toContain("delegate");
});

test("retains validated child identity and useful job fields from a completion", () => {
  const block = `<job-notification job_id="job_42" event="completed" job_type="delegate" status="completed" reason="" output_bytes="12" exit_code="0" transcript_ref="local:child">
Job job_42 completed.
excerpt:
did the thing
</job-notification>`;
  const n = notif(parseSteeringNotifications(block).notifications, 0);
  expect(n.jobId).toBe("job_42");
  expect(n.jobType).toBe("delegate");
  expect(n.status).toBe("completed");
  expect(n.outputBytes).toBe(12);
  expect(n.exitCode).toBe(0);
  expect(n.transcriptRef).toBe("local:child");
  expect(n.excerpt).toBe("did the thing");
});

test("retains qualified remote child references", () => {
  const block = `<job-notification job_id="job_remote" event="completed" status="completed" transcript_ref="remote:child">
done
</job-notification>`;
  expect(notif(parseSteeringNotifications(block).notifications, 0).transcriptRef).toBe("remote:child");
});

test("drops missing, empty, and malformed child references", () => {
  const refs = [undefined, "", "child", "local:child:extra", "local:bad..child", "local:bad ref"];
  for (const ref of refs) {
    const attr = ref === undefined ? "" : ` transcript_ref="${ref}"`;
    const block = `<job-notification job_id="job_bad" event="completed" status="completed"${attr}>done</job-notification>`;
    expect(notif(parseSteeringNotifications(block).notifications, 0).transcriptRef).toBeUndefined();
  }
});

test("several job-notification blocks each parse individually (no greedy aggregation across blocks)", () => {
  const two = `${oneBlock}\n<job-notification job_id="job_43" event="failed" job_type="shell" status="failed" reason="nonzero exit" output_bytes="4" exit_code="2">
Job job_43 failed.
excerpt:
boom
</job-notification>`;
  const { notifications } = parseSteeringNotifications(two);
  expect(notifications).toHaveLength(2);
  expect(notif(notifications, 1).tone).toBe("error");
  // Each card's raw text is only its own block, never bleeding across boundaries.
  expect(notif(notifications, 0).rawText).not.toContain("job_43");
  expect(notif(notifications, 1).rawText).not.toContain("job_42");
});

test("an exhausted notification is a terminal, non-success (error) tone", () => {
  const block = `<job-notification job_id="j" event="exhausted" job_type="delegate" status="exhausted" reason="budget" output_bytes="0" budget="10" limit="10" resumable="false">
Job j exhausted.
</job-notification>`;
  expect(notif(parseSteeringNotifications(block).notifications, 0).tone).toBe("error");
});

test("a nonzero exit code forces error tone even when the status is otherwise clean", () => {
  const block = `<job-notification job_id="j" event="completed" job_type="shell" status="completed" reason="" output_bytes="0" exit_code="1">
Job j completed.
</job-notification>`;
  expect(notif(parseSteeringNotifications(block).notifications, 0).tone).toBe("error");
});

test("a job-less watch event classifies as a watch notification", () => {
  const block = `<job-notification job_id="" event="watch" job_type="" status="watch" reason="file changed" output_bytes="0">
Watch event triggered: file changed.
</job-notification>`;
  const n = notif(parseSteeringNotifications(block).notifications, 0);
  expect(n.type).toBe("watch");
  expect(n.title).toBe("Watch triggered");
});

test("an Observer callback parses as a notification", () => {
  const { notifications } = parseSteeringNotifications(
    "Observer callback:\nmessage: something happened\noutput: details here",
  );
  expect(notif(notifications, 0).title).toBe("Observer callback");
});

test("an Observer callback with no output surfaces its message prose (not just the raw disclosure)", () => {
  // The daemon emits `Observer callback:\nmessage: X` with no `\noutput:` when
  // the callback carries no tool output (agent/session_tools_communicate.go:117).
  // The prose is then the ONLY content, so it must reach the card body (floor
  // parity-m4 §8:239 "body = observer-callback prose"), not be dropped to the
  // raw disclosure alone.
  const { notifications } = parseSteeringNotifications(
    "Observer callback:\nmessage: the sidecar noticed the build broke",
  );
  const n = notif(notifications, 0);
  expect(n.title).toBe("Observer callback");
  expect(n.excerpt).toBe("the sidecar noticed the build broke");
});

test("leftover text around a notification block is preserved for a trailing divider", () => {
  const { notifications, leftover } = parseSteeringNotifications(`some preface\n${oneBlock}\nsome epilogue`);
  expect(notifications).toHaveLength(1);
  expect(leftover).toContain("some preface");
  expect(leftover).toContain("some epilogue");
});

test("a communicate envelope inside a notification exposes its message for markdown rendering", () => {
  const block = `<job-notification job_id="j" event="completed" job_type="delegate" status="completed" reason="" output_bytes="0">
Job j completed.
excerpt:
{"message":"**done** with the work","data":{"concerns":["watch the edge case"]}}
</job-notification>`;
  const n = notif(parseSteeringNotifications(block).notifications, 0);
  expect(n.message).toBe("**done** with the work");
  expect(n.concerns).toEqual(["watch the edge case"]);
});
