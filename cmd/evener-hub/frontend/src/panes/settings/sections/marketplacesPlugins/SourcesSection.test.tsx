import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../../../stores/connection";
import { extensionsStore, resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { hostsStore } from "../../../../stores/hosts";
import { resetSettingsHostForTests, setSettingsHost } from "../../../../stores/settingsHost";
import { MarketplacesPluginsHostScope, MarketplacesPluginsSection } from "./index";

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
  hostsStore.getState().resetForTests();
  resetSettingsHostForTests();
});
afterEach(cleanup);

function connect() {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  fake.on("evener/marketplace/list", () => ({ marketplaces: [] }));
  fake.on("evener/plugin/list", () => ({ plugins: [] }));
  fake.on("evener/launch/getLayer", () => ({ pluginDirs: ["/fixture/plugins"] }));
  fake.on("evener/paths/complete", () => ({ data: [] }));
  fake.on("evener/path/validate", () => ({ path: "/fixture/new", valid: true }));
  return fake;
}

test.each(["evener/marketplace/list", "evener/plugin/list"] as const)(
  "Sources keeps a directory draft through %s failure and recovery",
  async (method) => {
    const fake = connect();
    fake.on(method, () => {
      throw new Error("fixture inventory failure");
    });
    const user = userEvent.setup();
    render(<MarketplacesPluginsSection />);
    await user.click(screen.getByRole("radio", { name: /Sources/ }));
    await screen.findByText("/fixture/plugins");
    await user.click(screen.getByRole("button", { name: /^New directory:/ }));
    const input = await screen.findByRole("textbox", { name: "Path" });
    await user.clear(input);
    await user.type(input, "/fixture/new");
    fake.on("evener/marketplace/list", () => ({ marketplaces: [] }));
    fake.on("evener/plugin/list", () => ({ plugins: [] }));
    await act(async () => {
      await extensionsStore.getState().fetchMarketplaces();
      await extensionsStore.getState().fetchPlugins();
    });
    expect((screen.getByRole("textbox", { name: "Path" }) as HTMLInputElement).value).toBe("/fixture/new");
    expect(fake.calls.filter((c) => c.method === "evener/launch/setLayer")).toHaveLength(0);
    await user.keyboard("{Enter}");
    await user.click(screen.getByRole("button", { name: "Use this folder" }));
    fake.on("evener/launch/setLayer", () => ({ effective: {}, layers: {}, provenance: {} }));
    await user.click(screen.getByRole("button", { name: "Add" }));
    await screen.findByText("/fixture/new");
    expect(fake.calls.filter((c) => c.method === "evener/launch/setLayer").map((c) => c.params)).toEqual([
      { cwd: "/", layer: "global", config: { pluginDirs: ["/fixture/plugins", "/fixture/new"] } },
    ]);
  },
);

test("one host picker owns marketplace and directory reads across remote and local visits", async () => {
  const fake = connect();
  fake.on("evener/host/list", () => ({
    hosts: [
      {
        name: "beta",
        generation: 1,
        incarnationId: "fixture",
        origin: "hub.toml",
        attached: true,
        midAttach: false,
        removed: false,
      },
    ],
  }));
  fake.on("evener/host/request", (params) => {
    const request = params as { host: string; method: string };
    expect(request.host).toBe("beta");
    if (request.method === "evener/marketplace/list") return { marketplaces: [] } as never;
    if (request.method === "evener/plugin/list" || request.method === "evener/plugin/checkUpdates")
      return { plugins: [] } as never;
    if (request.method === "evener/paths/complete") return { data: [] } as never;
    if (request.method === "evener/path/validate") return { path: "/beta/new", valid: true } as never;
    if (request.method === "evener/launch/setLayer") return { effective: {}, layers: {}, provenance: {} } as never;
    if (request.method === "evener/launch/getLayer") return { pluginDirs: ["/beta/plugins"] } as never;
    throw new Error(`Unexpected fixture method ${request.method}`);
  });
  setSettingsHost("beta");
  const user = userEvent.setup();
  render(<MarketplacesPluginsHostScope />);
  await user.click(screen.getByRole("radio", { name: /Sources/ }));
  await screen.findByText("/beta/plugins");
  expect(screen.getAllByLabelText("Host")).toHaveLength(1);
  await user.click(screen.getByRole("button", { name: /^New directory:/ }));
  const input = await screen.findByRole("textbox", { name: "Path" });
  await user.clear(input);
  await user.type(input, "/beta/new");
  await user.keyboard("{Enter}");
  await user.click(screen.getByRole("button", { name: "Use this folder" }));
  await user.click(screen.getByRole("button", { name: "Add" }));
  await screen.findByText("/beta/new");
  await user.click(screen.getByRole("button", { name: "Remove /beta/new" }));
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
  await waitFor(() => expect(screen.queryByText("/beta/new")).toBeNull());
  expect(fake.calls.filter((c) => c.method === "evener/launch/setLayer")).toHaveLength(0);
  expect(
    fake.calls
      .filter(
        (c) =>
          c.method === "evener/host/request" && (c.params as { method: string }).method === "evener/launch/setLayer",
      )
      .map((c) => c.params),
  ).toEqual([
    {
      host: "beta",
      method: "evener/launch/setLayer",
      params: { cwd: "/", layer: "global", config: { pluginDirs: ["/beta/plugins", "/beta/new"] } },
    },
    {
      host: "beta",
      method: "evener/launch/setLayer",
      params: { cwd: "/", layer: "global", config: { pluginDirs: ["/beta/plugins"] } },
    },
  ]);

  expect(fake.calls.filter((c) => c.method === "evener/launch/getLayer")).toHaveLength(0);
  await user.selectOptions(screen.getByLabelText("Host"), "local");
  await user.click(screen.getByRole("radio", { name: /Sources/ }));
  await screen.findByText("/fixture/plugins");
  expect(screen.queryByText("/beta/plugins")).toBeNull();
  await user.selectOptions(screen.getByLabelText("Host"), "beta");
  await user.click(screen.getByRole("radio", { name: /Sources/ }));
  await screen.findByText("/beta/plugins");
  await waitFor(() => expect(screen.queryByText("/fixture/plugins")).toBeNull());
});

test("directory read failure does not prevent returning to installed plugins", async () => {
  const fake = connect();
  fake.on("evener/launch/getLayer", () => {
    throw new Error("fixture directory unavailable");
  });
  fake.on("evener/plugin/list", () => ({
    plugins: [
      {
        plugin: "fixture-linter",
        marketplace: "fixture",
        version: "1",
        enabled: true,
        autoUpgrade: false,
        broken: false,
        installPath: "/fixture",
        installedAt: 1,
        lastUpdated: 1,
      },
    ],
  }));
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await screen.findByRole("button", { name: /fixture-linter/ });
  await user.click(screen.getByRole("radio", { name: /Sources/ }));
  await waitFor(() => expect(document.querySelector('[data-reason="unavailable"]')).not.toBeNull());
  await user.click(screen.getByRole("radio", { name: /Installed/ }));
  expect(await screen.findByRole("button", { name: /fixture-linter/ })).toBeTruthy();
});

test("marketplace details remain reachable while plugin inventory is unavailable", async () => {
  const fake = connect();
  fake.on("evener/plugin/list", () => {
    throw new Error("fixture plugins unavailable");
  });
  fake.on("evener/marketplace/list", () => ({
    marketplaces: [{ name: "fixture-market", source: { kind: "github", repo: "fixture/plugins" }, lastUpdated: 1 }],
  }));
  const user = userEvent.setup();
  render(<MarketplacesPluginsSection />);
  await user.click(await screen.findByRole("radio", { name: /Sources/ }));
  await user.click(await screen.findByRole("button", { name: /fixture-market/ }));
  expect(await screen.findByRole("dialog", { name: "fixture-market" })).toBeTruthy();
});
