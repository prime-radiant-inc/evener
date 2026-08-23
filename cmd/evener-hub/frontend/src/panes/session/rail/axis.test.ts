// @vitest-environment node
import { describe, expect, test } from "vitest";
import {
  GAP_MIN_MS,
  indexAt,
  MIN_WINDOW_MS,
  makeAxisParams,
  type RailEvent,
  timeTicks,
  turnTicks,
  vMsY,
  vY,
  vYev,
  vYidx,
} from "./axis";

function makeEvents(count: number, startMs = 0, stepMs = 1000): RailEvent[] {
  const events: RailEvent[] = [];
  for (let i = 0; i < count; i++) {
    events.push({
      pos: i,
      ms: startMs + i * stepMs,
      kind: i % 2 === 0 ? "ASSISTANT" : "TOOL_RESULTS",
      inTok: i === 0 ? 100 : 50,
      outTok: i === 0 ? 200 : 30,
      resBytes: i % 2 === 1 ? 500 : 0,
    });
  }
  return events;
}

describe("indexAt", () => {
  test("returns -1 for empty events", () => {
    expect(indexAt([], 1000)).toBe(-1);
  });

  test("returns -1 for ms before first event", () => {
    const events = makeEvents(5, 1000);
    expect(indexAt(events, 500)).toBe(-1);
  });

  test("returns last index for ms at or after last event", () => {
    const events = makeEvents(5, 1000);
    // events: 1000, 2000, 3000, 4000, 5000. indexAt(4000) = 3 (last <= 4000)
    expect(indexAt(events, 4000)).toBe(3);
    expect(indexAt(events, 99999)).toBe(4);
  });

  test("returns correct index for ms between events", () => {
    const events = makeEvents(5, 1000);
    // events: 1000, 2000, 3000, 4000, 5000. indexAt(1500) = 0 (last <= 1500)
    expect(indexAt(events, 1500)).toBe(0);
    expect(indexAt(events, 2500)).toBe(1);
  });
});

describe("vY (time axis)", () => {
  test("maps start to 0 and end to H", () => {
    // Use timestamps exceeding MIN_WINDOW_MS (10 min) so end = nowMs, not the min window.
    const events = makeEvents(3, 0, 600000);
    const ap = makeAxisParams("time", 0, 1200000, 3);
    const V = { kind: "time" as const, nowMs: 1200000, startMs: 0, ap };
    expect(vY(V, 0, 100, events)).toBeCloseTo(0);
    expect(vY(V, 1200000, 100, events)).toBeCloseTo(100);
  });

  test("is monotonic: increasing ms → increasing y", () => {
    const events = makeEvents(10, 0, 600000);
    const ap = makeAxisParams("time", 0, 6000000, 10);
    const V = { kind: "time" as const, nowMs: 6000000, startMs: 0, ap };
    let prevY = -1;
    for (let ms = 0; ms <= 6000000; ms += 300000) {
      const y = vY(V, ms, 100, events);
      expect(y).toBeGreaterThanOrEqual(prevY);
      prevY = y;
    }
  });

  test("clamps to [0, H]", () => {
    const events = makeEvents(3, 0, 600000);
    const ap = makeAxisParams("time", 0, 1200000, 3);
    const V = { kind: "time" as const, nowMs: 1200000, startMs: 0, ap };
    expect(vY(V, -1000, 100, events)).toBe(0);
    expect(vY(V, 9999999, 100, events)).toBe(100);
  });

  test("min window: when nowMs < start + MIN_WINDOW, axis end = start + MIN_WINDOW", () => {
    const events = makeEvents(3, 0, 1000);
    const ap = makeAxisParams("time", 0, 5000, 3);
    const V = { kind: "time" as const, nowMs: 5000, startMs: 0, ap };
    // end = max(5000, 600000) = 600000; now maps to 5000/600000 * H
    expect(vY(V, 5000, 100, events)).toBeCloseTo((5000 / 600000) * 100, 1);
    // MIN_WINDOW edge maps to 100
    expect(vY(V, 600000, 100, events)).toBeCloseTo(100);
  });
});

describe("vY (turn axis)", () => {
  test("maps index 0 to 0 and last index to H", () => {
    const events = makeEvents(5, 0, 1000);
    const ap = makeAxisParams("turn", 0, 5000, 5);
    const V = { kind: "turn" as const, nowMs: 5000, startMs: 0, ap };
    expect(vYidx(V, 0, 100, events)).toBeCloseTo(0);
    expect(vYidx(V, 4, 100, events)).toBeCloseTo(100);
  });
});

describe("vYev", () => {
  test("turn mode uses event position", () => {
    const events = makeEvents(5, 0, 1000);
    const ap = makeAxisParams("turn", 0, 5000, 5);
    const V = { kind: "turn" as const, nowMs: 5000, startMs: 0, ap };
    const ev = events[2]!;
    expect(vYev(V, ev, 100, events)).toBeCloseTo(50);
  });

  test("time mode uses event timestamp", () => {
    const events = makeEvents(5, 0, 600000);
    const ap = makeAxisParams("time", 0, 2400000, 5);
    const V = { kind: "time" as const, nowMs: 2400000, startMs: 0, ap };
    const ev = events[2]!; // ms = 1200000
    expect(vYev(V, ev, 100, events)).toBeCloseTo(50);
  });
});

describe("vMsY / vIdxY round-trip", () => {
  test("vMsY is the inverse of vY for time axis", () => {
    const events = makeEvents(10, 0, 600000);
    const ap = makeAxisParams("time", 0, 6000000, 10);
    const V = { kind: "time" as const, nowMs: 6000000, startMs: 0, ap };
    for (let ms = 0; ms <= 6000000; ms += 600000) {
      const y = vY(V, ms, 100, events);
      const back = vMsY(V, y, 100, events);
      expect(back).toBeCloseTo(ms, -1);
    }
  });
});

describe("timeTicks", () => {
  test("first tick is at start", () => {
    const start = Date.UTC(2026, 0, 1, 10, 0, 0);
    const end = start + 4 * 3600_000;
    const events = makeEvents(1, start, 1000);
    const ap = makeAxisParams("time", start, end, 1);
    const V = { kind: "time" as const, nowMs: end, startMs: start, ap };
    const ticks = timeTicks(start, end, 200, (ms) => vY(V, ms, 200, events));
    expect(ticks[0]?.y).toBe(0);
  });
});

describe("turnTicks", () => {
  test("generates ticks at round steps", () => {
    const ticks = turnTicks(100, 200);
    expect(ticks.length).toBeGreaterThan(0);
    expect(ticks[0]?.label).toBe("#0");
  });
});

describe("MIN_WINDOW and GAP constants", () => {
  test("MIN_WINDOW_MS is 10 minutes", () => {
    expect(MIN_WINDOW_MS).toBe(10 * 60 * 1000);
  });

  test("GAP_MIN_MS is 10 minutes", () => {
    expect(GAP_MIN_MS).toBe(10 * 60 * 1000);
  });
});
