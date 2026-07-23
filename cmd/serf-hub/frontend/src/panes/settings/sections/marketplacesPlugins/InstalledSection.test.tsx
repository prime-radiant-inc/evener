import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { PluginEntry } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { extensionsStore, resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { getToasts, resetToastStoreForTests } from "../../../../widgets/toast/store";
import { InstalledSection } from "./InstalledSection";

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

test("renders the heading, count, name, marketplace, and version", () => {
  connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  render(<InstalledSection />);
  expect(screen.getByText("Installed")).toBeTruthy();
  expect(screen.getByText("1 entry")).toBeTruthy();
  expect(screen.getByText("linter")).toBeTruthy();
  expect(screen.getByText("@ acme-plugins")).toBeTruthy();
  expect(screen.getByText("v1.2.0")).toBeTruthy();
});

test("shows v.unknown when version is empty", () => {
  connectFakeClient();
  extensionsStore.setState({ plugins: [{ ...LINTER, version: "" }] });
  render(<InstalledSection />);
  expect(screen.getByText("vunknown")).toBeTruthy();
});

test("shows the empty state and pluralizes the count", () => {
  connectFakeClient();
  extensionsStore.setState({ plugins: [] });
  render(<InstalledSection />);
  expect(screen.getByText("No plugins installed yet. Install one from Browse above.")).toBeTruthy();
  expect(screen.getByText("0 entries")).toBeTruthy();
});

test("shows broken/disabled/auto-upgrade badges only when applicable", () => {
  connectFakeClient();
  extensionsStore.setState({
    plugins: [{ ...LINTER, broken: true, enabled: false, autoUpgrade: true }],
  });
  render(<InstalledSection />);
  expect(screen.getByText("broken")).toBeTruthy();
  expect(screen.getByText("disabled")).toBeTruthy();
  expect(screen.getByText("auto-upgrade")).toBeTruthy();
});

test("no badges render for a healthy, enabled, non-auto-upgrading plugin", () => {
  connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  render(<InstalledSection />);
  expect(screen.queryByText("broken")).toBeNull();
  expect(screen.queryByText("disabled")).toBeNull();
  expect(screen.queryByText("auto-upgrade")).toBeNull();
});

test("Disable calls pluginDisable (enabled plugin); no success toast", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  fake.on("serf/plugin/disable", (params) => {
    expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins" });
    return { plugins: [{ ...LINTER, enabled: false }] };
  });
  render(<InstalledSection />);
  await user.click(screen.getByRole("button", { name: "Disable" }));
  expect(await screen.findByRole("button", { name: "Enable" })).toBeTruthy();
  expect(getToasts()).toEqual([]);
});

test("Enable calls pluginEnable (disabled plugin)", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [{ ...LINTER, enabled: false }] });
  fake.on("serf/plugin/enable", (params) => {
    expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins" });
    return { plugins: [{ ...LINTER, enabled: true }] };
  });
  render(<InstalledSection />);
  await user.click(screen.getByRole("button", { name: "Enable" }));
  expect(await screen.findByRole("button", { name: "Disable" })).toBeTruthy();
});

test("a failed enable/disable toggle toasts 'Toggle enable failed'", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  fake.on("serf/plugin/disable", () => {
    throw new Error("boom");
  });
  render(<InstalledSection />);
  await user.click(screen.getByRole("button", { name: "Disable" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "error" && t.text === "Toggle enable failed: boom")).toBe(true),
  );
});

test("the auto-upgrade toggle shows on/off and flips the current value; no success toast", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  fake.on("serf/plugin/setAutoUpgrade", (params) => {
    expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins", autoUpgrade: true });
    return { plugins: [{ ...LINTER, autoUpgrade: true }] };
  });
  render(<InstalledSection />);
  expect(screen.getByRole("button", { name: "Auto-upgrade: off" })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Auto-upgrade: off" }));
  expect(await screen.findByRole("button", { name: "Auto-upgrade: on" })).toBeTruthy();
  expect(getToasts()).toEqual([]);
});

test("a failed auto-upgrade toggle toasts 'Toggle auto-upgrade failed'", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  fake.on("serf/plugin/setAutoUpgrade", () => {
    throw new Error("boom");
  });
  render(<InstalledSection />);
  await user.click(screen.getByRole("button", { name: "Auto-upgrade: off" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "error" && t.text === "Toggle auto-upgrade failed: boom")).toBe(true),
  );
});

test("Upgrade calls pluginUpgrade and toasts a checked-for-upgrades success", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  fake.on("serf/plugin/upgrade", (params) => {
    expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins" });
    return { plugins: [{ ...LINTER, version: "1.3.0" }] };
  });
  render(<InstalledSection />);
  await user.click(screen.getByRole("button", { name: "Upgrade" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "success" && t.text === "Checked linter for upgrades")).toBe(true),
  );
});

test("a failed upgrade toasts failure", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  fake.on("serf/plugin/upgrade", () => {
    throw new Error("boom");
  });
  render(<InstalledSection />);
  await user.click(screen.getByRole("button", { name: "Upgrade" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "error" && t.text === "Upgrade failed: boom")).toBe(true),
  );
});

test("Disable disables its own button while the RPC is in flight, and re-enables after", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  let resolveDisable: (v: { plugins: PluginEntry[] }) => void = () => {};
  fake.on(
    "serf/plugin/disable",
    () =>
      new Promise((resolve) => {
        resolveDisable = resolve;
      }),
  );
  render(<InstalledSection />);
  const disableButton = screen.getByRole("button", { name: "Disable" });
  await user.click(disableButton);
  expect((disableButton as HTMLButtonElement).disabled).toBe(true);

  resolveDisable({ plugins: [{ ...LINTER, enabled: false }] });
  await waitFor(() => expect(screen.getByRole("button", { name: "Enable" })).toBeTruthy());
});

test("a failed enable/disable toggle re-enables the button too", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  fake.on("serf/plugin/disable", () => {
    throw new Error("boom");
  });
  render(<InstalledSection />);
  const disableButton = screen.getByRole("button", { name: "Disable" });
  await user.click(disableButton);
  await waitFor(() => expect((disableButton as HTMLButtonElement).disabled).toBe(false));
});

test("the auto-upgrade toggle disables its own button while in flight, without disabling this row's other actions", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  fake.on("serf/plugin/setAutoUpgrade", () => new Promise(() => {})); // never resolves - observe mid-flight only
  render(<InstalledSection />);
  const autoUpgradeButton = screen.getByRole("button", { name: "Auto-upgrade: off" });
  await user.click(autoUpgradeButton);
  expect((autoUpgradeButton as HTMLButtonElement).disabled).toBe(true);
  expect((screen.getByRole("button", { name: "Disable" }) as HTMLButtonElement).disabled).toBe(false);
  expect((screen.getByRole("button", { name: "Upgrade" }) as HTMLButtonElement).disabled).toBe(false);
});

test("Upgrade disables its own button while in flight, and re-enables after", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  let resolveUpgrade: (v: { plugins: PluginEntry[] }) => void = () => {};
  fake.on(
    "serf/plugin/upgrade",
    () =>
      new Promise((resolve) => {
        resolveUpgrade = resolve;
      }),
  );
  render(<InstalledSection />);
  const upgradeButton = screen.getByRole("button", { name: "Upgrade" });
  await user.click(upgradeButton);
  expect((upgradeButton as HTMLButtonElement).disabled).toBe(true);

  resolveUpgrade({ plugins: [LINTER] });
  await waitFor(() => expect((upgradeButton as HTMLButtonElement).disabled).toBe(false));
});

test("a busy row action does not disable the same action on a different row", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  const OTHER: PluginEntry = { ...LINTER, plugin: "formatter" };
  extensionsStore.setState({ plugins: [LINTER, OTHER] });
  fake.on("serf/plugin/upgrade", () => new Promise(() => {})); // never resolves - observe mid-flight only
  render(<InstalledSection />);
  const upgradeButtons = screen.getAllByRole("button", { name: "Upgrade" });
  await user.click(upgradeButtons[0]!);
  expect((upgradeButtons[0] as HTMLButtonElement).disabled).toBe(true);
  expect((upgradeButtons[1] as HTMLButtonElement).disabled).toBe(false);
});

test("Remove opens a destructive confirm; confirming removes and toasts success", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  fake.on("serf/plugin/remove", (params) => {
    expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins" });
    return { plugins: [] };
  });
  render(<InstalledSection />);
  await user.click(screen.getByRole("button", { name: "Remove" }));
  const dialog = screen.getByRole("dialog", { name: "Remove plugin" });
  expect(within(dialog).getByText('Remove plugin "linter"?')).toBeTruthy();
  await user.click(within(dialog).getByRole("button", { name: "Remove" }));
  await waitFor(() => expect(getToasts().some((t) => t.kind === "success" && t.text === "Removed linter")).toBe(true));
});

test("cancelling the remove confirm does not call pluginRemove", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  const removeSpy = vi.fn();
  fake.on("serf/plugin/remove", removeSpy);
  render(<InstalledSection />);
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  expect(removeSpy).not.toHaveBeenCalled();
});

test("a failed remove toasts failure", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  fake.on("serf/plugin/remove", () => {
    throw new Error("boom");
  });
  render(<InstalledSection />);
  await user.click(screen.getByRole("button", { name: "Remove" }));
  const dialog = screen.getByRole("dialog", { name: "Remove plugin" });
  await user.click(within(dialog).getByRole("button", { name: "Remove" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "error" && t.text === "Remove failed: boom")).toBe(true),
  );
});

test("the remove confirm's buttons disable while the removal is in flight", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  fake.on("serf/plugin/remove", () => new Promise(() => {})); // never resolves - just observe the mid-flight state
  render(<InstalledSection />);
  await user.click(screen.getByRole("button", { name: "Remove" }));
  const dialog = screen.getByRole("dialog", { name: "Remove plugin" });
  await user.click(within(dialog).getByRole("button", { name: "Remove" }));
  expect((within(dialog).getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true);
  expect((within(dialog).getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(true);
});
