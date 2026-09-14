import { describe, expect, test } from "vitest";
import { watchDurationLabel, watchNextFireLabel } from "./watchText";

// The label is derived from ONE whole-second total so a remainder can never
// round up into a unit that is then printed next to the un-carried quotient.
// Before the fix, 119.6s printed "1m60s": the minutes were floored to 1 while
// the seconds rounded to 60.
describe("watchDurationLabel", () => {
  test("carries a rounded remainder into the larger unit instead of printing 60", () => {
    expect(watchDurationLabel(119.6)).toBe("2m");
    expect(watchDurationLabel(3599.6)).toBe("1h");
    expect(watchDurationLabel(86399.6)).toBe("1d");
  });

  test("floors each unit from the same whole-second total", () => {
    expect(watchDurationLabel(90)).toBe("1m30s");
    expect(watchDurationLabel(119)).toBe("1m59s");
    expect(watchDurationLabel(3599)).toBe("59m59s");
    // 59.6 used to print "60s", a second count at its own unit's boundary.
    expect(watchDurationLabel(59.6)).toBe("1m");
  });

  test("never emits a remainder at or above its unit", () => {
    for (const seconds of [59.6, 119.6, 3599.6, 86399.6, 60, 3600, 86400, 7199.6]) {
      const label = watchDurationLabel(seconds);
      expect(label).not.toMatch(/\b60s\b/);
      expect(label).not.toMatch(/m60s/);
      expect(label).not.toMatch(/\b60m\b/);
      expect(label).not.toMatch(/h60m/);
      expect(label).not.toMatch(/\b24h\b/);
      expect(label).not.toMatch(/d24h/);
    }
  });

  test("keeps the documented boundaries and empty input", () => {
    expect(watchDurationLabel(undefined)).toBe("");
    expect(watchDurationLabel(0)).toBe("");
    expect(watchDurationLabel(30)).toBe("30s");
    expect(watchDurationLabel(600)).toBe("10m");
    expect(watchDurationLabel(3600)).toBe("1h");
    expect(watchDurationLabel(86400)).toBe("1d");
  });

  test("a sub-second duration rounds to nothing, not a misleading 0s", () => {
    // Anything under half a second rounds to a whole 0, which must read as
    // "nothing here" rather than "0s" (see watchNextFireLabel, which would
    // otherwise render "next ~0s" for a fire a few hundred ms away).
    expect(watchDurationLabel(0.4)).toBe("");
    // From half a second the rounded total is a real second and stays honest.
    expect(watchDurationLabel(0.5)).toBe("1s");
  });
});

describe("watchNextFireLabel", () => {
  const now = Date.parse("2026-09-12T10:00:00.000Z");

  test("a derived fire a few hundred milliseconds away renders no countdown", () => {
    expect(watchNextFireLabel({ kind: "every", derived_next_fire_at: "2026-09-12T10:00:00.400Z" }, now)).toBe("");
  });

  test("a derived fire rounds to an honest whole-second approximation", () => {
    expect(watchNextFireLabel({ kind: "every", derived_next_fire_at: "2026-09-12T10:00:00.600Z" }, now)).toBe(
      "next ~1s",
    );
    expect(watchNextFireLabel({ kind: "after", derived_next_fire_at: "2026-09-12T10:00:04.000Z" }, now)).toBe(
      "next ~4s",
    );
  });

  test("an already-past fire renders no countdown", () => {
    expect(watchNextFireLabel({ kind: "every", derived_next_fire_at: "2026-09-12T09:59:59.000Z" }, now)).toBe("");
  });

  test("a non-clock cadence never renders a countdown", () => {
    expect(watchNextFireLabel({ kind: "output", derived_next_fire_at: "2026-09-12T10:00:04.000Z" }, now)).toBe("");
  });
});
