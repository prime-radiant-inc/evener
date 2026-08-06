import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { threadsStore } from "../../../stores/threads";
import { ActivityRowDetail } from "./ActivityRowDetail";
import type { ActivityDelegate, ActivityJob, ActivitySessionNode } from "./activityData";
import type { ActivityDelegateRow, ActivityJobRow } from "./activityRows";

// Pinned clock: every quiet-age assertion below measures against this instant.
const NOW = Date.parse("2026-08-05T15:00:12.000Z");

// localHHMM computes the expected "started HH:MM" through the same local-time
// projection formatClockTime uses, keeping the suite timezone-independent.
function localHHMM(iso: string): string {
  const parsed = new Date(iso);
  return `${String(parsed.getHours()).padStart(2, "0")}:${String(parsed.getMinutes()).padStart(2, "0")}`;
}

function shellJob(overrides: Record<string, unknown>): ActivityJob {
  return {
    jobId: "job_x",
    ownerSessionId: "sess_root",
    ownerRef: "ref_root",
    type: "shell",
    status: "running",
    terminal: false,
    background: false,
    hasOutput: true,
    description: "shell job",
    startedAt: "2026-08-05T15:00:00Z",
    outputBytes: 0,
    ...overrides,
  } as ActivityJob;
}

function jobRow(overrides: Record<string, unknown>, rowOverrides: Partial<ActivityJobRow> = {}): ActivityJobRow {
  return {
    kind: "job",
    id: "job:job_x",
    level: 1,
    job: shellJob(overrides),
    live: false,
    defaultDetailOpen: true,
    transcriptRef: "job:job_x",
    parentRef: "ref_root",
    ...rowOverrides,
  };
}

function childSession(label: string): ActivitySessionNode {
  return {
    kind: "session",
    sessionId: "sess_child",
    ref: "ref_child",
    label,
    aggregate: "running",
    counts: { active: 1, failed: 0, completed: 0, complete: false },
    entries: [],
    branch: {},
  };
}

function delegateRow(
  delegateOverrides: Record<string, unknown>,
  rowOverrides: Partial<ActivityDelegateRow> = {},
): ActivityDelegateRow {
  const delegate = {
    delegateId: "dlg_x",
    childSessionId: "sess_child",
    childRef: "ref_child",
    turns: [],
    branch: {},
    ...delegateOverrides,
  } as ActivityDelegate;
  return {
    kind: "delegate",
    id: "delegate:dlg_x",
    level: 1,
    delegate,
    live: true,
    defaultDetailOpen: true,
    transcriptRef: "ref_child",
    parentRef: "ref_root",
    ...rowOverrides,
  };
}

// setupJobOutput spies the store method (not the module) so the preview's
// fetch stays in-process; the default resolve is an empty tail, and tests
// override it with mockResolvedValue/mockRejectedValue on the returned spy.
function setupJobOutput() {
  return vi
    .spyOn(threadsStore.getState(), "jobOutput")
    .mockResolvedValue({ tail: "", totalBytes: 0, retainedStart: 0 });
}

let jobOutput: ReturnType<typeof setupJobOutput>;

beforeEach(() => {
  // The output preview only fetches once the connection store reports ready;
  // the stub keeps the wire out of the unit test entirely.
  connectionStore.setState({ state: "ready" });
  jobOutput = setupJobOutput();
});

afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", client: null });
  vi.restoreAllMocks();
});

describe("ActivityRowDetail", () => {
  test("renders the command in mono for a shell job row, preferring command over task over description", () => {
    const { rerender } = render(
      <ActivityRowDetail row={jobRow({ command: "npm test", task: "ignored task" }, { live: true })} now={NOW} />,
    );
    const command = screen.getByText("npm test");
    expect(command.tagName).toBe("CODE");
    expect(screen.queryByText("ignored task")).toBeNull();

    rerender(<ActivityRowDetail row={jobRow({ task: "fallback task" }, { live: true })} now={NOW} />);
    expect(screen.getByText("fallback task").tagName).toBe("CODE");

    rerender(<ActivityRowDetail row={jobRow({ description: "bare description" }, { live: true })} now={NOW} />);
    expect(screen.getByText("bare description").tagName).toBe("CODE");
  });

  test("renders the mandate for a delegate row, falling back to child label then childSessionId", () => {
    const { rerender } = render(<ActivityRowDetail row={delegateRow({ mandate: "Inspect the repo" })} now={NOW} />);
    expect(screen.getByText("Inspect the repo").tagName).toBe("P");

    rerender(<ActivityRowDetail row={delegateRow({ child: childSession("Child label") })} now={NOW} />);
    expect(screen.getByText("Child label").tagName).toBe("CODE");

    rerender(<ActivityRowDetail row={delegateRow({})} now={NOW} />);
    expect(screen.getByText("sess_child").tagName).toBe("CODE");
  });

  test("renders delegate mandate markdown inline and discloses later paragraphs", async () => {
    const { rerender } = render(
      <ActivityRowDetail
        row={delegateRow({ mandate: "Inspect **the repo**.\n\nAdditional instructions: keep the report concise." })}
        now={NOW}
      />,
    );
    expect(screen.getByText("the repo").tagName).toBe("STRONG");
    expect(screen.queryByText("Additional instructions: keep the report concise.")).toBeNull();

    const showMore = screen.getByText("Show more");
    expect(showMore.tagName).toBe("SUMMARY");
    await userEvent.click(showMore);
    expect(screen.getByText("Additional instructions: keep the report concise.")).toBeTruthy();

    rerender(<ActivityRowDetail row={delegateRow({ mandate: "Only one paragraph." })} now={NOW} />);
    expect(screen.queryByText("Show more")).toBeNull();
  });

  test("live job row meta says running with quiet age, output bytes, and started time", () => {
    render(
      <ActivityRowDetail
        row={jobRow(
          {
            command: "npm test",
            outputBytes: 512,
            startedAt: "2026-08-05T14:58:00Z",
            lastOutputAt: "2026-08-05T15:00:00Z",
          },
          { live: true },
        )}
        now={NOW}
      />,
    );
    // 12s of quiet measured from lastOutputAt, not from startedAt.
    expect(screen.getByText(`running 12s · 512b · started ${localHHMM("2026-08-05T14:58:00Z")}`)).toBeTruthy();
  });

  test("live delegate row meta measures quiet from the latest turn and sums turn output bytes", () => {
    render(
      <ActivityRowDetail
        row={delegateRow({
          mandate: "Inspect the repo",
          turns: [
            shellJob({
              jobId: "job_turn_1",
              type: "delegate",
              startedAt: "2026-08-05T14:59:00Z",
              endedAt: "2026-08-05T14:59:30Z",
              terminal: true,
              outputBytes: 100,
            }),
            shellJob({
              jobId: "job_turn_2",
              type: "delegate",
              startedAt: "2026-08-05T14:59:30Z",
              lastOutputAt: "2026-08-05T15:00:00Z",
              outputBytes: 412,
            }),
          ],
        })}
        now={NOW}
      />,
    );
    expect(screen.getByText(`running 12s · 512b · started ${localHHMM("2026-08-05T14:59:00Z")}`)).toBeTruthy();
  });

  test("terminal row meta drops the duplicated runtime and a successful exit code", () => {
    render(
      <ActivityRowDetail
        row={jobRow({
          status: "completed",
          terminal: true,
          exitCode: 0,
          outputBytes: 2048,
          startedAt: "2026-08-05T14:00:00Z",
          endedAt: "2026-08-05T14:02:30Z",
        })}
        now={NOW}
      />,
    );
    // The dense row already shows the 2m runtime, and exit 0 is the expected
    // case: the meta line is down to just the output size ("Nb", not
    // "N output bytes").
    expect(screen.getByText("2048b")).toBeTruthy();
    expect(screen.queryByText(/duration/)).toBeNull();
    expect(screen.queryByText(/exit/)).toBeNull();
  });

  test("terminal row meta keeps a non-zero exit code", () => {
    render(
      <ActivityRowDetail
        row={jobRow({
          status: "completed",
          terminal: true,
          exitCode: 1,
          outputBytes: 41,
          startedAt: "2026-08-05T14:00:00Z",
          endedAt: "2026-08-05T14:02:30Z",
        })}
        now={NOW}
      />,
    );
    expect(screen.getByText("exit 1 · 41b")).toBeTruthy();
  });

  test("a terminal row with no parseable span falls back to its status text", () => {
    render(
      <ActivityRowDetail
        row={jobRow({ status: "failed", terminal: true, outputBytes: 7, startedAt: "", endedAt: undefined })}
        now={NOW}
      />,
    );
    expect(screen.getByText("failed · 7b")).toBeTruthy();
  });

  test("a shell job row with output fetches a bounded tail and renders its ANSI escapes as styled runs", async () => {
    jobOutput.mockResolvedValue({ tail: "[32mok[39m\n[2mPASS[22m\n", totalBytes: 8, retainedStart: 0 });
    render(
      <ActivityRowDetail
        row={jobRow(
          { command: "go test ./...", hasOutput: true },
          { live: true, transcriptRef: "job:job_x", parentRef: "ref_root" },
        )}
        now={NOW}
      />,
    );
    // The escapes resolve to styled text: no literal "[32m" noise survives.
    expect(await screen.findByText("ok")).toBeTruthy();
    expect(await screen.findByText("PASS")).toBeTruthy();
    expect(screen.queryByText(/\[32m|\[2m/)).toBeNull();
    // The preview asks for a bounded tail: the latest couple hundred bytes,
    // not the daemon's default window.
    expect(jobOutput).toHaveBeenCalledWith("ref_root", "job_x", undefined, 256);
  });

  test("a job row without output, and delegate rows, fetch no preview", () => {
    const { rerender } = render(<ActivityRowDetail row={jobRow({ hasOutput: false }, { live: true })} now={NOW} />);
    rerender(<ActivityRowDetail row={delegateRow({ mandate: "Inspect the repo" })} now={NOW} />);
    expect(jobOutput).not.toHaveBeenCalled();
  });

  test("a failed or empty preview fetch renders nothing", async () => {
    jobOutput.mockRejectedValue(new Error("job not found"));
    render(<ActivityRowDetail row={jobRow({ command: "npm test", hasOutput: true }, { live: true })} now={NOW} />);
    // The command and meta still render; no error surface appears for the preview.
    expect(screen.getByText("npm test")).toBeTruthy();
    await vi.waitFor(() => expect(jobOutput).toHaveBeenCalled());
    expect(document.querySelector("pre")).toBeNull();
  });
});
