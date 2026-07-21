import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { TurnModel } from "../../../../protocol/model";
import { TurnSeparator } from "./TurnSeparator";

afterEach(cleanup);

function turn(overrides: Partial<TurnModel> = {}): TurnModel {
  return { id: "turn_1", status: "completed", items: [], ...overrides };
}

// Not registered via registerItemRenderer - this is a per-TURN concept, not
// a per-item one, so it's exercised by rendering it directly with a
// TurnModel rather than through itemRendererFor/TurnBlock. See the wave-4
// T2 report for the exact wiring TurnBlock.tsx needs at merge.

test("a turn with none of duration/usage/cost renders nothing (an in-progress turn has nothing real to show yet)", () => {
  const { container } = render(<TurnSeparator turn={turn()} />);
  expect(container.firstChild).toBeNull();
});

test("renders the duration alone when only durationMs is present", () => {
  render(<TurnSeparator turn={turn({ durationMs: 1500 })} />);
  expect(screen.getByTestId("turn-separator").textContent).toBe("1.5s");
});

test("renders duration, tokens, and cost joined with the standard separator", () => {
  render(
    <TurnSeparator turn={turn({ durationMs: 65432, usage: { inputTokens: 500, outputTokens: 120 }, cost: "$1.00" })} />,
  );
  expect(screen.getByTestId("turn-separator").textContent).toBe("65s · ↑500 ↓120 · $1.00");
});

test("omits a missing segment rather than showing a placeholder for it (fields may be absent)", () => {
  render(<TurnSeparator turn={turn({ usage: { inputTokens: 10, outputTokens: 5 } })} />);
  expect(screen.getByTestId("turn-separator").textContent).toBe("↑10 ↓5");
});

test("cost alone (a source that reports cost but not duration/usage) still renders", () => {
  render(<TurnSeparator turn={turn({ cost: "$0.02" })} />);
  expect(screen.getByTestId("turn-separator").textContent).toBe("$0.02");
});
