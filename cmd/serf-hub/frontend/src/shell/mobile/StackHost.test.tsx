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

// --- back navigation ---------------------------------------------------

test("the back affordance is hidden while welcome (the stack's root) is focused", async () => {
  render(<StackHost />);
  await screen.findByText("No session open");
  expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
});

test("the back affordance is shown once a non-welcome pane is focused", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  expect(await screen.findByRole("button", { name: "Back" })).toBeTruthy();
});

test("back returns to the pane that was focused before the current one", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);

  workspaceStore.getState().openPane("doc", { ref: "ref_b" }); // a forward navigation, observed live
  await screen.findByText(/doc pane: ref_b/);

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));

  expect(await screen.findByText(/doc pane: ref_a \(focused=true\)/)).toBeTruthy();
});

test("back falls all the way to welcome when this component never observed an earlier pane", async () => {
  // StackHost mounts with ref_a already focused (simulating AppShell
  // having pre-seeded it via routing, same as the real integration) - this
  // component never itself witnessed a transition INTO ref_a, so there is
  // nothing on ITS OWN back-stack to return to; welcome is the documented
  // fallback (see requirement 2 - "pops to the previously-focused pane or
  // welcome").
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));

  expect(await screen.findByText("No session open")).toBeTruthy();
});

test("back walks multiple levels deep, one pane per tap", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  await screen.findByText(/doc pane: ref_b/);
  workspaceStore.getState().openPane("doc", { ref: "ref_c" });
  await screen.findByText(/doc pane: ref_c/);

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" }));
  expect(await screen.findByText(/doc pane: ref_b/)).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Back" }));
  expect(await screen.findByText(/doc pane: ref_a/)).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Back" }));
  expect(await screen.findByText("No session open")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Back" })).toBeNull(); // welcome: nothing further behind it
});

test("a back tap does not push the pane it left onto the stack (no ping-pong)", async () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<StackHost />);
  await screen.findByText(/doc pane: ref_a/);
  workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  await screen.findByText(/doc pane: ref_b/);

  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Back" })); // -> ref_a
  await screen.findByText(/doc pane: ref_a/);

  // ref_a is StackHost's own root here (it never observed anything before
  // ref_a - see the "falls all the way to welcome" test above) - a second
  // tap must go straight to welcome, not bounce forward to ref_b.
  await user.click(screen.getByRole("button", { name: "Back" }));
  expect(await screen.findByText("No session open")).toBeTruthy();
});
