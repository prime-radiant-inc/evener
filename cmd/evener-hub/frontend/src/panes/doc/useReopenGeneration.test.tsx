import { act, render, screen } from "@testing-library/react";
import { lazy } from "react";
import { afterAll, beforeEach, expect, test, vi } from "vitest";
import { registerPaneForTests } from "../../shell/paneRegistry";
import { recordDocumentPaneState, resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { useReopenGeneration } from "./useReopenGeneration";

afterAll(
  registerPaneForTests({
    id: "doc",
    title: () => "test doc",
    component: lazy(() => Promise.resolve({ default: () => null })),
  }),
);

function Consumer({ paneId }: { paneId: string }) {
  const generation = useReopenGeneration(paneId);
  renders += 1;
  return <div data-testid="generation">{`generation:${generation}:renders:${renders}`}</div>;
}

let renders = 0;

beforeEach(() => {
  resetWorkspaceStoreForTests();
  renders = 0;
});

test("an open pane's reopen generation reaches the consumer, and a closing pane's record removal does not re-render it", async () => {
  vi.spyOn(Math, "random").mockReturnValue(0.5);
  const paneId = workspaceStore.getState().openPane("doc", {
    session: "root",
    path: "NOTES.md",
    kind: "file",
    origin: { kind: "browser-restored-session" },
  });
  const pane = workspaceStore.getState().panes.find((candidate) => candidate.id === paneId);
  if (!pane) throw new Error("the doc pane did not open");
  // The retained record arrives after the pane exists; record it like the
  // real opener's canonical publication does.
  recordDocumentPaneState(pane, {
    reference: { path: "NOTES.md", cwd: "/repo", readTarget: "/repo/NOTES.md", provenance: "relative" },
    origin: undefined,
    reopen: 0,
  });

  render(<Consumer paneId={paneId} />);
  await screen.findByText("generation:0:renders:1");

  // Reopening the same document increments the retained generation; the
  // consumer re-renders and sees it.
  act(() => {
    recordDocumentPaneState(pane, {
      reference: { path: "NOTES.md", cwd: "/repo", readTarget: "/repo/NOTES.md", provenance: "relative" },
      origin: undefined,
      reopen: 1,
    });
  });
  await screen.findByText("generation:1:renders:2");

  // The pane closes: its record disappears. A dying pane must NOT re-render
  // (a re-render there re-creates disposed work and issues a read from the
  // dying pane that supersedes live peers' tickets), so the snapshot keeps the
  // last generation seen while the record existed.
  const rendersBeforeClose = renders;
  act(() => {
    workspaceStore.setState({ panes: [] });
  });
  await act(async () => {
    await Promise.resolve();
  });
  expect(renders).toBe(rendersBeforeClose);
  expect(screen.getByTestId("generation").textContent).toBe("generation:1:renders:2");
});
