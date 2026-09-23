import type { HostRow, MarketplaceEntry, PluginEntry } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../../../stores/connection";
import { resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { hostsStore } from "../../../../stores/hosts";
import { resetSettingsHostForTests, setSettingsHost } from "../../../../stores/settingsHost";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
import { MarketplacesPluginsHostScope } from "./index";

// The Marketplaces & Plugins settings surface's host scope (component 07b): a
// remote selection shows THAT host's own marketplaces and plugins and changes
// them back through evener/host/request, never this hub's.

function hostRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return { origin: "sidecar", attached: false, midAttach: false, removed: false, ...overrides };
}

function plugin(name: string): PluginEntry {
  return {
    plugin: name,
    marketplace: "acme-plugins",
    version: "1.0.0",
    enabled: true,
    autoUpgrade: false,
    broken: false,
    installPath: "/x",
    installedAt: 1,
    lastUpdated: 1,
  };
}

const MARKETPLACE: MarketplaceEntry = {
  name: "acme-plugins",
  source: { kind: "github", repo: "acme/plugins" },
  lastUpdated: 1,
};

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function controllerScopedCalls(fake: FakeClient): string[] {
  return fake.calls
    .map((call) => call.method)
    .filter((method) => method.startsWith("evener/marketplace/") || method.startsWith("evener/plugin/"));
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
  resetToastStoreForTests();
  hostsStore.getState().resetForTests();
  resetSettingsHostForTests();
  window.history.pushState({}, "", "/settings/plugins-manager");
  setSettingsHost("local");
});

afterEach(() => {
  cleanup();
  resetExtensionsStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  window.history.pushState({}, "", "/");
});

test("with this hub selected the section reads this hub's own lists and never the proxy", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/marketplace/list", () => ({ marketplaces: [MARKETPLACE] }));
  fake.on("evener/plugin/list", () => ({ plugins: [plugin("controller-linter")] }));
  fake.on("evener/host/request", () => {
    throw new Error("the local hub must never route through the proxy");
  });

  render(<MarketplacesPluginsHostScope />);

  expect(await screen.findByRole("button", { name: /controller-linter/ })).toBeTruthy();
  expect(fake.calls.some((call) => call.method === "evener/host/request")).toBe(false);
});

test("a remote selection shows THAT host's own marketplaces and plugins, never this hub's", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/marketplace/list", () => {
    throw new Error("a remote selection must not read this hub's marketplaces");
  });
  fake.on("evener/plugin/list", () => {
    throw new Error("a remote selection must not read this hub's plugins");
  });
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { host: string; method: string };
    expect(forwarded.host).toBe("beta");
    if (forwarded.method === "evener/marketplace/list") return { marketplaces: [MARKETPLACE] } as never;
    if (forwarded.method === "evener/plugin/list") return { plugins: [plugin("beta-linter")] } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<MarketplacesPluginsHostScope />);

  // The host's own plugin is shown...
  expect(await screen.findByRole("button", { name: /beta-linter/ })).toBeTruthy();
  expect(screen.getByRole("radio", { name: "Installed (1)" })).toBeTruthy();
  // ...and no controller-scoped marketplace/plugin call was made.
  expect(controllerScopedCalls(fake)).toEqual([]);
});

test("a remote plugin mutation goes through the proxy, never this hub's plugin methods", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  // Booby-trap every controller-scoped marketplace/plugin method.
  for (const method of ["evener/marketplace/list", "evener/plugin/list", "evener/plugin/disable"] as const) {
    fake.on(method, () => {
      throw new Error(`a remote selection must not call this hub's ${method}`);
    });
  }
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { host: string; method: string };
    expect(forwarded.host).toBe("beta");
    if (forwarded.method === "evener/marketplace/list") return { marketplaces: [MARKETPLACE] } as never;
    if (forwarded.method === "evener/plugin/list") return { plugins: [plugin("beta-linter")] } as never;
    if (forwarded.method === "evener/marketplace/browse") return { name: "acme-plugins", plugins: [] } as never;
    if (forwarded.method === "evener/plugin/disable") return { plugins: [plugin("beta-linter")] } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<MarketplacesPluginsHostScope />);
  const user = userEvent.setup();

  await user.click(await screen.findByRole("button", { name: /beta-linter/ }));
  await screen.findByRole("dialog", { name: "beta-linter" });
  await user.click(screen.getByRole("switch", { name: "Enabled by default" }));

  await waitFor(() =>
    expect(
      fake.calls.some(
        (call) =>
          call.method === "evener/host/request" &&
          (call.params as { method: string }).method === "evener/plugin/disable",
      ),
    ).toBe(true),
  );
  expect(fake.calls.some((call) => call.method === "evener/plugin/disable")).toBe(false);
});
