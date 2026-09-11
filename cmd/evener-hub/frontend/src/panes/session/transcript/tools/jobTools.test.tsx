import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { toolRendererFor } from "../toolRenderers";
import "./jobTools";
import "./jobWatch";
import type { ItemModel } from "../../../../protocol/model";

afterEach(() => {
  cleanup();
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

test("job_status: summary reads current target/id and lifecycle status", () => {
  const d = toolRendererFor("job_status");
  const args = JSON.stringify({ target: "dlg_42" });
  const output = JSON.stringify({ id: "dlg_42", type: "delegate", status: "idle" });
  expect(d.summary(item({ toolName: "job_status", argumentsJSON: args, output }))).toBe("Checked dlg_42 · idle");
});

test("job_status: falls back to the target arg with no status suffix when output isn't parseable yet", () => {
  const d = toolRendererFor("job_status");
  const args = JSON.stringify({ target: "job_43" });
  expect(d.summary(item({ toolName: "job_status", argumentsJSON: args, output: "" }))).toBe("Checked job_43");
});

test("job controls keep same-owner job suffixes distinct in summaries", () => {
  const owner = "02wMz5TxvEMoJEDTDGOTil";
  const first = `job_${owner}_000000000001`;
  const second = `job_${owner}_000000000002`;
  for (const toolName of ["job_status", "job_stop"] as const) {
    const d = toolRendererFor(toolName);
    const summary = (jobID: string) =>
      d.summary(item({ toolName, argumentsJSON: JSON.stringify({ target: jobID }), output: "" }));
    expect(summary(first)).toContain("000000000001");
    expect(summary(second)).toContain("000000000002");
    expect(summary(first)).not.toBe(summary(second));
  }
});

test("job_status: body falls back to raw text when item.raw is absent (legacy/stored transcript)", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const output = JSON.stringify({ id: "dlg_42", type: "delegate", status: "idle" });
  render(<Body item={item({ toolName: "job_status", output })} live={false} />);
  expect(screen.getByText(output)).toBeTruthy();
});

test("job_status: body falls back to raw text when item.raw is not a delegate status (shell job)", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const output = JSON.stringify({
    job_id: "job_99",
    kind: "shell",
    status: "completed",
    started_at: "2026-01-01T00:00:00Z",
  });
  render(<Body item={item({ toolName: "job_status", output, raw: JSON.parse(output) })} live={false} />);
  // Shell job output has no id+status delegate shape, so the structured body
  // is bypassed and the raw text renders through HeadClippedOutputBody.
  expect(screen.getByText(output)).toBeTruthy();
});

// --- job_status: structured delegate status body (DelegateStatusBody) ---
// When item.raw carries a stableDelegateStatusResult (the State field of
// tool.StateResult from agent/session_tools_jobs.go's
// stableDelegateStatusTool), the body renders a structured card instead of
// raw JSON.

function delegateStatusRaw(overrides: Record<string, unknown> = {}): unknown {
  return {
    id: "dlg_034HQ2kSDXfKFq1mm3idL1",
    type: "delegate",
    status: "running",
    task: "You are implementing Task 2: Strict resource-schema validation.",
    agent_type: "subagent",
    tools: ["apply_patch", "communicate", "exec_command", "read_file", "write_file"],
    model: "gpt-5.6-sol",
    reasoning_effort: "high",
    resumable: true,
    needs_attention: false,
    transcript_ref: "local:034HzkSDXiDAIUBDoff1h",
    running_for_ms: 103652,
    quiet_for_ms: 617,
    run_started_at: "2026-09-02T19:10:07.136326Z",
    last_outcome: { status: "completed", ended_at: "2026-09-01T14:03:21.048695-07:00" },
    cwd: "/Users/jesse/git/prime-radiant/evener",
    isolation: "worktree",
    sandbox_mode: "workspace-write",
    sandbox_network: true,
    ...overrides,
  };
}

test("job_status: body renders a structured card with the delegate ID", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const raw = delegateStatusRaw();
  const output = JSON.stringify(raw);
  render(<Body item={item({ toolName: "job_status", output, raw })} live={false} />);
  expect(screen.getByText("dlg_034HQ2kSDXfKFq1mm3idL1")).toBeTruthy();
});

test("job_status: body shows a running status pill", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const raw = delegateStatusRaw();
  const { container } = render(
    <Body item={item({ toolName: "job_status", output: JSON.stringify(raw), raw })} live={false} />,
  );
  // Chip renders "running" as its label text. The lifecycle meta line also
  // contains "running" as a key, so query the Chip widget's span directly.
  const chip = container.querySelector("[class*='chip']");
  expect(chip?.textContent).toContain("running");
});

test("job_status: body shows needs attention pill when needs_attention is true", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const raw = delegateStatusRaw({ needs_attention: true });
  render(<Body item={item({ toolName: "job_status", output: JSON.stringify(raw), raw })} live={false} />);
  expect(screen.getByText("Needs attention")).toBeTruthy();
});

test("job_status: idle status does not receive the alive/running tone", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const raw = delegateStatusRaw({ status: "idle", running_for_ms: undefined, quiet_for_ms: undefined });
  const { container } = render(
    <Body item={item({ toolName: "job_status", output: JSON.stringify(raw), raw })} live={false} />,
  );
  // The Chip should render "idle" text in the neutral tone, not alive/running.
  const chip = container.querySelector("[class*='chip']");
  expect(chip?.textContent).toContain("idle");
  expect(chip?.className).not.toContain("alive");
});

test("job_status: idle delegate with failed last outcome shows danger tone", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const raw = delegateStatusRaw({
    status: "idle",
    running_for_ms: undefined,
    quiet_for_ms: undefined,
    last_outcome: { status: "failed", ended_at: "2026-09-01T14:03:21Z" },
  });
  const { container } = render(
    <Body item={item({ toolName: "job_status", output: JSON.stringify(raw), raw })} live={false} />,
  );
  const chip = container.querySelector("[class*='chip']");
  expect(chip?.textContent).toContain("idle");
  expect(chip?.className).toContain("danger");
});

test("job_status: body renders the mandate (first paragraph of task)", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const raw = delegateStatusRaw();
  render(<Body item={item({ toolName: "job_status", output: JSON.stringify(raw), raw })} live={false} />);
  expect(screen.getByText(/Strict resource-schema validation/)).toBeTruthy();
});

test("job_status: body shows a disclosure for a multi-paragraph mandate", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const raw = delegateStatusRaw({
    task: "First paragraph of the mandate.\n\nSecond paragraph with more detail.\n\nThird paragraph.",
  });
  render(<Body item={item({ toolName: "job_status", output: JSON.stringify(raw), raw })} live={false} />);
  expect(screen.getByText("First paragraph of the mandate.")).toBeTruthy();
  expect(screen.getByText("Show full mandate")).toBeTruthy();
});

test("job_status: body renders available tools with a count", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const raw = delegateStatusRaw();
  render(<Body item={item({ toolName: "job_status", output: JSON.stringify(raw), raw })} live={false} />);
  expect(screen.getByText("Available tools (5)")).toBeTruthy();
  expect(screen.getByText("apply_patch")).toBeTruthy();
  expect(screen.getByText("write_file")).toBeTruthy();
});

test("job_status: body renders environment fields (cwd, isolation, sandbox, network)", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const raw = delegateStatusRaw();
  render(<Body item={item({ toolName: "job_status", output: JSON.stringify(raw), raw })} live={false} />);
  expect(screen.getByText("/Users/jesse/git/prime-radiant/evener")).toBeTruthy();
  expect(screen.getByText("worktree")).toBeTruthy();
  expect(screen.getByText("workspace-write")).toBeTruthy();
  expect(screen.getByText("enabled")).toBeTruthy();
});

test("job_status: body renders config line with model, agent, reasoning", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const raw = delegateStatusRaw();
  render(<Body item={item({ toolName: "job_status", output: JSON.stringify(raw), raw })} live={false} />);
  expect(screen.getByText("gpt-5.6-sol")).toBeTruthy();
  expect(screen.getByText("subagent")).toBeTruthy();
  expect(screen.getByText("high")).toBeTruthy();
});

test("job_status: body renders a copy raw JSON button", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const raw = delegateStatusRaw();
  render(<Body item={item({ toolName: "job_status", output: JSON.stringify(raw), raw })} live={false} />);
  expect(screen.getByRole("button", { name: "Copy raw JSON" })).toBeTruthy();
});

test("job_status: copy button writes the normalized structured state, not item.output", async () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const raw = delegateStatusRaw();
  // output carries trailing annotation text after the JSON — the copy button
  // must write the validated state, not this raw string.
  const outputWithJunk = `${JSON.stringify(raw)}\n--- breaker ---`;
  const writeText = vi.fn().mockResolvedValue(undefined);
  const originalClipboard = navigator.clipboard;
  Object.defineProperty(navigator, "clipboard", {
    value: { writeText },
    configurable: true,
  });
  try {
    render(<Body item={item({ toolName: "job_status", output: outputWithJunk, raw })} live={false} />);
    const btn = screen.getByRole("button", { name: "Copy raw JSON" });
    btn.click();
    expect(writeText).toHaveBeenCalledTimes(1);
    // The copied text must be valid JSON of the structured state, not the
    // raw output string (which has trailing junk). Compare parsed values
    // (key ordering is not significant).
    const copied = writeText.mock.calls[0]![0] as string;
    expect(JSON.parse(copied)).toEqual(raw);
    expect(copied).not.toContain("--- breaker ---");
  } finally {
    Object.defineProperty(navigator, "clipboard", {
      value: originalClipboard,
      configurable: true,
    });
  }
});

test("job_status: body renders not-resumable reason diagnostic", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const raw = delegateStatusRaw({ not_resumable_reason: "Delegate was disposed" });
  render(<Body item={item({ toolName: "job_status", output: JSON.stringify(raw), raw })} live={false} />);
  expect(screen.getByTestId("delegate-not-resumable")).toBeTruthy();
  expect(screen.getByText(/Not resumable: Delegate was disposed/)).toBeTruthy();
});

test("job_status: body renders failed outcome reason in danger text", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const raw = delegateStatusRaw({
    status: "idle",
    last_outcome: { status: "failed", reason: "exec: command not found" },
  });
  render(<Body item={item({ toolName: "job_status", output: JSON.stringify(raw), raw })} live={false} />);
  expect(screen.getByTestId("delegate-outcome-reason")).toBeTruthy();
  expect(screen.getByText(/Last run failed: exec: command not found/)).toBeTruthy();
});

test("job_status: body renders exhausted outcome reason without danger text", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const raw = delegateStatusRaw({
    status: "idle",
    last_outcome: { status: "exhausted", reason: "budget limit reached" },
  });
  render(<Body item={item({ toolName: "job_status", output: JSON.stringify(raw), raw })} live={false} />);
  const diag = screen.getByTestId("delegate-outcome-reason");
  expect(diag).toBeTruthy();
  expect(screen.getByText(/Last run exhausted: budget limit reached/)).toBeTruthy();
  // Soft stops are not errors — the diagnostic must not carry the danger class.
  expect(diag.querySelector("[class]")?.className).not.toMatch(/dangerText/);
});

test("job_status: body renders stopped outcome reason without danger text", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const raw = delegateStatusRaw({
    status: "idle",
    last_outcome: { status: "stopped", reason: "stopped_by_parent" },
  });
  render(<Body item={item({ toolName: "job_status", output: JSON.stringify(raw), raw })} live={false} />);
  const diag = screen.getByTestId("delegate-outcome-reason");
  expect(diag).toBeTruthy();
  expect(screen.getByText(/Last run stopped: stopped_by_parent/)).toBeTruthy();
  expect(diag.querySelector("[class]")?.className).not.toMatch(/dangerText/);
});

test("job_status: body renders cancelled outcome with cancelled label", () => {
  const d = toolRendererFor("job_status");
  const Body = d.body!;
  const raw = delegateStatusRaw({
    status: "idle",
    last_outcome: { status: "cancelled", reason: "user requested cancel" },
  });
  render(<Body item={item({ toolName: "job_status", output: JSON.stringify(raw), raw })} live={false} />);
  const diag = screen.getByTestId("delegate-outcome-reason");
  expect(diag).toBeTruthy();
  expect(screen.getByText(/Last run cancelled: user requested cancel/)).toBeTruthy();
  expect(diag.querySelector("[class]")?.className).not.toMatch(/dangerText/);
});

test("job_read_output aliases to the same descriptor as job_status", () => {
  expect(toolRendererFor("job_read_output")).toBe(toolRendererFor("job_status"));
});

test("job_status: target reads straight from a settled item's own argumentsJSON", () => {
  const d = toolRendererFor("job_status");
  const settled = item({ toolName: "job_status", argumentsJSON: JSON.stringify({ target: "job_99" }), output: "" });
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
      item={item({ toolName: "job_list", output: "# id  type  status\njob_1  shell  running\n1 job(s)." })}
      live={false}
    />,
  );
  expect(screen.getByText(/1 job\(s\)\./)).toBeTruthy();
});

test("stored job_list raw jobs/job_id state remains readable", () => {
  const d = toolRendererFor("job_list");
  const Body = d.body!;
  render(
    <Body
      item={item({
        toolName: "job_list",
        output: "stored listing text",
        raw: {
          jobs: [{ job_id: "job_stored", type: "shell", status: "completed" }],
          total: 1,
        },
      })}
      live={false}
    />,
  );
  expect(screen.getByText(/job_stored/)).toBeTruthy();
  expect(screen.queryByText("stored listing text")).toBeNull();
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
          items: [
            {
              id: "dlg_raw",
              type: "delegate",
              status: "idle",
              phase: "idle",
              description: "inspect the frontend",
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
  expect(screen.getByText(/dlg_raw/)).toBeTruthy();
  expect(screen.getByText(/idle/)).toBeTruthy();
  expect(screen.getByText(/inspect the frontend/)).toBeTruthy();
  expect(screen.queryByText("formatted listing text")).toBeNull();
});

// --- job_stop -----------------------------------------------------------

test("job_stop: summary shows the target job and the tool's own outcome footer", () => {
  const d = toolRendererFor("job_stop");
  const args = JSON.stringify({ target: "job_7" });
  const output = "[job job_7 · cancelled · cancelled_by_request]";
  expect(d.summary(item({ toolName: "job_stop", argumentsJSON: args, output }))).toBe(
    "Stopped job_7 · job job_7 · cancelled · cancelled_by_request",
  );
});

test("job_stop: no footer yet (request in flight) shows just the target", () => {
  const d = toolRendererFor("job_stop");
  const args = JSON.stringify({ target: "job_8" });
  expect(d.summary(item({ toolName: "job_stop", argumentsJSON: args, output: "" }))).toBe("Stopped job_8");
});

// --- delegate_send (+ legacy job_send_message alias) ---------------------

test("delegate_send: summary names the target delegate and a one-word status, not the raw footer", () => {
  const d = toolRendererFor("delegate_send");
  const args = JSON.stringify({ to: "dlg_abc123", message: "status?" });
  const output = "on it\n[delegate_id dlg_abc123 · delivered · running]";
  expect(d.summary(item({ toolName: "delegate_send", argumentsJSON: args, output }))).toBe(
    "Sent a message to delegate dlg_abc123 · running",
  );
});

test("delegate_send: summary omits the status segment when there is no footer yet (in flight)", () => {
  const d = toolRendererFor("delegate_send");
  const args = JSON.stringify({ to: "dlg_abc123", message: "status?" });
  expect(d.summary(item({ toolName: "delegate_send", argumentsJSON: args, output: "" }))).toBe(
    "Sent a message to delegate dlg_abc123",
  );
});

test("delegate_send: summary degrades gracefully with no target arg", () => {
  const d = toolRendererFor("delegate_send");
  expect(d.summary(item({ toolName: "delegate_send", argumentsJSON: "{}", output: "" }))).toBe(
    "Sent a message to a delegate",
  );
});

test("delegate_send: openTranscriptInline anchors the control between the delegate target and the status meta", () => {
  const d = toolRendererFor("delegate_send");
  const args = JSON.stringify({ to: "dlg_abc123", message: "status?" });
  const output = "on it\n[delegate_id dlg_abc123 · delivered · running]";
  const it = item({
    toolName: "delegate_send",
    argumentsJSON: args,
    output,
    raw: { action: "steered", running_in_background: true, transcript_ref: "local:child1" },
  });
  const anchor = d.openTranscriptInline?.(it);
  expect(anchor).toBe("Sent a message to delegate dlg_abc123");
  const summary = d.summary(it);
  expect(summary.startsWith(anchor!)).toBe(true);
});

test("delegate_send: openTranscriptInline falls back to the full summary when there is no transcript to open", () => {
  const d = toolRendererFor("delegate_send");
  const args = JSON.stringify({ to: "dlg_abc123", message: "status?" });
  const it = item({ toolName: "delegate_send", argumentsJSON: args, output: "" });
  expect(d.openTranscriptRef?.(it)).toBeUndefined();
  expect(d.openTranscriptInline?.(it)).toBeUndefined();
});

test("delegate_send: openTranscriptRef reads transcript_ref from valid raw state", () => {
  const d = toolRendererFor("delegate_send");
  const it = item({
    toolName: "delegate_send",
    raw: { action: "steered", running_in_background: true, transcript_ref: "local:child1" },
  });
  expect(d.openTranscriptRef?.(it)).toBe("local:child1");
});

test("delegate_send: openTranscriptRef is undefined for absent, malformed, or blank-ref raw state", () => {
  const d = toolRendererFor("delegate_send");
  expect(d.openTranscriptRef?.(item({ toolName: "delegate_send" }))).toBeUndefined();
  // Missing running_in_background: not a valid delegateSendResult at all.
  expect(
    d.openTranscriptRef?.(item({ toolName: "delegate_send", raw: { action: "steered", transcript_ref: "local:c" } })),
  ).toBeUndefined();
  expect(
    d.openTranscriptRef?.(
      item({
        toolName: "delegate_send",
        raw: { action: "steered", running_in_background: true, transcript_ref: "  " },
      }),
    ),
  ).toBeUndefined();
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

test("delegate_send: expanded body renders the sent message as an outgoing chat bubble and the reply as an incoming one", () => {
  renderDelegateSendBody();

  const outgoing = screen.getByTestId("delegate-send-message");
  expect(within(outgoing).getByText("Agent → dlg_abc123")).toBeTruthy();
  expect(within(outgoing).getByTestId("speaker-avatar")).toBeTruthy();
  expect(within(outgoing).getByTestId("user-bubble").textContent).toBe("Inspect the parser.\nReport exact findings.");
  expect(within(outgoing).getByRole("button", { name: "Copy message" })).toBeTruthy();

  const reply = screen.getByTestId("delegate-send-response");
  expect(within(reply).getByText("dlg_abc123 (delegate)")).toBeTruthy();
  expect(within(reply).getByTestId("user-bubble").textContent).toBe("Found two call sites.\nBoth need coverage.");
  expect(within(reply).getByRole("button", { name: "Copy response" })).toBeTruthy();

  expect(screen.queryByText(/delegate_id dlg_abc123 · delivered · completed/)).toBeNull();
});

test("delegate_send: canonical raw output preserves the delegate response when formatted output has trailing metadata", () => {
  renderDelegateSendBody({
    output:
      'Exact response\n[delegate_id dlg_abc123 · delivered · completed]\nstructured_result (valid=true): {"ok":true}',
    raw: { output: "Exact response", status: "completed", action: "delivered" },
  });

  expect(within(screen.getByTestId("delegate-send-response")).getByTestId("user-bubble").textContent).toBe(
    "Exact response",
  );
  expect(screen.queryByText(/structured_result/)).toBeNull();
});

test("delegate_send: malformed raw output falls back to the formatted response", () => {
  renderDelegateSendBody({
    output: "formatted response\n[delegate_id dlg_abc123 · delivered · completed]",
    raw: { output: "raw response" },
  });

  expect(within(screen.getByTestId("delegate-send-response")).getByTestId("user-bubble").textContent).toBe(
    "formatted response",
  );
});

test("delegate_send: whitespace-only canonical output is treated as absent", () => {
  renderDelegateSendBody({
    output: "formatted response\n[delegate_id dlg_abc123 · delivered · completed]",
    raw: { action: "delivered", running_in_background: false, output: " \n\t" },
  });

  expect(within(screen.getByTestId("delegate-send-response")).getByTestId("user-bubble").textContent).toBe(
    "formatted response",
  );
});

test("delegate_send: raw whitespace-only output is omitted when there is no formatted response", () => {
  renderDelegateSendBody({
    output: "",
    raw: { action: "delivered", running_in_background: false, output: " \n\t" },
  });

  expect(screen.queryByTestId("delegate-send-response")).toBeNull();
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
  expect(screen.queryByTestId("delegate-send-response")).toBeNull();

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
  expect(screen.queryByTestId("delegate-send-response")).toBeNull();
});

test("delegate_send: unrecognized output remains visible as the response", () => {
  renderDelegateSendBody({ output: "historical result without a recognized footer" });
  expect(within(screen.getByTestId("delegate-send-response")).getByTestId("user-bubble").textContent).toBe(
    "historical result without a recognized footer",
  );
});

test("delegate_send: footer-like response content without separator-delimited fields is preserved", () => {
  renderDelegateSendBody({ output: "[delegate_id this is response text]" });
  expect(within(screen.getByTestId("delegate-send-response")).getByTestId("user-bubble").textContent).toBe(
    "[delegate_id this is response text]",
  );
});

test("delegate_send: a complete delegate footer is stripped from the response text", () => {
  renderDelegateSendBody({ output: "reply from historical data\n[delegate_id dlg_abc123 · delivered · completed]" });
  expect(within(screen.getByTestId("delegate-send-response")).getByTestId("user-bubble").textContent).toBe(
    "reply from historical data",
  );
});

test("delegate_send preserves wait_ignored_reason in result rendering without inventing a delegate job", () => {
  const descriptor = toolRendererFor("delegate_send");
  const settled = item({
    toolName: "delegate_send",
    argumentsJSON: JSON.stringify({ to: "dlg_wait", message: "status?", wait: true }),
    raw: {
      delegate_id: "dlg_wait",
      action: "steered",
      running_in_background: true,
      wait_ignored_reason: "delegate is already running",
      transcript_ref: "local:sess_child",
    },
    output:
      "[delegate_id dlg_wait · steered · running · running in background · wait ignored: delegate is already running]",
  });

  expect(descriptor.summary(settled)).toContain("running");
  const Body = descriptor.body!;
  render(<Body item={settled} live={false} />);
  expect(screen.getByText(/wait ignored: delegate is already running/i)).toBeTruthy();
  expect(screen.queryByText(/started_job_id/i)).toBeNull();
});

test("delegate_send: malformed or missing message arguments omit Message without hiding a response", () => {
  renderDelegateSendBody({ argumentsJSON: "not json", output: "reply from historical data" });
  expect(screen.queryByTestId("delegate-send-message")).toBeNull();
  expect(within(screen.getByTestId("delegate-send-response")).getByTestId("user-bubble").textContent).toBe(
    "reply from historical data",
  );
});

test("job_send_message: expanded body reads the legacy target shape and shows message and response", () => {
  renderDelegateSendBody({
    toolName: "job_send_message",
    argumentsJSON: JSON.stringify({ target: "dlg_legacy", message: "continue" }),
    output: "continuing\n[delegate_id dlg_legacy · delivered · running]",
  });

  expect(within(screen.getByTestId("delegate-send-message")).getByTestId("user-bubble").textContent).toBe("continue");
  expect(within(screen.getByTestId("delegate-send-response")).getByTestId("user-bubble").textContent).toBe(
    "continuing",
  );
});

test("job_send_message aliases to the same descriptor as delegate_send, reading its legacy `target` arg", () => {
  const delegateSend = toolRendererFor("delegate_send");
  const jobSendMessage = toolRendererFor("job_send_message");
  expect(jobSendMessage).toBe(delegateSend);
  const args = JSON.stringify({ target: "dlg_legacy", message: "hi" });
  expect(jobSendMessage.summary(item({ toolName: "job_send_message", argumentsJSON: args, output: "" }))).toBe(
    "Sent a message to delegate dlg_legacy",
  );
});

// --- generic job_* family predicate (job_watch has its own descriptor) ----
// job_watch's exact-match descriptor lives in jobWatch.tsx (mockups
// 23-job-watch §A-D); the family predicate below still covers every OTHER
// unlisted job_* name. These pin the predicate with a name no exact
// descriptor claims, plus the exact-wins-over-predicate precedence rule.

test("an unlisted job_* tool falls to the generic family descriptor, mentioning its operation arg when present", () => {
  const d = toolRendererFor("job_zzz_unlisted");
  const args = JSON.stringify({ operation: "frobnicate" });
  expect(d.summary(item({ id: "jw_1", toolName: "job_zzz_unlisted", argumentsJSON: args }))).toBe(
    "job_zzz_unlisted: frobnicate",
  );
});

test("the generic job_* descriptor degrades to the bare tool name with no operation arg", () => {
  const d = toolRendererFor("job_zzz_unlisted");
  expect(d.summary(item({ id: "jw_2", toolName: "job_zzz_unlisted", argumentsJSON: "{}" }))).toBe("job_zzz_unlisted");
});

test("the generic job_* descriptor never wins over an exact match", () => {
  expect(toolRendererFor("job_stop")).not.toBe(toolRendererFor("job_zzz_unlisted"));
});

test("job_watch resolves to its own descriptor, not the generic family fallback", () => {
  expect(toolRendererFor("job_watch")).not.toBe(toolRendererFor("job_zzz_unlisted"));
});
