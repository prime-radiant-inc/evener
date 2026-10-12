import { type SettingsOverviewResponse, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { extensionsStore, resetExtensionsStoreForTests } from "../../../stores/extensions";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { McpSection, type SettingsOverviewLike } from "./mcp";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

// Both launch-layer editors mount together once the layer has loaded.
async function launchLayerLoaded(): Promise<void> {
  await screen.findByRole("textbox", { name: "Inline MCP servers" });
}

function discoveredBlock(): Element | null {
  return document.querySelector("[data-section='mcp-discovered']");
}

function overviewHook(overrides: Partial<SettingsOverviewLike> = {}): () => SettingsOverviewLike {
  const state: SettingsOverviewLike = {
    data: null,
    loading: false,
    error: null,
    fetch: vi.fn(async () => {}),
    ...overrides,
  };
  return () => state;
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

test("calls the injected overview hook's fetch on mount", async () => {
  connectFakeClient();
  const fetchFn = vi.fn(async () => {});
  const fetchLaunchLayer = vi.spyOn(extensionsStore.getState(), "fetchLaunchLayer");
  render(<McpSection useOverviewStore={overviewHook({ fetch: fetchFn })} />);
  expect(fetchFn).toHaveBeenCalledOnce();
  const fetch = fetchLaunchLayer.mock.results[0];
  if (fetch?.type !== "return") throw new Error("MCP section did not start its launch-layer fetch");
  await act(() => fetch.value);
});

test("renders discovered servers from the injected overview data with a status per row", async () => {
  connectFakeClient();
  const data: SettingsOverviewResponse = {
    mcpDiscovered: {
      servers: [
        { name: "local-tool", transport: "stdio", status: "available" },
        { name: "remote-api", transport: "http", status: "unreachable", error: "probe-error-from-hub" },
      ],
    },
  };
  const fetchLaunchLayer = vi.spyOn(extensionsStore.getState(), "fetchLaunchLayer");
  render(<McpSection useOverviewStore={overviewHook({ data })} />);
  expect(screen.getByText(/local-tool/)).toBeTruthy();
  expect(screen.getByText("available")).toBeTruthy();
  expect(screen.getByText(/remote-api/)).toBeTruthy();
  expect(screen.getByText("unreachable")).toBeTruthy();
  expect(screen.getByText("probe-error-from-hub")).toBeTruthy();
  const fetch = fetchLaunchLayer.mock.results[0];
  if (fetch?.type !== "return") throw new Error("MCP section did not start its launch-layer fetch");
  await act(() => fetch.value);
});

test("shows the discovered-servers empty state", async () => {
  connectFakeClient();
  const fetchLaunchLayer = vi.spyOn(extensionsStore.getState(), "fetchLaunchLayer");
  render(<McpSection useOverviewStore={overviewHook({ data: { mcpDiscovered: { servers: [] } } })} />);
  expect(screen.getByTestId("mcp-discovered-empty")).toBeTruthy();
  const fetch = fetchLaunchLayer.mock.results[0];
  if (fetch?.type !== "return") throw new Error("MCP section did not start its launch-layer fetch");
  await act(() => fetch.value);
});

test("shows a loading state for discovered servers before the overview resolves", async () => {
  connectFakeClient();
  const fetchLaunchLayer = vi.spyOn(extensionsStore.getState(), "fetchLaunchLayer");
  render(<McpSection useOverviewStore={overviewHook({ loading: true, data: null })} />);
  expect(screen.getAllByRole("status", { name: "Loading" }).length).toBeGreaterThan(0);
  const fetch = fetchLaunchLayer.mock.results[0];
  if (fetch?.type !== "return") throw new Error("MCP section did not start its launch-layer fetch");
  await act(() => fetch.value);
});

test("a top-level probe failure replaces the discovered-servers list with one message", async () => {
  connectFakeClient();
  const fetchLaunchLayer = vi.spyOn(extensionsStore.getState(), "fetchLaunchLayer");
  render(<McpSection useOverviewStore={overviewHook({ data: { mcpDiscovered: { error: "probe timed out" } } })} />);
  expect(screen.queryByRole("list", { name: "Discovered MCP servers" })).toBeNull();
  expect(screen.getByRole("alert").textContent).toContain("probe timed out");
  const fetch = fetchLaunchLayer.mock.results[0];
  if (fetch?.type !== "return") throw new Error("MCP section did not start its launch-layer fetch");
  await act(() => fetch.value);
});

test("renders the mcpConfigs entries from the launch layer", async () => {
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({ mcpConfigs: ["/etc/mcp.json"], mcps: [] }));
  render(<McpSection useOverviewStore={overviewHook()} />);
  expect(await screen.findByText("/etc/mcp.json")).toBeTruthy();
});

test("adding a config file validates as kind:file and saves mcpConfigs", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({ mcpConfigs: [], mcps: [] }));
  fake.on("evener/path/validate", (params) => {
    expect(params).toEqual({ path: "/etc/mcp.json", kind: "file" });
    return { path: "/etc/mcp.json", valid: true };
  });
  fake.on("evener/launch/setLayer", (params) => {
    expect(params).toEqual({ cwd: "/", layer: "global", config: { mcpConfigs: ["/etc/mcp.json"], mcps: [] } });
    return { effective: {}, layers: {}, provenance: {} };
  });
  fake.on("evener/paths/complete", () => ({ data: [] }));
  render(<McpSection useOverviewStore={overviewHook()} />);
  await launchLayerLoaded();
  await user.click(screen.getByRole("button", { name: /^New config file:/ }));
  await screen.findByRole("combobox", { name: "Path" });
  await user.keyboard("/etc/mcp.json");
  await user.keyboard("{Enter}");
  const addButtons = screen.getAllByRole("button", { name: "Add" });
  await user.click(addButtons[0]!);
  expect(await screen.findByText("/etc/mcp.json")).toBeTruthy();
});

// Config files name FILES, so their picker lists them - clicking one fills the
// add field outright rather than only navigating toward its directory.
test("the config-file add row browses files, and picking one fills the add field", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({ mcpConfigs: [], mcps: [] }));
  fake.on("evener/paths/complete", (params) => {
    expect(params).toEqual({ prefix: "", includeFiles: true });
    return { data: ["/etc/mcp/", "/etc/mcp.json"] };
  });
  render(<McpSection useOverviewStore={overviewHook()} />);
  await launchLayerLoaded();
  await user.click(screen.getByRole("button", { name: /^New config file:/ }));
  await user.click(await screen.findByRole("option", { name: /mcp\.json/ }));
  await waitFor(() =>
    expect(screen.getByRole("button", { name: /^New config file:/ }).textContent).toMatch(/\/etc\/mcp\.json/),
  );
});

test("removing a config file confirms first, then saves with it filtered out", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({ mcpConfigs: ["/etc/mcp.json"], mcps: [] }));
  fake.on("evener/launch/setLayer", (params) => {
    expect(params).toEqual({ cwd: "/", layer: "global", config: { mcpConfigs: [], mcps: [] } });
    return { effective: {}, layers: {}, provenance: {} };
  });
  render(<McpSection useOverviewStore={overviewHook()} />);
  await screen.findByText("/etc/mcp.json");
  await user.click(screen.getByRole("button", { name: "Remove /etc/mcp.json" }));
  expect(screen.getByRole("dialog", { name: "Remove config file" })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await waitFor(() => expect(screen.queryByText("/etc/mcp.json")).toBeNull());
});

// The inline-server add field takes "name command [args...]" and submits
// through the Add button of its own form (the config-file list has another).
async function addInlineServer(user: ReturnType<typeof userEvent.setup>, entry: string): Promise<void> {
  const field = screen.getByRole("textbox", { name: "Inline MCP servers" });
  await user.type(field, entry);
  const form = field.closest("form");
  if (form === null) throw new Error("the inline-server add form did not render");
  await user.click(within(form).getByRole("button", { name: "Add" }));
}

test("an inline server entry without a command is refused before validating", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({ mcpConfigs: [], mcps: [] }));
  const validateSpy = vi.fn();
  fake.on("evener/path/validate", validateSpy);
  render(<McpSection useOverviewStore={overviewHook()} />);
  await launchLayerLoaded();
  await addInlineServer(user, "search");
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(validateSpy).not.toHaveBeenCalled();
});

test("renders inline MCP servers as '{name} → {command} {args}'", async () => {
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({
    mcpConfigs: [],
    mcps: [{ name: "search", command: "/usr/bin/search-mcp", args: ["--port", "9000"] }],
  }));
  render(<McpSection useOverviewStore={overviewHook()} />);
  expect(await screen.findByText("search → /usr/bin/search-mcp --port 9000")).toBeTruthy();
});

test("adding an inline server validates the command as kind:command and saves mcps", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({ mcpConfigs: [], mcps: [] }));
  fake.on("evener/path/validate", (params) => {
    expect(params).toEqual({ path: "/usr/bin/search-mcp", kind: "command" });
    return { path: "/usr/bin/search-mcp", valid: true };
  });
  fake.on("evener/launch/setLayer", (params) => {
    expect(params).toEqual({
      cwd: "/",
      layer: "global",
      config: { mcpConfigs: [], mcps: [{ name: "search", command: "/usr/bin/search-mcp", args: ["--port", "9000"] }] },
    });
    return { effective: {}, layers: {}, provenance: {} };
  });
  render(<McpSection useOverviewStore={overviewHook()} />);
  await launchLayerLoaded();
  await addInlineServer(user, "search /usr/bin/search-mcp --port 9000");
  expect(await screen.findByText("search → /usr/bin/search-mcp --port 9000")).toBeTruthy();
});

test("an invalid command shows the inline error and does not save", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({ mcpConfigs: [], mcps: [] }));
  fake.on("evener/path/validate", () => ({ path: "nope", valid: false, error: "validator-refusal" }));
  const setLayerSpy = vi.fn();
  fake.on("evener/launch/setLayer", setLayerSpy);
  render(<McpSection useOverviewStore={overviewHook()} />);
  await launchLayerLoaded();
  await addInlineServer(user, "search nope");
  expect(await screen.findByText("validator-refusal")).toBeTruthy();
  expect(setLayerSpy).not.toHaveBeenCalled();
});

test("removing an inline server confirms first, then saves with it filtered out", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({
    mcpConfigs: [],
    mcps: [{ name: "search", command: "/usr/bin/search-mcp", args: [] }],
  }));
  fake.on("evener/launch/setLayer", (params) => {
    expect(params).toEqual({ cwd: "/", layer: "global", config: { mcpConfigs: [], mcps: [] } });
    return { effective: {}, layers: {}, provenance: {} };
  });
  render(<McpSection useOverviewStore={overviewHook()} />);
  await screen.findByText("search → /usr/bin/search-mcp");
  await user.click(screen.getByRole("button", { name: "Remove search" }));
  expect(screen.getByRole("dialog", { name: "Remove MCP server" })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await waitFor(() => expect(screen.queryByText("search → /usr/bin/search-mcp")).toBeNull());
});

test("the remove-server confirm's buttons disable while the save is in flight", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({
    mcpConfigs: [],
    mcps: [{ name: "search", command: "/usr/bin/search-mcp", args: [] }],
  }));
  fake.on("evener/launch/setLayer", () => new Promise(() => {})); // never resolves - just observe the mid-flight state
  render(<McpSection useOverviewStore={overviewHook()} />);
  await screen.findByText("search → /usr/bin/search-mcp");
  await user.click(screen.getByRole("button", { name: "Remove search" }));
  const dialog = screen.getByRole("dialog", { name: "Remove MCP server" });
  await user.click(within(dialog).getByRole("button", { name: "Remove" }));
  expect((within(dialog).getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true);
  expect((within(dialog).getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(true);
});

test("a failed save while removing a config file toasts failure", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({ mcpConfigs: ["/etc/mcp.json"], mcps: [] }));
  fake.on("evener/launch/setLayer", () => {
    throw new Error("disk full");
  });
  render(<McpSection useOverviewStore={overviewHook()} />);
  await screen.findByText("/etc/mcp.json");
  await user.click(screen.getByRole("button", { name: "Remove /etc/mcp.json" }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await waitFor(() =>
    expect(getToasts().some((t) => t.kind === "error" && t.text.includes("Remove failed:"))).toBe(true),
  );
  // Assert raw JS error text no longer appears in toast
  expect(getToasts().some((t) => t.text.includes("disk full"))).toBe(false);
});

test("adding a config file with invalid path shows error without saving", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({ mcpConfigs: [], mcps: [] }));
  fake.on("evener/path/validate", () => ({ path: "", valid: false, error: "validator-refusal" }));
  const setLayerSpy = vi.fn();
  fake.on("evener/launch/setLayer", setLayerSpy);
  fake.on("evener/paths/complete", () => ({ data: [] }));
  render(<McpSection useOverviewStore={overviewHook()} />);
  await launchLayerLoaded();
  await user.click(screen.getByRole("button", { name: /^New config file:/ }));
  await user.keyboard("/bad/path");
  await user.keyboard("{Enter}");
  const addButtons = screen.getAllByRole("button", { name: "Add" });
  await user.click(addButtons[0]!);
  expect(await screen.findByText("validator-refusal")).toBeTruthy();
  expect(setLayerSpy).not.toHaveBeenCalled();
});

test("adding a config file when setLayer fails returns error to PathListEditor", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({ mcpConfigs: [], mcps: [] }));
  fake.on("evener/path/validate", () => ({ path: "/etc/mcp.json", valid: true }));
  fake.on("evener/launch/setLayer", () => {
    throw new WireError("set-layer-failure", -1);
  });
  fake.on("evener/paths/complete", () => ({ data: [] }));
  render(<McpSection useOverviewStore={overviewHook()} />);
  await launchLayerLoaded();
  await user.click(screen.getByRole("button", { name: /^New config file:/ }));
  await user.keyboard("/etc/mcp.json");
  await user.keyboard("{Enter}");
  const addButtons = screen.getAllByRole("button", { name: "Add" });
  await user.click(addButtons[0]!);
  // The add fails — the error text should appear (friendlyErrorMessage converts WireError)
  await screen.findByText("set-layer-failure");
});

test("adding an inline server when setLayer fails shows error message", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({ mcpConfigs: [], mcps: [] }));
  fake.on("evener/path/validate", () => ({ path: "/usr/bin/srv", valid: true }));
  fake.on("evener/launch/setLayer", () => {
    throw new WireError("persist failed", -1);
  });
  render(<McpSection useOverviewStore={overviewHook()} />);
  await launchLayerLoaded();
  await addInlineServer(user, "srv /usr/bin/srv");
  expect(await screen.findByText("persist failed")).toBeTruthy();
});

test("removing an inline server when setLayer fails shows error toast", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({
    mcpConfigs: [],
    mcps: [{ name: "srv", command: "/usr/bin/srv", args: [] }],
  }));
  fake.on("evener/launch/setLayer", () => {
    throw new WireError("remove denied", -1);
  });
  render(<McpSection useOverviewStore={overviewHook()} />);
  await screen.findByText("srv → /usr/bin/srv");
  await user.click(screen.getByRole("button", { name: "Remove srv" }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await waitFor(() => expect(getToasts().map((toast) => toast.text)).toContain("Remove failed: remove denied"));
});

test("cancelling the remove server dialog does not call setLayer", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({
    mcpConfigs: [],
    mcps: [{ name: "srv", command: "/usr/bin/srv", args: [] }],
  }));
  const setLayerSpy = vi.fn();
  fake.on("evener/launch/setLayer", setLayerSpy);
  render(<McpSection useOverviewStore={overviewHook()} />);
  await screen.findByText("srv → /usr/bin/srv");
  await user.click(screen.getByRole("button", { name: "Remove srv" }));
  expect(screen.getByRole("dialog", { name: "Remove MCP server" })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(setLayerSpy).not.toHaveBeenCalled();
});

// The discovered-servers block reads evener/settings/overview, which is this
// hub's own read (settingsOverview.ts) and is NOT on the proxy allow-list - so
// it can only ever describe the controller's hub. Under a remote selection it
// must not be presented as the host's probe.
test("under a remote host the discovered-servers block is not shown, and this hub's probe is not fetched", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { host: string; method: string };
    if (forwarded.method === "evener/launch/getLayer") return { mcps: [] } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });
  const fetchFn = vi.fn(async () => {});
  const data: SettingsOverviewResponse = {
    mcpDiscovered: { servers: [{ name: "controller-tool", transport: "stdio", status: "available" }] },
  };
  render(<McpSection host="beta" useOverviewStore={overviewHook({ data, fetch: fetchFn })} />);
  await screen.findByRole("textbox", { name: "Inline MCP servers" });

  expect(discoveredBlock()).toBeNull();
  expect(screen.queryByText(/controller-tool/)).toBeNull();
  expect(fetchFn).not.toHaveBeenCalled();
});

test("the local hub still renders the discovered-servers block", async () => {
  const fake = connectFakeClient();
  fake.on("evener/launch/getLayer", () => ({ mcps: [] }));
  const data: SettingsOverviewResponse = {
    mcpDiscovered: { servers: [{ name: "local-tool", transport: "stdio", status: "available" }] },
  };
  render(<McpSection useOverviewStore={overviewHook({ data })} />);

  expect(discoveredBlock()).not.toBeNull();
  expect(screen.getByText(/local-tool/)).toBeTruthy();
  // The mount's launch-layer read lands before the test ends, not in the next.
  await launchLayerLoaded();
});

test("an unsaved inline-server entry survives a failed refresh and the recovery after it", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let failRefresh = false;
  fake.on("evener/launch/getLayer", () => {
    if (failRefresh) throw new WireError("refresh failed", -1);
    return { mcpConfigs: [], mcps: [] };
  });
  render(<McpSection useOverviewStore={overviewHook()} />);
  const field = await screen.findByRole("textbox", { name: "Inline MCP servers" });
  await user.type(field, "fs /usr/bin/fs-mcp");

  failRefresh = true;
  await act(() => extensionsStore.getState().fetchLaunchLayer());
  expect(await screen.findByRole("alert")).toBeTruthy();
  failRefresh = false;
  await act(() => extensionsStore.getState().fetchLaunchLayer());

  expect(screen.getByRole("textbox", { name: "Inline MCP servers" })).toHaveProperty("value", "fs /usr/bin/fs-mcp");
});

// Editing a layer that has not loaded yet would save a layer holding only the
// new entry, so the editors wait for the first load, however late the
// connection becomes ready.
test("the inline-server editor waits for the launch layer's first load", async () => {
  render(<McpSection useOverviewStore={overviewHook()} />);
  expect(screen.queryByRole("textbox", { name: "Inline MCP servers" })).toBeNull();
  const fake = new FakeClient("ready");
  fake.on("evener/launch/getLayer", () => ({ mcpConfigs: [], mcps: [] }));
  act(() => connectionStore.getState().connect(fake));
  expect(await screen.findByRole("textbox", { name: "Inline MCP servers" })).toBeTruthy();
});

// A failed refresh leaves the last good layer on screen, possibly stale, so
// nothing saves until a refresh succeeds: a write from that snapshot could
// overwrite edits made to the file elsewhere. The typed entry stays.
test("while a refresh has failed, saves are refused and keep the typed entry", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let failReads = false;
  fake.on("evener/launch/getLayer", () => {
    if (failReads) throw new WireError("read failed", -1);
    return { mcpConfigs: [], mcps: [] };
  });
  fake.on("evener/path/validate", () => ({ path: "/usr/bin/fs-mcp", valid: true }));
  const setLayerSpy = vi.fn();
  fake.on("evener/launch/setLayer", setLayerSpy);
  render(<McpSection useOverviewStore={overviewHook()} />);
  const field = await screen.findByRole("textbox", { name: "Inline MCP servers" });

  failReads = true;
  await act(() => extensionsStore.getState().fetchLaunchLayer());
  await addInlineServer(user, "fs /usr/bin/fs-mcp");

  expect(await screen.findAllByRole("alert")).not.toHaveLength(0);
  expect(setLayerSpy).not.toHaveBeenCalled();
  expect(field).toHaveProperty("value", "fs /usr/bin/fs-mcp");
});

test("removing one of two identical servers removes the one confirmed", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  const a = { name: "a", command: "/bin/a", args: [] };
  const b = { name: "b", command: "/bin/b", args: [] };
  // Distinct objects, as the wire decodes them: identical entries are equal,
  // never the same object.
  fake.on("evener/launch/getLayer", () => ({ mcpConfigs: [], mcps: [{ ...a }, { ...b }, { ...a }] }));
  const setLayerSpy = vi.fn(() => ({ effective: {}, layers: {}, provenance: {} }));
  fake.on("evener/launch/setLayer", setLayerSpy);
  render(<McpSection useOverviewStore={overviewHook()} />);
  const list = await screen.findByRole("list", { name: "Inline MCP servers" });
  const removes = within(list).getAllByRole("button");
  const last = removes[removes.length - 1];
  if (last === undefined) throw new Error("no rows rendered");
  await user.click(last);
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
  await waitFor(() => expect(setLayerSpy).toHaveBeenCalledTimes(1));
  const [params] = setLayerSpy.mock.calls[0] as unknown as [{ config: { mcps: unknown[] } }];
  expect(params.config.mcps).toEqual([a, b]);
});

test("saves stay refused while a recovery refresh is still in flight, and resume on its fresh layer", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  let mode: "ok" | "fail" | "hold" = "ok";
  let release: (layer: Record<string, unknown>) => void = () => {};
  fake.on("evener/launch/getLayer", () => {
    if (mode === "fail") throw new WireError("read failed", -1);
    if (mode === "hold") return new Promise((resolve) => (release = resolve));
    return { mcpConfigs: [], mcps: [] };
  });
  fake.on("evener/path/validate", () => ({ path: "/usr/bin/fs-mcp", valid: true }));
  const setLayerSpy = vi.fn(() => ({ effective: {}, layers: {}, provenance: {} }));
  fake.on("evener/launch/setLayer", setLayerSpy);
  render(<McpSection useOverviewStore={overviewHook()} />);
  await screen.findByRole("textbox", { name: "Inline MCP servers" });

  mode = "fail";
  await act(() => extensionsStore.getState().fetchLaunchLayer());
  mode = "hold";
  let recovery: Promise<void> = Promise.resolve();
  act(() => {
    recovery = extensionsStore.getState().fetchLaunchLayer();
  });
  await addInlineServer(user, "fs /usr/bin/fs-mcp");
  expect(await screen.findAllByRole("alert")).not.toHaveLength(0);
  expect(setLayerSpy).not.toHaveBeenCalled();

  await act(async () => {
    release({ mcpConfigs: [], mcps: [], skillsDirs: ["/skills/changed-elsewhere"] });
    await recovery;
  });
  await user.click(
    within(screen.getByRole("textbox", { name: "Inline MCP servers" }).closest("form") as HTMLElement).getByRole(
      "button",
      { name: "Add" },
    ),
  );
  await waitFor(() => expect(setLayerSpy).toHaveBeenCalledTimes(1));
  const [params] = setLayerSpy.mock.calls[0] as unknown as [{ config: Record<string, unknown> }];
  expect(params.config.skillsDirs).toEqual(["/skills/changed-elsewhere"]);
});
