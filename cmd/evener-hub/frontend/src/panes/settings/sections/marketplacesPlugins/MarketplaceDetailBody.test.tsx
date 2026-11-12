import { type MarketplaceEntry, WireError } from "@evener/appwire-client";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore, resetConnectionStoreForTests } from "../../../../stores/connection";
import { extensionsStore, resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { connectReadyFakeClient } from "../../../../testing/connectionTestUtils";
import { Toast } from "../../../../widgets";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
import { MarketplaceDetailBody, type MarketplaceDetailBodyProps } from "./MarketplaceDetailBody";

const ACME: MarketplaceEntry = {
  name: "acme",
  source: { kind: "github", repo: "acme/plugins" },
  installLocation: "/home/u/.config/evener/plugins/marketplaces/acme",
  lastUpdated: 1_700_000_000,
};

const LOCAL: MarketplaceEntry = {
  name: "local",
  source: { kind: "directory", path: "/srv/mkt" },
  installLocation: "",
  lastUpdated: 0,
};

const EMPTY_CATALOG = { status: "loaded" as const, plugins: [] };

function renderBody(overrides: Partial<MarketplaceDetailBodyProps> = {}) {
  const entry = overrides.marketplace ?? ACME;
  extensionsStore.setState({ marketplaces: [entry] });
  const onRenamed = vi.fn();
  const onCollapse = vi.fn();
  const onAppliedRemoval = vi.fn();
  const props: MarketplaceDetailBodyProps = {
    id: "marketplace-body",
    marketplace: entry,
    catalog: EMPTY_CATALOG,
    isInstalled: () => false,
    installDisabled: false,
    expandedRow: null,
    onToggleRow: vi.fn(),
    onInstall: vi.fn(),
    onRenamed,
    onCollapse,
    appliedRemovalNames: new Set(),
    connectionClient: connectionStore.getState().client ?? null,
    onAppliedRemoval,
    ...overrides,
  };
  const view = render(
    <>
      <Toast />
      <MarketplaceDetailBody {...props} />
    </>,
  );
  return { view, props, onRenamed, onCollapse, onAppliedRemoval };
}

function field(label: string): HTMLInputElement {
  return screen.getByLabelText(label) as HTMLInputElement;
}
function saveButton(): HTMLButtonElement {
  return screen.getByRole("button", { name: "Save" }) as HTMLButtonElement;
}

function cloneLitterError(marketplaces: unknown, extra: Record<string, unknown> = {}): WireError {
  return new WireError('marketplace "acme": marketplace unregistered, but its clone could not be removed', -32603, {
    evenerErrorInfo: "marketplaceUnregisteredCloneRemains",
    applied: { marketplaces },
    ...extra,
  });
}

beforeEach(() => {
  resetConnectionStoreForTests();
  resetExtensionsStoreForTests();
  resetToastStoreForTests();
  connectReadyFakeClient();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

test("shows install location, the ref pin, and last updated without opening the editor", () => {
  renderBody({ marketplace: { ...ACME, source: { kind: "github", repo: "acme/plugins", ref: "main" } } });
  expect(screen.getByTestId("marketplace-install-location").textContent).toContain("/marketplaces/acme");
  expect(screen.getByTestId("marketplace-pinned-to").textContent).toBe("main");
  expect(screen.getByTestId("marketplace-last-updated").textContent).not.toBe("never");
  // The management actions are inline; the editor only when asked for.
  expect(screen.getByRole("button", { name: "Refresh" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Edit" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Remove" })).toBeTruthy();
  expect(screen.queryByLabelText("Name")).toBeNull();
});

test("Edit prefills the name, the source kind, and its field; Save is disabled until something changes", async () => {
  const user = userEvent.setup();
  renderBody();
  await user.click(screen.getByRole("button", { name: "Edit" }));
  expect(field("Name").value).toBe("acme");
  expect(field("owner/repo").value).toBe("acme/plugins");
  expect(saveButton().disabled).toBe(true);

  await user.type(field("Name"), "x");
  expect(saveButton().disabled).toBe(false);
});

test("switching the kind shows only that kind's field and the re-fetch note", async () => {
  const user = userEvent.setup();
  renderBody();
  await user.click(screen.getByRole("button", { name: "Edit" }));
  expect(screen.queryByTestId("marketplace-refetch-note")).toBeNull();
  await user.click(screen.getByRole("radio", { name: "Local path" }));
  expect(field("Local path")).toBeTruthy();
  expect(screen.queryByLabelText("owner/repo")).toBeNull();
  expect(screen.getByTestId("marketplace-refetch-note")).toBeTruthy();
});

test("a rename saves and hands the new name to the page", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/marketplace/edit", () => ({ marketplaces: [{ ...ACME, name: "acme-2" }] }));
  const { onRenamed } = renderBody();
  await user.click(screen.getByRole("button", { name: "Edit" }));
  await user.clear(field("Name"));
  await user.type(field("Name"), "acme-2");
  await user.click(saveButton());

  expect(onRenamed).toHaveBeenCalledWith("acme", "acme-2");
});

test("a source save reseeds the form from the refreshed entry", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  const refreshed = { ...ACME, source: { kind: "url" as const, url: "https://example.com/acme.git" } };
  fake.on("evener/marketplace/edit", () => ({ marketplaces: [refreshed] }));
  const { view, props } = renderBody();
  await user.click(screen.getByRole("button", { name: "Edit" }));
  await user.click(screen.getByRole("radio", { name: "Git URL" }));
  const url = field("Git URL");
  await user.type(url, "https://example.com/acme.git");
  await user.click(saveButton());
  // The page hands the refreshed entry back; the editor reopens on it.
  view.rerender(
    <>
      <Toast />
      <MarketplaceDetailBody {...props} marketplace={refreshed} />
    </>,
  );
  await user.click(screen.getByRole("button", { name: "Edit" }));
  expect(field("Git URL").value).toBe("https://example.com/acme.git");
  expect(saveButton().disabled).toBe(true);
});

test("a source changed elsewhere offers reload or keep, and keeps Save off until chosen", async () => {
  const user = userEvent.setup();
  const { view, props } = renderBody();
  await user.click(screen.getByRole("button", { name: "Edit" }));
  await user.type(field("Name"), "x");
  const moved = { ...ACME, source: { kind: "github" as const, repo: "acme/plugins-moved" } };
  view.rerender(
    <>
      <Toast />
      <MarketplaceDetailBody {...props} marketplace={moved} />
    </>,
  );

  expect(screen.getByRole("button", { name: "Reload latest" })).toBeTruthy();
  expect(saveButton().disabled).toBe(true);
  await user.click(screen.getByRole("button", { name: "Keep my edits" }));
  expect(saveButton().disabled).toBe(false);
});

test("a failed save shows the error inline and toasts", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/marketplace/edit", () => {
    throw new Error("edit failed");
  });
  renderBody();
  await user.click(screen.getByRole("button", { name: "Edit" }));
  await user.type(field("Name"), "x");
  await user.click(saveButton());

  expect(within(screen.getByRole("form", { name: "Edit acme" })).getByText(/edit failed/)).toBeTruthy();
});

test("Enter in the form never saves - the Save button is the only path", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  const edit = vi.fn(() => ({ marketplaces: [ACME] }));
  fake.on("evener/marketplace/edit", edit);
  renderBody();
  await user.click(screen.getByRole("button", { name: "Edit" }));
  await user.type(field("Name"), "x{Enter}");
  expect(edit).not.toHaveBeenCalled();
});

test("Refresh refreshes the marketplace and re-browses its catalog", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/marketplace/refresh", () => ({ marketplaces: [ACME] }));
  fake.on("evener/marketplace/browse", () => ({ name: "acme", plugins: [] }));
  const before = fake.calls.filter((call) => call.method === "evener/marketplace/browse").length;
  renderBody();
  await user.click(screen.getByRole("button", { name: "Refresh" }));
  await vi.waitFor(() =>
    expect(fake.calls.filter((call) => call.method === "evener/marketplace/refresh")).toHaveLength(1),
  );
  await vi.waitFor(() =>
    expect(fake.calls.filter((call) => call.method === "evener/marketplace/browse").length).toBe(before + 1),
  );
});

test("Remove asks first; confirming removes the marketplace", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/marketplace/remove", () => ({ marketplaces: [] }));
  renderBody();
  await user.click(screen.getByRole("button", { name: "Remove" }));
  const dialog = screen.getByRole("dialog", { name: "Remove marketplace" });
  await user.click(within(dialog).getByRole("button", { name: "Remove" }));

  expect(fake.calls.filter((call) => call.method === "evener/marketplace/remove")).toHaveLength(1);
});

test("cancelling the remove confirm calls nothing", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  renderBody();
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  expect(fake.calls.filter((call) => call.method === "evener/marketplace/remove")).toHaveLength(0);
});

test("a removal applied before reconciliation records it and disables Remove", async () => {
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("evener/marketplace/remove", () => {
    throw cloneLitterError([ACME]);
  });
  const { onAppliedRemoval } = renderBody();
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(
    within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Remove" }),
  );

  await vi.waitFor(() => expect(onAppliedRemoval).toHaveBeenCalled());
});

test("Hide details collapses through the page", async () => {
  const user = userEvent.setup();
  const { onCollapse } = renderBody();
  await user.click(screen.getByRole("button", { name: "Hide details" }));
  expect(onCollapse).toHaveBeenCalledOnce();
});

test("a catalog row expands its plugin details inline and installs through the page", async () => {
  const user = userEvent.setup();
  const onInstall = vi.fn();
  const onToggleRow = vi.fn();
  const catalog = {
    status: "loaded" as const,
    plugins: [{ name: "linter", description: "Lints", category: undefined }],
  } as unknown as MarketplaceDetailBodyProps["catalog"];
  renderBody({ catalog, onInstall, onToggleRow });
  await user.click(screen.getByRole("button", { name: /linter/ }));
  expect(onToggleRow).toHaveBeenCalledWith({ plugin: "linter", marketplace: "acme" });
  await user.click(screen.getByRole("button", { name: "Install" }));
  expect(onInstall).toHaveBeenCalledWith("linter", "acme");
});

test("an installed catalog plugin shows its badge instead of an Install action", () => {
  const catalog = {
    status: "loaded" as const,
    plugins: [{ name: "linter", description: "Lints", category: undefined }],
  } as unknown as MarketplaceDetailBodyProps["catalog"];
  renderBody({ catalog, isInstalled: () => true });
  expect(screen.getByText("Installed")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Install" })).toBeNull();
});

test("a directory-source body offers the directory field and no pin row", async () => {
  const user = userEvent.setup();
  renderBody({ marketplace: LOCAL });
  expect(screen.queryByTestId("marketplace-pinned-to")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Edit" }));
  expect(screen.getByRole("button", { name: /Local path: \/srv\/mkt/ })).toBeTruthy();
});

// --- review round: ownership, Escape, and filtering ---

test("a connection change closes the remove confirm and reseeds the editor", async () => {
  const user = userEvent.setup();
  connectReadyFakeClient();
  const { view, props } = renderBody();
  await user.click(screen.getByRole("button", { name: "Edit" }));
  await user.type(field("Name"), "-draft");
  await user.click(screen.getByRole("button", { name: "Remove" }));
  expect(screen.getByRole("dialog", { name: "Remove marketplace" })).toBeTruthy();

  // A replacement registration: the open confirm and the draft belonged to the
  // previous client.
  const second = connectReadyFakeClient();
  view.rerender(
    <>
      <Toast />
      <MarketplaceDetailBody {...props} connectionClient={connectionStore.getState().client ?? null} />
    </>,
  );

  expect(screen.queryByRole("dialog", { name: "Remove marketplace" })).toBeNull();
  expect(field("Name").value).toBe("acme");
  expect(second.calls.filter((call) => call.method === "evener/marketplace/remove")).toHaveLength(0);
});

test("Escape inside the marketplace body collapses it and marks the key handled", async () => {
  const user = userEvent.setup();
  connectReadyFakeClient();
  const { onCollapse } = renderBody();
  const parentKeys: boolean[] = [];
  const listener = (event: KeyboardEvent): void => {
    if (event.key === "Escape") parentKeys.push(event.defaultPrevented);
  };
  document.addEventListener("keydown", listener);
  await user.click(screen.getByRole("button", { name: "Edit" }));
  field("Name").focus();
  await user.keyboard("{Escape}");
  document.removeEventListener("keydown", listener);

  expect(onCollapse).toHaveBeenCalled();
  expect(parentKeys).toEqual([true]);
});

test("a plugin query filters the body's rows when the marketplace name does not match", async () => {
  const catalog = {
    status: "loaded" as const,
    plugins: [
      { name: "linter", description: "Lints", category: undefined },
      { name: "formatter", description: "Formats", category: undefined },
    ],
  } as unknown as MarketplaceDetailBodyProps["catalog"];
  renderBody({ catalog, query: "lint" });
  expect(screen.getByRole("button", { name: /linter/ })).toBeTruthy();
  expect(screen.queryByRole("button", { name: /formatter/ })).toBeNull();
});

test("cancelling the editor keeps the keyboard inside the marketplace body", async () => {
  const user = userEvent.setup();
  const catalog = {
    status: "loaded" as const,
    plugins: [{ name: "linter", description: "Lints", category: undefined }],
  } as unknown as MarketplaceDetailBodyProps["catalog"];
  renderBody({ catalog });
  await user.click(screen.getByRole("button", { name: "Edit" }));
  const cancel = screen.getByRole("button", { name: "Cancel" });
  cancel.focus();
  await user.click(cancel);

  const body = document.querySelector('[data-kind="marketplace-detail"]');
  expect(body).not.toBeNull();
  expect(body?.contains(document.activeElement)).toBe(true);
});

test("closing one plugin's details returns focus to that row, not the first row", async () => {
  const catalog = {
    status: "loaded" as const,
    plugins: [
      { name: "alpha", description: "A", category: undefined },
      { name: "formatter", description: "F", category: undefined },
    ],
  } as unknown as MarketplaceDetailBodyProps["catalog"];
  const pair = { plugin: "formatter", marketplace: "acme" };
  const { view, props } = renderBody({ catalog, expandedRow: pair });
  const details = document.querySelector('[data-kind="plugin-detail"]');
  if (!(details instanceof HTMLElement)) throw new Error("no plugin details");
  within(details).getByRole("button", { name: "Hide details" }).focus();

  // The page collapses the row it was browsing.
  view.rerender(
    <>
      <Toast />
      <MarketplaceDetailBody {...props} catalog={catalog} expandedRow={null} />
    </>,
  );

  const formatterRow = screen.getByRole("button", { name: /formatter/ });
  expect(document.activeElement).toBe(formatterRow);
});
