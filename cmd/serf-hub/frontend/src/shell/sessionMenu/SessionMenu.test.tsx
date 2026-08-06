import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { TreeNode as ApiTreeNode } from "../../stores/tree";
import { resetToastStoreForTests } from "../../widgets/toast/store";
import { listPinSections } from "../rail/actions";
import { SessionMenu, type SessionMenuActions, type SessionMenuProps } from "./SessionMenu";

// Task 4's "Pin this session…" mounts the real PinSectionPicker, which
// fetches its section list on mount - stub that fetch exactly the way
// shell/rail/PinSectionPicker.test.tsx does.
vi.mock("../rail/actions", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../rail/actions")>();
  return { ...actual, listPinSections: vi.fn() };
});

const mockedListPinSections = vi.mocked(listPinSections);

function renderMenu(overrides: Partial<SessionMenuProps> = {}) {
  const actions: SessionMenuActions = {
    onOpenPane: vi.fn(),
    onRename: vi.fn().mockResolvedValue(undefined),
    onShutdown: vi.fn().mockResolvedValue(undefined),
    onPin: vi.fn().mockResolvedValue(undefined),
    onUnpin: vi.fn().mockResolvedValue(undefined),
    onToggleArchive: vi.fn().mockResolvedValue(undefined),
    onDelete: vi.fn().mockResolvedValue(undefined),
  };
  render(
    <SessionMenu
      sessionRef="ref_a"
      title="My session"
      triggerLabel="Session actions"
      canRename
      canShutdown
      panesOpen={{ details: false, tasks: true, activity: false }}
      actions={actions}
      {...overrides}
    />,
  );
  return overrides.actions ?? actions;
}

async function openMenu(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: /session actions/i }));
}

beforeEach(() => {
  resetToastStoreForTests();
  mockedListPinSections.mockResolvedValue([{ id: "sec_1", name: "Client", member_count: 0 }]);
});

afterEach(() => {
  cleanup();
});

test("panes group leads with open-state checkmarks and dispatches onOpenPane", async () => {
  const user = userEvent.setup();
  const actions = renderMenu();
  await openMenu(user);
  expect(screen.getByRole("menuitem", { name: "Details" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Tasks ✓" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Activity" })).toBeTruthy();
  await user.click(screen.getByRole("menuitem", { name: "Tasks ✓" }));
  expect(actions.onOpenPane).toHaveBeenCalledWith("tasks");
});

test("live labels replace the plain pane names", async () => {
  const user = userEvent.setup();
  renderMenu({ taskLabel: "Tasks 3/7", activityLabel: "Activity · 2" });
  await openMenu(user);
  expect(screen.getByRole("menuitem", { name: "Tasks 3/7 ✓" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Activity · 2" })).toBeTruthy();
});

test("Rename opens its dialog; saving calls onRename and closes", async () => {
  const user = userEvent.setup();
  const actions = renderMenu();
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Rename" }));
  const dialog = screen.getByRole("dialog", { name: "Rename session" });
  const input = within(dialog).getByLabelText("Name");
  expect((input as HTMLInputElement).value).toBe("My session");
  await user.clear(input);
  await user.type(input, "New name");
  await user.click(within(dialog).getByRole("button", { name: "Rename" }));
  await waitFor(() => expect(actions.onRename).toHaveBeenCalledWith("New name"));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("a rejected onRename keeps the dialog open (adapter toasted)", async () => {
  const user = userEvent.setup();
  const actions = renderMenu({
    actions: {
      onOpenPane: vi.fn(),
      onRename: vi.fn().mockRejectedValue(new Error("boom")),
      onShutdown: vi.fn().mockResolvedValue(undefined),
      onPin: vi.fn().mockResolvedValue(undefined),
      onUnpin: vi.fn().mockResolvedValue(undefined),
      onToggleArchive: vi.fn().mockResolvedValue(undefined),
      onDelete: vi.fn().mockResolvedValue(undefined),
    },
  });
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Rename" }));
  await user.click(screen.getByRole("button", { name: "Rename" }));
  await waitFor(() => expect(actions.onRename).toHaveBeenCalled());
  expect(screen.getByRole("dialog", { name: "Rename session" })).toBeTruthy();
});

test("Rename is disabled when canRename is false", async () => {
  const user = userEvent.setup();
  renderMenu({ canRename: false });
  await openMenu(user);
  expect(screen.getByRole("menuitem", { name: "Rename" }).getAttribute("aria-disabled")).toBe("true");
});

test("Shut down confirms before calling onShutdown", async () => {
  const user = userEvent.setup();
  const actions = renderMenu();
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Shut down" }));
  const dialog = screen.getByRole("dialog", { name: "Shut down this session?" });
  await user.click(within(dialog).getByRole("button", { name: "Shut down" }));
  await waitFor(() => expect(actions.onShutdown).toHaveBeenCalledTimes(1));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("no organization or delete items without a treeNode", async () => {
  const user = userEvent.setup();
  renderMenu();
  await openMenu(user);
  expect(screen.queryByRole("menuitem", { name: /pin/i })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: /archive/i })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: /delete/i })).toBeNull();
});

function treeNode(overrides: Partial<ApiTreeNode> = {}): ApiTreeNode {
  return {
    row_id: "row_1",
    ref: "ref_a",
    host_id: "local",
    session_id: "sess_a",
    title: "My session",
    project: "proj",
    state: "idle",
    kind: "session",
    live: false,
    children: [],
    ...overrides,
  };
}

test("full menu: organize group between separators, delete last", async () => {
  const user = userEvent.setup();
  renderMenu({ treeNode: treeNode() });
  await openMenu(user);
  const items = screen.getAllByRole("menuitem").map((el) => el.textContent);
  expect(items).toEqual([
    "Details",
    "Tasks ✓",
    "Activity",
    "Rename",
    "Pin this session…",
    "Archive",
    "Shut down",
    "Delete…",
  ]);
  expect(screen.getAllByRole("separator")).toHaveLength(2);
});

test("nested kinds and remote hosts lose organization/delete items", async () => {
  const user = userEvent.setup();
  renderMenu({ treeNode: treeNode({ kind: "subagent" }) });
  await openMenu(user);
  expect(screen.queryByRole("menuitem", { name: /pin/i })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: /archive/i })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: /delete/i })).toBeNull();
  cleanup();
  renderMenu({ treeNode: treeNode({ host_id: "remote" }) });
  await openMenu(user);
  expect(screen.getByRole("menuitem", { name: "Pin this session…" })).toBeTruthy();
  expect(screen.queryByRole("menuitem", { name: /delete/i })).toBeNull();
});

test("pinned session offers Unpin; archived offers Unarchive", async () => {
  const user = userEvent.setup();
  const actions = renderMenu({ treeNode: treeNode({ pin_section_id: "sec_1", tier: "archived" }) });
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Unpin" }));
  expect(actions.onUnpin).toHaveBeenCalledTimes(1);
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Unarchive" }));
  expect(actions.onToggleArchive).toHaveBeenCalledTimes(1);
});

test("Pin this session… opens the PinSectionPicker; assigning calls onPin and closes", async () => {
  const user = userEvent.setup();
  const actions = renderMenu({ treeNode: treeNode() });
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Pin this session…" }));
  await user.click(await screen.findByRole("button", { name: "Client" }));
  await waitFor(() =>
    expect(actions.onPin).toHaveBeenCalledWith({ section_id: expect.any(String) }, expect.anything()),
  );
});

test("Delete… confirms before calling onDelete", async () => {
  const user = userEvent.setup();
  const actions = renderMenu({ treeNode: treeNode() });
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Delete…" }));
  const dialog = screen.getByRole("dialog", { name: "Delete session?" });
  expect(within(dialog).getByText(/Permanently delete "My session"\?/)).toBeTruthy();
  await user.click(within(dialog).getByRole("button", { name: "Delete" }));
  await waitFor(() => expect(actions.onDelete).toHaveBeenCalledTimes(1));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});
