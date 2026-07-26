import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { ToolCallItem } from "./ToolCallItem";
import { registerToolRenderer, type ToolRenderProps } from "./toolRenderers";
import { ignoringTurn, itemRendererFor } from "./types";
import "./tools/shellTool"; // registers the real "shell" descriptor, incl. its own autoExpand heuristic
import "./tools/fsTools"; // registers the real "read_file" (openBesidePath) + grep/list_dir/glob (opt-out)
import type { ItemModel, ThreadModel, TurnModel } from "../../../protocol/model";
import * as paneActions from "../../../shell/paneActions";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";

// The expand/collapse state now lives in the shared disclosureStore keyed by
// item.id (yt2q), so it MUST be reset between tests - every test's default
// item id is "item_1", so a prior test's toggle would otherwise leak in.
afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

const turn: TurnModel = { id: "turn_1", status: "inProgress", items: [] };

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

test('self-registers under the wire\'s tool-call item type ("commandExecution")', () => {
  expect(itemRendererFor("commandExecution")).toBe(ToolCallItem);
});

test("is memoized ignoring turn identity - a fresh turn object on every streaming delta must not re-render an unrelated settled tool call", () => {
  expect(ToolCallItem.$$typeof).toBe(Symbol.for("react.memo"));
  expect((ToolCallItem as unknown as { compare: unknown }).compare).toBe(ignoringTurn);
});

test("renders the resolved descriptor's summary", () => {
  registerToolRenderer({ match: "tci_tool_a", summary: () => "did a thing" });
  render(<ToolCallItem item={item({ toolName: "tci_tool_a" })} turn={turn} live={false} />);
  expect(screen.getByText("did a thing")).toBeTruthy();
});

// The expanded content mounts only while the row is open (the same shape
// widgets/disclosure uses), so a body assertion has to open the row first.
function expandRow(): void {
  fireEvent.click(screen.getByTestId("tool-row"));
}

test("falls back to the default descriptor (raw output body) for an unregistered tool name", () => {
  render(
    <ToolCallItem item={item({ toolName: "tci_unregistered", output: "raw bytes here" })} turn={turn} live={false} />,
  );
  expect(screen.getByText("tci_unregistered")).toBeTruthy(); // default summary = tool name
  expandRow();
  expect(screen.getByText("raw bytes here")).toBeTruthy(); // default body = raw output
});

test("renders no body element when the resolved descriptor has none", () => {
  registerToolRenderer({ match: "tci_no_body", summary: () => "no body here" });
  render(<ToolCallItem item={item({ toolName: "tci_no_body" })} turn={turn} live={false} />);
  // Only the shared row - nothing carrying body content, and no chevron
  // promising something to open.
  expect(screen.queryByTestId("tool-call-body")).toBe(null);
  expect(screen.queryByTestId("tool-row-chevron")).toBe(null);
});

test("passes live through to the descriptor's body component", () => {
  function LiveEcho({ live }: ToolRenderProps) {
    return <span data-testid="live-echo">{String(live)}</span>;
  }
  registerToolRenderer({ match: "tci_live_echo", summary: () => "s", body: LiveEcho });
  render(<ToolCallItem item={item({ toolName: "tci_live_echo" })} turn={turn} live={true} />);
  expandRow();
  expect(screen.getByTestId("live-echo").textContent).toBe("true");
});

// kata 0pzz: a descriptor's body needs the enclosing session's ref to build
// a durable "return to parent" link (the subagent-transcript body is the
// first consumer - see subagentModule.tsx's openTranscript). Mirrors the
// live-echo test above: ToolCallItem must forward its own sessionRef prop
// straight through to Body, exactly like it already does for `item`/`live`.
test("passes sessionRef through to the descriptor's body component", () => {
  function SessionRefEcho({ sessionRef }: ToolRenderProps) {
    return <span data-testid="session-ref-echo">{sessionRef ?? "(none)"}</span>;
  }
  registerToolRenderer({ match: "tci_session_ref_echo", summary: () => "s", body: SessionRefEcho });
  render(
    <ToolCallItem
      item={item({ toolName: "tci_session_ref_echo" })}
      turn={turn}
      live={false}
      sessionRef="ref_parent_1"
    />,
  );
  expandRow();
  expect(screen.getByTestId("session-ref-echo").textContent).toBe("ref_parent_1");
});

test("sessionRef is undefined at the descriptor's body when ToolCallItem itself has none", () => {
  function SessionRefEcho({ sessionRef }: ToolRenderProps) {
    return <span data-testid="session-ref-echo-2">{sessionRef ?? "(none)"}</span>;
  }
  registerToolRenderer({ match: "tci_session_ref_echo_2", summary: () => "s", body: SessionRefEcho });
  render(<ToolCallItem item={item({ toolName: "tci_session_ref_echo_2" })} turn={turn} live={false} />);
  expandRow();
  expect(screen.getByTestId("session-ref-echo-2").textContent).toBe("(none)");
});

test("tags the root with the tool name for styling/testing hooks", () => {
  const { container } = render(<ToolCallItem item={item({ toolName: "tci_tag_test" })} turn={turn} live={false} />);
  expect(container.querySelector('[data-tool-name="tci_tag_test"]')).toBeTruthy();
});

test("handles a missing toolName gracefully (falls back to the default descriptor, no crash)", () => {
  expect(() => render(<ToolCallItem item={item({ toolName: undefined })} turn={turn} live={false} />)).not.toThrow();
});

// --- suppress: a descriptor can remove its whole row (task_list view /
// malformed non-mutation) - ToolCallItem renders null ---------------------

test("a descriptor whose suppress returns true renders nothing at all", () => {
  registerToolRenderer({ match: "tci_suppress", summary: () => "s", body: () => <div>b</div>, suppress: () => true });
  render(<ToolCallItem item={item({ toolName: "tci_suppress" })} turn={turn} live={false} />);
  expect(screen.queryByTestId("tool-call-item")).toBe(null);
});

test("suppress returning false renders the row normally", () => {
  registerToolRenderer({
    match: "tci_unsuppress",
    summary: () => "kept",
    body: () => <div>b</div>,
    suppress: () => false,
  });
  render(<ToolCallItem item={item({ toolName: "tci_unsuppress" })} turn={turn} live={false} />);
  expect(screen.getByTestId("tool-call-item")).toBeTruthy();
  expect(screen.getByText("kept")).toBeTruthy();
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

// yt2q: the open/closed state lives in the shared disclosureStore keyed by
// item.id, so an expanded tool row survives the VirtualList/dockview remount
// that would reset a component-local useState.
test("an expanded tool row stays expanded across an unmount+remount with the same item id (store-backed)", () => {
  registerToolRenderer({ match: "tci_remount", summary: () => "s", body: () => <div>body text</div> });
  const toolItem = item({ id: "item_remount_1", toolName: "tci_remount" });
  const { unmount } = render(<ToolCallItem item={toolItem} turn={turn} live={false} />);
  fireEvent.click((screen.getByTestId("tool-call-item") as HTMLDetailsElement).querySelector("summary")!);
  expect((screen.getByTestId("tool-call-item") as HTMLDetailsElement).open).toBe(true);

  unmount();
  render(<ToolCallItem item={toolItem} turn={turn} live={false} />);
  // Still open after the remount - the state came from the store, not useState.
  expect((screen.getByTestId("tool-call-item") as HTMLDetailsElement).open).toBe(true);
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

// --- outputImages: rendered through ImageGallery -----------------------

test("a tool call's outputImages render as gallery thumbnails", () => {
  registerToolRenderer({ match: "tci_images", summary: () => "s", body: () => <div>body</div> });
  render(<ToolCallItem item={item({ toolName: "tci_images", outputImages: ["a", "b"] })} turn={turn} live={false} />);
  expandRow();
  expect(screen.getAllByTestId("image-gallery-thumb")).toHaveLength(2);
});

test("an empty outputImages array renders no gallery thumbnails", () => {
  registerToolRenderer({ match: "tci_no_images", summary: () => "s", body: () => <div>body</div> });
  render(<ToolCallItem item={item({ toolName: "tci_no_images", outputImages: [] })} turn={turn} live={false} />);
  expect(screen.queryAllByTestId("image-gallery-thumb")).toHaveLength(0);
});

test("outputImages render even for a body-less descriptor (the row still becomes expandable)", () => {
  registerToolRenderer({ match: "tci_images_no_body", summary: () => "s" });
  render(
    <ToolCallItem item={item({ toolName: "tci_images_no_body", outputImages: ["a"] })} turn={turn} live={false} />,
  );
  const details = screen.getByTestId("tool-call-item") as HTMLDetailsElement;
  expect(details.tagName).toBe("DETAILS");
  expandRow();
  expect(screen.getAllByTestId("image-gallery-thumb")).toHaveLength(1);
});

// --- ItemModel.error rendering: a failed/denied tool call surfaces its
// error text, force-expands, and earns a failure marker (parity-m4 §11:261
// "only failure earns the eye"; §2:100 renderer-tools.js:589-594 force-open
// on error). Keyed off item.error PRESENCE as primary (present on old-daemon
// reloads whose status is still "completed"), with the honest status:"failed"
// as corroboration (appwire_projection.go:438 SettledToolStatus). ----------

test("a settled tool call carrying item.error surfaces the error text", () => {
  registerToolRenderer({ match: "tci_err_text", summary: () => "s" });
  render(
    <ToolCallItem
      item={item({ toolName: "tci_err_text", error: "permission denied by sandbox" })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByText("permission denied by sandbox")).toBeTruthy();
});

test("an errored tool row force-expands even for a descriptor with no autoExpand", () => {
  registerToolRenderer({ match: "tci_err_expand", summary: () => "s", body: () => <div>body text</div> });
  render(<ToolCallItem item={item({ toolName: "tci_err_expand", error: "boom" })} turn={turn} live={false} />);
  const details = screen.getByTestId("tool-call-item") as HTMLDetailsElement;
  expect(details.open).toBe(true);
});

test("an errored tool row earns a failure marker in its summary", () => {
  registerToolRenderer({ match: "tci_err_glyph", summary: () => "did a thing" });
  render(<ToolCallItem item={item({ toolName: "tci_err_glyph", error: "nope" })} turn={turn} live={false} />);
  expect(screen.getByTestId("tool-call-item").getAttribute("data-failed")).toBe("true");
  expect(screen.getByTestId("failure-glyph")).toBeTruthy();
});

test("a clean tool call earns NO failure marker and stays collapsed (success recedes)", () => {
  registerToolRenderer({ match: "tci_ok_glyph", summary: () => "did a thing", body: () => <div>b</div> });
  render(<ToolCallItem item={item({ toolName: "tci_ok_glyph" })} turn={turn} live={false} />);
  expect(screen.getByTestId("tool-call-item").getAttribute("data-failed")).toBe(null);
  expect(screen.queryByTestId("failure-glyph")).toBe(null);
  expect((screen.getByTestId("tool-call-item") as HTMLDetailsElement).open).toBe(false);
});

test("a descriptor's own failed() predicate marks the row even with no wire error (nonzero shell exit)", () => {
  registerToolRenderer({
    match: "tci_descriptor_failed",
    summary: () => "s",
    failed: (it) => it.exitCode === 1,
  });
  render(<ToolCallItem item={item({ toolName: "tci_descriptor_failed", exitCode: 1 })} turn={turn} live={false} />);
  expect(screen.getByTestId("failure-glyph")).toBeTruthy();
  expect(screen.getByTestId("tool-call-item").getAttribute("data-failed")).toBe("true");
});

test("a descriptor's detail() becomes the row's hover title, never its headline text", () => {
  registerToolRenderer({ match: "tci_detail", summary: () => "Ran false", detail: () => "exit 1" });
  render(<ToolCallItem item={item({ toolName: "tci_detail" })} turn={turn} live={false} />);
  expect(screen.getByTestId("tool-row").getAttribute("title")).toBe("exit 1");
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("Ran false");
});

test("a body-less descriptor still becomes an expandable details when the call errored (shows the error)", () => {
  registerToolRenderer({ match: "tci_err_no_body", summary: () => "s" });
  render(<ToolCallItem item={item({ toolName: "tci_err_no_body", error: "denied" })} turn={turn} live={false} />);
  const details = screen.getByTestId("tool-call-item") as HTMLDetailsElement;
  expect(details.tagName).toBe("DETAILS");
  expect(details.open).toBe(true);
  expect(screen.getByText("denied")).toBeTruthy();
});

test('honest status:"failed" corroborates a failure even with no error text', () => {
  registerToolRenderer({ match: "tci_status_failed", summary: () => "s", body: () => <div>b</div> });
  render(<ToolCallItem item={item({ toolName: "tci_status_failed", status: "failed" })} turn={turn} live={false} />);
  const details = screen.getByTestId("tool-call-item") as HTMLDetailsElement;
  expect(details.getAttribute("data-failed")).toBe("true");
  expect(details.open).toBe(true);
});

test('old-daemon reload: error present but status still "completed" is treated as failed (error presence is primary)', () => {
  registerToolRenderer({ match: "tci_old_daemon", summary: () => "s" });
  render(
    <ToolCallItem
      item={item({ toolName: "tci_old_daemon", error: "old daemon denial", status: "completed" })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByTestId("tool-call-item").getAttribute("data-failed")).toBe("true");
  expect(screen.getByText("old daemon denial")).toBeTruthy();
});

test("an empty-string error is not a failure (the wire only stamps failed when error is non-empty)", () => {
  registerToolRenderer({ match: "tci_empty_err", summary: () => "s", body: () => <div>b</div> });
  render(<ToolCallItem item={item({ toolName: "tci_empty_err", error: "" })} turn={turn} live={false} />);
  expect(screen.getByTestId("tool-call-item").getAttribute("data-failed")).toBe(null);
  expect((screen.getByTestId("tool-call-item") as HTMLDetailsElement).open).toBe(false);
});

test("a manual collapse of an errored row sticks (the reader's own choice wins over force-expand)", () => {
  registerToolRenderer({ match: "tci_err_toggle", summary: () => "s", body: () => <div>body text</div> });
  render(<ToolCallItem item={item({ toolName: "tci_err_toggle", error: "boom" })} turn={turn} live={false} />);
  const details = screen.getByTestId("tool-call-item") as HTMLDetailsElement;
  expect(details.open).toBe(true);
  fireEvent.click(details.querySelector("summary")!);
  expect(details.open).toBe(false);
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

// --- file "open beside" affordance (floor §3.7 / PIN-A): a file-referencing
// tool card (read_file/edit_file/write_file) exposes descriptor.openBesidePath,
// which ToolCallItem turns into an "Open beside" control that routes through
// openDocBeside with the cwd-relativized path. Non-file tools (grep/ls/glob)
// opt out; out-of-cwd paths and a missing ref get no control. -------------

function seedThreadCwd(ref: string, cwd: string): void {
  threadsStore.setState({ threads: new Map([[ref, { ref, cwd } as unknown as ThreadModel]]) });
}

test("a read_file card in the session cwd shows an Open beside control that opens the doc pane", () => {
  resetThreadsStoreForTests();
  seedThreadCwd("ref_a", "/home/proj");
  const spy = vi.spyOn(paneActions, "openBeside").mockImplementation(() => {});
  render(
    <ToolCallItem
      item={item({ toolName: "read_file", argumentsJSON: JSON.stringify({ file_path: "/home/proj/src/a.ts" }) })}
      turn={turn}
      live={false}
      sessionRef="ref_a"
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: /open beside/i }));
  expect(spy).toHaveBeenCalledWith({ type: "doc", params: { session: "ref_a", path: "src/a.ts", kind: "file" } });
  spy.mockRestore();
});

test("a read_file card OUTSIDE the session cwd shows no Open beside control", () => {
  resetThreadsStoreForTests();
  seedThreadCwd("ref_a", "/home/proj");
  render(
    <ToolCallItem
      item={item({ toolName: "read_file", argumentsJSON: JSON.stringify({ file_path: "/etc/passwd" }) })}
      turn={turn}
      live={false}
      sessionRef="ref_a"
    />,
  );
  expect(screen.queryByRole("button", { name: /open beside/i })).toBe(null);
});

test("a read_file card with no session ref shows no Open beside control", () => {
  resetThreadsStoreForTests();
  seedThreadCwd("ref_a", "/home/proj");
  render(
    <ToolCallItem
      item={item({ toolName: "read_file", argumentsJSON: JSON.stringify({ file_path: "/home/proj/a.ts" }) })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.queryByRole("button", { name: /open beside/i })).toBe(null);
});

test("a grep card (a directory/pattern tool, not a single file) shows no Open beside control", () => {
  resetThreadsStoreForTests();
  seedThreadCwd("ref_a", "/home/proj");
  render(
    <ToolCallItem
      item={item({ toolName: "grep", argumentsJSON: JSON.stringify({ pattern: "foo", path: "/home/proj/src" }) })}
      turn={turn}
      live={false}
      sessionRef="ref_a"
    />,
  );
  expect(screen.queryByRole("button", { name: /open beside/i })).toBe(null);
});

test("clicking Open beside does not toggle the row open (the summary's own toggle is not fired)", () => {
  resetThreadsStoreForTests();
  seedThreadCwd("ref_a", "/home/proj");
  vi.spyOn(paneActions, "openBeside").mockImplementation(() => {});
  render(
    <ToolCallItem
      item={item({
        toolName: "read_file",
        argumentsJSON: JSON.stringify({ file_path: "/home/proj/a.ts" }),
        output: "x",
      })}
      turn={turn}
      live={false}
      sessionRef="ref_a"
    />,
  );
  const details = screen.getByTestId("tool-call-item") as HTMLDetailsElement;
  expect(details.open).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: /open beside/i }));
  expect(details.open).toBe(false); // still collapsed - the open-beside click did not toggle it
  vi.restoreAllMocks();
});

test("a read_file card on an image file opens beside as an image (DECISION C: kind:image)", () => {
  resetThreadsStoreForTests();
  seedThreadCwd("ref_a", "/home/proj");
  const spy = vi.spyOn(paneActions, "openBeside").mockImplementation(() => {});
  render(
    <ToolCallItem
      item={item({ toolName: "read_file", argumentsJSON: JSON.stringify({ file_path: "/home/proj/assets/logo.png" }) })}
      turn={turn}
      live={false}
      sessionRef="ref_a"
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: /open beside/i }));
  expect(spy).toHaveBeenCalledWith({
    type: "doc",
    params: { session: "ref_a", path: "assets/logo.png", kind: "image" },
  });
  spy.mockRestore();
});
