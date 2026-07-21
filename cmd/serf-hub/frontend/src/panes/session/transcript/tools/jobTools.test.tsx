import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { toolRendererFor } from "../toolRenderers";
import "./jobTools";
import "./subagentModule"; // registers `delegate` too - needed for the row-correlation tests below
import { resetSubagentModuleStoreForTests } from "./subagentModuleStore";
import type { ItemModel } from "../../../../protocol/model";

afterEach(() => {
  cleanup();
  resetSubagentModuleStoreForTests();
});

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

test("job_status: job_id reads straight from a settled item's own argumentsJSON (the model preserves it through item/completed - see R2)", () => {
  const d = toolRendererFor("job_status");
  const settled = item({ toolName: "job_status", argumentsJSON: JSON.stringify({ job_id: "job_99" }), output: "" });
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
  const d = toolRendererFor("job_watch");
  expect(d.summary(item({ id: "jw_2", toolName: "job_watch", argumentsJSON: "{}" }))).toBe("job_watch");
});

test("the generic job_* descriptor never wins over an exact match", () => {
  expect(toolRendererFor("job_stop")).not.toBe(toolRendererFor("job_watch"));
});

// --- correlating follow-up calls into the subagent module's existing row
// (never spawning a fresh row of their own - mirrors the legacy
// reconcileSubagent's identical "update only" rule) ------------------------

function spawnRow(turnId: string, jobId: string, transcriptRef: string) {
  const d = toolRendererFor("delegate");
  const Body = d.body!;
  render(
    <Body
      item={item({
        id: `spawn_${jobId}`,
        turnId,
        callId: `call_spawn_${jobId}`,
        toolName: "delegate",
        argumentsJSON: JSON.stringify({ task: "spawn" }),
        output: JSON.stringify({ job_id: jobId, status: "running", transcript_ref: transcriptRef }),
      })}
      live={false}
    />,
  );
}

test("job_status checking on a delegate spawned earlier in the SAME turn updates its existing row", () => {
  spawnRow("turn_wire_status", "job_wired", "ref_wired");
  expect(screen.getByTestId("subagent-row").dataset.kind).toBe("running");

  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const output = JSON.stringify({ job_id: "job_wired", status: "failed", reason: "crashed" });
  render(
    <Body
      item={item({
        id: "check_1",
        turnId: "turn_wire_status",
        callId: "call_check_1",
        toolName: "job_status",
        argumentsJSON: JSON.stringify({ job_id: "job_wired" }),
        output,
      })}
      live={false}
    />,
  );

  expect(screen.getByTestId("subagent-row").dataset.kind).toBe("failed");
});

test("job_stop's own footer status updates the existing row for its job_id", () => {
  spawnRow("turn_wire_stop", "job_stopme", "ref_stopme");

  const d = toolRendererFor("job_stop");
  const Body = d.body!;
  render(
    <Body
      item={item({
        id: "stop_1",
        turnId: "turn_wire_stop",
        callId: "call_stop_1",
        toolName: "job_stop",
        argumentsJSON: JSON.stringify({ job_id: "job_stopme" }),
        output: "[job job_stopme · cancelled · cancelled_by_request]",
      })}
      live={false}
    />,
  );

  expect(screen.getByTestId("subagent-row").dataset.kind).toBe("done"); // cancelled -> done, not failed
});

test("delegate_send checking on a delegate (by delegate_id) updates its existing row", () => {
  const d0 = toolRendererFor("delegate");
  const Body0 = d0.body!;
  render(
    <Body0
      item={item({
        id: "spawn_dlg",
        turnId: "turn_wire_send",
        callId: "call_spawn_dlg",
        toolName: "delegate",
        argumentsJSON: JSON.stringify({ task: "spawn" }),
        output: JSON.stringify({ delegate_id: "dlg_wired", job_id: "job_x", status: "running", transcript_ref: "ref_x" }),
      })}
      live={false}
    />,
  );

  const d = toolRendererFor("delegate_send");
  const Body = d.body!;
  render(
    <Body
      item={item({
        id: "send_1",
        turnId: "turn_wire_send",
        callId: "call_send_1",
        toolName: "delegate_send",
        argumentsJSON: JSON.stringify({ to: "dlg_wired", message: "status?" }),
        output: "on it\n[delegate_id dlg_wired · delivered · completed]",
      })}
      live={false}
    />,
  );

  expect(screen.getByTestId("subagent-row").dataset.kind).toBe("done");
});

test("a follow-up call for a job_id that was never spawned this turn creates no row at all", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  render(
    <Body
      item={item({
        id: "orphan_1",
        turnId: "turn_wire_orphan",
        callId: "call_orphan_1",
        toolName: "job_status",
        argumentsJSON: JSON.stringify({ job_id: "job_never_spawned" }),
        output: JSON.stringify({ job_id: "job_never_spawned", status: "completed" }),
      })}
      live={false}
    />,
  );
  expect(screen.queryByTestId("subagent-row")).toBeNull();
});
