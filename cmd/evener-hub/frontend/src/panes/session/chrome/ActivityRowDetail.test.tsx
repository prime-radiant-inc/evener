import type { ActivityDelegate, ActivityJob, ActivitySessionNode, EvenerDelegateInfo } from "@evener/appwire-client";
import { type ActivityDelegateRow, type ActivityJobRow, buildEntityView } from "@evener/appwire-client";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { threadsStore } from "../../../stores/threads";
import { EntityViewsProvider } from "../../../transcriptDisplay/entityViews";
import { ActivityRowDetail } from "./ActivityRowDetail";
import { detailLineByText } from "./detailLine.testFixture";

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
    ownerSessionId: "sess_root",
    rootSessionId: "sess_root",
    childSessionId: "sess_child",
    childRef: "ref_child",
    transcriptRef: "ref_child",
    type: "delegate",
    lifecycle: "running",
    phase: "running",
    status: "running",
    projectionRevision: 1,
    terminal: false,
    resumable: true,
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

// The id the strip's delegate line names, and the view the session's entity
// map holds for it. buildEntityView is the chrome's own builder (useEntityView
// wraps it), so this is the same map ActivityPanelBody provides to the tree;
// the tests below wrap the strip in EntityViewsProvider because the strip
// lives outside the transcript subtree that provides the same map there.
const DELEGATE_ID = "dlg_034HQ2kSDXfKFq1mm3idL1";

function delegateEntities(id: string, task: string) {
  const delegate: EvenerDelegateInfo = {
    delegateId: id,
    ownerSessionId: "sess_root",
    rootSessionId: "sess_root",
    childSessionId: "sess_child",
    transcriptRef: "ref_child",
    type: "delegate",
    lifecycle: "running",
    phase: "running",
    status: "running",
    resumable: true,
    needsAttention: false,
    projectionRevision: 1,
    task,
  };
  return buildEntityView({ sessionRef: "ref_root", delegates: [delegate], turns: [], stale: false, ended: false });
}

// Every provider-wrapped delegate-line case below renders the same strip over
// the same fixture map; only the assertions differ.
function renderDelegateStrip() {
  return render(
    <EntityViewsProvider entities={delegateEntities(DELEGATE_ID, "Inspect the repo")}>
      <ActivityRowDetail row={delegateRow({ delegateId: DELEGATE_ID, mandate: "Inspect the repo" })} now={NOW} />
    </EntityViewsProvider>,
  );
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
  test("terminal delegate detail displays outcome instead of idle lifecycle status", () => {
    render(
      <ActivityRowDetail
        row={delegateRow(
          { lifecycle: "idle", phase: "idle", status: "idle", outcome: "failed", terminal: true },
          { live: false },
        )}
        now={NOW}
      />,
    );

    expect(screen.getByText(/failed · 0b/)).toBeTruthy();
  });

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

  // The delegate line names a real entity, so its id renders as the shared
  // entity card trigger rather than plain text. The line's words are otherwise
  // unchanged, the trigger takes no tab stop of its own (the strip lives inside
  // the row's own treeitem control - ruling R13), and it adds no second open
  // control to a row that already carries one.
  test("the delegate id renders as an embedded entity card trigger, not plain text", () => {
    const { container } = renderDelegateStrip();

    const line = detailLineByText(`Delegate ${DELEGATE_ID} · send · stop · status`, container);
    const trigger = within(line).getByTestId("entity-trigger");
    expect(trigger.textContent).toBe(DELEGATE_ID);
    // The provider is the only path that can mint the trigger (the strip takes
    // no `entities` prop), and it mints exactly one for the whole document.
    expect(screen.getAllByTestId("entity-trigger")).toHaveLength(1);
    expect(trigger.getAttribute("tabindex")).toBeNull();
    expect(within(line).queryByRole("button")).toBeNull();
  });

  // Resolving is not enough: the trigger has to open the card, which is what a
  // naive swap that silently fell back to plain text could never do.
  test("the delegate id's trigger opens the entity card", () => {
    vi.useFakeTimers();
    try {
      renderDelegateStrip();

      fireEvent.focus(screen.getByTestId("entity-trigger"));
      act(() => {
        vi.advanceTimersByTime(300);
      });

      const card = screen.getByRole("tooltip");
      expect(card.textContent).toContain("Delegate");
      expect(card.textContent).toContain("Inspect the repo");
    } finally {
      vi.useRealTimers();
    }
  });

  // No provider owns the map in a bare render (a direct strip, or the chrome
  // before its body mounts): the id stays the plain text it always was.
  test("with no entity provider the delegate id stays plain text", () => {
    const { container } = render(
      <ActivityRowDetail row={delegateRow({ delegateId: DELEGATE_ID, mandate: "Inspect the repo" })} now={NOW} />,
    );

    const line = detailLineByText(`Delegate ${DELEGATE_ID} · send · stop · status`, container);
    expect(within(line).queryByTestId("entity-trigger")).toBeNull();
    expect(line.textContent).toContain(DELEGATE_ID);
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

  test("a legacy terminal failed row states the display word in its meta", () => {
    render(
      <ActivityRowDetail
        row={jobRow({ status: "failed", reason: "exit_nonzero", exitCode: 2, outputBytes: 512 })}
        now={NOW}
      />,
    );
    expect(screen.getByText("Command failed · exit 2 · 512b")).toBeTruthy();
  });

  test("live delegate row meta uses stable timing and quiet evidence", () => {
    render(
      <ActivityRowDetail
        row={delegateRow({
          mandate: "Inspect the repo",
          runStartedAt: "2026-08-05T14:59:00Z",
          latestActivityAt: "2026-08-05T15:00:00Z",
          quietForMs: 12_000,
        })}
        now={NOW}
      />,
    );
    expect(screen.getByText(`running 12s · 0b · started ${localHHMM("2026-08-05T14:59:00Z")}`)).toBeTruthy();
  });

  // quietForMs is frozen at snapshot time, and a quiet delegate emits no
  // frames to refresh the snapshot: the displayed age must be re-derived
  // from the quiet anchor and the ticking `now`, never the frozen number.
  test("live delegate quiet age tracks the ticking clock, not the snapshot's quietForMs", () => {
    render(
      <ActivityRowDetail
        row={delegateRow({
          mandate: "Inspect the repo",
          runStartedAt: "2026-08-05T14:59:00Z",
          latestActivityAt: "2026-08-05T15:00:00Z",
          quietForMs: 12_000,
        })}
        now={NOW + 30_000}
      />,
    );
    expect(screen.getByText(`running 42s · 0b · started ${localHHMM("2026-08-05T14:59:00Z")}`)).toBeTruthy();
  });

  // A resumed run keeps the previous run's latestActivityAt until the child
  // reports again, and an anchor that does not parse is no anchor: either way
  // the quiet age measures from runStartedAt, never from the frozen snapshot.
  test.each([
    ["predates the run start", "2026-08-05T14:59:00Z", 72_000],
    ["does not parse", "not-a-timestamp", 5_000],
  ])(
    "live delegate quiet age anchors at the run start when latestActivityAt %s",
    (_case, latestActivityAt, quietForMs) => {
      render(
        <ActivityRowDetail
          row={delegateRow({
            mandate: "Inspect the repo",
            runStartedAt: "2026-08-05T15:00:00Z",
            latestActivityAt,
            quietForMs,
          })}
          now={NOW}
        />,
      );
      expect(screen.getByText(`running 12s · 0b · started ${localHHMM("2026-08-05T15:00:00Z")}`)).toBeTruthy();
    },
  );

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
