import { type MarketplaceEntry, type PluginEntry, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../../../stores/connection";
import { extensionsStore, resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { getToasts, resetToastStoreForTests } from "../../../../widgets/toast/store";
import { MarketplacesPluginsSection } from "./index";

const LINTER: PluginEntry = {
  plugin: "linter",
  marketplace: "acme-plugins",
  version: "1.2.0",
  enabled: true,
  autoUpgrade: false,
  broken: false,
  installPath: "/x",
  installedAt: 1,
  lastUpdated: 1,
};

const ACME: MarketplaceEntry = {
  name: "acme-plugins",
  source: { kind: "github", repo: "acme/plugins" },
  lastUpdated: 1,
};

function cloneLitterError(data: unknown): WireError {
  return new WireError("marketplace unregistered, but its clone could not be removed", -32603, {
    evenerErrorInfo: "marketplaceUnregisteredCloneRemains",
    ...(data === undefined ? {} : { applied: data }),
  });
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

// Registers list handlers returning one marketplace + one plugin, so the
// section's own mount fetch populates the store (the same path production
// takes) instead of the test forcing store state around it.
function connectSeededClient(): FakeClient {
  const fake = connectFakeClient();
  fake.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
  fake.on("evener/plugin/list", () => ({ plugins: [LINTER] }));
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

test("shows a loading state before both the marketplace and plugin lists resolve", () => {
  const fake = connectFakeClient();
  fake.on("evener/marketplace/list", () => new Promise(() => {}));
  fake.on("evener/plugin/list", () => new Promise(() => {}));
  render(<MarketplacesPluginsSection />);
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
});

test("fetches both marketplaces and plugins in parallel on mount", async () => {
  const fake = connectFakeClient();
  fake.on("evener/marketplace/list", () => ({ marketplaces: [] }));
  fake.on("evener/plugin/list", () => ({ plugins: [] }));
  render(<MarketplacesPluginsSection />);
  expect(await screen.findByRole("radiogroup", { name: "View" })).toBeTruthy();
  expect(fake.calls.some((c) => c.method === "evener/marketplace/list")).toBe(true);
  expect(fake.calls.some((c) => c.method === "evener/plugin/list")).toBe(true);
});

test("shows one failed-to-load message replacing everything when the marketplace list fails to load", async () => {
  const fake = connectFakeClient();
  fake.on("evener/marketplace/list", () => {
    throw new Error("network down");
  });
  fake.on("evener/plugin/list", () => ({ plugins: [] }));
  render(<MarketplacesPluginsSection />);
  expect(await screen.findByText("Failed to load")).toBeTruthy();
  // error is converted via friendlyErrorMessage: raw JS errors become the generic message
  expect(screen.getByText("Something went wrong.")).toBeTruthy();
  // Assert the raw string no longer appears
  expect(screen.queryByText("network down")).toBeNull();
  expect(screen.queryByRole("radiogroup", { name: "View" })).toBeNull();
});

test("shows one failed-to-load message when the plugin list fails to load", async () => {
  const fake = connectFakeClient();
  fake.on("evener/marketplace/list", () => ({ marketplaces: [] }));
  fake.on("evener/plugin/list", () => {
    throw new Error("boom");
  });
  render(<MarketplacesPluginsSection />);
  expect(await screen.findByText("Failed to load")).toBeTruthy();
  // error is converted via friendlyErrorMessage: raw JS errors become the generic message
  expect(screen.getByText("Something went wrong.")).toBeTruthy();
  // Assert the raw string no longer appears
  expect(screen.queryByText("boom")).toBeNull();
});

test("the segment control carries the list counts and defaults to Installed", async () => {
  connectSeededClient();
  render(<MarketplacesPluginsSection />);
  const installed = await screen.findByRole("radio", { name: "Installed (1)" });
  expect(installed.getAttribute("aria-checked")).toBe("true");
  expect(screen.getByRole("radio", { name: "Browse" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByRole("radio", { name: "Marketplaces (1)" }).getAttribute("aria-checked")).toBe("false");
});

test("the Installed segment is the default view; other segments' lists are hidden", async () => {
  connectSeededClient();
  render(<MarketplacesPluginsSection />);
  expect(await screen.findByRole("list", { name: "Installed plugins" })).toBeTruthy();
  expect(screen.queryByRole("list", { name: "Marketplace browse tree" })).toBeNull();
  expect(screen.queryByRole("list", { name: "Marketplaces" })).toBeNull();
});

test("switching to the Browse segment shows the browse tree and hides installed rows", async () => {
  const user = userEvent.setup();
  connectSeededClient();
  render(<MarketplacesPluginsSection />);
  await user.click(await screen.findByRole("radio", { name: "Browse" }));
  expect(screen.getByRole("list", { name: "Marketplace browse tree" })).toBeTruthy();
  expect(screen.queryByRole("list", { name: "Installed plugins" })).toBeNull();
});

test("switching to the Marketplaces segment shows the marketplace list and hides installed rows", async () => {
  const user = userEvent.setup();
  connectSeededClient();
  render(<MarketplacesPluginsSection />);
  await user.click(await screen.findByRole("radio", { name: "Marketplaces (1)" }));
  expect(screen.getByRole("list", { name: "Marketplaces" })).toBeTruthy();
  expect(screen.queryByRole("list", { name: "Installed plugins" })).toBeNull();
});

test("clicking an installed row opens the detail sheet; its close button dismisses it", async () => {
  const user = userEvent.setup();
  connectSeededClient();
  render(<MarketplacesPluginsSection />);
  await user.click(await screen.findByRole("button", { name: /linter/ }));
  expect(await screen.findByRole("dialog", { name: "linter" })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Close" }));
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "linter" })).toBeNull());
});

test("switching segments while the detail sheet is open closes it", async () => {
  const user = userEvent.setup();
  connectSeededClient();
  render(<MarketplacesPluginsSection />);
  await user.click(await screen.findByRole("button", { name: /linter/ }));
  expect(await screen.findByRole("dialog", { name: "linter" })).toBeTruthy();
  await user.click(screen.getByRole("radio", { name: "Browse" }));
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "linter" })).toBeNull());
});

test("switching segments while the marketplace sheet is open closes it", async () => {
  connectSeededClient();
  render(<MarketplacesPluginsSection />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("radio", { name: "Marketplaces (1)" }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  expect(screen.getByRole("dialog", { name: "acme-plugins" })).toBeTruthy();
  await user.click(screen.getByRole("radio", { name: "Browse" }));
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "acme-plugins" })).toBeNull());
});

test("keeps an applied removal guard across failed reconciliation and sheet remount", async () => {
  const fake = connectFakeClient();
  let listCalls = 0;
  fake.on("evener/marketplace/list", () => {
    listCalls += 1;
    if (listCalls === 1) return { marketplaces: [ACME] };
    throw new Error("reconcile unavailable");
  });
  fake.on("evener/plugin/list", () => ({ plugins: [] }));
  fake.on("evener/marketplace/remove", () => {
    throw cloneLitterError(null);
  });
  render(<MarketplacesPluginsSection />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("radio", { name: "Marketplaces (1)" }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );

  await waitFor(() => expect(screen.getByText("Failed to load")).toBeTruthy());
  act(() => extensionsStore.setState({ marketplacesError: null }));
  expect(await screen.findByRole("dialog", { name: "acme-plugins" })).toBeTruthy();
  expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true);
});

test("a later accepted notification publication clears a same-name applied removal guard", async () => {
  // The notification's re-list waits out the store's 250ms refetch debounce.
  // Fake timers own that clock, and the stubbed `jest` global lets Testing
  // Library's findBy/waitFor polls advance it, so the wait costs no real time.
  vi.stubGlobal("jest", { advanceTimersByTime: vi.advanceTimersByTime });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  const fake = connectFakeClient();
  let listCalls = 0;
  fake.on("evener/marketplace/list", () => {
    listCalls += 1;
    if (listCalls === 2) throw new Error("reconcile unavailable");
    return { marketplaces: [ACME] };
  });
  fake.on("evener/plugin/list", () => ({ plugins: [] }));
  fake.on("evener/marketplace/remove", () => {
    throw cloneLitterError(null);
  });
  render(<MarketplacesPluginsSection />);
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
  await user.click(await screen.findByRole("radio", { name: "Marketplaces (1)" }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );

  await waitFor(() => expect(screen.getByText("Failed to load")).toBeTruthy());
  fake.emitNotification({ method: "evener/marketplace/updated", params: {} });
  await waitFor(() => expect(listCalls).toBe(3), { timeout: 1000 });
  await waitFor(() => expect(screen.getByRole("dialog", { name: "acme-plugins" })).toBeTruthy());
  expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
});

test("a delayed applied outcome after authoritative absence cannot leave a removal guard", async () => {
  const fake = connectFakeClient();
  let listCalls = 0;
  fake.on("evener/marketplace/list", () => {
    listCalls += 1;
    return { marketplaces: listCalls === 1 ? [ACME] : [] };
  });
  fake.on("evener/plugin/list", () => ({ plugins: [] }));
  let rejectRemoval: ((reason: unknown) => void) | undefined;
  fake.on(
    "evener/marketplace/remove",
    () =>
      new Promise<never>((_resolve, reject) => {
        rejectRemoval = reject;
      }),
  );
  render(<MarketplacesPluginsSection />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("radio", { name: "Marketplaces (1)" }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );
  await Promise.resolve();

  await act(async () => {
    await extensionsStore.getState().fetchMarketplaces();
  });
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "acme-plugins" })).toBeNull());
  await act(async () => {
    rejectRemoval?.(cloneLitterError([ACME]));
    await Promise.resolve();
  });
  await waitFor(() => expect(getToasts().some((toast) => toast.kind === "warning")).toBe(true));

  fake.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
  await act(async () => {
    await extensionsStore.getState().fetchMarketplaces();
  });
  await user.click(await screen.findByRole("radio", { name: "Marketplaces (1)" }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
});

test("clears an applied removal guard when the connection client changes", async () => {
  const first = connectFakeClient();
  let listCalls = 0;
  first.on("evener/marketplace/list", () => {
    listCalls += 1;
    if (listCalls === 2) throw new Error("reconcile unavailable");
    return { marketplaces: [ACME] };
  });
  first.on("evener/plugin/list", () => ({ plugins: [] }));
  first.on("evener/marketplace/remove", () => {
    throw cloneLitterError(null);
  });
  render(<MarketplacesPluginsSection />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("radio", { name: "Marketplaces (1)" }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );
  await waitFor(() => expect(screen.getByText("Failed to load")).toBeTruthy());

  const second = new FakeClient("ready");
  second.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
  second.on("evener/plugin/list", () => ({ plugins: [] }));
  act(() => connectionStore.getState().connect(second));
  await waitFor(() => expect(second.calls.some((call) => call.method === "evener/marketplace/list")).toBe(true));
  expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
});

test("a fenced outcome from the replaced client cannot leave a removal guard", async () => {
  const first = connectFakeClient();
  first.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
  first.on("evener/plugin/list", () => ({ plugins: [] }));
  let rejectRemoval: ((reason: unknown) => void) | undefined;
  first.on(
    "evener/marketplace/remove",
    () =>
      new Promise<never>((_resolve, reject) => {
        rejectRemoval = reject;
      }),
  );
  render(<MarketplacesPluginsSection />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("radio", { name: "Marketplaces (1)" }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );
  await Promise.resolve();

  const second = new FakeClient("ready");
  second.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
  second.on("evener/plugin/list", () => ({ plugins: [] }));
  act(() => connectionStore.getState().connect(second));
  await waitFor(() => expect(second.calls.some((call) => call.method === "evener/marketplace/list")).toBe(true));
  const secondListCallsBeforeOutcome = second.calls.filter((call) => call.method === "evener/marketplace/list").length;
  rejectRemoval?.(cloneLitterError(null));
  await waitFor(() => expect(screen.getByRole("dialog", { name: "acme-plugins" })).toBeTruthy());
  expect(second.calls.filter((call) => call.method === "evener/marketplace/list")).toHaveLength(
    secondListCallsBeforeOutcome,
  );
  expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
});

test("a rename carries the Browse expansion to the new name", async () => {
  const user = userEvent.setup();
  const fake = connectSeededClient();
  fake.on("evener/marketplace/browse", (params) => ({
    name: params.name,
    description: "Acme plugins catalog",
    plugins: [{ name: "formatter", description: "A formatter" }],
  }));
  fake.on("evener/marketplace/edit", () => ({ marketplaces: [{ ...ACME, name: "acme-plugins2" }] }));
  render(<MarketplacesPluginsSection />);
  await screen.findByText("linter");

  await user.click(screen.getByRole("radio", { name: "Browse" }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await waitFor(() => expect(screen.getByText("formatter")).toBeTruthy());

  // Rename it through its sheet, which re-browses the new name.
  await user.click(screen.getByRole("radio", { name: "Marketplaces (1)" }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.type(screen.getByLabelText("Name"), "2");
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(screen.getByRole("dialog", { name: "acme-plugins2" })).toBeTruthy());

  // The expansion set is keyed by name: still holding the old one, the
  // renamed marketplace renders collapsed over the catalog that save just
  // re-requested.
  await user.click(screen.getByRole("radio", { name: "Browse" }));
  expect(await screen.findByRole("list", { name: "acme-plugins2 plugins" })).toBeTruthy();
  expect(screen.getByText("formatter")).toBeTruthy();
});

test("Browse tree expansion survives a segment round trip (Installed → Browse → Installed → Browse)", async () => {
  const user = userEvent.setup();
  const fake = connectSeededClient();
  fake.on("evener/marketplace/browse", () => ({
    name: "acme-plugins",
    description: "Acme plugins catalog",
    plugins: [{ name: "formatter", description: "A formatter" }],
  }));
  render(<MarketplacesPluginsSection />);
  await screen.findByText("linter");

  // Switch to Browse and expand the marketplace tree
  await user.click(screen.getByRole("radio", { name: "Browse" }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await waitFor(() => expect(screen.getByText("formatter")).toBeTruthy());

  // Switch to Installed and back to Browse
  await user.click(screen.getByRole("radio", { name: /Installed/ }));
  await screen.findByText("linter");
  await user.click(screen.getByRole("radio", { name: "Browse" }));

  // The tree should still be expanded — formatter should be visible without re-expanding
  await waitFor(() => expect(screen.getByText("formatter")).toBeTruthy());
});

test("clears an applied removal guard when the selected host changes (same connection)", async () => {
  const fake = connectFakeClient();
  let betaListCalls = 0;
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { host: string; method: string };
    if (forwarded.method === "evener/marketplace/list") {
      if (forwarded.host === "beta") {
        betaListCalls += 1;
        if (betaListCalls === 1) return { marketplaces: [ACME] } as never;
        throw new Error("reconcile unavailable");
      }
      return { marketplaces: [ACME] } as never;
    }
    if (forwarded.method === "evener/plugin/list") return { plugins: [] } as never;
    if (forwarded.method === "evener/marketplace/remove") throw cloneLitterError(null);
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  const view = render(<MarketplacesPluginsSection host="beta" />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("radio", { name: "Marketplaces (1)" }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );
  await waitFor(() => expect(screen.getByText("Failed to load")).toBeTruthy());

  // Same connection, different host: the guard belongs to beta's catalog and
  // must not suppress the reminder for gamma's marketplace of the same name.
  view.rerender(<MarketplacesPluginsSection host="gamma" />);
  await waitFor(() => expect(screen.getByRole("dialog", { name: "acme-plugins" })).toBeTruthy());
  expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
});

// --- Whole-page lifetime tests ---------------------------------------------
// The page-level counterpart to the sheet tests above: the applied-removal
// guard lives on the whole settings section, so its lifecycle boundary is the
// section UNMOUNTING and remounting, not the sheet opening and closing. A late
// typed outcome published from a disposed sheet must not reach the remounted
// page's guard.

function cloneCleanupWarnings() {
  return getToasts().filter((toast) => toast.kind === "warning" && toast.text.includes("clone cleanup failed"));
}

// How many marketplace-list reads a client has served; each lifetime test
// snapshots it to show whether a late outcome started a re-fetch.
function marketplaceListCalls(fake: FakeClient): number {
  return fake.calls.filter((call) => call.method === "evener/marketplace/list").length;
}

// Opens acme's sheet from the Marketplaces segment and confirms its removal.
// The sheet is read from the store, so this works on a first mount and a remount.
async function openMarketplaceSheetAndConfirmRemoval(): Promise<void> {
  const user = userEvent.setup();
  await user.click(await screen.findByRole("radio", { name: "Marketplaces (1)" }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );
}

// Confirms a removal that never settles, returning the reject handle its typed
// outcome lands through. The extra microtask lets the store-bound request reach
// the fake before the caller unmounts the page.
async function beginPendingRemoval(fake: FakeClient): Promise<(reason: unknown) => void> {
  let rejectRemoval: ((reason: unknown) => void) | undefined;
  fake.on(
    "evener/marketplace/remove",
    () =>
      new Promise<never>((_resolve, reject) => {
        rejectRemoval = reject;
      }),
  );
  await openMarketplaceSheetAndConfirmRemoval();
  await Promise.resolve();
  return (reason) => rejectRemoval?.(reason);
}

// Lands a typed outcome and flushes the store update its continuation makes.
async function settleLateOutcome(rejectRemoval: (reason: unknown) => void, reason: unknown): Promise<void> {
  await act(async () => {
    rejectRemoval(reason);
    await Promise.resolve();
  });
}

// A list read that answers only when `release` is called. Holding reads pending
// pins behavior at a fixed publication version: every accepted list publish
// advances marketplacesPublicationVersion and retires a guard on its own, so a
// test that lets one land would hide a guard that leaked across a remount.
function heldListReads() {
  const pending: Array<(value: { marketplaces: MarketplaceEntry[] }) => void> = [];
  return {
    handler: () => new Promise<{ marketplaces: MarketplaceEntry[] }>((resolve) => pending.push(resolve)),
    // Settle every held read: the store's list revision keeps only the newest
    // read's answer, so leaving the latest one pending would fence this out.
    release: (marketplaces: MarketplaceEntry[]) => {
      for (const resolve of pending.splice(0)) resolve({ marketplaces });
    },
  };
}

test("a late applied outcome after the whole page unmounts leaves no guard on the remounted page and warns once", async () => {
  const fake = connectFakeClient();
  const held = heldListReads();
  let listCalls = 0;
  fake.on("evener/marketplace/list", () => {
    listCalls += 1;
    return listCalls === 1 ? { marketplaces: [ACME] } : held.handler();
  });
  fake.on("evener/plugin/list", () => ({ plugins: [] }));

  const view = render(<MarketplacesPluginsSection />);
  const rejectRemoval = await beginPendingRemoval(fake);
  view.unmount();

  // The typed applied outcome lands with the page disposed. Its continuation
  // starts a reconcile read (held, so no publication retires a guard on its
  // own) and reports the litter; the guard it tries to write belongs to a page
  // that no longer exists.
  await settleLateOutcome(rejectRemoval, cloneLitterError(null));
  await waitFor(() => expect(marketplaceListCalls(fake)).toBe(2));
  expect(cloneCleanupWarnings()).toHaveLength(1);

  render(<MarketplacesPluginsSection />);
  const remounted = userEvent.setup();
  await remounted.click(await screen.findByRole("radio", { name: "Marketplaces (1)" }));
  // The remount's own read is held too, so the store still lists acme at the
  // same publication version the late outcome marked at: a guard that leaked
  // across the remount would disable Remove here.
  await remounted.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
  expect(cloneCleanupWarnings()).toHaveLength(1);

  // The held reconcile read lands the authoritative list without acme.
  await act(async () => {
    held.release([]);
    await Promise.resolve();
  });
  await waitFor(() => expect(screen.queryByRole("button", { name: /acme-plugins/ })).toBeNull());
  expect(cloneCleanupWarnings()).toHaveLength(1);
});

test("an outcome from the replaced client cannot mark or block the remounted page and adds no list call", async () => {
  const first = connectFakeClient();
  first.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
  first.on("evener/plugin/list", () => ({ plugins: [] }));

  const view = render(<MarketplacesPluginsSection />);
  const rejectRemoval = await beginPendingRemoval(first);
  view.unmount();

  const second = new FakeClient("ready");
  second.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
  second.on("evener/plugin/list", () => ({ plugins: [] }));
  act(() => connectionStore.getState().connect(second));

  render(<MarketplacesPluginsSection />);
  const remounted = userEvent.setup();
  await remounted.click(await screen.findByRole("radio", { name: "Marketplaces (1)" }));
  await screen.findByRole("button", { name: /acme-plugins/ });
  const secondListCallsBefore = marketplaceListCalls(second);

  await settleLateOutcome(rejectRemoval, cloneLitterError(null));

  await remounted.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
  expect(marketplaceListCalls(second)).toBe(secondListCallsBefore);
  expect(getToasts().some((toast) => toast.kind === "warning")).toBe(false);
});

test("a late applied outcome against an already-reconciled list is not marked, does not re-fetch, and warns truthfully once", async () => {
  const fake = connectFakeClient();
  const held = heldListReads();
  let listCalls = 0;
  fake.on("evener/marketplace/list", () => {
    listCalls += 1;
    if (listCalls === 1) return { marketplaces: [ACME] };
    if (listCalls === 2) return { marketplaces: [] };
    return held.handler();
  });
  fake.on("evener/plugin/list", () => ({ plugins: [] }));

  const view = render(<MarketplacesPluginsSection />);
  const rejectRemoval = await beginPendingRemoval(fake);
  view.unmount();

  // An authoritative read already dropped the name before the outcome lands.
  await act(async () => {
    await extensionsStore.getState().fetchMarketplaces();
  });
  expect(extensionsStore.getState().marketplaces).toEqual([]);
  const listCallsBeforeOutcome = marketplaceListCalls(fake);

  await settleLateOutcome(rejectRemoval, cloneLitterError(null));
  await Promise.resolve();
  // Already reconciled: the outcome neither re-fetches nor writes a guard, and
  // the clone litter it names is the one truthful warning.
  expect(marketplaceListCalls(fake)).toBe(listCallsBeforeOutcome);
  expect(cloneCleanupWarnings()).toHaveLength(1);

  render(<MarketplacesPluginsSection />);
  const remounted = userEvent.setup();
  await remounted.click(await screen.findByRole("radio", { name: "Marketplaces (0)" }));
  expect(cloneCleanupWarnings()).toHaveLength(1);
  // The store is seeded directly at the outcome's own version - below the
  // remount's held read - so a guard the outcome should not have written would
  // disable this row's Remove.
  act(() => extensionsStore.setState({ marketplaces: [ACME] }));
  await remounted.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
});

test("an ordinary removal failure stays retryable across a whole-page remount", async () => {
  const fake = connectFakeClient();
  fake.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
  fake.on("evener/plugin/list", () => ({ plugins: [] }));
  let removeAttempts = 0;
  fake.on("evener/marketplace/remove", () => {
    removeAttempts += 1;
    throw new Error("network hiccup");
  });

  const view = render(<MarketplacesPluginsSection />);
  await openMarketplaceSheetAndConfirmRemoval();
  await waitFor(() => expect(removeAttempts).toBe(1));
  // Ordinary failures carry no typed outcome, so the sheet keeps the retryable
  // error behavior and writes no applied-removal guard.
  await waitFor(() =>
    expect(
      getToasts().some((toast) => toast.kind === "error" && toast.text.includes("Remove marketplace failed")),
    ).toBe(true),
  );
  expect(cloneCleanupWarnings()).toHaveLength(0);

  view.unmount();
  render(<MarketplacesPluginsSection />);
  // Re-opened by hand rather than through the helper: the point is to inspect
  // Remove before the retry.
  const remounted = userEvent.setup();
  await remounted.click(await screen.findByRole("radio", { name: "Marketplaces (1)" }));
  await remounted.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
  await remounted.click(screen.getByRole("button", { name: "Remove" }));
  await remounted.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );
  await waitFor(() => expect(removeAttempts).toBe(2));
});
