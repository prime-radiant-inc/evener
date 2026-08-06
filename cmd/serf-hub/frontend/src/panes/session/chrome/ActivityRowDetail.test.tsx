import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import { ActivityRowDetail } from "./ActivityRowDetail";
import type { ActivityDelegate, ActivityJob, ActivitySessionNode } from "./activityData";
import type { ActivityDelegateRow, ActivityJobRow } from "./activityRows";

vi.mock("../transcript/openTranscript", async () => {
  const { createElement } = await import("react");
  return {
    OpenTranscriptButton: (props: { transcriptRef: string; parentRef?: string }) =>
      createElement(
        "button",
        { type: "button", "data-transcript-ref": props.transcriptRef, "data-parent-ref": props.parentRef },
        "open",
      ),
  };
});

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
    transcriptRef: "ref_child",
    parentRef: "ref_root",
    ...rowOverrides,
  };
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
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
    expect(
      screen.getByText(`running 12s · 512 output bytes · started ${localHHMM("2026-08-05T14:58:00Z")}`),
    ).toBeTruthy();
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
    expect(
      screen.getByText(`running 12s · 512 output bytes · started ${localHHMM("2026-08-05T14:59:00Z")}`),
    ).toBeTruthy();
  });

  test("terminal row meta shows duration, exit code, and output bytes", () => {
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
    expect(screen.getByText("duration 2m · exit 0 · 2048 output bytes")).toBeTruthy();
  });

  test("OpenTranscriptButton receives the row's transcriptRef and parentRef", () => {
    render(
      <ActivityRowDetail
        row={jobRow(
          { transcriptRef: "job:job_y" },
          { live: true, transcriptRef: "job:job_y", parentRef: "ref_parent" },
        )}
        now={NOW}
      />,
    );
    const button = screen.getByRole("button", { name: "open" });
    expect(button.getAttribute("data-transcript-ref")).toBe("job:job_y");
    expect(button.getAttribute("data-parent-ref")).toBe("ref_parent");
  });

  test("a row with no transcript ref renders no transcript action", () => {
    render(<ActivityRowDetail row={jobRow({}, { live: true, transcriptRef: undefined })} now={NOW} />);
    expect(screen.queryByRole("button", { name: "open" })).toBeNull();
  });
});
