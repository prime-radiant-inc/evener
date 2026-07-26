import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import type { TurnModel } from "../../../../protocol/model";
import { prefsStore, resetPrefsStoreForTests } from "../../../../stores/prefs";
import { TurnSeparator } from "./TurnSeparator";

// See shell/rail/Rail.test.tsx's identical comment: Node 26 shadows jsdom's
// real window.localStorage with its own (non-functional under vitest)
// global, so every test file that touches localStorage needs this same
// small in-memory stand-in. Scoped to this file only.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

beforeEach(() => {
  localStorage.clear();
  resetPrefsStoreForTests();
});

afterEach(cleanup);

function turn(overrides: Partial<TurnModel> = {}): TurnModel {
  return { id: "turn_1", status: "completed", items: [], ...overrides };
}

// Turn all three segment prefs on - the default is off for each (see
// prefs.ts), so the data-shape tests below need them enabled to have
// anything to assert on at all.
function enableAllSegments(): void {
  prefsStore.getState().setTranscriptStatus("roundTimings", true);
  prefsStore.getState().setTranscriptStatus("tokenCounts", true);
  prefsStore.getState().setShowCost(true);
}

// Not registered via registerItemRenderer - this is a per-TURN concept, not
// a per-item one, so it's exercised by rendering it directly with a
// TurnModel rather than through itemRendererFor/TurnBlock. See the wave-4
// T2 report for the exact wiring TurnBlock.tsx needs at merge.

test("a turn with none of duration/usage/cost renders nothing (an in-progress turn has nothing real to show yet)", () => {
  enableAllSegments();
  const { container } = render(<TurnSeparator turn={turn()} />);
  expect(container.firstChild).toBeNull();
});

test("renders the duration alone when only durationMs is present", () => {
  enableAllSegments();
  render(<TurnSeparator turn={turn({ durationMs: 1500 })} />);
  expect(screen.getByTestId("turn-separator").textContent).toBe("1.5s");
});

test("renders duration, tokens, and cost joined with the standard separator", () => {
  enableAllSegments();
  render(
    <TurnSeparator turn={turn({ durationMs: 65432, usage: { inputTokens: 500, outputTokens: 120 }, cost: "$1.00" })} />,
  );
  expect(screen.getByTestId("turn-separator").textContent).toBe("65s · ↑500 ↓120 · $1.00");
});

test("omits a missing segment rather than showing a placeholder for it (fields may be absent)", () => {
  enableAllSegments();
  render(<TurnSeparator turn={turn({ usage: { inputTokens: 10, outputTokens: 5 } })} />);
  expect(screen.getByTestId("turn-separator").textContent).toBe("↑10 ↓5");
});

test("cost alone (a source that reports cost but not duration/usage) still renders", () => {
  enableAllSegments();
  render(<TurnSeparator turn={turn({ cost: "$0.02" })} />);
  expect(screen.getByTestId("turn-separator").textContent).toBe("$0.02");
});

// --- preference gating: all three segments are opt-in.

const fullTurn = () => turn({ durationMs: 65432, usage: { inputTokens: 500, outputTokens: 120 }, cost: "$1.00" });

test("a turn carrying all three figures renders no row at all with every pref at its default (off)", () => {
  const { container } = render(<TurnSeparator turn={fullTurn()} />);
  expect(container.firstChild).toBeNull();
  expect(screen.queryByTestId("turn-separator")).toBeNull();
});

test("Round timings on alone shows only the duration - no leading or trailing separator dot", () => {
  prefsStore.getState().setTranscriptStatus("roundTimings", true);
  render(<TurnSeparator turn={fullTurn()} />);
  expect(screen.getByTestId("turn-separator").textContent).toBe("65s");
});

test("Token counts on alone shows only the token counts", () => {
  prefsStore.getState().setTranscriptStatus("tokenCounts", true);
  render(<TurnSeparator turn={fullTurn()} />);
  expect(screen.getByTestId("turn-separator").textContent).toBe("↑500 ↓120");
});

test("Show estimated cost on alone shows only the cost", () => {
  prefsStore.getState().setShowCost(true);
  render(<TurnSeparator turn={fullTurn()} />);
  expect(screen.getByTestId("turn-separator").textContent).toBe("$1.00");
});

test("a suppressed MIDDLE segment leaves no doubled separator between the two that remain", () => {
  prefsStore.getState().setTranscriptStatus("roundTimings", true);
  prefsStore.getState().setShowCost(true);
  render(<TurnSeparator turn={fullTurn()} />);
  expect(screen.getByTestId("turn-separator").textContent).toBe("65s · $1.00");
});

test("each segment is gated by its own pref - one on does not drag the others in", () => {
  prefsStore.getState().setTranscriptStatus("tokenCounts", true);
  render(<TurnSeparator turn={fullTurn()} />);
  const text = screen.getByTestId("turn-separator").textContent ?? "";
  expect(text).not.toContain("65s");
  expect(text).not.toContain("$1.00");
});

test("flipping a pref re-renders an already-mounted separator live (a Settings toggle takes effect immediately)", () => {
  render(<TurnSeparator turn={fullTurn()} />);
  expect(screen.queryByTestId("turn-separator")).toBeNull();

  act(() => {
    prefsStore.getState().setShowCost(true);
  });
  expect(screen.getByTestId("turn-separator").textContent).toBe("$1.00");

  act(() => {
    prefsStore.getState().setTranscriptStatus("roundTimings", true);
  });
  expect(screen.getByTestId("turn-separator").textContent).toBe("65s · $1.00");

  act(() => {
    prefsStore.getState().setShowCost(false);
  });
  expect(screen.getByTestId("turn-separator").textContent).toBe("65s");
});

test("the unrelated transcript toggles do not switch any segment on", () => {
  prefsStore.getState().setTranscriptStatus("hookExitsAll", true);
  prefsStore.getState().setTranscriptStatus("promptLoaded", true);
  render(<TurnSeparator turn={fullTurn()} />);
  expect(screen.queryByTestId("turn-separator")).toBeNull();
});

// --- t8nc: duration carries a visible attachment, like its siblings -------
// Tokens self-label via "↑/↓"; cost self-labels via "$". Duration alone was
// a bare number with no such attachment, so when Round timings is the only
// segment on, the row was just "10s" sitting there unattached to anything.
// A small icon (not text, so the .textContent assertions above - and every
// wire format this row promises - stay byte-for-byte unchanged) attaches it.

test("Round timings alone: the duration segment carries a visible icon, not just a bare number", () => {
  prefsStore.getState().setTranscriptStatus("roundTimings", true);
  const { container } = render(<TurnSeparator turn={turn({ durationMs: 10000 })} />);
  const row = screen.getByTestId("turn-separator");
  expect(row.textContent).toBe("10s"); // wire format is unchanged
  expect(container.querySelector("svg")).toBeTruthy(); // but it's not bare
});

test("the duration icon is decorative only (aria-hidden), never read twice by a screen reader", () => {
  prefsStore.getState().setTranscriptStatus("roundTimings", true);
  const { container } = render(<TurnSeparator turn={turn({ durationMs: 10000 })} />);
  expect(container.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
});
