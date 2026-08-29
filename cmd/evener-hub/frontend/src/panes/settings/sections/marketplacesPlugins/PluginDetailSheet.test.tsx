import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { MarketplaceEntry, PluginEntry } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { extensionsStore, resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { getToasts, resetToastStoreForTests } from "../../../../widgets/toast/store";
import { PluginDetailSheet } from "./PluginDetailSheet";

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

const TARGET = { plugin: "linter", marketplace: "acme-plugins" };

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
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
});

test("renders nothing when target is null", () => {
  connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  render(<PluginDetailSheet target={null} onClose={() => {}} />);
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("renders name, state chips, version, marketplace, and source", () => {
  connectFakeClient();
  extensionsStore.setState({
    plugins: [{ ...LINTER, broken: true, enabled: false, autoUpgrade: true }],
    marketplaces: [ACME],
  });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  expect(screen.getByRole("dialog", { name: "linter" })).toBeTruthy();
  expect(screen.getByText("broken")).toBeTruthy();
  expect(screen.getByText("disabled")).toBeTruthy();
  expect(screen.getByText("auto-upgrade")).toBeTruthy();
  expect(screen.getByText("v1.2.0")).toBeTruthy();
  expect(screen.getByText("acme-plugins")).toBeTruthy();
  expect(screen.getByText("github: acme/plugins")).toBeTruthy();
});

test("omits the source row when the marketplace is not registered", () => {
  connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [] });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  expect(screen.getByRole("dialog", { name: "linter" })).toBeTruthy();
  expect(screen.queryByText("github: acme/plugins")).toBeNull();
});

test("the Enabled and Auto-upgrade switches reflect the entry's state", () => {
  connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  expect(screen.getByRole("switch", { name: "Enabled" }).getAttribute("aria-checked")).toBe("true");
  expect(screen.getByRole("switch", { name: "Auto-upgrade" }).getAttribute("aria-checked")).toBe("false");
});

test("the Enabled switch calls pluginDisable on an enabled plugin, pluginEnable on a disabled one", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  fake.on("evener/plugin/disable", (params) => {
    expect(params).toEqual(TARGET);
    return { plugins: [{ ...LINTER, enabled: false }] };
  });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  await user.click(screen.getByRole("switch", { name: "Enabled" }));
  await waitFor(() =>
    expect(screen.getByRole("switch", { name: "Enabled" }).getAttribute("aria-checked")).toBe("false"),
  );
  expect(getToasts()).toEqual([]);
});

test("the Enabled switch calls pluginEnable on a disabled plugin", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [{ ...LINTER, enabled: false }], marketplaces: [ACME] });
  fake.on("evener/plugin/enable", (params) => {
    expect(params).toEqual(TARGET);
    return { plugins: [{ ...LINTER, enabled: true }] };
  });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  await user.click(screen.getByRole("switch", { name: "Enabled" }));
  await waitFor(() =>
    expect(screen.getByRole("switch", { name: "Enabled" }).getAttribute("aria-checked")).toBe("true"),
  );
  expect(getToasts()).toEqual([]);
});

test("a failed enable toggle re-enables the switch too", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  fake.on("evener/plugin/disable", () => {
    throw new Error("boom");
  });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  const enabledSwitch = screen.getByRole("switch", { name: "Enabled" });
  await user.click(enabledSwitch);
  await waitFor(() => expect((enabledSwitch as HTMLButtonElement).disabled).toBe(false));
});

test("a failed enable toggle toasts 'Toggle enable failed'", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  fake.on("evener/plugin/disable", () => {
    throw new Error("boom");
  });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  await user.click(screen.getByRole("switch", { name: "Enabled" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "error" && t.text === "Toggle enable failed: boom")).toBe(true),
  );
});

test("the Enabled switch is disabled while its RPC is in flight, and re-enables after", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
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
  const enabledSwitch = screen.getByRole("switch", { name: "Enabled" });
  await user.click(enabledSwitch);
  expect((enabledSwitch as HTMLButtonElement).disabled).toBe(true);

  resolveDisable({ plugins: [{ ...LINTER, enabled: false }] });
  await waitFor(() =>
    expect((screen.getByRole("switch", { name: "Enabled" }) as HTMLButtonElement).disabled).toBe(false),
  );
});

test("the Auto-upgrade switch flips the current value; failure toasts", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  fake.on("evener/plugin/setAutoUpgrade", (params) => {
    expect(params).toEqual({ ...TARGET, autoUpgrade: true });
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
  const fake = connectFakeClient();
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

test("Upgrade calls pluginUpgrade, toasts a checked-for-upgrades success, and is busy in flight", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  let resolveUpgrade: (v: { plugins: PluginEntry[] }) => void = () => {};
  fake.on("evener/plugin/upgrade", (params) => {
    expect(params).toEqual(TARGET);
    return new Promise((resolve) => {
      resolveUpgrade = resolve;
    });
  });
  render(<PluginDetailSheet target={TARGET} onClose={() => {}} />);
  const upgradeButton = screen.getByRole("button", { name: "Upgrade" });
  await user.click(upgradeButton);
  expect((upgradeButton as HTMLButtonElement).disabled).toBe(true);

  resolveUpgrade({ plugins: [LINTER] });
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "success" && t.text === "Checked linter for upgrades")).toBe(true),
  );
});

test("a failed upgrade toasts failure", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
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
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  fake.on("evener/plugin/remove", (params) => {
    expect(params).toEqual(TARGET);
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
  const fake = connectFakeClient();
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
  const fake = connectFakeClient();
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
  const fake = connectFakeClient();
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
  connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  const onClose = vi.fn();
  render(<PluginDetailSheet target={TARGET} onClose={onClose} />);
  await user.click(screen.getByRole("button", { name: "Close" }));
  expect(onClose).toHaveBeenCalled();
});

test("lazily browses the marketplace and shows the catalog description when loaded", async () => {
  const fake = connectFakeClient();
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

test("does not re-browse a marketplace whose catalog is already cached", () => {
  const fake = connectFakeClient();
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
});

test("closes itself when the entry disappears from the store (removed elsewhere)", async () => {
  connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER], marketplaces: [ACME] });
  const onClose = vi.fn();
  render(<PluginDetailSheet target={TARGET} onClose={onClose} />);
  expect(screen.getByRole("dialog", { name: "linter" })).toBeTruthy();
  extensionsStore.setState({ plugins: [] });
  await waitFor(() => expect(onClose).toHaveBeenCalled());
});
