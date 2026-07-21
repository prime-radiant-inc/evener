import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { toolRendererFor } from "../toolRenderers";
import "./jobTools";
import type { ItemModel } from "../../../../protocol/model";

afterEach(cleanup);

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

// --- job_status (+ legacy job_read_output alias) -------------------------
// Ground truth: agent/session_tools_jobs.go's jobStatusTool is the ONLY
// job_* tool whose Output is genuine whole-string JSON (marshalBoundedJSON)
// - registered as "job_status" (agent/internal/tool/definitions.go:245);
// "job_read_output" (DefJobReadOutput) exists but is never wired into
// registerJobToolsWithRegistrar, so it's kept only as a defensive alias
// for the parity checklist's legacy name / any not-yet-observed path.

test("job_status: summary reads job_id and status from the parsed JSON output", () => {
  const d = toolRendererFor("job_status");
  const args = JSON.stringify({ job_id: "job_42" });
  const output = JSON.stringify({ job_id: "job_42", status: "running", kind: "shell" });
  expect(d.summary(item({ toolName: "job_status", argumentsJSON: args, output }))).toBe(
    "Checked job_42 · running",
  );
});

test("job_status: falls back to the job_id arg with no status suffix when output isn't parseable yet (still running)", () => {
  const d = toolRendererFor("job_status");
  const args = JSON.stringify({ job_id: "job_43" });
  expect(d.summary(item({ toolName: "job_status", argumentsJSON: args, output: "" }))).toBe("Checked job_43");
});

test("job_status: body shows the raw JSON output text", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const output = JSON.stringify({ job_id: "job_42", status: "completed" });
  render(<Body item={item({ toolName: "job_status", output })} live={false} />);
  expect(screen.getByText(output)).toBeTruthy();
});

test("job_read_output aliases to the same descriptor as job_status", () => {
  expect(toolRendererFor("job_read_output")).toBe(toolRendererFor("job_status"));
});

test("job_status: job_id survives settlement when argumentsJSON goes missing, via rememberedArgs", () => {
  const d = toolRendererFor("job_status");
  const callId = "job_status_settle_1";
  d.summary(item({ toolName: "job_status", callId, argumentsJSON: JSON.stringify({ job_id: "job_99" }) }));
  const settled = item({ toolName: "job_status", callId, argumentsJSON: undefined, output: "" });
  expect(d.summary(settled)).toBe("Checked job_99");
});

// --- job_list ---------------------------------------------------------

test("job_list: summary is a plain label with no filter args", () => {
  const d = toolRendererFor("job_list");
  expect(d.summary(item({ toolName: "job_list", argumentsJSON: "{}", output: "No jobs.\n0 job(s)." }))).toBe(
    "Listed jobs",
  );
});

test("job_list: a status filter is mentioned in the summary", () => {
  const d = toolRendererFor("job_list");
  const args = JSON.stringify({ status: ["running", "failed"] });
  expect(d.summary(item({ toolName: "job_list", argumentsJSON: args, output: "" }))).toBe(
    "Listed jobs (running, failed)",
  );
});

test("job_list: body shows the tool's own formatted listing text", () => {
  const d = toolRendererFor("job_list");
  const Body = d.body!;
  render(<Body item={item({ toolName: "job_list", output: "# job_id  type  status\njob_1  shell  running\n1 job(s)." })} live={false} />);
  expect(screen.getByText(/1 job\(s\)\./)).toBeTruthy();
});

// --- job_stop -----------------------------------------------------------

test("job_stop: summary shows the target job and the tool's own outcome footer", () => {
  const d = toolRendererFor("job_stop");
  const args = JSON.stringify({ job_id: "job_7" });
  const output = "[job job_7 · cancelled · cancelled_by_request]";
  expect(d.summary(item({ toolName: "job_stop", argumentsJSON: args, output }))).toBe(
    "Stopped job_7 · job job_7 · cancelled · cancelled_by_request",
  );
});

test("job_stop: no footer yet (request in flight) shows just the target", () => {
  const d = toolRendererFor("job_stop");
  const args = JSON.stringify({ job_id: "job_8" });
  expect(d.summary(item({ toolName: "job_stop", argumentsJSON: args, output: "" }))).toBe("Stopped job_8");
});

// --- delegate_send (+ legacy job_send_message alias) ---------------------

test("delegate_send: summary shows the target delegate and the tool's own footer", () => {
  const d = toolRendererFor("delegate_send");
  const args = JSON.stringify({ to: "dlg_abc123", message: "status?" });
  const output = "on it\n[delegate_id dlg_abc123 · delivered · running]";
  expect(d.summary(item({ toolName: "delegate_send", argumentsJSON: args, output }))).toBe(
    "Messaged dlg_abc123 · delegate_id dlg_abc123 · delivered · running",
  );
});

test("job_send_message aliases to the same descriptor as delegate_send, reading its legacy `target` arg", () => {
  const delegateSend = toolRendererFor("delegate_send");
  const jobSendMessage = toolRendererFor("job_send_message");
  expect(jobSendMessage).toBe(delegateSend);
  const args = JSON.stringify({ target: "dlg_legacy", message: "hi" });
  expect(jobSendMessage.summary(item({ toolName: "job_send_message", argumentsJSON: args, output: "" }))).toBe(
    "Messaged dlg_legacy",
  );
});

// --- generic job_* family predicate (e.g. job_watch) ----------------------

test("an unlisted job_* tool falls to the generic family descriptor, mentioning its operation arg when present", () => {
  const d = toolRendererFor("job_watch");
  const args = JSON.stringify({ operation: "list" });
  expect(d.summary(item({ id: "jw_1", toolName: "job_watch", argumentsJSON: args }))).toBe("job_watch: list");
});

test("the generic job_* descriptor degrades to the bare tool name with no operation arg", () => {
  // A distinct id from the test above - rememberedArgs' cache is keyed by
  // callId/id, so two items sharing the default id would otherwise bleed
  // the previous test's cached args into this one.
  const d = toolRendererFor("job_watch");
  expect(d.summary(item({ id: "jw_2", toolName: "job_watch", argumentsJSON: "{}" }))).toBe("job_watch");
});

test("the generic job_* descriptor never wins over an exact match", () => {
  expect(toolRendererFor("job_stop")).not.toBe(toolRendererFor("job_watch"));
});
