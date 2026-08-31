import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { keyID, type ResourceState } from "../../stores/navigation/types";
import { resetToastStoreForTests } from "../../widgets/toast/store";
import { resetMobileViewportForTests } from "../useIsMobile";
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
    mode: "v1",
    resources: new Map([[keyID(resource.key), resource]]),
  });
  navigationStore.setState({ loadPinCatalogPages: vi.fn(async () => undefined) as LoadPinCatalogPages });
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
  resetNavigationStoreForTests();
  seedPinCatalog();
});

afterEach(() => {
  cleanup();
  resetNavigationStoreForTests();
  // Drops any mobile matchMedia stub a drawer test installed.
  vi.unstubAllGlobals();
  resetMobileViewportForTests();
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

test("pane-only Verbosity follows Activity, precedes the first separator, and dispatches its callback", async () => {
  const user = userEvent.setup();
  const onOpenVerbosity = vi.fn();
  renderMenu({ onOpenVerbosity });
  await openMenu(user);

  const menu = screen.getByRole("menu");
  const verbosity = within(menu).getByRole("menuitem", { name: "Verbosity…" });
  const activity = within(menu).getByRole("menuitem", { name: "Activity" });
  const firstSeparator = within(menu).getAllByRole("separator")[0];
  if (!firstSeparator) throw new Error("Session menu is missing its first separator");
  expect(activity.compareDocumentPosition(verbosity) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
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

// --- mobile: the same entries render as a bottom Sheet drawer ---------------
// Mirrors ModelSwitchTrigger's mobile Sheet (design-system §11: mobile choice
// controls use the bottom Sheet). jsdom implements no matchMedia, so the
// mobile query is stubbed the same way AppShell.test.tsx stubs it.

function installMobileViewport(): void {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((media: string) => ({
      media,
      matches: media === "(max-width: 899px)",
      addEventListener: () => {},
      removeEventListener: () => {},
    })),
  );
  resetMobileViewportForTests();
}

test("mobile: the trigger opens a bottom Sheet named for the session, not a menu popover", async () => {
  installMobileViewport();
  const user = userEvent.setup();
  renderMenu({ session: navigationSession() });
  await openMenu(user);
  expect(screen.getByRole("dialog", { name: "My session" })).toBeTruthy();
  expect(screen.queryByRole("menu")).toBeNull();
});

test("mobile: the drawer lists the same entries in the same groups as the desktop menu", async () => {
  installMobileViewport();
  const user = userEvent.setup();
  renderMenu({ session: navigationSession() });
  await openMenu(user);
  const drawer = screen.getByRole("dialog", { name: "My session" });
  const labels = within(drawer)
    .getAllByRole("button")
    .map((el) => el.textContent)
    // The Sheet's own close button is chrome, not an entry (icon-only: no
    // text content of its own).
    .filter((label) => label !== "" && label !== "Close");
  expect(labels).toEqual([
    "Details",
    "Tasks ✓",
    "Activity",
    "Rename",
    "Pin this session…",
    "Archive",
    "Shut down",
    "Delete…",
  ]);
  expect(within(drawer).getAllByRole("separator")).toHaveLength(2);
});

test("mobile: tapping an entry closes the drawer, then runs the action", async () => {
  installMobileViewport();
  const user = userEvent.setup();
  const actions = renderMenu();
  await openMenu(user);
  await user.click(screen.getByRole("button", { name: "Tasks ✓" }));
  expect(screen.queryByRole("dialog", { name: "My session" })).toBeNull();
  expect(actions.onOpenPane).toHaveBeenCalledWith("tasks");
});

test("mobile: a dialog-opening entry stacks nothing on the drawer", async () => {
  installMobileViewport();
  const user = userEvent.setup();
  renderMenu();
  await openMenu(user);
  await user.click(screen.getByRole("button", { name: "Rename" }));
  // The drawer closed before the Rename dialog opened: exactly one dialog.
  expect(screen.getAllByRole("dialog")).toHaveLength(1);
  expect(screen.getByRole("dialog", { name: "Rename session" })).toBeTruthy();
});

test("mobile: disabled entries stay disabled", async () => {
  installMobileViewport();
  const user = userEvent.setup();
  renderMenu({ canRename: false });
  await openMenu(user);
  const rename = screen.getByRole("button", { name: "Rename" });
  expect((rename as HTMLButtonElement).disabled).toBe(true);
});

test("mobile: drawer rows meet the 44px --tap-min touch floor", () => {
  // jsdom evaluates no cascade, so the row sizing is verified from the CSS
  // source (the repo's stylesheet-assertion pattern). Comments are stripped
  // first so a comment quoting the declaration cannot satisfy the match.
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "sessionmenu.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rule = css.match(/\.drawerItem \{([\s\S]*?)\n\}/);
  expect(rule).not.toBeNull();
  expect(rule![1]).toContain("min-height: var(--tap-min)");
});
