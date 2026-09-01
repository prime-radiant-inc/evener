// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { makeTranscriptDisplayConfig } from "../../../transcriptDisplay/config";
import {
  createTranscriptRenderContext,
  defaultDisclosureScope,
  TranscriptRenderProvider,
} from "../../../transcriptDisplay/renderContext";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import galleryStyles from "./flow/imagegallery.module.css";
import { ToolCallItem } from "./ToolCallItem";
import { registerToolRenderer, type ToolRenderProps } from "./toolRenderers";
import { ignoringTurn, itemRendererFor } from "./types";
import "./tools/shellTool"; // registers the real "shell" descriptor, incl. its own autoExpand heuristic
import "./tools/fsTools"; // registers the real "read_file" (openBesidePath) + grep/list_dir/glob (opt-out)
import type { ItemModel, ThreadModel, TurnModel } from "../../../protocol/model";
import * as paneActions from "../../../shell/paneActions";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { resetSubagentModuleStoreForTests } from "./tools/subagentModuleStore";

// The expand/collapse state now lives in the shared disclosureStore keyed by
// item.id (yt2q), so it MUST be reset between tests - every test's default
// item id is "item_1", so a prior test's toggle would otherwise leak in.
afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
  resetSubagentModuleStoreForTests();
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

test("settled intent-bearing commandExecution rows stack intent over the demoted summary", () => {
  registerToolRenderer({ match: "tci_one_line", summary: () => "Ran npm test -- src/foo" });
  render(
    <ToolCallItem
      item={item({
        toolName: "tci_one_line",
        description: "Running the foo tests",
        output: "done",
      })}
      turn={turn}
      live={false}
    />,
  );
  // Two lines through the real consumer: no composed "intent — summary"
  // single line (tried in tiered density, reverted on review).
  expect(screen.getByTestId("tool-row-intent").textContent).toBe("Running the foo tests");
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("Ran npm test -- src/foo");
  expect(screen.getByTestId("tool-row-intent").textContent).not.toContain(" — ");
});

// The expanded content mounts only while the row is open (the same shape
// widgets/disclosure uses), so a body assertion has to open the row first.
function expandRow(): void {
  fireEvent.click(screen.getByTestId("tool-row-trigger"));
}

// The disclosure trigger is a real button[aria-expanded] (see ToolRow.tsx),
// not a native <details>/<summary> - the open/closed state is
// read off aria-expanded on the tool-row, and toggled by clicking it. These
// helpers keep the per-test assertions to the same one-liner shape the old
// `details.open` / `details.querySelector("summary")` idioms had.
function rowIsOpen(root: HTMLElement = screen.getByTestId("tool-call-item")): boolean {
  const trigger = root.querySelector('[data-testid="tool-row-trigger"]');
  return trigger?.getAttribute("aria-expanded") === "true";
}

function toggleRow(root: HTMLElement = screen.getByTestId("tool-call-item")): void {
  fireEvent.click(root.querySelector('[data-testid="tool-row-trigger"]')!);
}
test("falls back to the default descriptor (raw output body) for an unregistered tool name", () => {
  const args = JSON.stringify({ kind: "mcp", id: 7 });
  render(
    <ToolCallItem
      item={item({ toolName: "tci_unregistered", argumentsJSON: args, output: "raw bytes here" })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByText("tci_unregistered")).toBeTruthy(); // default summary = tool name
  expandRow();
  const body = screen.getByTestId("tool-call-body");
  const blocks = body.querySelectorAll("pre > code");
  expect(blocks).toHaveLength(2);
  expect(blocks[0]?.textContent).toBe(JSON.stringify(JSON.parse(args), null, 2));
  expect(blocks[1]?.textContent).toBe("raw bytes here");
});

test("the default descriptor keeps both arguments and output visible for a settled unregistered tool", () => {
  const args = '{"width":375,"options":{"mobile":true}}';
  render(
    <ToolCallItem
      item={item({ toolName: "tci_default_coexist", argumentsJSON: args, output: "downloaded 128 bytes" })}
      turn={turn}
      live={false}
    />,
  );
  expandRow();

  const body = screen.getByTestId("tool-call-body");
  const argsBlock = within(body).getByRole("region", { name: "Tool call arguments" });
  expect(argsBlock).toBeTruthy();
  expect(argsBlock.textContent).toBe(`{\n  "width": 375,\n  "options": {\n    "mobile": true\n  }\n}`);
  expect(within(body).getByText("downloaded 128 bytes")).toBeTruthy();
});

test("the default descriptor keeps both arguments and error text visible for a settled unregistered tool", () => {
  const args = '{"width":375,"options":{"mobile":true}}';
  render(
    <ToolCallItem
      item={item({ toolName: "tci_default_error", argumentsJSON: args, error: "permission denied by sandbox" })}
      turn={turn}
      live={false}
    />,
  );

  const details = screen.getByTestId("tool-call-item");
  expect(rowIsOpen(details)).toBe(true);
  const body = screen.getByTestId("tool-call-body");
  expect(within(body).getByRole("region", { name: "Tool call arguments" })).toBeTruthy();
  expect(within(body).getByText("permission denied by sandbox")).toBeTruthy();
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

test("dedicated renderers do not get MCP argument blocks", () => {
  registerToolRenderer({
    match: "tci_no_mcp_args",
    summary: () => "s",
    body: ({ item }) => <div data-testid="dedicated-body">{item.output}</div>,
  });
  render(
    <ToolCallItem
      item={item({ toolName: "tci_no_mcp_args", argumentsJSON: '{"width":375}', output: "body output" })}
      turn={turn}
      live={false}
    />,
  );
  expandRow();
  const body = screen.getByTestId("tool-call-body");
  expect(within(body).queryByRole("region", { name: "Tool call arguments" })).toBeNull();
  expect(screen.getByTestId("dedicated-body").textContent).toBe("body output");
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
// row, including diffs, starts collapsed" - the default-expanded states are
// a failed shell call and an image read whose picture IS its output) -------

test("a row with a body starts collapsed", () => {
  registerToolRenderer({ match: "tci_collapsed", summary: () => "s", body: () => <div>body text</div> });
  render(<ToolCallItem item={item({ toolName: "tci_collapsed" })} turn={turn} live={false} />);
  const details = screen.getByTestId("tool-call-item");
  expect(rowIsOpen(details)).toBe(false);
});

test("the disclosure trigger controls the mounted body by its stable ID", () => {
  registerToolRenderer({ match: "tci_controls_body", summary: () => "s", body: () => <div>body</div> });
  render(<ToolCallItem item={item({ toolName: "tci_controls_body" })} turn={turn} live={false} />);
  const trigger = screen.getByTestId("tool-row-trigger");
  expect(trigger.getAttribute("aria-controls")).toBeTruthy();
  expect(document.getElementById(trigger.getAttribute("aria-controls")!)).toBe(null);

  fireEvent.click(trigger);
  const body = screen.getByTestId("tool-call-body");
  expect(body.id).toBe(trigger.getAttribute("aria-controls"));
  expect(trigger.getAttribute("aria-expanded")).toBe("true");
});

test("Full establishes one open baseline, preserves a later manual close, and opens new eligible rows", () => {
  registerToolRenderer({ match: "tci_baseline", summary: () => "s", body: () => <div>body text</div> });
  const activity = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" });
  const full = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  const first = item({ id: "baseline_first", toolName: "tci_baseline" });
  const second = item({ id: "baseline_second", toolName: "tci_baseline" });
  const renderBody = (config: typeof activity, items: ItemModel[]) => (
    <TranscriptRenderProvider
      config={config}
      surface="readOnly"
      disclosureScope="live:baseline"
      eligibleDisclosureIds={items.map((entry) => entry.id)}
    >
      {items.map((entry) => (
        <ToolCallItem key={entry.id} item={entry} turn={turn} live={false} />
      ))}
    </TranscriptRenderProvider>
  );

  const { rerender } = render(renderBody(activity, [first]));
  expect(rowIsOpen()).toBe(false);
  toggleRow();
  toggleRow();
  expect(rowIsOpen()).toBe(false);

  rerender(renderBody(full, [first]));
  expect(rowIsOpen()).toBe(true);
  toggleRow();
  expect(rowIsOpen()).toBe(false);

  rerender(renderBody(full, [first, second]));
  const rows = screen.getAllByTestId("tool-call-item");
  expect(rowIsOpen(rows[0]!)).toBe(false);
  expect(rowIsOpen(rows[1]!)).toBe(true);
});

test("omitted disclosure scopes remain isolated by live, readOnly, and preview surfaces", () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" });
  const live = createTranscriptRenderContext({ config, surface: "live" });
  const readOnly = createTranscriptRenderContext({ config, surface: "readOnly" });
  const preview = createTranscriptRenderContext({ config, surface: "preview" });

  expect(live.disclosureScope).toBe(defaultDisclosureScope("live"));
  expect(readOnly.disclosureScope).toBe(defaultDisclosureScope("readOnly"));
  expect(preview.disclosureScope).toBe(defaultDisclosureScope("preview"));
  expect(new Set([live.disclosureScope, readOnly.disclosureScope, preview.disclosureScope]).size).toBe(3);
  expect(createTranscriptRenderContext({ config, surface: "live", sessionRef: "session_a" }).disclosureScope).not.toBe(
    createTranscriptRenderContext({ config, surface: "live", sessionRef: "session_b" }).disclosureScope,
  );
});

test("clicking the summary manually expands a collapsed row", () => {
  registerToolRenderer({ match: "tci_manual_open", summary: () => "s", body: () => <div>body text</div> });
  render(<ToolCallItem item={item({ toolName: "tci_manual_open" })} turn={turn} live={false} />);
  const details = screen.getByTestId("tool-call-item");
  expect(rowIsOpen(details)).toBe(false);
  toggleRow(details);
  expect(rowIsOpen(details)).toBe(true);
});

// yt2q: the open/closed state lives in the shared disclosureStore keyed by
// item.id, so an expanded tool row survives the VirtualList/dockview remount
// that would reset a component-local useState.
test("an expanded tool row stays expanded across an unmount+remount with the same item id (store-backed)", () => {
  registerToolRenderer({ match: "tci_remount", summary: () => "s", body: () => <div>body text</div> });
  const toolItem = item({ id: "item_remount_1", toolName: "tci_remount" });
  const { unmount } = render(<ToolCallItem item={toolItem} turn={turn} live={false} />);
  toggleRow(screen.getByTestId("tool-call-item"));
  expect(rowIsOpen(screen.getByTestId("tool-call-item"))).toBe(true);

  unmount();
  render(<ToolCallItem item={toolItem} turn={turn} live={false} />);
  // Still open after the remount - the state came from the store, not useState.
  expect(rowIsOpen(screen.getByTestId("tool-call-item"))).toBe(true);
});

test("the same tool item id has independent disclosure state in different sessions", () => {
  registerToolRenderer({ match: "tci_session_isolation", summary: () => "s", body: () => <div>body text</div> });
  const shared = item({ id: "same_item", toolName: "tci_session_isolation" });
  render(
    <>
      <ToolCallItem item={shared} turn={turn} live={false} sessionRef="session_a" />
      <ToolCallItem item={shared} turn={turn} live={false} sessionRef="session_b" />
    </>,
  );

  const rows = screen.getAllByTestId("tool-call-item");
  expect(rows).toHaveLength(2);
  expect(rowIsOpen(rows[0]!)).toBe(false);
  expect(rowIsOpen(rows[1]!)).toBe(false);

  toggleRow(rows[0]!);
  expect(rowIsOpen(rows[0]!)).toBe(true);
  expect(rowIsOpen(rows[1]!)).toBe(false);
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
  const details = screen.getByTestId("tool-call-item");
  expect(rowIsOpen(details)).toBe(true);
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
  const details = screen.getByTestId("tool-call-item");
  expect(rowIsOpen(details)).toBe(false);
});

test("manual collapse of an auto-expanded row sticks (wins over autoExpand)", () => {
  const output = "stdout\n[exit 1]";
  const failing = item({ toolName: "shell", argumentsJSON: JSON.stringify({ command: "false" }), output });
  render(<ToolCallItem item={failing} turn={turn} live={false} />);
  const details = screen.getByTestId("tool-call-item");
  expect(rowIsOpen(details)).toBe(true); // auto-expanded at settle

  toggleRow(details);
  expect(rowIsOpen(details)).toBe(false); // the user's own collapse wins
});

// --- outputImages: rendered through ImageGallery -----------------------

test("a tool call's outputImages render as gallery thumbnails", () => {
  registerToolRenderer({ match: "tci_images", summary: () => "s", body: () => <div>body</div> });
  render(
    <ToolCallItem
      item={item({ toolName: "tci_images", outputImages: [{ src: "a" }, { src: "b" }] })}
      turn={turn}
      live={false}
    />,
  );
  expandRow();
  expect(screen.getAllByTestId("image-gallery-thumb")).toHaveLength(2);
});

test("a read_file image read renders its output image at the large (up-to-600px) size, not a 96px thumbnail", () => {
  render(
    <ToolCallItem
      item={item({
        toolName: "read_file",
        argumentsJSON: JSON.stringify({ file_path: "shot.png" }),
        output: "[image: png, 3 bytes, base64 data follows]",
        outputImages: [{ src: "/doc/image?session=s&path=shot.png", name: "shot.png" }],
      })}
      turn={turn}
      live={false}
    />,
  );
  // An image read auto-expands (the picture is the call's whole output), so
  // the gallery is already mounted - no expandRow() toggle needed.
  const thumb = screen.getByTestId("image-gallery-thumb");
  expect(thumb.className.split(/\s+/)).toContain(galleryStyles.thumbLarge);
});

test("other tools' outputImages keep the default 96px thumbnail size", () => {
  registerToolRenderer({ match: "tci_images_default_size", summary: () => "s", body: () => <div>body</div> });
  render(
    <ToolCallItem
      item={item({ toolName: "tci_images_default_size", outputImages: [{ src: "a" }] })}
      turn={turn}
      live={false}
    />,
  );
  expandRow();
  const thumb = screen.getByTestId("image-gallery-thumb");
  expect(thumb.className.split(/\s+/)).not.toContain(galleryStyles.thumbLarge);
});

test("an empty outputImages array renders no gallery thumbnails", () => {
  registerToolRenderer({ match: "tci_no_images", summary: () => "s", body: () => <div>body</div> });
  render(<ToolCallItem item={item({ toolName: "tci_no_images", outputImages: [] })} turn={turn} live={false} />);
  expect(screen.queryAllByTestId("image-gallery-thumb")).toHaveLength(0);
});

test("outputImages render even for a body-less descriptor (the row still becomes expandable)", () => {
  registerToolRenderer({ match: "tci_images_no_body", summary: () => "s" });
  render(
    <ToolCallItem
      item={item({ toolName: "tci_images_no_body", outputImages: [{ src: "a" }] })}
      turn={turn}
      live={false}
    />,
  );
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
  const details = screen.getByTestId("tool-call-item");
  expect(rowIsOpen(details)).toBe(true);
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
  expect(rowIsOpen(screen.getByTestId("tool-call-item"))).toBe(false);
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
  const details = screen.getByTestId("tool-call-item");
  expect(rowIsOpen(details)).toBe(true);
  expect(screen.getByText("denied")).toBeTruthy();
});

test("an expanded shell row drops the one-line summary - the body's pretty-printed block is the single copy", () => {
  // A nonzero exit auto-expands the row on settle (descriptor.autoExpand).
  render(
    <ToolCallItem
      item={item({
        toolName: "shell",
        argumentsJSON: JSON.stringify({ command: "echo hi" }),
        output: "oops\n[exit 1]",
      })}
      turn={turn}
      live={false}
    />,
  );
  const details = screen.getByTestId("tool-call-item");
  expect(rowIsOpen(details)).toBe(true);
  expect(screen.queryByTestId("tool-row-summary")).toBeNull();
  // The command still appears exactly once: the body's pretty-printed block.
  expect(screen.getByTestId("tool-call-body").textContent).toContain("echo hi");
  // The row stays toggleable: with no intent and no summary, the chevron
  // still renders.
  expect(screen.getByTestId("tool-row-chevron")).toBeTruthy();
});

test("a collapsed shell row keeps the one-line summary; opening the row drops it", () => {
  render(
    <ToolCallItem
      item={item({
        toolName: "shell",
        argumentsJSON: JSON.stringify({ command: "echo hi" }),
        output: "hi\n[exit 0]",
      })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("Ran echo hi");
  expandRow();
  expect(screen.queryByTestId("tool-row-summary")).toBeNull();
});

test("an expanded row of a descriptor WITHOUT summaryHiddenWhenExpanded keeps its summary", () => {
  registerToolRenderer({
    match: "tci_keep_summary",
    summary: () => "did a thing",
    body: () => <div>body text</div>,
    autoExpand: () => true,
  });
  render(<ToolCallItem item={item({ toolName: "tci_keep_summary" })} turn={turn} live={false} />);
  expect(rowIsOpen(screen.getByTestId("tool-call-item"))).toBe(true);
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("did a thing");
});

test('honest status:"failed" corroborates a failure even with no error text', () => {
  registerToolRenderer({ match: "tci_status_failed", summary: () => "s", body: () => <div>b</div> });
  render(<ToolCallItem item={item({ toolName: "tci_status_failed", status: "failed" })} turn={turn} live={false} />);
  const details = screen.getByTestId("tool-call-item");
  expect(details.getAttribute("data-failed")).toBe("true");
  expect(rowIsOpen(details)).toBe(true);
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
  expect(rowIsOpen(screen.getByTestId("tool-call-item"))).toBe(false);
});

// --- kata hgm1: a self-corrected preval-only failure (never reached real
// execution) collapses once the model's next call to the same tool
// succeeds, but is still marked failed and reachable - "only failure earns
// the eye" is untouched for every real execution failure/denial. ----------

function threadWith(items: ItemModel[]): ThreadModel {
  return { turns: [{ id: "t1", status: "completed", items }] } as unknown as ThreadModel;
}

test("a preval-only failure superseded by a later same-tool success starts collapsed", () => {
  registerToolRenderer({ match: "tci_preval_ok", summary: () => "s" });
  const failedItem = item({
    id: "item_bad",
    toolName: "tci_preval_ok",
    error: "missing required field",
    prevalOnly: true,
  });
  const okItem: ItemModel = {
    id: "item_ok",
    turnId: "t1",
    type: "commandExecution",
    text: "",
    toolName: "tci_preval_ok",
  };
  threadsStore.setState({ threads: new Map([["ref_a", threadWith([failedItem, okItem])]]) });

  render(<ToolCallItem item={failedItem} turn={turn} live={false} sessionRef="ref_a" />);

  const details = screen.getByTestId("tool-call-item");
  // Still attributable: the failure marker never goes away.
  expect(details.getAttribute("data-failed")).toBe("true");
  expect(screen.getByTestId("failure-glyph")).toBeTruthy();
  // But no longer forced open, since the very next attempt succeeded.
  expect(rowIsOpen(details)).toBe(false);
});

test("a preval-only failure with NO later success stays forced open (nothing corrected it)", () => {
  registerToolRenderer({ match: "tci_preval_unfixed", summary: () => "s" });
  const failedItem = item({
    id: "item_bad",
    toolName: "tci_preval_unfixed",
    error: "missing required field",
    prevalOnly: true,
  });
  threadsStore.setState({ threads: new Map([["ref_a", threadWith([failedItem])]]) });

  render(<ToolCallItem item={failedItem} turn={turn} live={false} sessionRef="ref_a" />);

  expect(rowIsOpen(screen.getByTestId("tool-call-item"))).toBe(true);
});

test("a preval-only failure followed by ANOTHER preval-only failure stays forced open (recurring, not yet corrected)", () => {
  registerToolRenderer({ match: "tci_preval_recur", summary: () => "s" });
  const failed1 = item({
    id: "item_bad1",
    toolName: "tci_preval_recur",
    error: "missing required field",
    prevalOnly: true,
  });
  const failed2: ItemModel = {
    id: "item_bad2",
    turnId: "t1",
    type: "commandExecution",
    text: "",
    toolName: "tci_preval_recur",
    error: "still missing required field",
    prevalOnly: true,
  };
  threadsStore.setState({ threads: new Map([["ref_a", threadWith([failed1, failed2])]]) });

  render(<ToolCallItem item={failed1} turn={turn} live={false} sessionRef="ref_a" />);

  expect(rowIsOpen(screen.getByTestId("tool-call-item"))).toBe(true);
});

test("a REAL execution failure stays forced open even when the next same-tool call succeeds (shared contract untouched)", () => {
  registerToolRenderer({ match: "tci_real_fail_then_ok", summary: () => "s" });
  const failedItem = item({
    id: "item_bad",
    toolName: "tci_real_fail_then_ok",
    error: "permission denied",
  });
  const okItem: ItemModel = {
    id: "item_ok",
    turnId: "t1",
    type: "commandExecution",
    text: "",
    toolName: "tci_real_fail_then_ok",
  };
  threadsStore.setState({ threads: new Map([["ref_a", threadWith([failedItem, okItem])]]) });

  render(<ToolCallItem item={failedItem} turn={turn} live={false} sessionRef="ref_a" />);

  expect(rowIsOpen(screen.getByTestId("tool-call-item"))).toBe(true);
});

test("supersession is reactive: a row already settled and rendered collapses once the correcting call lands", () => {
  registerToolRenderer({ match: "tci_preval_reactive", summary: () => "s" });
  const failedItem = item({
    id: "item_bad",
    toolName: "tci_preval_reactive",
    error: "missing required field",
    prevalOnly: true,
  });
  threadsStore.setState({ threads: new Map([["ref_a", threadWith([failedItem])]]) });

  render(<ToolCallItem item={failedItem} turn={turn} live={false} sessionRef="ref_a" />);
  expect(rowIsOpen(screen.getByTestId("tool-call-item"))).toBe(true);

  const okItem: ItemModel = {
    id: "item_ok",
    turnId: "t1",
    type: "commandExecution",
    text: "",
    toolName: "tci_preval_reactive",
  };
  act(() => {
    threadsStore.setState({ threads: new Map([["ref_a", threadWith([failedItem, okItem])]]) });
  });

  expect(rowIsOpen(screen.getByTestId("tool-call-item"))).toBe(false);
});

test("a reader who manually reopened a superseded row keeps it open (explicit toggle still wins)", () => {
  registerToolRenderer({ match: "tci_preval_manual_reopen", summary: () => "s" });
  const failedItem = item({
    id: "item_bad",
    toolName: "tci_preval_manual_reopen",
    error: "missing required field",
    prevalOnly: true,
  });
  const okItem: ItemModel = {
    id: "item_ok",
    turnId: "t1",
    type: "commandExecution",
    text: "",
    toolName: "tci_preval_manual_reopen",
  };
  threadsStore.setState({ threads: new Map([["ref_a", threadWith([failedItem, okItem])]]) });

  render(<ToolCallItem item={failedItem} turn={turn} live={false} sessionRef="ref_a" />);
  const details = screen.getByTestId("tool-call-item");
  expect(rowIsOpen(details)).toBe(false);

  fireEvent.click(screen.getByTestId("tool-row-trigger"));
  expect(rowIsOpen(details)).toBe(true);
});

test("a manual collapse of an errored row sticks (the reader's own choice wins over force-expand)", () => {
  registerToolRenderer({ match: "tci_err_toggle", summary: () => "s", body: () => <div>body text</div> });
  render(<ToolCallItem item={item({ toolName: "tci_err_toggle", error: "boom" })} turn={turn} live={false} />);
  const details = screen.getByTestId("tool-call-item");
  expect(rowIsOpen(details)).toBe(true);
  toggleRow(details);
  expect(rowIsOpen(details)).toBe(false);
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
  expect(rowIsOpen(screen.getByTestId("tool-call-item"))).toBe(true);

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
  threadsStore.setState({ threads: new Map([[ref, { ref, cwd, turns: [] } as unknown as ThreadModel]]) });
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

test("a read_file card outside the session cwd renders no trailing-anchor wrapper", () => {
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
  expect(screen.queryByTestId("tool-row-trailing")).toBe(null);
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
  const details = screen.getByTestId("tool-call-item");
  expect(rowIsOpen(details)).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: /open beside/i }));
  expect(rowIsOpen(details)).toBe(false); // still collapsed - the open-beside click did not toggle it
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

// read_file's openBesideInline: the summary quotes the path verbatim between
// the verb and the line range, so the control rides INLINE between the file
// name and the range it opens - not off at the end of the line.
test("a read_file card's Open beside control rides inline between the file name and the line range", () => {
  resetThreadsStoreForTests();
  seedThreadCwd("ref_a", "/home/proj");
  render(
    <ToolCallItem
      item={item({
        toolName: "read_file",
        description: "Reviewing Sheet tests before adding size coverage",
        argumentsJSON: JSON.stringify({ file_path: "/home/proj/src/widgets/sheet/sheet.test.tsx" }),
        output: "a\nb\nc\n",
      })}
      turn={turn}
      live={false}
      sessionRef="ref_a"
    />,
  );
  const button = screen.getByRole("button", { name: /open beside/i });
  const meta = screen.getByTestId("tool-row-summary-meta");
  expect(meta.textContent).toBe(" · lines 1-3");
  // The control precedes the line-range meta and follows the path text.
  expect(button.compareDocumentPosition(meta) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  const head = screen.getByTestId("tool-row-summary-head");
  const tail = screen.getByTestId("tool-row-summary-tail");
  // The truncation cut may land inside the path, so the path's visible text
  // is head+tail together - and the control sits right after it.
  expect((head.textContent ?? "") + (tail.textContent ?? "")).toBe("Read /home/proj/src/widgets/sheet/sheet.test.tsx");
  expect(tail.compareDocumentPosition(button) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
});

// --- summarySuffix (kata h70z): a descriptor may append text to the
// collapsed row's summary computed from the FULL thread model (ask_user's
// "— answered: ..." recap is the one real consumer, covered in
// tools/askUser.test.tsx; these tests exercise the generic plumbing here
// with a synthetic descriptor so ToolCallItem's own contract is verified
// independently of ask_user's parsing). ---------------------------------

test("appends descriptor.summarySuffix to the row's summary, reactively, off the live thread model", () => {
  resetThreadsStoreForTests();
  registerToolRenderer({
    match: "tci_suffix",
    summary: () => "base summary",
    summarySuffix: (_item, model) => (model?.name === "answered" ? " — answered" : undefined),
  });
  threadsStore.setState({ threads: new Map([["ref_a", { name: "", turns: [] } as unknown as ThreadModel]]) });
  render(<ToolCallItem item={item({ toolName: "tci_suffix" })} turn={turn} live={false} sessionRef="ref_a" />);
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("base summary");

  act(() => {
    threadsStore.setState({
      threads: new Map([["ref_a", { name: "answered", turns: [] } as unknown as ThreadModel]]),
    });
  });
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("base summary — answered");
});

test("summarySuffix is omitted (bare summary) when the descriptor doesn't define one", () => {
  resetThreadsStoreForTests();
  registerToolRenderer({ match: "tci_no_suffix", summary: () => "plain summary" });
  render(<ToolCallItem item={item({ toolName: "tci_no_suffix" })} turn={turn} live={false} sessionRef="ref_a" />);
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("plain summary");
});

test("delegate tool rows keep the human description as intent and suppress the technical delegate summary", () => {
  render(
    <ToolCallItem
      item={item({
        toolName: "delegate",
        description: "Testing delegation",
        argumentsJSON: JSON.stringify({ task: "Run the linter" }),
      })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getByTestId("tool-row-intent").textContent).toBe("Testing delegation");
  expect(screen.queryByTestId("tool-row-summary")).toBeNull();
});

test("task-only delegate intent previews preserve an emoji at the Unicode clipping boundary", () => {
  const emojiTask = `${"a".repeat(119)}😀 suffix`;
  const exactAsciiTask = "b".repeat(120);
  render(
    <>
      <ToolCallItem
        item={item({
          id: "unicode_delegate",
          toolName: "delegate",
          argumentsJSON: JSON.stringify({ task: emojiTask }),
          output: JSON.stringify({ status: "completed" }),
        })}
        turn={turn}
        live={false}
      />
      <ToolCallItem
        item={item({
          id: "ascii_boundary_delegate",
          toolName: "delegate",
          argumentsJSON: JSON.stringify({ task: exactAsciiTask }),
          output: JSON.stringify({ status: "completed" }),
        })}
        turn={turn}
        live={false}
      />
    </>,
  );

  const [unicodeTool, asciiTool] = screen.getAllByTestId("tool-call-item");
  expect(within(unicodeTool!).getByTestId("tool-row-intent").textContent).toBe(`${"a".repeat(119)}😀…`);
  expect(within(asciiTool!).getByTestId("tool-row-intent").textContent).toBe(exactAsciiTask);
});

test("delegate controls require stable delegate_id and reject activation-only job_id", () => {
  render(
    <>
      <ToolCallItem
        item={item({
          id: "stable_delegate",
          callId: "call_stable_delegate",
          toolName: "delegate",
          argumentsJSON: JSON.stringify({ task: "stable" }),
          output: JSON.stringify({
            delegate_id: "dlg_stable",
            status: "running",
            transcript_ref: "local:sess_child",
          }),
        })}
        turn={turn}
        live={false}
      />
      <ToolCallItem
        item={item({
          id: "activation_only_delegate",
          callId: "call_activation_only_delegate",
          toolName: "delegate",
          argumentsJSON: JSON.stringify({ task: "legacy activation" }),
          output: JSON.stringify({ job_id: "job_legacy", status: "running", transcript_ref: "job:job_legacy" }),
        })}
        turn={turn}
        live={false}
      />
    </>,
  );

  const rows = screen.getAllByTestId("subagent-row");
  expect(rows).toHaveLength(1);
  // The stable call cards; the activation-only one does not. The card is
  // headless - assert the kind, and that exactly one row (the stable one)
  // earned the open control.
  expect(rows[0]?.getAttribute("data-kind")).toBe("running");
  expect(screen.getAllByRole("button", { name: "Open transcript" })).toHaveLength(1);
});

test("malformed delegate arguments keep status without inventing an intent", () => {
  render(
    <ToolCallItem
      item={item({
        id: "malformed_delegate",
        toolName: "delegate",
        argumentsJSON: "{not-json",
        output: JSON.stringify({ status: "completed" }),
      })}
      turn={turn}
      live={false}
    />,
  );

  expect(screen.queryByTestId("tool-row-intent")).toBeNull();
  expect(screen.queryByTestId("tool-row-summary")).toBeNull();
  expect(screen.getByTestId("tool-row-status")).toBeTruthy();
  expect(screen.queryByText("delegate")).toBeNull();
});

test("blank and non-string delegate tasks keep status without inventing an intent", () => {
  const blank = item({
    id: "blank_delegate",
    toolName: "delegate",
    argumentsJSON: JSON.stringify({ task: " \n\t " }),
    output: JSON.stringify({ status: "completed" }),
  });
  const nonString = item({
    id: "non_string_delegate",
    toolName: "delegate",
    argumentsJSON: JSON.stringify({ task: ["not", "text"] }),
    output: JSON.stringify({ status: "completed" }),
  });
  render(
    <>
      <ToolCallItem item={blank} turn={turn} live={false} />
      <ToolCallItem item={nonString} turn={turn} live={false} />
    </>,
  );

  expect(screen.queryAllByTestId("tool-row-intent")).toHaveLength(0);
  expect(screen.queryAllByTestId("tool-row-summary")).toHaveLength(0);
  expect(screen.getAllByTestId("tool-row-status")).toHaveLength(2);
});

test("delegate tool rows use a single top-level disclosure trigger owned by ToolCallItem", () => {
  const { container } = render(
    <ToolCallItem
      item={item({
        toolName: "delegate",
        description: "Testing disclosure boundaries",
        argumentsJSON: JSON.stringify({ task: "Run tests" }),
      })}
      turn={turn}
      live={false}
    />,
  );
  // The disclosure trigger is a real button[aria-expanded] (ToolRow),
  // not a native <details>/<summary> - exactly one per tool call.
  const triggers = container.querySelectorAll('[data-testid="tool-row-trigger"][aria-expanded]');
  expect(triggers).toHaveLength(1);
});

test("a live, unsettled delegate call renders a running/working status dot (never unknown)", () => {
  render(
    <ToolCallItem
      item={item({
        toolName: "delegate",
        description: "Delegated task is still live",
        argumentsJSON: JSON.stringify({ task: "Keep going" }),
      })}
      turn={turn}
      live={true}
    />,
  );
  expect(screen.getByRole("img", { name: "Working" })).toBeTruthy();
  expect(screen.queryByText("unknown")).toBeNull();
});

// --- two-level disclosure: summaryOpen defaults by verbosity level --------
// Task 5: ToolCallItem computes summaryOpen from summaryOpenByDefault(config)
// (tools level → true, chat level → false) and passes it to ToolRow, whose
// intent button toggles the summary line independently of the body chevron.
// Intent-less rows force summaryOpen=true (their only line is the summary).

// A render helper that wraps ToolCallItem in a TranscriptRenderProvider so the
// verbosity level's content vector drives summaryOpenByDefault(config).
function renderWithConfig(config: ReturnType<typeof makeTranscriptDisplayConfig>, toolItem: ItemModel) {
  return render(
    <TranscriptRenderProvider config={config} surface="readOnly" disclosureScope="test:summary">
      <ToolCallItem item={toolItem} turn={turn} live={false} />
    </TranscriptRenderProvider>,
  );
}

test("at the tools level an intent-bearing row shows summary and the intent trigger controls the body", () => {
  registerToolRenderer({ match: "tci_summary_tools", summary: () => "Ran tests", body: () => <div>body</div> });
  const toolsConfig = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  renderWithConfig(
    toolsConfig,
    item({ id: "summary_tools", toolName: "tci_summary_tools", description: "Running the test suite" }),
  );
  // At tools level, summaryOpen defaults true but twoLevel is false (the
  // summary has no separate toggle — the intent button controls the body
  // directly, same as the legacy behavior).
  expect(screen.getByTestId("tool-row-intent").textContent).toBe("Running the test suite");
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("Ran tests");
  // The intent trigger controls the body (expanded), so aria-expanded=false
  // while the body is collapsed.
  const trigger = screen.getByTestId("tool-row-trigger");
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  // No separate body trigger at tools level — the intent button IS the toggle.
  expect(screen.queryByTestId("tool-row-body-trigger")).toBeNull();
});

test("at the chat level an intent-bearing row defaults summaryOpen=false (only intent visible)", () => {
  registerToolRenderer({ match: "tci_summary_chat", summary: () => "Ran tests", body: () => <div>body</div> });
  const chatConfig = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });
  renderWithConfig(
    chatConfig,
    item({ id: "summary_chat", toolName: "tci_summary_chat", description: "Running the test suite" }),
  );
  // summaryOpen=false → only the intent line is visible, summary hidden.
  expect(screen.getByTestId("tool-row-intent").textContent).toBe("Running the test suite");
  expect(screen.queryByTestId("tool-row-summary")).toBeNull();
  const trigger = screen.getByTestId("tool-row-trigger");
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
});

test("an intent-less row at the chat level forces summaryOpen=true (summary visible)", () => {
  registerToolRenderer({ match: "tci_summary_no_intent", summary: () => "Ran tests", body: () => <div>body</div> });
  const chatConfig = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });
  renderWithConfig(chatConfig, item({ id: "summary_no_intent", toolName: "tci_summary_no_intent" }));
  // No description → no intent line; the summary is the row's only line and
  // must be visible regardless of the verbosity level.
  expect(screen.queryByTestId("tool-row-intent")).toBeNull();
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("Ran tests");
});

test("a shell row with summaryHiddenWhenExpanded hides the summary when the body opens (tools level, legacy toggle)", () => {
  const toolsConfig = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  renderWithConfig(
    toolsConfig,
    item({
      id: "summary_shell_hidden",
      toolName: "shell",
      description: "Running a command",
      argumentsJSON: JSON.stringify({ command: "echo hi" }),
      output: "hi\n[exit 0]",
    }),
  );
  // At tools level, the intent trigger controls the body (legacy behavior).
  // The summary is visible while the body is collapsed.
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("Ran echo hi");

  // Expand the body via the intent trigger (the row's click target).
  // With summaryHiddenWhenExpanded, the summary disappears.
  const trigger = screen.getByTestId("tool-row-trigger");
  fireEvent.click(trigger);
  expect(screen.queryByTestId("tool-row-summary")).toBeNull();
  // The intent line stays.
  expect(screen.getByTestId("tool-row-intent").textContent).toBe("Running a command");
});

test("switching from chat to tools ignores persisted summary close (summary always visible at tools+)", () => {
  registerToolRenderer({ match: "tci_summary_switch", summary: () => "Ran tests", body: () => <div>body</div> });
  const chatConfig = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });
  const toolsConfig = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const toolItem = item({
    id: "summary_switch",
    toolName: "tci_summary_switch",
    description: "Running the test suite",
  });

  // At chat level, close the summary (persist a false choice).
  const { rerender } = renderWithConfig(chatConfig, toolItem);
  const trigger = screen.getByTestId("tool-row-trigger");
  // Summary starts hidden (chat defaults summaryOpen=false).
  expect(trigger.getAttribute("aria-expanded")).toBe("false");

  // Switch to tools level — the persisted close must NOT win.
  // summaryConfigDefault=true forces summaryOpen=true regardless.
  rerender(
    <TranscriptRenderProvider config={toolsConfig} surface="readOnly" disclosureScope="test:summary">
      <ToolCallItem item={toolItem} turn={turn} live={false} />
    </TranscriptRenderProvider>,
  );
  // The summary is visible at tools level.
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("Ran tests");
  // The intent trigger controls the body (legacy behavior, aria-expanded=false while collapsed).
  const toolsTrigger = screen.getByTestId("tool-row-trigger");
  expect(toolsTrigger.getAttribute("aria-expanded")).toBe("false");
  // No body trigger at tools level.
  expect(screen.queryByTestId("tool-row-body-trigger")).toBeNull();
});
