import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef } from "react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { WireError } from "../../../protocol/errors";
import type { ThreadModel } from "../../../protocol/model";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { ThreadCapabilities } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetThreadsStoreForTests } from "../../../stores/threads";
import { Toast } from "../../../widgets";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { JobsPanel, type JobsPanelHandle, statusTone } from "./JobsPanel";

const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function testModel(overrides: Partial<ThreadModel> = {}): ThreadModel {
  return {
    ref: "ref_a",
    threadId: "thr_a",
    name: "",
    status: { type: "idle" },
    modelProvider: "anthropic",
    model: "claude",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    lastFrameAt: 0,
    capabilities: CAPABILITIES,
    goal: null,
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "/tmp/project",
    ...overrides,
  };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

// SessionChrome passes its useNowTick value; the fixtures below are all
// anchored relative to this fixed instant so durations are deterministic.
const NOW = Date.parse("2026-07-31T12:05:00Z");

// Wire-true fixture: agent/jobs_panel.go's JobSummary shape (jobId/type/
// status/reason/description/command/task/background/startedAt/endedAt/
// exitCode/outputBytes/hasOutput), same fixture family as jobData.test.ts's
// own. Both rows are exactly 60s long at NOW (ended 12:01 / started 12:04),
// so both render formatWorkDuration's "1m".
const JOBS_DATA = [
  {
    jobId: "job_1",
    type: "shell",
    status: "completed",
    description: "run tests",
    command: "go test ./...",
    background: true,
    startedAt: "2026-07-31T12:00:00Z",
    endedAt: "2026-07-31T12:01:00Z",
    exitCode: 0,
    outputBytes: 123,
    hasOutput: true,
  },
  {
    jobId: "job_2",
    type: "delegate",
    status: "running",
    description: "scout the repo",
    task: "survey the codebase",
    background: true,
    startedAt: "2026-07-31T12:04:00Z",
    outputBytes: 0,
    hasOutput: false,
  },
];

const TWO_RUNNING_DATA = [
  {
    jobId: "job_3",
    type: "shell",
    status: "running",
    description: "build",
    background: true,
    startedAt: "2026-07-31T12:04:30Z",
    outputBytes: 0,
    hasOutput: false,
  },
  {
    jobId: "job_4",
    type: "delegate",
    status: "running",
    description: "review diff",
    background: true,
    startedAt: "2026-07-31T12:04:40Z",
    outputBytes: 0,
    hasOutput: false,
  },
];

// TWO_RUNNING_DATA one push later: job_3 has finished, so the badge that
// counted it is now a job behind.
const ONE_STILL_RUNNING_DATA = [
  {
    jobId: "job_3",
    type: "shell",
    status: "completed",
    description: "build",
    background: true,
    startedAt: "2026-07-31T12:04:30Z",
    endedAt: "2026-07-31T12:04:50Z",
    exitCode: 0,
    outputBytes: 0,
    hasOutput: false,
  },
  {
    jobId: "job_4",
    type: "delegate",
    status: "running",
    description: "review diff",
    background: true,
    startedAt: "2026-07-31T12:04:40Z",
    outputBytes: 0,
    hasOutput: false,
  },
];

// A status no version of this bundle has heard of - JobSummary.Status is a
// plain Go string, so a newer daemon can send one.
const UNKNOWN_STATUS_DATA = [
  {
    jobId: "job_7",
    type: "shell",
    status: "quarantined",
    description: "held for review",
    background: false,
    startedAt: "2026-07-31T12:04:00Z",
    outputBytes: 0,
    hasOutput: false,
  },
];

// Go's zero time, which is what agent/jobs_panel.go formats when a record
// folded without a start timestamp: year 1, not a job two millennia old.
const ZERO_START_DATA = [
  {
    jobId: "job_6",
    type: "shell",
    status: "running",
    description: "orphan",
    background: true,
    startedAt: "0001-01-01T00:00:00Z",
    outputBytes: 0,
    hasOutput: false,
  },
];

// A job the wire says is settled but carries no endedAt - the shape a fold
// produces when the finishing event never recorded a timestamp.
const SETTLED_NO_END_DATA = [
  {
    jobId: "job_5",
    type: "shell",
    status: "completed",
    description: "run tests",
    background: true,
    startedAt: "2026-07-31T12:00:00Z",
    outputBytes: 0,
    hasOutput: false,
  },
];

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  // The toast queue is module state and outlives cleanup(): without this, a
  // toast an earlier test pushed is still on screen when a later test asserts
  // a message is ABSENT, and the matcher finds the stale one.
  resetToastStoreForTests();
  // Row disclosure open/closed state lives in the shared disclosureStore
  // (module-level, survives cleanup()) - same reset every transcript
  // disclosure test performs, so an earlier test's expanded row can't leak
  // into a later test that expects to start collapsed.
  resetDisclosureStoreForTests();
});

afterEach(() => {
  cleanup();
});

// 1. The sheet's row list is fetched fresh on open (the plan's
// push-driven-plus-fetch-on-open model) - never while the panel sits closed.
test("closed by default: no listJobs call until the trigger is clicked", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => ({ data: JOBS_DATA }));

  render(<JobsPanel sessionRef="ref_a" model={testModel()} now={NOW} />);
  expect(fake.calls.filter((c) => c.method === "serf/jobs/list")).toHaveLength(0);

  await user.click(screen.getByRole("button", { name: "Jobs" }));
  await screen.findAllByTestId("job-row");
  expect(fake.calls.filter((c) => c.method === "serf/jobs/list")).toHaveLength(1);
});

// 2.
test("opening fetches and renders one row per job (glyph, description, status chip, duration)", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => ({ data: JOBS_DATA }));

  render(<JobsPanel sessionRef="ref_a" model={testModel()} now={NOW} />);
  await user.click(screen.getByRole("button", { name: "Jobs" }));

  const rows = await screen.findAllByTestId("job-row");
  expect(rows).toHaveLength(2);
  expect(fake.calls.find((c) => c.method === "serf/jobs/list")?.params).toEqual({ ref: "ref_a" });
  // Shell row: › glyph, description, "completed" status chip, 1m duration.
  expect(rows[0]?.textContent).toContain("run tests");
  expect(rows[0]?.textContent).toContain("completed");
  expect(rows[0]?.textContent).toContain("›");
  expect(rows[0]?.textContent).toContain("1m");
  // Delegate row: ◈ glyph, "running" status chip, 1m elapsed at NOW.
  expect(rows[1]?.textContent).toContain("scout the repo");
  expect(rows[1]?.textContent).toContain("running");
  expect(rows[1]?.textContent).toContain("◈");
  expect(rows[1]?.textContent).toContain("1m");
});

// 3.
test("an empty list shows the 'No jobs yet' empty state", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => ({ data: [] }));

  render(<JobsPanel sessionRef="ref_a" model={testModel()} now={NOW} />);
  await user.click(screen.getByRole("button", { name: "Jobs" }));

  expect(await screen.findByText("No jobs yet")).toBeTruthy();
});

// 4. Null data is an old daemon with no jobsFn registered (jobData.ts's own
// comment) - a capability gap, folded into the same unsupported state as
// actionUnavailable, not an error.
test("null data shows the unsupported state, with no Try again", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => ({ data: null }));

  render(<JobsPanel sessionRef="ref_a" model={testModel()} now={NOW} />);
  await user.click(screen.getByRole("button", { name: "Jobs" }));

  expect(await screen.findByText(/job list isn.t available/i)).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
});

// 5. A Codex-source thread rejects the wire call outright (appwire.
// Unavailable) - an expected capability gap, so no error toast either.
test("an actionUnavailable rejection shows the same unsupported state, with no error toast", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => {
    throw new WireError("codex source does not expose jobs", -32014, { serfErrorInfo: "actionUnavailable" });
  });

  render(
    <>
      <JobsPanel sessionRef="ref_codex" model={testModel()} now={NOW} />
      <Toast />
    </>,
  );
  await user.click(screen.getByRole("button", { name: "Jobs" }));

  expect(await screen.findByText(/job list isn.t available/i)).toBeTruthy();
  expect(screen.queryByText(/couldn.t load jobs/i)).toBeNull();
});

// 6. isThreadNotFound with no prior rows is terminal: the hub found neither
// a live daemon nor a past-index record (sessionErrors.ts's own comment), so
// there is nothing to retry and no "No jobs yet" to contradict.
test("a 'thread not found: ' rejection with no prior rows shows the terminal 'This session has ended'", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => {
    throw new WireError("thread not found: thr_a", -32014, { serfErrorInfo: "sessionUnavailable" });
  });

  render(
    <>
      <JobsPanel sessionRef="ref_a" model={testModel()} now={NOW} />
      <Toast />
    </>,
  );
  await user.click(screen.getByRole("button", { name: "Jobs" }));

  await screen.findByText("This session has ended");
  expect(screen.queryByText("No jobs yet")).toBeNull();
  expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
  expect(screen.queryByText(/couldn.t load jobs/i)).toBeNull();
});

// 7. A first fetch that fails has nothing to keep, so it gets the error
// state alone - toast AND inline, with Try again as the only way out.
test("a first-fetch failure shows the error state plus a toast; Try again refetches", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let calls = 0;
  fake.on("serf/jobs/list", () => {
    calls += 1;
    if (calls === 1) throw new Error("jobs boom");
    return { data: JOBS_DATA };
  });

  render(
    <>
      <JobsPanel sessionRef="ref_a" model={testModel()} now={NOW} />
      <Toast />
    </>,
  );
  await user.click(screen.getByRole("button", { name: "Jobs" }));

  await screen.findByText("Couldn't load jobs");
  await screen.findByText("jobs boom");
  // Toast AND inline state both report the same failure.
  expect(screen.getAllByText(/couldn.t load jobs/i).length).toBeGreaterThanOrEqual(2);

  await user.click(screen.getByRole("button", { name: "Try again" }));

  expect(await screen.findAllByTestId("job-row")).toHaveLength(2);
  // Scoped to the dialog: the toast's own copy of the sentence outlives the
  // retry until its timeout, and it isn't what this assertion is about.
  expect(within(screen.getByRole("dialog")).queryByText(/couldn.t load jobs/i)).toBeNull();
});

// Scripts serf/jobs/list to answer `first` once and to reject every later
// call: the exact shape of a live re-fetch blipping under a reader.
function failAfterFirstFetch(fake: FakeClient, first: unknown, err: unknown): void {
  let calls = 0;
  fake.on("serf/jobs/list", () => {
    calls += 1;
    if (calls === 1) return { data: first };
    throw err;
  });
}

// Opens the panel, waits for the first fetch to land, then bumps
// jobsUpdatedAt the way a serf/job/updated push does - which is the only
// thing that re-fetches while the panel stays open.
async function openThenPush(fake: FakeClient): Promise<ReturnType<typeof userEvent.setup>> {
  const user = userEvent.setup();
  const panel = (bump: number) => (
    <>
      <JobsPanel sessionRef="ref_a" model={testModel({ jobsUpdatedAt: bump })} now={NOW} />
      <Toast />
    </>
  );
  const { rerender } = render(panel(1));
  await user.click(screen.getByRole("button", { name: "Jobs" }));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "serf/jobs/list")).toHaveLength(1));

  rerender(panel(2));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "serf/jobs/list")).toHaveLength(2));
  return user;
}

// 8.
test("a refetch failure keeps the rows already on screen under a stale notice", async () => {
  const fake = connectFakeClient();
  failAfterFirstFetch(fake, JOBS_DATA, new Error("local daemon unavailable: broken pipe"));

  await openThenPush(fake);

  const stale = await screen.findByTestId("jobs-stale");
  expect(stale.textContent).toContain("Showing the last list that loaded.");
  expect(screen.getAllByTestId("job-row")).toHaveLength(2);
  expect(screen.getByText("run tests")).toBeTruthy();
});

// 9.
test("a jobsUpdatedAt bump while open refetches the list", async () => {
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => ({ data: JOBS_DATA }));

  await openThenPush(fake);

  expect(fake.calls.filter((c) => c.method === "serf/jobs/list")).toHaveLength(2);
  expect(await screen.findAllByTestId("job-row")).toHaveLength(2);
});

// 10.
test("a jobsUpdatedAt bump while closed does not fetch at all", () => {
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => ({ data: JOBS_DATA }));

  const { rerender } = render(<JobsPanel sessionRef="ref_a" model={testModel({ jobsUpdatedAt: 1 })} now={NOW} />);
  rerender(<JobsPanel sessionRef="ref_a" model={testModel({ jobsUpdatedAt: 2 })} now={NOW} />);

  expect(fake.calls.filter((c) => c.method === "serf/jobs/list")).toHaveLength(0);
});

// 11. The tail fetch is lazy-on-expand: Disclosure mounts its body only when
// open (widgets/disclosure/index.tsx), so mounting JobOutputTailView inside
// the body IS the lazy trigger.
test("expanding a row with hasOutput fetches jobOutput and renders the tail in a <pre>", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => ({ data: JOBS_DATA }));
  fake.on("serf/jobs/output", () => ({
    data: { tail: "ok pkg/a\nok pkg/b\n", totalBytes: 18, retainedStart: 0, truncated: false },
  }));

  render(<JobsPanel sessionRef="ref_a" model={testModel()} now={NOW} />);
  await user.click(screen.getByRole("button", { name: "Jobs" }));
  await screen.findAllByTestId("job-row");
  expect(fake.calls.filter((c) => c.method === "serf/jobs/output")).toHaveLength(0);

  await user.click(screen.getByText("run tests"));

  const output = await screen.findByTestId("job-output");
  expect(fake.calls.find((c) => c.method === "serf/jobs/output")?.params).toEqual({ ref: "ref_a", jobId: "job_1" });
  const pre = output.querySelector("pre");
  expect(pre?.textContent).toContain("ok pkg/a");
  expect(pre?.textContent).toContain("ok pkg/b");
});

// 12.
test("expanding a row without hasOutput never calls jobOutput", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => ({ data: JOBS_DATA }));
  fake.on("serf/jobs/output", () => ({ data: { tail: "", totalBytes: 0, retainedStart: 0, truncated: false } }));

  render(<JobsPanel sessionRef="ref_a" model={testModel()} now={NOW} />);
  await user.click(screen.getByRole("button", { name: "Jobs" }));
  await screen.findAllByTestId("job-row");

  await user.click(screen.getByText("scout the repo"));
  await screen.findByTestId("job-detail-status");

  expect(fake.calls.filter((c) => c.method === "serf/jobs/output")).toHaveLength(0);
});

// 13.
test("a truncated tail renders the 'Showing last N of M bytes' caption", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => ({ data: JOBS_DATA }));
  fake.on("serf/jobs/output", () => ({ data: { tail: "6789", totalBytes: 10, retainedStart: 6, truncated: true } }));

  render(<JobsPanel sessionRef="ref_a" model={testModel()} now={NOW} />);
  await user.click(screen.getByRole("button", { name: "Jobs" }));
  await user.click(await screen.findByText("run tests"));

  expect(await screen.findByText(/showing last 4 of 10 bytes/i)).toBeTruthy();
});

// 14.
test("a jobOutput rejection renders an inline error line and does not clear the detail fields", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => ({ data: JOBS_DATA }));
  fake.on("serf/jobs/output", () => {
    throw new Error("output boom");
  });

  render(
    <>
      <JobsPanel sessionRef="ref_a" model={testModel()} now={NOW} />
      <Toast />
    </>,
  );
  await user.click(screen.getByRole("button", { name: "Jobs" }));
  await user.click(await screen.findByText("run tests"));

  const errBox = await screen.findByTestId("job-output-error");
  expect(errBox.querySelector("[role='alert']")?.textContent).toContain("Couldn't load job output: output boom");
  // The row's own detail fields are untouched by the tail's failure.
  expect(screen.getByTestId("job-detail-status").textContent).toContain("completed");
  expect(screen.getByTestId("job-detail-type").textContent).toContain("shell");
});

// 15. The trigger badge counts the non-terminal (running) jobs in the last
// fetched list - "Jobs" alone until a fetch lands with running jobs.
test("the trigger label shows the running count after a fetch with running jobs", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => ({ data: TWO_RUNNING_DATA }));

  render(<JobsPanel sessionRef="ref_a" model={testModel()} now={NOW} />);
  expect(screen.getByRole("button", { name: "Jobs" })).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Jobs" }));
  await screen.findAllByTestId("job-row");

  expect(screen.getByRole("button", { name: "Jobs ●2" })).toBeTruthy();
});

// 16. The terminal daemon-gone notice arrives under a reader already looking
// at a list, with nothing on screen leading up to it - the same case the
// stale-refetch notice announces, so it announces too.
test("a daemonGone refetch keeps the rows under a role=alert terminal notice", async () => {
  const fake = connectFakeClient();
  failAfterFirstFetch(
    fake,
    JOBS_DATA,
    new WireError("thread not found: thr_a", -32014, { serfErrorInfo: "sessionUnavailable" }),
  );

  await openThenPush(fake);

  const gone = await screen.findByTestId("jobs-daemon-gone");
  expect(gone.querySelector("[role='alert']")?.textContent).toContain("This session's daemon has exited.");
  expect(screen.getAllByTestId("job-row")).toHaveLength(2);
});

// 17. A settled status is the wire's own word that the job is over, so it
// beats a missing endedAt: the row shows no clock rather than an elapsed
// time that keeps climbing on every tick.
test("a settled job with no endedAt shows no clock instead of ticking forever", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => ({ data: SETTLED_NO_END_DATA }));

  const { rerender } = render(<JobsPanel sessionRef="ref_a" model={testModel()} now={NOW} />);
  await user.click(screen.getByRole("button", { name: "Jobs" }));
  const row = (await screen.findAllByTestId("job-row"))[0];
  const atNow = row?.textContent;
  expect(atNow).not.toMatch(/\d+[smh]/);

  rerender(<JobsPanel sessionRef="ref_a" model={testModel()} now={NOW + 3_600_000} />);
  expect(row?.textContent).toBe(atNow);
});

// 18. The caption is in BYTES, and the daemon's own byte offsets are the only
// honest source for them: this tail is a single 4-byte UTF-8 emoji, which JS
// measures as 2 UTF-16 code units.
test("the truncated caption counts the daemon's bytes, not JS string length", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => ({ data: JOBS_DATA }));
  fake.on("serf/jobs/output", () => ({ data: { tail: "😀", totalBytes: 10, retainedStart: 6, truncated: true } }));

  render(<JobsPanel sessionRef="ref_a" model={testModel()} now={NOW} />);
  await user.click(screen.getByRole("button", { name: "Jobs" }));
  await user.click(await screen.findByText("run tests"));

  expect(await screen.findByText(/showing last 4 of 10 bytes/i)).toBeTruthy();
});

// 19. The badge must not keep claiming a job is running after the push that
// says it finished. The push is the signal, the list is the evidence: once
// the reader has opened the panel once, a push re-fetches even while the
// panel is closed, and the badge follows the fetch.
test("a push after the first open refreshes the badge while the panel is closed", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let calls = 0;
  fake.on("serf/jobs/list", () => {
    calls += 1;
    return { data: calls === 1 ? TWO_RUNNING_DATA : ONE_STILL_RUNNING_DATA };
  });

  const panel = (bump: number) => <JobsPanel sessionRef="ref_a" model={testModel({ jobsUpdatedAt: bump })} now={NOW} />;
  const { rerender } = render(panel(1));
  await user.click(screen.getByRole("button", { name: "Jobs" }));
  await screen.findAllByTestId("job-row");
  expect(screen.getByRole("button", { name: "Jobs ●2" })).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Close" }));
  expect(screen.queryByRole("dialog")).toBeNull();
  rerender(panel(2));

  expect(await screen.findByRole("button", { name: "Jobs ●1" })).toBeTruthy();
  expect(fake.calls.filter((c) => c.method === "serf/jobs/list")).toHaveLength(2);
});

// 20. A refresh the reader never asked for and cannot see must not interrupt
// them: the badge just stays at its last known count until the next fetch
// lands.
test("a background refresh failure while the panel is closed pushes no toast", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let calls = 0;
  fake.on("serf/jobs/list", () => {
    calls += 1;
    if (calls === 2) throw new Error("jobs boom");
    return { data: TWO_RUNNING_DATA };
  });

  const panel = (bump: number) => (
    <>
      <JobsPanel sessionRef="ref_a" model={testModel({ jobsUpdatedAt: bump })} now={NOW} />
      <Toast />
    </>
  );
  const { rerender } = render(panel(1));
  await user.click(screen.getByRole("button", { name: "Jobs" }));
  await screen.findAllByTestId("job-row");
  await user.click(screen.getByRole("button", { name: "Close" }));

  rerender(panel(2));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "serf/jobs/list")).toHaveLength(2));

  // Re-opening fetches a third time and succeeds; a toast pushed by the
  // failed background refresh would still be on screen underneath it (a
  // toast outlives a retry - see the first-fetch-failure test above).
  await user.click(screen.getByRole("button", { name: "Jobs ●2" }));
  await screen.findAllByTestId("job-row");
  expect(screen.queryByText(/couldn.t load jobs/i)).toBeNull();
});

// 21. SessionChrome's collapsed layout hides the trigger and opens the panel
// from a menu instead. With no badge rendered there is nothing for a closed
// panel to keep honest, so a push costs no round trip.
test("a push while closed and the trigger hidden does not refresh in the background", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => ({ data: TWO_RUNNING_DATA }));

  const handle = createRef<JobsPanelHandle>();
  const panel = (bump: number) => (
    <JobsPanel ref={handle} sessionRef="ref_a" model={testModel({ jobsUpdatedAt: bump })} now={NOW} hideTrigger />
  );
  const { rerender } = render(panel(1));
  act(() => handle.current?.open());
  await screen.findAllByTestId("job-row");
  expect(fake.calls.filter((c) => c.method === "serf/jobs/list")).toHaveLength(1);

  await user.click(screen.getByRole("button", { name: "Close" }));
  rerender(panel(2));

  expect(fake.calls.filter((c) => c.method === "serf/jobs/list")).toHaveLength(1);
});

// 22. A zero startedAt is the wire's "no start timestamp", not a start in
// the year 1 - clocking it would put two millennia on the row. Same call
// statusFormat.ts's totalWorkMillis makes about its own anchor.
test("a zero-time startedAt shows no clock rather than a two-millennia one", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => ({ data: ZERO_START_DATA }));

  render(<JobsPanel sessionRef="ref_a" model={testModel()} now={NOW} />);
  await user.click(screen.getByRole("button", { name: "Jobs" }));

  const row = (await screen.findAllByTestId("job-row"))[0];
  expect(row?.textContent).toContain("orphan");
  expect(row?.textContent).not.toMatch(/\d+[smh]/);
});

// 23. A status this bundle doesn't know is not known to be alive and not
// known to have failed, so it recedes neutral rather than borrowing either
// signal. "constructor" is in here because a bare object lookup would answer
// it off Object.prototype.
test("pins the chip tone for a status outside the known set: neutral, never alive or danger", () => {
  expect(statusTone("running")).toBe("alive");
  expect(statusTone("failed")).toBe("danger");
  expect(statusTone("quarantined")).toBe("neutral");
  expect(statusTone("constructor")).toBe("neutral");
});

// 24. The row itself is never hidden by an unrecognised status: a job that
// really ran stays in the list, labelled with the wire's own word for it.
test("a row with an unknown status still renders, labelled with the wire's own status", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/jobs/list", () => ({ data: UNKNOWN_STATUS_DATA }));

  render(<JobsPanel sessionRef="ref_a" model={testModel()} now={NOW} />);
  await user.click(screen.getByRole("button", { name: "Jobs" }));

  const rows = await screen.findAllByTestId("job-row");
  expect(rows).toHaveLength(1);
  expect(rows[0]?.textContent).toContain("quarantined");
  expect(rows[0]?.textContent).toContain("held for review");
});
