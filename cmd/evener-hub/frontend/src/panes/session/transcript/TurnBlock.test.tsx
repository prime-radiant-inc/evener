import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { ItemModel, ThreadModel, TurnModel } from "@evener/appwire-client";
import {
  ACTION_SUMMARY_UNAVAILABLE,
  hydrateThread,
  makeTranscriptDisplayConfig,
  projectThread,
  type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { memo, type ReactNode } from "react";
import { afterEach, expect, test } from "vitest";
import { accessibleName } from "../../../accessibleNameTestUtils";
import { readModuleCss } from "../../../styles/cssBlock";
import { TranscriptRenderProvider } from "../../../transcriptDisplay/renderContext";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { isItemLive, ProjectedIntentGroup, TurnBlock } from "./TurnBlock";
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

// Spec "Turn status": running state lives in the overlay, never the wire's
// own inProgress status. An open turn the thread does not name as its
// running turn - a daemonless read of a turn a crash left open, or a turn
// whose execution stood down - displays as interrupted, never as running
// forever (displayTurnStatus, itemFailure.ts).
test("an open turn with no running turn renders as interrupted", () => {
  const { container } = render(
    <TurnBlock
      turn={turn([], { id: "turn_1", status: "inProgress" })}
      thread={{ runningTurnId: undefined } as ThreadModel}
    />,
  );
  expect(container.querySelector('[data-turn-id="turn_1"]')?.getAttribute("data-turn-status")).toBe("interrupted");
});

test("an open turn the thread names as its running turn renders as running", () => {
  const { container } = render(
    <TurnBlock
      turn={turn([], { id: "turn_1", status: "inProgress" })}
      thread={{ runningTurnId: "turn_1" } as ThreadModel}
    />,
  );
  expect(container.querySelector('[data-turn-id="turn_1"]')?.getAttribute("data-turn-status")).toBe("inProgress");
});

test("a settled turn's status passes through untouched regardless of the running turn", () => {
  const { container } = render(
    <TurnBlock
      turn={turn([], { id: "turn_1", status: "completed" })}
      thread={{ runningTurnId: undefined } as ThreadModel}
    />,
  );
  expect(container.querySelector('[data-turn-id="turn_1"]')?.getAttribute("data-turn-status")).toBe("completed");
});

// Spec "The live overlay": a notice is anchored at a position between two
// recorded items, and reducer.ts's buildDisplayTurn already splices it into
// turn.items at that position (task-15-report.md). TurnBlock's job is to
// render whatever order turn.items presents, notices included, without
// reordering, folding, or hiding a critical one (eventKind "error" is always
// shown - decisionFor routes it to "critical" before any settings check).
test("a notice item interleaved between two turn items renders at its position, not at the end", () => {
  const before = item({ id: "before", type: "agentMessage", text: "before the notice" });
  const notice = item({ id: "the-notice", type: "systemMessage", eventKind: "error", text: "history failed" });
  const after = item({ id: "after", type: "agentMessage", text: "after the notice" });
  const { container } = render(<TurnBlock turn={turn([before, notice, after])} viewAnchorIndex={0} />);
  const ids = Array.from(container.querySelectorAll("[data-view-anchor-id]")).map((el) =>
    el.getAttribute("data-view-anchor-id"),
  );
  expect(ids).toEqual(["before", "the-notice", "after"]);
  expect(screen.getByText("history failed")).toBeTruthy();
});

test("wires a content-free thinking entry through TurnBlock to the placeholder (never the thought body)", () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const projected = turn(
    [item({ type: "reasoning", status: "inProgress", reasoningSummaries: [["abcdefghijklmnop"]] })],
    {},
    config,
  );
  render(withConfig(config, <TurnBlock turn={projected} />));
  expect(screen.getByTestId("think-block").getAttribute("data-live")).toBe("true");
  expect(screen.getByTestId("loader-grid")).toBeTruthy();
  expect(screen.queryByTestId("think-block-live-body")).toBeNull();
  expect(screen.queryByText("abcdefghijklmnop")).toBeNull();
});

test("redacts a critical reasoning row on a terminal turn", () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const projected = turn(
    [item({ type: "reasoning", status: "failed", reasoningSummaries: [["abcdefghijklmnop"]] })],
    { status: "interrupted" },
    config,
  );
  render(withConfig(config, <TurnBlock turn={projected} />));
  expect(screen.getByTestId("think-block").getAttribute("data-redacted")).toBe("true");
  expect(screen.getByTestId("think-block-redacted").textContent).not.toBe("");
  // The neutral label is the collapsed summary; the thought sits behind the
  // unfold and renders nothing while closed.
  const details = screen.getByRole("group") as HTMLDetailsElement;
  expect(details.open).toBe(false);
  expect(screen.queryByText("abcdefghijklmnop")).toBeNull();
  expect(screen.queryByTestId("think-block-live-body")).toBeNull();
  fireEvent.click(details.querySelector("summary")!);
  expect(screen.getByText("abcdefghijklmnop")).toBeTruthy();
});

test("a thought whose visible successor is filtered out keeps the projector's settled state", () => {
  // The projector decides liveness on the SOURCE turn: this thought is
  // superseded by the described command (the turn's tail) and carries
  // live:false. The command is hidden at this level, so a renderer deriving
  // liveness from the FILTERED turn would wrongly see the thought as the tail
  // and pulse it.
  const quiet = makeTranscriptDisplayConfig({
    kind: "custom",
    toolIntent: false,
    toolCalls: false,
    reasoning: false,
    expandByDefault: false,
    thinkingOpenByDefault: false,
  });
  const items = [
    item({ id: "think", type: "reasoning", status: "inProgress", reasoningSummaries: [["ongoing"]] }),
    item({ id: "cmd", type: "commandExecution", description: "Inspect the tree", status: "completed" }),
  ];
  render(withConfig(quiet, <TurnBlock turn={turn(items, { status: "inProgress" }, quiet)} />));

  const block = screen.getByTestId("think-block");
  expect(block.dataset.live).toBe("false");
  expect(block.querySelector("details")).not.toBeNull();
});

test("the trace journey: every thought stays readable on demand at tools, the switch opens them, chat stays conversation-only", () => {
  const tools = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const openThinking = makeTranscriptDisplayConfig({
    kind: "custom",
    toolIntent: true,
    toolCalls: true,
    reasoning: false,
    expandByDefault: false,
    thinkingOpenByDefault: true,
  });
  const chat = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });
  const items = () => [
    item({ id: "settled", type: "reasoning", status: "completed", reasoningSummaries: [["the settled trace body"]] }),
    item({ id: "failed", type: "reasoning", status: "failed", reasoningSummaries: [["the failed trace body"]] }),
  ];
  // Each phase renders in its own disclosure scope: the store keeps explicit
  // choices per scope, and a choice made in one phase must not answer for
  // another (the very precedence under test).
  const inScope = (scope: string, config: TranscriptDisplayConfigV1, node: ReactNode) => (
    <TranscriptRenderProvider config={config} surface="live" disclosureScope={`turnblock:${scope}`}>
      {node}
    </TranscriptRenderProvider>
  );

  // Tools: one collapsed trace row per thought, each unfolding to its own text.
  const { unmount: unmountTools } = render(
    inScope("journey-tools", tools, <TurnBlock turn={turn(items(), { status: "completed" }, tools)} />),
  );
  let details = screen.getAllByRole("group") as HTMLDetailsElement[];
  expect(details).toHaveLength(2);
  expect(details.every((row) => !row.open)).toBe(true);
  expect(screen.queryByText("the settled trace body")).toBeNull();
  expect(screen.queryByText("the failed trace body")).toBeNull();
  for (const row of details) fireEvent.click(row.querySelector("summary")!);
  expect(screen.getByText("the settled trace body")).toBeTruthy();
  expect(screen.getByText("the failed trace body")).toBeTruthy();
  unmountTools();

  // The switch: every thinking trace starts open, the failure row's neutral
  // label staying its summary; the reader can still fold it.
  const { unmount: unmountOpen } = render(
    inScope("journey-open", openThinking, <TurnBlock turn={turn(items(), { status: "completed" }, openThinking)} />),
  );
  details = screen.getAllByRole("group") as HTMLDetailsElement[];
  expect(details.filter((row) => row.open)).toHaveLength(2);
  expect(screen.getByText("the settled trace body")).toBeTruthy();
  expect(screen.getByText("the failed trace body")).toBeTruthy();
  const failedRow = details.find((row) =>
    row.querySelector('[data-testid="think-block-redacted"]'),
  ) as HTMLDetailsElement;
  expect(failedRow.querySelector('[data-testid="think-block-redacted"]')?.textContent).not.toBe("");
  fireEvent.click(failedRow.querySelector("summary")!);
  expect((failedRow as HTMLDetailsElement).open).toBe(false);
  unmountOpen();

  // Chat: conversation-only - the settled trace is gone, the broken turn's
  // explanation stays, and the live placeholder carries no text and no
  // disclosure to unfold.
  const { unmount: unmountChat } = render(
    inScope(
      "journey-chat",
      chat,
      <TurnBlock
        turn={turn(
          [
            ...items(),
            item({
              id: "live",
              type: "reasoning",
              status: "inProgress",
              reasoningSummaries: [["the live trace body"]],
            }),
          ],
          { status: "inProgress" },
          chat,
        )}
      />,
    ),
  );
  details = screen.getAllByRole("group") as HTMLDetailsElement[];
  expect(details).toHaveLength(1);
  expect(details[0]?.open).toBe(false);
  expect(screen.queryByText("the settled trace body")).toBeNull();
  expect(screen.queryByText("the live trace body")).toBeNull();
  fireEvent.click(details[0]!.querySelector("summary")!);
  expect(screen.getByText("the failed trace body")).toBeTruthy();
  const liveBlock = screen.getAllByTestId("think-block").find((block) => block.dataset.contentFree === "true");
  expect(liveBlock?.querySelector("details")).toBeNull();
  expect(liveBlock?.textContent).not.toContain("the live trace body");
  unmountChat();
});

test("the turn root remains a centered, shrinkable reading column", () => {
  const css = readModuleCss(import.meta.url, "turnblock.module.css");
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

test("passes closesExchange through ItemRenderProps", () => {
  const seen: Array<{ closesExchange?: boolean }> = [];
  const originalAgentMessageRenderer = itemRendererFor("agentMessage");
  try {
    registerItemRenderer("agentMessage", (props) => {
      seen.push({ closesExchange: props.closesExchange });
      return null;
    });
    const agentItem = { id: "a9", turnId: "t1", type: "agentMessage", text: "done", status: "completed" };
    render(<TurnBlock turn={turn([agentItem])} exchangeClosers={new Set(["a9"])} />);
    expect(seen).toEqual([{ closesExchange: true }]);
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

test("an intent-less tool shows its own derived summary at chat and tools levels", () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });
  const blankIntent = item({
    id: "critical-blank-intent",
    type: "commandExecution",
    toolName: "shell",
    argumentsJSON: JSON.stringify({ command: "echo derived-summary" }),
    description: "  ",
    status: "completed",
  });
  const { rerender } = render(withConfig(config, <TurnBlock turn={turn([blankIntent], {}, config)} />));

  // A closed intent group mounts its rows only once opened.
  fireEvent.click(screen.getByTestId("intent-group").querySelector("summary")!);
  expect(screen.getAllByTestId("tool-call-item")).toHaveLength(1);
  // Derived from the call's own arguments (the fixture's command).
  expect(screen.getByTestId("tool-row-summary").textContent).toContain("echo derived-summary");

  const tools = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  rerender(withConfig(tools, <TurnBlock turn={turn([blankIntent], {}, tools)} />));
  expect(screen.getAllByTestId("tool-call-item")).toHaveLength(1);
  expect(screen.getByTestId("tool-row-summary").textContent).toContain("echo derived-summary");
});

// Regression for session 034MY2rfMj2ho4Nj6J0jFB: an intent-less read_file row
// rendered "Action summary unavailable" instead of the row's own
// "Read <path> · lines N-M", because the projected neutral summary replaced the
// descriptor's own summary. Every level must show the derived summary.
test("an intent-less read_file shows the Read summary, never 'Action summary unavailable'", () => {
  const read = item({
    id: "read-no-intent",
    type: "commandExecution",
    toolName: "read_file",
    argumentsJSON: JSON.stringify({ file_path: "src/foo.ts" }),
    output: "line one\nline two\n",
    description: "  ",
    status: "completed",
  });
  for (const level of ["chat", "intent", "tools", "activity", "full"] as const) {
    const config = makeTranscriptDisplayConfig({ kind: "preset", level });
    const { unmount } = render(withConfig(config, <TurnBlock turn={turn([read], {}, config)} />));
    const group = screen.queryByTestId("intent-group") as HTMLDetailsElement | null;
    if (group && !group.open) fireEvent.click(group.querySelector("summary")!);
    // The descriptor's own summary names the fixture's path; the neutral
    // projected fallback would not.
    expect(screen.getByTestId("tool-row-summary").textContent).toContain("src/foo.ts");
    unmount();
  }
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
  // A closed group mounts none of its rows.
  expect(screen.queryByTestId("tool-call-item")).toBeNull();

  const summary = group.querySelector("summary");
  if (summary === null) throw new Error("Chat intent summary did not render");
  fireEvent.click(summary);
  expect(group.hasAttribute("open")).toBe(true);
  // The intent text renders head + glued final word (ToolRow's .intentTail);
  // assert it through the row rather than getByText, which only matches text
  // contained in one element.
  expect(screen.getByTestId("tool-row-intent").textContent).toBe("Run focused checks");
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
  expect(screen.getByTestId("tool-row-intent").textContent).toBe("Read the configuration");
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
  // Announced by name: the glyph's meaning must not live in its color alone.
  const glyph = screen.getByTestId("failure-glyph");
  expect(screen.getAllByRole("img")).toContain(glyph);
  expect(accessibleName(glyph).trim()).not.toBe("");
  // ToolCallItem renders eagerly inside the intent group. A failed row no
  // longer force-expands: it settles collapsed, and the body opens on the
  // reader's own toggle. "Action summary unavailable" is still never shown,
  // since the descriptor's own summary outranks the projected fallback.
  expect(screen.getByTestId("tool-call-item")).toBeTruthy();
  expect(screen.queryByTestId("tool-call-body")).toBeNull();
  expect(screen.queryByText("command failed")).toBeNull();
  expect(screen.queryByText(ACTION_SUMMARY_UNAVAILABLE)).toBeNull();
  // The row stays an expandable disclosure: opening it surfaces the body's
  // error output as the single representation of the command.
  fireEvent.click(screen.getByTestId("tool-row-trigger"));
  expect(screen.getByText("command failed")).toBeTruthy();
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
  // Intent text is visible (head + glued final word - see ToolRow's
  // .intentTail).
  expect(screen.getByTestId("tool-row-intent").textContent).toBe("Read the configuration");
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
    thinkingOpenByDefault: false,
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
    thinkingOpenByDefault: true,
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
  expect(screen.getByTestId("system-notice-scaffold-body").textContent).toContain("You are a helpful assistant.");
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
// agentMessage, and the user-sourced steer that renders the same view)
// render full-width outside any [data-testid="run-content"] wrapper; "run"
// rows - EVERYTHING else, including daemon steering, system notices,
// warnings, and unknown future types - render inside the wrapper and take
// the gutter indent. The steering/notification indent is the consistency fix
// (Jesse's review call); these tests pin it.
//
// The ONE exception inside steering is the human's own steer (source ===
// "user"): SteeringItem renders it through UserMessageView, whose avatar
// column already IS the gutter arithmetic, so it takes the speaker role
// instead. See the dedicated test below.

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

test("daemon-sourced steering, systemMessage, and warning are run rows: they render INSIDE a run-content wrapper (the consistency fix)", () => {
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

test("a user-typed steer is a speaker row: it renders OUTSIDE the wrapper, because its own avatar column IS the gutter", () => {
  // appwire projects a human's mid-turn steer as a STEERING entry with
  // steering_source=user, and SteeringItem renders that through the SAME
  // UserMessageView a prompt uses (its source === "user" branch). That view's
  // 24px tile + 10px gap IS the content column the run gutter reserves, so
  // wrapping it in .runContent composes the two indents (34 + 34) and seats
  // the steer one gutter right of every row around it. Speaker is the role
  // whose contract is exactly "renders its own avatar header, so it spans the
  // full width unwrapped" (layoutRoles.ts).
  const items = [item({ id: "st-user", type: "steering", text: "work in a new worktree.", source: "user" })];
  render(<TurnBlock turn={turn(items)} />);
  expectOutsideRunContent(screen.getByTestId("user-message-item"));
  expect(screen.queryByTestId("run-content")).toBeNull();
});

test("a human-note steer keeps its run row: the kind routes it to the divider, not the message view", () => {
  // A human note rides the user-sourced steer path but the KIND routes it to
  // SteeringDivider (SteeringItem's human-note branch) instead of
  // UserMessageView. A divider is a run row: its rail icon pulls into the
  // gutter with margin-left: -speaker-gutter, which only exists under
  // .runContent - unwrapping it would pull the icon out of the column.
  const items = [
    item({ id: "st-note", type: "steering", text: "note to self", source: "user", steeringKind: "human-note" }),
  ];
  render(<TurnBlock turn={turn(items)} />);
  expectInsideRunContent(screen.getByTestId("steering-item"));
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

// A run of adjacent tool rows is one card: each row's wrapper says whether it
// opens, continues or closes the card, and a row that is not a tool row ends
// the run, so the stylesheet can round only the card's outer corners.
function toolCardPositionsOf(container: HTMLElement): (string | null)[] {
  return Array.from(container.querySelectorAll('[data-testid="run-content"]')).map((wrapper) =>
    wrapper.getAttribute("data-tool-card"),
  );
}

test("adjacent tool rows share one card; a row between them splits it into two", () => {
  const items = [
    item({ id: "a", type: "commandExecution", toolName: "tb-card-a", output: "one" }),
    item({ id: "b", type: "commandExecution", toolName: "tb-card-b", output: "two" }),
    item({ id: "c", type: "commandExecution", toolName: "tb-card-c", output: "three" }),
    item({ id: "note", type: "someFutureWireType", text: "between" }),
    item({ id: "d", type: "commandExecution", toolName: "tb-card-d", output: "four" }),
  ];
  const { container } = render(<TurnBlock turn={turn(items)} />);
  expect(toolCardPositionsOf(container)).toEqual(["first", "middle", "last", null, "only"]);
});

test("a tool call whose row is suppressed neither joins a card nor splits the one around it", () => {
  const items = [
    item({ id: "a", type: "commandExecution", toolName: "tb-card-a", output: "one" }),
    item({
      id: "view",
      type: "commandExecution",
      toolName: "task_list",
      argumentsJSON: JSON.stringify({ action: "view" }),
    }),
    item({ id: "b", type: "commandExecution", toolName: "tb-card-b", output: "two" }),
  ];
  const { container } = render(<TurnBlock turn={turn(items)} />);
  expect(screen.getAllByTestId("tool-call-item")).toHaveLength(2);
  expect(toolCardPositionsOf(container)).toEqual(["first", null, "last"]);
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
  const css = readModuleCss(import.meta.url, "turnblock.module.css");
  expect(css).toMatch(
    /@media\s*\(min-width:\s*700px\)\s*\{\s*\.runContent\s*\{\s*padding-left:\s*var\(--speaker-gutter\);\s*\}\s*\}/,
  );
});

test("an intent group's rule spans the turn column while its members take the gutter indent", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "..", "session.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  expect(css).toMatch(
    /@media\s*\(min-width:\s*700px\)\s*\{\s*\.intentGroupItems\s*\{\s*padding-left:\s*var\(--speaker-gutter\);\s*\}\s*\}/,
  );
  expect(css).not.toMatch(/\.intentGroup\s*\{[^}]*padding/);
});

test("the speaker geometry is declared once on .turn: 34px gutter = 24px avatar + 10px gap", () => {
  const css = readModuleCss(import.meta.url, "turnblock.module.css");
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
  const css = readModuleCss(import.meta.url, "turnblock.module.css");
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

// A streaming reply is an overlay item ("stream:<round>/<attempt>:agentMessage")
// until its round is recorded, when it becomes a history item with a new id.
// Both carry the round's id, so the reply keys by its round and keeps one React
// key through the change: React reconciles the row instead of remounting it, so
// the markdown keeps its DOM and a text selection survives.
test("a streamed reply keeps its DOM node when history records its round", () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" });
  const itemStatus = (status: string) => item({ type: "agentMessage", text: "Looking", roundId: "r1", status });
  const live = turn(
    [
      {
        ...itemStatus("inProgress"),
        id: "stream:r1/0:agentMessage",
        // The overlay display item mints pendingText from the stream's text
        // (reducer's overlayDisplayItem); the live branch renders from it.
        pendingText: ["Looking"],
      },
    ],
    {},
    config,
  );
  const recorded = turn([{ ...itemStatus("completed"), id: "item_assistant_1_0" }], {}, config);

  const { container, rerender } = render(withConfig(config, <TurnBlock turn={live} />));
  const before = container.querySelector('[data-testid="agent-message-item"]');
  expect(before).not.toBeNull();

  rerender(withConfig(config, <TurnBlock turn={recorded} />));
  expect(container.querySelector('[data-testid="agent-message-item"]')).toBe(before);
});

const NO_ROUND_TIMINGS = makeTranscriptDisplayConfig(
  { kind: "preset", level: "activity" },
  { roundTimings: false, systemEvents: true, promptEvents: true },
);

test("the completion cap waits for the turn's last fragment", () => {
  const projected = turn(
    [item({ type: "userMessage", text: "go" }), item({ id: "answer", type: "agentMessage", text: "done" })],
    { status: "completed", durationMs: 134_000 },
    NO_ROUND_TIMINGS,
  );
  const { rerender } = render(withConfig(NO_ROUND_TIMINGS, <TurnBlock turn={projected} showTurnSeparator={false} />));
  expect(screen.queryByTestId("turn-done")).toBeNull();
  rerender(withConfig(NO_ROUND_TIMINGS, <TurnBlock turn={projected} />));
  expect(screen.getByTestId("turn-done")).toBeTruthy();
});

test("a completed intent group closes after its trailing tool calls", () => {
  const source: TurnModel = {
    id: "turn_1",
    status: "completed",
    durationMs: 134_000,
    items: [item({ id: "tool", type: "commandExecution", toolName: "shell" })],
  };
  const entry = {
    kind: "intent" as const,
    id: "intent:tool" as const,
    turnId: "turn_1",
    sourceIndex: 0,
    sourceItemId: "tool",
    rationale: "",
    failed: false,
    item: source.items[0]!,
  };
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" }, { roundTimings: true });
  render(
    withConfig(config, <ProjectedIntentGroup rowId="intent-group-row" entries={[entry]} separatorTurn={source} />),
  );
  expect(screen.getByTestId("turn-done")).toBeTruthy();
  const group = document.querySelector('[data-transcript-row-id="intent-group-row"]');
  expect(group?.contains(screen.getByTestId("turn-separator"))).toBe(true);
});

// Issue #5836: an answered ask round's reply text must render in the thread.
// The wire serves the reply as a userMessage whose observation carries the
// answer link exactly this way (internal/apptranscript's answerObservation);
// this drives the real wire fold (hydrateThread), projection and row
// rendering together. The reply bubble itself already renders on main - the
// issue's reported-blank symptom traces to a stale build - so the
// discriminating pins here are the durable link surviving the fold (red on
// pre-fix main) and the ask row's "— answered:" recap rendering FROM that
// link: the recap is the visible behavior this change adds on the wire path
// a served transcript takes.
test("an answered round renders its reply text in the thread", () => {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });
  const thread = {
    id: "thread_probe",
    sessionId: "sess_probe",
    source: "local",
    status: { type: "idle" },
    cwd: "/w",
    turns: [
      {
        id: "turn_q",
        itemsView: "full",
        status: "completed",
        items: [
          {
            id: "item_user_1",
            type: "userMessage",
            turnId: "turn_q",
            text: "please unblock the Jobs rendering work",
            status: "completed",
          },
          {
            id: "item_ask",
            type: "commandExecution",
            turnId: "turn_q",
            toolName: "ask_user",
            callId: "call_ask",
            description: "Ask",
            argumentsJson:
              '{"questions":[{"header":"Jobs rendering blocker","question":"What should I do about the Jobs rendering blocker?","options":[{"label":"Authorize a bounded Jobs investigation","detail":"go"}]}]}',
            output: "questions posted",
            status: "completed",
          },
        ],
      },
      {
        id: "turn_a",
        itemsView: "full",
        status: "completed",
        items: [
          {
            id: "item_reply",
            type: "userMessage",
            turnId: "turn_a",
            text: '[answers]\n1. [Jobs rendering blocker] → "Authorize a bounded Jobs investigation"',
            status: "completed",
            startedAt: 1_700_000_000_000,
            observation: { ref: "", turnId: "turn_a", answerToTurnIds: ["turn_q"], causalKnown: false },
          },
        ],
      },
    ],
    evener: { ref: "local:sess_probe", queue: { revision: 0 } },
  } as unknown as Parameters<typeof hydrateThread>[0]["thread"];
  const model = hydrateThread({ thread }, "local:sess_probe", 0);
  const projection = projectThread(model as ThreadModel, config);
  render(
    withConfig(
      config,
      projection.turns.map((projected) => (
        <TurnBlock key={projected.id} turn={projected} thread={model as ThreadModel} />
      )),
    ),
  );
  const bubbles = screen.getAllByTestId("user-bubble");
  expect(bubbles.some((bubble) => bubble.textContent?.includes("Authorize a bounded Jobs investigation"))).toBe(true);
  // The durable link survived the fold: the reply model item names the
  // question round it answers.
  expect(
    model.turns.flatMap((turn) => turn.items).find((candidate) => candidate.id === "item_reply")?.answerToTurnIds,
  ).toEqual(["turn_q"]);
  // And the ask row renders its recap FROM the link - the visible behavior
  // this change adds on the served-transcript path. At chat level the summary
  // line sits behind the row's intent trigger, so open it and read the
  // row's own summary element.
  fireEvent.click(screen.getByTestId("tool-row-trigger"));
  expect(screen.getByTestId("tool-row-summary").textContent).toContain(
    '— answered: "Authorize a bounded Jobs investigation"',
  );
});
