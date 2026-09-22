import { act, cleanup, render, screen } from "@testing-library/react";
import { lazy } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { registerPaneForTests } from "../paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { MobilePanel } from "./MobilePanel";
import styles from "./MobilePanel.module.css";

// openPane("doc", ...) requires a registered pane type (openPane -> paneFor
// throws otherwise), so this file registers "doc" as a fixture - the same
// setup every sibling shell test (TreeDrawer.test.tsx, StackHost.test.tsx,
// DockHost.test.tsx) uses for the same reason.
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

test("does not render an inline search box", () => {
  render(<MobilePanel rail={<RailFixture />} open onClose={vi.fn()} />);
  expect(screen.queryByRole("searchbox")).toBeNull();
});

test("makes the Sheet panel the mobile scroll owner", () => {
  render(<MobilePanel rail={<RailFixture />} open onClose={vi.fn()} />);
  const panel = screen.getByRole("dialog");

  expect(styles.singleScrollPanel).toBeTruthy();
  expect(panel.className.split(/\s+/)).toContain(styles.singleScrollPanel);
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
