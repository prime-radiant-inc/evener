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
});
