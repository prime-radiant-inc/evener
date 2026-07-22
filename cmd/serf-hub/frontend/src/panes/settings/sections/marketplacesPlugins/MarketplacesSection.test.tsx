import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { MarketplaceEntry } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { extensionsStore, resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { getToasts, resetToastStoreForTests } from "../../../../widgets/toast/store";
import { MarketplacesSection } from "./MarketplacesSection";

const MARKETPLACE_A: MarketplaceEntry = {
  name: "acme-plugins",
  source: { kind: "github", repo: "acme/plugins" },
  lastUpdated: 1000,
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

test("renders the heading, count, and each marketplace's name/kind/sourceLabel", () => {
  connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A] });
  render(<MarketplacesSection expandedMarketplaces={new Set()} />);
  expect(screen.getByText("Marketplaces")).toBeTruthy();
  expect(screen.getByText("1 entry")).toBeTruthy();
  expect(screen.getByText("acme-plugins")).toBeTruthy();
  expect(screen.getByText("github")).toBeTruthy();
  expect(screen.getByText("github: acme/plugins")).toBeTruthy();
});

test("shows the empty state and pluralizes the count when there are no marketplaces", () => {
  connectFakeClient();
  extensionsStore.setState({ marketplaces: [] });
  render(<MarketplacesSection expandedMarketplaces={new Set()} />);
  expect(screen.getByText("No marketplaces registered. Add one below.")).toBeTruthy();
  expect(screen.getByText("0 entries")).toBeTruthy();
});

test("the Add form and the + Add marketplace button are mutually exclusive", async () => {
  const user = userEvent.setup();
  connectFakeClient();
  extensionsStore.setState({ marketplaces: [] });
  render(<MarketplacesSection expandedMarketplaces={new Set()} />);
  expect(screen.getByRole("button", { name: "+ Add marketplace" })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "+ Add marketplace" }));
  expect(screen.queryByRole("button", { name: "+ Add marketplace" })).toBeNull();
  expect(screen.getByRole("radiogroup", { name: "Source" })).toBeTruthy();
});

test("only the field matching the checked source radio is shown", async () => {
  const user = userEvent.setup();
  connectFakeClient();
  extensionsStore.setState({ marketplaces: [] });
  render(<MarketplacesSection expandedMarketplaces={new Set()} />);
  await user.click(screen.getByRole("button", { name: "+ Add marketplace" }));
  expect(screen.getByPlaceholderText("https://github.com/owner/repo.git")).toBeTruthy();
  expect(screen.queryByPlaceholderText("owner/repo")).toBeNull();

  await user.click(screen.getByRole("radio", { name: "owner/repo" }));
  expect(screen.getByPlaceholderText("owner/repo")).toBeTruthy();
  expect(screen.queryByPlaceholderText("https://github.com/owner/repo.git")).toBeNull();

  await user.click(screen.getByRole("radio", { name: "Local path" }));
  expect(screen.getByPlaceholderText("/absolute/path")).toBeTruthy();
});

test("submitting the github kind sends {kind:github,repo} and closes on success", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [] });
  fake.on("serf/marketplace/add", (params) => {
    expect(params).toEqual({ name: "", source: { kind: "github", repo: "acme/plugins" } });
    return { marketplaces: [MARKETPLACE_A] };
  });
  render(<MarketplacesSection expandedMarketplaces={new Set()} />);
  await user.click(screen.getByRole("button", { name: "+ Add marketplace" }));
  await user.click(screen.getByRole("radio", { name: "owner/repo" }));
  await user.type(screen.getByPlaceholderText("owner/repo"), "acme/plugins");
  await user.click(screen.getByRole("button", { name: "Add" }));
  await waitFor(() => expect(screen.queryByRole("radiogroup", { name: "Source" })).toBeNull());
  expect(getToasts().some((t) => t.kind === "success" && t.text === "Added marketplace")).toBe(true);
});

test("a non-empty name is appended to the success toast and sent in the payload", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [] });
  fake.on("serf/marketplace/add", (params) => {
    expect(params).toEqual({ name: "my-name", source: { kind: "url", url: "https://example.com/x.git" } });
    return { marketplaces: [] };
  });
  render(<MarketplacesSection expandedMarketplaces={new Set()} />);
  await user.click(screen.getByRole("button", { name: "+ Add marketplace" }));
  await user.type(screen.getByPlaceholderText("https://github.com/owner/repo.git"), "https://example.com/x.git");
  await user.type(screen.getByPlaceholderText("defaults to the marketplace's own name"), "my-name");
  await user.click(screen.getByRole("button", { name: "Add" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "success" && t.text === "Added marketplace my-name")).toBe(true),
  );
});

test("a failed add toasts failure and keeps the form open", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [] });
  fake.on("serf/marketplace/add", () => {
    throw new Error("boom");
  });
  render(<MarketplacesSection expandedMarketplaces={new Set()} />);
  await user.click(screen.getByRole("button", { name: "+ Add marketplace" }));
  await user.type(screen.getByPlaceholderText("https://github.com/owner/repo.git"), "https://example.com/x.git");
  await user.click(screen.getByRole("button", { name: "Add" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "error" && t.text === "Add marketplace failed: boom")).toBe(true),
  );
  expect(screen.getByRole("radiogroup", { name: "Source" })).toBeTruthy();
});

test("Cancel closes the form without calling addMarketplace", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [] });
  const addSpy = vi.fn();
  fake.on("serf/marketplace/add", addSpy);
  render(<MarketplacesSection expandedMarketplaces={new Set()} />);
  await user.click(screen.getByRole("button", { name: "+ Add marketplace" }));
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  expect(screen.getByRole("button", { name: "+ Add marketplace" })).toBeTruthy();
  expect(addSpy).not.toHaveBeenCalled();
});

test("Refresh calls refreshMarketplace and toasts success", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A] });
  fake.on("serf/marketplace/refresh", (params) => {
    expect(params).toEqual({ name: "acme-plugins" });
    return { marketplaces: [MARKETPLACE_A] };
  });
  render(<MarketplacesSection expandedMarketplaces={new Set()} />);
  await user.click(screen.getByRole("button", { name: "Refresh" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "success" && t.text === "Refreshed acme-plugins")).toBe(true),
  );
});

test("Refresh on an expanded marketplace also re-browses it (the cache the refresh just invalidated)", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A] });
  fake.on("serf/marketplace/refresh", () => ({ marketplaces: [MARKETPLACE_A] }));
  const browseSpy = vi.fn(() => ({ name: "acme-plugins", plugins: [] }));
  fake.on("serf/marketplace/browse", browseSpy);
  render(<MarketplacesSection expandedMarketplaces={new Set(["acme-plugins"])} />);
  await user.click(screen.getByRole("button", { name: "Refresh" }));
  await waitFor(() => expect(browseSpy).toHaveBeenCalledWith({ name: "acme-plugins" }));
});

test("Remove opens a confirm dialog; confirming removes and toasts success", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A] });
  fake.on("serf/marketplace/remove", (params) => {
    expect(params).toEqual({ name: "acme-plugins" });
    return { marketplaces: [] };
  });
  render(<MarketplacesSection expandedMarketplaces={new Set()} />);
  await user.click(screen.getByRole("button", { name: "Remove" }));
  const dialog = screen.getByRole("dialog", { name: "Remove marketplace" });
  expect(screen.getByText('Remove marketplace "acme-plugins"? Installed plugins from it are unaffected.')).toBeTruthy();
  await user.click(within(dialog).getByRole("button", { name: "Remove" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "success" && t.text === "Removed marketplace acme-plugins")).toBe(true),
  );
});

test("cancelling the remove confirm does not call removeMarketplace", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A] });
  const removeSpy = vi.fn();
  fake.on("serf/marketplace/remove", removeSpy);
  render(<MarketplacesSection expandedMarketplaces={new Set()} />);
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  expect(removeSpy).not.toHaveBeenCalled();
});

test("the confirm dialog's buttons disable while removal is in flight, and it stays open until it resolves", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A] });
  let resolveRemove: (v: { marketplaces: MarketplaceEntry[] }) => void = () => {};
  fake.on(
    "serf/marketplace/remove",
    () =>
      new Promise((resolve) => {
        resolveRemove = resolve;
      }),
  );
  render(<MarketplacesSection expandedMarketplaces={new Set()} />);
  await user.click(screen.getByRole("button", { name: "Remove" }));
  const dialog = screen.getByRole("dialog", { name: "Remove marketplace" });
  await user.click(within(dialog).getByRole("button", { name: "Remove" }));

  expect((within(dialog).getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true);
  expect((within(dialog).getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(true);
  expect(screen.getByRole("dialog", { name: "Remove marketplace" })).toBeTruthy(); // still open mid-flight

  resolveRemove({ marketplaces: [] });
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});
