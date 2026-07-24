import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy } from "react";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import { registerPane } from "../paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { TreeDrawer } from "./TreeDrawer";

function DocFixture() {
  return <div>doc</div>;
}

beforeAll(async () => {
  registerPane<{ ref: string }>({
    id: "doc",
    title: () => "Doc",
    component: lazy(() => Promise.resolve({ default: DocFixture })),
  });
  await import("../../panes/welcome/Welcome");
  await import("../../panes/welcome");
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
});

afterEach(() => {
  cleanup();
});

test("renders a trigger button labeled Sessions", () => {
  render(<TreeDrawer />);
  expect(screen.getByRole("button", { name: "Sessions" })).toBeTruthy();
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
      <RailHost />
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
