import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { MarketplaceEntry, PluginEntry } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { extensionsStore, resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { getToasts, resetToastStoreForTests } from "../../../../widgets/toast/store";
import { BrowseSection } from "./BrowseSection";

const MARKETPLACE_A: MarketplaceEntry = {
  name: "acme-plugins",
  source: { kind: "github", repo: "acme/plugins" },
  lastUpdated: 1000,
};
const MARKETPLACE_B: MarketplaceEntry = {
  name: "other-plugins",
  source: { kind: "url", url: "https://example.com/x.git" },
  lastUpdated: 2000,
};

const INSTALLED_LINTER: PluginEntry = {
  plugin: "linter",
  marketplace: "acme-plugins",
  version: "1.0.0",
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

// Wraps BrowseSection with the lifted expandedMarketplaces state its real
// parent (marketplacesPlugins/index.tsx) owns - a plain useState here
// stands in for that parent for this component's own tests.
function Harness({ initialExpanded = new Set<string>() }: { initialExpanded?: Set<string> }) {
  const [expanded, setExpanded] = useState(initialExpanded);
  return <BrowseSection expandedMarketplaces={expanded} setExpandedMarketplaces={setExpanded} />;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

test("every registered marketplace renders as a collapsed node regardless of filter", () => {
  connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A, MARKETPLACE_B], plugins: [] });
  render(<Harness />);
  expect(screen.getByRole("button", { name: /acme-plugins/ }).getAttribute("aria-expanded")).toBe("false");
  expect(screen.getByRole("button", { name: /other-plugins/ }).getAttribute("aria-expanded")).toBe("false");
});

test("shows the no-marketplaces empty state", () => {
  connectFakeClient();
  extensionsStore.setState({ marketplaces: [], plugins: [] });
  render(<Harness />);
  expect(screen.getByText("No marketplaces registered. Add one above to browse plugins.")).toBeTruthy();
});

test("expanding a node lazily fetches its catalog once; re-expanding does not refetch", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A], plugins: [] });
  const browseSpy = vi.fn(() => ({ name: "acme-plugins", plugins: [{ name: "linter", description: "Lints code" }] }));
  fake.on("serf/marketplace/browse", browseSpy);
  render(<Harness />);
  const toggle = screen.getByRole("button", { name: /acme-plugins/ });
  await user.click(toggle);
  expect(await screen.findByText("linter")).toBeTruthy();
  await user.click(toggle); // collapse
  await user.click(toggle); // re-expand
  expect(screen.getByText("linter")).toBeTruthy();
  expect(browseSpy).toHaveBeenCalledTimes(1);
});

test("shows loading, then error, for a failed browse", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A], plugins: [] });
  fake.on("serf/marketplace/browse", () => {
    throw new Error("network down");
  });
  render(<Harness />);
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  expect(await screen.findByText("Failed to browse: network down")).toBeTruthy();
});

test("an empty catalog shows its own empty message", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A], plugins: [] });
  fake.on("serf/marketplace/browse", () => ({ name: "acme-plugins", plugins: [] }));
  render(<Harness />);
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  expect(await screen.findByText("This marketplace has no plugins.")).toBeTruthy();
});

test("an already-installed plugin shows an Installed badge instead of an Install button", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A], plugins: [INSTALLED_LINTER] });
  fake.on("serf/marketplace/browse", () => ({
    name: "acme-plugins",
    plugins: [{ name: "linter" }, { name: "formatter" }],
  }));
  render(<Harness />);
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  await screen.findByText("linter");
  const linterRow = screen.getByText("linter").closest("li") as HTMLElement;
  expect(within(linterRow).getByText("Installed")).toBeTruthy();
  expect(within(linterRow).queryByRole("button", { name: "Install" })).toBeNull();
  const formatterRow = screen.getByText("formatter").closest("li") as HTMLElement;
  expect(within(formatterRow).getByRole("button", { name: "Install" })).toBeTruthy();
});

test("Install opens a non-destructive confirm; confirming installs and toasts success", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A], plugins: [] });
  fake.on("serf/marketplace/browse", () => ({ name: "acme-plugins", plugins: [{ name: "linter" }] }));
  fake.on("serf/plugin/install", (params) => {
    expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins" });
    return { plugins: [INSTALLED_LINTER] };
  });
  render(<Harness />);
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  await user.click(await screen.findByRole("button", { name: "Install" }));
  const dialog = screen.getByRole("dialog", { name: "Install plugin" });
  expect(within(dialog).getByRole("button", { name: "Install" })).toBeTruthy();
  await user.click(within(dialog).getByRole("button", { name: "Install" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "success" && t.text === "Installed linter")).toBe(true),
  );
});

test("cancelling the install confirm does not call installPlugin", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A], plugins: [] });
  fake.on("serf/marketplace/browse", () => ({ name: "acme-plugins", plugins: [{ name: "linter" }] }));
  const installSpy = vi.fn();
  fake.on("serf/plugin/install", installSpy);
  render(<Harness />);
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  await user.click(await screen.findByRole("button", { name: "Install" }));
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  expect(installSpy).not.toHaveBeenCalled();
});

test("a failed install toasts failure", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A], plugins: [] });
  fake.on("serf/marketplace/browse", () => ({ name: "acme-plugins", plugins: [{ name: "linter" }] }));
  fake.on("serf/plugin/install", () => {
    throw new Error("boom");
  });
  render(<Harness />);
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  await user.click(await screen.findByRole("button", { name: "Install" }));
  const dialog = screen.getByRole("dialog", { name: "Install plugin" });
  await user.click(within(dialog).getByRole("button", { name: "Install" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "error" && t.text === "Install failed: boom")).toBe(true),
  );
});

test("the install confirm's buttons disable while the install is in flight", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A], plugins: [] });
  fake.on("serf/marketplace/browse", () => ({ name: "acme-plugins", plugins: [{ name: "linter" }] }));
  fake.on("serf/plugin/install", () => new Promise(() => {})); // never resolves - just observe the mid-flight state
  render(<Harness />);
  await user.click(screen.getByRole("button", { name: /acme-plugins/ }));
  await user.click(await screen.findByRole("button", { name: "Install" }));
  const dialog = screen.getByRole("dialog", { name: "Install plugin" });
  await user.click(within(dialog).getByRole("button", { name: "Install" }));
  expect((within(dialog).getByRole("button", { name: "Install" }) as HTMLButtonElement).disabled).toBe(true);
  expect((within(dialog).getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(true);
});

test("typing a filter query auto-expands (after the debounce) a marketplace with a matching plugin and hides a successfully-loaded, zero-match one entirely", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const user = userEvent.setup({ delay: null, advanceTimers: vi.advanceTimersByTime });
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A, MARKETPLACE_B], plugins: [] });
  fake.on("serf/marketplace/browse", (params) =>
    params.name === "acme-plugins"
      ? { name: "acme-plugins", plugins: [{ name: "linter" }] }
      : { name: "other-plugins", plugins: [{ name: "formatter" }] },
  );
  render(<Harness />);
  await user.type(screen.getByPlaceholderText("Filter plugins…"), "lint");
  await vi.advanceTimersByTimeAsync(150);
  await waitFor(() =>
    expect(screen.getByRole("button", { name: /acme-plugins/ }).getAttribute("aria-expanded")).toBe("true"),
  );
  // other-plugins' catalog resolved with zero matches for "lint" - per the
  // floor doc ("only a successfully-loaded, zero-match catalog is hidden
  // entirely"), it's not merely collapsed, it's absent from the tree.
  expect(screen.queryByRole("button", { name: /other-plugins/ })).toBeNull();
  expect(screen.getByText("linter")).toBeTruthy();
});

test("clearing the filter immediately collapses every node with no debounce", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const user = userEvent.setup({ delay: null, advanceTimers: vi.advanceTimersByTime });
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A], plugins: [] });
  fake.on("serf/marketplace/browse", () => ({ name: "acme-plugins", plugins: [{ name: "linter" }] }));
  render(<Harness initialExpanded={new Set(["acme-plugins"])} />);
  const filter = screen.getByPlaceholderText("Filter plugins…");
  await user.type(filter, "x");
  await user.clear(filter);
  expect(screen.getByRole("button", { name: /acme-plugins/ }).getAttribute("aria-expanded")).toBe("false");
});

test("zero matches anywhere shows a not-found message quoting the query", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const user = userEvent.setup({ delay: null, advanceTimers: vi.advanceTimersByTime });
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A], plugins: [] });
  fake.on("serf/marketplace/browse", () => ({ name: "acme-plugins", plugins: [{ name: "linter" }] }));
  render(<Harness />);
  await user.type(screen.getByPlaceholderText("Filter plugins…"), "zzz");
  await vi.advanceTimersByTimeAsync(150);
  expect(await screen.findByText('No plugins match "zzz".')).toBeTruthy();
});
