import { act, cleanup, render, screen } from "@testing-library/react";
import { lazy } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { registerPaneForTests } from "../paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { MobilePanel } from "./MobilePanel";

// The brief's tests call openPane("doc", ...) and openPane("welcome"). Both
// require a registered pane type (openPane -> paneFor throws otherwise), so
// this file registers "doc" as a fixture and imports the real welcome module
// for its registerPane("welcome") side effect - the same setup every sibling
// shell test (TreeDrawer.test.tsx, StackHost.test.tsx, DockHost.test.tsx)
// uses for the same reason.
function DocFixture() {
  return <div>doc</div>;
}

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

afterEach(cleanup);

beforeEach(() => {
  resetWorkspaceStoreForTests();
});

function RailFixture() {
  return <div data-testid="rail-fixture">Rail</div>;
}

test("renders rail content when open", () => {
  render(<MobilePanel rail={<RailFixture />} open onClose={vi.fn()} />);
  expect(screen.getByTestId("rail-fixture")).toBeTruthy();
});

test("renders a search box at the top", () => {
  render(<MobilePanel rail={<RailFixture />} open onClose={vi.fn()} />);
  expect(screen.getByRole("searchbox")).toBeTruthy();
  expect(screen.getByRole("searchbox").getAttribute("placeholder")).toBe("Search sessions");
});

test("renders welcome content when nothing is focused", () => {
  // Nothing focused → backstop opens welcome
  workspaceStore.getState().openPane("welcome");
  render(<MobilePanel rail={<RailFixture />} open onClose={vi.fn()} />);
  expect(screen.getByText(/read and edit the repository/i)).toBeTruthy();
});

test("hides welcome content when a session is focused", () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<MobilePanel rail={<RailFixture />} open onClose={vi.fn()} />);
  expect(screen.queryByText(/read and edit the repository/i)).toBeNull();
  expect(screen.getByTestId("rail-fixture")).toBeTruthy();
});

test("calls onClose when focusedPaneId changes while open", () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  const onClose = vi.fn();
  render(<MobilePanel rail={<RailFixture />} open onClose={onClose} />);
  expect(onClose).not.toHaveBeenCalled();
  // act() flushes the focus-change effect synchronously, the same idiom
  // TreeDrawer.test.tsx and StackHost.test.tsx use for a store mutation
  // issued after the component is already mounted.
  act(() => {
    workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  });
  expect(onClose).toHaveBeenCalled();
});
