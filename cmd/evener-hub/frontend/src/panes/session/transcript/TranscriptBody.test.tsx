import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createRef, useState } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { ItemModel, ThreadModel } from "../../../protocol/model";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import { threadsStore } from "../../../stores/threads";
import { makeTranscriptDisplayConfig } from "../../../transcriptDisplay/config";
import { createTranscriptRenderContext, TranscriptRenderProvider } from "../../../transcriptDisplay/renderContext";
import type { VirtualListHandle } from "../../../widgets";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import {
  captureTranscriptViews,
  resetTranscriptViewRegistryForTests,
  transitionTranscriptViews,
} from "./flow/transcriptViewRegistry";
import { ToolCallCluster } from "./ToolCallCluster";
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

const previewClusterFixture = {
  ...fixture,
  turns: [
    {
      ...fixture.turns[0],
      items: [
        fixture.turns[0]?.items[0],
        {
          id: "cluster_1",
          turnId: "turn_1",
          type: "commandExecution",
          toolName: "shell",
          argumentsJSON: '{"command":"pwd"}',
          status: "completed",
        },
        {
          id: "cluster_2",
          turnId: "turn_1",
          type: "commandExecution",
          toolName: "shell",
          argumentsJSON: '{"command":"ls"}',
          status: "completed",
        },
        {
          id: "cluster_3",
          turnId: "turn_1",
          type: "commandExecution",
          toolName: "shell",
          argumentsJSON: '{"command":"git status"}',
          status: "completed",
        },
        fixture.turns[0]?.items.at(-1),
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
    expect(screen.queryByTestId("tool-call-item")).toBeNull();
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

  test("focuses the stable transcript region when a view change removes the focused row", async () => {
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

    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("region", { name: "Transcript" })));
    expect(announce).toHaveBeenCalledWith("Chat");
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
        ?.querySelector('[data-testid="tool-row-trigger"]')
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

  test("Tools/Full previews mount item and cluster renderers without threadsStore or RPC access", () => {
    const getState = vi.spyOn(threadsStore, "getState");
    const subscribe = vi.spyOn(threadsStore, "subscribe");
    const getInitialState = vi.spyOn(threadsStore, "getInitialState");
    const fake = new FakeClient("ready");
    const request = vi.spyOn(fake, "request");
    const previewTurn = previewClusterFixture.turns[0];
    if (previewTurn === undefined) throw new Error("preview cluster turn did not render");
    const clusterItems = previewTurn.items.slice(1, 4) as ItemModel[];
    render(
      <>
        <TranscriptBody
          model={previewClusterFixture}
          config={preset("tools")}
          surface="preview"
          disclosureScope="preview:one"
        />
        <TranscriptBody
          model={previewClusterFixture}
          config={preset("full")}
          surface="preview"
          disclosureScope="preview:two"
        />
        <TranscriptRenderProvider
          config={preset("tools")}
          surface="preview"
          disclosureScope="preview:explicit-cluster"
          thread={previewClusterFixture}
        >
          <ToolCallCluster
            items={clusterItems}
            turn={previewTurn}
            sessionRef="preview:explicit-cluster"
            renderContext={createTranscriptRenderContext({
              config: preset("tools"),
              surface: "preview",
              disclosureScope: "preview:explicit-cluster",
              thread: previewClusterFixture,
            })}
          />
        </TranscriptRenderProvider>
      </>,
    );
    expect(screen.getAllByTestId("tool-call-item").length).toBeGreaterThan(0);
    expect(screen.getAllByTestId("tool-call-cluster")).toHaveLength(1);
    const cluster = screen.getByTestId("tool-call-cluster");
    const clusterTrigger = cluster.querySelector('[data-testid="tool-row-trigger"]');
    if (!(clusterTrigger instanceof HTMLElement)) throw new Error("preview cluster trigger did not render");
    fireEvent.click(clusterTrigger);
    expect(clusterTrigger.getAttribute("aria-expanded")).toBe("true");
    expect(cluster.querySelector('[data-testid="tool-call-cluster-body"]')).toBeTruthy();
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
    expect(crossGroup?.getAttribute("data-transcript-row-id")).toBe("intent-group:intent:tool_a:intent:tool_c");
    expect(crossGroup?.hasAttribute("open")).toBe(false);
    const crossSummary = crossGroup.querySelector("summary");
    if (crossSummary === null) throw new Error("cross-turn intent summary did not render");
    fireEvent.click(crossSummary);
    expect(crossGroup?.hasAttribute("open")).toBe(true);
    expect(single.hasAttribute("open")).toBe(true);
  });

  test("coalesces purpose-only intents across adjacent turns into one stable virtual row", () => {
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
    expect(screen.queryAllByTestId("tool-call-item")).toHaveLength(0);
    expect(screen.getAllByTestId("transcript-row")).toHaveLength(2);
    expect(screen.getAllByTestId("transcript-row")[0]?.getAttribute("data-row-id")).toBe(
      "intent-group:intent:tool_a:intent:tool_c",
    );
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
