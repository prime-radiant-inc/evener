import { keyID, type ResourceState } from "@evener/appwire-client/state/navigation";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetToastStoreForTests } from "../../widgets/toast/store";
import type { NavigationSessionModel } from "./SessionMenu";
import { SessionMenu, type SessionMenuActions, type SessionMenuProps } from "./SessionMenu";

// "Pin this session…" mounts the real PinSectionPicker, which reads
// pin sections from the navigation store's bounded pin-catalog resource
// (loadPinCatalogPages + selectPinSections). Seed the store with a pin_catalog resource and
// stub loadPinCatalogPages so the picker's mount effect resolves without a
// real network fetch.
const generation = "generation_test";
const pinKey = { kind: "pin_catalog" as const, offset: 0, limit: 100 };

type LoadPinCatalogPages = (force?: boolean) => Promise<void>;

function seedPinCatalog(): void {
  const resource: ResourceState = {
    key: pinKey,
    data: {
      generation_id: generation,
      revision: 1,
      pin_sections: [{ id: "sec_1", name: "Client", count: 0 }],
      remaining: 0,
    },
    loadedRevision: 1,
    targetRevision: 1,
    forceToken: 0,
    etag: "a",
    loading: false,
    stale: false,
    error: null,
    generationID: generation,
  };
  navigationStore.setState({
    mode: "v3",
    resources: new Map([[keyID(resource.key), resource]]),
  });
  navigationStore.setState({ loadPinCatalogPages: vi.fn(async () => undefined) as LoadPinCatalogPages });
}

function renderMenu(overrides: Partial<SessionMenuProps> = {}, actionOverrides: Partial<SessionMenuActions> = {}) {
  const actions: SessionMenuActions = {
    onOpenOverview: vi.fn(),
    onRename: vi.fn().mockResolvedValue(undefined),
    onShutdown: vi.fn().mockResolvedValue(undefined),
    onPin: vi.fn().mockResolvedValue(undefined),
    onUnpin: vi.fn().mockResolvedValue(undefined),
    onToggleArchive: vi.fn().mockResolvedValue(undefined),
    onDelete: vi.fn().mockResolvedValue(undefined),
    ...actionOverrides,
  };
  render(
    <SessionMenu
      sessionRef="ref_a"
      title="My session"
      triggerLabel="Session actions"
      canRename
      canShutdown
      stopped
      overviewOpen={false}
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
  resetNavigationStoreForTests();
  seedPinCatalog();
});

afterEach(() => {
  cleanup();
  resetNavigationStoreForTests();
});

test("inspection group offers Overview without Details", async () => {
  const user = userEvent.setup();
  renderMenu();
  await openMenu(user);
  expect(screen.getByRole("menuitem", { name: "Overview" })).toBeTruthy();
  expect(screen.queryByRole("menuitem", { name: "Details" })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: "Activity" })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: /Tasks/ })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: /Notes/ })).toBeNull();
});

test("pane-only Verbosity follows Overview, precedes the first separator, and dispatches its callback", async () => {
  const user = userEvent.setup();
  const onOpenVerbosity = vi.fn();
  renderMenu({ onOpenVerbosity });
  await openMenu(user);

  const menu = screen.getByRole("menu");
  const verbosity = within(menu).getByRole("menuitem", { name: "Verbosity…" });
  const overview = within(menu).getByRole("menuitem", { name: "Overview" });
  const firstSeparator = within(menu).getAllByRole("separator")[0];
  if (!firstSeparator) throw new Error("Session menu is missing its first separator");
  expect(overview.compareDocumentPosition(verbosity) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  expect(verbosity.compareDocumentPosition(firstSeparator) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);

  await user.click(verbosity);
  expect(onOpenVerbosity).toHaveBeenCalledOnce();
});

test("rail-style callers that omit the pane-only callback do not get Verbosity", async () => {
  const user = userEvent.setup();
  renderMenu({ triggerLabel: "Actions for My session" });
  await user.click(screen.getByRole("button", { name: "Actions for My session" }));
  expect(screen.queryByRole("menuitem", { name: "Verbosity…" })).toBeNull();
});

test("live labels replace the plain Overview name", async () => {
  const user = userEvent.setup();
  renderMenu({ overviewLabel: "Overview · 2" });
  await openMenu(user);
  expect(screen.getByRole("menuitem", { name: "Overview · 2" })).toBeTruthy();
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
      onOpenOverview: vi.fn(),
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

test("no organization or delete items without a navigation session", async () => {
  const user = userEvent.setup();
  renderMenu();
  await openMenu(user);
  expect(screen.queryByRole("menuitem", { name: /pin/i })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: /archive/i })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: /delete/i })).toBeNull();
});

function navigationSession(overrides: Partial<NavigationSessionModel> = {}): NavigationSessionModel {
  return {
    ref: "ref_a",
    host_id: "local",
    session_id: "sess_a",
    title: "My session",
    kind: "session",
    top_level: true,
    ...overrides,
  };
}

test("full menu: organize group between separators, delete last", async () => {
  const user = userEvent.setup();
  renderMenu({ session: navigationSession() });
  await openMenu(user);
  const items = screen.getAllByRole("menuitem").map((el) => el.textContent);
  expect(items).toEqual(["Overview", "Rename", "Pin this session…", "Archive", "Shut down", "Delete…"]);
  expect(screen.getAllByRole("separator")).toHaveLength(2);
});

test("Delete is hidden until the session is stopped", async () => {
  const user = userEvent.setup();
  renderMenu({ session: navigationSession(), stopped: false });
  await openMenu(user);
  expect(screen.queryByRole("menuitem", { name: "Delete…" })).toBeNull();
});

test("force-stop action is labeled Force shutdown", async () => {
  const user = userEvent.setup();
  renderMenu({}, { onForceStop: vi.fn().mockResolvedValue(undefined) });
  await openMenu(user);
  expect(screen.getByRole("menuitem", { name: "Force shutdown…" })).toBeTruthy();
  expect(screen.queryByRole("menuitem", { name: "Force stop…" })).toBeNull();
});

test("nested kinds and remote hosts lose organization/delete items", async () => {
  const user = userEvent.setup();
  renderMenu({ session: navigationSession({ kind: "subagent" }) });
  await openMenu(user);
  expect(screen.queryByRole("menuitem", { name: /pin/i })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: /archive/i })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: /delete/i })).toBeNull();
  cleanup();
  renderMenu({ session: navigationSession({ host_id: "remote" }) });
  await openMenu(user);
  expect(screen.getByRole("menuitem", { name: "Pin this session…" })).toBeTruthy();
  expect(screen.queryByRole("menuitem", { name: /delete/i })).toBeNull();
});

test("pinned session offers Unpin; archived offers Unarchive", async () => {
  const user = userEvent.setup();
  const actions = renderMenu({ session: navigationSession({ pin_section_id: "sec_1", tier: "archived" }) });
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Unpin" }));
  expect(actions.onUnpin).toHaveBeenCalledTimes(1);
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Unarchive" }));
  expect(actions.onToggleArchive).toHaveBeenCalledTimes(1);
});

test("Pin this session… opens the PinSectionPicker; assigning calls onPin and closes", async () => {
  const user = userEvent.setup();
  const actions = renderMenu({ session: navigationSession() });
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Pin this session…" }));
  await user.click(await screen.findByRole("button", { name: "Client" }));
  await waitFor(() =>
    expect(actions.onPin).toHaveBeenCalledWith({ section_id: expect.any(String) }, expect.anything()),
  );
});

test("Delete… confirms before calling onDelete", async () => {
  const user = userEvent.setup();
  const actions = renderMenu({ session: navigationSession() });
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Delete…" }));
  const dialog = screen.getByRole("dialog", { name: "Delete session?" });
  expect(within(dialog).getByText(/Permanently delete "My session"\?/)).toBeTruthy();
  await user.click(within(dialog).getByRole("button", { name: "Delete" }));
  await waitFor(() => expect(actions.onDelete).toHaveBeenCalledTimes(1));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("turn verbs lead the menu in their own group, ahead of the pane group, and dispatch", async () => {
  const user = userEvent.setup();
  const onStop = vi.fn();
  const onSteer = vi.fn();
  renderMenu({ turnVerbs: { stop: { onSelect: onStop }, steer: { onSelect: onSteer } } });
  await openMenu(user);
  const menu = screen.getByRole("menu");
  const stopItem = within(menu).getByRole("menuitem", { name: "Stop" });
  const steerItem = within(menu).getByRole("menuitem", { name: "Steer" });
  const overview = within(menu).getByRole("menuitem", { name: "Overview" });
  // Stop before Steer, both before the pane group, and their group separated
  // from it (the menu's other two separators are the organize/destructive ones).
  expect(stopItem.compareDocumentPosition(steerItem) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  expect(steerItem.compareDocumentPosition(overview) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  expect(screen.getAllByRole("separator")).toHaveLength(3);
  await user.click(stopItem);
  expect(onStop).toHaveBeenCalledOnce();
  await openMenu(user);
  await user.click(within(screen.getByRole("menu")).getByRole("menuitem", { name: "Steer" }));
  expect(onSteer).toHaveBeenCalledOnce();
});

test("a disabled turn verb stays put when clicked", async () => {
  const user = userEvent.setup();
  const onSteer = vi.fn();
  renderMenu({ turnVerbs: { steer: { onSelect: onSteer, disabled: true } } });
  await openMenu(user);
  expect(screen.getByRole("menuitem", { name: "Steer" }).getAttribute("aria-disabled")).toBe("true");
  await user.click(screen.getByRole("menuitem", { name: "Steer" }));
  expect(onSteer).not.toHaveBeenCalled();
});

test("a caller that passes only one turn verb gets only that item", async () => {
  const user = userEvent.setup();
  const onStop = vi.fn();
  renderMenu({ turnVerbs: { stop: { onSelect: onStop } } });
  await openMenu(user);
  expect(screen.getByRole("menuitem", { name: "Stop" })).toBeTruthy();
  expect(screen.queryByRole("menuitem", { name: "Steer" })).toBeNull();
});
