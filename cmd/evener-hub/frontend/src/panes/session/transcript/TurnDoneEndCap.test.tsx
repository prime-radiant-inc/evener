import { type ItemModel, makeTranscriptDisplayConfig, type TurnModel } from "@evener/appwire-client";
import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { TranscriptRenderProvider } from "../../../transcriptDisplay/renderContext";
import { TurnDoneEndCap } from "./TurnDoneEndCap";

function item(id: string, toolName?: string): ItemModel {
  return { id, turnId: "t1", type: toolName === undefined ? "agentMessage" : "commandExecution", text: "", toolName };
}

function turn(overrides: Partial<TurnModel> = {}): TurnModel {
  return { id: "t1", status: "completed", items: [], ...overrides };
}

function renderCap(turnModel: TurnModel, roundTimings = false) {
  return render(
    <TranscriptRenderProvider
      config={makeTranscriptDisplayConfig(
        { kind: "preset", level: "activity" },
        { roundTimings, systemEvents: true, promptEvents: true },
      )}
      surface="readOnly"
    >
      <TurnDoneEndCap turn={turnModel} />
    </TranscriptRenderProvider>,
  );
}

test("a completed turn closes with its duration and tool-call count", () => {
  renderCap(turn({ durationMs: 134_000, items: [item("a", "shell"), item("b", "read"), item("c")] }));
  const cap = screen.getByTestId("turn-done");
  expect(cap.textContent).toContain("Done in 2m");
  expect(cap.textContent).toContain("2 tool calls");
  expect(cap.getAttribute("data-turn-duration-ms")).toBe("134000");
});

test("one tool call reads in the singular", () => {
  renderCap(turn({ durationMs: 5_000, items: [item("a", "shell")] }));
  expect(screen.getByTestId("turn-done").textContent).toContain("1 tool call");
});

test("a turn with no tool calls still closes with its duration", () => {
  renderCap(turn({ durationMs: 5_000, items: [item("a")] }));
  const cap = screen.getByTestId("turn-done");
  expect(cap.textContent).toContain("Done in 5s");
  expect(cap.textContent).not.toContain("tool call");
});

test("a turn without a duration renders no end-cap", () => {
  renderCap(turn({ items: [item("a", "shell")] }));
  expect(screen.queryByTestId("turn-done")).toBeNull();
});

test("a user-only turn renders no end-cap", () => {
  renderCap(turn({ durationMs: 5_000, items: [{ id: "u", turnId: "t1", type: "userMessage", text: "hi" }] }));
  expect(screen.queryByTestId("turn-done")).toBeNull();
});

test("a completed turn with a zero duration closes without a fabricated duration", () => {
  renderCap(turn({ durationMs: 0, items: [item("a", "shell")] }));
  const cap = screen.getByTestId("turn-done");
  expect(cap.textContent).toContain("Done");
  expect(cap.textContent).not.toContain(" in ");
});

test("with round timings on, the cap leaves the exact duration to the footer", () => {
  renderCap(turn({ durationMs: 134_000, items: [item("a", "shell")] }), true);
  const cap = screen.getByTestId("turn-done");
  expect(cap.textContent).toContain("Done");
  expect(cap.textContent).not.toContain("Done in");
});
