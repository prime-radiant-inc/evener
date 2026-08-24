// Edge cases for jobTools.tsx that close remaining uncovered lines:
// - CopyTextButton CopiedGlyph rendering (line 32)
// - CopyTextButton timer effect (lines 47-48)
// - CopyTextButton clipboard guard (lines 59-60)
// - delegateSendFooter with started_job_id empty (lines 255-256)
// - delegateSendFooter with watching field (line 271)

import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import type { ItemModel } from "../../../../protocol/model";
import { toolRendererFor } from "../toolRenderers";
import "./jobTools";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
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
  expect(writeText).toHaveBeenCalledTimes(1);
  expect(writeText).toHaveBeenCalledWith("hello world");
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
  vi.useFakeTimers();
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
  await act(async () => {
    copyButton.click();
    await Promise.resolve();
  });

  expect(writeText).toHaveBeenCalledWith("hello world");
  expect(screen.getByRole("button", { name: "Copied" })).toBeTruthy();

  await act(async () => {
    await vi.advanceTimersByTimeAsync(2_000);
  });

  expect(screen.queryByRole("button", { name: "Copied" })).toBeNull();
  expect(screen.getByRole("button", { name: "Copy message" })).toBeTruthy();
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

// --- delegateSendFooter rejection paths ---

test.each([
  ["missing delegate_id prefix", "not_delegate · some action · running in background"],
  ["empty delegate_id", "delegate_id   · some action · running in background"],
  ["empty action", "delegate_id dlg_1 ·   · running in background"],
  ["empty started_job_id", "delegate_id dlg_1 · some action · started_job_id  · running in background"],
  ["empty wait-ignored reason", "delegate_id dlg_1 · some action · running in background · wait ignored:  "],
  ["unknown extra field", "delegate_id dlg_1 · some action · running in background · unknown_field"],
])("delegate_send output with %s preserves the rejected footer as response text", (_case, footer) => {
  const d = toolRendererFor("delegate_send");
  const output = `Started delegate\n[${footer}]`;
  const toolItem = item({ toolName: "delegate_send", argumentsJSON: "{}", output });

  expect(d.summary(toolItem)).toBe("Sent a message to a delegate");
  const Body = d.body!;
  render(<Body item={toolItem} live={false} />);
  expect(screen.getByTestId("delegate-send-response").textContent).toContain(`[${footer}]`);
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

// --- delegateSendFooter: watching field (line 271) ---

test("delegate_send footer with watching field parses correctly", () => {
  const d = toolRendererFor("delegate_send");
  const output = "Started delegate\n[delegate_id dlg_1 · some action · running in background · watching]";
  const toolItem = item({ toolName: "delegate_send", argumentsJSON: "{}", output });
  expect(d.summary(toolItem)).toBe("Sent a message to a delegate · running");

  const Body = d.body!;
  render(<Body item={toolItem} live={false} />);
  const response = screen.getByTestId("delegate-send-response");
  expect(response.textContent).toContain("Started delegate");
  expect(response.textContent).not.toContain("delegate_id dlg_1");
});

test("delegate_send footer with started_job_id and watching and running parses", () => {
  const d = toolRendererFor("delegate_send");
  const output =
    "Started delegate\n[delegate_id dlg_1 · some action · started_job_id job_42 · running in background · watching]";
  const toolItem = item({ toolName: "delegate_send", argumentsJSON: "{}", output });
  expect(d.summary(toolItem)).toBe("Sent a message to a delegate · running");

  const Body = d.body!;
  render(<Body item={toolItem} live={false} />);
  expect(screen.getByTestId("delegate-send-response").textContent).not.toContain("started_job_id job_42");
});
