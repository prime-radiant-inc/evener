import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { MarketplaceEntry, PluginEntry } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
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
