import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { toolRendererFor } from "../toolRenderers";
import "./jobTools";
import "./subagentModule"; // registers `delegate` too - needed for the row-correlation tests below
import type { ItemModel } from "../../../../protocol/model";
import { resetSubagentModuleStoreForTests } from "./subagentModuleStore";

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
// "job_read_output" has neither a definition nor a registration, so it is
// kept only as a defensive alias for the parity checklist's legacy name and
// for stored transcripts that still carry such calls.

test("job_status: summary reads job_id and status from the parsed JSON output", () => {
  const d = toolRendererFor("job_status");
  const args = JSON.stringify({ job_id: "job_42" });
  const output = JSON.stringify({ job_id: "job_42", status: "running", kind: "shell" });
  expect(d.summary(item({ toolName: "job_status", argumentsJSON: args, output }))).toBe("Checked job_42 · running");
});

test("job_status: falls back to the job_id arg with no status suffix when output isn't parseable yet (still running)", () => {
  const d = toolRendererFor("job_status");
  const args = JSON.stringify({ job_id: "job_43" });
  expect(d.summary(item({ toolName: "job_status", argumentsJSON: args, output: "" }))).toBe("Checked job_43");
});

test("job controls keep same-owner job suffixes distinct in summaries", () => {
  const owner = "02wMz5TxvEMoJEDTDGOTil";
  const first = `job_${owner}_000000000001`;
  const second = `job_${owner}_000000000002`;
  for (const toolName of ["job_status", "job_stop"] as const) {
    const d = toolRendererFor(toolName);
    const summary = (jobID: string) =>
      d.summary(item({ toolName, argumentsJSON: JSON.stringify({ job_id: jobID }), output: "" }));
    expect(summary(first)).toContain("000000000001");
    expect(summary(second)).toContain("000000000002");
    expect(summary(first)).not.toBe(summary(second));
  }
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
  render(
    <Body
      item={item({ toolName: "job_list", output: "# job_id  type  status\njob_1  shell  running\n1 job(s)." })}
      live={false}
    />,
  );
  expect(screen.getByText(/1 job\(s\)\./)).toBeTruthy();
});

test("job_list: body renders stable direct raw state when the producer supplies it", () => {
  const d = toolRendererFor("job_list");
  const Body = d.body!;
  render(
    <Body
      item={item({
        toolName: "job_list",
        output: "formatted listing text",
        raw: {
          jobs: [
            {
              job_id: "job_raw",
              type: "shell",
              status: "running",
              phase: "compiling",
              description: "build the frontend",
            },
          ],
          count: 1,
          total: 1,
        },
      })}
      live={false}
    />,
  );
  expect(screen.getByTestId("job-list-structured")).toBeTruthy();
  expect(screen.getByText(/job_raw/)).toBeTruthy();
  expect(screen.getByText(/compiling/)).toBeTruthy();
  expect(screen.getByText(/build the frontend/)).toBeTruthy();
  expect(screen.queryByText("formatted listing text")).toBeNull();
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

function renderDelegateSendBody({
  toolName = "delegate_send",
  argumentsJSON = JSON.stringify({ to: "dlg_abc123", message: "Inspect the parser.\nReport exact findings." }),
  output = "Found two call sites.\nBoth need coverage.\n[delegate_id dlg_abc123 · delivered · completed]",
  raw,
}: {
  toolName?: "delegate_send" | "job_send_message";
  argumentsJSON?: string;
  output?: string;
  raw?: unknown;
} = {}) {
  const Body = toolRendererFor(toolName).body!;
  render(
    <Body
      item={item({
        toolName,
        argumentsJSON,
        output,
        raw,
      })}
      live={false}
    />,
  );
}

test("delegate_send: expanded body shows the complete sent message and response without repeated status metadata", () => {
  renderDelegateSendBody();

  expect(screen.getByText("Message")).toBeTruthy();
  expect(screen.getByTestId("delegate-send-message").textContent).toBe("Inspect the parser.\nReport exact findings.");
  expect(screen.getByText("Response")).toBeTruthy();
  expect(screen.getByTestId("delegate-send-response").textContent).toBe("Found two call sites.\nBoth need coverage.");
  expect(screen.queryByText(/delegate_id dlg_abc123 · delivered · completed/)).toBeNull();
});

test("delegate_send: canonical raw output preserves the delegate response when formatted output has trailing metadata", () => {
  renderDelegateSendBody({
    output:
      'Exact response\n[delegate_id dlg_abc123 · delivered · completed]\nstructured_result (valid=true): {"ok":true}',
    raw: { output: "Exact response", status: "completed", action: "delivered" },
  });

  expect(screen.getByTestId("delegate-send-response").textContent).toBe("Exact response");
  expect(screen.queryByText(/structured_result/)).toBeNull();
});

test("delegate_send: footer-only and in-flight calls omit the Response section", () => {
  const Body = toolRendererFor("delegate_send").body!;

  const { rerender } = render(
    <Body
      item={item({
        toolName: "delegate_send",
        argumentsJSON: JSON.stringify({ to: "dlg_abc123", message: "status?" }),
        output: "[delegate_id dlg_abc123 · delivered · running]",
      })}
      live={false}
    />,
  );
  expect(screen.queryByText("Response")).toBeNull();

  rerender(
    <Body
      item={item({
        toolName: "delegate_send",
        argumentsJSON: JSON.stringify({ to: "dlg_abc123", message: "status?" }),
        output: "",
      })}
      live={true}
    />,
  );
  expect(screen.queryByText("Response")).toBeNull();
});

test("delegate_send: unrecognized output remains visible as the response", () => {
  renderDelegateSendBody({ output: "historical result without a recognized footer" });
  expect(screen.getByTestId("delegate-send-response").textContent).toBe(
    "historical result without a recognized footer",
  );
});

test("delegate_send: malformed or missing message arguments omit Message without hiding a response", () => {
  renderDelegateSendBody({ argumentsJSON: "not json", output: "reply from historical data" });
  expect(screen.queryByText("Message")).toBeNull();
  expect(screen.getByTestId("delegate-send-response").textContent).toBe("reply from historical data");
});

test("job_send_message: expanded body reads the legacy target shape and shows message and response", () => {
  renderDelegateSendBody({
    toolName: "job_send_message",
    argumentsJSON: JSON.stringify({ target: "dlg_legacy", message: "continue" }),
    output: "continuing\n[delegate_id dlg_legacy · delivered · running]",
  });

  expect(screen.getByTestId("delegate-send-message").textContent).toBe("continue");
  expect(screen.getByTestId("delegate-send-response").textContent).toBe("continuing");
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

  expect(screen.getByTestId("subagent-row").dataset.kind).toBe("stopped"); // cancelled -> stopped: not a failure, but not a clean done either (3zf8)
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
        output: JSON.stringify({
          delegate_id: "dlg_wired",
          job_id: "job_x",
          status: "running",
          transcript_ref: "ref_x",
        }),
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
  expect(screen.getByTestId("delegate-send-message").textContent).toBe("status?");
  expect(screen.getByTestId("delegate-send-response").textContent).toBe("on it");
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
