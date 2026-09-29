// @vitest-environment node

import { expect, test } from "vitest";
import { subagentWireStep } from "./testing/subagentWireFixtures";
import { toolStepSummary, toolStepWords } from "./toolSummaries";

// The recorded delegate and delegate_send calls (agent/testdata/subagentwire),
// worded as the web words them: a delegate by its intent, a send by the
// delegate it messaged and the status its footer reports.
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
});
