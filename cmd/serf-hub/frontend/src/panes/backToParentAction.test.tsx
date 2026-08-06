import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { lazy } from "react";
import { afterEach, beforeEach, expect, test } from "vitest";
import type { ThreadModel } from "../protocol/model";
import { registerPane } from "../shell/paneRegistry";
import { registerDockviewApi, resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { resetThreadsStoreForTests, threadsStore } from "../stores/threads";
import { BackToParentAction } from "./backToParentAction";
// Side-effect import: registers the real "doc" pane type, used below as an
// arbitrary already-registered OTHER pane type to focus before testing that
// clicking Back moves focus back to the parent session pane.
import "./doc";

// A minimal, test-only "session" pane registration - mirrors
// Transcript.test.tsx's own precedent: real registerPane/paneFor/openPane
// machinery, without pulling in the actual (heavier) panes/session module.
registerPane({
  id: "session",
  title: () => "test session",
  component: lazy(() => Promise.resolve({ default: () => null })),
});

beforeEach(() => {
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
});

afterEach(() => {
  cleanup();
  registerDockviewApi(null); // never leak a fake dockview host to another test
});

test("falls back to the raw parent ref when no cached name is available", () => {
  render(<BackToParentAction parentRef="ref_parent_unknown" />);
  expect(screen.getByRole("button", { name: /back to ref_parent_unknown/i })).toBeTruthy();
});

test("shows the live parent thread name once hydrated", () => {
  threadsStore.setState((s) => {
    const threads = new Map(s.threads);
    threads.set("ref_parent", { ref: "ref_parent", name: "fix the flaky test", turns: [] } as unknown as ThreadModel);
    return { ...s, threads };
  });
  render(<BackToParentAction parentRef="ref_parent" />);
  expect(screen.getByRole("button", { name: /back to fix the flaky test/i })).toBeTruthy();
});

test("clicking it focuses (or reopens) the parent session pane", () => {
  render(<BackToParentAction parentRef="ref_parent" />);
  fireEvent.click(screen.getByRole("button", { name: /back to/i }));

  const panes = workspaceStore.getState().panes;
  const parentPane = panes.find((p) => p.type === "session");
  expect(parentPane?.params).toEqual({ ref: "ref_parent" });
  expect(workspaceStore.getState().focusedPaneId).toBe(parentPane?.id);
});

test("re-focuses an ALREADY-OPEN parent pane rather than opening a duplicate", () => {
  const existingId = workspaceStore.getState().openPane("session", { ref: "ref_parent" });
  // Focus something else first, so clicking has to move focus back.
  workspaceStore.getState().openPane("doc", { session: "ref_parent", path: "a.txt", kind: "file" });

  render(<BackToParentAction parentRef="ref_parent" />);
  fireEvent.click(screen.getByRole("button", { name: /back to/i }));

  const panes = workspaceStore.getState().panes;
  expect(panes.filter((p) => p.type === "session")).toHaveLength(1);
  expect(workspaceStore.getState().focusedPaneId).toBe(existingId);
});
