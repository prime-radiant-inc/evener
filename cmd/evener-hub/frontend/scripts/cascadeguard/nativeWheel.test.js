// @vitest-environment node

// `readInsideTallEntry` repositions the source reader with a genuine native
// wheel before an interruption measurement. An upward wheel needs a full step
// of room to apply; a reader closer to the top than that clamps at the
// boundary and leaves the reading line at the entry's edge. These cases pin
// the rule that a repositioning wheel always points at the end that has room.
//
// `run.mjs` cannot be imported to check the call site itself: it reads the
// fixture from stdin at module load, so the last case pins that call site in
// its source text. The retry composition is exercised through its own
// behavior tests below.
import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import { readerWheelDelta, repositionReader } from "./nativeWheel.mjs";

const RUN_MJS = new URL("./run.mjs", import.meta.url);

// The failing run's own settled point (`reading-attempt.json`): the reader is
// at the top of a scrollport with 11,755px of content.
const RECORDED_TOP = { scrollTop: 0, scrollHeight: 11755, clientHeight: 657 };
// A re-render had parked the same reader 92px down when its wheel was chosen;
// the virtualizer reset it to the top before the wheel event landed.
const RECORDED_NEAR_TOP = { scrollTop: 92, scrollHeight: 10569, clientHeight: 657 };
// The same run's preceding wheel interruption, parked inside a tall entry.
const RECORDED_INTERIOR = { scrollTop: 4707, scrollHeight: 24026, clientHeight: 644 };
// A bottom follower at the end of the same scrollport.
const RECORDED_END = { scrollTop: 11098, scrollHeight: 11755, clientHeight: 657 };

test("a reader with less than a full wheel step above it scrolls down", () => {
  expect(readerWheelDelta(RECORDED_TOP)).toBe(900);
  expect(readerWheelDelta(RECORDED_NEAR_TOP)).toBe(900);
});

test("a reader away from the top keeps the established upward gesture", () => {
  expect(readerWheelDelta(RECORDED_INTERIOR)).toBe(-900);
  expect(readerWheelDelta(RECORDED_END)).toBe(-900);
});

test("a wheel that fails outright is retried and the retry's point is returned", async () => {
  let wheels = 0;
  const result = await repositionReader({
    wheel: async () => {
      wheels += 1;
      if (wheels === 1) throw new Error("the wheel did not move the reader");
    },
    tallEntry: async () => ({ entry: "retried" }),
  });
  expect(result).toEqual({ entry: "retried" });
  expect(wheels).toBe(2);
});

test("a wheel that settles outside tall content is retried", async () => {
  let samples = 0;
  const result = await repositionReader({
    wheel: async () => {},
    tallEntry: async () => {
      samples += 1;
      if (samples === 1) throw new Error("the reading point stayed at the entry edge");
      return "inside a tall entry";
    },
  });
  expect(result).toBe("inside a tall entry");
  expect(samples).toBe(2);
});

test("every attempt failing throws the last failure", async () => {
  let wheels = 0;
  await expect(repositionReader({
    wheel: async () => {
      wheels += 1;
      throw new Error(`swallowed wheel ${wheels}`);
    },
    tallEntry: async () => "never sampled",
  })).rejects.toThrow("swallowed wheel 3");
});

test("a violated input guarantee is not retried", async () => {
  let wheels = 0;
  const violation = new Error("native wheel targets its intended reader");
  violation.code = "ERR_ASSERTION";
  await expect(repositionReader({
    wheel: async () => {
      wheels += 1;
      throw violation;
    },
    tallEntry: async () => "never sampled",
  })).rejects.toBe(violation);
  expect(wheels).toBe(1);
});

test("one budget covers every attempt", async () => {
  let now = 0;
  let wheels = 0;
  await expect(repositionReader({
    now: () => now,
    budgetMs: 100,
    wheel: async () => {
      wheels += 1;
      now += 60;
      throw new Error("swallowed wheel");
    },
    tallEntry: async () => "never sampled",
  })).rejects.toThrow("swallowed wheel");
  expect(wheels).toBe(2);
});

test("the sampling wait gets only the budget the wheel left", async () => {
  let now = 0;
  let sampleTimeout;
  const result = await repositionReader({
    now: () => now,
    budgetMs: 6000,
    attemptTimeoutMs: 5000,
    wheel: async () => {
      now += 3000;
    },
    tallEntry: async (timeoutMs) => {
      sampleTimeout = timeoutMs;
      return "inside a tall entry";
    },
  });
  expect(result).toBe("inside a tall entry");
  expect(sampleTimeout).toBe(3000);
});

test("the tall-entry gesture is retried as a unit at the call site", () => {
  const source = readFileSync(RUN_MJS, "utf8");
  expect(source).toContain('import { readerWheelDelta, repositionReader } from "./nativeWheel.mjs";');
  expect(source).toContain(
    "wheel: (timeoutMs) => nativeReaderWheel(portExpr, readerWheelDelta, { timeoutMs })",
  );
  expect(source).not.toContain("await nativeReaderWheel(portExpr, -900);");
});
