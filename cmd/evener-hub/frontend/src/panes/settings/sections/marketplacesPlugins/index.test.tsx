import { friendlyErrorMessage, type MarketplaceEntry, type PluginEntry, WireError } from "@evener/appwire-client";
import { deferRequest, FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../../../stores/connection";
import { extensionsStore, resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { getToasts, resetToastStoreForTests } from "../../../../widgets/toast/store";
import { alertReasons, statusReasons } from "../reasonTestUtils";
import { MarketplacesPluginsSection } from "./index";
import { chipKinds } from "./marketplacesTestUtils";

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
  fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] }));
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

test("cached inventory keeps install disabled throughout a delayed retry", async () => {
  const fake = connectSeededClient();
  fake.on("evener/marketplace/browse", () => ({ name: ACME.name, plugins: [{ name: "formatter" }] }));
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await screen.findByRole("button", { name: /linter/ });
  await user.click(screen.getByRole("radio", { name: "Browse" }));
  await user.click(screen.getByText(/acme-plugins/, { selector: "summary" }));
  await screen.findByRole("button", { name: "Install" });
  fake.on("evener/plugin/list", () => {
    throw new Error("read failed");
  });
  await act(async () => {
    await extensionsStore.getState().fetchPlugins();
  });
  expect((screen.getByRole("button", { name: "Install" }) as HTMLButtonElement).disabled).toBe(true);
  const release = deferRequest<{ plugins: PluginEntry[] }>(fake, "evener/plugin/list");
  await user.click(screen.getByRole("button", { name: "Retry plugins" }));
  expect(extensionsStore.getState()).toMatchObject({ plugins: [LINTER], pluginsLoading: true, pluginsError: null });
  expect((screen.getByRole("button", { name: "Install" }) as HTMLButtonElement).disabled).toBe(true);
  await user.click(screen.getByRole("button", { name: "Install" }));
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(fake.calls.filter((c) => c.method === "evener/plugin/install")).toHaveLength(0);
  await act(async () => release({ plugins: [LINTER] }));
  expect((screen.getByRole("button", { name: "Install" }) as HTMLButtonElement).disabled).toBe(false);
});

test("an install confirmation reconciles a target installed during inventory recovery", async () => {
  const fake = connectSeededClient();
  fake.on("evener/marketplace/browse", () => ({ name: ACME.name, plugins: [{ name: "formatter" }] }));
  const installed = { ...LINTER, plugin: "formatter", enabled: false, autoUpgrade: true };
  fake.on("evener/plugin/install", () => ({ plugins: [LINTER, { ...installed, enabled: true, autoUpgrade: false }] }));
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await screen.findByRole("button", { name: /linter/ });
  await user.click(screen.getByRole("radio", { name: "Browse" }));
  await user.click(screen.getByText(/acme-plugins/, { selector: "summary" }));
  await user.click(await screen.findByRole("button", { name: "Install" }));
  const release = deferRequest<{ plugins: PluginEntry[] }>(fake, "evener/plugin/list");
  await act(async () => {
    void extensionsStore.getState().fetchPlugins();
  });
  expect(
    (
      within(screen.getByRole("dialog", { name: "Install plugin" })).getByRole("button", {
        name: "Install",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  await act(async () => release({ plugins: [LINTER, installed] }));
  const confirmation = screen.queryByRole("dialog", { name: "Install plugin" });
  if (confirmation) await user.click(within(confirmation).getByRole("button", { name: "Install" }));
  expect(fake.calls.filter((c) => c.method === "evener/plugin/install")).toHaveLength(0);
  expect(extensionsStore.getState().plugins).toEqual([LINTER, installed]);
  expect(screen.queryByRole("dialog", { name: "Install plugin" })).toBeNull();
});

test("cached installed details remain inspectable through failed reads and delayed recovery without mutations", async () => {
  const fake = connectSeededClient();
  fake.on("evener/plugin/list", () => ({ plugins: [{ ...LINTER, updateAvailable: true }] }));
  fake.on("evener/plugin/enable", () => ({ plugins: [{ ...LINTER, enabled: true, autoUpgrade: true }] }));
  fake.on("evener/marketplace/browse", () => ({ name: ACME.name, plugins: [{ name: LINTER.plugin }] }));
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await screen.findByRole("button", { name: /linter/ });
  fake.on("evener/plugin/list", () => {
    throw new Error("read failed");
  });
  await act(async () => {
    await extensionsStore.getState().fetchPlugins();
  });
  await user.click(screen.getByRole("button", { name: /linter/ }));
  const details = screen.getByRole("dialog", { name: LINTER.plugin });
  expect(within(details).getByRole("switch", { name: "Enabled by default" }).getAttribute("aria-disabled")).toBe(
    "true",
  );
  await user.click(within(details).getByRole("switch", { name: "Enabled by default" }));
  await user.click(within(details).getByRole("switch", { name: "Auto-upgrade" }));
  expect(within(details).getByRole("button", { name: "Upgrade" }).getAttribute("aria-disabled")).toBe("true");
  await user.click(within(details).getByRole("button", { name: "Upgrade" }));
  expect((within(details).getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true);
  await user.click(within(details).getByRole("button", { name: "Close" }));
  const release = deferRequest<{ plugins: PluginEntry[] }>(fake, "evener/plugin/list");
  await user.click(screen.getByRole("button", { name: "Retry plugins" }));
  expect(screen.queryByRole("dialog")).toBeNull();
  await user.click(screen.getByRole("button", { name: /linter/ }));
  const recovering = screen.getByRole("dialog", { name: LINTER.plugin });
  expect(within(recovering).getByRole("switch", { name: "Enabled by default" }).getAttribute("aria-disabled")).toBe(
    "true",
  );
  await act(async () => release({ plugins: [{ ...LINTER, enabled: false, autoUpgrade: true }] }));
  expect(within(recovering).getByRole("switch", { name: "Enabled by default" }).getAttribute("aria-checked")).toBe(
    "false",
  );
  expect(within(recovering).getByRole("switch", { name: "Enabled by default" }).getAttribute("aria-disabled")).not.toBe(
    "true",
  );
  expect(
    fake.calls.filter((c) => /evener\/plugin\/(install|enable|disable|setAutoUpgrade|remove|upgrade)$/.test(c.method)),
  ).toHaveLength(0);
  await user.click(within(recovering).getByRole("switch", { name: "Enabled by default" }));
  expect(fake.calls.filter((c) => c.method === "evener/plugin/enable").map((c) => c.params)).toEqual([
    { plugin: LINTER.plugin, marketplace: LINTER.marketplace },
  ]);
  expect(within(recovering).getByRole("switch", { name: "Enabled by default" }).getAttribute("aria-checked")).toBe(
    "true",
  );
});

test("first plugin journey navigates through Sources and explicitly browses the added catalog without installing", async () => {
  const fake = connectFakeClient();
  fake.on("evener/marketplace/list", () => ({ marketplaces: [] }));
  fake.on("evener/plugin/list", () => ({ plugins: [] }));
  fake.on("evener/marketplace/add", () => ({ marketplaces: [ACME] }));
  fake.on("evener/marketplace/browse", () => ({
    name: ACME.name,
    plugins: [{ name: "formatter", description: "Fixture formatter" }],
  }));
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await user.click(await screen.findByRole("button", { name: "Browse plugins" }));
  expect(screen.getByRole("radio", { name: "Browse" }).getAttribute("aria-checked")).toBe("true");
  await user.click(screen.getByRole("button", { name: "Manage sources" }));
  expect(screen.getByRole("radio", { name: "Sources (0)" }).getAttribute("aria-checked")).toBe("true");
  await user.click(screen.getByRole("button", { name: "+ Add marketplace" }));
  await user.type(screen.getByLabelText("Git URL"), "https://example.com/acme.git");
  expect(fake.calls.filter((c) => c.method === "evener/marketplace/add")).toHaveLength(0);
  await user.click(
    within(screen.getByLabelText("Git URL").closest("form") as HTMLFormElement).getByRole("button", { name: "Add" }),
  );
  expect(fake.calls.filter((c) => c.method === "evener/marketplace/add").map((c) => c.params)).toEqual([
    { name: "", source: { kind: "url", url: "https://example.com/acme.git" } },
  ]);
  expect(screen.getByRole("radio", { name: "Sources (1)" }).getAttribute("aria-checked")).toBe("true");
  expect(fake.calls.filter((c) => c.method === "evener/marketplace/browse")).toHaveLength(0);
  await user.click(screen.getByRole("button", { name: "Browse plugins" }));
  expect(screen.getByRole("radio", { name: "Browse" }).getAttribute("aria-checked")).toBe("true");
  await user.click(screen.getByText(/acme-plugins/, { selector: "summary" }));
  expect(await screen.findByRole("list", { name: `${ACME.name} plugins` })).toBeTruthy();
  expect(fake.calls.filter((c) => /evener\/plugin\/(install|enable|disable)$/.test(c.method))).toHaveLength(0);
});

test.each(["plugin", "marketplace"] as const)(
  "a failed %s read leaves the other segment usable and retries only that read",
  async (failed) => {
    const fake = connectSeededClient();
    fake.on(`evener/${failed}/list`, () => {
      throw new Error("temporary failure");
    });
    fake.on("evener/marketplace/browse", () => ({ name: ACME.name, plugins: [{ name: "formatter" }] }));
    const user = userEvent.setup();
    render(<MarketplacesPluginsSection />);
    await waitFor(() => expect(statusReasons()).toEqual(["unavailable"]));
    if (failed === "marketplace") {
      await user.click(screen.getByRole("button", { name: /linter/ }));
      expect(screen.getByRole("dialog", { name: "linter" })).toBeTruthy();
      await user.click(screen.getByRole("button", { name: "Close" }));
    } else {
      await user.click(screen.getByRole("radio", { name: "Browse" }));
      await user.click(screen.getByText(/acme-plugins/, { selector: "summary" }));
      expect(await screen.findByRole("list", { name: `${ACME.name} plugins` })).toBeTruthy();
      expect((screen.getByRole("button", { name: "Install" }) as HTMLButtonElement).disabled).toBe(true);
    }
    const before = fake.calls.filter((c) => c.method.endsWith("/list")).map((c) => c.method);
    if (failed === "plugin") fake.on("evener/plugin/list", () => ({ plugins: [LINTER] }));
    else fake.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
    await user.click(
      screen.getByRole("button", { name: failed === "plugin" ? "Retry plugins" : "Retry marketplaces" }),
    );
    await waitFor(() => expect(statusReasons()).toEqual([]));
    expect(fake.calls.filter((c) => c.method.endsWith("/list")).map((c) => c.method)).toEqual([
      ...before,
      `evener/${failed}/list`,
    ]);
  },
);

test.each(["add", "edit"] as const)("retains an %s source draft through a failed read and recovery", async (kind) => {
  const fake = connectSeededClient();
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await user.click(await screen.findByRole("radio", { name: "Sources (1)" }));
  if (kind === "add") await user.click(screen.getByRole("button", { name: "+ Add marketplace" }));
  else await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  const label = kind === "add" ? "Git URL" : "owner/repo";
  await user.clear(screen.getByLabelText(label));
  await user.type(screen.getByLabelText(label), "draft/value");
  fake.on("evener/marketplace/list", () => {
    throw new Error("temporary failure");
  });
  await act(async () => {
    await extensionsStore.getState().fetchMarketplaces();
  });
  expect((screen.getByLabelText(label) as HTMLInputElement).value).toBe("draft/value");
  await user.type(screen.getByLabelText(label), "2");
  fake.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
  await act(async () => {
    await extensionsStore.getState().fetchMarketplaces();
  });
  expect((screen.getByLabelText(label) as HTMLInputElement).value).toBe("draft/value2");
  expect(fake.calls.filter((c) => /evener\/marketplace\/(add|edit)$/.test(c.method))).toHaveLength(0);
});

test("an untouched source follows canonical changes without arming Save", async () => {
  const fake = connectSeededClient();
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await user.click(await screen.findByRole("radio", { name: "Sources (1)" }));
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  fake.on("evener/marketplace/list", () => ({
    marketplaces: [{ ...ACME, source: { kind: "github", repo: "canonical/repo" } }],
  }));
  await act(async () => {
    await extensionsStore.getState().fetchMarketplaces();
  });
  expect((screen.getByLabelText("owner/repo") as HTMLInputElement).value).toBe("canonical/repo");
  expect((screen.getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(true);
  expect(fake.calls.filter((c) => c.method === "evener/marketplace/edit")).toHaveLength(0);
});

test("an incomplete source draft survives metadata refresh and same-client reconnect", async () => {
  const fake = connectSeededClient();
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await user.click(await screen.findByRole("radio", { name: "Sources (1)" }));
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  await user.clear(screen.getByLabelText("owner/repo"));
  fake.on("evener/marketplace/list", () => ({ marketplaces: [{ ...ACME, lastUpdated: 2 }] }));
  await act(async () => {
    fake.emitStateChange("reconnecting");
    fake.emitReady();
  });
  expect((screen.getByLabelText("owner/repo") as HTMLInputElement).value).toBe("");
  expect((screen.getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(true);
  expect(screen.queryByRole("button", { name: "Reload latest" })).toBeNull();
  expect(fake.calls.filter((c) => c.method === "evener/marketplace/edit")).toHaveLength(0);
});

test("a source pin change requires a conflict choice even when the visible repository is unchanged", async () => {
  const fake = connectSeededClient();
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await user.click(await screen.findByRole("radio", { name: "Sources (1)" }));
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  await user.type(screen.getByLabelText("Name"), "-draft");
  fake.on("evener/marketplace/list", () => ({
    marketplaces: [{ ...ACME, source: { ...ACME.source, ref: "release" } }],
  }));
  await act(async () => {
    await extensionsStore.getState().fetchMarketplaces();
  });
  expect((screen.getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(true);
  await user.click(screen.getByRole("button", { name: "Reload latest" }));
  expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe(ACME.name);
  expect(fake.calls.filter((c) => c.method === "evener/marketplace/edit")).toHaveLength(0);
});

test.each(["Reload latest", "Keep my edits"])(
  "an edited source requires explicit conflict resolution via %s",
  async (choice) => {
    const fake = connectSeededClient();
    fake.on("evener/marketplace/edit", () => ({
      marketplaces: [{ ...ACME, source: { kind: "github", repo: "draft/repo" } }],
    }));
    const user = userEvent.setup();
    render(<MarketplacesPluginsSection />);
    await user.click(await screen.findByRole("radio", { name: "Sources (1)" }));
    await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
    await user.clear(screen.getByLabelText("owner/repo"));
    await user.type(screen.getByLabelText("owner/repo"), "draft/repo");
    fake.on("evener/marketplace/list", () => ({
      marketplaces: [{ ...ACME, source: { kind: "github", repo: "canonical/repo" } }],
    }));
    await act(async () => {
      await extensionsStore.getState().fetchMarketplaces();
    });
    expect((screen.getByLabelText("owner/repo") as HTMLInputElement).value).toBe("draft/repo");
    expect((screen.getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(true);
    await user.click(screen.getByRole("button", { name: choice }));
    expect(fake.calls.filter((c) => c.method === "evener/marketplace/edit")).toHaveLength(0);
    if (choice === "Reload latest") {
      expect((screen.getByLabelText("owner/repo") as HTMLInputElement).value).toBe("canonical/repo");
      expect((screen.getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(true);
    } else {
      await user.click(screen.getByRole("button", { name: "Save" }));
      expect(fake.calls.filter((c) => c.method === "evener/marketplace/edit").map((c) => c.params)).toEqual([
        { name: ACME.name, source: { kind: "github", repo: "draft/repo" } },
      ]);
    }
  },
);

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

test("opening the view asks the hub for plugin updates once the lists load, and marks what it finds", async () => {
  const fake = connectFakeClient();
  fake.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
  const releaseList = deferRequest<{ plugins: PluginEntry[] }>(fake, "evener/plugin/list");
  fake.on("evener/plugin/checkUpdates", () => {
    // The hub now holds the answer, so the list read that follows carries it.
    fake.on("evener/plugin/list", () => ({ plugins: [{ ...LINTER, updateAvailable: true }] }));
    return { plugins: [{ ...LINTER, updateAvailable: true }] };
  });
  render(<MarketplacesPluginsSection />);
  await act(async () => {});
  expect(fake.calls.some((c) => c.method === "evener/plugin/checkUpdates")).toBe(false);

  await act(async () => releaseList({ plugins: [LINTER] }));
  await waitFor(() => expect(chipKinds()).toContain("update-available"));
  expect(fake.calls.filter((c) => c.method === "evener/plugin/checkUpdates")).toHaveLength(1);
});

test("a view whose plugin list failed to load asks the hub for no updates", async () => {
  const fake = connectFakeClient();
  fake.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
  fake.on("evener/plugin/list", () => {
    throw new Error("boom");
  });
  render(<MarketplacesPluginsSection />);
  await waitFor(() => expect(statusReasons()).toEqual(["unavailable"]));
  expect(fake.calls.some((c) => c.method === "evener/plugin/checkUpdates")).toBe(false);
});

test("keeps navigation available with one failure notice when the marketplace list fails to load", async () => {
  const fake = connectFakeClient();
  fake.on("evener/marketplace/list", () => {
    throw new Error("network down");
  });
  fake.on("evener/plugin/list", () => ({ plugins: [] }));
  render(<MarketplacesPluginsSection />);
  await waitFor(() => expect(statusReasons()).toEqual(["unavailable"]));
  // A load that fails as the section opens is a polite status, not an alert.
  expect(alertReasons()).toEqual([]);
  // friendlyErrorMessage keeps a raw JS error's message off the screen.
  expect(screen.getByRole("status").textContent).not.toContain("network down");
  expect(screen.getByRole("radiogroup", { name: "View" })).toBeTruthy();
});

test("shows one failed-to-load message when the plugin list fails to load", async () => {
  const fake = connectFakeClient();
  fake.on("evener/marketplace/list", () => ({ marketplaces: [] }));
  fake.on("evener/plugin/list", () => {
    throw new Error("boom");
  });
  render(<MarketplacesPluginsSection />);
  // One failure notice, however many fetches failed; friendlyErrorMessage
  // keeps the raw string off the screen.
  await waitFor(() => expect(statusReasons()).toEqual(["unavailable"]));
  expect(screen.queryByText("boom")).toBeNull();
});

// The extensions stores translate a list failure while it still has its type,
// so the notice renders the stored text as-is: a hub-reported explanation or a
// client-side diagnosis survives instead of collapsing to the generic one.
test.each([
  ["marketplace", "a hub-reported WireError", new WireError("marketplace registry is locked", -32000)],
  ["marketplace", "a client-side failure", new Error("AppwireClient internal detail")],
  ["plugin", "a hub-reported WireError", new WireError("plugin index is rebuilding", -32000)],
  ["plugin", "a client-side failure", new Error("AppwireClient internal detail")],
])("a %s list failure from %s shows that error's friendly form", async (list, _name, err) => {
  const fake = connectFakeClient();
  fake.on("evener/marketplace/list", () => {
    if (list === "marketplace") throw err;
    return { marketplaces: [] };
  });
  fake.on("evener/plugin/list", () => {
    if (list === "plugin") throw err;
    return { plugins: [] };
  });
  render(<MarketplacesPluginsSection />);
  await waitFor(() => expect(statusReasons()).toEqual(["unavailable"]));
  expect(screen.getByRole("status").textContent).toContain(friendlyErrorMessage(err));
});

test("the segment control carries the list counts and defaults to Installed", async () => {
  connectSeededClient();
  render(<MarketplacesPluginsSection />);
  const installed = await screen.findByRole("radio", { name: "Installed (1)" });
  expect(installed.getAttribute("aria-checked")).toBe("true");
  expect(screen.getByRole("radio", { name: "Browse" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByRole("radio", { name: "Sources (1)" }).getAttribute("aria-checked")).toBe("false");
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
  await user.click(await screen.findByRole("radio", { name: "Sources (1)" }));
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
  await user.click(await screen.findByRole("radio", { name: "Sources (1)" }));
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
  await user.click(await screen.findByRole("radio", { name: "Sources (1)" }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );

  await waitFor(() => expect(statusReasons()).toEqual(["unavailable"]));
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
  await user.click(await screen.findByRole("radio", { name: "Sources (1)" }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );

  await waitFor(() => expect(statusReasons()).toEqual(["unavailable"]));
  act(() => fake.emitNotification({ method: "evener/marketplace/updated", params: {} }));
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
  await user.click(await screen.findByRole("radio", { name: "Sources (1)" }));
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
  await user.click(await screen.findByRole("radio", { name: "Sources (1)" }));
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
  await user.click(await screen.findByRole("radio", { name: "Sources (1)" }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );
  await waitFor(() => expect(statusReasons()).toEqual(["unavailable"]));

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
  await user.click(await screen.findByRole("radio", { name: "Sources (1)" }));
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
  await user.click(await screen.findByText(/acme-plugins/, { selector: "summary" }));
  await waitFor(() => expect(screen.getByText("formatter")).toBeTruthy());

  // Rename it through its sheet, which re-browses the new name.
  await user.click(screen.getByRole("radio", { name: "Sources (1)" }));
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
  await user.click(await screen.findByText(/acme-plugins/, { selector: "summary" }));
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
    if (forwarded.method === "evener/launch/getLayer") return { pluginDirs: [] } as never;
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
  await user.click(await screen.findByRole("radio", { name: "Sources (1)" }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );
  await waitFor(() => expect(statusReasons()).toEqual(["unavailable"]));

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
  await user.click(await screen.findByRole("radio", { name: "Sources (1)" }));
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
  await remounted.click(await screen.findByRole("radio", { name: "Sources (1)" }));
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
  await remounted.click(await screen.findByRole("radio", { name: "Sources (1)" }));
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
  await remounted.click(await screen.findByRole("radio", { name: "Sources (0)" }));
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
  await remounted.click(await screen.findByRole("radio", { name: "Sources (1)" }));
  await remounted.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
  await remounted.click(screen.getByRole("button", { name: "Remove" }));
  await remounted.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );
  await waitFor(() => expect(removeAttempts).toBe(2));
});
