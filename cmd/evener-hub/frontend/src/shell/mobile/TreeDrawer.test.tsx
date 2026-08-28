import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { keyID } from "../../stores/navigation/types";
import { ClientProvider } from "../clientContext";
import { registerPaneForTests } from "../paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { TreeDrawer } from "./TreeDrawer";

function needsYouNode(n: number) {
  return {
    ref: `local:s${n}`,
    host_id: "local",
    session_id: `s${n}`,
    title: `Needs you ${n}`,
    project: "proj",
    state: "awaiting",
    kind: "session",
    live: true,
    children: [],
  };
}
function setNeedsYou(count: number): void {
  const key = { kind: "section", section: "needs_you", offset: 0, limit: 50 } as const;
  const rows = Array.from({ length: count }, (_, i) => needsYouNode(i));
  navigationStore.setState({
    mode: "v1",
    clientGenerationID: "generation_test",
    resources: new Map([
      [
        keyID(key),
        {
          key,
          data: { generation_id: "generation_test", revision: 1, sessions: rows, remaining: 0, truncated: false },
          loadedRevision: 1,
          targetRevision: null,
          forceToken: 0,
          etag: "etag",
          loading: false,
          stale: false,
          error: null,
          generationID: "generation_test",
        },
      ],
    ]),
  });
}

function DocFixture() {
  return <div>doc</div>;
}

// paneRegistry.ts is a shared module singleton - registerPaneForTests's
// restorer (called in afterAll below) puts back whatever "doc" resolved to
// before this file ran, so a later file sharing the same registry never
// inherits this fixture.
let restoreDocPane: () => void;

beforeAll(async () => {
  restoreDocPane = registerPaneForTests<{ ref: string }>({
    id: "doc",
    title: () => "Doc",
    component: lazy(() => Promise.resolve({ default: DocFixture })),
  });
  await import("../../panes/welcome/Welcome");
  await import("../../panes/welcome");
});

afterAll(() => {
  restoreDocPane();
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
});

afterEach(() => {
  cleanup();
});

test("renders a trigger button labeled Sessions", () => {
  render(<TreeDrawer />);
  expect(screen.getByRole("button", { name: "Sessions" })).toBeTruthy();
});

// UX fix: parity with RailHost's own hidden-rail ☰ chip, which already shows
// Badge(needsYou) - the mobile trigger carries the same overlay so attention
// is visible without opening the drawer first.
test("the trigger carries the same needs-you Badge overlay the desktop chip has", () => {
  setNeedsYou(2);
  render(<TreeDrawer />);
  expect(screen.getByText("2")).toBeTruthy();
});

test("no Badge overlay when nothing needs attention", () => {
  setNeedsYou(0);
  render(<TreeDrawer />);
  expect(screen.queryByText("0")).toBeNull();
});

// FIX 4 (real-browser report): the trigger's badge read
// attentionSummary.needsYou, a SEPARATE, more narrowly-scoped aggregate
// (hubcore's DeriveAttention: top-level, non-subagent, non-archived
// sessions only - attention.go's own tierEligible) than tree.needs_you (the
// server's own ordered needs-you list every OTHER needs-you surface in this
// app reads from - see needsYouCycle.ts's own header comment on why it is
// the one source of truth). A session that needs you but falls outside
// attentionSummary's narrower population (e.g. reached only via the tree's
// own needs_you list) left the drawer's rows showing "your move" while its
// trigger stayed unbadged. Keying the badge off tree.needs_you.length
// instead matches every other needs-you surface and can never disagree
// with what the drawer's own rows show once opened.
test("the trigger badge follows the bounded needs-you rows", () => {
  setNeedsYou(1);
  render(<TreeDrawer />);
  expect(screen.getByText("1")).toBeTruthy();
});

test("the drawer is closed until the trigger is clicked", () => {
  render(<TreeDrawer />);
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("clicking the trigger opens the sheet, titled Sessions", async () => {
  const user = userEvent.setup();
  render(<TreeDrawer />);

  await user.click(screen.getByRole("button", { name: "Sessions" }));

  const dialog = screen.getByRole("dialog");
  expect(dialog).toBeTruthy();
  expect(screen.getByText("Sessions", { selector: "h2" })).toBeTruthy();
});

test("Escape closes the drawer (the open/onClose wiring into Sheet is correct)", async () => {
  const user = userEvent.setup();
  render(<TreeDrawer />);
  await user.click(screen.getByRole("button", { name: "Sessions" }));
  expect(screen.getByRole("dialog")).toBeTruthy();

  await user.keyboard("{Escape}");

  expect(screen.queryByRole("dialog")).toBeNull();
});

test("shows a clearly-marked placeholder for the rail when no children are given", async () => {
  const user = userEvent.setup();
  render(<TreeDrawer />);
  await user.click(screen.getByRole("button", { name: "Sessions" }));

  expect(screen.getByTestId("rail-slot")).toBeTruthy();
});

test("renders provided children instead of the placeholder", async () => {
  const user = userEvent.setup();
  render(
    <TreeDrawer>
      <div data-testid="real-rail">the real rail, once T3 lands</div>
    </TreeDrawer>,
  );
  await user.click(screen.getByRole("button", { name: "Sessions" }));

  expect(screen.getByTestId("real-rail")).toBeTruthy();
  expect(screen.queryByTestId("rail-slot")).toBeNull();
});

// The drawer-hosted rail is the SAME full-chrome Rail as desktop (collapsed
// mode + the hostedInSheet suppression were removed 2026-07-24): search,
// + New session, and the settings footer all render inside the sheet.
test("the drawer-hosted RailHost carries the full sidebar chrome", async () => {
  const { RailHost } = await import("../rail");
  const user = userEvent.setup();
  render(
    <TreeDrawer>
      <ClientProvider client={new FakeClient()}>
        <RailHost />
      </ClientProvider>
    </TreeDrawer>,
  );
  await user.click(screen.getByRole("button", { name: "Sessions" }));

  expect(screen.getByTestId("rail-search")).toBeTruthy();
  expect(screen.getByRole("button", { name: /new session/i })).toBeTruthy();
  expect(screen.getByTestId("rail-settings")).toBeTruthy();
});

// --- auto-close on navigation -------------------------------------------

test("closes automatically when the focused pane changes elsewhere while open", async () => {
  const user = userEvent.setup();
  render(<TreeDrawer />);
  await user.click(screen.getByRole("button", { name: "Sessions" }));
  expect(screen.getByRole("dialog")).toBeTruthy();

  // Simulates the rail (a sibling stream, not built here) calling the same
  // workspaceStore.openPane() every other pane-opening trigger in the app
  // already calls - see this file's own header comment on the intended
  // integration contract. act() flushes the resulting re-render
  // synchronously, same idiom workspace.test.ts/DockHost.test.tsx use for
  // a store mutation issued after the component is already mounted.
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  });

  expect(screen.queryByRole("dialog")).toBeNull();
});

test("does not close from a store update that leaves focusedPaneId unchanged", async () => {
  workspaceStore.getState().openPane("welcome");
  const user = userEvent.setup();
  render(<TreeDrawer />);
  await user.click(screen.getByRole("button", { name: "Sessions" }));
  expect(screen.getByRole("dialog")).toBeTruthy();

  // welcome is a singleton and already focused - reopening it with
  // DIFFERENT params still updates the store (a real, observable change,
  // unlike an identical no-op call) but focusedPaneId's own VALUE does not
  // change, since the same pane stays focused throughout. Proves the
  // auto-close effect keys specifically off focusedPaneId changing, not
  // "the store emitted a change" more broadly.
  act(() => {
    workspaceStore.getState().openPane("welcome", { note: "a different note" });
  });

  expect(screen.getByRole("dialog")).toBeTruthy();
});
