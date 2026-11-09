import type { MarketplaceEntry, PluginComponents, PluginEntry } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../../../stores/connection";
import { extensionsStore, resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { connectReadyFakeClient } from "../../../../testing/connectionTestUtils";
import { getToasts, resetToastStoreForTests } from "../../../../widgets/toast/store";
import { chipKinds } from "./marketplacesTestUtils";
import { PluginDetailSheet, type PluginDetailTarget } from "./PluginDetailSheet";

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

const TARGET: PluginDetailTarget = { mode: "installed", plugin: "linter", marketplace: "acme-plugins" };

/** The pair form the sheet's RPCs address a plugin by - the target without
 * its mode, which is a UI-only discriminator. */
const PAIR = { plugin: "linter", marketplace: "acme-plugins" };

const COMPONENTS: PluginComponents = {
  skills: ["run"],
  commands: ["fmt"],
  mcpServers: ["acme-mcp"],
};

/** A browse cache already holding the marketplace's catalog, so a test never
 * waits on (or counts) the lazy browse. */
function seededCatalog(plugins: { name: string; description?: string; author?: string }[]) {
  return new Map([["acme-plugins", { status: "loaded" as const, plugins }]]);
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

test("renders nothing when target is null", () => {
  connectReadyFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  render(<PluginDetailSheet target={null} onClose={() => {}} />);
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("renders name, state chips, version, marketplace, and source", async () => {
  connectReadyFakeClient();
  extensionsStore.setState({
    plugins: [{ ...LINTER, broken: true, enabled: false, autoUpgrade: true }],
    marketplaces: [ACME],
  });
  const browseMarketplace = vi.spyOn(extensionsStore.getState(), "browseMarketplace");
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  expect(screen.getByRole("dialog", { name: "linter" })).toBeTruthy();
  expect(chipKinds()).toEqual(["broken", "disabled", "auto-upgrade"]);
  expect(screen.getByText("v1.2.0")).toBeTruthy();
  expect(screen.getByText("acme-plugins")).toBeTruthy();
  expect(screen.getByText("github: acme/plugins")).toBeTruthy();
  const browse = browseMarketplace.mock.results[0];
  if (browse?.type !== "return") throw new Error("Plugin detail sheet did not start its catalog browse");
  await act(() => browse.value);
});

test("omits the source row when the marketplace is not registered", async () => {
  connectReadyFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [] });
  const browseMarketplace = vi.spyOn(extensionsStore.getState(), "browseMarketplace");
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  expect(screen.getByRole("dialog", { name: "linter" })).toBeTruthy();
  expect(screen.queryByText("github: acme/plugins")).toBeNull();
  const browse = browseMarketplace.mock.results[0];
  if (browse?.type !== "return") throw new Error("Plugin detail sheet did not start its catalog browse");
  await act(() => browse.value);
});

test("the Enabled and Auto-upgrade switches reflect the entry's state", async () => {
  connectReadyFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  const browseMarketplace = vi.spyOn(extensionsStore.getState(), "browseMarketplace");
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  expect(screen.getByRole("switch", { name: "Enabled by default" }).getAttribute("aria-checked")).toBe("true");
  expect(screen.getByRole("switch", { name: "Auto-upgrade" }).getAttribute("aria-checked")).toBe("false");
  const browse = browseMarketplace.mock.results[0];
  if (browse?.type !== "return") throw new Error("Plugin detail sheet did not start its catalog browse");
  await act(() => browse.value);
});

test("the Enabled switch calls pluginDisable on an enabled plugin, pluginEnable on a disabled one", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  fake.on("evener/plugin/disable", (params) => {
    expect(params).toEqual(PAIR);
    return { plugins: [{ ...LINTER, enabled: false }] };
  });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  await user.click(screen.getByRole("switch", { name: "Enabled by default" }));
  await waitFor(() =>
    expect(screen.getByRole("switch", { name: "Enabled by default" }).getAttribute("aria-checked")).toBe("false"),
  );
  expect(getToasts()).toEqual([]);
});

test("the Enabled switch calls pluginEnable on a disabled plugin", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  extensionsStore.setState({ plugins: [{ ...LINTER, enabled: false }], marketplaces: [ACME] });
  fake.on("evener/plugin/enable", (params) => {
    expect(params).toEqual(PAIR);
    return { plugins: [{ ...LINTER, enabled: true }] };
  });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  await user.click(screen.getByRole("switch", { name: "Enabled by default" }));
  await waitFor(() =>
    expect(screen.getByRole("switch", { name: "Enabled by default" }).getAttribute("aria-checked")).toBe("true"),
  );
  expect(getToasts()).toEqual([]);
});

test("a failed enable toggle re-enables the switch too", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  fake.on("evener/plugin/disable", () => {
    throw new Error("boom");
  });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  const enabledSwitch = screen.getByRole("switch", { name: "Enabled by default" });
  await user.click(enabledSwitch);
  // The refusal is aria-disabled, never the native attribute: the click that
  // starts the toggle keeps the keyboard where it was (see widgets/switch).
  await waitFor(() => expect(enabledSwitch.getAttribute("aria-disabled")).not.toBe("true"));
});

test("a failed enable toggle toasts 'Toggle enable failed'", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  fake.on("evener/plugin/disable", () => {
    throw new Error("boom");
  });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  await user.click(screen.getByRole("switch", { name: "Enabled by default" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "error" && t.text === "Toggle enable failed: boom")).toBe(true),
  );
});

test("the Enabled switch is disabled while its RPC is in flight, and re-enables after", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  let resolveDisable: (v: { plugins: PluginEntry[] }) => void = () => {};
  fake.on(
    "evener/plugin/disable",
    () =>
      new Promise((resolve) => {
        resolveDisable = resolve;
      }),
  );
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  const enabledSwitch = screen.getByRole("switch", { name: "Enabled by default" });
  await user.click(enabledSwitch);
  expect(enabledSwitch.getAttribute("aria-disabled")).toBe("true");

  resolveDisable({ plugins: [{ ...LINTER, enabled: false }] });
  await waitFor(() =>
    expect(screen.getByRole("switch", { name: "Enabled by default" }).getAttribute("aria-disabled")).not.toBe("true"),
  );
});

test("the Auto-upgrade switch flips the current value; failure toasts", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  fake.on("evener/plugin/setAutoUpgrade", (params) => {
    expect(params).toEqual({ ...PAIR, autoUpgrade: true });
    return { plugins: [{ ...LINTER, autoUpgrade: true }] };
  });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  await user.click(screen.getByRole("switch", { name: "Auto-upgrade" }));
  await waitFor(() =>
    expect(screen.getByRole("switch", { name: "Auto-upgrade" }).getAttribute("aria-checked")).toBe("true"),
  );
});

test("a failed auto-upgrade toggle toasts 'Toggle auto-upgrade failed'", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  fake.on("evener/plugin/setAutoUpgrade", () => {
    throw new Error("boom");
  });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  await user.click(screen.getByRole("switch", { name: "Auto-upgrade" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "error" && t.text === "Toggle auto-upgrade failed: boom")).toBe(true),
  );
});

test("Upgrade and its badge show only when the hub found an update", async () => {
  connectReadyFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  const browseMarketplace = vi.spyOn(extensionsStore.getState(), "browseMarketplace");
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  expect(screen.queryByRole("button", { name: "Upgrade" })).toBeNull();
  expect(chipKinds()).not.toContain("update-available");

  act(() => extensionsStore.setState({ plugins: [{ ...LINTER, updateAvailable: true }] }));
  expect(screen.getByRole("button", { name: "Upgrade" })).toBeTruthy();
  expect(chipKinds()).toContain("update-available");
  const browse = browseMarketplace.mock.results[0];
  if (browse?.type !== "return") throw new Error("Plugin detail sheet did not start its catalog browse");
  await act(() => browse.value);
});

test("Upgrade calls pluginUpgrade, toasts an upgraded success, and is busy in flight", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  extensionsStore.setState({ plugins: [{ ...LINTER, updateAvailable: true }], marketplaces: [ACME] });
  let resolveUpgrade: (v: { plugins: PluginEntry[] }) => void = () => {};
  fake.on("evener/plugin/upgrade", (params) => {
    expect(params).toEqual(PAIR);
    return new Promise((resolve) => {
      resolveUpgrade = resolve;
    });
  });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  const upgradeButton = screen.getByRole("button", { name: "Upgrade" });
  await user.click(upgradeButton);
  // Its own click made it unavailable: a refusal that keeps the keyboard, not
  // the native attribute (which would drop focus to <body>).
  expect(upgradeButton.getAttribute("aria-disabled")).toBe("true");
  expect(document.activeElement).toBe(upgradeButton);

  resolveUpgrade({ plugins: [LINTER] });
  await waitFor(() => expect(getToasts().some((t) => t.kind === "success" && t.text === "Upgraded linter")).toBe(true));
  // The upgrade answered with the plugin current, so Upgrade is gone; the
  // keyboard stays in the sheet rather than falling to <body>.
  await waitFor(() => expect(screen.queryByRole("button", { name: "Upgrade" })).toBeNull());
  // The sheet's first control, the same one it focuses when it opens.
  await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("switch", { name: "Enabled by default" })));
});

test("an upgrade leaves alone focus the user moved while it ran", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  extensionsStore.setState({ plugins: [{ ...LINTER, updateAvailable: true }], marketplaces: [ACME] });
  let resolveUpgrade: (v: { plugins: PluginEntry[] }) => void = () => {};
  fake.on(
    "evener/plugin/upgrade",
    () =>
      new Promise((resolve) => {
        resolveUpgrade = resolve;
      }),
  );
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  await user.click(screen.getByRole("button", { name: "Upgrade" }));
  const autoUpgrade = screen.getByRole("switch", { name: "Auto-upgrade" });
  act(() => autoUpgrade.focus());

  resolveUpgrade({ plugins: [LINTER] });
  await waitFor(() => expect(screen.queryByRole("button", { name: "Upgrade" })).toBeNull());
  expect(document.activeElement).toBe(autoUpgrade);
});

test("a failed upgrade toasts failure", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  extensionsStore.setState({ plugins: [{ ...LINTER, updateAvailable: true }], marketplaces: [ACME] });
  fake.on("evener/plugin/upgrade", () => {
    throw new Error("boom");
  });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  await user.click(screen.getByRole("button", { name: "Upgrade" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "error" && t.text === "Upgrade failed: boom")).toBe(true),
  );
});

test("Remove opens a confirm; confirming removes, toasts, and closes the sheet", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  fake.on("evener/plugin/remove", (params) => {
    expect(params).toEqual(PAIR);
    return { plugins: [] };
  });
  const onClose = vi.fn();
  render(<PluginDetailSheet target={TARGET} onClose={onClose} />);
  await user.click(screen.getByRole("button", { name: "Remove" }));
  const dialog = screen.getByRole("dialog", { name: "Remove plugin" });
  expect(within(dialog).getByText('Remove plugin "linter"?')).toBeTruthy();
  await user.click(within(dialog).getByRole("button", { name: "Remove" }));
  await waitFor(() => expect(getToasts().some((t) => t.kind === "success" && t.text === "Removed linter")).toBe(true));
  await waitFor(() => expect(onClose).toHaveBeenCalled());
});

test("cancelling the remove confirm does not call pluginRemove and keeps the sheet open", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  const removeSpy = vi.fn();
  fake.on("evener/plugin/remove", removeSpy);
  const onClose = vi.fn();
  render(<PluginDetailSheet target={TARGET} onClose={onClose} />);
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  expect(removeSpy).not.toHaveBeenCalled();
  expect(onClose).not.toHaveBeenCalled();
  expect(screen.getByRole("dialog", { name: "linter" })).toBeTruthy();
});

test("a failed remove toasts failure and keeps the sheet open", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  fake.on("evener/plugin/remove", () => {
    throw new Error("boom");
  });
  const onClose = vi.fn();
  render(<PluginDetailSheet target={TARGET} onClose={onClose} />);
  await user.click(screen.getByRole("button", { name: "Remove" }));
  const dialog = screen.getByRole("dialog", { name: "Remove plugin" });
  await user.click(within(dialog).getByRole("button", { name: "Remove" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "error" && t.text === "Remove failed: boom")).toBe(true),
  );
  expect(onClose).not.toHaveBeenCalled();
  expect(screen.getByRole("dialog", { name: "linter" })).toBeTruthy();
});

test("the remove confirm's buttons disable while the removal is in flight", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  fake.on("evener/plugin/remove", () => new Promise(() => {})); // never resolves - just observe the mid-flight state
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  await user.click(screen.getByRole("button", { name: "Remove" }));
  const dialog = screen.getByRole("dialog", { name: "Remove plugin" });
  await user.click(within(dialog).getByRole("button", { name: "Remove" }));
  expect((within(dialog).getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true);
  expect((within(dialog).getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(true);
});

test("the close button calls onClose", async () => {
  const user = userEvent.setup();
  connectReadyFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  const onClose = vi.fn();
  render(<PluginDetailSheet target={TARGET} onClose={onClose} />);
  await user.click(screen.getByRole("button", { name: "Close" }));
  expect(onClose).toHaveBeenCalled();
});

test("lazily browses the marketplace and shows the catalog description when loaded", async () => {
  const fake = connectReadyFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  fake.on("evener/marketplace/browse", (params) => {
    expect(params).toEqual({ name: "acme-plugins" });
    return {
      name: "acme-plugins",
      plugins: [{ name: "linter", description: "Checks code style on every save." }],
    };
  });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  expect(fake.calls.some((c) => c.method === "evener/marketplace/browse")).toBe(true);
  expect(await screen.findByText("Checks code style on every save.")).toBeTruthy();
});

test("does not re-browse a marketplace whose catalog is already cached", async () => {
  const fake = connectReadyFakeClient();
  extensionsStore.setState({
    plugins: [LINTER],
    marketplaces: [ACME],
    browseCatalogs: new Map([
      ["acme-plugins", { status: "loaded" as const, plugins: [{ name: "linter", description: "Checks code style." }] }],
    ]),
  });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  expect(fake.calls.some((c) => c.method === "evener/marketplace/browse")).toBe(false);
  expect(screen.getByText("Checks code style.")).toBeTruthy();
  // The sheet's lazy components read settles inside the test, not after it.
  await act(async () => {});
});

test("closes itself when the entry disappears from the store (removed elsewhere)", async () => {
  connectReadyFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  const onClose = vi.fn();
  const browseMarketplace = vi.spyOn(extensionsStore.getState(), "browseMarketplace");
  render(<PluginDetailSheet target={TARGET} onClose={onClose} />);
  expect(screen.getByRole("dialog", { name: "linter" })).toBeTruthy();
  const browse = browseMarketplace.mock.results[0];
  if (browse?.type !== "return") throw new Error("Plugin detail sheet did not start its catalog browse");
  act(() => extensionsStore.setState({ plugins: [] }));
  await act(() => browse.value);
  await waitFor(() => expect(onClose).toHaveBeenCalled());
});

test("installed mode with a loaded cache entry renders the Contents block and today's switches and footer", async () => {
  const fake = connectReadyFakeClient();
  extensionsStore.setState({
    plugins: [LINTER],
    marketplaces: [ACME],
    browseCatalogs: seededCatalog([{ name: "linter", description: "Checks code style." }]),
  });
  // The sheet's own open-fetch fills the cache entry (the lazy read the
  // brief pins) from the hub's answer.
  fake.on("evener/plugin/components", () => ({
    results: [{ plugin: "linter", marketplace: "acme-plugins", components: COMPONENTS }],
  }));
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  const sheet = screen.getByRole("dialog", { name: "linter" });
  expect(await within(sheet).findByRole("heading", { name: "Contents" })).toBeTruthy();
  // One row per present kind, each with its badge count and mono names.
  const skills = within(sheet).getByText("Skills");
  expect(skills.closest('[data-kind="skills"]')?.textContent).toContain("run");
  expect(within(sheet).queryByText("Agents")).toBeNull();
  expect(within(sheet).getByText("acme-mcp")).toBeTruthy();
  // The manifest-only reading is reserved for an all-absent inventory.
  expect(document.querySelector('[data-reason="manifest-only"]')).toBeNull();
  // Today's editor surface is unchanged: both switches, Remove, no Upgrade.
  expect(within(sheet).getByRole("switch", { name: "Enabled by default" })).toBeTruthy();
  expect(within(sheet).getByRole("switch", { name: "Auto-upgrade" })).toBeTruthy();
  expect(within(sheet).getByRole("button", { name: "Remove" })).toBeTruthy();
  expect(within(sheet).queryByRole("button", { name: "Upgrade" })).toBeNull();
});

test("a broken plugin's error entry shows the load failure where Contents would be and keeps the editor actions", async () => {
  const fake = connectReadyFakeClient();
  extensionsStore.setState({
    plugins: [{ ...LINTER, broken: true }],
    marketplaces: [ACME],
  });
  // The hub's fail-soft answer: the broken target's row carries its load
  // error, which is what the sheet shows where Contents would be.
  fake.on("evener/plugin/components", () => ({
    results: [{ plugin: "linter", marketplace: "acme-plugins", error: "linter: manifest is not valid" }],
  }));
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  const sheet = screen.getByRole("dialog", { name: "linter" });
  const failure = await within(sheet).findByRole("alert");
  expect(failure.getAttribute("data-reason")).toBe("plugin-broken");
  expect(failure.textContent).toContain("linter: manifest is not valid");
  // The failure is the plugin's, not a degraded read: no retry, no
  // manifest-only fallback, no Contents block.
  expect(within(sheet).queryByRole("status")).toBeNull();
  expect(within(sheet).queryByRole("button", { name: "Retry" })).toBeNull();
  expect(within(sheet).queryByRole("heading", { name: "Contents" })).toBeNull();
  expect(document.querySelector('[data-reason="manifest-only"]')).toBeNull();
  // Chips, switches and Remove are unchanged.
  expect(chipKinds()).toEqual(["broken"]);
  expect(within(sheet).getByRole("switch", { name: "Enabled by default" })).toBeTruthy();
  expect(within(sheet).getByRole("button", { name: "Remove" })).toBeTruthy();
});

test("an older hub's method error degrades with a working Retry that re-reads the contents", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  extensionsStore.setState({
    plugins: [LINTER],
    marketplaces: [ACME],
    browseCatalogs: seededCatalog([{ name: "linter", description: "Checks code style." }]),
  });
  fake.on("evener/plugin/components", () => {
    throw new WireError("method not found: evener/plugin/components", -32601);
  });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  const sheet = screen.getByRole("dialog", { name: "linter" });
  // The read failed for the whole method, not for this plugin: a quiet
  // degrade notice where Contents would be, never the load-failure alert.
  const notice = await within(sheet).findByRole("status");
  expect(notice.getAttribute("data-reason")).toBe("components-degraded");
  expect(within(sheet).queryByRole("alert")).toBeNull();
  expect(within(sheet).queryByRole("heading", { name: "Contents" })).toBeNull();

  // The hub is upgraded: the retry's fresh read now answers.
  fake.on("evener/plugin/components", (params) => ({
    results: params.targets.map((t) => ({
      plugin: t.plugin,
      marketplace: t.marketplace,
      components: { skills: ["run"] },
    })),
  }));
  await user.click(within(sheet).getByRole("button", { name: "Retry" }));
  expect(fake.calls.filter((c) => c.method === "evener/plugin/components")).toHaveLength(2);
  expect(fake.calls.at(-1)?.params).toEqual({ targets: [{ plugin: "linter", marketplace: "acme-plugins" }] });
  expect(await within(sheet).findByRole("heading", { name: "Contents" })).toBeTruthy();
  expect(within(sheet).queryByRole("status")).toBeNull();
});

test("catalog mode renders description and metadata with no switches, and installs through the confirm flow", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  extensionsStore.setState({
    plugins: [LINTER],
    marketplaces: [ACME],
    browseCatalogs: seededCatalog([{ name: "formatter", description: "Formats code.", author: "Acme Corp" }]),
  });
  fake.on("evener/plugin/install", () => ({ plugins: [LINTER, { ...LINTER, plugin: "formatter" }] }));
  const onClose = vi.fn();
  render(
    <PluginDetailSheet
      target={{ mode: "catalog", plugin: "formatter", marketplace: "acme-plugins" }}
      onClose={onClose}
    />,
  );
  const sheet = screen.getByRole("dialog", { name: "formatter" });
  expect(within(sheet).getByText("Formats code.")).toBeTruthy();
  // No switches, no Contents block, and no components read in catalog mode.
  expect(within(sheet).queryByRole("switch")).toBeNull();
  expect(within(sheet).queryByRole("heading", { name: "Contents" })).toBeNull();
  expect(fake.calls.some((c) => c.method === "evener/plugin/components")).toBe(false);
  expect(chipKinds()).toEqual([]);
  // Catalog metadata: Marketplace, Source, Author when present; no Version.
  expect(within(sheet).getByText("acme-plugins")).toBeTruthy();
  expect(within(sheet).getByText("github: acme/plugins")).toBeTruthy();
  expect(within(sheet).getByText("Acme Corp")).toBeTruthy();
  expect(within(sheet).queryByText("Version")).toBeNull();

  await user.click(within(sheet).getByRole("button", { name: "Install" }));
  const confirm = screen.getByRole("dialog", { name: "Install plugin" });
  await user.click(within(confirm).getByRole("button", { name: "Install" }));
  expect(fake.calls.filter((c) => c.method === "evener/plugin/install").map((c) => c.params)).toEqual([
    { plugin: "formatter", marketplace: "acme-plugins" },
  ]);
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "success" && t.text === "Installed formatter")).toBe(true),
  );
  await waitFor(() => expect(onClose).toHaveBeenCalled());
  expect(screen.queryByRole("dialog", { name: "Install plugin" })).toBeNull();
});
