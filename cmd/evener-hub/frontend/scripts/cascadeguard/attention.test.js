// @vitest-environment jsdom

// The cascadeguard attention journey leaves the reader away from the end with a
// real native wheel before it waits for the New Content pill. A single changed
// scrollTop is not that reading position: a virtualizer correction after rows
// measured at zero height can move the offset and still leave the reader at the
// end. `sustainedAwayExpr` is the exact expression run.mjs polls, and
// `wheelReaderAway` is the exact step the journey awaits, so these fixtures
// fail here instead of as a 15s pill timeout.
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { AWAY_HOLD_MS, sustainedAwayExpr, wheelReaderAway } from "./attention.mjs";

let now = 0;

beforeEach(() => {
  now = 0;
  vi.spyOn(performance, "now").mockImplementation(() => now);
  delete window.__cascadeAwaySince;
  delete window.__awayPort;
});

afterEach(() => {
  vi.restoreAllMocks();
  delete window.__cascadeAwaySince;
  delete window.__awayPort;
});

const advance = (ms) => {
  now += ms;
};

function port({ scrollTop, scrollHeight = 200, clientHeight = 100, width = 100, height = 100 } = {}) {
  window.__awayPort = {
    scrollTop,
    scrollHeight,
    clientHeight,
    getBoundingClientRect: () => ({ x: 0, y: 0, left: 0, top: 0, right: width, bottom: height, width, height }),
  };
}

const sample = () => new Function(`return ${sustainedAwayExpr("window.__awayPort")}`)();

test("a changed scrollTop is not away until the end geometry holds for the window", () => {
  // The wheel starts at the end; nothing holds yet.
  port({ scrollTop: 100 });
  expect(sample()).toBeNull();
  // One changed scrollTop is exactly the reading the old wait accepted.
  port({ scrollTop: 50 });
  expect(sample()).toBeNull();
  advance(AWAY_HOLD_MS - 1);
  expect(sample()).toBeNull();
  advance(1);
  expect(sample()).toMatchObject({ scrollTop: 50, heldMs: AWAY_HOLD_MS });
  // Zero-height rows collapse the list and the virtualizer snaps the reader
  // back to the end: the sustained proof resets instead of passing.
  port({ scrollTop: 100 });
  expect(sample()).toBeNull();
  port({ scrollTop: 50 });
  expect(sample()).toBeNull();
  advance(AWAY_HOLD_MS);
  expect(sample()).toMatchObject({ scrollTop: 50 });
});

test("a collapsed or unmounted reader is not an away reading", () => {
  port({ scrollTop: 50, width: 0 });
  expect(sample()).toBeNull();
  port({ scrollTop: 50, height: 0 });
  expect(sample()).toBeNull();
  delete window.__awayPort;
  expect(sample()).toBeNull();
});

test("the wheel step awaits the sustained away proof after the changed offset", async () => {
  port({ scrollTop: 100 });
  const waits = [];
  const wait = vi.fn(async (expression, label) => {
    waits.push({ expression, label });
    if (waits.length === 1) return { x: 5, y: 5, scrollTop: 100 };
    if (waits.length === 2) {
      // The wheel's own default action moves the offset, but the surrounding
      // geometry is still the end: the sustained poll must stay unsatisfied.
      port({ scrollTop: 0, scrollHeight: 100, clientHeight: 100 });
      return true;
    }
    const poll = () => new Function(`return ${expression}`)();
    expect(poll()).toBeNull();
    port({ scrollTop: 50 });
    expect(poll()).toBeNull();
    advance(AWAY_HOLD_MS);
    return poll();
  });
  const driver = { send: vi.fn(async () => ({})) };

  const away = await wheelReaderAway({ driver, wait, portExpr: "window.__awayPort", deltaY: -900 });

  expect(driver.send).toHaveBeenCalledTimes(2);
  expect(waits).toHaveLength(3);
  expect(away).toMatchObject({ scrollTop: 50 });
});
