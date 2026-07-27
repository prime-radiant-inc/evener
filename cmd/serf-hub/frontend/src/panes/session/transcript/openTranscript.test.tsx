import { beforeAll, beforeEach, expect, test } from "vitest";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { openTranscript } from "./openTranscript";

beforeAll(async () => {
  await import("../");
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
});

function transcriptPanes(ref: string) {
  return workspaceStore
    .getState()
    .panes.filter((pane) => pane.type === "transcript" && (pane.params as { ref?: unknown }).ref === ref);
}

function sessionPane(ref: string) {
  return workspaceStore
    .getState()
    .panes.find((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === ref);
}

test("canonicalizes a child opened without a parent when its owning session is later known", () => {
  openTranscript("local:child");
  const first = transcriptPanes("local:child")[0];
  expect(first?.params).toEqual({ ref: "local:child" });

  openTranscript("local:child", "local:owner");

  const child = transcriptPanes("local:child");
  expect(child).toHaveLength(1);
  expect(child[0]?.params).toEqual({ ref: "local:child", parentRef: "local:owner" });
  expect(child[0]?.id).not.toBe(first?.id);
  expect(sessionPane("local:owner")?.slot).toBe("main");
  expect(child[0]?.slot).toBe("secondary");
  expect(workspaceStore.getState().focusedPaneId).toBe(child[0]?.id);
});

test("replaces a child pane with a different parent context and clears prior secondary panes", () => {
  openTranscript("local:child", "local:owner-a");
  openTranscript("local:other", "local:other-owner");
  expect(transcriptPanes("local:other")).toHaveLength(1);

  openTranscript("local:child", "local:owner-b");

  expect(transcriptPanes("local:child")).toHaveLength(1);
  expect(transcriptPanes("local:child")[0]?.params).toEqual({
    ref: "local:child",
    parentRef: "local:owner-b",
  });
  expect(transcriptPanes("local:other")).toHaveLength(0);
  expect(sessionPane("local:owner-b")?.slot).toBe("main");
  expect(workspaceStore.getState().focusedPaneId).toBe(transcriptPanes("local:child")[0]?.id);
});

test("focuses an already exact child pane without remounting it or duplicating it", () => {
  const owner = workspaceStore.getState().openPane("session", { ref: "local:owner" });
  const exact = workspaceStore.getState().openPane("transcript", { ref: "local:child", parentRef: "local:owner" });
  const unrelated = workspaceStore.getState().openPane("transcript", { ref: "local:other" });
  workspaceStore.getState().focusPane(unrelated);

  openTranscript("local:child", "local:owner");

  expect(transcriptPanes("local:child")).toHaveLength(1);
  expect(transcriptPanes("local:child")[0]?.id).toBe(exact);
  expect(sessionPane("local:owner")?.id).toBe(owner);
  expect(sessionPane("local:owner")?.slot).toBe("main");
  expect(workspaceStore.getState().focusedPaneId).toBe(exact);
  expect(transcriptPanes("local:other")).toHaveLength(1);
});

test("keeps no-parent opening deduped and usable without a desktop host", () => {
  openTranscript("remote:child");
  const first = transcriptPanes("remote:child")[0];

  openTranscript("remote:child");

  expect(transcriptPanes("remote:child")).toHaveLength(1);
  expect(transcriptPanes("remote:child")[0]?.id).toBe(first?.id);
  expect(transcriptPanes("remote:child")[0]?.slot).toBe("main");
  expect(workspaceStore.getState().focusedPaneId).toBe(first?.id);
});
