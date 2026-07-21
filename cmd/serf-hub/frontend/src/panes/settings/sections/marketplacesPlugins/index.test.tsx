import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import { connectionStore } from "../../../../stores/connection";
import { resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
import { MarketplacesPluginsSection } from "./index";

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

test("shows a loading state before both the marketplace and plugin lists resolve", () => {
  const fake = connectFakeClient();
  fake.on("serf/marketplace/list", () => new Promise(() => {}));
  fake.on("serf/plugin/list", () => new Promise(() => {}));
  render(<MarketplacesPluginsSection />);
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
});

test("fetches both marketplaces and plugins in parallel on mount", async () => {
  const fake = connectFakeClient();
  fake.on("serf/marketplace/list", () => ({ marketplaces: [] }));
  fake.on("serf/plugin/list", () => ({ plugins: [] }));
  render(<MarketplacesPluginsSection />);
  expect(await screen.findByText("Marketplaces")).toBeTruthy();
  expect(screen.getByText("Browse")).toBeTruthy();
  expect(screen.getByText("Installed")).toBeTruthy();
  expect(fake.calls.some((c) => c.method === "serf/marketplace/list")).toBe(true);
  expect(fake.calls.some((c) => c.method === "serf/plugin/list")).toBe(true);
});

test("shows one failed-to-load message replacing everything when the marketplace list fails to load", async () => {
  const fake = connectFakeClient();
  fake.on("serf/marketplace/list", () => {
    throw new Error("network down");
  });
  fake.on("serf/plugin/list", () => ({ plugins: [] }));
  render(<MarketplacesPluginsSection />);
  expect(await screen.findByText(/Failed to load: network down/)).toBeTruthy();
  expect(screen.queryByText("Marketplaces")).toBeNull();
  expect(screen.queryByText("Browse")).toBeNull();
  expect(screen.queryByText("Installed")).toBeNull();
});

test("shows one failed-to-load message when the plugin list fails to load", async () => {
  const fake = connectFakeClient();
  fake.on("serf/marketplace/list", () => ({ marketplaces: [] }));
  fake.on("serf/plugin/list", () => {
    throw new Error("boom");
  });
  render(<MarketplacesPluginsSection />);
  expect(await screen.findByText(/Failed to load: boom/)).toBeTruthy();
});
