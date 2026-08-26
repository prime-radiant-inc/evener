import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import type { ThreadModel } from "../../../protocol/model";
import { makeTranscriptDisplayConfig } from "../../../transcriptDisplay/config";
import { TranscriptBody } from "./TranscriptBody";

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

afterEach(cleanup);

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

  test("uses normal page flow for preview without an inner virtual scroller", () => {
    render(
      <TranscriptBody model={fixture} config={preset("tools")} surface="preview" disclosureScope="preview:test" />,
    );

    expect(screen.queryByTestId("transcript-virtual-list")).toBeNull();
    expect(screen.getByText("Inspect the tree")).toBeTruthy();
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
