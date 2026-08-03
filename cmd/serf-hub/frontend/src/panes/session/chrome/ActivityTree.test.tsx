import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { openTranscript } from "../transcript/openTranscript";
import { ActivityTree } from "./ActivityTree";
import type { ActivityTree as ActivityTreeData } from "./activityData";

vi.mock("../transcript/openTranscript", () => ({
  openTranscript: vi.fn(),
}));

const TREE: ActivityTreeData = {
  revision: 1,
  root: {
    kind: "session",
    sessionId: "sess_root",
    ref: "ref_root",
    label: "Root session",
    aggregate: "running",
    counts: { active: 1, failed: 1, completed: 2, complete: true },
    entries: [
      {
        kind: "delegate",
        delegate: {
          delegateId: "dlg_1",
          childSessionId: "sess_child",
          childRef: "ref_child",
          mandate: "Inspect the repo",
          turns: [
            {
              jobId: "job_delegate_1",
              ownerSessionId: "sess_root",
              ownerRef: "ref_root",
              type: "delegate",
              status: "running",
              terminal: false,
              background: true,
              hasOutput: true,
              description: "delegate turn",
              startedAt: "2026-08-03T00:00:00Z",
              outputBytes: 7,
            },
            {
              jobId: "job_delegate_2",
              ownerSessionId: "sess_root",
              ownerRef: "ref_root",
              type: "delegate",
              status: "failed",
              terminal: true,
              background: true,
              hasOutput: true,
              description: "delegate report",
              startedAt: "2026-08-03T00:01:00Z",
              endedAt: "2026-08-03T00:02:00Z",
              outputBytes: 9,
            },
          ],
          child: {
            kind: "session",
            sessionId: "sess_child",
            ref: "ref_child",
            label: "Child session",
            aggregate: "running",
            counts: { active: 1, failed: 0, completed: 0, complete: true },
            entries: [
              {
                kind: "shell",
                job: {
                  jobId: "job_child_shell",
                  ownerSessionId: "sess_child",
                  ownerRef: "ref_child",
                  type: "shell",
                  status: "quarantined",
                  terminal: false,
                  background: false,
                  hasOutput: false,
                  description: "child shell",
                transcriptRef: "job:job_child_shell",
                  startedAt: "2026-08-03T00:03:00Z",
                  outputBytes: 0,
                },
              },
            ],
            branch: {},
          },
          branch: {},
        },
      },
      {
        kind: "shell",
        job: {
          jobId: "job_root_done",
          ownerSessionId: "sess_root",
          ownerRef: "ref_root",
          type: "shell",
          status: "completed",
          terminal: true,
          background: false,
          hasOutput: false,
          description: "root shell",
          transcriptRef: "job:job_root_done",
          startedAt: "2026-08-03T00:04:00Z",
          endedAt: "2026-08-03T00:05:00Z",
          outputBytes: 0,
        },
      },
    ],
    branch: {},
  },
};

function Host({ expanded = ["delegate:dlg_1"] }: { expanded?: string[] }) {
  const [expandedIDs, setExpandedIDs] = useState(expanded);
  const [selectedID, setSelectedID] = useState<string | undefined>();
  return (
    <ActivityTree
      tree={TREE}
      expandedIDs={expandedIDs}
      selectedID={selectedID}
      onExpandedChange={setExpandedIDs}
      onSelect={setSelectedID}
    />
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("ActivityTree", () => {
  test("renders tree semantics, roving tabindex, and keyboard navigation", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const onExpandedChange = vi.fn();
    render(
      <ActivityTree
        tree={TREE}
        expandedIDs={["delegate:dlg_1"]}
        selectedID={undefined}
        onExpandedChange={onExpandedChange}
        onSelect={onSelect}
      />,
    );

    const tree = screen.getByRole("tree");
    expect(tree).toBeTruthy();
    const root = screen.getByRole("treeitem", { name: /root session/i });
    expect(root.getAttribute("aria-level")).toBe("1");
    expect(root.getAttribute("aria-expanded")).toBe("false");
    expect(root.getAttribute("tabindex")).toBe("0");

    root.focus();
    await user.keyboard("{ArrowRight}{ArrowDown}{Enter}");
    expect(onExpandedChange).toHaveBeenCalledWith(expect.arrayContaining(["session:sess_root", "delegate:dlg_1"]));
    expect(onSelect).toHaveBeenCalledWith("delegate:dlg_1");

    cleanup();
    render(<Host />);
    const hostedRoot = screen.getByRole("treeitem", { name: /root session/i });
    hostedRoot.focus();
    await user.keyboard("{ArrowRight}{ArrowDown}{ArrowDown}{ArrowUp}{Space}");
    expect(screen.getByRole("treeitem", { name: /inspect the repo/i }).getAttribute("tabindex")).toBe("0");
    expect(screen.getByRole("treeitem", { name: /inspect the repo/i }).getAttribute("aria-selected")).toBe("true");
    await user.keyboard("{ArrowLeft}");
    expect(screen.getByRole("treeitem", { name: /root session/i })).toBeTruthy();
  });

  test("renders status text beside every indicator and keeps unknown status neutral", async () => {
    const user = userEvent.setup();
    render(<Host expanded={["session:sess_root", "delegate:dlg_1", "session:sess_child"]} />);

    expect(screen.getAllByText(/^running$/i).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/^failed$/i).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/^completed$/i).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/^quarantined$/i).length).toBeGreaterThan(0);
    expect(screen.getByLabelText("Idle")).toBeTruthy();

    screen.getByRole("treeitem", { name: /inspect the repo/i }).focus();
    await user.keyboard("{ArrowRight}");
    expect(screen.getByRole("treeitem", { name: /child session/i }).getAttribute("tabindex")).toBe("0");
  });

  test("renders transcript actions for session, delegate, and shell rows with correct refs", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(
      <ActivityTree
        tree={TREE}
        expandedIDs={["session:sess_root", "delegate:dlg_1", "session:sess_child"]}
        selectedID={undefined}
        onExpandedChange={vi.fn()}
        onSelect={onSelect}
      />,
    );

    expect(screen.getAllByRole("button", { name: "Open transcript beside" })).toHaveLength(5);

    await user.click(
      within(screen.getByRole("treeitem", { name: /root session/i })).getByRole("button", {
        name: "Open transcript beside",
      }),
    );
    await user.click(
      within(screen.getByRole("treeitem", { name: /inspect the repo/i })).getByRole("button", {
        name: "Open transcript beside",
      }),
    );
    await user.click(
      within(screen.getByRole("treeitem", { name: /child session/i })).getByRole("button", {
        name: "Open transcript beside",
      }),
    );
    await user.click(
      within(screen.getByRole("treeitem", { name: /child shell/i })).getByRole("button", {
        name: "Open transcript beside",
      }),
    );
    await user.click(
      within(screen.getByRole("treeitem", { name: /root shell/i })).getByRole("button", {
        name: "Open transcript beside",
      }),
    );

    expect(onSelect).not.toHaveBeenCalled();
    expect(openTranscript).toHaveBeenNthCalledWith(1, "ref_root", undefined);
    expect(openTranscript).toHaveBeenNthCalledWith(2, "ref_child", "ref_root");
    expect(openTranscript).toHaveBeenNthCalledWith(3, "ref_child", undefined);
    expect(openTranscript).toHaveBeenNthCalledWith(4, "job:job_child_shell", "ref_child");
    expect(openTranscript).toHaveBeenNthCalledWith(5, "job:job_root_done", "ref_root");
  });
});
