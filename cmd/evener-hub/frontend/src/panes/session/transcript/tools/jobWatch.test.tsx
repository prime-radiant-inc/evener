// @vitest-environment jsdom

// job_watch tool descriptor tests (mockups 23-job-watch §A-D).
//
// TDD RED: this file is written first, against the generic job_* fallback
// still registered in jobTools.tsx. Every summary expectation below names
// the approved per-operation rendering, so each test fails until jobWatch.tsx
// registers its exact-match "job_watch" descriptor (exact matches win over
// the family predicate per toolRenderers.ts).
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
