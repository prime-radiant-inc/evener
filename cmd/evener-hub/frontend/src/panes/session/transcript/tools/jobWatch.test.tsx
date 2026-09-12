// @vitest-environment jsdom

// job_watch tool descriptor tests (mockups 23-job-watch §A-D).
// TDD RED: this file is written first, against the generic job_* fallback
// still registered in jobTools.tsx. Every summary expectation below names
// the approved per-operation rendering, so each test fails until jobWatch.tsx
// registers its exact-match "job_watch" descriptor (exact matches win over
// the family predicate per toolRenderers.ts).
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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

test("list rows are buttons that expand the row's detail sentence (RoboRev PR #954)", async () => {
  const user = userEvent.setup();
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(<Body item={watchItem({ operation: "list" }, LIST_RAW)} live={false} />);
  const rows = screen.getAllByTestId("job-watch-row");
  expect(rows).toHaveLength(3);
  // Only rows that CAN expand are buttons; the ended row is plain text
  // (RoboRev PR #954 combined review: no focusable no-op controls).
  expect(rows[0]!.tagName).toBe("BUTTON");
  expect(rows[1]!.tagName).toBe("BUTTON");
  expect(rows[2]!.tagName).not.toBe("BUTTON");
  // Detail hidden until tapped.
  expect(screen.queryByTestId("job-watch-row-detail")).toBeNull();
  await user.click(rows[1]!);
  const detail = screen.getByTestId("job-watch-row-detail").textContent ?? "";
  expect(detail).toContain("job_a1b2");
  expect(detail).toContain("ready|done");
  await user.click(rows[1]!);
  expect(screen.queryByTestId("job-watch-row-detail")).toBeNull();
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
    "Inspected watch_09QmWzRtNvxK · watching · 3 deliveries",
  );
});

test("inspect body is one sentence with the source, pattern, and budget use", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(<Body item={watchItem({ operation: "inspect", watch_id: "watch_09QmWzRtNvxK" }, INSPECT_RAW)} live={false} />);
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("job_a1b2");
  expect(body).toContain("ready|done");
  expect(body).toContain("3 deliveries");
});

test("inspect body humanizes the embedded heartbeat instead of raw milliseconds (RoboRev PR #954)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(<Body item={watchItem({ operation: "inspect", watch_id: "watch_09QmWzRtNvxK" }, INSPECT_RAW)} live={false} />);
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("every 2m");
  expect(body).not.toContain("120000");
});

test("inspect body renders embedded events, every throttle, and filter in words (RoboRev PR #954)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_ev" },
        {
          watch_id: "watch_ev",
          source: "dlg_7Hk2",
          watching: true,
          condition: "events: [assistant.tool] every 3 where tool_name=read_file, status=error",
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("dlg_7Hk2");
  expect(body).toContain("assistant.tool");
  expect(body).not.toContain("events: [assistant.tool]");
  expect(body).not.toContain("where tool_name=");
});

test("inspect body humanizes a repeating timer condition (RoboRev PR #954)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_rep" },
        { watch_id: "watch_rep", source: "self", watching: true, condition: "repeat_seconds: 300" },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("every 5m");
  expect(body).not.toContain("repeat_seconds");
});

test("list rows humanize repeating timers and event conditions (RoboRev PR #954)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "list" },
        {
          watches: [
            { watch_id: "watch_rep", source: "self", watching: true, condition: "repeat_seconds: 90" },
            {
              watch_id: "watch_ev",
              source: "dlg_7Hk2",
              watching: true,
              condition: "events: [assistant.tool] every 3 where tool_name=read_file, status=error",
            },
          ],
          count: 2,
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("every 1m30s");
  expect(body).not.toContain("repeat_seconds");
  expect(body).not.toContain("where tool_name=");
});

test("the every throttle survives in create summaries and bodies (RoboRev PR #954, review 3)", () => {
  const d = toolRendererFor("job_watch");
  const args = { operation: "create", source: "dlg_7Hk2", events: ["communicate"], every: 3 };
  const raw = {
    watch_id: "watch_ev3",
    source: "dlg_7Hk2",
    watching: true,
    events: ["communicate"],
    replaced_existing: false,
    fired: false,
  };
  expect(d.summary(watchItem(args, raw))).toContain("(every 3)");
  const Body = d.body!;
  render(<Body item={watchItem(args, raw)} live={false} />);
  expect(screen.getByTestId("job-watch-body").textContent ?? "").toContain("(every 3)");
});

test("a successful-calls filter reads explicitly, not as a bare tool name (RoboRev PR #954, review 3)", () => {
  const d = toolRendererFor("job_watch");
  const args = { operation: "create", source: "dlg_7Hk2" };
  const raw = {
    watch_id: "watch_ok",
    source: "dlg_7Hk2",
    watching: true,
    events: ["assistant.tool"],
    event_filter: { tool_name: "read_file", status: "ok" },
    replaced_existing: false,
    fired: false,
  };
  expect(d.summary(watchItem(args, raw))).toContain("successful tool calls");
  const Body = d.body!;
  render(<Body item={watchItem(args, raw)} live={false} />);
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("ok");
  expect(body).toContain("read_file");
});

test("every list row form expands a detail sentence; no expandable row is a no-op (RoboRev PR #954, review 3)", async () => {
  const user = userEvent.setup();
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "list" },
        {
          watches: [
            { watch_id: "watch_evonly", source: "self", watching: true, condition: "events: [communicate]" },
            {
              watch_id: "watch_okf",
              source: "dlg_7Hk2",
              watching: true,
              condition: "events: [assistant.tool] where tool_name=read_file, status=ok",
            },
            { watch_id: "watch_hb", source: "job_a1b2", watching: true, condition: "progress_interval_ms: 120000" },
          ],
          count: 3,
        },
      )}
      live={false}
    />,
  );
  const rows = screen.getAllByTestId("job-watch-row");
  expect(rows).toHaveLength(3);
  // Every expandable row opens its detail; static rows (none here — all
  // three watch with parseable conditions) are never buttons.
  for (const row of rows) expect(row.tagName).toBe("BUTTON");
  for (const [index, row] of rows.entries()) {
    await user.click(row);
    const details = screen.getAllByTestId("job-watch-row-detail");
    expect(details).toHaveLength(index + 1);
  }
  const text = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(text).toContain("communicate");
  expect(text).toContain("successful tool calls");
  expect(text).toContain("every 2m");
});

test("absent structured state falls back to the raw footer text (RoboRev PR #954)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  const footer = "[watching self · watch_id watch_legacy · after 300s note: hello]";
  render(
    <Body
      item={item({ toolName: "job_watch", argumentsJSON: JSON.stringify({ operation: "create" }), output: footer })}
      live={false}
    />,
  );
  expect(screen.getByText(footer)).toBeTruthy();
  // And the summary still degrades to the operation verb, never a bare name.
  expect(d.summary(item({ toolName: "job_watch", argumentsJSON: JSON.stringify({ operation: "create" }) }))).toBe(
    "job_watch: create",
  );
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

test("leftover seconds are kept, never rounded into the minute (RoboRev PR #954)", () => {
  const d = toolRendererFor("job_watch");
  const ninety = watchItem({ operation: "create" }, { ...TIMER_RAW, after_seconds: 90, note: "ping" });
  expect(d.summary(ninety)).toContain("in 1m30s");
  const repeating = watchItem({ operation: "create" }, { ...TIMER_RAW, after_seconds: undefined, repeat_seconds: 90 });
  expect(d.summary(repeating)).toContain("every 1m30s");
});

// --- RoboRev PR #954 review 3 -----------------------------------------------

test("an output pattern containing a semicolon survives Condition parsing (finding C)", () => {
  // output_match is caller-supplied and unbounded, so it may itself contain
  // "; " — only semicolons introducing a recognized field may split.
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_semi" },
        {
          watch_id: "watch_semi",
          source: "job_a1b2",
          watching: true,
          condition: "output_match: a;b; progress_interval_ms: 120000",
          deliveries: 0,
          created_at: "2026-09-06T09:41:00-07:00",
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("a;b");
  expect(body).toContain("every 2m");
});

test("list rows keep a semicolon-bearing pattern whole (finding C)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "list" },
        {
          watches: [
            {
              watch_id: "watch_semi",
              source: "job_a1b2",
              watching: true,
              condition: "output_match: a;b; progress_interval_ms: 120000",
            },
          ],
          count: 1,
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("a;b");
  expect(body).toContain("every 2m");
});

test("a filter condition's every throttle renders in the list row (finding D)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "list" },
        {
          watches: [
            {
              watch_id: "watch_f",
              source: "dlg_7Hk2",
              watching: true,
              condition: "events: [assistant.tool] every 3 where tool_name=read_file, status=error",
            },
          ],
          count: 1,
        },
      )}
      live={false}
    />,
  );
  expect(screen.getByTestId("job-watch-body").textContent ?? "").toContain("(every 3)");
});

test("a filter condition's every throttle renders in the create sentence (finding D)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "create", source: "dlg_7Hk2", events: ["assistant.tool"], every: 3 },
        {
          watch_id: "watch_f",
          source: "dlg_7Hk2",
          watching: true,
          events: ["assistant.tool"],
          event_filter: { tool_name: "read_file", status: "error" },
          replaced_existing: false,
          fired: false,
        },
      )}
      live={false}
    />,
  );
  expect(screen.getByTestId("job-watch-body").textContent ?? "").toContain("(every 3)");
});

test("a filter condition's every throttle renders in inspect (finding D)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_f" },
        {
          watch_id: "watch_f",
          source: "dlg_7Hk2",
          watching: true,
          condition: "events: [assistant.tool] every 3 where tool_name=read_file, status=error",
        },
      )}
      live={false}
    />,
  );
  expect(screen.getByTestId("job-watch-body").textContent ?? "").toContain("(every 3)");
});

test("list rows name the tool for both filter outcomes (finding E)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "list" },
        {
          watches: [
            {
              watch_id: "watch_ok",
              source: "dlg_7Hk2",
              watching: true,
              condition: "events: [assistant.tool] where tool_name=read_file, status=ok",
            },
            {
              watch_id: "watch_err",
              source: "dlg_7Hk2",
              watching: true,
              condition: "events: [assistant.tool] where tool_name=read_file, status=error",
            },
          ],
          count: 2,
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("successful tool calls on read_file");
  expect(body).toContain("failed tool calls on read_file");
});

test("a progress-only create summarizes the heartbeat (finding F)", () => {
  const d = toolRendererFor("job_watch");
  const raw = {
    watch_id: "watch_hb",
    source: "job_a1b2",
    watching: true,
    progress_interval_ms: 120000,
    replaced_existing: false,
    fired: false,
  };
  expect(d.summary(watchItem({ operation: "create" }, raw))).toBe("Watch job_a1b2 · every 2m");
});

// --- RoboRev PR #954 combined review (ba9a9d0): watch-state honesty ---------
// The producer's inspect grammar (agent/session_tools_jobs.go
// formatJobWatchInspect) is three-way: watching; end_reason set (ended);
// source set without end_reason (pending — a detached watch still holding
// frames); neither (not found). "ended" for all three misreports pending
// watches and invents an ending for missing ones.

test("inspect summary distinguishes pending from ended (finding M1)", () => {
  const d = toolRendererFor("job_watch");
  // Detached pending: source present, no end_reason — the terminal-flush rail
  // still holds its frames, so it is not ended.
  expect(
    d.summary(
      watchItem(
        { operation: "inspect", watch_id: "watch_p" },
        { watch_id: "watch_p", source: "job_a1b2", watching: false },
      ),
    ),
  ).toBe("Inspected watch_p · pending");
  // Truly ended: end_reason present.
  expect(
    d.summary(
      watchItem(
        { operation: "inspect", watch_id: "watch_e" },
        { watch_id: "watch_e", source: "job_a1b2", watching: false, end_reason: "budget_exhausted" },
      ),
    ),
  ).toBe("Inspected watch_e · ended");
  // Missing: neither source nor end_reason — not an ending at all.
  expect(
    d.summary(watchItem({ operation: "inspect", watch_id: "watch_m" }, { watch_id: "watch_m", watching: false })),
  ).toBe("Inspected watch_m · not found");
});

test("inspect body of a missing watch names no source (finding M1)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem({ operation: "inspect", watch_id: "watch_m" }, { watch_id: "watch_m", watching: false })}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("not found");
  expect(body).not.toContain("this session");
});

test("inspect body of a pending watch reads pending, never ended (finding M1)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_p" },
        { watch_id: "watch_p", source: "job_a1b2", watching: false },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("pending");
  expect(body).not.toContain("ended");
});

test("a pending list row chips pending and the summary counts it (finding M1)", () => {
  const d = toolRendererFor("job_watch");
  const raw = {
    watches: [{ watch_id: "watch_p", source: "job_a1b2", watching: false }],
    count: 1,
  };
  expect(d.summary(watchItem({ operation: "list" }, raw))).toBe("Listed watches (0 active · 1 pending)");
  const Body = d.body!;
  render(<Body item={watchItem({ operation: "list" }, raw)} live={false} />);
  expect(screen.getByTestId("job-watch-body").textContent ?? "").toContain("pending");
});

// --- RoboRev PR #954 combined review (ba9a9d0): non-interactive rows --------
// A row that cannot expand must not be a focusable <button> with a no-op
// onClick: ended rows have no detail sentence, and neither do watching rows
// whose condition parses to nothing.

test("ended rows are not focusable buttons (finding M2)", async () => {
  const user = userEvent.setup();
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(<Body item={watchItem({ operation: "list" }, LIST_RAW)} live={false} />);
  const rows = screen.getAllByTestId("job-watch-row");
  expect(rows).toHaveLength(3);
  // The two watching rows stay buttons; the ended row is plain text.
  expect(rows[0]!.tagName).toBe("BUTTON");
  expect(rows[1]!.tagName).toBe("BUTTON");
  expect(rows[2]!.tagName).not.toBe("BUTTON");
  expect(rows[2]!.textContent ?? "").toContain("ended");
  // Clicking the ended row opens nothing.
  await user.click(rows[2]!);
  expect(screen.queryByTestId("job-watch-row-detail")).toBeNull();
});

test("watching rows with unparsable conditions are not buttons (finding M2)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "list" },
        {
          watches: [{ watch_id: "watch_x", source: "self", watching: true, condition: "note: just a note" }],
          count: 1,
        },
      )}
      live={false}
    />,
  );
  const row = screen.getByTestId("job-watch-row");
  expect(row.tagName).not.toBe("BUTTON");
});

// --- combined RoboRev review (6bdc9ed): note-only watching rows ---------------
// A watching row whose condition parses to no trigger bits must never echo
// the raw Condition grammar ("note: just a note · this session"): it names
// the note prose (structured field verbatim, else the parsed note: clause),
// and falls back to the bare source when there is no note at all.

test("a note-only watching row renders the note prose, not the raw Condition grammar", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "list" },
        {
          watches: [{ watch_id: "watch_x", source: "self", watching: true, condition: "note: just a note" }],
          count: 1,
        },
      )}
      live={false}
    />,
  );
  const row = screen.getByTestId("job-watch-row").textContent ?? "";
  expect(row).toContain("just a note");
  expect(row).not.toContain("note: just a note");
});

test("a note-only watching row prefers the structured note field verbatim", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "list" },
        {
          watches: [
            {
              watch_id: "watch_x",
              source: "self",
              watching: true,
              condition: "note: just a note",
              note: "the full note; events: [x] is prose, not a trigger",
            },
          ],
          count: 1,
        },
      )}
      live={false}
    />,
  );
  const row = screen.getByTestId("job-watch-row").textContent ?? "";
  expect(row).toContain("the full note; events: [x] is prose, not a trigger");
  expect(row).not.toContain("note: just a note");
});

test("a watching row with no triggers and no note names only the source", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "list" },
        {
          watches: [{ watch_id: "watch_x", source: "self", watching: true, condition: "bogus grammar here" }],
          count: 1,
        },
      )}
      live={false}
    />,
  );
  const row = screen.getByTestId("job-watch-row").textContent ?? "";
  expect(row).toContain("this session");
  expect(row).not.toContain("bogus grammar here");
});

// --- RoboRev PR #954 combined review (ba9a9d0): lows ------------------------
// An unrecognized object raw ({} or a legacy/future shape) must fall back to
// the raw footer text — never an empty card with a "Watch this session"
// summary that invents state.

test("an unrecognized object raw falls back to the raw footer (finding L2)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  const footer = "[watching self · something the future invented]";
  render(<Body item={watchItem({ operation: "create" }, {}, footer)} live={false} />);
  expect(screen.getByText(footer)).toBeTruthy();
  expect(screen.queryByTestId("job-watch-body")).toBeNull();
  expect(d.summary(watchItem({ operation: "create" }, {}))).toBe("job_watch: create");
});

test("a tool-only filter sentence reads matching with the tool named (finding L3)", () => {
  // The dead ternary's two "matching" branches are collapsed to one path:
  // a tool-only filter reads "makes a tool call matching on <tool>". (A bare
  // filter with neither tool nor status cannot reach this sentence — with no
  // status/tool the events branch wins first, or the row has no detail at
  // all — so there is no second case to distinguish.)
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "create", source: "dlg_7Hk2" },
        {
          watch_id: "watch_toolonly",
          source: "dlg_7Hk2",
          watching: true,
          events: ["assistant.tool"],
          event_filter: { tool_name: "read_file" },
          replaced_existing: false,
          fired: false,
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("matching");
  expect(body).toContain("read_file");
});

test("an inspect with no id falls back to the operation verb (finding L4)", () => {
  const d = toolRendererFor("job_watch");
  expect(d.summary(watchItem({ operation: "inspect" }, { source: "job_a1b2", watching: true }))).toBe(
    "job_watch: inspect",
  );
});

// --- RoboRev combined review (43fe73f): combined triggers (M3) --------------
// output_match + progress_interval_ms combine freely on a concrete job
// (only timer fields are mutually exclusive with conditions). The summary
// and inspect body must name BOTH the pattern and the heartbeat.

test("a combined pattern + heartbeat watch names both (M3)", () => {
  const d = toolRendererFor("job_watch");
  const raw = {
    watch_id: "watch_combo",
    source: "job_a1b2",
    watching: true,
    output_match: "ready|done",
    progress_interval_ms: 120000,
    replaced_existing: false,
    fired: false,
  };
  const summary = d.summary(watchItem({ operation: "create" }, raw));
  expect(summary).toContain("ready|done");
  expect(summary).toContain("every 2m");
  const Body = d.body!;
  render(<Body item={watchItem({ operation: "create" }, raw)} live={false} />);
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("ready|done");
  expect(body).toContain("every 2m");
});

test("a combined pattern + heartbeat inspect names both (M3)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_combo" },
        {
          watch_id: "watch_combo",
          source: "job_a1b2",
          watching: true,
          condition: "output_match: ready|done; progress_interval_ms: 120000",
          deliveries: 1,
          created_at: "2026-09-06T09:41:00-07:00",
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("ready|done");
  expect(body).toContain("every 2m");
});

// --- RoboRev combined review (43fe73f): empty create card (L2) --------------
// A recognized create with only a source (no timer, no condition) has no
// sentence to render — the summary ("Watch this session") IS the rendering,
// so the body must be null, not an empty bordered card.

test("a sourceless-condition create renders no body card (L2)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  const { container } = render(
    <Body
      item={watchItem({ operation: "create" }, { watch_id: "watch_bare", source: "self", watching: true })}
      live={false}
    />,
  );
  expect(container.textContent?.trim() ?? "").toBe("");
});

// --- RoboRev combined review (43fe73f): live ended rows count ended (L3) ----
// A live row carrying an end_reason is ended, not pending — the summary must
// agree with the row chip.

test("a live row with an end_reason counts as ended (L3)", () => {
  const d = toolRendererFor("job_watch");
  const raw = {
    watches: [{ watch_id: "watch_e", source: "job_a1b2", watching: false, end_reason: "cleared" }],
    count: 1,
  };
  expect(d.summary(watchItem({ operation: "list" }, raw))).toBe("Listed watches (0 active · 1 ended)");
  const Body = d.body!;
  render(<Body item={watchItem({ operation: "list" }, raw)} live={false} />);
  expect(screen.getByTestId("job-watch-body").textContent ?? "").toContain("ended");
});

// --- RoboRev combined review (818e809): sentence punctuation (M1) ------------
// Clause nodes must carry no separators of their own — joining inserts them.
// Pattern + heartbeat rendered "outputs ready, , heartbeat every 2m".

test("a combined pattern + heartbeat sentence has no doubled comma (M1)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "create", source: "job_a1b2" },
        {
          watch_id: "watch_combo",
          source: "job_a1b2",
          watching: true,
          output_match: "ready",
          progress_interval_ms: 120000,
          replaced_existing: false,
          fired: false,
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).not.toContain(", ,");
  expect(body).toContain("heartbeat every 2m");
});

test("a filter sentence joins its clauses with spaces, not commas (M1)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "create", source: "dlg_7Hk2", events: ["assistant.tool"], every: 3 },
        {
          watch_id: "watch_f",
          source: "dlg_7Hk2",
          watching: true,
          events: ["assistant.tool"],
          event_filter: { tool_name: "read_file", status: "error" },
          replaced_existing: false,
          fired: false,
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("makes a tool call on read_file ending in error (assistant.tool) (every 3)");
});

// --- RoboRev combined review (818e809): heartbeat-only lifecycle (M2) -------
// Periodic progress ticks never consume the condition-fire budget, so a
// heartbeat-only watch can live indefinitely — claiming it "auto-clears
// after 50 matches" is a false lifecycle guarantee.

test("a heartbeat-only sentence claims no auto-clear (M2)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "create", source: "job_a1b2" },
        {
          watch_id: "watch_hb",
          source: "job_a1b2",
          watching: true,
          progress_interval_ms: 120000,
          replaced_existing: false,
          fired: false,
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("heartbeat every 2m");
  expect(body).not.toContain("auto-clears");
});

test("a budgeted trigger keeps the auto-clear clause (M2)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "create", source: "job_a1b2" },
        {
          watch_id: "watch_combo",
          source: "job_a1b2",
          watching: true,
          output_match: "ready",
          progress_interval_ms: 120000,
          replaced_existing: false,
          fired: false,
        },
      )}
      live={false}
    />,
  );
  expect(screen.getByTestId("job-watch-body").textContent ?? "").toContain("auto-clears after 50 matches");
});

// --- RoboRev combined review (33d5b9a): composite detail views (M1) ---------
// A watch combining output_match with events/filter/every must name every
// armed clause in the inspect body and the expanded row detail — not just
// the pattern and heartbeat.

test("a composite inspect body names events and filter beside the pattern (M1)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_combo" },
        {
          watch_id: "watch_combo",
          source: "job_a1b2",
          watching: true,
          condition: "output_match: ready; events: [assistant.tool] every 3 where tool_name=read_file, status=error",
          deliveries: 1,
          created_at: "2026-09-06T09:41:00-07:00",
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("ready");
  expect(body).toContain("assistant.tool");
  expect(body).toContain("read_file");
  expect(body).toContain("(every 3)");
});

test("a composite row detail names events and filter beside the pattern (M1)", async () => {
  const user = userEvent.setup();
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "list" },
        {
          watches: [
            {
              watch_id: "watch_combo",
              source: "job_a1b2",
              watching: true,
              condition:
                "output_match: ready; events: [assistant.tool] every 3 where tool_name=read_file, status=error",
            },
          ],
          count: 1,
        },
      )}
      live={false}
    />,
  );
  await user.click(screen.getByTestId("job-watch-row"));
  const detail = screen.getByTestId("job-watch-row-detail").textContent ?? "";
  expect(detail).toContain("ready");
  expect(detail).toContain("assistant.tool");
  expect(detail).toContain("read_file");
  expect(detail).toContain("(every 3)");
});

// --- RoboRev combined review (e9113df): sub-second + carry precision ------
// progress_interval_ms is milliseconds: 1500ms must read "every 1.5s", not
// "every 2s", and rounding must never produce a 60s carry ("every 1m60s").

test("a 1500ms heartbeat keeps sub-second precision", () => {
  const d = toolRendererFor("job_watch");
  const raw = {
    watch_id: "watch_ms",
    source: "job_a1b2",
    watching: true,
    progress_interval_ms: 1500,
    replaced_existing: false,
    fired: false,
  };
  expect(d.summary(watchItem({ operation: "create" }, raw))).toBe("Watch job_a1b2 · every 1.5s");
});

test("millisecond heartbeats normalize rounded carry-over", () => {
  const d = toolRendererFor("job_watch");
  const mk = (ms: number) =>
    watchItem({ operation: "create" }, { watch_id: "w", source: "job_a1b2", watching: true, progress_interval_ms: ms });
  expect(d.summary(mk(59500))).toBe("Watch job_a1b2 · every 1m");
  expect(d.summary(mk(119999))).toBe("Watch job_a1b2 · every 2m");
});

// --- RoboRev combined review (322f7aa): condition notes (M1) ----------------
// Any watch can carry a note (backend #995); the structured create card must
// render it alongside the condition sentence, not drop it.

test("a condition create body renders the note alongside the sentence (M1)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "create", source: "job_a1b2" },
        {
          watch_id: "watch_n",
          source: "job_a1b2",
          watching: true,
          output_match: "ready",
          note: "check the build",
          replaced_existing: false,
          fired: false,
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("ready");
  expect(body).toContain("check the build");
});

test("an inspect body renders the embedded note (M1)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_n" },
        {
          watch_id: "watch_n",
          source: "job_a1b2",
          watching: true,
          condition: "output_match: ready; note: check the build",
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("ready");
  expect(body).toContain("check the build");
});

test("a row detail names the embedded note (M1)", async () => {
  const user = userEvent.setup();
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "list" },
        {
          watches: [
            {
              watch_id: "watch_n",
              source: "job_a1b2",
              watching: true,
              condition: "output_match: ready; note: check the build",
            },
          ],
          count: 1,
        },
      )}
      live={false}
    />,
  );
  await user.click(screen.getByTestId("job-watch-row"));
  const detail = screen.getByTestId("job-watch-row-detail").textContent ?? "";
  expect(detail).toContain("ready");
  expect(detail).toContain("check the build");
});

// --- RoboRev combined review (322f7aa): self source label (M3) --------------

test("the self source reads as this session, never verbatim self (M3)", () => {
  const d = toolRendererFor("job_watch");
  // Condition summaries name their source; timers never render one (their
  // summary is cadence + note head), so assert on a condition shape.
  expect(
    d.summary(
      watchItem({ operation: "create" }, { watch_id: "w", source: "self", watching: true, output_match: "ready" }),
    ),
  ).toBe("Watch this session for “ready”");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "list" },
        { watches: [{ watch_id: "w", source: "self", watching: true, condition: "after_seconds: 300" }], count: 1 },
      )}
      live={false}
    />,
  );
  const text = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(text).not.toMatch(/(^|\s)self(\s|$)/);
});

// --- RoboRev combined review (322f7aa): dot-all patterns (L1) --------------

test("a multiline output pattern survives Condition parsing (L1)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_ml" },
        {
          watch_id: "watch_ml",
          source: "job_a1b2",
          watching: true,
          condition: "output_match: line1\nline2; progress_interval_ms: 120000",
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("line1");
  expect(body).toContain("line2");
  expect(body).toContain("every 2m");
});

// --- RoboRev combined review (98e31d5): catch-up/clear ordering (L1) --------
// A send-branch terminal catch-up raw carries watch_id + watching:false +
// terminal_catchup:true. Without operation args it must read as a catch-up
// (terminal outcome), never as "Cleared …".

test("an arg-less send-branch catch-up reads as a catch-up, not a clear (L1)", () => {
  const d = toolRendererFor("job_watch");
  const raw = {
    watch_id: "watch_cu",
    source: "job_a1b2",
    watching: false,
    terminal_catchup: true,
    fired: true,
    status: "completed",
  };
  const noArgs = item({ toolName: "job_watch", argumentsJSON: "", raw, output: "" });
  expect(d.summary(noArgs)).toContain("completed");
  expect(d.summary(noArgs)).not.toContain("Cleared");
  const Body = d.body!;
  const { container } = render(<Body item={noArgs} live={false} />);
  expect(container.textContent?.trim() ?? "").toBe("");
});

// --- RoboRev combined review (98e31d5): every:1 normalization (L3) ----------
// The backend normalizes every==1 to unset; the renderer must too, or the
// same watch describes itself as throttled in create but unthrottled
// everywhere else.

test("every:1 in create args renders as unthrottled (L3)", () => {
  const d = toolRendererFor("job_watch");
  const args = { operation: "create", source: "dlg_7Hk2", events: ["communicate"], every: 1 };
  const raw = {
    watch_id: "watch_ev1",
    source: "dlg_7Hk2",
    watching: true,
    events: ["communicate"],
    replaced_existing: false,
    fired: false,
  };
  expect(d.summary(watchItem(args, raw))).not.toContain("(every 1)");
  const Body = d.body!;
  render(<Body item={watchItem(args, raw)} live={false} />);
  expect(screen.getByTestId("job-watch-body").textContent ?? "").not.toContain("(every 1)");
});

test("expanded timer rows name the embedded note (e9ab5ec review)", async () => {
  const user = userEvent.setup();
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  for (const condition of ["after_seconds: 300; note: check the build", "repeat_seconds: 300; note: check the build"]) {
    cleanup();
    render(
      <Body
        item={watchItem(
          { operation: "list" },
          { watches: [{ watch_id: "watch_t", source: "self", watching: true, condition }], count: 1 },
        )}
        live={false}
      />,
    );
    await user.click(screen.getByTestId("job-watch-row"));
    const detail = screen.getByTestId("job-watch-row-detail").textContent ?? "";
    expect(detail).toContain("check the build");
  }
});

// --- RoboRev combined review (80abf55): button reset + separators (L1,L2) ---
// .row wraps a native <button>: without a reset, native chrome (border,
// background, font, color, cursor) leaks through. And rows must be direct
// children of the list container for separators to render.

test("the row rule resets native button chrome", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "jobWatch.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rowRule = css.match(/\.row\s*\{([^}]*)\}/)?.[1] ?? "";
  for (const decl of ["background: transparent", "cursor: pointer", "border: 0", "font: inherit"]) {
    expect(rowRule).toContain(decl);
  }
});

test("list rows are direct children of the list container (L2)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "list" },
        {
          watches: [
            { watch_id: "watch_a", source: "self", watching: true, condition: "after_seconds: 300" },
            { watch_id: "watch_b", source: "self", watching: true, condition: "after_seconds: 300" },
          ],
          count: 2,
        },
      )}
      live={false}
    />,
  );
  const rows = screen.getAllByTestId("job-watch-row");
  expect(rows).toHaveLength(2);
  // No per-row wrapper divs: every row is a direct child of one container,
  // so sibling separators apply to all but the first.
  const container = rows[0]!.parentElement!;
  expect(container.querySelectorAll(":scope > [data-testid='job-watch-row']")).toHaveLength(2);
});

// --- RoboRev combined review (80abf55): summary-only expandability (L3) -----
// Clear and terminal catch-up results render nothing in the body — their
// rows must not offer a disclosure that opens to nothing.

test("clear results are not expandable (L3)", () => {
  const d = toolRendererFor("job_watch");
  expect(
    d.hasBody?.(watchItem({ operation: "clear", watch_id: "w" }, { watch_id: "w", source: "", watching: false })),
  ).toBe(false);
});

test("terminal catch-up results are not expandable (L3)", () => {
  const d = toolRendererFor("job_watch");
  expect(
    d.hasBody?.(
      watchItem(
        { operation: "create" },
        { source: "job_a1b2", watching: false, terminal_catchup: true, fired: false, status: "completed" },
      ),
    ),
  ).toBe(false);
});

test("a timer note create stays expandable (L3)", () => {
  const d = toolRendererFor("job_watch");
  expect(d.hasBody?.(watchItem({ operation: "create" }, TIMER_RAW))).toBe(true);
});

// --- RoboRev combined review (80abf55): wildcard everywhere (L4) ------------

test("wildcard events read as any event in summaries and sentences (L4)", () => {
  const d = toolRendererFor("job_watch");
  const raw = {
    watch_id: "watch_wc",
    source: "dlg_7Hk2",
    watching: true,
    events: ["*"],
    replaced_existing: false,
    fired: false,
  };
  const summary = d.summary(watchItem({ operation: "create", source: "dlg_7Hk2", events: ["*"] }, raw));
  expect(summary).toContain("any event");
  expect(summary).not.toMatch(/wakes on \*/);
  expect(summary).not.toContain("for *");
  const Body = d.body!;
  render(<Body item={watchItem({ operation: "create", source: "dlg_7Hk2", events: ["*"] }, raw)} live={false} />);
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("any event");
  expect(body).not.toContain("wakes on *");
});

test("a structured note containing delimiters survives whole", () => {
  // The raw carries the note in its own field beside the Condition string:
  // free prose that itself contains "; events: [...]" must render verbatim
  // instead of truncating at the delimiter (RoboRev PR #954). The trigger
  // clauses still parse from the Condition string beside it.
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_n" },
        {
          watch_id: "watch_n",
          source: "job_a1b2",
          watching: true,
          condition:
            "output_match: p; note: check the build; events: [assistant.tool] every 3 where tool_name=read_file, status=error",
          note: "check; events: [other]",
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("assistant.tool");
  expect(body).toContain("read_file");
  expect(body).toContain("(every 3)");
  expect(body).toContain("check; events: [other]");
  expect(body).not.toContain("for other");
});

test("a legacy note-before-events Condition parses every clause", () => {
  // Stored frames predate the structured note field and use the legacy
  // ordering "output_match: …; note: …; events: […]" — the parser must not
  // discard the trigger clauses after the note (RoboRev PR #954).
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_l" },
        {
          watch_id: "watch_l",
          source: "job_a1b2",
          watching: true,
          condition: "output_match: ready; note: check; events: [assistant.tool]",
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("ready");
  expect(body).toContain("assistant.tool");
  expect(body).toContain("check");
});

test("an arg-less legacy inspect-miss reads as not found, not cleared", () => {
  const d = toolRendererFor("job_watch");
  const miss = item({
    toolName: "job_watch",
    argumentsJSON: "",
    raw: { watch_id: "watch_m", watching: false },
    output: "",
  });
  expect(d.summary(miss)).toBe("Inspected watch_m · not found");
  expect(d.hasBody?.(miss)).toBe(true);
  const Body = d.body!;
  render(<Body item={miss} live={false} />);
  expect(screen.getByTestId("job-watch-body").textContent ?? "").toContain("not found");
});

test("an arg-less clear with create markers still reads as cleared", () => {
  const d = toolRendererFor("job_watch");
  const cleared = item({
    toolName: "job_watch",
    argumentsJSON: "",
    raw: { watch_id: "watch_c", source: "", watching: false, replaced_existing: false, fired: false },
    output: "",
  });
  expect(d.summary(cleared)).toContain("Cleared");
  expect(d.hasBody?.(cleared)).toBe(false);
});

test("an arg-less pending inspect with source reads as pending, not cleared", () => {
  // A pending inspect carries watching:false + source with no end_reason (a
  // detached watch on the terminal-flush rail) — and no replaced_existing /
  // fired markers, which only create/clear results carry. Source alone must
  // never infer a clear.
  const d = toolRendererFor("job_watch");
  const pending = item({
    toolName: "job_watch",
    argumentsJSON: "",
    raw: { watch_id: "watch_p", source: "job_a1b2", watching: false },
    output: "",
  });
  expect(d.summary(pending)).toBe("Inspected watch_p · pending");
  expect(d.hasBody?.(pending)).toBe(true);
  const Body = d.body!;
  render(<Body item={pending} live={false} />);
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("pending");
  expect(body).not.toContain("Cleared");
});

// --- RoboRev combined review (8a56741): inspect punctuation (finding 1) ------
// ConditionSentence owns the sentence's single terminal period: an inspect
// body with deliveries/created metadata must read "matches — 3 deliveries,
// created …." with exactly one period, never "matches. — 3 deliveries.".

test("an inspect body with deliveries owns one terminal period (8a56741 finding 1)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_09QmWzRtNvxK" },
        {
          watch_id: "watch_09QmWzRtNvxK",
          source: "job_a1b2",
          watching: true,
          condition: "output_match: ready|done",
          deliveries: 3,
          created_at: "2026-09-06T09:41:00-07:00",
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).not.toContain("matches. —");
  expect(body).not.toContain("matches. ");
  expect(body).toContain("matches — 3 deliveries");
  expect(body.endsWith(".")).toBe(true);
});

// --- RoboRev combined review (8a56741): one-shot timer wording (finding 2) ---
// humanizeSeconds returns "in …" phrases; the one-shot inspect/row-detail
// renderer must not prefix them with "Reminds" ("Reminds in 5m"). One-shot
// timers read "Remind me …" (mirroring the create summary); repeating timers
// keep "Reminds every …".

test("a one-shot timer inspect reads Remind me, not Reminds in (8a56741 finding 2)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_one" },
        { watch_id: "watch_one", source: "self", watching: true, condition: "after_seconds: 300" },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("Remind me in 5m");
  expect(body).not.toContain("Reminds in");
});

test("a one-shot row detail reads Remind me, not Reminds in (8a56741 finding 2)", async () => {
  const user = userEvent.setup();
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "list" },
        { watches: [{ watch_id: "watch_one", source: "self", watching: true, condition: "after_seconds: 300" }] },
      )}
      live={false}
    />,
  );
  await user.click(screen.getByTestId("job-watch-row"));
  const detail = screen.getByTestId("job-watch-row-detail").textContent ?? "";
  expect(detail).toContain("Remind me in 5m");
  expect(detail).not.toContain("Reminds in");
});

test("a repeating timer inspect keeps Reminds every (8a56741 finding 2)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_rep" },
        { watch_id: "watch_rep", source: "self", watching: true, condition: "repeat_seconds: 300" },
      )}
      live={false}
    />,
  );
  expect(screen.getByTestId("job-watch-body").textContent ?? "").toContain("Reminds every 5m");
});

// --- RoboRev combined review (8a56741): pending details (finding 3) ----------
// A pending inspect raw carries the same detail fields as any other inspect
// (condition, note, deliveries, created_at). The body must render the pending
// state together with them, not a bare "is pending.".

test("a pending inspect body renders the trigger and details (8a56741 finding 3)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_p" },
        {
          watch_id: "watch_p",
          source: "job_a1b2",
          watching: false,
          condition: "output_match: ready|done",
          deliveries: 3,
          created_at: "2026-09-06T09:41:00-07:00",
          note: "check the build",
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("pending");
  expect(body).toContain("ready|done");
  expect(body).toContain("3 deliveries");
  expect(body).toContain("created Sep 6");
  expect(body).toContain("check the build");
  expect(body).not.toContain("ended");
});

// --- RoboRev combined review (8a56741): end-reason wording (finding 4) -------
// List rows and inspect bodies share one formatter over the backend's real
// end-reason ids (agent/session_tools_jobs.go recentWatchEntry comment,
// agent/job_watch.go, agent/jobs.go); unknown ids fall back to the raw id.

test("ended rows and inspect bodies name end reasons in words (8a56741 finding 4)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "list" },
        {
          watches: [
            { watch_id: "watch_b", source: "job_a1b2", watching: false, end_reason: "budget_exhausted" },
            { watch_id: "watch_t", source: "job_a1b2", watching: false, end_reason: "auto_removed_terminal" },
            { watch_id: "watch_r", source: "job_a1b2", watching: false, end_reason: "replaced" },
            { watch_id: "watch_x", source: "job_a1b2", watching: false, end_reason: "mystery_future_id" },
          ],
        },
      )}
      live={false}
    />,
  );
  const rows = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(rows).toContain("matched 50 times (budget exhausted)");
  expect(rows).toContain("watched job finished before it could fire");
  expect(rows).toContain("replaced by a newer watch");
  expect(rows).toContain("mystery_future_id");
  expect(rows).not.toContain("budget_exhausted");
  expect(rows).not.toContain("auto_removed_terminal");
  cleanup();
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_b" },
        { watch_id: "watch_b", source: "job_a1b2", watching: false, end_reason: "budget_exhausted" },
      )}
      live={false}
    />,
  );
  const inspect = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(inspect).toContain("matched 50 times (budget exhausted)");
  expect(inspect).not.toContain("budget_exhausted");
});

// --- RoboRev combined review of 5c202de (LOW): ended inspect details --------
// The ended inspect branch returned after rendering only the end reason,
// hiding the structured condition/details + note the backend now carries
// (inspectResultFromWatchHistory, agent/job_watch.go:2363 — condition, note,
// deliveries beside end_reason). The ended body must mirror the
// pending/active branches: end sentence first, then the trigger clauses and
// the note section.

test("an ended inspect body renders the condition and note beside the end reason (LOW)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_e" },
        {
          watch_id: "watch_e",
          source: "job_a1b2",
          watching: false,
          condition: "output_match: ready|done",
          note: "check the build",
          deliveries: 3,
          end_reason: "cleared",
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("ended");
  expect(body).toContain("cleared");
  expect(body).toContain("ready|done");
  expect(body).toContain("check the build");
  expect(screen.getByTestId("job-watch-note").textContent).toContain("check the build");
});

test("an ended timer inspect renders its cadence beside the end reason (LOW)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_et" },
        {
          watch_id: "watch_et",
          source: "self",
          watching: false,
          condition: "repeat_seconds: 300",
          end_reason: "fired",
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("ended");
  expect(body).toContain("fired");
  expect(body).toContain("Reminds every 5m");
});

test("an ended inspect with no condition still reads as an ending (LOW)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_b" },
        { watch_id: "watch_b", source: "job_a1b2", watching: false, end_reason: "budget_exhausted" },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("matched 50 times (budget exhausted)");
  expect(body).not.toContain("budget_exhausted");
});

// --- RoboRev combined review of 71a7981 (Finding 1) --------------------------
// A structured note is verbatim prose that may itself contain delimiter-looking
// text ("; events: […]"). The Condition string embeds it as a note: clause,
// so parsing the flattened condition without removing that exact clause
// invents triggers the watch never armed.

test("a structured note with delimiter text invents no triggers in inspect (Finding 1)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_delim" },
        {
          watch_id: "watch_delim",
          source: "job_a1b2",
          watching: true,
          condition: "output_match: ready; note: check; events: [communicate]",
          note: "check; events: [communicate]",
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("ready");
  expect(body).toContain("check; events: [communicate]");
  expect(body).not.toContain("wakes on communicate");
  expect(body).not.toContain("Watches job_a1b2: communicate");
});

test("a structured note with delimiter text invents no triggers in list rows (Finding 1)", () => {
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "list" },
        {
          watches: [
            {
              watch_id: "watch_delim",
              source: "job_a1b2",
              watching: true,
              condition: "output_match: ready; note: check; events: [communicate]",
              note: "check; events: [communicate]",
            },
          ],
          count: 1,
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("ready");
  expect(body).not.toContain("communicate");
});

test("a legacy string-only note keeps the fallback parse (Finding 1)", () => {
  // Stored frames predate the structured note field: with no structured value
  // the parser keeps its current behavior — the events clause beside the note
  // clause still renders.
  const d = toolRendererFor("job_watch");
  const Body = d.body!;
  render(
    <Body
      item={watchItem(
        { operation: "inspect", watch_id: "watch_legacy" },
        {
          watch_id: "watch_legacy",
          source: "job_a1b2",
          watching: true,
          condition: "output_match: ready; note: check; events: [communicate]",
        },
      )}
      live={false}
    />,
  );
  const body = screen.getByTestId("job-watch-body").textContent ?? "";
  expect(body).toContain("ready");
  expect(body).toContain("communicate");
  expect(body).toContain("check");
});

// --- RoboRev combined review of 71a7981 (Finding 2) --------------------------
// A valid watch with only a note and no trigger must still surface the note:
// the summary heads it and the body renders it, mirroring the timer-note
// create conventions. A truly conditionless watch without a note stays
// summary-only.

test("a note-only create heads the note in the summary (Finding 2)", () => {
  const d = toolRendererFor("job_watch");
  const raw = {
    watch_id: "watch_noteonly",
    source: "job_a1b2",
    watching: true,
    note: "keep an eye on the flaky shard",
    replaced_existing: false,
    fired: false,
  };
  expect(d.summary(watchItem({ operation: "create" }, raw))).toBe("Watch job_a1b2 · keep an eye on the flaky shard");
});

test("a note-only create body renders the full note section (Finding 2)", () => {
  const d = toolRendererFor("job_watch");
  const raw = {
    watch_id: "watch_noteonly",
    source: "job_a1b2",
    watching: true,
    note: "keep an eye on the flaky shard",
    replaced_existing: false,
    fired: false,
  };
  const Body = d.body!;
  render(<Body item={watchItem({ operation: "create" }, raw)} live={false} />);
  expect(screen.getByTestId("job-watch-note").textContent).toContain("keep an eye on the flaky shard");
  expect(d.hasBody?.(watchItem({ operation: "create" }, raw))).toBe(true);
});

test("a conditionless noteless create stays summary-only (Finding 2)", () => {
  const d = toolRendererFor("job_watch");
  const raw = { watch_id: "watch_bare", source: "self", watching: true };
  expect(d.summary(watchItem({ operation: "create" }, raw))).toBe("Watch this session");
  expect(d.hasBody?.(watchItem({ operation: "create" }, raw))).toBe(false);
});
