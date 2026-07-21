import { afterEach, test, expect, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { ToolCallItem } from "./ToolCallItem";
import { itemRendererFor } from "./types";
import { registerToolRenderer, type ToolRenderProps } from "./toolRenderers";
import "./tools/shellTool"; // registers the real "shell" descriptor, incl. its own autoExpand heuristic
import type { ItemModel, TurnModel } from "../../../protocol/model";

afterEach(cleanup);

const turn: TurnModel = { id: "turn_1", status: "inProgress", items: [] };

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

test('self-registers under the wire\'s tool-call item type ("commandExecution")', () => {
  expect(itemRendererFor("commandExecution")).toBe(ToolCallItem);
});

test("renders the resolved descriptor's summary", () => {
  registerToolRenderer({ match: "tci_tool_a", summary: () => "did a thing" });
  render(<ToolCallItem item={item({ toolName: "tci_tool_a" })} turn={turn} live={false} />);
  expect(screen.getByText("did a thing")).toBeTruthy();
});

test("falls back to the default descriptor (raw output body) for an unregistered tool name", () => {
  render(<ToolCallItem item={item({ toolName: "tci_unregistered", output: "raw bytes here" })} turn={turn} live={false} />);
  expect(screen.getByText("tci_unregistered")).toBeTruthy(); // default summary = tool name
  expect(screen.getByText("raw bytes here")).toBeTruthy(); // default body = raw output
});

test("renders no body element when the resolved descriptor has none", () => {
  registerToolRenderer({ match: "tci_no_body", summary: () => "no body here" });
  const { container } = render(<ToolCallItem item={item({ toolName: "tci_no_body" })} turn={turn} live={false} />);
  // Only the summary span - no second child carrying body content.
  expect(container.querySelector('[data-testid="tool-call-item"]')?.children).toHaveLength(1);
});

test("passes live through to the descriptor's body component", () => {
  function LiveEcho({ live }: ToolRenderProps) {
    return <span data-testid="live-echo">{String(live)}</span>;
  }
  registerToolRenderer({ match: "tci_live_echo", summary: () => "s", body: LiveEcho });
  render(<ToolCallItem item={item({ toolName: "tci_live_echo" })} turn={turn} live={true} />);
  expect(screen.getByTestId("live-echo").textContent).toBe("true");
});

test("tags the root with the tool name for styling/testing hooks", () => {
  const { container } = render(<ToolCallItem item={item({ toolName: "tci_tag_test" })} turn={turn} live={false} />);
  expect(container.querySelector('[data-tool-name="tci_tag_test"]')).toBeTruthy();
});

test("handles a missing toolName gracefully (falls back to the default descriptor, no crash)", () => {
  expect(() => render(<ToolCallItem item={item({ toolName: undefined })} turn={turn} live={false} />)).not.toThrow();
});

// --- expand/collapse: collapsed by default, descriptor.autoExpand can pop
// it open at settle (parity-m4-transcript.md's own Highlights: "every tool
// row, including diffs, starts collapsed" - the only default-expanded
// state anywhere is a failed shell call once it settles) ------------------

test("a row with a body starts collapsed", () => {
  registerToolRenderer({ match: "tci_collapsed", summary: () => "s", body: () => <div>body text</div> });
  render(<ToolCallItem item={item({ toolName: "tci_collapsed" })} turn={turn} live={false} />);
  const details = screen.getByTestId("tool-call-item") as HTMLDetailsElement;
  expect(details.tagName).toBe("DETAILS");
  expect(details.open).toBe(false);
});

test("clicking the summary manually expands a collapsed row", () => {
  registerToolRenderer({ match: "tci_manual_open", summary: () => "s", body: () => <div>body text</div> });
  render(<ToolCallItem item={item({ toolName: "tci_manual_open" })} turn={turn} live={false} />);
  const details = screen.getByTestId("tool-call-item") as HTMLDetailsElement;
  expect(details.open).toBe(false);
  fireEvent.click(details.querySelector("summary")!);
  expect(details.open).toBe(true);
});

test("shell: a failing exit code auto-expands the row once it settles (the real parseShellExitCode heuristic)", () => {
  const output = "stdout\n[exit 1]";
  render(
    <ToolCallItem
      item={item({ toolName: "shell", argumentsJSON: JSON.stringify({ command: "false" }), output })}
      turn={turn}
      live={false}
    />,
  );
  const details = screen.getByTestId("tool-call-item") as HTMLDetailsElement;
  expect(details.open).toBe(true);
});

test("shell: a clean exit does not auto-expand", () => {
  const output = "stdout\n[exit 0]";
  render(
    <ToolCallItem
      item={item({ toolName: "shell", argumentsJSON: JSON.stringify({ command: "true" }), output })}
      turn={turn}
      live={false}
    />,
  );
  const details = screen.getByTestId("tool-call-item") as HTMLDetailsElement;
  expect(details.open).toBe(false);
});

test("manual collapse of an auto-expanded row sticks (wins over autoExpand)", () => {
  const output = "stdout\n[exit 1]";
  const failing = item({ toolName: "shell", argumentsJSON: JSON.stringify({ command: "false" }), output });
  render(<ToolCallItem item={failing} turn={turn} live={false} />);
  const details = screen.getByTestId("tool-call-item") as HTMLDetailsElement;
  expect(details.open).toBe(true); // auto-expanded at settle

  fireEvent.click(details.querySelector("summary")!);
  expect(details.open).toBe(false); // the user's own collapse wins
});

test("live -> settled transition applies autoExpand exactly once", () => {
  const autoExpand = vi.fn((it: ItemModel) => it.output === "[exit 1]");
  registerToolRenderer({ match: "tci_once", summary: () => "s", body: () => <div>b</div>, autoExpand });

  const liveItem = item({ toolName: "tci_once", output: "" });
  const { rerender } = render(<ToolCallItem item={liveItem} turn={turn} live={true} />);
  expect(autoExpand).not.toHaveBeenCalled(); // not consulted while still live

  const settledItem = item({ toolName: "tci_once", output: "[exit 1]" });
  rerender(<ToolCallItem item={settledItem} turn={turn} live={false} />);
  expect(autoExpand).toHaveBeenCalledTimes(1);
  expect((screen.getByTestId("tool-call-item") as HTMLDetailsElement).open).toBe(true);

  // A further re-render at the SAME settled state must not re-invoke it.
  rerender(<ToolCallItem item={settledItem} turn={turn} live={false} />);
  expect(autoExpand).toHaveBeenCalledTimes(1);
});
