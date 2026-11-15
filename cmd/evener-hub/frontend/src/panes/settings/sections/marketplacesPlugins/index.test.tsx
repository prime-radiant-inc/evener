import { friendlyErrorMessage, type MarketplaceEntry, type PluginEntry, WireError } from "@evener/appwire-client";
import { deferRequest, FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore, resetConnectionStoreForTests } from "../../../../stores/connection";
import { extensionsStore, resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { connectReadyFakeClient } from "../../../../testing/connectionTestUtils";
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

// Registers list handlers returning one marketplace + one plugin, so the
// section's own mount fetch populates the store (the same path production
// takes) instead of the test forcing store state around it.
function connectSeededClient(): FakeClient {
  const fake = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
  fake.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
  fake.on("evener/plugin/list", () => ({ plugins: [LINTER] }));
  return fake;
}

beforeEach(() => {
  resetConnectionStoreForTests();
  resetExtensionsStoreForTests();
  resetToastStoreForTests();
});

test("cached inventory keeps install disabled throughout a delayed retry", async () => {
  const fake = connectSeededClient();
  fake.on("evener/marketplace/browse", () => ({ name: ACME.name, plugins: [{ name: "formatter" }] }));
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await screen.findByRole("button", { name: /linter/ });
  await user.click(screen.getByRole("radio", { name: /^Marketplaces/ }));
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
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

test("catalog details open after the initial plugin list fails without allowing writes", async () => {
  const fake = connectSeededClient();
  fake.on("evener/plugin/list", () => {
    throw new Error("initial-list-failure-sentinel");
  });
  fake.on("evener/marketplace/browse", () => ({ name: ACME.name, plugins: [{ name: "formatter" }] }));
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await waitFor(() => expect(extensionsStore.getState().pluginsError).not.toBeNull());
  expect(extensionsStore.getState().plugins).toBeNull();
  await user.click(screen.getByRole("radio", { name: /^Marketplaces/ }));
  await user.click(screen.getByRole("button", { name: new RegExp(ACME.name) }));
  const catalog = await screen.findByRole("list", { name: `${ACME.name} plugins` });
  await user.click(within(catalog).getByRole("button", { name: /formatter/ }));
  const details = document.querySelector('[data-kind="plugin-detail"]');
  if (!(details instanceof HTMLElement)) throw new Error("no catalog details expanded");
  expect(details.getAttribute("data-mode")).toBe("catalog");
  expect(within(details).queryByRole("switch")).toBeNull();
  const install = within(details).getByRole("button", { name: "Install" });
  expect((install as HTMLButtonElement).disabled).toBe(true);
  await user.click(install);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(
    fake.calls.filter((call) =>
      /evener\/plugin\/(install|enable|disable|setAutoUpgrade|remove|upgrade)$/.test(call.method),
    ),
  ).toHaveLength(0);
});

test("an install confirmation reconciles a target installed during inventory recovery", async () => {
  const fake = connectSeededClient();
  fake.on("evener/marketplace/browse", () => ({ name: ACME.name, plugins: [{ name: "formatter" }] }));
  const installed = { ...LINTER, plugin: "formatter", enabled: false, autoUpgrade: true };
  fake.on("evener/plugin/install", () => ({ plugins: [LINTER, { ...installed, enabled: true, autoUpgrade: false }] }));
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await screen.findByRole("button", { name: /linter/ });
  await user.click(screen.getByRole("radio", { name: /^Marketplaces/ }));
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
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
  const details = document.querySelector('[data-kind="plugin-detail"]');
  if (!(details instanceof HTMLElement)) throw new Error("no details expanded");
  expect(within(details).getByRole("switch", { name: "Enabled by default" }).getAttribute("aria-disabled")).toBe(
    "true",
  );
  await user.click(within(details).getByRole("switch", { name: "Enabled by default" }));
  await user.click(within(details).getByRole("switch", { name: "Auto-upgrade" }));
  expect(within(details).getByRole("button", { name: "Upgrade" }).getAttribute("aria-disabled")).toBe("true");
  await user.click(within(details).getByRole("button", { name: "Upgrade" }));
  expect((within(details).getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true);
  await user.click(within(details).getByRole("button", { name: "Hide details" }));
  const release = deferRequest<{ plugins: PluginEntry[] }>(fake, "evener/plugin/list");
  await user.click(screen.getByRole("button", { name: "Retry plugins" }));
  expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
  await user.click(screen.getByRole("button", { name: /linter/ }));
  const recovering = document.querySelector('[data-kind="plugin-detail"]');
  if (!(recovering instanceof HTMLElement)) throw new Error("no recovering details expanded");
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
  const fake = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
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
  expect(screen.getByRole("radio", { name: /^Marketplaces/ }).getAttribute("aria-checked")).toBe("true");
  await user.click(screen.getByRole("button", { name: "+ Add marketplace" }));
  await user.type(screen.getByLabelText("Git URL"), "https://example.com/acme.git");
  expect(fake.calls.filter((c) => c.method === "evener/marketplace/add")).toHaveLength(0);
  await user.click(
    within(screen.getByLabelText("Git URL").closest("form") as HTMLFormElement).getByRole("button", { name: "Add" }),
  );
  expect(fake.calls.filter((c) => c.method === "evener/marketplace/add").map((c) => c.params)).toEqual([
    { name: "", source: { kind: "url", url: "https://example.com/acme.git" } },
  ]);
  expect(screen.getByRole("radio", { name: /^Marketplaces/ }).getAttribute("aria-checked")).toBe("true");
  expect(fake.calls.filter((c) => c.method === "evener/marketplace/browse")).toHaveLength(0);
  expect(screen.getByRole("radio", { name: /^Marketplaces/ }).getAttribute("aria-checked")).toBe("true");
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
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
      expect(document.querySelector('[data-kind="plugin-detail"]')).not.toBeNull();
      await user.click(screen.getByRole("button", { name: "Hide details" }));
    } else {
      await user.click(screen.getByRole("radio", { name: /^Marketplaces/ }));
      await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
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
  await user.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  if (kind === "add") await user.click(screen.getByRole("button", { name: "+ Add marketplace" }));
  else {
    await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
    await user.click(screen.getByRole("button", { name: "Edit" }));
  }
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
  await user.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  await user.click(await screen.findByRole("button", { name: "Edit" }));
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
  await user.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  await user.click(await screen.findByRole("button", { name: "Edit" }));
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
  await user.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  await user.click(await screen.findByRole("button", { name: "Edit" }));
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
    await user.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
    await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
    await user.click(await screen.findByRole("button", { name: "Edit" }));
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
  const fake = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
  fake.on("evener/marketplace/list", () => new Promise(() => {}));
  fake.on("evener/plugin/list", () => new Promise(() => {}));
  render(<MarketplacesPluginsSection />);
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
});

test("fetches both marketplaces and plugins in parallel on mount", async () => {
  const fake = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
  fake.on("evener/marketplace/list", () => ({ marketplaces: [] }));
  fake.on("evener/plugin/list", () => ({ plugins: [] }));
  render(<MarketplacesPluginsSection />);
  expect(await screen.findByRole("radiogroup", { name: "View" })).toBeTruthy();
  expect(fake.calls.some((c) => c.method === "evener/marketplace/list")).toBe(true);
  expect(fake.calls.some((c) => c.method === "evener/plugin/list")).toBe(true);
});

test("opening the view asks the hub for plugin updates once the lists load, and marks what it finds", async () => {
  const fake = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
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
  const fake = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
  fake.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
  fake.on("evener/plugin/list", () => {
    throw new Error("boom");
  });
  render(<MarketplacesPluginsSection />);
  await waitFor(() => expect(statusReasons()).toEqual(["unavailable"]));
  expect(fake.calls.some((c) => c.method === "evener/plugin/checkUpdates")).toBe(false);
});

test("keeps navigation available with one failure notice when the marketplace list fails to load", async () => {
  const fake = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
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
  const fake = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
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
  const fake = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
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
  expect(screen.getByRole("radio", { name: /^Marketplaces/ }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByRole("radio", { name: "Directories" }).getAttribute("aria-checked")).toBe("false");
});

test("the Installed segment is the default view; other segments' lists are hidden", async () => {
  connectSeededClient();
  render(<MarketplacesPluginsSection />);
  expect(await screen.findByRole("list", { name: "Installed plugins" })).toBeTruthy();
  expect(screen.queryByRole("list", { name: "Marketplaces" })).toBeNull();
  expect(screen.queryByRole("list", { name: /^Marketplaces/ })).toBeNull();
});

test("switching to the Browse segment shows the browse tree and hides installed rows", async () => {
  const user = userEvent.setup();
  connectSeededClient();
  render(<MarketplacesPluginsSection />);
  await user.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  expect(screen.getByRole("list", { name: "Marketplaces" })).toBeTruthy();
  expect(screen.queryByRole("list", { name: "Installed plugins" })).toBeNull();
});

test("switching to the Marketplaces segment shows the marketplace list and hides installed rows", async () => {
  const user = userEvent.setup();
  connectSeededClient();
  render(<MarketplacesPluginsSection />);
  await user.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  expect(screen.getByRole("list", { name: /^Marketplaces/ })).toBeTruthy();
  expect(screen.queryByRole("list", { name: "Installed plugins" })).toBeNull();
});

test("clicking an installed row expands its details inline; the Hide details action collapses them", async () => {
  const user = userEvent.setup();
  connectSeededClient();
  render(<MarketplacesPluginsSection />);
  await user.click(await screen.findByRole("button", { name: /linter/ }));
  expect(document.querySelector('[data-kind="plugin-detail"]')).not.toBeNull();
  expect(screen.queryByRole("dialog")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Hide details" }));
  expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
});

test("switching segments while a row's details are expanded collapses them", async () => {
  const user = userEvent.setup();
  connectSeededClient();
  render(<MarketplacesPluginsSection />);
  await user.click(await screen.findByRole("button", { name: /linter/ }));
  expect(document.querySelector('[data-kind="plugin-detail"]')).not.toBeNull();
  await user.click(screen.getByRole("radio", { name: /^Marketplaces/ }));
  expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
});

test("Escape on the expanded row collapses its details and marks the key handled", async () => {
  const user = userEvent.setup();
  connectSeededClient();
  const parentKeys: boolean[] = [];
  // The pane's own Escape listener sits at the document; a bubbled event that
  // arrives already handled is what keeps the pane open.
  const listener = (event: KeyboardEvent): void => {
    if (event.key === "Escape") parentKeys.push(event.defaultPrevented);
  };
  document.addEventListener("keydown", listener);
  render(<MarketplacesPluginsSection />);
  const row = await screen.findByRole("button", { name: /linter/ });
  await user.click(row);
  expect(document.querySelector('[data-kind="plugin-detail"]')).not.toBeNull();
  row.focus();

  await user.keyboard("{Escape}");
  document.removeEventListener("keydown", listener);

  // The row dismisses the details it opened - an ancestor (the Settings pane)
  // sees the Escape already handled and keeps itself open.
  expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
  expect(parentKeys).toEqual([true]);
  expect(row.getAttribute("aria-expanded")).toBe("false");
});

test("an expandable installed row exposes its expansion state and points at its body", async () => {
  const user = userEvent.setup();
  connectSeededClient();
  render(<MarketplacesPluginsSection />);
  const row = await screen.findByRole("button", { name: /linter/ });
  expect(row.getAttribute("aria-expanded")).toBe("false");
  expect(row.getAttribute("aria-controls")).toBeNull();

  await user.click(row);

  expect(row.getAttribute("aria-expanded")).toBe("true");
  const controls = row.getAttribute("aria-controls");
  expect(controls).not.toBeNull();
  expect(document.getElementById(controls ?? "")?.getAttribute("data-kind")).toBe("plugin-detail");
});

test("a catalog row's opener exposes its expansion state on the button itself", async () => {
  const fake = connectSeededClient();
  fake.on("evener/marketplace/browse", () => ({ name: ACME.name, plugins: [{ name: "formatter" }] }));
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await screen.findByRole("button", { name: /linter/ });
  await user.click(screen.getByRole("radio", { name: /^Marketplaces/ }));
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  await screen.findByRole("button", { name: "Install" });

  // A not-installed catalog row splits around its Install control; the
  // opener is the main-area button, and the expansion state belongs there.
  const opener = screen.getByRole("button", { name: /formatter/ });
  expect(opener.getAttribute("aria-expanded")).toBe("false");
  await user.click(opener);

  expect(opener.getAttribute("aria-expanded")).toBe("true");
  const controls = opener.getAttribute("aria-controls");
  expect(controls).not.toBeNull();
  expect(document.getElementById(controls ?? "")?.getAttribute("data-kind")).toBe("plugin-detail");
});

test("a marketplace's expansion survives a segment round trip", async () => {
  connectSeededClient();
  render(<MarketplacesPluginsSection />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  expect(screen.getByRole("list", { name: "acme-plugins plugins" })).toBeTruthy();
  await user.click(screen.getByRole("radio", { name: /^Installed/ }));
  await user.click(screen.getByRole("radio", { name: /^Marketplaces/ }));
  expect(screen.getByRole("list", { name: "acme-plugins plugins" })).toBeTruthy();
});

test("keeps an applied removal guard across failed reconciliation and sheet remount", async () => {
  const fake = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
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
  await user.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(await screen.findByRole("button", { name: "Edit" }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );

  await waitFor(() => expect(statusReasons()).toEqual(["unavailable"]));
  act(() => extensionsStore.setState({ marketplacesError: null }));
  expect(await screen.findByRole("button", { name: "Refresh" })).toBeTruthy();
  expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true);
});

test("a later accepted notification publication clears a same-name applied removal guard", async () => {
  // The notification's re-list waits out the store's 250ms refetch debounce;
  // real timers with a bounded wait keep RTL's polling act-aware.
  const fake = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
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
  const user = userEvent.setup();
  await user.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(await screen.findByRole("button", { name: "Edit" }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );

  await waitFor(() => expect(statusReasons()).toEqual(["unavailable"]));
  act(() => fake.emitNotification({ method: "evener/marketplace/updated", params: {} }));
  await waitFor(() => expect(listCalls).toBe(3), { timeout: 2000 });
  await act(async () => {
    await waitFor(() => expect(screen.getByRole("button", { name: "Refresh" })).toBeTruthy());
  });
  // The replaced client's confirm is still open in the body, so the guard
  // check reads the enabled body action, not the dialog's confirm.
  expect(screen.getAllByRole("button", { name: "Remove" }).some((b) => !(b as HTMLButtonElement).disabled)).toBe(true);
});

test("a delayed applied outcome after authoritative absence cannot leave a removal guard", async () => {
  const fake = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
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
  await user.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(await screen.findByRole("button", { name: "Edit" }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );
  await Promise.resolve();

  await act(async () => {
    await extensionsStore.getState().fetchMarketplaces();
  });
  await waitFor(() => expect(screen.queryByRole("button", { name: "Refresh" })).toBeNull());
  await act(async () => {
    rejectRemoval?.(cloneLitterError([ACME]));
    await Promise.resolve();
  });
  await waitFor(() => expect(getToasts().some((toast) => toast.kind === "warning")).toBe(true));

  fake.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
  await act(async () => {
    await extensionsStore.getState().fetchMarketplaces();
  });
  await user.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(await screen.findByRole("button", { name: "Edit" }));
  expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
});

test("clears an applied removal guard when the connection client changes", async () => {
  const first = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
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
  await user.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(await screen.findByRole("button", { name: "Edit" }));
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
  const first = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
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
  await user.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(await screen.findByRole("button", { name: "Edit" }));
  const body = document.querySelector('[data-kind="marketplace-detail"]');
  if (!(body instanceof HTMLElement)) throw new Error("no marketplace body expanded");
  await user.click(within(body).getByRole("button", { name: "Remove" }));
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
  await waitFor(() => expect(screen.getByRole("button", { name: "Refresh" })).toBeTruthy());
  expect(second.calls.filter((call) => call.method === "evener/marketplace/list")).toHaveLength(
    secondListCallsBeforeOutcome,
  );
  // The reconnect resets the store, so the body comes back with its read.
  await waitFor(() => expect(document.querySelector('[data-kind="marketplace-detail"]')).not.toBeNull());
  const fencedBody = document.querySelector('[data-kind="marketplace-detail"]');
  if (!(fencedBody instanceof HTMLElement)) throw new Error("no marketplace body after the reconnect");
  // The replaced client's confirm is still open inside the body, so the
  // guard check reads the enabled body action, not the dialog's confirm.
  expect(
    within(fencedBody)
      .getAllByRole("button", { name: "Remove" })
      .some((b) => !(b as HTMLButtonElement).disabled),
  ).toBe(true);
});

test("a rename carries the marketplaces expansion to the new name", async () => {
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

  await user.click(screen.getByRole("radio", { name: /^Marketplaces/ }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(await screen.findByRole("button", { name: "Edit" }));
  await waitFor(() => expect(screen.getByText("formatter")).toBeTruthy());

  await user.type(screen.getByLabelText("Name"), "2");
  await user.click(screen.getByRole("button", { name: "Save" }));
  expect(await screen.findByRole("button", { name: /acme-plugins2/ })).toBeTruthy();
  // The expansion followed the rename and re-browed under the new name.
  expect(await screen.findByRole("list", { name: "acme-plugins2 plugins" })).toBeTruthy();
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
  await user.click(screen.getByRole("radio", { name: /^Marketplaces/ }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(await screen.findByRole("button", { name: "Edit" }));
  await waitFor(() => expect(screen.getByText("formatter")).toBeTruthy());

  // Switch to Installed and back to Browse
  await user.click(screen.getByRole("radio", { name: /Installed/ }));
  await screen.findByText("linter");
  await user.click(screen.getByRole("radio", { name: /^Marketplaces/ }));

  // The tree should still be expanded — formatter should be visible without re-expanding
  await waitFor(() => expect(screen.getByText("formatter")).toBeTruthy());
});

test("clears an applied removal guard when the selected host changes (same connection)", async () => {
  const fake = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
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
  await user.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(await screen.findByRole("button", { name: "Edit" }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );
  await waitFor(() => expect(statusReasons()).toEqual(["unavailable"]));

  // Same connection, different host: the guard belongs to beta's catalog and
  // must not suppress the reminder for gamma's marketplace of the same name.
  view.rerender(<MarketplacesPluginsSection host="gamma" />);
  await waitFor(() => expect(screen.getByRole("button", { name: "Refresh" })).toBeTruthy());
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
  await user.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  await user.click(await screen.findByRole("button", { name: "Edit" }));
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
  const fake = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
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
  await remounted.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
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
  const first = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
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
  await remounted.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  await screen.findByRole("button", { name: /acme-plugins/ });
  const secondListCallsBefore = marketplaceListCalls(second);

  await settleLateOutcome(rejectRemoval, cloneLitterError(null));

  await remounted.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
  expect(marketplaceListCalls(second)).toBe(secondListCallsBefore);
  expect(getToasts().some((toast) => toast.kind === "warning")).toBe(false);
});

test("a late applied outcome against an already-reconciled list is not marked, does not re-fetch, and warns truthfully once", async () => {
  const fake = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
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
  await remounted.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  expect(cloneCleanupWarnings()).toHaveLength(1);
  // The store is seeded directly at the outcome's own version - below the
  // remount's held read - so a guard the outcome should not have written would
  // disable this row's Remove.
  act(() => extensionsStore.setState({ marketplaces: [ACME] }));
  await remounted.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
});

test("an ordinary removal failure stays retryable across a whole-page remount", async () => {
  const fake = connectReadyFakeClient((fake) => fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] })));
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
  await remounted.click(await screen.findByRole("radio", { name: /^Marketplaces/ }));
  await remounted.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  const remountBody = document.querySelector('[data-kind="marketplace-detail"]');
  if (!(remountBody instanceof HTMLElement)) throw new Error("no marketplace body on the remount");
  expect((within(remountBody).getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
  await remounted.click(within(remountBody).getByRole("button", { name: "Remove" }));
  await remounted.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );
  await waitFor(() => expect(removeAttempts).toBe(2));
});

test("a catalog row's main area expands its details in catalog mode, with no components read", async () => {
  const fake = connectSeededClient();
  fake.on("evener/marketplace/browse", () => ({
    name: ACME.name,
    plugins: [{ name: "formatter", description: "Fixture formatter" }],
  }));
  fake.on("evener/plugin/components", (params) => ({
    results: params.targets.map((t) => ({
      plugin: t.plugin,
      marketplace: t.marketplace,
      components: { skills: ["run"] },
    })),
  }));
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await screen.findByRole("button", { name: /linter/ });
  await user.click(screen.getByRole("radio", { name: /^Marketplaces/ }));
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  await screen.findByRole("list", { name: `${ACME.name} plugins` });
  const componentsCallsBefore = fake.calls.filter((c) => c.method === "evener/plugin/components").length;

  await user.click(screen.getByRole("button", { name: /formatter/ }));
  const details = document.querySelector('[data-kind="plugin-detail"]');
  if (!(details instanceof HTMLElement)) throw new Error("no catalog details expanded");
  // Catalog mode: description + metadata + the Install footer, no switches
  // and no Contents block (spec decision 2).
  expect(details.getAttribute("data-mode")).toBe("catalog");
  expect(within(details).getByText("Fixture formatter")).toBeTruthy();
  expect(within(details).queryByRole("switch")).toBeNull();
  expect(details.querySelector('[data-kind="plugin-contents"]')).toBeNull();
  expect(within(details).getByRole("button", { name: "Install" })).toBeTruthy();
  expect(within(details).queryByRole("button", { name: "Remove" })).toBeNull();
  // Expanding the catalog details reads no components: no new call, and no
  // components call ever named the not-installed plugin.
  expect(fake.calls.filter((c) => c.method === "evener/plugin/components")).toHaveLength(componentsCallsBefore);
  expect(
    fake.calls.some(
      (c) =>
        c.method === "evener/plugin/components" &&
        (c.params as { targets: { plugin?: string }[] }).targets.some((t) => t.plugin === "formatter"),
    ),
  ).toBe(false);
});

test("an install through the catalog details returns focus to the row it reshaped", async () => {
  const fake = connectSeededClient();
  fake.on("evener/marketplace/browse", () => ({
    name: ACME.name,
    plugins: [{ name: "formatter", description: "Fixture formatter" }],
  }));
  fake.on("evener/plugin/install", () => ({
    plugins: [
      LINTER,
      {
        plugin: "formatter",
        marketplace: ACME.name,
        version: "1.4.0",
        enabled: true,
        autoUpgrade: false,
        broken: false,
        installPath: "/formatter",
        installedAt: 1,
        lastUpdated: 1,
      },
    ],
  }));
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await screen.findByRole("button", { name: /linter/ });
  await user.click(screen.getByRole("radio", { name: /^Marketplaces/ }));
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  await screen.findByRole("list", { name: `${ACME.name} plugins` });
  // Expand the row, then install through the details' own Install action,
  // which routes to the list's single confirm.
  await user.click(screen.getByRole("button", { name: /formatter/ }));
  const details = document.querySelector('[data-kind="plugin-detail"]');
  if (!(details instanceof HTMLElement)) throw new Error("no catalog details expanded");
  await user.click(within(details).getByRole("button", { name: "Install" }));
  const confirm = screen.getByRole("dialog", { name: "Install plugin" });
  await user.click(within(confirm).getByRole("button", { name: "Install" }));
  // The install's fresh list reshapes the row - the split row's main button
  // is replaced by the whole-row button - and the confirm's FocusScope loses
  // its opener.
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Install plugin" })).toBeNull());
  const reshapedRow = screen.getByRole("button", { name: /formatter/ });
  expect(document.activeElement).toBe(reshapedRow);
});

const TIDY: PluginEntry = {
  plugin: "tidy",
  marketplace: "acme-plugins",
  version: "2.0.0",
  enabled: false,
  autoUpgrade: true,
  broken: false,
  installPath: "/t",
  installedAt: 1,
  lastUpdated: 1,
};

test("tapping an installed row expands its details inline; tapping again collapses", async () => {
  const fake = connectSeededClient();
  fake.on("evener/plugin/components", (params) => ({
    results: params.targets.map((t) => ({
      plugin: t.plugin,
      marketplace: t.marketplace,
      components: { skills: ["run"] },
    })),
  }));
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await screen.findByRole("button", { name: /linter/ });
  await user.click(screen.getByRole("button", { name: /linter/ }));
  // The row's details expand in place - no dialog opens.
  expect(document.querySelector('[data-kind="plugin-detail"]')).not.toBeNull();
  expect(screen.queryByRole("dialog")).toBeNull();
  await user.click(screen.getByRole("button", { name: /linter/ }));
  expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
});

test("expanding one row collapses another row's details", async () => {
  const fake = connectSeededClient();
  fake.on("evener/plugin/list", () => ({ plugins: [LINTER, TIDY] }));
  fake.on("evener/plugin/components", (params) => ({
    results: params.targets.map((t) => ({
      plugin: t.plugin,
      marketplace: t.marketplace,
      components: { skills: ["run"] },
    })),
  }));
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await screen.findByRole("button", { name: /linter/ });
  await user.click(screen.getByRole("button", { name: /tidy/ }));
  expect(document.querySelectorAll('[data-kind="plugin-detail"]')).toHaveLength(1);
  await user.click(screen.getByRole("button", { name: /linter/ }));
  // Still one open detail - the newly expanded row, not both.
  const openDetails = document.querySelectorAll('[data-kind="plugin-detail"]');
  expect(openDetails).toHaveLength(1);
  expect(openDetails[0]?.getAttribute("data-mode")).toBe("installed");
  expect(within(openDetails[0] as HTMLElement).getByText("Enabled by default")).toBeTruthy();
});

test("collapsing installed details from the body's action returns focus to the row", async () => {
  const user = userEvent.setup();
  connectSeededClient();
  render(<MarketplacesPluginsSection />);
  await user.click(await screen.findByRole("button", { name: /linter/ }));
  // The body's own exit is the focused control; collapsing unmounts it.
  await user.click(screen.getByRole("button", { name: "Hide details" }));
  expect(document.querySelector('[data-kind="plugin-detail"]')).toBeNull();
  expect(document.activeElement).toBe(screen.getByRole("button", { name: /linter/ }));
});

test("an already-installed catalog row expands its details in installed mode", async () => {
  const fake = connectSeededClient();
  fake.on("evener/marketplace/browse", () => ({
    name: ACME.name,
    plugins: [{ name: LINTER.plugin, description: "The linter" }],
  }));
  fake.on("evener/plugin/components", (params) => ({
    results: params.targets.map((t) => ({
      plugin: t.plugin,
      marketplace: t.marketplace,
      components: { skills: ["run"] },
    })),
  }));
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await screen.findByRole("button", { name: /linter/ });
  await user.click(screen.getByRole("radio", { name: /^Marketplaces/ }));
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  await screen.findByRole("list", { name: `${ACME.name} plugins` });

  // The catalog row reads as installed: no trailing Install control.
  expect(screen.queryByRole("button", { name: "Install" })).toBeNull();
  await user.click(screen.getByRole("button", { name: /linter/ }));
  const details = document.querySelector('[data-kind="plugin-detail"]');
  if (!(details instanceof HTMLElement)) throw new Error("no details expanded");
  // The installed entry's editor, not the catalog body: switches and the
  // Remove action, plus the Contents the components cache holds.
  expect(details.getAttribute("data-mode")).toBe("installed");
  expect(within(details).getByRole("switch", { name: "Enabled by default" })).toBeTruthy();
  expect(within(details).getByRole("button", { name: "Remove" })).toBeTruthy();
  expect(within(details).queryByRole("button", { name: "Install" })).toBeNull();
  await waitFor(() => expect(details.querySelector('[data-kind="plugin-contents"]')).toBeTruthy());
});

// --- review round: catalog membership never closes Installed details ---

test("a marketplace publication that drops the catalog entry leaves an installed plugin's details open", async () => {
  const fake = connectSeededClient();
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await user.click(await screen.findByRole("button", { name: /linter/ }));
  expect(document.querySelector('[data-kind="plugin-detail"]')).not.toBeNull();

  // The catalog stops listing the marketplace, but the plugin stays installed.
  fake.on("evener/marketplace/list", () => ({ marketplaces: [] }));
  await act(async () => {
    await extensionsStore.getState().fetchMarketplaces();
  });

  expect(document.querySelector('[data-kind="plugin-detail"]')).not.toBeNull();
});
