import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createRef, useState } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import { threadsStore } from "../../../stores/threads";
import { makeTranscriptDisplayConfig } from "../../../transcriptDisplay/config";
import type { VirtualListHandle } from "../../../widgets";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import {
  captureTranscriptViews,
  resetTranscriptViewRegistryForTests,
  transitionTranscriptViews,
} from "./flow/transcriptViewRegistry";
import { TranscriptBody } from "./TranscriptBody";
import { threadFingerprintForItem } from "./types";

function preset(level: "chat" | "intent" | "tools" | "activity" | "full") {
  return makeTranscriptDisplayConfig({ kind: "preset", level });
}

const fixture = {
  ref: "preview:test",
  threadId: "thread_preview",
  name: "Preview thread",
  status: { type: "idle" },
  modelProvider: "anthropic",
  model: "claude",
  askPending: false,
  pendingEscalations: [],
  turns: [
    {
      id: "turn_1",
      status: "completed",
      items: [
        {
          id: "user_1",
          turnId: "turn_1",
          type: "userMessage",
          text: "Please inspect the project",
          status: "completed",
        },
        {
          id: "tool_1",
          turnId: "turn_1",
          type: "commandExecution",
          text: "",
          toolName: "read_file",
          description: "Inspect the tree",
          output: "tree output",
          status: "completed",
        },
        { id: "agent_1", turnId: "turn_1", type: "agentMessage", text: "The tree is ready", status: "completed" },
      ],
    },
  ],
} as unknown as ThreadModel;

const ordinaryToolFixture = {
  ...fixture,
  cwd: "/workspace",
  turns: [
    {
      id: "ordinary_turn",
      status: "completed",
      items: [
        { id: "ordinary_user", turnId: "ordinary_turn", type: "userMessage", text: "run tools", status: "completed" },
        {
          id: "ordinary_shell",
          turnId: "ordinary_turn",
          type: "commandExecution",
          toolName: "shell",
          description: "Run tests",
          argumentsJSON: '{"command":"cd /workspace && make test"}',
          status: "completed",
        },
        { id: "ordinary_agent", turnId: "ordinary_turn", type: "agentMessage", text: "done", status: "completed" },
        {
          id: "ordinary_read",
          turnId: "ordinary_turn",
          type: "commandExecution",
          toolName: "read_file",
          description: "Read README",
          argumentsJSON: '{"file_path":"/workspace/README.md"}',
          status: "completed",
        },
        { id: "ordinary_agent_2", turnId: "ordinary_turn", type: "agentMessage", text: "read", status: "completed" },
      ],
    },
  ],
} as unknown as ThreadModel;

const crossTurnFixture = {
  ...fixture,
  turns: [
    {
      id: "turn_a",
      status: "completed",
      items: [
        {
          id: "tool_a",
          turnId: "turn_a",
          type: "commandExecution",
          text: "",
          description: "One",
          toolName: "read_file",
          status: "completed",
        },
      ],
    },
    {
      id: "turn_b",
      status: "completed",
      items: [
        {
          id: "tool_b",
          turnId: "turn_b",
          type: "commandExecution",
          text: "",
          description: "Two",
          toolName: "grep_files",
          status: "completed",
        },
      ],
    },
    {
      id: "turn_c",
      status: "completed",
      items: [
        {
          id: "tool_c",
          turnId: "turn_c",
          type: "commandExecution",
          text: "",
          description: "Three",
          toolName: "read_file",
          status: "completed",
        },
        { id: "agent_c", turnId: "turn_c", type: "agentMessage", text: "done", status: "completed" },
      ],
    },
  ],
} as unknown as ThreadModel;

const boundaryFixture = {
  ...fixture,
  turns: [
    {
      id: "turn_before",
      status: "completed",
      items: [
        {
          id: "tool_before",
          turnId: "turn_before",
          type: "commandExecution",
          text: "",
          description: "Solo",
          toolName: "read_file",
          status: "completed",
        },
      ],
    },
    {
      id: "turn_message",
      status: "completed",
      items: [
        { id: "message", turnId: "turn_message", type: "agentMessage", text: "between", status: "completed" },
        {
          id: "tool_after_message",
          turnId: "turn_message",
          type: "commandExecution",
          text: "",
          description: "After message",
          toolName: "read_file",
          status: "completed",
        },
      ],
    },
    {
      id: "turn_critical",
      status: "completed",
      items: [
        {
          id: "critical_tool",
          turnId: "turn_critical",
          type: "warning",
          text: "critical boundary",
          status: "completed",
        },
      ],
    },
    {
      id: "turn_after",
      status: "completed",
      items: [
        {
          id: "tool_after",
          turnId: "turn_after",
          type: "commandExecution",
          text: "",
          description: "After critical",
          toolName: "read_file",
          status: "completed",
        },
      ],
    },
  ],
} as unknown as ThreadModel;

const mixedBoundaryFixture = {
  ...fixture,
  turns: [
    {
      id: "turn_mixed_a",
      status: "completed",
      items: [
        {
          id: "local_intent",
          turnId: "turn_mixed_a",
          type: "commandExecution",
          text: "",
          description: "Local intent",
          toolName: "read_file",
          status: "completed",
        },
        {
          id: "local_critical",
          turnId: "turn_mixed_a",
          type: "warning",
          text: "critical boundary",
          status: "completed",
        },
        {
          id: "cross_a",
          turnId: "turn_mixed_a",
          type: "commandExecution",
          text: "",
          description: "Cross A",
          toolName: "read_file",
          status: "completed",
        },
      ],
    },
    {
      id: "turn_mixed_b",
      status: "completed",
      items: [
        {
          id: "cross_b",
          turnId: "turn_mixed_b",
          type: "commandExecution",
          text: "",
          description: "Cross B",
          toolName: "grep_files",
          status: "completed",
        },
      ],
    },
  ],
} as unknown as ThreadModel;

afterEach(() => {
  cleanup();
  resetTranscriptViewRegistryForTests();
  resetDisclosureStoreForTests();
  vi.restoreAllMocks();
});

let offsetHeightDescriptor: PropertyDescriptor | undefined;

beforeEach(() => {
  offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: 500 });
});

afterEach(() => {
  if (offsetHeightDescriptor) Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
});

describe("TranscriptBody", () => {
  test("renders Intent through one body without raw tool rows", () => {
    render(
      <TranscriptBody model={fixture} config={preset("intent")} surface="preview" disclosureScope="preview:test" />,
    );

    expect(screen.getByText("Inspect the tree")).toBeTruthy();
    expect(screen.getByText("The tree is ready")).toBeTruthy();
    // ToolCallItem renders eagerly inside the intent group (jsdom does not hide
    // <details> children). The body (raw tool output) is still collapsed.
    expect(screen.getByTestId("tool-call-item")).toBeTruthy();
    expect(screen.queryByText("tree output")).toBeNull();
  });

  test.each(["live", "readOnly"] as const)("uses the projected VirtualList for %s", (surface) => {
    render(
      <TranscriptBody model={fixture} config={preset("tools")} surface={surface} disclosureScope={`${surface}:test`} />,
    );

    expect(screen.getByTestId("transcript-virtual-list")).toBeTruthy();
    expect(screen.getByText("Inspect the tree")).toBeTruthy();
    expect(screen.getByTestId("tool-call-item")).toBeTruthy();
    expect(
      document.querySelector('[data-view-anchor-id="tool_1"]')?.getAttribute("data-view-anchor-source-index"),
    ).toBe("1");
  });

  test("registers live/read-only bodies but excludes preview", () => {
    const live = render(
      <TranscriptBody model={fixture} config={preset("tools")} surface="live" disclosureScope="live:registered" />,
    );
    expect(captureTranscriptViews().size).toBe(1);
    live.unmount();

    const readOnly = render(
      <TranscriptBody
        model={fixture}
        config={preset("tools")}
        surface="readOnly"
        disclosureScope="readOnly:registered"
      />,
    );
    expect(captureTranscriptViews().size).toBe(1);
    readOnly.unmount();

    render(<TranscriptBody model={fixture} config={preset("tools")} surface="preview" disclosureScope="preview" />);
    expect(captureTranscriptViews().size).toBe(0);
  });

  test("moves focus from a Tools row to its Chat intent proxy", async () => {
    const announce = vi.fn();
    const listRef = createRef<VirtualListHandle>();
    let showChat: () => void = () => {
      throw new Error("focus harness did not mount");
    };
    function FocusHarness() {
      const [config, setConfig] = useState(preset("tools"));
      showChat = () => setConfig(preset("chat"));
      return (
        <TranscriptBody
          model={fixture}
          config={config}
          surface="live"
          disclosureScope="live:focus-fallback"
          viewId="focus-fallback"
          listRef={listRef}
          onAnnounceViewChange={announce}
        />
      );
    }
    render(<FocusHarness />);
    const tool = await screen.findByTestId("tool-row-trigger");
    tool.focus();
    expect(document.activeElement).toBe(tool);
    expect(document.querySelector('[data-view-anchor-id="tool_1"]')?.contains(tool)).toBe(true);
    const capturedViews = captureTranscriptViews();
    expect([...capturedViews.keys()]).toEqual(["focus-fallback"]);
    expect(capturedViews.get("focus-fallback")?.focusedEntryId).toBe("tool_1");

    act(() => {
      transitionTranscriptViews(showChat, "Chat", {
        fingerprint: "chat",
      });
    });

    const group = await screen.findByTestId("intent-group");
    const summary = group.querySelector(":scope > summary");
    if (!(summary instanceof HTMLElement)) throw new Error("Chat intent group summary did not render");
    await waitFor(() => expect(document.activeElement).toBe(summary));
    expect(group.hasAttribute("open")).toBe(false);
    expect(announce).toHaveBeenCalledWith("Chat");
  });

  test("focuses the Transcript region when a view change removes the focused row", async () => {
    const announce = vi.fn();
    const listRef = createRef<VirtualListHandle>();
    const hiddenTools = makeTranscriptDisplayConfig({
      kind: "custom",
      toolIntent: false,
      toolCalls: false,
      reasoning: false,
      expandByDefault: false,
    });
    let hideTools: () => void = () => {
      throw new Error("focus fallback harness did not mount");
    };
    function FocusFallbackHarness() {
      const [config, setConfig] = useState(preset("tools"));
      hideTools = () => setConfig(hiddenTools);
      return (
        <TranscriptBody
          model={fixture}
          config={config}
          surface="live"
          disclosureScope="live:focus-region-fallback"
          viewId="focus-region-fallback"
          listRef={listRef}
          onAnnounceViewChange={announce}
        />
      );
    }
    render(<FocusFallbackHarness />);
    const tool = await screen.findByTestId("tool-row-trigger");
    tool.focus();
    expect(document.activeElement).toBe(tool);
    expect(captureTranscriptViews().get("focus-region-fallback")?.focusedEntryId).toBe("tool_1");

    act(() => {
      transitionTranscriptViews(hideTools, "Custom content", {
        fingerprint: "custom-without-tools",
      });
    });

    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("region", { name: "Transcript" })));
    expect(document.body.contains(tool)).toBe(false);
    expect(screen.queryByTestId("tool-call-item")).toBeNull();
    expect(screen.queryByTestId("intent-group")).toBeNull();
    expect(announce).toHaveBeenCalledWith("Custom content");
  });

  test("uses normal page flow for preview without an inner virtual scroller", () => {
    render(
      <TranscriptBody model={fixture} config={preset("tools")} surface="preview" disclosureScope="preview:test" />,
    );

    expect(screen.queryByTestId("transcript-virtual-list")).toBeNull();
    expect(screen.getByText("Inspect the tree")).toBeTruthy();
  });

  test.each(["live", "readOnly"] as const)(
    "passes initial snapshot inputs to ordinary %s tool rows",
    async (surface) => {
      const getState = vi.spyOn(threadsStore, "getState");
      render(
        <TranscriptBody
          model={ordinaryToolFixture}
          config={preset("tools")}
          surface={surface}
          disclosureScope={`${surface}:ordinary-initial`}
          sessionRef="ordinary:initial"
        />,
      );
      let shellRow: HTMLElement | undefined;
      await waitFor(() => {
        shellRow = screen.getAllByTestId("tool-call-item").find((row) => row.textContent?.includes("make test"));
        expect(shellRow).toBeDefined();
      });
      expect(shellRow?.textContent).not.toContain("cd /workspace && make test");
      expect(screen.getByRole("button", { name: "Open beside: README.md" })).toBeTruthy();
      expect(getState).not.toHaveBeenCalled();
    },
  );

  test("refreshes ordinary ask_user suffix, delegate terminal outcome, and supersession", async () => {
    const askItem = {
      id: "ordinary_ask",
      turnId: "ask_turn",
      type: "commandExecution",
      toolName: "ask_user",
      description: "Ask about mode",
      argumentsJSON: JSON.stringify({
        questions: [{ header: "Mode", question: "Choose", options: [{ label: "Fast", detail: "" }] }],
      }),
      status: "completed",
    };
    const askBefore = {
      ...ordinaryToolFixture,
      turns: [{ id: "ask_turn", status: "completed", items: [askItem] }],
    } as unknown as ThreadModel;
    const askAfter = {
      ...askBefore,
      turns: [
        {
          id: "ask_turn",
          status: "completed",
          items: [
            askItem,
            {
              id: "answer",
              turnId: "ask_turn",
              type: "userMessage",
              text: "[answers]\n1. [Mode] → Fast",
              status: "completed",
            },
          ],
        },
      ],
    } as unknown as ThreadModel;
    const { rerender } = render(
      <TranscriptBody model={askBefore} config={preset("tools")} surface="preview" disclosureScope="ordinary:ask" />,
    );
    expect(screen.getByTestId("tool-row-summary").textContent).toContain("Asked: [Mode]");
    const askDetails = screen.getByTestId("tool-call-item");
    const askRow = screen.getByTestId("tool-row");
    const askTrigger = screen.getByTestId("tool-row-trigger");
    askTrigger.focus();
    expect(document.activeElement).toBe(askTrigger);
    rerender(
      <TranscriptBody model={askAfter} config={preset("tools")} surface="preview" disclosureScope="ordinary:ask" />,
    );
    expect(screen.getByTestId("tool-row-summary").textContent).toContain("answered: Fast");
    expect(screen.getByTestId("tool-call-item")).toBe(askDetails);
    expect(screen.getByTestId("tool-row")).toBe(askRow);
    expect(screen.getByTestId("tool-row-trigger")).toBe(askTrigger);
    expect(document.activeElement).toBe(askTrigger);

    const delegateItem = {
      id: "ordinary_delegate",
      turnId: "delegate_turn",
      type: "commandExecution",
      text: "",
      toolName: "delegate",
      description: "Inspect a child session",
      argumentsJSON: '{"task":"inspect"}',
      output: JSON.stringify({ delegate_id: "dlg_ordinary", status: "running", transcript_ref: "local:child" }),
      status: "completed",
    };
    const delegateBefore = {
      ...ordinaryToolFixture,
      delegates: [{ delegateId: "dlg_ordinary", status: "running", terminal: false }],
      turns: [{ id: "delegate_turn", status: "completed", items: [delegateItem] }],
    } as unknown as ThreadModel;
    const delegateAfter = {
      ...delegateBefore,
      delegates: [{ delegateId: "dlg_ordinary", status: "done", outcome: "done", terminal: true }],
    } as unknown as ThreadModel;
    rerender(
      <TranscriptBody
        model={delegateBefore}
        config={preset("tools")}
        surface="preview"
        disclosureScope="ordinary:delegate"
        sessionRef="ordinary:delegate"
      />,
    );
    expect(screen.getByRole("img", { name: "Working" })).toBeTruthy();
    rerender(
      <TranscriptBody
        model={delegateAfter}
        config={preset("tools")}
        surface="preview"
        disclosureScope="ordinary:delegate"
        sessionRef="ordinary:delegate"
      />,
    );
    expect(threadFingerprintForItem(delegateItem, delegateBefore)).not.toBe(
      threadFingerprintForItem(delegateItem, delegateAfter),
    );
    await waitFor(() => expect(screen.getByRole("img", { name: "Ended" })).toBeTruthy());

    const failed = {
      id: "ordinary_failed",
      turnId: "supersede_turn",
      type: "commandExecution",
      toolName: "shell",
      description: "Retry shell",
      error: "bad",
      prevalOnly: true,
      status: "failed",
    };
    const corrected = {
      id: "ordinary_corrected",
      turnId: "supersede_turn",
      type: "commandExecution",
      toolName: "shell",
      description: "Retry shell",
      status: "completed",
    };
    const supersedeBefore = {
      ...ordinaryToolFixture,
      turns: [{ id: "supersede_turn", status: "completed", items: [failed] }],
    } as unknown as ThreadModel;
    const supersedeAfter = {
      ...supersedeBefore,
      turns: [{ id: "supersede_turn", status: "completed", items: [failed, corrected] }],
    } as unknown as ThreadModel;
    const firstToolExpanded = () =>
      screen
        .getAllByTestId("tool-call-item")[0]
        ?.querySelector('[data-testid="tool-row-body-trigger"]')
        ?.getAttribute("aria-expanded");
    rerender(
      <TranscriptBody
        model={supersedeBefore}
        config={preset("tools")}
        surface="preview"
        disclosureScope="ordinary:supersede"
      />,
    );
    expect(firstToolExpanded()).toBe("true");
    rerender(
      <TranscriptBody
        model={supersedeAfter}
        config={preset("tools")}
        surface="preview"
        disclosureScope="ordinary:supersede"
      />,
    );
    expect(firstToolExpanded()).toBe("false");
  });

  test("Tools/Full previews mount item renderers without threadsStore or RPC access", () => {
    const getState = vi.spyOn(threadsStore, "getState");
    const subscribe = vi.spyOn(threadsStore, "subscribe");
    const getInitialState = vi.spyOn(threadsStore, "getInitialState");
    const fake = new FakeClient("ready");
    const request = vi.spyOn(fake, "request");
    render(
      <>
        <TranscriptBody model={fixture} config={preset("tools")} surface="preview" disclosureScope="preview:one" />
        <TranscriptBody model={fixture} config={preset("full")} surface="preview" disclosureScope="preview:two" />
      </>,
    );
    expect(screen.getAllByTestId("tool-call-item").length).toBeGreaterThanOrEqual(2);
    expect(getState).not.toHaveBeenCalled();
    expect(subscribe).not.toHaveBeenCalled();
    expect(getInitialState).not.toHaveBeenCalled();
    expect(request).not.toHaveBeenCalled();
  });

  test("intent-group defaults are not user choices, while summary activation persists by stable scope", () => {
    const intentOpen = makeTranscriptDisplayConfig({
      kind: "custom",
      toolIntent: true,
      toolCalls: false,
      reasoning: false,
      expandByDefault: true,
    });
    const intentClosed = makeTranscriptDisplayConfig({
      kind: "custom",
      toolIntent: true,
      toolCalls: false,
      reasoning: false,
      expandByDefault: false,
    });
    const { rerender } = render(
      <TranscriptBody model={fixture} config={intentOpen} surface="preview" disclosureScope="preview:single" />,
    );
    const single = screen.getAllByTestId("intent-group")[0];
    if (single === undefined) throw new Error("single-turn intent group did not render");
    expect(single).toBeTruthy();
    expect(single.hasAttribute("open")).toBe(true);

    rerender(
      <TranscriptBody model={fixture} config={intentClosed} surface="preview" disclosureScope="preview:single" />,
    );
    expect(single.hasAttribute("open")).toBe(false);
    const singleSummary = single.querySelector("summary");
    if (singleSummary === null) throw new Error("single-turn intent summary did not render");
    fireEvent.click(singleSummary);
    expect(single.hasAttribute("open")).toBe(true);

    rerender(
      <TranscriptBody model={fixture} config={intentClosed} surface="preview" disclosureScope="preview:single" />,
    );
    expect(single.hasAttribute("open")).toBe(true);

    render(
      <TranscriptBody
        model={crossTurnFixture}
        config={preset("intent")}
        surface="preview"
        disclosureScope="preview:cross"
      />,
    );
    const crossGroup = screen.getAllByTestId("intent-group").at(-1);
    if (crossGroup === undefined) throw new Error("cross-turn intent group did not render");
    expect(crossGroup?.getAttribute("data-transcript-row-id")).toBe("intent-group:intent:tool_a");
    expect(crossGroup?.hasAttribute("open")).toBe(true);
    const crossSummary = crossGroup.querySelector("summary");
    if (crossSummary === null) throw new Error("cross-turn intent summary did not render");
    fireEvent.click(crossSummary);
    expect(crossGroup?.hasAttribute("open")).toBe(false);
    expect(single.hasAttribute("open")).toBe(true);
  });

  test("coalesces intent-only actions across adjacent turns into one stable virtual row", () => {
    const { rerender } = render(
      <TranscriptBody
        model={crossTurnFixture}
        config={preset("intent")}
        surface="live"
        disclosureScope="live:cross-turn"
      />,
    );

    const group = screen.getAllByTestId("intent-group");
    expect(group).toHaveLength(1);
    expect(group[0]?.textContent).toContain("3 actions");
    expect(group[0]?.textContent).toContain("One");
    expect(group[0]?.textContent).toContain("Two");
    expect(group[0]?.textContent).toContain("Three");
    // ToolCallItem renders eagerly inside the intent group (jsdom does not hide
    // <details> children), so 3 tool-call-items are present for 3 coalesced actions.
    expect(screen.getAllByTestId("tool-call-item")).toHaveLength(3);
    expect(screen.getAllByTestId("transcript-row")).toHaveLength(2);
    expect(screen.getAllByTestId("transcript-row")[0]?.getAttribute("data-row-id")).toBe("intent-group:intent:tool_a");
    expect(
      document.querySelector('[data-view-anchor-id="intent:tool_b"]')?.getAttribute("data-view-anchor-turn-id"),
    ).toBe("turn_b");

    const rowIds = screen.getAllByTestId("transcript-row").map((row) => row.getAttribute("data-row-id"));
    rerender(
      <TranscriptBody
        model={crossTurnFixture}
        config={preset("intent")}
        surface="live"
        disclosureScope="live:cross-turn"
      />,
    );
    expect(screen.getAllByTestId("transcript-row").map((row) => row.getAttribute("data-row-id"))).toEqual(rowIds);
  });

  test("a growing cross-turn group keeps its first-action row identity and manually closed state (catches last-id row key)", () => {
    const config = makeTranscriptDisplayConfig({
      kind: "custom",
      toolIntent: true,
      toolCalls: false,
      reasoning: false,
      expandByDefault: true,
    });
    const initial = { ...crossTurnFixture, turns: crossTurnFixture.turns.slice(0, 2) } as ThreadModel;
    const finalTurn = crossTurnFixture.turns[2];
    if (finalTurn === undefined || finalTurn.items[0] === undefined)
      throw new Error("cross-turn stream fixture is incomplete");
    const streamed = {
      ...crossTurnFixture,
      turns: [...initial.turns, { ...finalTurn, items: [finalTurn.items[0]] }],
    } as ThreadModel;
    const { rerender } = render(
      <TranscriptBody model={initial} config={config} surface="preview" disclosureScope="preview:cross-stream" />,
    );
    const row = screen.getByTestId("transcript-row");
    const rowId = row.getAttribute("data-row-id");
    const group = screen.getByTestId("intent-group");
    const summary = group.querySelector("summary");
    if (summary === null) throw new Error("cross-turn streaming summary did not render");
    expect(group.hasAttribute("open")).toBe(true);
    fireEvent.click(summary);
    expect(group.hasAttribute("open")).toBe(false);

    rerender(
      <TranscriptBody model={streamed} config={config} surface="preview" disclosureScope="preview:cross-stream" />,
    );

    expect(screen.getByTestId("transcript-row")).toBe(row);
    expect(row.getAttribute("data-row-id")).toBe(rowId);
    expect(screen.getByTestId("intent-group")).toBe(group);
    expect(group.textContent).toContain("3 actions");
    expect(group.hasAttribute("open")).toBe(false);
  });

  test("a terminal one-turn intent row extends across a streamed second turn without remounting", () => {
    const initialTurn = crossTurnFixture.turns[0];
    const streamedTurn = crossTurnFixture.turns[1];
    if (initialTurn === undefined || streamedTurn === undefined) throw new Error("streaming fixture is incomplete");
    const initial = { ...crossTurnFixture, turns: [{ ...initialTurn, durationMs: 1500 }] } as ThreadModel;
    const streamed = {
      ...crossTurnFixture,
      turns: [
        { ...initialTurn, durationMs: 1500 },
        { ...streamedTurn, durationMs: 2500 },
      ],
    } as ThreadModel;
    const config = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }, { roundTimings: true });
    const { rerender } = render(
      <TranscriptBody
        model={initial}
        config={config}
        surface="live"
        disclosureScope="live:one-to-two-turns"
        showSeenDividerTurnId="turn_a"
      />,
    );
    const row = screen.getByTestId("transcript-row");
    const group = screen.getByTestId("intent-group");
    const divider = screen.getByTestId("seen-divider");
    const separator = screen.getByTestId("turn-separator");
    const firstAnchor = document.querySelector('[data-view-anchor-id="intent:tool_a"]');
    if (!(firstAnchor instanceof HTMLElement)) throw new Error("first streaming intent anchor did not render");
    const summary = group.querySelector(":scope > summary");
    if (!(summary instanceof HTMLElement)) throw new Error("one-turn intent summary did not render");
    expect(row.getAttribute("data-row-id")).toBe("intent-group:intent:tool_a");
    expect(divider.compareDocumentPosition(group) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(group.compareDocumentPosition(separator) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(separator.textContent).toContain("1.5s");
    expect(firstAnchor.getAttribute("data-view-anchor-source-index")).toBe("0");
    expect(group.hasAttribute("open")).toBe(false);
    fireEvent.click(summary);
    expect(group.hasAttribute("open")).toBe(true);

    rerender(
      <TranscriptBody
        model={streamed}
        config={config}
        surface="live"
        disclosureScope="live:one-to-two-turns"
        showSeenDividerTurnId="turn_a"
      />,
    );

    expect(screen.getByTestId("transcript-row")).toBe(row);
    expect(row.getAttribute("data-row-id")).toBe("intent-group:intent:tool_a");
    expect(screen.getByTestId("intent-group")).toBe(group);
    expect(group.textContent).toContain("2 actions");
    expect(group.hasAttribute("open")).toBe(true);
    expect(group.getAttribute("data-transcript-source-turn-ids")).toBe("turn_a,turn_b");
    expect(screen.getByTestId("seen-divider")).toBe(divider);
    expect(divider.compareDocumentPosition(group) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(screen.getByTestId("turn-separator")).toBe(separator);
    expect(group.compareDocumentPosition(separator) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(separator.textContent).toContain("2.5s");
    expect(document.querySelector('[data-view-anchor-id="intent:tool_a"]')).toBe(firstAnchor);
    expect(firstAnchor.getAttribute("data-view-anchor-source-index")).toBe("0");
    expect(
      document.querySelector('[data-view-anchor-id="intent:tool_b"]')?.getAttribute("data-view-anchor-source-index"),
    ).toBe("1");
  });

  test("an actions-only failed turn renders one end cap after its terminal intent group", () => {
    const failed = {
      ...fixture,
      turns: [
        {
          id: "failed_actions_only",
          status: "failed",
          error: { message: "Actions-only turn failed" },
          items: [
            {
              id: "failed_action",
              turnId: "failed_actions_only",
              type: "commandExecution",
              toolName: "shell",
              description: "Run the failing action",
              status: "completed",
            },
          ],
        },
      ],
    } as unknown as ThreadModel;
    render(
      <TranscriptBody model={failed} config={preset("chat")} surface="preview" disclosureScope="failure:actions" />,
    );

    const group = screen.getByTestId("intent-group");
    const failures = screen.getAllByTestId("turn-failure");
    expect(failures).toHaveLength(1);
    const failure = failures[0];
    if (failure === undefined) throw new Error("actions-only failure end cap did not render");
    expect(group.compareDocumentPosition(failure) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  });

  test("a failed turn with earlier visible content renders one end cap after its terminal intent group", () => {
    const failed = {
      ...fixture,
      turns: [
        {
          id: "failed_with_message",
          status: "failed",
          error: { message: "Message turn failed" },
          items: [
            {
              id: "failed_message",
              turnId: "failed_with_message",
              type: "agentMessage",
              text: "Visible before the action",
              status: "completed",
            },
            {
              id: "failed_after_message",
              turnId: "failed_with_message",
              type: "commandExecution",
              toolName: "shell",
              description: "Run after the message",
              status: "completed",
            },
          ],
        },
      ],
    } as unknown as ThreadModel;
    render(
      <TranscriptBody model={failed} config={preset("chat")} surface="preview" disclosureScope="failure:message" />,
    );

    const message = screen.getByText("Visible before the action");
    const group = screen.getByTestId("intent-group");
    const failures = screen.getAllByTestId("turn-failure");
    expect(failures).toHaveLength(1);
    const failure = failures[0];
    if (failure === undefined) throw new Error("message failure end cap did not render");
    expect(message.compareDocumentPosition(group) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(group.compareDocumentPosition(failure) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  });

  test("a renderable failed-turn end cap is a grouping boundary between adjacent intent runs", () => {
    const action = (id: string, turnId: string, description: string) => ({
      id,
      turnId,
      type: "commandExecution",
      toolName: "shell",
      description,
      status: "completed",
    });
    const failedBoundary = {
      ...fixture,
      turns: [
        { id: "clean_before", status: "completed", items: [action("clean_action", "clean_before", "Before")] },
        {
          id: "failed_boundary",
          status: "failed",
          error: { message: "Boundary turn failed" },
          items: [action("boundary_action", "failed_boundary", "Boundary")],
        },
        { id: "clean_after", status: "completed", items: [action("after_action", "clean_after", "After")] },
      ],
    } as unknown as ThreadModel;
    render(
      <TranscriptBody
        model={failedBoundary}
        config={preset("chat")}
        surface="preview"
        disclosureScope="failure:boundary"
      />,
    );

    const groups = screen.getAllByTestId("intent-group");
    expect(groups).toHaveLength(3);
    expect(groups.map((group) => group.textContent)).toEqual(["1 actionBefore", "1 actionBoundary", "1 actionAfter"]);
    const boundaryGroup = groups[1];
    const afterGroup = groups[2];
    if (boundaryGroup === undefined || afterGroup === undefined)
      throw new Error("failure boundary groups did not render");
    const failure = screen.getByTestId("turn-failure");
    expect(boundaryGroup.compareDocumentPosition(failure) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(failure.compareDocumentPosition(afterGroup) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  });

  test("does not merge intents across visible message or critical boundaries", () => {
    render(
      <TranscriptBody
        model={boundaryFixture}
        config={preset("intent")}
        surface="preview"
        disclosureScope="preview:boundaries"
      />,
    );

    const groups = screen.getAllByTestId("intent-group");
    expect(groups).toHaveLength(3);
    expect(groups.map((group) => group.textContent)).toEqual(
      expect.arrayContaining(["1 actionSolo", "1 actionAfter message", "1 actionAfter critical"]),
    );
    expect(screen.getByTestId("warning-item")).toBeTruthy();
  });

  test("preserves a local intent before a critical boundary while coalescing only the suffix across turns", () => {
    render(
      <TranscriptBody
        model={mixedBoundaryFixture}
        config={preset("intent")}
        surface="preview"
        disclosureScope="preview:mixed-boundary"
      />,
    );

    const groups = screen.getAllByTestId("intent-group");
    expect(groups).toHaveLength(2);
    expect(groups[0]?.textContent).toContain("Local intent");
    expect(groups[1]?.textContent).toContain("2 actions");
    expect(groups[1]?.textContent).toContain("Cross A");
    expect(groups[1]?.textContent).toContain("Cross B");
    expect(screen.getByTestId("warning-item")).toBeTruthy();

    const bodyText = screen.getByTestId("transcript-preview-flow").textContent ?? "";
    expect(bodyText.indexOf("Local intent")).toBeLessThan(bodyText.indexOf("critical boundary"));
    expect(bodyText.indexOf("critical boundary")).toBeLessThan(bodyText.indexOf("Cross A"));
    expect(screen.queryAllByTestId("intent-group")).toHaveLength(2);
  });
});
