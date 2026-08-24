// Edge cases for jobTools.tsx that close remaining uncovered lines:
// - CopyTextButton CopiedGlyph rendering (line 32)
// - CopyTextButton timer effect (lines 47-48)
// - CopyTextButton clipboard guard (lines 59-60)
// - delegateSendFooter with started_job_id empty (lines 255-256)
// - delegateSendFooter with watching field (line 271)

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import type { ItemModel } from "../../../../protocol/model";
import { toolRendererFor } from "../toolRenderers";
import "./jobTools";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

// --- CopyTextButton: CopiedGlyph (line 32), timer effect (lines 47-48) ---
// CopyTextButton lives in the delegate_send body (UserMessageView actions slot),
// NOT in the job_list body. We render the delegate_send body as JSX with proper
// ToolRenderProps so the component receives { item, live } correctly.

test("delegate_send copy button shows Copied feedback after successful clipboard write", async () => {
  const user = userEvent.setup();
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { clipboard: { writeText } });

  const d = toolRendererFor("delegate_send");
  const Body = d.body!;
  render(
    <Body
      item={item({
        toolName: "delegate_send",
        argumentsJSON: JSON.stringify({ message: "hello world" }),
        output: "",
      })}
      live={false}
    />,
  );

  const copyButton = screen.getByRole("button", { name: /copy message/i });
  await user.click(copyButton);

  await waitFor(() => {
    expect(screen.getByRole("button", { name: "Copied" })).toBeTruthy();
  });
});

// --- CopyTextButton: clipboard guard (lines 59-60) ---

test("delegate_send copy button is a no-op when clipboard API is unavailable", async () => {
  const user = userEvent.setup();
  vi.stubGlobal("navigator", {});

  const d = toolRendererFor("delegate_send");
  const Body = d.body!;
  render(
    <Body
      item={item({
        toolName: "delegate_send",
        argumentsJSON: JSON.stringify({ message: "hello world" }),
        output: "",
      })}
      live={false}
    />,
  );

  const copyButton = screen.getByRole("button", { name: /copy message/i });
  await user.click(copyButton);

  // No "Copied" feedback
  expect(screen.queryByRole("button", { name: "Copied" })).toBeNull();
});

// --- CopyTextButton timer effect (line 47) ---

test("delegate_send copy button resets Copied state after timeout", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { clipboard: { writeText } });

  const d = toolRendererFor("delegate_send");
  const Body = d.body!;
  render(
    <Body
      item={item({
        toolName: "delegate_send",
        argumentsJSON: JSON.stringify({ message: "hello world" }),
        output: "",
      })}
      live={false}
    />,
  );

  const copyButton = screen.getByRole("button", { name: /copy message/i });
  await new Promise<void>((resolve) => {
    copyButton.onclick = () => resolve();
    copyButton.click();
  });

  // Let the promise microtask resolve
  await vi.waitFor(() => {
    expect(screen.getByRole("button", { name: "Copied" })).toBeTruthy();
  });

  // Advance past the 2s reset timer to trigger cleanup (line 48)
  vi.advanceTimersByTime(2100);

  await vi.waitFor(() => {
    expect(screen.queryByRole("button", { name: "Copied" })).toBeNull();
  });
  vi.useRealTimers();
});

// --- jobListState early returns (lines 81, 87) ---

test("job_list body with no items or jobs array falls back to raw output", () => {
  const d = toolRendererFor("job_list");
  const Body = d.body!;
  render(
    <Body
      item={item({
        toolName: "job_list",
        argumentsJSON: JSON.stringify({ target: "dlg_1" }),
        output: '{"status":"ok"}',
        raw: { status: "ok" },
      })}
      live={false}
    />,
  );
  // Falls back to HeadClippedOutputBody which shows output
  expect(screen.getByText('{"status":"ok"}')).toBeTruthy();
});

test("job_list body with non-string id in item falls back to raw output", () => {
  const d = toolRendererFor("job_list");
  const Body = d.body!;
  render(
    <Body
      item={item({
        toolName: "job_list",
        argumentsJSON: JSON.stringify({ target: "dlg_1" }),
        output: '{"items":[{"id":123}]}',
        raw: { items: [{ id: 123 }] },
      })}
      live={false}
    />,
  );
  // Falls back to HeadClippedOutputBody
  expect(screen.getByText('{"items":[{"id":123}]}')).toBeTruthy();
});

// --- JobListBody row with undefined identity (line 112) ---

test("job_list body with item having empty id string renders no row for it", () => {
  const d = toolRendererFor("job_list");
  const Body = d.body!;
  render(
    <Body
      item={item({
        toolName: "job_list",
        argumentsJSON: JSON.stringify({ target: "dlg_1" }),
        output: "",
        raw: {
          items: [
            { id: "job_1", type: "shell" },
            { id: "", description: "no id" },
          ],
          count: 2,
          total: 2,
        },
      })}
      live={false}
    />,
  );
  // Should render one row (the one with non-empty id), not the one with empty id
  const rows = screen.getAllByTestId("job-list-row");
  expect(rows).toHaveLength(1);
  expect(rows[0]?.textContent).toContain("job_1");
});

// --- delegateSendFooter early returns (lines 245, 246, 250) ---

test("delegate_send footer without delegate_id prefix returns undefined", () => {
  const d = toolRendererFor("delegate_send");
  const output = "Started delegate\n[not_delegate · some action · running in background]";
  const summary = d.summary(item({ toolName: "delegate_send", argumentsJSON: "{}", output }));
  expect(summary).toContain("Sent a message to a delegate");
});

test("delegate_send footer with empty delegate_id returns undefined", () => {
  const d = toolRendererFor("delegate_send");
  const output = "Started delegate\n[delegate_id   · some action · running in background]";
  const summary = d.summary(item({ toolName: "delegate_send", argumentsJSON: "{}", output }));
  expect(summary).toContain("Sent a message to a delegate");
});

test("delegate_send footer with empty action returns undefined", () => {
  const d = toolRendererFor("delegate_send");
  const output = "Started delegate\n[delegate_id dlg_1 ·   · running in background]";
  const summary = d.summary(item({ toolName: "delegate_send", argumentsJSON: "{}", output }));
  expect(summary).toContain("Sent a message to a delegate");
});

// --- delegateSendFooter wait_ignored empty (line 276) and field count mismatch (line 280) ---

test("delegate_send footer with empty wait_ignored reason returns undefined", () => {
  const d = toolRendererFor("delegate_send");
  const output = "Started delegate\n[delegate_id dlg_1 · some action · running in background · wait ignored:  ]";
  const summary = d.summary(item({ toolName: "delegate_send", argumentsJSON: "{}", output }));
  expect(summary).toContain("Sent a message to a delegate");
});

test("delegate_send footer with extra fields returns undefined", () => {
  const d = toolRendererFor("delegate_send");
  const output = "Started delegate\n[delegate_id dlg_1 · some action · running in background · unknown_field]";
  const summary = d.summary(item({ toolName: "delegate_send", argumentsJSON: "{}", output }));
  expect(summary).toContain("Sent a message to a delegate");
});

// --- delegateSendResponse with raw output (line 287) ---

test("delegate_send body with raw state and output renders the raw output as response", () => {
  const d = toolRendererFor("delegate_send");
  const Body = d.body!;
  render(
    <Body
      item={item({
        toolName: "delegate_send",
        argumentsJSON: JSON.stringify({ message: "hello" }),
        output: "",
        raw: {
          action: "test action",
          running_in_background: false,
          output: "delegate reply text",
        },
      })}
      live={false}
    />,
  );
  expect(screen.getByText("delegate reply text")).toBeTruthy();
});

// --- DelegateSendBody returns null when no message and no response (line 346) ---

test("delegate_send body returns null when no message and no response", () => {
  const d = toolRendererFor("delegate_send");
  const Body = d.body!;
  const { container } = render(
    <Body
      item={item({
        toolName: "delegate_send",
        argumentsJSON: "{}",
        output: "",
      })}
      live={false}
    />,
  );
  expect(container.firstChild).toBeNull();
});

// --- delegateSendFooter: started_job_id empty (lines 255-256) ---

test("delegate_send footer with empty started_job_id returns undefined", () => {
  const d = toolRendererFor("delegate_send");
  const output = "Started delegate\n[delegate_id dlg_1 · some action · started_job_id  · running in background]";
  const summary = d.summary(item({ toolName: "delegate_send", argumentsJSON: "{}", output }));
  // When footer parsing fails (empty started_job_id), summary has no status word
  expect(summary).toContain("Sent a message to a delegate");
});

// --- delegateSendFooter: watching field (line 271) ---

test("delegate_send footer with watching field parses correctly", () => {
  const d = toolRendererFor("delegate_send");
  const output = "Started delegate\n[delegate_id dlg_1 · some action · running in background · watching]";
  const summary = d.summary(item({ toolName: "delegate_send", argumentsJSON: "{}", output }));
  expect(summary).toBeTruthy();
});

test("delegate_send footer with started_job_id and watching and running parses", () => {
  const d = toolRendererFor("delegate_send");
  const output =
    "Started delegate\n[delegate_id dlg_1 · some action · started_job_id job_42 · running in background · watching]";
  const summary = d.summary(item({ toolName: "delegate_send", argumentsJSON: "{}", output }));
  expect(summary).toBeTruthy();
});
