import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { memo, type ReactNode } from "react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "../../../protocol/model";
import { makeTranscriptDisplayConfig, type TranscriptDisplayConfigV1 } from "../../../transcriptDisplay/config";
import { projectThread } from "../../../transcriptDisplay/projector";
import { TranscriptRenderProvider } from "../../../transcriptDisplay/renderContext";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { isItemLive, TurnBlock } from "./TurnBlock";
import { type ItemRenderProps, ignoringTurn, itemRendererFor, registerItemRenderer } from "./types";
import "./tools";

// A tool row's open/closed state lives in the shared disclosureStore keyed by
// item.id, so a row this file opens must not leak into another test's row of the
// same id.
afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  const value = { id: "item_1", turnId: "turn_1", type: "somethingUnregistered", text: "", ...overrides };
  return value.type === "commandExecution" && value.description === undefined
    ? { ...value, description: "test action" }
    : value;
}

function turn(
  items: ItemModel[],
  overrides: Partial<TurnModel> = {},
  config: TranscriptDisplayConfigV1 = makeTranscriptDisplayConfig(
    { kind: "preset", level: "activity" },
    { roundTimings: true, systemEvents: true, promptEvents: true },
  ),
) {
  const source = { id: "turn_1", status: "inProgress", items, ...overrides };
  const projection = projectThread({ turns: [source] } as unknown as ThreadModel, config);
  const projected = projection.turns[0];
  if (!projected) throw new Error("test turn did not project");
  return projected;
}

function withConfig(config: TranscriptDisplayConfigV1, children: ReactNode) {
  return (
    <TranscriptRenderProvider config={config} surface="live" disclosureScope="turnblock:test">
      {children}
    </TranscriptRenderProvider>
  );
}

test("isItemLive: inProgress is live", () => {
  expect(isItemLive(item({ status: "inProgress" }))).toBe(true);
});

test("isItemLive: completed is not live", () => {
  expect(isItemLive(item({ status: "completed" }))).toBe(false);
});

test("isItemLive: an item with no status at all is not live", () => {
  expect(isItemLive(item({ status: undefined }))).toBe(false);
});

test("renders an empty turn without crashing", () => {
  const { container } = render(<TurnBlock turn={turn([])} />);
  expect(container.querySelector('[data-testid="turn-block"]')).toBeTruthy();
});

test("tags the root with the turn id", () => {
  const { container } = render(<TurnBlock turn={turn([], { id: "turn_42" })} />);
  expect(container.querySelector('[data-turn-id="turn_42"]')).toBeTruthy();
});

test("the turn root remains a centered, shrinkable reading column", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "turnblock.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  expect(css).toMatch(
    /\.turn\s*\{[\s\S]*width:\s*100%;[\s\S]*max-width:\s*var\(--session-measure\);[\s\S]*margin-inline:\s*auto;/,
  );
});

test("showSeenDivider defaults to false: no divider marker rendered", () => {
  render(<TurnBlock turn={turn([])} />);
  expect(screen.queryByTestId("seen-divider")).toBeNull();
});

test("showSeenDivider renders the divider marker above this turn's content", () => {
  render(<TurnBlock turn={turn([], { id: "turn_42" })} showSeenDivider />);
  expect(screen.getByTestId("seen-divider")).toBeTruthy();
});

test("renders items in order via the item-renderer registry", () => {
  const items = [
    item({ id: "a", type: "userMessage", text: "first" }),
    item({ id: "b", type: "agentMessage", text: "second" }),
    item({ id: "c", type: "userMessage", text: "third" }),
  ];
  render(<TurnBlock turn={turn(items)} />);
  // With the messages renderers registered (TurnBlock imports "./messages"
  // for TurnSeparator, whose module registers them as a side effect), these
  // types no longer fall back to the raw view - so assert ordering on the
  // block's text, renderer-agnostic: the registry must dispatch each item
  // in wire order regardless of which component won the type.
  const text = screen.getByTestId("turn-block").textContent ?? "";
  const positions = ["first", "second", "third"].map((t) => text.indexOf(t));
  expect(positions.every((p) => p >= 0)).toBe(true);
  expect([...positions]).toEqual([...positions].sort((a, b) => a - b));
});

test("dispatches a registered item type to its own renderer instead of the raw fallback", () => {
  function DummyRenderer({ item: i }: ItemRenderProps) {
    return <div data-testid="dummy-rendered">{i.text}</div>;
  }
  registerItemRenderer("tb-dummy-type", DummyRenderer);
  const items = [item({ id: "a", type: "tb-dummy-type", text: "via dummy" })];
  render(<TurnBlock turn={turn(items)} />);
  expect(screen.getByTestId("dummy-rendered").textContent).toBe("via dummy");
  expect(screen.queryByTestId("raw-item")).toBeNull();
});

test("dispatches a commandExecution item to ToolCallItem", () => {
  const items = [item({ id: "a", type: "commandExecution", toolName: "tb-tool-x", output: "tool output" })];
  render(<TurnBlock turn={turn(items)} />);
  expect(screen.getByTestId("tool-call-item")).toBeTruthy();
  // A tool row mounts its body only while open. At activity level
  // (expandByDefault=true) the body auto-expands, so the output is already
  // visible — confirming the descriptor's body ran rather than an empty shell.
  expect(screen.getByText("tool output")).toBeTruthy();
});

test("computes live per item from its own status, passed through to the renderer", () => {
  function LiveEcho({ live }: ItemRenderProps) {
    return <span data-testid="live-echo">{String(live)}</span>;
  }
  registerItemRenderer("tb-live-echo", LiveEcho);
  const items = [
    item({ id: "a", type: "tb-live-echo", status: "inProgress" }),
    item({ id: "b", type: "tb-live-echo", status: "completed" }),
  ];
  render(<TurnBlock turn={turn(items)} />);
  const echoes = screen.getAllByTestId("live-echo").map((el) => el.textContent);
  expect(echoes).toEqual(["true", "false"]);
});

test("passes the owning turn through to each item renderer", () => {
  function TurnEcho({ turn: t }: ItemRenderProps) {
    return <span data-testid="turn-echo">{t.id}</span>;
  }
  registerItemRenderer("tb-turn-echo", TurnEcho);
  const items = [item({ id: "a", type: "tb-turn-echo" })];
  render(<TurnBlock turn={turn(items, { id: "turn_owner" })} />);
  expect(screen.getByTestId("turn-echo").textContent).toBe("turn_owner");
});

test("passes opensExchange and agentLabel through ItemRenderProps", () => {
  const seen: Array<{ opensExchange?: boolean; agentLabel?: string }> = [];
  const originalAgentMessageRenderer = itemRendererFor("agentMessage");
  try {
    registerItemRenderer("agentMessage", (props) => {
      seen.push({ opensExchange: props.opensExchange, agentLabel: props.agentLabel });
      return null;
    });
    const agentItem = { id: "a1", turnId: "t1", type: "agentMessage", text: "hi", status: "completed" };
    render(<TurnBlock turn={turn([agentItem])} exchangeOpeners={new Set(["a1"])} agentLabel="k3" />);
    expect(seen).toEqual([{ opensExchange: true, agentLabel: "k3" }]);
  } finally {
    registerItemRenderer("agentMessage", originalAgentMessageRenderer);
  }
});

// The exact mechanism wave-4 T5c wraps most registered item renderers with
// (ToolCallItem, RawItemView, and every registered messages/ renderer except
// SystemNoticeItem - see each's own registerItemRenderer call site): a
// renderer memoized with `memo(Component, ignoringTurn)` must not re-render
// when TurnBlock re-renders with a NEW turn object but the SAME item
// reference and the same live-determining status - exactly what a streaming
// delta targeting a DIFFERENT item in the same turn produces (reducer.ts's
// immutable-update discipline: only the delta's own item gets a new
// reference, every sibling item keeps its exact reference, but the
// enclosing TurnModel is rebuilt every time).
test("a renderer memoized with ignoringTurn does not re-render when only the enclosing turn object changes (same item, same live)", () => {
  let renderCount = 0;
  const MemoEcho = memo(function MemoEcho({ item: i }: ItemRenderProps) {
    renderCount += 1;
    return <span data-testid="memo-echo">{i.text}</span>;
  }, ignoringTurn);
  registerItemRenderer("tb-memo-echo", MemoEcho);

  const sharedItem = item({ id: "a", type: "tb-memo-echo", text: "stable", status: "completed" });
  const { rerender } = render(<TurnBlock turn={turn([sharedItem], { id: "turn_1" })} />);
  expect(renderCount).toBe(1);
  expect(screen.getByTestId("memo-echo").textContent).toBe("stable");

  // A brand-new turn object (different id, different reference) - the SAME
  // item reference and thus the same computed `live` - must not re-invoke
  // MemoEcho's render function.
  rerender(<TurnBlock turn={turn([sharedItem], { id: "turn_2" })} />);
  expect(renderCount).toBe(1);
  expect(screen.getByTestId("memo-echo").textContent).toBe("stable");
});

// --- Projected visibility ----------------------------------------------------
// TranscriptBody applies configuration through projectThread before TurnBlock
// renders, so hidden items are gone before SystemNoticeItem computes grouping.

function systemItem(id: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return { id, turnId: "turn_1", type: "systemMessage", text: `notice ${id}`, ...overrides };
}

function hookItem(id: string, exitCode: number): ItemModel {
  return systemItem(id, { eventKind: "hook_completed", text: `hook ${id} exit ${exitCode}`, exitCode });
}

test("with both hook toggles off, only a non-zero hook survives as a compact critical row", () => {
  render(<TurnBlock turn={turn([hookItem("h", 0), hookItem("i", 1)])} />);
  expect(screen.queryByText(/hook h exit 0/)).toBeNull();
  expect(screen.getByText(/hook i exit 1/)).toBeTruthy();
  expect(screen.getByTestId("system-notice-failure")).toBeTruthy();
});

test("a blank-intent tool uses the projected neutral summary without a raw command summary", () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });
  const blankIntent = item({
    id: "critical-blank-intent",
    type: "commandExecution",
    toolName: "shell",
    argumentsJSON: JSON.stringify({ command: "echo should-not-be-recomputed" }),
    description: "  ",
    status: "completed",
  });
  const { rerender } = render(withConfig(config, <TurnBlock turn={turn([blankIntent], {}, config)} />));

  // ToolCallItem renders eagerly inside the intent group (jsdom does not hide
  // <details> children), so it is present even when the group is closed.
  expect(screen.getAllByTestId("tool-call-item")).toHaveLength(1);
  expect(screen.getByText("Action summary unavailable")).toBeTruthy();
  expect(screen.queryByText("Ran echo should-not-be-recomputed")).toBeNull();

  const tools = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  rerender(withConfig(tools, <TurnBlock turn={turn([blankIntent], {}, tools)} />));
  expect(screen.getAllByTestId("tool-call-item")).toHaveLength(1);
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("Action summary unavailable");
});

test("Chat renders a closed action group that expands reasons without tool UI (catches missing Chat intent)", () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });
  const action = item({
    id: "chat-action",
    type: "commandExecution",
    toolName: "shell",
    description: "Run focused checks",
    output: "private tool output",
    status: "completed",
  });
  render(withConfig(config, <TurnBlock turn={turn([action], { status: "completed" }, config)} />));

  const group = screen.getByTestId("intent-group");
  expect(group.hasAttribute("open")).toBe(false);
  // ToolCallItem renders eagerly inside the intent group (jsdom does not hide
  // <details> children), so it is present even when the group is closed.
  expect(screen.getByTestId("tool-call-item")).toBeTruthy();

  const summary = group.querySelector("summary");
  if (summary === null) throw new Error("Chat intent summary did not render");
  fireEvent.click(summary);
  expect(group.hasAttribute("open")).toBe(true);
  expect(screen.getByText("Run focused checks")).toBeTruthy();
  // The body is not yet expanded, so private output is hidden.
  expect(screen.queryByText("private tool output")).toBeNull();
  expect(screen.getByTestId("tool-call-item")).toBeTruthy();
});

test("Intent renders an open action group without a tool row (catches closed Intent default)", () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "intent" });
  const action = item({
    id: "intent-action",
    type: "commandExecution",
    toolName: "read_file",
    description: "Read the configuration",
    output: "private file output",
    status: "completed",
  });
  render(withConfig(config, <TurnBlock turn={turn([action], { status: "completed" }, config)} />));

  expect(screen.getByTestId("intent-group").hasAttribute("open")).toBe(true);
  expect(screen.getByText("Read the configuration")).toBeTruthy();
  expect(screen.queryByText("private file output")).toBeNull();
  // ToolCallItem renders eagerly inside the open intent group. Its body
  // (private output) is still collapsed.
  expect(screen.getByTestId("tool-call-item")).toBeTruthy();
});

test("named Intent opens only its action group while generic interaction and system disclosures stay closed", () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "intent" }, { promptEvents: true });
  const ordinaryAction = item({
    id: "intent-ordinary",
    type: "commandExecution",
    toolName: "read_file",
    description: "Read the configuration",
    status: "completed",
  });
  const interaction = item({
    id: "intent-question",
    type: "commandExecution",
    toolName: "ask_user",
    description: "Ask about mode",
    argumentsJSON: JSON.stringify({
      questions: [{ header: "Mode", question: "Choose", options: [{ label: "Fast", detail: "" }] }],
    }),
    status: "completed",
  });
  const systemPrompt = systemItem("intent-system-prompt", {
    eventKind: "system_prompt",
    text: "System prompt details",
    status: "completed",
  });
  render(
    withConfig(
      config,
      <TurnBlock turn={turn([ordinaryAction, interaction, systemPrompt], { status: "completed" }, config)} />,
    ),
  );

  expect(screen.getByTestId("intent-group").hasAttribute("open")).toBe(true);
  // At intent level, tool-row-trigger controls summaryOpen (not body). summaryOpenByDefault
  // is false at intent level (toolCalls is false), so all summary triggers start collapsed.
  const triggers = screen.getAllByTestId("tool-row-trigger");
  for (const trigger of triggers) {
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
  }
  expect(screen.getByTestId("system-notice-scaffold").hasAttribute("open")).toBe(false);
});

test("failed intent proxy renders the accessible FailureGlyph and neutral missing summary (catches full tool row)", () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "intent" });
  const failedAction = item({
    id: "failed-intent-action",
    type: "commandExecution",
    toolName: "shell",
    description: "   ",
    error: "command failed",
    output: "sensitive failure output",
    status: "failed",
  });
  render(withConfig(config, <TurnBlock turn={turn([failedAction], { status: "completed" }, config)} />));

  expect(screen.getByTestId("intent-group")).toBeTruthy();
  expect(screen.getByRole("img", { name: "Failed" })).toBeTruthy();
  // ToolCallItem renders eagerly inside the intent group. The failed row
  // auto-expands its body (failure earns the eye). Shell's summaryHiddenWhenExpanded
  // hides the summary line while the body is open, so "Action summary unavailable"
  // (the projected neutral summary) is not shown as visible text — the body's
  // error output is the single representation instead.
  expect(screen.getByTestId("tool-call-item")).toBeTruthy();
  expect(screen.getByText("command failed")).toBeTruthy();
  expect(screen.queryByText("Action summary unavailable")).toBeNull();
});

test("intent row drills down through 3 levels: intent button -> summary, body chevron -> body", () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "intent" });
  const action = item({
    id: "intent-drill-action",
    type: "commandExecution",
    toolName: "read_file",
    description: "Read the configuration",
    output: "private file output",
    status: "completed",
  });
  render(withConfig(config, <TurnBlock turn={turn([action], { status: "completed" }, config)} />));

  // Level 1: the intent group is open at intent level, showing the ToolCallItem.
  expect(screen.getByTestId("intent-group").hasAttribute("open")).toBe(true);
  // ToolCallItem renders eagerly (jsdom does not hide <details> children).
  expect(screen.getByTestId("tool-call-item")).toBeTruthy();
  // Tool icon is rendered (read_file uses the file icon kind).
  expect(screen.getByTestId("tool-row-icon")).toBeTruthy();
  // Intent text is visible.
  expect(screen.getByText("Read the configuration")).toBeTruthy();
  // The summary trigger (tool-row-trigger) controls summaryOpen, not body.
  // At intent level summaryOpen defaults false, so the summary is hidden.
  const summaryTrigger = screen.getByTestId("tool-row-trigger");
  expect(summaryTrigger.getAttribute("aria-expanded")).toBe("false");
  // The body trigger only renders once the summary is visible OR the body is
  // expanded (ToolRow places it on the summary line or the intent line). With
  // both collapsed it is not yet in the DOM.
  expect(screen.queryByTestId("tool-row-body-trigger")).toBeNull();
  // Private output is hidden (body not yet expanded).
  expect(screen.queryByText("private file output")).toBeNull();

  // Level 2: click the summary trigger to reveal the summary line.
  fireEvent.click(summaryTrigger);
  expect(summaryTrigger.getAttribute("aria-expanded")).toBe("true");
  // The summary is now visible (read_file's summary text appears).
  expect(screen.getByTestId("tool-row-summary")).toBeTruthy();
  // The body trigger now renders on the summary line.
  const bodyTrigger = screen.getByTestId("tool-row-body-trigger");
  expect(bodyTrigger.getAttribute("aria-expanded")).toBe("false");
  expect(screen.queryByText("private file output")).toBeNull();

  // Level 3: click the body trigger to reveal the body.
  fireEvent.click(bodyTrigger);
  expect(bodyTrigger.getAttribute("aria-expanded")).toBe("true");
  // The private output is now visible.
  expect(screen.getByText("private file output")).toBeTruthy();
});

test("a growing Chat group keeps its first-action identity and manually opened state (catches last-id key)", () => {
  const config = makeTranscriptDisplayConfig({
    kind: "custom",
    toolIntent: true,
    toolCalls: false,
    reasoning: false,
    expandByDefault: false,
  });
  const first = item({
    id: "stream-first",
    type: "commandExecution",
    description: "First action",
    status: "completed",
  });
  const second = item({
    id: "stream-second",
    type: "commandExecution",
    description: "Second action",
    status: "completed",
  });
  const { rerender } = render(withConfig(config, <TurnBlock turn={turn([first], { status: "completed" }, config)} />));
  const group = screen.getByTestId("intent-group");
  const summary = group.querySelector("summary");
  if (summary === null) throw new Error("Chat streaming summary did not render");
  fireEvent.click(summary);
  expect(group.hasAttribute("open")).toBe(true);

  rerender(withConfig(config, <TurnBlock turn={turn([first, second], { status: "completed" }, config)} />));

  expect(screen.getByTestId("intent-group")).toBe(group);
  expect(group.textContent).toContain("2 actions");
  expect(group.hasAttribute("open")).toBe(true);
});

test("a growing Intent group keeps its first-action identity and manually closed state (catches last-id key)", () => {
  const config = makeTranscriptDisplayConfig({
    kind: "custom",
    toolIntent: true,
    toolCalls: false,
    reasoning: false,
    expandByDefault: true,
  });
  const first = item({
    id: "stream-first",
    type: "commandExecution",
    description: "First action",
    status: "completed",
  });
  const second = item({
    id: "stream-second",
    type: "commandExecution",
    description: "Second action",
    status: "completed",
  });
  const { rerender } = render(withConfig(config, <TurnBlock turn={turn([first], { status: "completed" }, config)} />));
  const group = screen.getByTestId("intent-group");
  const summary = group.querySelector("summary");
  if (summary === null) throw new Error("Intent streaming summary did not render");
  expect(group.hasAttribute("open")).toBe(true);
  fireEvent.click(summary);
  expect(group.hasAttribute("open")).toBe(false);

  rerender(withConfig(config, <TurnBlock turn={turn([first, second], { status: "completed" }, config)} />));

  expect(screen.getByTestId("intent-group")).toBe(group);
  expect(group.textContent).toContain("2 actions");
  expect(group.hasAttribute("open")).toBe(false);
});

test("hookExitsAll renders full hook rows; hookExitsNormal keeps success rows plus a compact failure", () => {
  const items = [hookItem("clean", 0), hookItem("failed", 1)];
  const all = makeTranscriptDisplayConfig(
    { kind: "preset", level: "activity" },
    { systemEvents: true, hookExits: "all" },
  );
  const successful = makeTranscriptDisplayConfig(
    { kind: "preset", level: "activity" },
    { systemEvents: true, hookExits: "successful" },
  );
  const { rerender } = render(withConfig(all, <TurnBlock turn={turn(items, {}, all)} />));
  expect(screen.getByText(/hook clean exit 0/)).toBeTruthy();
  expect(screen.getByText(/hook failed exit 1/)).toBeTruthy();

  rerender(withConfig(successful, <TurnBlock turn={turn(items, {}, successful)} />));
  expect(screen.getByText(/hook clean exit 0/)).toBeTruthy();
  expect(screen.getByText(/hook failed exit 1/)).toBeTruthy();
  expect(screen.getByTestId("system-notice-failure")).toBeTruthy();
});

test("a configuration change re-renders the transcript through the provider", () => {
  const items = [hookItem("h", 0)];
  const hidden = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" }, { systemEvents: true });
  const shown = makeTranscriptDisplayConfig(
    { kind: "preset", level: "activity" },
    { systemEvents: true, hookExits: "all" },
  );
  const { rerender } = render(withConfig(hidden, <TurnBlock turn={turn(items, {}, hidden)} />));
  expect(screen.queryByText(/hook h exit 0/)).toBeNull();

  rerender(withConfig(shown, <TurnBlock turn={turn(items, {}, shown)} />));
  expect(screen.getByText(/hook h exit 0/)).toBeTruthy();
});

// The reason filtering happens in TurnBlock rather than inside each renderer:
// SystemNoticeItem groups a run of 3+ ADJACENT systemMessage items into one
// disclosure whose summary counts them. Hiding an item any later would leave
// the survivors wrongly grouped and the count overstated.
test("a hidden item is excluded from system-run grouping, not merely from the output", () => {
  const items = [systemItem("a"), hookItem("h", 1), systemItem("c")];
  render(<TurnBlock turn={turn(items)} />);

  // Three adjacent system items minus the hidden hook leaves two - below the
  // grouping threshold, so each must stand alone and no group may appear.
  expect(screen.queryByTestId("system-notice-group")).toBeNull();
  expect(screen.getByText("notice a")).toBeTruthy();
  expect(screen.getByText("notice c")).toBeTruthy();
});

test("the same three items DO group once the hidden one is shown again", () => {
  const items = [systemItem("a"), hookItem("h", 1), systemItem("c")];
  const all = makeTranscriptDisplayConfig(
    { kind: "preset", level: "activity" },
    { systemEvents: true, hookExits: "all" },
  );
  render(withConfig(all, <TurnBlock turn={turn(items, {}, all)} />));

  const group = screen.getByTestId("system-notice-group");
  expect(group.textContent).toContain("3 system events");
});

// Sets the provider config both ways explicitly rather than leaning on its
// default: this asserts what the projected configuration does.
test("promptLoaded off hides the system-prompt scaffold disclosure; on shows it", () => {
  const items = [systemItem("p", { eventKind: "system_prompt", text: "You are a helpful assistant." })];
  const hidden = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" }, { promptEvents: false });
  const shown = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" }, { promptEvents: true });
  const { rerender } = render(withConfig(hidden, <TurnBlock turn={turn(items, {}, hidden)} />));
  expect(screen.queryByTestId("system-notice-scaffold")).toBeNull();

  rerender(withConfig(shown, <TurnBlock turn={turn(items, {}, shown)} />));
  expect(screen.getByTestId("system-notice-scaffold").textContent).toContain("System prompt");
});

test("items no toggle governs are untouched with every toggle off", () => {
  const items = [
    item({ id: "u", type: "userMessage", text: "hello" }),
    systemItem("s", { eventKind: "skill_activated", text: "Activated skill: x" }),
  ];
  render(<TurnBlock turn={turn(items)} />);
  const text = screen.getByTestId("turn-block").textContent ?? "";
  expect(text).toContain("hello");
  expect(text).toContain("Activated skill: x");
});

// --- Layout roles (layoutRoles.ts) ----------------------------------------
// Two roles, no exception set: "speaker" rows (userMessage, exchange-opening
// agentMessage) render full-width outside any [data-testid="run-content"]
// wrapper; "run" rows - EVERYTHING else, including steering, system notices,
// warnings, and unknown future types - render inside the wrapper and take
// the gutter indent. The steering/notification indent is the consistency fix
// (Jesse's review call); these tests pin it.

function expectOutsideRunContent(el: HTMLElement) {
  expect(el.closest('[data-testid="run-content"]')).toBeNull();
}

function expectInsideRunContent(el: HTMLElement) {
  expect(el.closest('[data-testid="run-content"]')).not.toBeNull();
}

test("speaker rows render outside any run-content wrapper: userMessage always", () => {
  render(<TurnBlock turn={turn([item({ id: "u", type: "userMessage", text: "hi there" })])} />);
  expectOutsideRunContent(screen.getByTestId("user-message-item"));
  expect(screen.queryByTestId("run-content")).toBeNull();
});

test("steering, systemMessage, and warning are run rows: they render INSIDE a run-content wrapper (the consistency fix)", () => {
  const items = [
    item({ id: "st", type: "steering", text: "keep going" }),
    systemItem("s", { eventKind: "skill_activated", text: "Activated skill: x" }),
    item({ id: "w", type: "warning", text: "context nearly full" }),
  ];
  render(<TurnBlock turn={turn(items)} />);
  expectInsideRunContent(screen.getByTestId("steering-item"));
  expectInsideRunContent(screen.getByTestId("system-notice-line"));
  expectInsideRunContent(screen.getByTestId("warning-item"));
  expect(screen.getAllByTestId("run-content")).toHaveLength(3);
});

test("agentMessage and reasoning render inside a run-content wrapper", () => {
  // At activity level reasoning=false hides reasoning items; use full level
  // (reasoning=true) so the think-block renders.
  const fullConfig = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  const items = [
    item({ id: "a", type: "agentMessage", text: "reply" }),
    item({ id: "r", type: "reasoning", reasoningSummaries: [["hmm"]], status: "completed" }),
  ];
  render(<TurnBlock turn={turn(items, {}, fullConfig)} />);
  expectInsideRunContent(screen.getByTestId("agent-message-item"));
  expectInsideRunContent(screen.getByTestId("think-block"));
});

test("an exchange-OPENING agentMessage is the avatar row: it renders OUTSIDE the wrapper so its avatar sits in the gutter", () => {
  const opener = item({ id: "a-open", type: "agentMessage", text: "reply" });
  const { unmount } = render(<TurnBlock turn={turn([opener])} exchangeOpeners={new Set(["a-open"])} />);
  expectOutsideRunContent(screen.getByTestId("agent-message-item"));
  unmount();
  // ...and a mid-exchange agentMessage in the same turn still takes the indent.
  render(
    <TurnBlock
      turn={turn([item({ id: "a-mid", type: "agentMessage", text: "more" })])}
      exchangeOpeners={new Set(["a-open"])}
    />,
  );
  expectInsideRunContent(screen.getByTestId("agent-message-item"));
});

test("a lone commandExecution renders inside a run-content wrapper", () => {
  const items = [item({ id: "t", type: "commandExecution", toolName: "tb-tool-solo", output: "out" })];
  render(<TurnBlock turn={turn(items)} />);
  expectInsideRunContent(screen.getByTestId("tool-call-item"));
});

test("an unknown future item type defaults to run content", () => {
  const items = [item({ id: "x", type: "someFutureWireType", text: "mystery" })];
  render(<TurnBlock turn={turn(items)} />);
  expectInsideRunContent(screen.getByTestId("raw-item"));
});

test("transcript chrome stays outside any wrapper: TurnSeparator and SeenDivider", () => {
  const items = [item({ id: "a", type: "agentMessage", text: "reply" })];
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" }, { roundTimings: true });
  render(withConfig(config, <TurnBlock turn={turn(items, { durationMs: 1500 }, config)} showSeenDivider />));
  expectOutsideRunContent(screen.getByTestId("turn-separator"));
  expectOutsideRunContent(screen.getByTestId("seen-divider"));
});

test("the gutter indent is one media query at exactly 700px with padding-left: var(--speaker-gutter)", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "turnblock.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  expect(css).toMatch(
    /@media\s*\(min-width:\s*700px\)\s*\{\s*\.runContent\s*\{\s*padding-left:\s*var\(--speaker-gutter\);\s*\}\s*\}/,
  );
});

test("the speaker geometry is declared once on .turn: 34px gutter = 24px avatar + 10px gap", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "turnblock.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  expect(css).toMatch(
    // Speaker geometry hoisted to tokens.css - .turn must NOT redeclare it.
    /\.turn\s*\{(?![\s\S]*--speaker-gap:)/,
  );
});

// Overflow containment (2026-07-30-mobile-session-layout-design.md, decision
// 5): .turn sits in the virtual list's width:100% item; min-width: 0 keeps a
// long child (a wide diff, an unwrapped token) from pinning the column wider
// than the item that contains it.
test("the turn column can shrink below its content's natural width", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "turnblock.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const turn = css.match(/\.turn \{([^}]*)\}/);
  expect(turn).not.toBeNull();
  expect(turn![1]).toContain("min-width: 0");
});

// --- Folding a settled run of tool calls (critique R9) --------------------
//
// These render at the "tools" level - the shipped desktop default, whose
// expandedDetails is off, so a fold actually folds. At "activity"/"full" the
// reader has asked for everything expanded and a run opens with the rest.

const TOOLS_CONFIG = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });

function toolItem(id: string, toolName: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return item({
    id,
    type: "commandExecution",
    toolName,
    status: "completed",
    argumentsJSON: JSON.stringify({ file_path: `${id}.ts` }),
    ...overrides,
  });
}

function renderTools(items: ItemModel[], turnOverrides: Partial<TurnModel> = { status: "completed" }) {
  render(withConfig(TOOLS_CONFIG, <TurnBlock turn={turn(items, turnOverrides, TOOLS_CONFIG)} />));
}

function openRun() {
  const summary = screen.getByTestId("tool-run").querySelector("summary");
  if (summary === null) throw new Error("the folded run rendered no summary to click");
  fireEvent.click(summary);
}

test("a settled turn folds three quiet tool calls into one row that opens to them", () => {
  renderTools([toolItem("a", "read_file"), toolItem("b", "read_file"), toolItem("c", "read_file")]);
  expect(screen.getAllByTestId("tool-run")).toHaveLength(1);
  expect(screen.queryAllByTestId("tool-row")).toHaveLength(0);
  openRun();
  expect(screen.getAllByTestId("tool-row")).toHaveLength(3);
});

test("the folded row counts the steps and names the consequential one", () => {
  renderTools([
    toolItem("a", "read_file"),
    toolItem("b", "write_file", { argumentsJSON: JSON.stringify({ file_path: "foo.py" }) }),
    toolItem("c", "read_file"),
  ]);
  expect(screen.getByTestId("tool-run").textContent).toContain("3 steps · Wrote foo.py");
});

test("a folded run's anchor lists the entries it stands in for", () => {
  // roborev on PR #947: scroll/focus restoration for the second or third call
  // needs a way from that call's id to the one anchor the run renders.
  render(
    withConfig(
      TOOLS_CONFIG,
      <TurnBlock
        turn={turn(
          [toolItem("a", "read_file"), toolItem("b", "read_file"), toolItem("c", "read_file")],
          { status: "completed" },
          TOOLS_CONFIG,
        )}
        viewAnchorIndex={0}
      />,
    ),
  );
  const anchor = document.querySelector('[data-view-anchor-id="run:a"]');
  expect(anchor?.getAttribute("data-view-anchor-members")).toBe("a,b,c");
});

test("two calls are not a run: they keep their own rows", () => {
  renderTools([toolItem("a", "read_file"), toolItem("b", "read_file")]);
  expect(screen.queryByTestId("tool-run")).toBeNull();
  expect(screen.getAllByTestId("tool-row")).toHaveLength(2);
});

test("a failed call breaks the run: every row stays visible", () => {
  renderTools([
    toolItem("a", "read_file"),
    toolItem("b", "read_file", { status: "failed", error: "no such file" }),
    toolItem("c", "read_file"),
    toolItem("d", "read_file"),
  ]);
  expect(screen.queryByTestId("tool-run")).toBeNull();
  expect(screen.getAllByTestId("tool-row")).toHaveLength(4);
});

test("a live turn never folds: each call keeps its own row while the agent works", () => {
  renderTools([toolItem("a", "read_file"), toolItem("b", "read_file"), toolItem("c", "read_file")], {
    status: "inProgress",
  });
  expect(screen.queryByTestId("tool-run")).toBeNull();
  expect(screen.getAllByTestId("tool-row")).toHaveLength(3);
});

test("a never-fold tool splits the run around itself", () => {
  renderTools([
    toolItem("a", "read_file"),
    toolItem("b", "use_skill"),
    toolItem("c", "read_file"),
    toolItem("d", "read_file"),
    toolItem("e", "read_file"),
  ]);
  expect(screen.getAllByTestId("tool-run")).toHaveLength(1);
  expect(screen.getAllByTestId("tool-row")).toHaveLength(2);
});

test("a folded run takes the run-content indent, like the rows it stands in for", () => {
  renderTools([toolItem("a", "read_file"), toolItem("b", "read_file"), toolItem("c", "read_file")]);
  expectInsideRunContent(screen.getByTestId("tool-run"));
});

test("a folded run keeps a view anchor so scroll coordination still has a position for it", () => {
  render(
    withConfig(
      TOOLS_CONFIG,
      <TurnBlock
        turn={turn(
          [toolItem("a", "read_file"), toolItem("b", "read_file"), toolItem("c", "read_file")],
          { status: "completed" },
          TOOLS_CONFIG,
        )}
        viewAnchorIndex={7}
      />,
    ),
  );
  const anchor = document.querySelector('[data-view-anchor-id="run:a"]');
  expect(anchor).not.toBeNull();
  expect(anchor?.getAttribute("data-view-anchor-index")).toBe("7");
});
