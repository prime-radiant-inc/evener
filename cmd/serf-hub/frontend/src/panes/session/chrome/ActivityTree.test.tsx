import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement, useState } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import * as openTranscriptModule from "../transcript/openTranscript";
import { ActivityTree } from "./ActivityTree";
import type { ActivityTree as ActivityTreeData } from "./activityData";

// vi.spyOn, not vi.mock: ActivityPanel.test.tsx statically imports ActivityTree
// (this file's own subject) without ever mocking this module, so under a
// shared module registry ActivityTree.tsx's own `import { openTranscript }`
// binding is already resolved to the real function by the time this file's
// tests run - a vi.mock() factory registered this late replaces what THIS
// file's test-level import resolves to, but not what the already-loaded
// ActivityTree.tsx calls internally. Spying on the real module's own export
// patches the one binding every importer (this file's assertions AND
// ActivityTree.tsx's internal calls) actually shares, regardless of import
// order. OpenTranscriptButton is stubbed: it lives in the SAME module as
// openTranscript, so its internal call uses the module-local binding and the
// spy above would never observe it. The stub records the props the tree
// passes (iconOnly included) and routes its click to the spied openTranscript;
// the real button's icon-only rendering and click behavior are covered in
// openTranscript.test.tsx, where the workspace harness exists.
let openTranscript: typeof openTranscriptModule.openTranscript;
let openButtonProps: Array<{
  transcriptRef: string;
  parentRef?: string;
  iconOnly?: boolean;
}>;
beforeEach(() => {
  openTranscript = vi.spyOn(openTranscriptModule, "openTranscript").mockImplementation(() => {});
  openButtonProps = [];
  vi.spyOn(openTranscriptModule, "OpenTranscriptButton").mockImplementation((props) => {
    openButtonProps.push(props);
    return createElement("button", {
      type: "button",
      "aria-label": props.label ?? "Open transcript",
      onClick: (event: { stopPropagation: () => void }) => {
        event.stopPropagation();
        openTranscript(props.transcriptRef, props.parentRef);
      },
    });
  });
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

// The fold row's accessible name matches its visible text: the failed count
// is part of the aria-label whenever failedCount > 0.
const FOLD_NAME = "2 inactive · 1 failed";

function setupUser() {
  return userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
}

function Host({ initialExpanded = [] }: { initialExpanded?: string[] }) {
  const [expandedFoldIDs, setExpandedFoldIDs] = useState<string[]>(initialExpanded);
  return (
    <ActivityTree
      tree={TREE}
      expandedFoldIDs={expandedFoldIDs}
      onToggleFold={(foldID) =>
        setExpandedFoldIDs((current) =>
          current.includes(foldID) ? current.filter((id) => id !== foldID) : [...current, foldID],
        )
      }
    />
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("ActivityTree", () => {
  test("renders one dense row per live entry with kind glyph and meta", () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    render(<ActivityTree tree={TREE} expandedFoldIDs={[]} onToggleFold={vi.fn()} />);

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
    render(<ActivityTree tree={TREE} expandedFoldIDs={[]} onToggleFold={onToggleFold} />);

    const foldRow = screen.getByRole("treeitem", { name: FOLD_NAME });
    // The accessible name carries the failure count, matching the visible text.
    expect(foldRow.getAttribute("aria-label")).toBe("2 inactive · 1 failed");
    expect(foldRow.getAttribute("aria-expanded")).toBe("false");
    expect(foldRow.textContent).toContain("1 failed");
    expect(screen.queryByRole("treeitem", { name: "finished build" })).toBeNull();

    await user.click(foldRow);
    expect(onToggleFold).toHaveBeenCalledWith(FOLD_ID);
    expect(openTranscript).not.toHaveBeenCalled();
  });

  test("expanded fold reveals terminal rows with duration meta; the kind glyph carries the failure", () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    render(<ActivityTree tree={TREE} expandedFoldIDs={[FOLD_ID]} onToggleFold={vi.fn()} />);

    const foldRow = screen.getByRole("treeitem", { name: FOLD_NAME });
    expect(foldRow.getAttribute("aria-expanded")).toBe("true");

    const doneRow = screen.getByRole("treeitem", { name: "finished build" });
    expect(doneRow.textContent).toContain("1m");

    const failedRow = screen.getByRole("treeitem", { name: "broken lint" });
    expect(failedRow.textContent).toContain("2m");
    // No "failed" text in the meta: the colored kind glyph says it instead.
    expect(failedRow.textContent).not.toContain("failed");
    const kindGlyph = within(failedRow).getByText("$");
    expect(kindGlyph.getAttribute("aria-label")).toBe("Failed");
  });

  test("the kind glyph carries the row's status hue and accessible name", () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    render(<ActivityTree tree={TREE} expandedFoldIDs={[FOLD_ID]} onToggleFold={vi.fn()} />);

    // Working rows color the glyph with the alive hue.
    const liveGlyph = within(screen.getByRole("treeitem", { name: "run tests" })).getByText("$");
    expect(liveGlyph.getAttribute("aria-label")).toBe("Working");
    expect(liveGlyph.className).toContain("kindAlive");

    // Failed rows color it danger; ended rows keep the default low ink.
    const failedGlyph = within(screen.getByRole("treeitem", { name: "broken lint" })).getByText("$");
    expect(failedGlyph.getAttribute("aria-label")).toBe("Failed");
    expect(failedGlyph.className).toContain("kindDanger");
    const endedGlyph = within(screen.getByRole("treeitem", { name: "finished build" })).getByText("$");
    expect(endedGlyph.getAttribute("aria-label")).toBe("Ended");
    expect(endedGlyph.className).not.toContain("kindDanger");
    expect(endedGlyph.className).not.toContain("kindAlive");
  });

  test("opening the fold reveals rows with their detail strips collapsed", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    const user = setupUser();
    render(<Host initialExpanded={[FOLD_ID]} />);

    // The fold click means "show the list", not "expand every child": the
    // revealed rows' strips stay closed. The live top-level rows' strips are
    // open by default, so the probe is the terminal strip's exact meta text.
    const doneRow = screen.getByRole("treeitem", { name: "finished build" });
    const chevron = within(doneRow).getByRole("button", { name: /show details for finished build/i });
    expect(chevron.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText("0b")).toBeNull();

    // Expanding one child reveals just that row's strip.
    await user.click(chevron);
    expect(chevron.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText("0b")).toBeTruthy();
  });

  test("rows with a transcript ref carry an icon-only open button between the title and the meta", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    const user = setupUser();
    render(<ActivityTree tree={TREE} expandedFoldIDs={[FOLD_ID]} onToggleFold={vi.fn()} />);

    const shellRow = screen.getByRole("treeitem", { name: "run tests" });
    const openButton = within(shellRow).getByRole("button", { name: "Open transcript" });
    // The tree asks for the icon-only form; the real component's glyph-only
    // rendering is covered in openTranscript.test.tsx.
    expect(openButtonProps.every((props) => props.iconOnly === true)).toBe(true);
    // The button ends the title: after the name text, before the meta cluster.
    const nameText = within(shellRow).getByText("run tests");
    const metaText = within(shellRow).getByText("12s");
    expect(nameText.compareDocumentPosition(openButton) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(openButton.compareDocumentPosition(metaText) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    // The button opens the row's transcript without also activating the row.
    await user.click(openButton);
    expect(openTranscript).toHaveBeenCalledTimes(1);
    expect(openTranscript).toHaveBeenCalledWith("job:job_shell_live", "ref_root");

    // A row with no transcript ref (broken lint) gets no open button at all.
    const failedRow = screen.getByRole("treeitem", { name: "broken lint" });
    expect(within(failedRow).queryByRole("button", { name: "Open transcript" })).toBeNull();
  });

  test("clicking rows opens their transcripts with the row's parent ref", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    const user = setupUser();
    render(<ActivityTree tree={TREE} expandedFoldIDs={[]} onToggleFold={vi.fn()} />);

    await user.click(screen.getByRole("treeitem", { name: "run tests" }));
    expect(openTranscript).toHaveBeenNthCalledWith(1, "job:job_shell_live", "ref_root");

    await user.click(screen.getByRole("treeitem", { name: "Inspect the repo" }));
    expect(openTranscript).toHaveBeenNthCalledWith(2, "ref_child", "ref_root");
  });

  test("top-level rows render their detail strips expanded by default", () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    render(<ActivityTree tree={TREE} expandedFoldIDs={[]} onToggleFold={vi.fn()} />);

    // The shell row's strip shows its command; its chevron reads "hide".
    expect(screen.getByText("npm test")).toBeTruthy();
    const shellRow = screen.getByRole("treeitem", { name: "run tests" });
    const shellChevron = within(shellRow).getByRole("button", { name: /hide details for run tests/i });
    expect(shellChevron.getAttribute("aria-expanded")).toBe("true");

    // The delegate row's strip is open too: several strips coexist (both
    // live strips render the same meta line, hence the pair of matches).
    const delegateRow = screen.getByRole("treeitem", { name: "Inspect the repo" });
    expect(within(delegateRow).getByRole("button", { name: /hide details for inspect the repo/i })).toBeTruthy();
    expect(screen.getAllByText(/running 12s · 0b · started \d{2}:\d{2}/)).toHaveLength(2);
  });

  test("nested rows stay collapsed by default", () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    const nestedTree: ActivityTreeData = {
      revision: 1,
      root: {
        ...TREE.root,
        entries: [
          {
            kind: "delegate",
            delegate: {
              delegateId: "dlg_parent",
              childSessionId: "sess_child",
              childRef: "ref_child",
              mandate: "Parent agent",
              turns: [
                shellJob({
                  jobId: "job_parent_turn",
                  type: "delegate",
                  description: "delegate turn",
                  lastOutputAt: "2026-08-05T15:00:00Z",
                }),
              ],
              branch: {},
              child: {
                kind: "session",
                sessionId: "sess_child",
                ref: "ref_child",
                label: "Child session",
                aggregate: "working",
                counts: { active: 1, failed: 0, completed: 0, complete: false },
                entries: [
                  {
                    kind: "shell",
                    job: shellJob({
                      jobId: "job_nested",
                      description: "nested work",
                      command: "make nested",
                      transcriptRef: "job:job_nested",
                      lastOutputAt: "2026-08-05T15:00:00Z",
                    }),
                  },
                ],
                branch: {},
              },
            },
          },
        ],
      },
    };
    render(<ActivityTree tree={nestedTree} expandedFoldIDs={[]} onToggleFold={vi.fn()} />);

    // The top-level delegate row is expanded by default…
    const delegateRow = screen.getByRole("treeitem", { name: "Parent agent" });
    expect(within(delegateRow).getByRole("button", { name: /hide details for parent agent/i })).toBeTruthy();

    // …but the nested shell row is not: collapsed chevron, no command strip.
    const nestedRow = screen.getByRole("treeitem", { name: "nested work" });
    const nestedChevron = within(nestedRow).getByRole("button", { name: /show details for nested work/i });
    expect(nestedChevron.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText("make nested")).toBeNull();
  });

  test("chevrons toggle each row's detail strip independently without opening transcripts", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    const user = setupUser();
    render(<ActivityTree tree={TREE} expandedFoldIDs={[]} onToggleFold={vi.fn()} />);

    // Both top-level strips start open; closing one leaves the other open.
    const shellRow = screen.getByRole("treeitem", { name: "run tests" });
    await user.click(within(shellRow).getByRole("button", { name: /hide details for run tests/i }));
    expect(screen.queryByText("npm test")).toBeNull();
    expect(screen.getByText(/running 12s · 0b · started \d{2}:\d{2}/)).toBeTruthy();
    expect(openTranscript).not.toHaveBeenCalled();

    // Clicking again reopens exactly that row's strip.
    await user.click(within(shellRow).getByRole("button", { name: /show details for run tests/i }));
    expect(screen.getByText("npm test")).toBeTruthy();
    expect(openTranscript).not.toHaveBeenCalled();
  });

  test("keyboard: arrows move focus, Enter activates, Right/Left toggles detail", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    const user = setupUser();
    const onToggleFold = vi.fn();
    render(<ActivityTree tree={TREE} expandedFoldIDs={[]} onToggleFold={onToggleFold} />);

    const shellRow = screen.getByRole("treeitem", { name: "run tests" });
    const delegateRow = screen.getByRole("treeitem", { name: "Inspect the repo" });
    const foldRow = screen.getByRole("treeitem", { name: FOLD_NAME });

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

    // Top-level rows start expanded: ArrowLeft closes the strip, ArrowRight reopens it.
    expect(screen.getByText("npm test")).toBeTruthy();
    await user.keyboard("{ArrowLeft}");
    expect(screen.queryByText("npm test")).toBeNull();
    await user.keyboard("{ArrowRight}");
    expect(screen.getByText("npm test")).toBeTruthy();

    // Enter on the fold row toggles the fold.
    foldRow.focus();
    await user.keyboard("{Enter}");
    expect(onToggleFold).toHaveBeenCalledWith(FOLD_ID);
    expect(openTranscript).toHaveBeenCalledTimes(1);
  });

  test("keyboard: arrows still navigate rows when focus sits on a row chevron button", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    const user = setupUser();
    render(<ActivityTree tree={TREE} expandedFoldIDs={[]} onToggleFold={vi.fn()} />);

    const shellRow = screen.getByRole("treeitem", { name: "run tests" });
    const delegateRow = screen.getByRole("treeitem", { name: "Inspect the repo" });

    // Firefox and Safari focus a button when it is clicked; ArrowDown from
    // that focus must still move to the next row, not be swallowed.
    const chevron = within(shellRow).getByRole("button", { name: /hide details for run tests/i });
    await user.click(chevron);
    expect(document.activeElement).toBe(chevron);
    await user.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(delegateRow);

    // Enter on the chevron remains the chevron's own activation (the detail
    // strip), never the row's transcript activation.
    chevron.focus();
    await user.keyboard("{Enter}");
    expect(openTranscript).not.toHaveBeenCalled();
  });

  test("host-driven fold toggle reveals and re-hides terminal rows", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    const user = setupUser();
    render(<Host />);

    expect(screen.queryByRole("treeitem", { name: "finished build" })).toBeNull();
    await user.click(screen.getByRole("treeitem", { name: FOLD_NAME }));
    expect(screen.getByRole("treeitem", { name: "finished build" })).toBeTruthy();
    await user.click(screen.getByRole("treeitem", { name: FOLD_NAME }));
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
    render(<ActivityTree tree={oldDaemonTree} expandedFoldIDs={[]} onToggleFold={vi.fn()} />);

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
    render(<ActivityTree tree={continuedTree} expandedFoldIDs={[]} onToggleFold={vi.fn()} onContinue={onContinue} />);

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
        expandedFoldIDs={[]}
        onToggleFold={vi.fn()}
        onContinue={vi.fn()}
        continuationFailures={{ "session:sess_root": "Couldn't load more." }}
        loadingContinuationID={undefined}
      />,
    );
    expect(screen.getByText("Couldn't load more.")).toBeTruthy();
  });
});
