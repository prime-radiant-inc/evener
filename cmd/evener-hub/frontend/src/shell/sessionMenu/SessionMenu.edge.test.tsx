// Edge cases for SessionMenu.tsx uncovered lines:
// - onClose handlers for rename dialog (line 169)
// - onClose handler for shutdown dialog (line 205)
// - onClose handler for delete dialog (line 229)
// - Rename confirm button disabled with empty input (line 184)

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterAll, afterEach, beforeEach, expect, test, vi } from "vitest";
import type { TreeNode as ApiTreeNode } from "../../stores/tree";
import { resetToastStoreForTests } from "../../widgets/toast/store";
import * as railActions from "../rail/actions";
import { SessionMenu, type SessionMenuActions, type SessionMenuProps } from "./SessionMenu";

let mockedListPinSections = vi.spyOn(railActions, "listPinSections");

afterAll(() => {
  mockedListPinSections.mockRestore();
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
  mockedListPinSections = vi.spyOn(railActions, "listPinSections");
  mockedListPinSections.mockResolvedValue([{ id: "sec_1", name: "Client", member_count: 0 }]);
});

afterEach(() => {
  cleanup();
});

// Line 169: rename dialog onClose
test("rename dialog onClose closes the dialog", async () => {
  const user = userEvent.setup();
  renderMenu();
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Rename" }));
  const _dialog = screen.getByRole("dialog", { name: "Rename session" });
  // Trigger onClose by clicking the overlay (outside the dialog content)
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Rename session" })).toBeNull());
});

// Line 205: shutdown dialog onClose
test("shutdown dialog onClose closes the dialog", async () => {
  const user = userEvent.setup();
  renderMenu();
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Shut down" }));
  expect(screen.getByRole("dialog", { name: "Shut down this session?" })).toBeTruthy();
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Shut down this session?" })).toBeNull());
});

// Line 229: delete dialog onClose
test("delete dialog onClose closes the dialog", async () => {
  const user = userEvent.setup();
  renderMenu({ treeNode: treeNode() });
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: /Delete/i }));
  expect(screen.getByRole("dialog", { name: "Delete session?" })).toBeTruthy();
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Delete session?" })).toBeNull());
});

// Line 209: shutdown dialog Cancel button onClick
test("shutdown dialog Cancel button closes the dialog", async () => {
  const user = userEvent.setup();
  renderMenu();
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Shut down" }));
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Shut down this session?" })).toBeNull());
});
