import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { SettingsOverviewResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetExtensionsStoreForTests } from "../../../stores/extensions";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { McpSection, type SettingsOverviewLike } from "./mcp";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
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

test("calls the injected overview hook's fetch on mount", () => {
  connectFakeClient();
  const fetchFn = vi.fn(async () => {});
  render(<McpSection useOverviewStore={overviewHook({ fetch: fetchFn })} />);
  expect(fetchFn).toHaveBeenCalledOnce();
});

test("renders discovered servers from the injected overview data with a status per row", () => {
  connectFakeClient();
  const data: SettingsOverviewResponse = {
    mcpDiscovered: {
      servers: [
        { name: "local-tool", transport: "stdio", status: "available" },
        { name: "remote-api", transport: "http", status: "unreachable", error: "connection refused" },
      ],
    },
  };
  render(<McpSection useOverviewStore={overviewHook({ data })} />);
  expect(screen.getByText(/local-tool/)).toBeTruthy();
  expect(screen.getByText("available")).toBeTruthy();
  expect(screen.getByText(/remote-api/)).toBeTruthy();
  expect(screen.getByText("unreachable")).toBeTruthy();
  expect(screen.getByText("connection refused")).toBeTruthy();
});

test("shows the discovered-servers empty state", () => {
  connectFakeClient();
  render(<McpSection useOverviewStore={overviewHook({ data: { mcpDiscovered: { servers: [] } } })} />);
  expect(screen.getByText("No MCP servers configured.")).toBeTruthy();
});

test("shows a loading state for discovered servers before the overview resolves", () => {
  connectFakeClient();
  render(<McpSection useOverviewStore={overviewHook({ loading: true, data: null })} />);
  expect(screen.getAllByRole("status", { name: "Loading" }).length).toBeGreaterThan(0);
});

test("a top-level probe failure replaces the discovered-servers list with one message", () => {
  connectFakeClient();
  render(<McpSection useOverviewStore={overviewHook({ data: { mcpDiscovered: { error: "probe timed out" } } })} />);
  expect(screen.getByText(/Failed to load: probe timed out/)).toBeTruthy();
});

test("renders the mcpConfigs entries from the launch layer", async () => {
  const fake = connectFakeClient();
  fake.on("serf/launch/getLayer", () => ({ mcpConfigs: ["/etc/mcp.json"], mcps: [] }));
  render(<McpSection useOverviewStore={overviewHook()} />);
  expect(await screen.findByText("/etc/mcp.json")).toBeTruthy();
});

test("adding a config file validates as kind:file and saves mcpConfigs", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/launch/getLayer", () => ({ mcpConfigs: [], mcps: [] }));
  fake.on("serf/path/validate", (params) => {
    expect(params).toEqual({ path: "/etc/mcp.json", kind: "file" });
    return { path: "/etc/mcp.json", valid: true };
  });
  fake.on("serf/launch/setLayer", (params) => {
    expect(params).toEqual({ cwd: "/", layer: "global", config: { mcpConfigs: ["/etc/mcp.json"], mcps: [] } });
    return { effective: {}, layers: {}, provenance: {} };
  });
  render(<McpSection useOverviewStore={overviewHook()} />);
  await screen.findByText("No MCP config files. Add one below.");
  await user.type(screen.getByPlaceholderText("/absolute/path/to/mcp.json"), "/etc/mcp.json");
  const addButtons = screen.getAllByRole("button", { name: "Add" });
  await user.click(addButtons[0]!);
  expect(await screen.findByText("/etc/mcp.json")).toBeTruthy();
});

test("removing a config file confirms first, then saves with it filtered out", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/launch/getLayer", () => ({ mcpConfigs: ["/etc/mcp.json"], mcps: [] }));
  fake.on("serf/launch/setLayer", (params) => {
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

test("renders inline MCP servers as '{name} → {command} {args}'", async () => {
  const fake = connectFakeClient();
  fake.on("serf/launch/getLayer", () => ({
    mcpConfigs: [],
    mcps: [{ name: "search", command: "/usr/bin/search-mcp", args: ["--port", "9000"] }],
  }));
  render(<McpSection useOverviewStore={overviewHook()} />);
  expect(await screen.findByText("search → /usr/bin/search-mcp --port 9000")).toBeTruthy();
});

test("adding an inline server validates the command as kind:command and saves mcps", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/launch/getLayer", () => ({ mcpConfigs: [], mcps: [] }));
  fake.on("serf/path/validate", (params) => {
    expect(params).toEqual({ path: "/usr/bin/search-mcp", kind: "command" });
    return { path: "/usr/bin/search-mcp", valid: true };
  });
  fake.on("serf/launch/setLayer", (params) => {
    expect(params).toEqual({
      cwd: "/",
      layer: "global",
      config: { mcpConfigs: [], mcps: [{ name: "search", command: "/usr/bin/search-mcp", args: ["--port", "9000"] }] },
    });
    return { effective: {}, layers: {}, provenance: {} };
  });
  render(<McpSection useOverviewStore={overviewHook()} />);
  await screen.findByText("No inline MCP servers. Add one below.");
  await user.type(screen.getByPlaceholderText("name"), "search");
  await user.type(screen.getByPlaceholderText("command"), "/usr/bin/search-mcp");
  await user.type(screen.getByPlaceholderText("args (space-separated)"), "--port 9000");
  const addButtons = screen.getAllByRole("button", { name: "Add" });
  await user.click(addButtons[addButtons.length - 1]!);
  expect(await screen.findByText("search → /usr/bin/search-mcp --port 9000")).toBeTruthy();
});

test("an invalid command shows the inline error and does not save", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/launch/getLayer", () => ({ mcpConfigs: [], mcps: [] }));
  fake.on("serf/path/validate", () => ({ path: "nope", valid: false, error: "command not found" }));
  const setLayerSpy = vi.fn();
  fake.on("serf/launch/setLayer", setLayerSpy);
  render(<McpSection useOverviewStore={overviewHook()} />);
  await screen.findByText("No inline MCP servers. Add one below.");
  await user.type(screen.getByPlaceholderText("name"), "search");
  await user.type(screen.getByPlaceholderText("command"), "nope");
  const addButtons = screen.getAllByRole("button", { name: "Add" });
  await user.click(addButtons[addButtons.length - 1]!);
  expect(await screen.findByText("command not found")).toBeTruthy();
  expect(setLayerSpy).not.toHaveBeenCalled();
});

test("removing an inline server confirms first, then saves with it filtered out", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/launch/getLayer", () => ({
    mcpConfigs: [],
    mcps: [{ name: "search", command: "/usr/bin/search-mcp", args: [] }],
  }));
  fake.on("serf/launch/setLayer", (params) => {
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

test("a failed save while removing a config file toasts failure", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("serf/launch/getLayer", () => ({ mcpConfigs: ["/etc/mcp.json"], mcps: [] }));
  fake.on("serf/launch/setLayer", () => {
    throw new Error("disk full");
  });
  render(<McpSection useOverviewStore={overviewHook()} />);
  await screen.findByText("/etc/mcp.json");
  await user.click(screen.getByRole("button", { name: "Remove /etc/mcp.json" }));
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await waitFor(() => expect(getToasts().some((t) => t.kind === "error" && t.text.includes("disk full"))).toBe(true));
});
