import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { openTranscript } from "../transcript/openTranscript";
import { ActivityTree } from "./ActivityTree";
import type { ActivityTree as ActivityTreeData } from "./activityData";

vi.mock("../transcript/openTranscript", async () => {
  const { createElement } = await import("react");
  return {
    openTranscript: vi.fn(),
    OpenTranscriptButton: (props: { transcriptRef: string; parentRef?: string }) =>
      createElement(
        "button",
        { type: "button", "data-transcript-ref": props.transcriptRef, "data-parent-ref": props.parentRef },
        "open",
      ),
  };
});

// Pinned clock: every quiet-time assertion below measures against this instant.
const NOW = new Date("2026-08-05T15:00:12.000Z");

function shellJob(overrides: Record<string, unknown>) {
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
  };
}

const TREE: ActivityTreeData = {
  revision: 1,
  root: {
    kind: "session",
    sessionId: "sess_root",
    ref: "ref_root",
    label: "Root session",
    aggregate: "running",
    counts: { active: 2, failed: 1, completed: 1, complete: true },
    entries: [
      {
        kind: "shell",
        job: shellJob({
          jobId: "job_shell_live",
          description: "run tests",
          command: "npm test",
          transcriptRef: "job:job_shell_live",
          lastOutputAt: "2026-08-05T15:00:00Z",
        }),
      },
      {
        kind: "delegate",
        delegate: {
          delegateId: "dlg_live",
          childSessionId: "sess_child",
          childRef: "ref_child",
          mandate: "Inspect the repo",
          usage: { inputTokens: 41_000, outputTokens: 6_000 },
          turns: [
            shellJob({
              jobId: "job_dlg_turn",
              type: "delegate",
              description: "delegate turn",
              lastOutputAt: "2026-08-05T15:00:00Z",
            }),
          ],
          branch: {},
        },
      },
      {
        kind: "shell",
        job: shellJob({
          jobId: "job_done",
          description: "finished build",
          status: "completed",
          terminal: true,
          transcriptRef: "job:job_done",
          startedAt: "2026-08-05T14:00:00Z",
          endedAt: "2026-08-05T14:01:00Z",
        }),
      },
      {
        kind: "shell",
        job: shellJob({
          jobId: "job_failed",
          description: "broken lint",
          status: "failed",
          terminal: true,
          startedAt: "2026-08-05T14:00:00Z",
          endedAt: "2026-08-05T14:02:00Z",
        }),
      },
    ],
    branch: {},
  },
};

const FOLD_ID = "session:sess_root:inactive-fold";

function setupUser() {
  return userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
}

function Host({ initialCollapsed = [FOLD_ID] }: { initialCollapsed?: string[] }) {
  const [collapsedFoldIDs, setCollapsedFoldIDs] = useState<string[]>(initialCollapsed);
  return (
    <ActivityTree
      tree={TREE}
      collapsedFoldIDs={collapsedFoldIDs}
      onToggleFold={(foldID) =>
        setCollapsedFoldIDs((current) =>
          current.includes(foldID) ? current.filter((id) => id !== foldID) : [...current, foldID],
        )
      }
    />
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.useRealTimers();
});

describe("ActivityTree", () => {
  test("renders one dense row per live entry with kind glyph and meta", () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    render(<ActivityTree tree={TREE} collapsedFoldIDs={[FOLD_ID]} onToggleFold={vi.fn()} />);

    const shellRow = screen.getByRole("treeitem", { name: "run tests" });
    expect(within(shellRow).getByText("$")).toBeTruthy();
    expect(shellRow.textContent).toContain("— · 12s");

    const delegateRow = screen.getByRole("treeitem", { name: "Inspect the repo" });
    expect(within(delegateRow).getByText("⌘")).toBeTruthy();
    expect(delegateRow.textContent).toContain("↑41k ↓6k · 12s");

    // One row per live entry plus the fold row: sessions never become rows.
    expect(screen.getAllByRole("treeitem")).toHaveLength(3);
  });

  test("terminal entries hide behind a fold row; clicking it toggles the fold only", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    const user = setupUser();
    const onToggleFold = vi.fn();
    render(<ActivityTree tree={TREE} collapsedFoldIDs={[FOLD_ID]} onToggleFold={onToggleFold} />);

    const foldRow = screen.getByRole("treeitem", { name: "2 inactive" });
    expect(foldRow.getAttribute("aria-expanded")).toBe("false");
    expect(foldRow.textContent).toContain("1 failed");
    expect(screen.queryByRole("treeitem", { name: "finished build" })).toBeNull();

    await user.click(foldRow);
    expect(onToggleFold).toHaveBeenCalledWith(FOLD_ID);
    expect(openTranscript).not.toHaveBeenCalled();
  });

  test("expanded fold reveals terminal rows with duration and failed meta", () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    render(<ActivityTree tree={TREE} collapsedFoldIDs={[]} onToggleFold={vi.fn()} />);

    const foldRow = screen.getByRole("treeitem", { name: "2 inactive" });
    expect(foldRow.getAttribute("aria-expanded")).toBe("true");

    const doneRow = screen.getByRole("treeitem", { name: "finished build" });
    expect(doneRow.textContent).toContain("1m");

    const failedRow = screen.getByRole("treeitem", { name: "broken lint" });
    expect(failedRow.textContent).toContain("2m");
    expect(failedRow.textContent).toContain("failed");
  });

  test("clicking rows opens their transcripts with the row's parent ref", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    const user = setupUser();
    render(<ActivityTree tree={TREE} collapsedFoldIDs={[FOLD_ID]} onToggleFold={vi.fn()} />);

    await user.click(screen.getByRole("treeitem", { name: "run tests" }));
    expect(openTranscript).toHaveBeenNthCalledWith(1, "job:job_shell_live", "ref_root");

    await user.click(screen.getByRole("treeitem", { name: "Inspect the repo" }));
    expect(openTranscript).toHaveBeenNthCalledWith(2, "ref_child", "ref_root");
  });

  test("chevron toggles one inline detail strip at a time without opening transcripts", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    const user = setupUser();
    render(<ActivityTree tree={TREE} collapsedFoldIDs={[FOLD_ID]} onToggleFold={vi.fn()} />);

    const shellRow = screen.getByRole("treeitem", { name: "run tests" });
    await user.click(within(shellRow).getByRole("button", { name: /show details for run tests/i }));
    expect(screen.getByText("npm test")).toBeTruthy();
    expect(openTranscript).not.toHaveBeenCalled();

    const delegateRow = screen.getByRole("treeitem", { name: "Inspect the repo" });
    await user.click(within(delegateRow).getByRole("button", { name: /show details for inspect the repo/i }));
    // The strip moved: the shell command is gone, the delegate strip shows
    // the mandate plus the Task 8 live meta (quiet age, bytes, started time).
    expect(screen.queryByText("npm test")).toBeNull();
    expect(screen.getAllByText("Inspect the repo").length).toBeGreaterThan(0);
    expect(screen.getByText(/running 12s · 0 output bytes · started \d{2}:\d{2}/)).toBeTruthy();
    expect(openTranscript).not.toHaveBeenCalled();
  });

  test("keyboard: arrows move focus, Enter activates, Right/Left toggles detail", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    const user = setupUser();
    const onToggleFold = vi.fn();
    render(<ActivityTree tree={TREE} collapsedFoldIDs={[FOLD_ID]} onToggleFold={onToggleFold} />);

    const shellRow = screen.getByRole("treeitem", { name: "run tests" });
    const delegateRow = screen.getByRole("treeitem", { name: "Inspect the repo" });
    const foldRow = screen.getByRole("treeitem", { name: "2 inactive" });

    shellRow.focus();
    await user.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(delegateRow);
    await user.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(foldRow);
    await user.keyboard("{ArrowUp}");
    expect(document.activeElement).toBe(delegateRow);
    await user.keyboard("{ArrowUp}");
    expect(document.activeElement).toBe(shellRow);

    // Enter on a job row opens its transcript.
    await user.keyboard("{Enter}");
    expect(openTranscript).toHaveBeenCalledWith("job:job_shell_live", "ref_root");

    // ArrowRight opens the detail strip, ArrowLeft closes it.
    await user.keyboard("{ArrowRight}");
    expect(screen.getByText("npm test")).toBeTruthy();
    await user.keyboard("{ArrowLeft}");
    expect(screen.queryByText("npm test")).toBeNull();

    // Enter on the fold row toggles the fold.
    foldRow.focus();
    await user.keyboard("{Enter}");
    expect(onToggleFold).toHaveBeenCalledWith(FOLD_ID);
    expect(openTranscript).toHaveBeenCalledTimes(1);
  });

  test("host-driven fold toggle reveals and re-hides terminal rows", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    const user = setupUser();
    render(<Host />);

    expect(screen.queryByRole("treeitem", { name: "finished build" })).toBeNull();
    await user.click(screen.getByRole("treeitem", { name: "2 inactive" }));
    expect(screen.getByRole("treeitem", { name: "finished build" })).toBeTruthy();
    await user.click(screen.getByRole("treeitem", { name: "2 inactive" }));
    expect(screen.queryByRole("treeitem", { name: "finished build" })).toBeNull();
  });

  test("old-daemon shape: no usage and no lastOutputAt renders dash plus quiet from startedAt", () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    const oldDaemonTree: ActivityTreeData = {
      revision: 1,
      root: {
        ...TREE.root,
        entries: [
          {
            kind: "delegate",
            delegate: {
              delegateId: "dlg_old",
              childSessionId: "sess_old_child",
              childRef: "ref_old_child",
              turns: [
                shellJob({
                  jobId: "job_old_turn",
                  type: "delegate",
                  description: "delegate turn",
                  startedAt: "2026-08-05T15:00:00Z",
                }),
              ],
              branch: {},
            },
          },
        ],
      },
    };
    render(<ActivityTree tree={oldDaemonTree} collapsedFoldIDs={[]} onToggleFold={vi.fn()} />);

    const row = screen.getByRole("treeitem", { name: "sess_old_child" });
    expect(row.textContent).toContain("— · 12s");
    expect(row.textContent).not.toContain("↑");
    expect(row.textContent).not.toContain("↓");
  });

  test("continuation strip renders after the session's rows and calls onContinue", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    const user = setupUser();
    const onContinue = vi.fn();
    const continuedTree: ActivityTreeData = {
      revision: 1,
      root: { ...TREE.root, branch: { continuation: "tok_root" } },
    };
    render(
      <ActivityTree tree={continuedTree} collapsedFoldIDs={[FOLD_ID]} onToggleFold={vi.fn()} onContinue={onContinue} />,
    );

    await user.click(screen.getByRole("button", { name: "Load more" }));
    expect(onContinue).toHaveBeenCalledWith("session:sess_root", "tok_root");
    expect(openTranscript).not.toHaveBeenCalled();
  });

  test("continuation failure message renders when the load failed", () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    const continuedTree: ActivityTreeData = {
      revision: 1,
      root: { ...TREE.root, branch: { continuation: "tok_root" } },
    };
    render(
      <ActivityTree
        tree={continuedTree}
        collapsedFoldIDs={[FOLD_ID]}
        onToggleFold={vi.fn()}
        onContinue={vi.fn()}
        continuationFailures={{ "session:sess_root": "Couldn't load more." }}
        loadingContinuationID={undefined}
      />,
    );
    expect(screen.getByText("Couldn't load more.")).toBeTruthy();
  });
});
