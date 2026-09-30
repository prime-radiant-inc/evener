// @vitest-environment node

import { expect, test } from "vitest";
import { delegateSendResponse, delegateSendWaitIgnoredReason } from "./delegateSteps";
import { subagentWireStep } from "./testing/subagentWireFixtures";
import { toolStepSummary, toolStepWords } from "./toolSummaries";

// The recorded delegate and delegate_send calls (agent/testdata/subagentwire),
// worded as the web words them: a delegate by its intent, a send by the
// delegate it messaged and the status its footer reports. The first send
// steered the running delegate; the second waited for its reply, so its
// footer reports the generation completed.
test("words the recorded delegate and delegate_send calls as the web does", () => {
  const delegate = subagentWireStep("call_delegate_1");
  expect(toolStepWords(delegate)).toEqual({ verb: "Fix race in tree settle" });
  expect(toolStepSummary(delegate)).toBe("Fix race in tree settle");
  const send = subagentWireStep("call_send_1");
  expect(toolStepWords(send)).toEqual({
    verb: "Sent a message to delegate",
    target: "dlg_02wMz5TxvSettleRace001",
    detail: "running",
  });
  expect(toolStepSummary(send)).toBe("Sent a message to delegate dlg_02wMz5TxvSettleRace001 · running");
  const waited = subagentWireStep("call_send_2");
  expect(toolStepWords(waited)).toEqual({
    verb: "Sent a message to delegate",
    target: "dlg_02wMz5TxvSettleRace001",
    detail: "completed",
  });
  expect(toolStepSummary(waited)).toBe("Sent a message to delegate dlg_02wMz5TxvSettleRace001 · completed");
});

// A send's status is the footer's own status field: a delivered or
// undelivered send says so, and a footer with no status that says the
// delegate runs in the background reads "running", as the web's rows always
// have. Its fallbacks: a send that names no delegate, and a delegate call
// with no intent.
test("reads a send's status from its footer's status field, and words the fallbacks", () => {
  const send = (output: string, args: Record<string, unknown> = { to: "dlg_x" }) =>
    toolStepWords({ toolName: "delegate_send", argumentsJSON: JSON.stringify(args), output });
  const sent = { verb: "Sent a message to delegate", target: "dlg_x" };
  expect(send("[delegate_id dlg_x · steered · delivered]")).toEqual({ ...sent, detail: "delivered" });
  expect(send("[delegate_id dlg_x · queued · not_delivered]")).toEqual({ ...sent, detail: "not delivered" });
  expect(send("[delegate_id dlg_x · steered · running in background]")).toEqual({ ...sent, detail: "running" });
  expect(send("[delegate_id dlg_x · steered]")).toEqual(sent);
  expect(send("no footer")).toEqual(sent);
  expect(send("[delegate_id dlg_x · steered · delivered]", {})).toEqual({
    verb: "Sent a message to a delegate",
    detail: "delivered",
  });
  expect(toolStepWords({ toolName: "delegate", argumentsJSON: "{}", description: "  " })).toEqual({
    verb: "Used delegate",
  });
});

// What a send exchanged, as both clients show it: the recorded waiting send's
// reply, from its raw state, and none for the steer, whose output is only its
// footer. Without a raw state the reply is what the tool printed above the
// footer, or all of it when there is no footer.
test("reads a send's reply from its raw state, else from above its footer", () => {
  expect(delegateSendResponse(subagentWireStep("call_send_2"))).toBe(
    "Yes: tree settle now waits for the drain, and a test pins the order.",
  );
  expect(delegateSendResponse(subagentWireStep("call_send_1"))).toBeUndefined();
  expect(delegateSendResponse({ output: "Done.\nAll green.\n[delegate_id dlg_x · completed · completed]" })).toBe(
    "Done.\nAll green.",
  );
  expect(delegateSendResponse({ output: "[delegate_id dlg_x · steered · running]" })).toBeUndefined();
  expect(delegateSendResponse({ output: "no footer" })).toBe("no footer");
  expect(delegateSendResponse({})).toBeUndefined();
});

// A wait the send couldn't honour says why, from its raw state first, then
// its footer; a send that waited, or didn't ask to, has no reason.
test("reads why a send's wait was ignored from its raw state, else its footer", () => {
  const raw = { action: "steered", running_in_background: true, wait_ignored_reason: " delegate is already running " };
  expect(delegateSendWaitIgnoredReason({ raw, output: "" })).toBe("delegate is already running");
  expect(
    delegateSendWaitIgnoredReason({
      output: "[delegate_id dlg_x · steered · running · running in background · wait ignored: delegate is busy]",
    }),
  ).toBe("delegate is busy");
  expect(delegateSendWaitIgnoredReason(subagentWireStep("call_send_2"))).toBeUndefined();
  expect(delegateSendWaitIgnoredReason({ output: "[delegate_id dlg_x · steered · running]" })).toBeUndefined();
});
