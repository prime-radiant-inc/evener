import { lazy, useState } from "react";
import { afterEach, beforeAll, beforeEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { registerPane, type PaneProps } from "../paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { StackHost } from "./StackHost";

// Fixture pane type for the bulk of this file's tests - non-singleton, so
// "doc"/{ref} with different refs are distinct panes (same dedup rule
// workspace.ts itself uses), and "doc" has no deep link (routing.ts's
// paneToURL returns null for it) which the URL-sync tests further down
// need a real example of. A local click counter proves whether a mount
// survived a focus change (StackHost's own remount-safety contract, same
// one DockHost's PaneHost documents - see this component's own comment).
function DocFixture({ params, paneId, focused }: PaneProps<{ ref: string }>) {
  const [clicks, setClicks] = useState(0);
  return (
    <div>
      <p>
        doc pane: {params.ref} (focused={String(focused)}) (paneId={paneId})
      </p>
      <button onClick={() => setClicks((c) => c + 1)}>clicks: {clicks}</button>
    </div>
  );
}

beforeAll(async () => {
  registerPane<{ ref: string }>({
    id: "doc",
    title: (params) => `Doc ${params.ref}`,
    component: lazy(() => Promise.resolve({ default: DocFixture })),
  });
  // Real production panes: "welcome" is StackHost's own hardcoded fallback
  // target, and "session" is this file's one real, deep-linked pane type
  // (routing.ts's paneToURL("session", ...) is the URL-sync tests' one
  // real, non-null example) - both registered for real rather than
  // fixtured, same dual approach DockHost.test.tsx uses for the same
  // reason.
  await import("../../panes/welcome/Welcome");
  await import("../../panes/welcome");
  await import("../../panes/session/Session");
  await import("../../panes/session");
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
});

test("falls back to opening welcome when nothing is focused at mount", async () => {
  render(<StackHost />);
  expect(await screen.findByText("No session open")).toBeTruthy();
});

test("renders the focused pane's component full-screen, with focused=true", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  expect(await screen.findByText(/doc pane: ref_a \(focused=true\)/)).toBeTruthy();
});

test("renders only the focused pane - a second open pane is not mounted at all", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  workspaceStore.getState().openPane("doc", { ref: "ref_b" }); // focused, most recently opened
  render(<StackHost />);

  await screen.findByText(/doc pane: ref_b/);
  expect(screen.queryByText(/doc pane: ref_a/)).toBeNull();
});

test("switching the focused pane (workspace.focusPane) swaps which one is rendered", async () => {
  const first = workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_b/);

  workspaceStore.getState().focusPane(first);

  expect(await screen.findByText(/doc pane: ref_a \(focused=true\)/)).toBeTruthy();
  expect(screen.queryByText(/doc pane: ref_b/)).toBeNull();
});

// --- remount safety: matches DockHost's own "unmount, not hide" contract -

test("switching away from a pane and back remounts it fresh - local state does not survive", async () => {
  // Real dockview unmounts a panel's whole tree whenever it isn't the
  // active tab (see DockHost.tsx's own PaneHost comment, live-probe
  // verified in that task's report) - every pane component is designed
  // around that contract already. StackHost only ever mounts ONE pane at a
  // time, so it must reproduce the same guarantee: a pane that regains
  // focus gets a FRESH instance, never one quietly kept alive off-screen.
  const first = workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  const second = workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  const user = userEvent.setup();
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_b/);

  workspaceStore.getState().focusPane(first);
  await screen.findByText(/doc pane: ref_a/);
  await user.click(screen.getByRole("button", { name: /clicks: 0/ }));
  expect(screen.getByRole("button", { name: /clicks: 1/ })).toBeTruthy();

  workspaceStore.getState().focusPane(second);
  await screen.findByText(/doc pane: ref_b/);
  workspaceStore.getState().focusPane(first);

  expect(await screen.findByText(/doc pane: ref_a/)).toBeTruthy();
  expect(screen.getByRole("button", { name: /clicks: 0/ })).toBeTruthy(); // reset, not preserved
});
