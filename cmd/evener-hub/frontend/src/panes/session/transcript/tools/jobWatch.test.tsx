// @vitest-environment jsdom

// job_watch tool descriptor tests (mockups 23-job-watch §A-D).
//
// TDD RED: this file is written first, against the generic job_* fallback
// still registered in jobTools.tsx. Every summary expectation below names
// the approved per-operation rendering, so each test fails until jobWatch.tsx
// registers its exact-match "job_watch" descriptor (exact matches win over
// the family predicate per toolRenderers.ts).
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel } from "../../../../protocol/model";
import { toolRendererFor } from "../toolRenderers";
import "../tools";
import "./jobWatch";

afterEach(() => {
  cleanup();
});

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

function watchItem(args: Record<string, unknown>, raw: unknown, output = ""): ItemModel {
  return item({ toolName: "job_watch", argumentsJSON: JSON.stringify(args), raw, output });
}

// The hero timer note from the live session in the mockups.
const TIMER_NOTE =
  "Check CI34049074976 current published ec738c514a35b604338c5346f98b661d62910623 PR822 and fresh RoboRev. " +
  "Lastcomment1f935 boundaryfindingfixedec738. USER says job-shell fuzz failure belongs separate PR; no agent edits; " +
  "diagnosis retained. Task41 only CI/review monitoring remaining, no full local tests/races.";

const TIMER_RAW = {
  watch_id: "watch_034KEfjYFbfoUaPeHJcLXY",
  source: "self",
  watching: true,
  after_seconds: 300,
  note: TIMER_NOTE,
  replaced_existing: false,
  fired: false,
};

// --- §A: create timer -----------------------------------------------------

test("create timer summary humanizes after_seconds and heads the note", () => {
  const d = toolRendererFor("job_watch");
  expect(d.summary(watchItem({ operation: "create" }, TIMER_RAW))).toBe(
    "Remind me in 5m · Check CI34049074976 current published ec738c514a…",
  );
});

test("create timer body renders the full note as prose with no disclosure at this length", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(<Body item={watchItem({ operation: "create" }, TIMER_RAW)} live={false} />);
  expect(screen.getByTestId("job-watch-note").textContent).toContain("Task41 only CI/review monitoring");
  expect(screen.queryByText("Show full note")).toBeNull();
});

test("a note over ~20 lines clamps behind an honest disclosure", () => {
  const longNote = Array.from({ length: 25 }, (_, i) => `note line ${i + 1}`).join("\n");
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem({ operation: "create" }, { ...TIMER_RAW, note: longNote, after_seconds: 60 })}
      live={false}
    />,
  );
  expect(screen.getByText("Show full note")).toBeTruthy();
  expect(screen.getByTestId("job-watch-note").textContent).toContain("note line 25");
});

test("the single-watch body never shows the watch id", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(<Body item={watchItem({ operation: "create" }, TIMER_RAW)} live={false} />);
  expect(screen.getByTestId("job-watch-body").textContent).not.toContain("watch_034KEfjYFbfoUaPeHJcLXY");
});

// --- §B: create condition watch -------------------------------------------

const CONDITION_RAW = {
  watch_id: "watch_09QmWzRtNvxK",
  source: "job_a1b2",
  watching: true,
  output_match: "ready|done",
  events: ["job.notification"],
  progress_interval_ms: 120000,
  replaced_existing: false,
  fired: false,
};

test("create condition summary humanizes progress_interval_ms and names the pattern", () => {
  const d = toolRendererFor("job_watch");
  expect(d.summary(watchItem({ operation: "create" }, CONDITION_RAW))).toContain("job_a1b2");
  expect(d.summary(watchItem({ operation: "create" }, CONDITION_RAW))).toContain("ready|done");
  expect(d.summary(watchItem({ operation: "create" }, CONDITION_RAW))).toContain("every 2m");
  expect(d.summary(watchItem({ operation: "create" }, CONDITION_RAW))).not.toContain("120000");
});

test("create condition body is one humanized sentence with no raw field names", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(<Body item={watchItem({ operation: "create" }, CONDITION_RAW)} live={false} />);
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("job_a1b2");
  expect(body).toContain("ready|done");
  expect(body).toContain("every 2m");
  expect(body).not.toContain("progress_interval_ms");
  expect(body).not.toContain("output_match:");
});

test("event-filter watches name the failing tool-call shape", () => {
  const raw = {
    watch_id: "watch_ev1",
    source: "dlg_7Hk2",
    watching: true,
    events: ["assistant.tool"],
    event_filter: { status: "error" },
    replaced_existing: false,
    fired: false,
  };
  const d = toolRendererFor("job_watch");
  expect(d.summary(watchItem({ operation: "create" }, raw))).toContain("dlg_7Hk2");
  const Body = d.body!;
  render(<Body item={watchItem({ operation: "create" }, raw)} live={false} />);
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("dlg_7Hk2");
  expect(body).toContain("error");
});

// --- §C: list + inspect ----------------------------------------------------

const LIST_RAW = {
  watches: [
    {
      watch_id: "watch_034KEfjYFbfoUaPeHJcLXY",
      source: "self",
      watching: true,
      condition: "after_seconds: 300",
    },
    {
      watch_id: "watch_09QmWzRtNvxK",
      source: "job_a1b2",
      watching: true,
      condition: "output_match: ready|done; progress_interval_ms: 120000",
    },
  ],
  recent_watches: [{ watch_id: "watch_51BdeNpV2sSr", watching: false, end_reason: "budget_exhausted" }],
  count: 2,
};

test("list summary counts active vs ended watches", () => {
  const d = toolRendererFor("job_watch");
  expect(d.summary(watchItem({ operation: "list" }, LIST_RAW))).toBe("Listed watches (2 active · 1 ended)");
});

test("list body renders one row per watch with a status chip and the watch id", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(<Body item={watchItem({ operation: "list" }, LIST_RAW)} live={false} />);
  const rows = screen.getAllByTestId("job-watch-row");
  expect(rows).toHaveLength(3);
  const text = screen.getByTestId("job-watch-body").textContent ?? "";
  // Long ids clip in the row (mockup §C truncates them) with the full id on
  // the hover title; the short id renders whole.
  expect(text).toContain("watch_034KEfj…foUaPeHJcLXY");
  expect(screen.getByTitle("watch_034KEfjYFbfoUaPeHJcLXY")).toBeTruthy();
  expect(text).toContain("watch_09QmWzRtNvxK");
  expect(text).toContain("watching");
  expect(text).toContain("ended");
});

const INSPECT_RAW = {
  watch_id: "watch_09QmWzRtNvxK",
  source: "job_a1b2",
  watching: true,
  condition: "output_match: ready|done; progress_interval_ms: 120000",
  deliveries: 3,
  created_at: "2026-09-06T09:41:00-07:00",
};

test("inspect summary names the id, watching state, and deliveries used", () => {
  const d = toolRendererFor("job_watch");
  expect(d.summary(watchItem({ operation: "inspect", watch_id: "watch_09QmWzRtNvxK" }, INSPECT_RAW))).toBe(
    "Inspected watch_09QmWzRtNvxK · watching · 3 of 50 used",
  );
});

test("inspect body is one sentence with the source, pattern, and budget use", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(<Body item={watchItem({ operation: "inspect", watch_id: "watch_09QmWzRtNvxK" }, INSPECT_RAW)} live={false} />);
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("job_a1b2");
  expect(body).toContain("ready|done");
  expect(body).toContain("3 of 50");
});

// --- §D: clear + terminal catch-up -----------------------------------------

test("clear summary names the cleared watch id", () => {
  const d = toolRendererFor("job_watch");
  const cleared = { watch_id: "watch_034KEfjYFbfoUaPeHJcLXY", source: "", watching: false };
  // The long id clips (mockup §D truncates it); a short id renders whole.
  expect(d.summary(watchItem({ operation: "clear", watch_id: "watch_034KEfjYFbfoUaPeHJcLXY" }, cleared))).toBe(
    "Cleared watch_034KEfj…foUaPeHJcLXY",
  );
  expect(
    d.summary(watchItem({ operation: "clear", watch_id: "watch_short" }, { ...cleared, watch_id: "watch_short" })),
  ).toContain("Cleared");
});

test("clear body is empty: the summary line is the rendering", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  const { container } = render(
    <Body
      item={watchItem(
        { operation: "clear", watch_id: "watch_034KEfjYFbfoUaPeHJcLXY" },
        { watch_id: "watch_034KEfjYFbfoUaPeHJcLXY", source: "", watching: false },
      )}
      live={false}
    />,
  );
  expect(container.textContent?.trim() ?? "").toBe("");
});

test("terminal catch-up summary names the terminal outcome", () => {
  const d = toolRendererFor("job_watch");
  const catchup = { source: "job_a1b2", watching: false, terminal_catchup: true, fired: false, status: "completed" };
  const summary = d.summary(watchItem({ operation: "create" }, catchup));
  expect(summary).toContain("job_a1b2");
  expect(summary).toContain("ended");
  expect(summary).toContain("completed");
});

test("terminal catch-up body is empty: the summary line is the rendering", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  const { container } = render(
    <Body
      item={watchItem(
        { operation: "create" },
        { source: "job_a1b2", watching: false, terminal_catchup: true, fired: false, status: "completed" },
      )}
      live={false}
    />,
  );
  expect(container.textContent?.trim() ?? "").toBe("");
});

// --- humanized durations ----------------------------------------------------

test("after 60s reads as one minute and sub-minute stays in seconds", () => {
  const d = toolRendererFor("job_watch");
  const oneMinute = watchItem({ operation: "create" }, { ...TIMER_RAW, after_seconds: 60, note: "ping" });
  expect(d.summary(oneMinute)).toContain("in 1m");
  const halfMinute = watchItem({ operation: "create" }, { ...TIMER_RAW, after_seconds: 45, note: "ping" });
  expect(d.summary(halfMinute)).toContain("in 45s");
});
