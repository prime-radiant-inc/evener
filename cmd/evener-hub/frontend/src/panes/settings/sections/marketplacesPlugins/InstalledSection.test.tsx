import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { PluginEntry } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { extensionsStore, resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
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

const FORMATTER: PluginEntry = {
  ...LINTER,
  plugin: "formatter",
  marketplace: "other-market",
  version: "0.3.1",
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

test("renders a row per plugin with name, marketplace, and version", () => {
  connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  render(<InstalledSection onSelect={() => {}} />);
  expect(screen.getByText("linter")).toBeTruthy();
  expect(screen.getByText("@ acme-plugins · v1.2.0")).toBeTruthy();
});

test("shows v.unknown when version is empty", () => {
  connectFakeClient();
  extensionsStore.setState({ plugins: [{ ...LINTER, version: "" }] });
  render(<InstalledSection onSelect={() => {}} />);
  expect(screen.getByText("@ acme-plugins · vunknown")).toBeTruthy();
});

test("shows the empty state when no plugins are installed", () => {
  connectFakeClient();
  extensionsStore.setState({ plugins: [] });
  render(<InstalledSection onSelect={() => {}} />);
  expect(screen.getByText("No plugins installed yet. Install one from Browse.")).toBeTruthy();
});

test("shows broken/disabled/auto-upgrade badges only when applicable", () => {
  connectFakeClient();
  extensionsStore.setState({
    plugins: [{ ...LINTER, broken: true, enabled: false, autoUpgrade: true }],
  });
  render(<InstalledSection onSelect={() => {}} />);
  expect(screen.getByText("broken")).toBeTruthy();
  expect(screen.getByText("disabled")).toBeTruthy();
  expect(screen.getByText("auto-upgrade")).toBeTruthy();
});

test("no badges render for a healthy, enabled, non-auto-upgrading plugin", () => {
  connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  render(<InstalledSection onSelect={() => {}} />);
  expect(screen.queryByText("broken")).toBeNull();
  expect(screen.queryByText("disabled")).toBeNull();
  expect(screen.queryByText("auto-upgrade")).toBeNull();
});

test("the status dot reads broken > disabled > idle, in that priority order", () => {
  connectFakeClient();
  extensionsStore.setState({
    plugins: [
      { ...LINTER, plugin: "broken-one", broken: true, enabled: false },
      { ...LINTER, plugin: "disabled-one", broken: false, enabled: false },
      { ...LINTER, plugin: "healthy-one", broken: false, enabled: true },
    ],
  });
  render(<InstalledSection onSelect={() => {}} />);
  expect(screen.getAllByRole("img", { name: "Failed" })).toHaveLength(1);
  expect(screen.getAllByRole("img", { name: "Ended" })).toHaveLength(1);
  expect(screen.getAllByRole("img", { name: "Idle" })).toHaveLength(1);
});

test("clicking a row calls onSelect with the plugin and marketplace", async () => {
  const user = userEvent.setup();
  connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER, FORMATTER] });
  const onSelect = vi.fn();
  render(<InstalledSection onSelect={onSelect} />);
  await user.click(screen.getByRole("button", { name: /formatter/ }));
  expect(onSelect).toHaveBeenCalledWith({ plugin: "formatter", marketplace: "other-market" });
});

test("rows carry no per-row action buttons - actions live in the detail sheet", () => {
  connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  render(<InstalledSection onSelect={() => {}} />);
  expect(screen.queryByRole("button", { name: "Disable" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Enable" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Upgrade" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
  expect(screen.queryByRole("button", { name: /Auto-upgrade/ })).toBeNull();
});

test("the filter narrows rows by plugin name, case-insensitively", async () => {
  const user = userEvent.setup();
  connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER, FORMATTER] });
  render(<InstalledSection onSelect={() => {}} />);
  await user.type(screen.getByRole("textbox", { name: "Filter installed" }), "FORM");
  expect(screen.queryByText("linter")).toBeNull();
  expect(screen.getByText("formatter")).toBeTruthy();
});

test("the filter also matches the marketplace name", async () => {
  const user = userEvent.setup();
  connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER, FORMATTER] });
  render(<InstalledSection onSelect={() => {}} />);
  await user.type(screen.getByRole("textbox", { name: "Filter installed" }), "other-mark");
  expect(screen.queryByText("linter")).toBeNull();
  expect(screen.getByText("formatter")).toBeTruthy();
});

test("a filter with no match says so, and clearing it restores the rows", async () => {
  const user = userEvent.setup();
  connectFakeClient();
  extensionsStore.setState({ plugins: [LINTER] });
  render(<InstalledSection onSelect={() => {}} />);
  const filter = screen.getByRole("textbox", { name: "Filter installed" });
  await user.type(filter, "zzz");
  expect(screen.getByText('No plugins match "zzz".')).toBeTruthy();
  expect(screen.queryByText("linter")).toBeNull();
  await user.clear(filter);
  expect(screen.getByText("linter")).toBeTruthy();
});
