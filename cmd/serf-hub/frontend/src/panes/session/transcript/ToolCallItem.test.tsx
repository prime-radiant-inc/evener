import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { ToolCallItem } from "./ToolCallItem";
import { registerToolRenderer, type ToolRenderProps } from "./toolRenderers";
import { ignoringTurn, itemRendererFor } from "./types";
import "./tools/shellTool"; // registers the real "shell" descriptor, incl. its own autoExpand heuristic
import "./tools/fsTools"; // registers the real "read_file" (openBesidePath) + grep/list_dir/glob (opt-out)
import type { ItemModel, ThreadModel, TurnModel } from "../../../protocol/model";
import * as paneActions from "../../../shell/paneActions";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import { resetThreadsStoreForTests, threadsStore } from "../../../stores/threads";
import { resetSubagentModuleStoreForTests } from "./tools/subagentModuleStore";

// Node 26 shadows jsdom's real window.localStorage with its own
// (non-functional under vitest) global - same stand-in TurnSeparator.test.tsx
// uses, needed here too now that the duration-gating tests below touch the
// prefs store (which persists to localStorage).
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

beforeEach(() => {
  localStorage.clear();
  resetPrefsStoreForTests();
});

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

test("the same tool item id has independent disclosure state in different sessions", () => {
  registerToolRenderer({ match: "tci_session_isolation", summary: () => "s", body: () => <div>body text</div> });
  const shared = item({ id: "same_item", toolName: "tci_session_isolation" });
  render(
    <>
      <ToolCallItem item={shared} turn={turn} live={false} sessionRef="session_a" />
      <ToolCallItem item={shared} turn={turn} live={false} sessionRef="session_b" />
    </>,
  );

  const rows = screen.getAllByTestId("tool-call-item") as HTMLDetailsElement[];
  expect(rows).toHaveLength(2);
  expect(rows[0]?.open).toBe(false);
  expect(rows[1]?.open).toBe(false);

  fireEvent.click(rows[0]!.querySelector("summary")!);
  expect(rows[0]?.open).toBe(true);
  expect(rows[1]?.open).toBe(false);
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

  const details = screen.getByTestId("tool-call-item") as HTMLDetailsElement;
  // Still attributable: the failure marker never goes away.
  expect(details.getAttribute("data-failed")).toBe("true");
  expect(screen.getByTestId("failure-glyph")).toBeTruthy();
  // But no longer forced open, since the very next attempt succeeded.
  expect(details.open).toBe(false);
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

  expect((screen.getByTestId("tool-call-item") as HTMLDetailsElement).open).toBe(true);
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

  expect((screen.getByTestId("tool-call-item") as HTMLDetailsElement).open).toBe(true);
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

  expect((screen.getByTestId("tool-call-item") as HTMLDetailsElement).open).toBe(true);
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
  expect((screen.getByTestId("tool-call-item") as HTMLDetailsElement).open).toBe(true);

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

  expect((screen.getByTestId("tool-call-item") as HTMLDetailsElement).open).toBe(false);
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
  const details = screen.getByTestId("tool-call-item") as HTMLDetailsElement;
  expect(details.open).toBe(false);

  fireEvent.click(screen.getByTestId("tool-row"));
  expect(details.open).toBe(true);
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
  threadsStore.setState({ threads: new Map([["ref_a", { name: "" } as unknown as ThreadModel]]) });
  render(<ToolCallItem item={item({ toolName: "tci_suffix" })} turn={turn} live={false} sessionRef="ref_a" />);
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("base summary");

  act(() => {
    threadsStore.setState({ threads: new Map([["ref_a", { name: "answered" } as unknown as ThreadModel]]) });
  });
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("base summary — answered");
});

test("summarySuffix is omitted (bare summary) when the descriptor doesn't define one", () => {
  resetThreadsStoreForTests();
  registerToolRenderer({ match: "tci_no_suffix", summary: () => "plain summary" });
  render(<ToolCallItem item={item({ toolName: "tci_no_suffix" })} turn={turn} live={false} sessionRef="ref_a" />);
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("plain summary");
});

// --- per-tool-call duration (P1, ux-plan-2026-07.md): opt-in via the same
// "Round timings" preference TurnSeparator already reads for the turn-level
// figure - not a separate toggle. That preference now ships ON by default
// (prefs.ts, ux-plan-2026-07.md); the gating tests below set it explicitly
// in each direction so they keep proving the gate works rather than riding
// on whichever way the default happens to point. --------

const timedItem = (overrides: Partial<ItemModel> = {}) =>
  item({
    toolName: "tci_timed",
    startedAt: "2026-01-01T00:00:00.000Z",
    completedAt: "2026-01-01T00:00:00.038Z",
    ...overrides,
  });

test("Round timings off: no duration renders even though the item carries real start/end", () => {
  prefsStore.getState().setTranscriptStatus("roundTimings", false);
  registerToolRenderer({ match: "tci_timed", summary: () => "did a thing" });
  render(<ToolCallItem item={timedItem()} turn={turn} live={false} />);
  expect(screen.queryByTestId("tool-row-duration")).toBe(null);
});

test("Round timings on (the default): a settled call with startedAt/completedAt shows its duration", () => {
  prefsStore.getState().setTranscriptStatus("roundTimings", true);
  registerToolRenderer({ match: "tci_timed", summary: () => "did a thing" });
  render(<ToolCallItem item={timedItem()} turn={turn} live={false} />);
  expect(screen.getByTestId("tool-row-duration").textContent).toBe(" · 38ms");
});

test("Round timings on but no startedAt/completedAt (e.g. a call still in flight): no duration, no crash", () => {
  prefsStore.getState().setTranscriptStatus("roundTimings", true);
  registerToolRenderer({ match: "tci_timed", summary: () => "did a thing" });
  render(<ToolCallItem item={timedItem({ startedAt: undefined, completedAt: undefined })} turn={turn} live={false} />);
  expect(screen.queryByTestId("tool-row-duration")).toBe(null);
});

test("the duration segment does not disturb the summary text an unrelated test asserted byte-for-byte", () => {
  prefsStore.getState().setTranscriptStatus("roundTimings", false);
  registerToolRenderer({ match: "tci_timed", summary: () => "did a thing" });
  render(<ToolCallItem item={timedItem()} turn={turn} live={false} />);
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("did a thing");
});

test("delegate tool rows keep the human description as purpose and suppress the technical delegate summary", () => {
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
  expect(screen.getByTestId("tool-row-purpose").textContent).toBe("Testing delegation");
  expect(screen.queryByTestId("tool-row-summary")).toBeNull();
});

test("task-only delegate purpose previews preserve an emoji at the Unicode clipping boundary", () => {
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
  expect(within(unicodeTool!).getByTestId("tool-row-purpose").textContent).toBe(`${"a".repeat(119)}😀…`);
  expect(within(asciiTool!).getByTestId("tool-row-purpose").textContent).toBe(exactAsciiTask);
});

test("task-only delegates show a bounded purpose preview while keeping the full Mandate in one disclosure", () => {
  const task =
    "**Inspect** the parser\n\nand report the `task` field. Keep the full mandate available for the reader. This suffix makes the preview bounded and honest.";
  const { container } = render(
    <ToolCallItem
      item={item({
        id: "task_only_delegate",
        callId: "call_task_only_delegate",
        toolName: "delegate",
        argumentsJSON: JSON.stringify({ task, mode: "foreground_timeout", delegateId: "dlg_task_only" }),
        output: JSON.stringify({ job_id: "job_task_only", status: "completed", transcript_ref: "ref_task_only" }),
      })}
      turn={turn}
      live={false}
    />,
  );

  const tool = screen.getByTestId("tool-call-item");
  const module = screen.getByTestId("subagent-module");
  const purpose = screen.getByTestId("tool-row-purpose");
  expect(purpose.textContent).toBe(
    "**Inspect** the parser and report the `task` field. Keep the full mandate available for the reader. This suffix makes th…",
  );
  expect(purpose.textContent).not.toBe(task);
  const mandate = screen.getByTestId("subagent-mandate");
  expect(mandate.lastElementChild?.textContent).toBe(task);
  expect(tool.querySelectorAll("details > summary")).toHaveLength(1);
  expect(module.querySelectorAll("details > summary")).toHaveLength(0);
  expect(within(tool).getByTestId("tool-row-status").querySelector('[role="img"]')).toBeTruthy();

  const openTranscript = within(module).getByRole("button", { name: "Open transcript" });
  expect(openTranscript.textContent).toContain("open");
  expect(openTranscript.querySelector("svg")).toBeTruthy();
  expect(container.querySelectorAll('[data-testid="tool-row"]')).toHaveLength(1);
});

test("malformed delegate arguments keep status without inventing a purpose", () => {
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

  expect(screen.queryByTestId("tool-row-purpose")).toBeNull();
  expect(screen.queryByTestId("tool-row-summary")).toBeNull();
  expect(screen.getByTestId("tool-row-status")).toBeTruthy();
  expect(screen.queryByText("delegate")).toBeNull();
});

test("blank and non-string delegate tasks keep status without inventing a purpose", () => {
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

  expect(screen.queryAllByTestId("tool-row-purpose")).toHaveLength(0);
  expect(screen.queryAllByTestId("tool-row-summary")).toHaveLength(0);
  expect(screen.getAllByTestId("tool-row-status")).toHaveLength(2);
});

test("delegate tool rows use a single top-level details/summary disclosure owned by ToolCallItem", () => {
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
  const summaries = container.querySelectorAll("details > summary");
  expect(summaries).toHaveLength(1);
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
