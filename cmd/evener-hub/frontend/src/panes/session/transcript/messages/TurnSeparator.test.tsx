import type { TurnModel } from "@evener/appwire-client";
import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { TranscriptRenderProvider } from "../../../../transcriptDisplay/renderContext";
import { TurnSeparator } from "./TurnSeparator";

function turn(overrides: Partial<TurnModel> = {}): TurnModel {
  return { id: "turn_1", status: "completed", items: [], ...overrides };
}

type Segments = { roundTimings: boolean; tokenCounts: boolean; estimatedCost: boolean };

// Most tests below are about per-segment GATING - "one on does not drag the
// others in" - so each starts from an all-off baseline and switches up only
// the segments it names.
const ALL_OFF: Segments = { roundTimings: false, tokenCounts: false, estimatedCost: false };
const ALL_ON: Segments = { roundTimings: true, tokenCounts: true, estimatedCost: true };

function SeparatorHarness({ value, segments }: { value: TurnModel; segments: Segments }) {
  const config = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" }, segments);
  return (
    <TranscriptRenderProvider config={config} surface="readOnly" disclosureScope="test:turn-separator">
      <TurnSeparator turn={value} />
    </TranscriptRenderProvider>
  );
}

function renderSeparator(value: TurnModel, segments: Partial<Segments> = {}) {
  const view = render(<SeparatorHarness value={value} segments={{ ...ALL_OFF, ...segments }} />);
  return {
    ...view,
    setSegments(next: Partial<Segments>) {
      view.rerender(<SeparatorHarness value={value} segments={{ ...ALL_OFF, ...next }} />);
    },
  };
}

// Not registered via registerItemRenderer - this is a per-TURN concept, not
// a per-item one, so it's exercised by rendering it directly with a
// TurnModel rather than through itemRendererFor/TurnBlock. See the wave-4
// T2 report for the exact wiring TurnBlock.tsx needs at merge.

test("a turn with none of duration/usage/cost renders nothing (an in-progress turn has nothing real to show yet)", () => {
  const { container } = renderSeparator(turn(), ALL_ON);
  expect(container.firstChild).toBeNull();
});

test("renders the duration alone when only durationMs is present", () => {
  renderSeparator(turn({ durationMs: 1500 }), ALL_ON);
  expect(screen.getByTestId("turn-separator").textContent).toBe("1.5s");
});

test("renders duration, tokens, and cost joined with the standard separator", () => {
  renderSeparator(turn({ durationMs: 65432, usage: { inputTokens: 500, outputTokens: 120 }, cost: "$1.00" }), ALL_ON);
  expect(screen.getByTestId("turn-separator").textContent).toBe("65s · ↑500 ↓120 · $1.00");
});

test("omits a missing segment rather than showing a placeholder for it (fields may be absent)", () => {
  renderSeparator(turn({ usage: { inputTokens: 10, outputTokens: 5 } }), ALL_ON);
  expect(screen.getByTestId("turn-separator").textContent).toBe("↑10 ↓5");
});

test("cost alone (a source that reports cost but not duration/usage) still renders", () => {
  renderSeparator(turn({ cost: "$0.02" }), ALL_ON);
  expect(screen.getByTestId("turn-separator").textContent).toBe("$0.02");
});

// --- preference gating: all three segments are opt-in.

const fullTurn = () => turn({ durationMs: 65432, usage: { inputTokens: 500, outputTokens: 120 }, cost: "$1.00" });

test("a turn carrying all three figures renders no row at all with every segment off", () => {
  const { container } = renderSeparator(fullTurn());
  expect(container.firstChild).toBeNull();
  expect(screen.queryByTestId("turn-separator")).toBeNull();
});

test("Round timings on alone shows only the duration - no leading or trailing separator dot", () => {
  renderSeparator(fullTurn(), { roundTimings: true });
  expect(screen.getByTestId("turn-separator").textContent).toBe("65s");
});

test("Token counts on alone shows only the token counts", () => {
  renderSeparator(fullTurn(), { tokenCounts: true });
  expect(screen.getByTestId("turn-separator").textContent).toBe("↑500 ↓120");
});

test("Show estimated cost on alone shows only the cost", () => {
  renderSeparator(fullTurn(), { estimatedCost: true });
  expect(screen.getByTestId("turn-separator").textContent).toBe("$1.00");
});

test("a suppressed MIDDLE segment leaves no doubled separator between the two that remain", () => {
  renderSeparator(fullTurn(), { roundTimings: true, estimatedCost: true });
  expect(screen.getByTestId("turn-separator").textContent).toBe("65s · $1.00");
});

test("each segment is gated by its own pref - one on does not drag the others in", () => {
  renderSeparator(fullTurn(), { tokenCounts: true });
  const text = screen.getByTestId("turn-separator").textContent ?? "";
  expect(text).not.toContain("65s");
  expect(text).not.toContain("$1.00");
});

test("flipping a segment re-renders an already-mounted separator live (a Settings toggle takes effect immediately)", () => {
  const view = renderSeparator(fullTurn());
  expect(screen.queryByTestId("turn-separator")).toBeNull();

  view.setSegments({ estimatedCost: true });
  expect(screen.getByTestId("turn-separator").textContent).toBe("$1.00");

  view.setSegments({ estimatedCost: true, roundTimings: true });
  expect(screen.getByTestId("turn-separator").textContent).toBe("65s · $1.00");

  view.setSegments({ roundTimings: true });
  expect(screen.getByTestId("turn-separator").textContent).toBe("65s");
});

// --- t8nc: duration carries a visible attachment, like its siblings -------
// Tokens self-label via "↑/↓"; cost self-labels via "$". Duration alone was
// a bare number with no such attachment, so when Round timings is the only
// segment on, the row was just "10s" sitting there unattached to anything.
// A small icon (not text, so the .textContent assertions above - and every
// wire format this row promises - stay byte-for-byte unchanged) attaches it.

test("Round timings alone: the duration segment carries a visible icon, not just a bare number", () => {
  const { container } = renderSeparator(turn({ durationMs: 10000 }), { roundTimings: true });
  const row = screen.getByTestId("turn-separator");
  expect(row.textContent).toBe("10s"); // wire format is unchanged
  expect(container.querySelector("svg")).toBeTruthy(); // but it's not bare
});

test("the duration icon is decorative only (aria-hidden), never read twice by a screen reader", () => {
  const { container } = renderSeparator(turn({ durationMs: 10000 }), { roundTimings: true });
  expect(container.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
});
