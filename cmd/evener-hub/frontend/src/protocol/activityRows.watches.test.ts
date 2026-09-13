// Pure-builder tests for the watch row model and the watch-string vocabulary.
// These pin the exact facts/meta shapes the brief specifies, built only from
// real wire fields (never a fabricated instant, name, or count).
import { describe, expect, test } from "vitest";
import { watchCadenceLabel, watchDurationLabel } from "../shell/rail/RailRow";
import { armedWatchCount } from "../shell/rail/railNodes";
import { buildWatchRows, watchFacts, watchIsScheduled, watchMeta, watchName, watchRowID } from "./activityRows";
import { formatClockTime } from "./displayFormat";
import type { NavigationWatchSummary } from "./types.gen";

// Pinned clock: every duration assertion measures against this instant.
const NOW = Date.parse("2026-08-05T15:00:12.000Z");
const CREATED = "2026-08-05T12:48:00Z";

function watch(overrides: Partial<NavigationWatchSummary> = {}): NavigationWatchSummary {
  return {
    id: "watch_1",
    source: "sess_root",
    deliveries: 0,
    created_at: CREATED,
    active: true,
    ...overrides,
  };
}

// armedLabel projects CREATED->NOW through the same helper the panel uses, so
// these assertions never hardcode a timezone-dependent string.
function armedLabel(createdAt = CREATED): string {
  return `${watchDurationLabel((NOW - Date.parse(createdAt)) / 1000)}`;
}

function clock(iso: string): string {
  return formatClockTime(iso) ?? "";
}

describe("watchRowID", () => {
  test("namespaces the watch id so expansion keys never collide with tree rows", () => {
    expect(watchRowID("watch_ab12")).toBe("watch:watch_ab12");
    expect(watchRowID("watch_ab12")).not.toBe("watch_ab12");
  });
});

describe("buildWatchRows", () => {
  test("produces one top-level, default-open row per watch, in wire order", () => {
    const rows = buildWatchRows([watch({ id: "watch_a", note: "First" }), watch({ id: "watch_b", note: "Second" })]);
    expect(rows.map((row) => row.kind)).toEqual(["watch", "watch"]);
    expect(rows.map((row) => row.id)).toEqual(["watch:watch_a", "watch:watch_b"]);
    expect(rows.every((row) => row.level === 1)).toBe(true);
    expect(rows.every((row) => row.defaultDetailOpen)).toBe(true);
  });

  test("an absent list is exactly an empty list", () => {
    expect(buildWatchRows(undefined)).toEqual([]);
    expect(buildWatchRows([])).toEqual([]);
  });

  test("counts only active watches", () => {
    expect(armedWatchCount([watch({ active: true }), watch({ active: false })])).toBe(1);
    expect(armedWatchCount(undefined)).toBe(0);
  });
});

describe("watchName", () => {
  test("prefers the trimmed note", () => {
    expect(watchName(watch({ note: "  Poll the queue depth  " }))).toBe("Poll the queue depth");
  });

  test("falls back to the id the same way the rail's watch row does when the note is absent or blank", () => {
    expect(watchName(watch({ note: undefined, id: "watch_abc" }))).toBe("watch_abc");
    expect(watchName(watch({ note: "   ", id: "watch_abc" }))).toBe("watch_abc");
  });
});

describe("watchMeta", () => {
  test("scheduled with deliveries carries the cadence and delivery count", () => {
    const w = watch({ cadence: [{ kind: "every", seconds: 600 }], deliveries: 3 });
    expect(watchMeta(w)).toBe("every 10m · 3 deliveries");
  });

  test("scheduled with no deliveries carries the armed state", () => {
    const w = watch({ cadence: [{ kind: "every", seconds: 600 }], deliveries: 0, active: true });
    expect(watchMeta(w)).toBe("every 10m · armed");
    expect(watchMeta({ ...w, active: false })).toBe("every 10m · not armed");
  });

  test("a single delivery is singular", () => {
    const w = watch({ cadence: [{ kind: "every", seconds: 600 }], deliveries: 1 });
    expect(watchMeta(w)).toBe("every 10m · 1 delivery");
  });

  test("output watches read on output plus armed state", () => {
    const w = watch({ output_match: "/DONE/", cadence: [{ kind: "output" }], active: true });
    expect(watchMeta(w)).toBe("on output · armed");
    expect(watchMeta({ ...w, active: false })).toBe("on output · not armed");
  });

  test("event watches read on event plus armed state", () => {
    const w = watch({ events: ["job.completed"], cadence: [{ kind: "events" }], active: true });
    expect(watchMeta(w)).toBe("on event · armed");
  });

  test("a watch with an output match and a clock cadence names both", () => {
    const w = watch({
      target: "job_ab12",
      output_match: "/DONE/",
      cadence: [{ kind: "output" }, { kind: "progress", seconds: 10 }],
      active: true,
    });
    // Collapsing to watchKind's first condition used to drop the cadence here,
    // reporting an output-only watch for one that also fires every 10s.
    expect(watchMeta(w)).toBe("on output · every 10s · armed");
  });

  test("a watch carrying both an output match and an event trigger names both conditions", () => {
    // watchKind classifies a multi-trigger watch as a single kind, so an
    // output+event watch used to report only "on output" while watchFacts and
    // the rail's watchGloss both named the event trigger too.
    const w = watch({
      target: "job_ab12",
      output_match: "/DONE/",
      events: ["job.completed"],
      cadence: [{ kind: "output" }, { kind: "events" }],
      active: true,
    });
    expect(watchMeta(w)).toBe("on output · on event · armed");
  });
});

describe("watchFacts", () => {
  test("scheduled with deliveries states cadence, armed age, count, and the newest instant", () => {
    const w = watch({
      cadence: [{ kind: "every", seconds: 600 }],
      deliveries: 3,
      delivery_times: ["2026-08-05T14:50:00Z", "2026-08-05T14:58:00Z"],
    });
    expect(watchFacts(w, NOW)).toBe(
      `Fires every 10m · armed ${armedLabel()} ago · 3 deliveries · last at ${clock("2026-08-05T14:58:00Z")}`,
    );
  });

  test("scheduled with no deliveries says so instead of inventing a last instant", () => {
    const w = watch({ cadence: [{ kind: "after", seconds: 300 }], deliveries: 0 });
    expect(watchFacts(w, NOW)).toBe(
      `Fires ${watchCadenceLabel({ kind: "after", seconds: 300 })} · armed ${armedLabel()} ago · no deliveries yet`,
    );
  });

  test("a single delivery is singular", () => {
    const w = watch({ cadence: [{ kind: "every", seconds: 600 }], deliveries: 1 });
    expect(watchFacts(w, NOW)).toBe(`Fires every 10m · armed ${armedLabel()} ago · 1 delivery`);
  });

  test("output watches name the target and the match", () => {
    const w = watch({
      target: "job_ab12cd",
      output_match: "/DONE/",
      cadence: [{ kind: "output" }],
      deliveries: 0,
    });
    expect(watchFacts(w, NOW)).toBe(
      `Waiting on job_ab12cd, matching /DONE/ · armed ${armedLabel()} ago · no deliveries yet`,
    );
  });

  test("output watches with deliveries report the count", () => {
    const w = watch({
      target: "job_ab12cd",
      output_match: "/DONE/",
      cadence: [{ kind: "output" }],
      deliveries: 4,
      delivery_times: ["2026-08-05T14:58:00Z"],
    });
    expect(watchFacts(w, NOW)).toBe(
      `Waiting on job_ab12cd, matching /DONE/ · armed ${armedLabel()} ago · 4 deliveries`,
    );
  });

  test("event watches join the watched event names", () => {
    const w = watch({
      events: ["job.completed", "job.failed"],
      cadence: [{ kind: "events" }],
      deliveries: 2,
    });
    expect(watchFacts(w, NOW)).toBe(`Waiting on job.completed, job.failed · armed ${armedLabel()} ago · 2 deliveries`);
  });

  test("a wildcard event watch reads as session events", () => {
    const w = watch({ wildcard_events: true, events: [], cadence: [{ kind: "events" }], deliveries: 0 });
    expect(watchFacts(w, NOW)).toBe(`Waiting on session events · armed ${armedLabel()} ago · no deliveries yet`);
  });

  test("a multi-trigger watch states every configured condition, not only its first", () => {
    const w = watch({
      target: "job_ab12",
      output_match: "/DONE/",
      cadence: [{ kind: "output" }, { kind: "progress", seconds: 10 }],
      deliveries: 0,
    });
    expect(watchFacts(w, NOW)).toBe(
      `Waiting on job_ab12, matching /DONE/ · Fires every 10s · armed ${armedLabel()} ago · no deliveries yet`,
    );
  });

  test("never overstates an inactive watch as armed", () => {
    const w = watch({ cadence: [{ kind: "every", seconds: 600 }], deliveries: 1, active: false });
    expect(watchFacts(w, NOW)).toBe("Fires every 10m · not armed · 1 delivery");
  });
});

describe("watchIsScheduled", () => {
  test("is true only for clock-driven watches", () => {
    expect(watchIsScheduled(watch({ cadence: [{ kind: "every", seconds: 600 }] }))).toBe(true);
    expect(watchIsScheduled(watch({ cadence: [{ kind: "after", seconds: 60 }] }))).toBe(true);
    expect(watchIsScheduled(watch({ cadence: [{ kind: "progress", seconds: 30 }] }))).toBe(true);
    expect(watchIsScheduled(watch({ output_match: "/x/", cadence: [{ kind: "output" }] }))).toBe(false);
    expect(watchIsScheduled(watch({ events: ["a"], cadence: [{ kind: "events" }] }))).toBe(false);
  });

  test("a watch that also has a clock cadence is scheduled even with an output condition", () => {
    // This is the case the no-schedule line is false for: it has a real period
    // and a real timeline, so it must not claim there is no schedule to draw.
    expect(
      watchIsScheduled(watch({ output_match: "/x/", cadence: [{ kind: "output" }, { kind: "every", seconds: 600 }] })),
    ).toBe(true);
    expect(
      watchIsScheduled(watch({ events: ["a"], cadence: [{ kind: "events" }, { kind: "after", seconds: 60 }] })),
    ).toBe(true);
  });
});

describe("forbidden vocabulary", () => {
  test("no rendered watch string mentions drops, delivery completeness, next fire, or a countdown", () => {
    const strings = [
      watchMeta(watch({ cadence: [{ kind: "every", seconds: 600 }], deliveries: 3 })),
      watchFacts(watch({ cadence: [{ kind: "every", seconds: 600 }], deliveries: 3 }), NOW),
      watchFacts(watch({ output_match: "/x/", target: "j", cadence: [{ kind: "output" }], deliveries: 0 }), NOW),
      watchFacts(watch({ events: ["a"], cadence: [{ kind: "events" }], deliveries: 0 }), NOW),
    ].join("\n");
    expect(strings).not.toMatch(/dropped/i);
    expect(strings).not.toMatch(/all delivered/i);
    expect(strings).not.toMatch(/\bnext\b/i);
    expect(strings).not.toMatch(/countdown/i);
  });
});
