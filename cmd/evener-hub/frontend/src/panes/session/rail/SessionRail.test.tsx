// @vitest-environment jsdom

import { render } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import type { TurnModel } from "../../../protocol/model";
import type { RailEvent } from "./axis";
import { railModelFromTurns } from "./railModel";
import { promptLayout, SessionRail } from "./SessionRail";
import type { RailTheme } from "./useRailTheme";

const mockTheme: RailTheme = {
  accent: "#3D9AFF",
  alive: "#3DBB72",
  attention: "#F68F3C",
  danger: "#EE5C61",
  inkHi: "#F2F3F4",
  inkMid: "#A5A8AD",
  inkLow: "#6C6F75",
  surfaceCanvas: "#1C1D1F",
  surfaceInset: "#1F2022",
  surface2: "#232427",
  edge: "#2E3033",
  hover1: "#2A2B2E",
  fontMono: "monospace",
};

function makeTurns(count: number, startMs = 1000000, stepMs = 60000): TurnModel[] {
  const turns: TurnModel[] = [];
  for (let i = 0; i < count; i++) {
    turns.push({
      id: `turn_${i}`,
      status: "completed",
      items: [{ type: i === 0 ? "userMessage" : "assistantMessage", id: `item_${i}` } as never],
      startedAt: new Date(startMs + i * stepMs).toISOString(),
      usage: i > 0 ? { inputTokens: 100 * (i + 1), outputTokens: 50 * (i + 1), totalTokens: 150 * (i + 1) } : undefined,
    });
  }
  return turns;
}

describe("SessionRail", () => {
  test("renders without crashing when model has turns", () => {
    const turns = makeTurns(10);
    const model = railModelFromTurns(turns, 2000000);
    const { container } = render(
      <SessionRail model={model} nowMs={2000000} axis="time" theme={mockTheme} playing={false} ended={false} />,
    );
    // The rail section should exist even if canvas doesn't render in jsdom.
    expect(container.querySelector("section")).toBeTruthy();
  });

  test("renders prompt anchor buttons for user input turns", () => {
    const turns = makeTurns(5, 100000, 60000);
    const model = railModelFromTurns(turns, 500000);
    const { container } = render(
      <SessionRail model={model} nowMs={500000} axis="time" theme={mockTheme} playing={false} ended={false} />,
    );
    const anchors = container.querySelectorAll("button");
    // Turn 0 is a userMessage → should have an anchor.
    expect(anchors.length).toBeGreaterThanOrEqual(1);
  });

  test("does not render events after nowMs (no-future-ink)", () => {
    const turns = makeTurns(10, 0, 100000);
    const nowMs = 500000; // only first 5 turns should be visible
    const model = railModelFromTurns(turns, nowMs);
    // live.n should only count events at or before nowMs
    expect(model.live.n).toBeLessThanOrEqual(6);
    for (let i = 0; i < model.live.n; i++) {
      const ev = model.events[i];
      expect(ev).toBeDefined();
      expect(ev!.ms).toBeLessThanOrEqual(nowMs);
    }
  });

  test("anchor click calls onJump with event index", () => {
    const turns = makeTurns(5, 100000, 60000);
    const model = railModelFromTurns(turns, 500000);
    const onJump = vi.fn();
    const { container } = render(
      <SessionRail
        model={model}
        nowMs={500000}
        axis="time"
        theme={mockTheme}
        playing={false}
        ended={false}
        onJump={onJump}
      />,
    );
    const anchor = container.querySelector("button");
    expect(anchor).toBeTruthy();
    anchor?.click();
    expect(onJump).toHaveBeenCalledWith(expect.any(Number));
  });
});

describe("promptLayout", () => {
  test("only returns prompts at or before nowMs", () => {
    const events: RailEvent[] = [
      { pos: 0, ms: 100, kind: "USER_INPUT", userInput: true },
      { pos: 1, ms: 200, kind: "ASSISTANT" },
      { pos: 2, ms: 300, kind: "USER_INPUT", userInput: true },
    ];
    const view = { kind: "time" as const, nowMs: 250, startMs: 100, ap: { end: 600000 } };
    const layout = promptLayout(view, 200, events);
    // Only the first user input (ms=100 <= 250) should be included.
    expect(layout).toHaveLength(1);
    expect(layout[0]?.p.ms).toBe(100);
  });

  test("fans anchors to lanes when too close", () => {
    const events: RailEvent[] = [];
    for (let i = 0; i < 5; i++) {
      events.push({ pos: i, ms: 100 + i * 5, kind: "USER_INPUT", userInput: true });
    }
    const view = { kind: "time" as const, nowMs: 500, startMs: 100, ap: { end: 600000 } };
    const layout = promptLayout(view, 200, events);
    // All 5 anchors should be laid out, some on different lanes.
    expect(layout).toHaveLength(5);
    const lanes = new Set(layout.map((l) => l.lane));
    expect(lanes.size).toBeGreaterThan(1);
  });
});
