import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test } from "vitest";
import { resetTreeStoreForTests, type TreeNode, type TreeResponse, treeStore } from "../../stores/tree";
import Welcome from "./Welcome";

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  resetTreeStoreForTests();
});

function node(overrides: Partial<TreeNode> = {}): TreeNode {
  return {
    row_id: "row1",
    ref: "local:row1",
    host_id: "local",
    session_id: "row1",
    title: "Session",
    project: "Proj",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...overrides,
  };
}

const EMPTY_TREE: TreeResponse = {
  generated_at: "2026-01-01T00:00:00Z",
  sources: [],
  live: [],
  needs_you: [],
  favorites: [],
  projects: [],
  archived_projects: [],
  test_runs: [],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
};

function setTree(overrides: Partial<TreeResponse>): void {
  treeStore.setState({ tree: { ...EMPTY_TREE, ...overrides } });
}

test('shows "No session open"', () => {
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(screen.getByText("No session open")).toBeTruthy();
});

test('offers a "New session" action', () => {
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(screen.getByRole("button", { name: "New session" })).toBeTruthy();
});

test('clicking "New session" navigates to /new', async () => {
  const user = userEvent.setup();
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  await user.click(screen.getByRole("button", { name: "New session" }));
  expect(window.location.pathname).toBe("/new");
});

test("shows params.note as a hint when provided", () => {
  render(<Welcome params={{ note: "Starting a new session isn't available yet." }} paneId="welcome" focused={true} />);
  expect(screen.getByText("Starting a new session isn't available yet.")).toBeTruthy();
});

test("shows no hint when params.note is absent", () => {
  const { container } = render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(container.textContent).not.toMatch(/available yet/i);
});

// tbk8: a cold "/" with no restored pane layout (a fresh browser, or
// localStorage cleared) shows the bare Welcome pane with nothing pointing
// back at a live or recent session - on a narrow viewport there's no rail
// beside it to fall back on. "Jump back in" is Welcome's own affordance for
// that case, independent of whether a docked rail happens to be visible.
test('offers "Jump back in" to the first needs-you session when one exists', () => {
  setTree({ needs_you: [node({ ref: "local:ny1", title: "Fix the thing", project: "myrepo", age: "4m" })] });
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(screen.getByRole("button", { name: /Jump back in.*Fix the thing/s })).toBeTruthy();
});

test("falls back to the first live session when nothing needs you", () => {
  setTree({ live: [node({ ref: "local:live1", title: "Refactor auth", project: "myrepo", age: "now" })] });
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(screen.getByRole("button", { name: /Jump back in.*Refactor auth/s })).toBeTruthy();
});

test("needs-you outranks live when both exist", () => {
  setTree({
    needs_you: [node({ ref: "local:ny1", title: "Needs you" })],
    live: [node({ ref: "local:live1", title: "Just running" })],
  });
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(screen.getByRole("button", { name: /Jump back in.*Needs you/s })).toBeTruthy();
  expect(screen.queryByText(/Just running/)).toBeNull();
});

test('omits "Jump back in" when there is nothing live or needing you', () => {
  setTree({});
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(screen.queryByRole("button", { name: /Jump back in/ })).toBeNull();
});

test('omits "Jump back in" before the tree has ever loaded', () => {
  // tree stays null (the store's own initial state, never fetched yet) -
  // must not crash and must not show a phantom resume link.
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(screen.queryByRole("button", { name: /Jump back in/ })).toBeNull();
});

test('clicking "Jump back in" opens that session\'s pane', async () => {
  const user = userEvent.setup();
  setTree({ live: [node({ ref: "local:live1", title: "Refactor auth" })] });
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  await user.click(screen.getByRole("button", { name: /Jump back in/ }));
  expect(window.location.pathname).toBe(`/s/${encodeURIComponent("local:live1")}`);
});
