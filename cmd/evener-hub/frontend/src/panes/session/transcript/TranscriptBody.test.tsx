import type { ThreadModel } from "@evener/appwire-client";
import * as appwireClient from "@evener/appwire-client";
import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import {
  subagentOutcomesDelegatesResponse,
  subagentWireStep,
} from "@evener/appwire-client/testing/subagentWireFixtures";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createRef, type RefObject, StrictMode, useRef, useState } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { sessionActivitySnapshot } from "../../../stores/sessionActivity";
import { activityClient, activitySummary } from "../../../stores/sessionActivityTestUtils";
import { threadsStore } from "../../../stores/threads";
import { transcriptDisplayStore } from "../../../stores/transcriptDisplay";
import type { VirtualListHandle } from "../../../widgets";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import {
  captureTranscriptViews,
  prepareTranscriptViewRemount,
  resetTranscriptViewRegistryForTests,
  restoreTranscriptViews,
  transitionTranscriptViews,
} from "./flow/transcriptViewRegistry";
import * as flowModule from "./flow/useTranscriptScroll";
import { TranscriptBody, transcriptAnchorEntriesForRows, transcriptRowsForProjection } from "./TranscriptBody";
import { installTranscriptGeometry } from "./transcriptReadingGeometryTestUtils";
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

    // The intent text renders head + glued final word (ToolRow's .intentTail);
    // assert it through the row rather than getByText, which only matches text
    // contained in one element.
    expect(screen.getByTestId("tool-row-intent").textContent).toBe("Inspect the tree");
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
    expect(screen.getByTestId("tool-row-intent").textContent).toBe("Inspect the tree");
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
    const external = installTranscriptGeometry(() => ({ width: 500, viewportHeight: 300, rowHeights: [600] }));
    try {
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
      await act(async () => external.notify());
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
      await act(async () => external.notify());
      await waitFor(() => expect(document.activeElement).toBe(summary));
      expect(group.hasAttribute("open")).toBe(false);
      expect(announce).toHaveBeenCalledWith("Chat");
    } finally {
      cleanup();
      external.restore();
    }
  });

  test("keeps newly focused Tools content through Chat while the prior display restore is pending", async () => {
    const geometry = { width: 500, viewportHeight: 300, rowHeights: [600] };
    const external = installTranscriptGeometry(() => geometry);
    try {
      const listRef = createRef<VirtualListHandle>();
      let show: (level: "full" | "tools" | "chat") => void = () => {
        throw new Error("pending focus harness did not mount");
      };
      function PendingFocusHarness() {
        const [config, setConfig] = useState(preset("full"));
        show = (level) => setConfig(preset(level));
        return (
          <TranscriptBody
            model={fixture}
            config={config}
            surface="live"
            disclosureScope="live:pending-focus"
            viewId="pending-focus"
            listRef={listRef}
          />
        );
      }
      render(<PendingFocusHarness />);
      await act(async () => external.notify());
      const before = captureTranscriptViews().get("pending-focus");
      expect(before?.readingPoint?.viewportWidth).toBe(500);

      act(() => {
        transitionTranscriptViews(
          () => {
            geometry.width = 480;
            show("tools");
          },
          "Tools",
          { fingerprint: "tools" },
        );
      });
      const tool = screen.getByTestId("tool-row-trigger");
      tool.focus();
      expect(document.activeElement).toBe(tool);
      const during = captureTranscriptViews().get("pending-focus");
      expect(during?.readingPoint).toEqual(before?.readingPoint);
      expect(during?.focusedEntryId).toBe("tool_1");

      act(() => transitionTranscriptViews(() => show("chat"), "Chat", { fingerprint: "chat" }));
      const group = screen.getByTestId("intent-group");
      const summary = group.querySelector(":scope > summary");
      if (!(summary instanceof HTMLElement)) throw new Error("Chat intent group summary did not render");
      await act(async () => external.notify());
      await waitFor(() => expect(document.activeElement).toBe(summary));
      expect(group.hasAttribute("open")).toBe(false);
      expect(document.body.contains(tool)).toBe(false);
    } finally {
      cleanup();
      external.restore();
    }
  });

  test.each(["width", "viewportHeight"] as const)(
    "moves focus to the Chat intent proxy when the display change also changes %s",
    async (dimension) => {
      const geometry = { width: 500, viewportHeight: 300, rowHeights: [600] };
      const external = installTranscriptGeometry(() => geometry);
      try {
        const listRef = createRef<VirtualListHandle>();
        let showChat: () => void = () => {
          throw new Error("focus reflow harness did not mount");
        };
        function FocusReflowHarness() {
          const [config, setConfig] = useState(preset("tools"));
          showChat = () => setConfig(preset("chat"));
          return (
            <TranscriptBody
              model={fixture}
              config={config}
              surface="live"
              disclosureScope={`live:focus-reflow-${dimension}`}
              viewId={`focus-reflow-${dimension}`}
              listRef={listRef}
            />
          );
        }
        render(<FocusReflowHarness />);
        await act(async () => external.notify());
        const tool = await screen.findByTestId("tool-row-trigger");
        tool.focus();
        expect(document.activeElement).toBe(tool);

        act(() => {
          transitionTranscriptViews(
            () => {
              geometry[dimension] += 36;
              showChat();
            },
            "Chat",
            { fingerprint: "chat" },
          );
        });

        const group = await screen.findByTestId("intent-group");
        const summary = group.querySelector(":scope > summary");
        if (!(summary instanceof HTMLElement)) throw new Error("Chat intent group summary did not render");
        await act(async () => external.notify());
        await waitFor(() => expect(document.activeElement).toBe(summary));
        expect(group.hasAttribute("open")).toBe(false);
        expect(document.body.contains(tool)).toBe(false);
      } finally {
        cleanup();
        external.restore();
      }
    },
  );

  test("focuses the Transcript region when a view change removes the focused row", async () => {
    const external = installTranscriptGeometry(() => ({ width: 500, viewportHeight: 300, rowHeights: [600] }));
    try {
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
      await act(async () => external.notify());
      const tool = await screen.findByTestId("tool-row-trigger");
      tool.focus();
      expect(document.activeElement).toBe(tool);
      expect(captureTranscriptViews().get("focus-region-fallback")?.focusedEntryId).toBe("tool_1");

      act(() => {
        transitionTranscriptViews(hideTools, "Custom content", {
          fingerprint: "custom-without-tools",
        });
      });

      await act(async () => external.notify());
      await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("region", { name: "Transcript" })));
      expect(document.body.contains(tool)).toBe(false);
      expect(screen.queryByTestId("tool-call-item")).toBeNull();
      expect(screen.queryByTestId("intent-group")).toBeNull();
      expect(announce).toHaveBeenCalledWith("Custom content");
    } finally {
      cleanup();
      external.restore();
    }
  });

  test("uses normal page flow for preview without an inner virtual scroller", () => {
    render(
      <TranscriptBody model={fixture} config={preset("tools")} surface="preview" disclosureScope="preview:test" />,
    );

    expect(screen.queryByTestId("transcript-virtual-list")).toBeNull();
    expect(screen.getByTestId("tool-row-intent").textContent).toBe("Inspect the tree");
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
          sourcePaneId="pane_fixture"
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

  test("refreshes ordinary ask_user suffix, delegate terminal outcome, and keeps a failed row collapsed", async () => {
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
      argumentsJSON: '{"prompt":"inspect"}',
      output: JSON.stringify({ delegate_id: "dlg_ordinary", status: "running", transcript_ref: "local:child" }),
      status: "completed",
    };
    const delegateBefore = {
      ...ordinaryToolFixture,
      delegates: [{ delegateId: "dlg_ordinary", transcriptRef: "local:child", status: "running", terminal: false }],
      turns: [{ id: "delegate_turn", status: "completed", items: [delegateItem] }],
    } as unknown as ThreadModel;
    const delegateAfter = {
      ...delegateBefore,
      delegates: [
        { delegateId: "dlg_ordinary", transcriptRef: "local:child", status: "done", outcome: "done", terminal: true },
      ],
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
    // A failed row is collapsed at tools level (no auto-open)...
    expect(firstToolExpanded()).toBe("false");
    rerender(
      <TranscriptBody
        model={supersedeAfter}
        config={preset("tools")}
        surface="preview"
        disclosureScope="ordinary:supersede"
      />,
    );
    // ...and stays collapsed once the model's next same-tool call lands.
    expect(firstToolExpanded()).toBe("false");
  });

  test("refreshes delegate exhaustion, reason, usage, and run timing without status or outcome changes", async () => {
    const delegateItem = {
      id: "refresh_delegate",
      turnId: "refresh_turn",
      type: "commandExecution",
      text: "",
      toolName: "delegate",
      description: "Inspect a settled child",
      argumentsJSON: '{"prompt":"inspect"}',
      output: JSON.stringify({ delegate_id: "dlg_refresh", status: "done", transcript_ref: "local:child" }),
      status: "completed",
    };
    const settledDelegate = {
      delegateId: "dlg_refresh",
      transcriptRef: "local:child",
      status: "done",
      outcome: "done",
      terminal: true,
      needsAttention: false,
      projectionRevision: 1,
    };
    const rowBefore = {
      ...ordinaryToolFixture,
      delegates: [settledDelegate],
      turns: [{ id: "refresh_turn", status: "completed", items: [delegateItem] }],
    } as unknown as ThreadModel;
    const { rerender } = render(
      <TranscriptBody
        model={rowBefore}
        config={preset("tools")}
        surface="preview"
        disclosureScope="ordinary:refresh"
        sessionRef="ordinary:refresh"
      />,
    );
    const settledLifecycle = screen.getByTestId("subagent-stats");
    expect(settledLifecycle.getAttribute("data-attention")).toBeNull();
    expect(settledLifecycle.textContent).not.toContain("Needs attention");

    // Every field the memoized delegate row renders but the old fingerprint
    // omitted: exhaustion evidence, failure reason, usage, run timing, and
    // the reducer's own revision.
    const onlyChange = (changes: Record<string, unknown>): ThreadModel =>
      ({
        ...rowBefore,
        delegates: [{ ...settledDelegate, ...changes }],
      }) as unknown as ThreadModel;
    const before = threadFingerprintForItem(delegateItem, rowBefore);
    for (const change of [
      { exhaustionBudget: "0 of 3", exhaustionLimit: 3, projectionRevision: 2 },
      { reason: "exhausted", projectionRevision: 2 },
      { usage: { inputTokens: 100, outputTokens: 20 }, projectionRevision: 2 },
      { runStartedAt: "2026-09-10T00:00:00Z", runEndedAt: "2026-09-10T00:01:00Z", projectionRevision: 2 },
    ]) {
      expect(threadFingerprintForItem(delegateItem, onlyChange(change))).not.toBe(before);
    }

    // Pin the tuple's contents, not production timing: a snapshot change
    // limited to the dead plumbing fields (which left the tuple with the
    // card's attention marker) does not move the fingerprint. Production
    // always pairs such a change with a projectionRevision bump - covered
    // by the rerender half below - so this pins that the fields never
    // re-enter the tuple, not that the row never re-renders.
    for (const change of [{ needsAttention: true }, { resumable: false }]) {
      expect(threadFingerprintForItem(delegateItem, onlyChange(change))).toBe(before);
    }

    // A needsAttention flip re-renders nothing the reader can see: the word
    // stays the lifecycle's own.
    const rowAfter = onlyChange({ needsAttention: true, projectionRevision: 2 });
    rerender(
      <TranscriptBody
        model={rowAfter}
        config={preset("tools")}
        surface="preview"
        disclosureScope="ordinary:refresh"
        sessionRef="ordinary:refresh"
      />,
    );
    await waitFor(() => {
      const statsLine = screen.getByTestId("subagent-stats");
      expect(statsLine.getAttribute("data-attention")).toBeNull();
      expect(statsLine.textContent).toContain("Idle · reported");
      expect(statsLine.textContent).not.toContain("Needs attention");
    });
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

describe("trailingRow", () => {
  test("renders the trailing row as the last virtual transcript row on the live surface", () => {
    render(
      <TranscriptBody
        model={fixture}
        config={preset("tools")}
        surface="live"
        disclosureScope="live:trailing-row"
        trailingRow={{ id: "live-edge", content: <div data-testid="trailing-sentinel">Answer me</div> }}
      />,
    );

    const rows = screen.getAllByTestId("transcript-row");
    expect(rows).toHaveLength(2);
    const last = rows.at(-1);
    expect(last?.getAttribute("data-row-id")).toBe("live-edge");
    const sentinel = screen.getByTestId("trailing-sentinel");
    expect(last?.contains(sentinel)).toBe(true);
  });

  test("a rerender keeps the trailing row's identity stable and after every transcript row", () => {
    const { rerender } = render(
      <TranscriptBody
        model={fixture}
        config={preset("tools")}
        surface="live"
        disclosureScope="live:trailing-row-stable"
        trailingRow={{ id: "live-edge", content: <div data-testid="trailing-sentinel">Answer me</div> }}
      />,
    );
    const before = screen.getAllByTestId("transcript-row").map((row) => row.getAttribute("data-row-id"));
    rerender(
      <TranscriptBody
        model={fixture}
        config={preset("tools")}
        surface="live"
        disclosureScope="live:trailing-row-stable"
        trailingRow={{ id: "live-edge", content: <div data-testid="trailing-sentinel">Answer me</div> }}
      />,
    );
    expect(screen.getAllByTestId("transcript-row").map((row) => row.getAttribute("data-row-id"))).toEqual(before);
  });

  test("omitting trailingRow renders exactly the transcript rows", () => {
    render(
      <TranscriptBody model={fixture} config={preset("tools")} surface="live" disclosureScope="live:no-trailing" />,
    );
    const rows = screen.getAllByTestId("transcript-row");
    expect(rows).toHaveLength(1);
    expect(rows[0]?.getAttribute("data-row-id")).toBe("turn_1");
  });
});

describe("trailingRow scroll coordination", () => {
  test("the view registration's rendered row count includes the trailing row", () => {
    const realRegistration = flowModule.useTranscriptViewRegistration;
    const capturedCounts: Array<number | undefined> = [];
    const spy = vi
      .spyOn(flowModule, "useTranscriptViewRegistration")
      .mockImplementation((options: Parameters<typeof realRegistration>[0]) => {
        capturedCounts.push(options.renderedRowCount);
        return realRegistration(options);
      });
    try {
      render(
        <TranscriptBody
          model={fixture}
          config={preset("tools")}
          surface="live"
          disclosureScope="live:trailing-count"
          trailingRow={{ id: "live-edge", content: <div data-testid="trailing-sentinel" /> }}
        />,
      );
      // One turn row + the synthetic trailing row: following-bottom view
      // restores scroll to renderedRowCount - 1, so the count must cover the
      // trailing row or a pending ask's dock restores one row short.
      expect(capturedCounts.at(-1)).toBe(2);
    } finally {
      spy.mockRestore();
    }
  });
});

describe("prepared view", () => {
  test("consumes a prepared view without re-deriving it", () => {
    const project = vi.spyOn(appwireClient, "projectThread");
    const projection = appwireClient.projectThread(fixture, preset("tools"));
    const rows = transcriptRowsForProjection(projection);
    const anchorEntries = transcriptAnchorEntriesForRows(rows);
    project.mockClear();

    render(
      <TranscriptBody
        model={fixture}
        config={preset("tools")}
        preparedView={{ projection, rows, anchorEntries }}
        surface="live"
        disclosureScope="live:prepared"
      />,
    );

    expect(project).not.toHaveBeenCalled();
    expect(screen.getByTestId("tool-row-intent").textContent).toBe("Inspect the tree");
  });

  test("a caller without a prepared view still derives its own (preview/read-only unchanged)", () => {
    const project = vi.spyOn(appwireClient, "projectThread");

    render(
      <TranscriptBody model={fixture} config={preset("tools")} surface="preview" disclosureScope="preview:own-view" />,
    );

    expect(project).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("tool-row-intent").textContent).toBe("Inspect the tree");
  });
});

test.each([
  ["retained completion", "done"],
  ["new run", "running"],
  ["wrong owner", "unknown"],
  ["wrong child", "unknown"],
  ["ambiguous child", "unknown"],
  ["missing delegate", "unknown"],
] as const)("retained delegate receipt uses current activity authority: %s", async (scenario, expectedKind) => {
  const response = subagentOutcomesDelegatesResponse();
  const reported = response.delegates.find((row) => row.delegateId === "dlg_reported");
  if (!reported) throw new Error("recorded reported delegate missing");
  if (scenario === "new run")
    Object.assign(reported, {
      runGeneration: reported.runGeneration + 1,
      status: "running",
      lifecycle: "running",
      terminal: false,
    });
  if (scenario === "wrong owner") reported.ownerRef = "local:other";
  if (scenario === "ambiguous child") response.delegates.push({ ...reported, childRef: "local:other-child" });
  if (scenario === "missing delegate") response.delegates = response.delegates.filter((row) => row !== reported);
  const recorded = subagentWireStep("call_delegate_1");
  if (scenario === "wrong child") reported.childRef = "local:wrong-child";
  const receipt = {
    ...recorded,
    output: JSON.stringify({
      ...JSON.parse(recorded.output ?? "{}"),
      delegate_id: reported.delegateId,
      transcript_ref: scenario === "wrong child" ? "local:child-dlg_reported" : reported.childRef,
    }),
  };
  const client = activityClient();
  client.on("evener/thread/delegates/list", () => ({
    ...response,
    context: { ...response.context, availability: "retained" },
    scope: "session",
  }));
  client.on("evener/thread/activity/read", () => ({
    ...activitySummary(response.context.ref),
    context: response.context,
    scope: "session",
  }));
  client.on("evener/thread/jobs/list", () => ({
    context: response.context,
    scope: "session",
    jobs: [],
    page: { complete: true, issues: [] },
  }));
  const model: ThreadModel = {
    ...fixture,
    ref: response.context.ref,
    delegates: undefined,
    turns: [{ id: receipt.turnId, status: "completed", items: [receipt] }],
  };
  connectionStore.setState({ client, state: "ready" });
  try {
    render(
      <TranscriptBody
        model={model}
        config={preset("tools")}
        surface="preview"
        sessionRef={response.context.ref}
        disclosureScope="retained:delegate"
      />,
    );
    await waitFor(() =>
      expect(sessionActivitySnapshot(client, response.context.ref, "session")?.delegates.rows).toHaveLength(
        response.delegates.length,
      ),
    );
    await waitFor(() =>
      expect(screen.getByTestId("delegate-status-word").getAttribute("data-kind")).toBe(expectedKind),
    );
    const bodyId = screen.getByTestId("tool-call-body").id;
    const toggle = screen.getByTestId("tool-row").querySelector(`button[aria-controls="${bodyId}"]`);
    if (!toggle) throw new Error("delegate disclosure missing");
    fireEvent.click(toggle);
    expect(screen.getByTestId("delegate-lifecycle").getAttribute("data-kind")).toBe(expectedKind);
    expect(client.calls.filter((call) => call.method === "thread/read").map((call) => call.params)).toEqual([
      { ref: response.context.ref, includeTurns: false, subscribe: true, replaceSubscription: false },
    ]);
  } finally {
    cleanup();
    connectionStore.setState({ client: null, state: "idle" });
  }
});

// Real Body + registry + ordinary parent coordinator + TanStack VirtualList.
// jsdom supplies no layout: only browser geometry and native resize delivery
// are controlled here. No Evener component, hook, registry or widget is mocked.
describe("retained transcript placement", () => {
  let rowHeight: number;
  const descriptors = new Map<string, PropertyDescriptor | undefined>();
  const observers = new Set<GeometryObserver>();
  class GeometryObserver {
    readonly elements = new Set<Element>();
    constructor(readonly callback: ResizeObserverCallback) {
      observers.add(this);
    }
    observe(element: Element) {
      this.elements.add(element);
    }
    unobserve(element: Element) {
      this.elements.delete(element);
    }
    disconnect() {
      this.elements.clear();
      observers.delete(this);
    }
  }
  const longModel = {
    ...fixture,
    turns: [
      {
        id: "long-turn",
        status: "completed",
        items: [
          {
            id: "long-message",
            turnId: "long-turn",
            type: "agentMessage",
            text: "Long assistant reply",
            status: "completed",
          },
        ],
      },
    ],
  } as ThreadModel;

  function isPort(element: HTMLElement) {
    return element.parentElement?.dataset.testid === "transcript-virtual-list";
  }
  function rect(top: number, height: number): DOMRect {
    return { x: 0, y: top, top, bottom: top + height, left: 0, right: 490, width: 490, height, toJSON: () => ({}) };
  }
  beforeEach(() => {
    rowHeight = 4368;
    for (const property of [
      "offsetHeight",
      "offsetWidth",
      "clientWidth",
      "clientHeight",
      "scrollHeight",
      "getBoundingClientRect",
      "scrollTo",
    ]) {
      descriptors.set(property, Object.getOwnPropertyDescriptor(HTMLElement.prototype, property));
    }
    Object.defineProperties(HTMLElement.prototype, {
      offsetHeight: {
        configurable: true,
        get() {
          return this.hasAttribute("data-index") ? rowHeight : 656;
        },
      },
      offsetWidth: {
        configurable: true,
        get() {
          return 490;
        },
      },
      clientWidth: {
        configurable: true,
        get() {
          return isPort(this) ? 490 : 0;
        },
      },
      clientHeight: {
        configurable: true,
        get() {
          return isPort(this) ? 656 : 0;
        },
      },
      scrollHeight: {
        configurable: true,
        get() {
          return isPort(this) ? Math.max(656, Number.parseFloat(this.firstElementChild?.style.height ?? "0")) : 0;
        },
      },
      getBoundingClientRect: {
        configurable: true,
        value: function (this: HTMLElement) {
          const port = this.closest('[data-testid="transcript-virtual-list"]')?.firstElementChild as HTMLElement | null;
          return this.hasAttribute("data-view-anchor-id")
            ? rect(-(port?.scrollTop ?? 0), rowHeight)
            : rect(
                0,
                isPort(this)
                  ? 656
                  : this.parentElement && isPort(this.parentElement)
                    ? Number.parseFloat(this.style.height)
                    : rowHeight,
              );
        },
      },
      scrollTo: {
        configurable: true,
        value: function (this: HTMLElement, options: ScrollToOptions) {
          this.scrollTop = Math.max(0, Math.min(options.top ?? 0, this.scrollHeight - this.clientHeight));
        },
      },
    });
    vi.stubGlobal("ResizeObserver", GeometryObserver);
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    for (const [property, descriptor] of descriptors) {
      if (descriptor) Object.defineProperty(HTMLElement.prototype, property, descriptor);
      else Reflect.deleteProperty(HTMLElement.prototype, property);
    }
    descriptors.clear();
    observers.clear();
  });
  function resized() {
    act(() => {
      for (const observer of [...observers]) {
        const entries = [...observer.elements].map((target) => {
          const size = { blockSize: (target as HTMLElement).offsetHeight, inlineSize: 490 };
          return {
            target,
            borderBoxSize: [size],
            contentBoxSize: [size],
            devicePixelContentBoxSize: [size],
            contentRect: target.getBoundingClientRect(),
          } satisfies ResizeObserverEntry;
        });
        observer.callback(entries, observer as unknown as ResizeObserver);
      }
    });
  }
  const keepMountedView = () => true;
  function Harness({
    loaded = true,
    session = fixture.ref,
    keepRemount,
    viewId = "retained-pane",
    empty = false,
    listRef: suppliedListRef,
  }: {
    loaded?: boolean;
    session?: string;
    keepRemount?: () => boolean;
    viewId?: string;
    empty?: boolean;
    listRef?: RefObject<VirtualListHandle | null>;
  }) {
    const internalListRef = useRef<VirtualListHandle>(null);
    const listRef = suppliedListRef ?? internalListRef;
    const currentModel = loaded ? { ...longModel, ref: session, turns: empty ? [] : longModel.turns } : undefined;
    const flow = flowModule.useTranscriptScroll({
      ref: session,
      model: currentModel,
      listRef,
      loadOlder: async () => {},
      viewId,
      keepViewRemount: keepRemount,
      renderedRowCount: loaded && !empty ? 1 : 0,
    });
    return currentModel ? (
      <TranscriptBody
        model={currentModel}
        config={preset("full")}
        surface="live"
        disclosureScope="retained"
        sessionRef={session}
        viewId={viewId}
        listRef={listRef}
        onMeasurementsChange={flow.restoreViewAnchorAfterMeasurement}
      />
    ) : (
      <p>Loading same view</p>
    );
  }
  function port() {
    return screen.getByTestId("transcript-virtual-list").firstElementChild as HTMLElement;
  }
  function readBack(top: number, element = port()) {
    act(() => {
      fireEvent.scroll(element);
      fireEvent.wheel(element, { deltaY: -420 });
      element.scrollTop = top;
      fireEvent.scroll(element);
    });
  }

  test("keeps the coherent long-row anchor when CSS reflows before viewport capture", () => {
    render(<Harness />);
    readBack(3292);
    expect(captureTranscriptViews().get("retained-pane")).toMatchObject({
      anchorId: "long-message",
      anchorOffset: -3292,
    });
    // CSS has reflowed the DOM, but ResizeObserver has not yet updated the sizer.
    rowHeight = 2639;
    expect(captureTranscriptViews().get("retained-pane")).toMatchObject({
      anchorId: "long-message",
      anchorOffset: -3292,
      followingBottom: false,
    });
  });

  test.each([false, true])(
    "coherent geometry keeps current focus ownership rather than cached focus, focused %s",
    (focused) => {
      render(
        <>
          <button type="button">Outside transcript</button>
          <Harness />
        </>,
      );
      readBack(3292);
      const anchor = port().querySelector<HTMLElement>('[data-view-anchor-id="long-message"]');
      if (!anchor) throw new Error("long message anchor missing");
      anchor.tabIndex = -1;
      anchor.focus();
      if (!focused) {
        captureTranscriptViews();
        screen.getByRole("button", { name: "Outside transcript" }).focus();
      }
      rowHeight = 2639;
      const captured = captureTranscriptViews().get("retained-pane");
      expect(captured?.anchorOffset).toBe(-3292);
      expect(captured?.focusedEntryId).toBe(focused ? "long-message" : undefined);
    },
  );

  test("does not consume normalized restoration in the initial zero-range measurement", () => {
    rowHeight = 96;
    render(<Harness />);
    act(() =>
      restoreTranscriptViews(
        new Map([
          [
            "retained-pane",
            {
              anchorOffset: 0,
              normalizedOffset: 0.5,
              followingBottom: false,
            },
          ],
        ]),
      ),
    );
    resized();
    expect(port().scrollTop).toBe(0);
    rowHeight = 4368;
    resized();
    expect((port().firstElementChild as HTMLElement).style.height).toBe("4368px");
    expect(port().scrollTop).toBe(1856);
  });

  test("an actually empty transcript consumes its normalized fallback without waiting for nonexistent rows", () => {
    const { rerender } = render(<Harness empty />);
    act(() =>
      restoreTranscriptViews(
        new Map([
          [
            "retained-pane",
            {
              anchorOffset: 0,
              normalizedOffset: 0.5,
              followingBottom: false,
            },
          ],
        ]),
      ),
    );
    // An empty list has no row resize to publish. Its next real Body commit
    // is the measurement boundary, just as for a view/configuration change.
    rerender(<Harness empty />);
    resized();
    expect(port().scrollTop).toBe(0);
    expect(captureTranscriptViews().get("retained-pane")?.followingBottom).toBe(true);
  });

  test.each([false, true])("same exact view rehydrates away from latest, StrictMode %s", (strict) => {
    const view = (loaded: boolean, session = fixture.ref) =>
      strict ? (
        <StrictMode>
          <Harness loaded={loaded} session={session} />
        </StrictMode>
      ) : (
        <Harness loaded={loaded} session={session} />
      );
    const { rerender } = render(view(true));
    readBack(3292);
    rerender(view(false));
    expect(screen.getByText("Loading same view")).toBeTruthy();
    rerender(view(true));
    resized();
    expect(port().scrollTop).toBe(3292);
  });

  test("a new ref in the same mounted pane rejects the old pending placement and opens at latest", () => {
    rowHeight = 96;
    const { rerender } = render(<Harness />);
    act(() =>
      restoreTranscriptViews(
        new Map([
          [
            "retained-pane",
            {
              anchorOffset: 0,
              normalizedOffset: 0.5,
              followingBottom: false,
            },
          ],
        ]),
      ),
    );
    resized();
    expect(port().scrollTop).toBe(0);
    rowHeight = 4368;
    rerender(<Harness session="local:new-session" />);
    resized();
    expect(port().scrollTop).toBe(3712);
  });

  test.each([false, true])(
    "a completed A placement cannot suppress latest after same-DOM A→B→A, StrictMode %s",
    (strict) => {
      const listRef = createRef<VirtualListHandle>();
      const view = (session: string) => {
        const body = <Harness session={session} listRef={listRef} />;
        return strict ? <StrictMode>{body}</StrictMode> : body;
      };
      const { rerender } = render(view(fixture.ref));
      const scrollingElement = port();
      resized();
      expect(listRef.current?.isMeasurementReady?.()).toBe(true);
      expect(scrollingElement.scrollHeight).toBe(4368);
      expect(scrollingElement.clientHeight).toBe(656);
      expect(scrollingElement.scrollTop).toBe(3712);

      readBack(3292);
      const captured = captureTranscriptViews();
      expect(captured.get("retained-pane")).toMatchObject({
        anchorId: "long-message",
        anchorOffset: -3292,
        followingBottom: false,
      });
      readBack(3000);
      act(() => restoreTranscriptViews(captured));
      // Unchanged row size need not notify the virtualizer again. Its real
      // Body commit also retries placement against the useful measurement.
      rerender(view(fixture.ref));
      resized();
      expect(port()).toBe(scrollingElement);
      expect(scrollingElement.scrollTop).toBe(3292);
      // The production restore completed, rather than remaining pending and
      // being consumed by B. A subsequent measurement respects reader input.
      readBack(3200);
      rerender(view(fixture.ref));
      resized();
      expect(scrollingElement.scrollTop).toBe(3200);

      rerender(view("local:session-b"));
      resized();
      expect(port()).toBe(scrollingElement);
      expect(listRef.current?.isMeasurementReady?.()).toBe(true);
      expect(scrollingElement.scrollTop).toBe(3712);
      readBack(3000);
      expect(captureTranscriptViews().get("retained-pane")).toMatchObject({
        anchorOffset: -3000,
        followingBottom: false,
      });

      rerender(view(fixture.ref));
      resized();
      expect(port()).toBe(scrollingElement);
      expect(listRef.current?.isMeasurementReady?.()).toBe(true);
      expect(scrollingElement.scrollTop).toBe(3712);
    },
  );

  test("same-A child registration hands measured retained placement to parent initialization", () => {
    const listRef = createRef<VirtualListHandle>();
    const { rerender } = render(<Harness key="outgoing" listRef={listRef} />);
    resized();
    expect(listRef.current?.isMeasurementReady?.()).toBe(true);
    expect(port().scrollTop).toBe(3712);
    readBack(3292);
    act(() => prepareTranscriptViewRemount(captureTranscriptViews(), "desktop"));
    // Registration is the only incoming restore. Do not replay a transition's
    // finally restore after the parent's initialization could overwrite it.
    rerender(<Harness key="incoming" listRef={listRef} />);
    expect(port().scrollTop).toBe(3292);
    resized();
    expect(listRef.current?.isMeasurementReady?.()).toBe(true);
    expect(port().scrollTop).toBe(3292);
  });

  test("closing and reopening the same pane id starts at latest rather than its previous session position", () => {
    const { rerender } = render(<Harness />);
    readBack(3292);
    rerender(<p>Closed pane</p>);
    rerender(<Harness />);
    resized();
    expect(port().scrollTop).toBe(3712);
  });

  test("two exact views keep distinct positions through same-view content reload", () => {
    const views = (loaded: boolean) => (
      <>
        <Harness loaded={loaded} viewId="view-a" />
        <Harness loaded={loaded} viewId="view-b" />
      </>
    );
    const { rerender } = render(views(true));
    const ports = () =>
      screen.getAllByTestId("transcript-virtual-list").map((list) => list.firstElementChild as HTMLElement);
    readBack(3292, ports()[0]);
    readBack(3000, ports()[1]);
    rerender(views(false));
    rerender(views(true));
    resized();
    expect(ports().map((element) => element.scrollTop)).toEqual([3292, 3000]);
  });

  test("keeps the pending host placement through StrictMode replay while its model hydrates", () => {
    transcriptDisplayStore.setState({ viewport: "mobile" });
    const { rerender } = render(<Harness key="phone" keepRemount={keepMountedView} />);
    readBack(3292);
    const desktop = (loaded: boolean) => (
      <StrictMode>
        <Harness key="desktop" loaded={loaded} keepRemount={keepMountedView} />
      </StrictMode>
    );
    act(() =>
      transitionTranscriptViews(
        () => {
          transcriptDisplayStore.setState({ viewport: "desktop" });
          rerender(desktop(false));
        },
        "Desktop",
        { force: true, prepareRemount: true, targetLayout: "desktop" },
      ),
    );
    expect(screen.getByText("Loading same view")).toBeTruthy();
    rerender(desktop(true));
    resized();
    expect(port().scrollTop).toBe(3292);
  });

  test("captures the restored phone position before its native scroll event and returns to desktop", () => {
    transcriptDisplayStore.setState({ viewport: "desktop" });
    const { rerender } = render(<Harness key="desktop" />);
    readBack(3292);
    rowHeight = 2639;
    act(() =>
      transitionTranscriptViews(
        () => {
          rowHeight = 4368;
          transcriptDisplayStore.setState({ viewport: "mobile" });
          rerender(<Harness key="phone" />);
        },
        "Phone",
        { force: true, prepareRemount: true, targetLayout: "mobile" },
      ),
    );
    expect(port().scrollTop).toBe(3292);
    // No fabricated scroll event after restore. The next CSS reflow precedes
    // viewport publication just as in the unchanged Chrome guard.
    rowHeight = 2639;
    expect(captureTranscriptViews().get("retained-pane")).toMatchObject({
      anchorId: "long-message",
      anchorOffset: -3292,
    });
    act(() =>
      transitionTranscriptViews(
        () => {
          rowHeight = 4368;
          transcriptDisplayStore.setState({ viewport: "desktop" });
          rerender(<Harness key="desktop-again" />);
        },
        "Desktop",
        { force: true, prepareRemount: true, targetLayout: "desktop" },
      ),
    );
    expect(port().scrollTop).toBe(3292);
  });
});
