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

test("renders each marketplace as one tappable row carrying name, kind, and source; tapping selects it", async () => {
  connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A] });
  const onSelect = vi.fn();
  render(<MarketplacesSection onSelect={onSelect} />);
  const row = screen.getByRole("button", { name: /acme-plugins/ });
  expect(within(row).getByText("github")).toBeTruthy();
  expect(within(row).getByText("github: acme/plugins")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Refresh" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
  // No in-section heading or count: the page-level SegmentedControl carries
  // "Marketplaces (n)" - asserted in index.test.tsx.
  expect(screen.queryByRole("heading")).toBeNull();
  expect(screen.queryByText("1 entry")).toBeNull();
  await userEvent.setup().click(row);
  expect(onSelect).toHaveBeenCalledWith("acme-plugins");
});

test("shows the empty state when there are no marketplaces", () => {
  connectFakeClient();
  extensionsStore.setState({ marketplaces: [] });
  render(<MarketplacesSection onSelect={vi.fn()} />);
  expect(screen.getByText("No marketplaces registered. Add one below.")).toBeTruthy();
});

test("the Add form and the + Add marketplace button are mutually exclusive", async () => {
  const user = userEvent.setup();
  connectFakeClient();
  extensionsStore.setState({ marketplaces: [] });
  render(<MarketplacesSection onSelect={vi.fn()} />);
  expect(screen.getByRole("button", { name: "+ Add marketplace" })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "+ Add marketplace" }));
  expect(screen.queryByRole("button", { name: "+ Add marketplace" })).toBeNull();
  expect(screen.getByRole("radiogroup", { name: "Source" })).toBeTruthy();
});

test("only the field matching the checked source radio is shown", async () => {
  const user = userEvent.setup();
  connectFakeClient();
  extensionsStore.setState({ marketplaces: [] });
  render(<MarketplacesSection onSelect={vi.fn()} />);
  await user.click(screen.getByRole("button", { name: "+ Add marketplace" }));
  expect(screen.getByPlaceholderText("https://github.com/owner/repo.git")).toBeTruthy();
  expect(screen.queryByPlaceholderText("owner/repo")).toBeNull();

  await user.click(screen.getByRole("radio", { name: "owner/repo" }));
  expect(screen.getByPlaceholderText("owner/repo")).toBeTruthy();
  expect(screen.queryByPlaceholderText("https://github.com/owner/repo.git")).toBeNull();

  await user.click(screen.getByRole("radio", { name: "Local path" }));
  expect(screen.getByText("/absolute/path")).toBeTruthy(); // the picker's own empty-value marker
});

test("the local-path field browses real directories and sends the picked one", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [] });
  fake.on("evener/paths/complete", (params) => {
    // Directories only, and the prefix goes over the wire verbatim.
    expect(params.includeFiles).toBe(false);
    return { data: params.prefix === "/opt/" ? ["/opt/marketplaces"] : [] };
  });
  fake.on("evener/marketplace/add", (params) => {
    expect(params).toEqual({ name: "", source: { kind: "directory", path: "/opt/marketplaces" } });
    return { marketplaces: [] };
  });
  fake.on("evener/path/validate", ({ path }) => ({ valid: true, path: path === "~" ? "/opt" : path }));
  render(<MarketplacesSection onSelect={vi.fn()} />);
  await user.click(screen.getByRole("button", { name: "+ Add marketplace" }));
  await user.click(screen.getByRole("radio", { name: "Local path" }));

  await user.click(screen.getByLabelText("Local path"));
  await user.click(await screen.findByRole("button", { name: "Open /opt/marketplaces" }));
  expect(fake.calls.some((call) => call.method === "evener/marketplace/add")).toBe(false);
  await user.click(screen.getByRole("button", { name: "Use this folder" }));

  await user.click(screen.getByRole("button", { name: "Add" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "success" && t.text === "Added marketplace")).toBe(true),
  );
});

test("submitting the github kind sends {kind:github,repo} and closes on success", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  extensionsStore.setState({ marketplaces: [] });
  fake.on("evener/marketplace/add", (params) => {
    expect(params).toEqual({ name: "", source: { kind: "github", repo: "acme/plugins" } });
    return { marketplaces: [MARKETPLACE_A] };
  });
  render(<MarketplacesSection onSelect={vi.fn()} />);
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
  fake.on("evener/marketplace/add", (params) => {
    expect(params).toEqual({ name: "my-name", source: { kind: "url", url: "https://example.com/x.git" } });
    return { marketplaces: [] };
  });
  render(<MarketplacesSection onSelect={vi.fn()} />);
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
  fake.on("evener/marketplace/add", () => {
    throw new Error("boom");
  });
  render(<MarketplacesSection onSelect={vi.fn()} />);
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
  fake.on("evener/marketplace/add", addSpy);
  render(<MarketplacesSection onSelect={vi.fn()} />);
  await user.click(screen.getByRole("button", { name: "+ Add marketplace" }));
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  expect(screen.getByRole("button", { name: "+ Add marketplace" })).toBeTruthy();
  expect(addSpy).not.toHaveBeenCalled();
});
