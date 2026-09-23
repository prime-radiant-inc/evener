import type { HostRow } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { resetExtensionsStoreForTests } from "../../../stores/extensions";
import { hostsStore } from "../../../stores/hosts";
import { resetSettingsHostForTests, setSettingsHost } from "../../../stores/settingsHost";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { McpSectionHostScope, type SettingsOverviewLike } from "./mcp";

// The MCP settings surface's host scope (component 07b): the shared HostPicker
// over the selected host, whose own global launch layer both shows and changes
// from here. This section edits two fields of that layer (mcpConfigs, mcps) -
// the same layer the launch-evener, plugins-dirs and skills-dirs sections edit
// - so it is host-scoped for the same reason they are. Local is today's
// plain-call section; a remote selection routes every read and write through
// evener/host/request and never touches the controller's launch layer.

function hostRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return { origin: "sidecar", attached: false, midAttach: false, removed: false, ...overrides };
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

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

/** The calls this section makes about the launch layer and the path helpers it
 * uses to validate an entry - the controller's own, i.e. NOT via the proxy. */
function controllerLayerCalls(fake: FakeClient): string[] {
  return fake.calls
    .map((call) => call.method)
    .filter((method) => method.startsWith("evener/launch/") || method.startsWith("evener/path"));
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
  resetToastStoreForTests();
  hostsStore.getState().resetForTests();
  resetSettingsHostForTests();
  window.history.pushState({}, "", "/settings/mcp");
  setSettingsHost("local");
});

afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  window.history.pushState({}, "", "/");
});

test("with this hub selected the section issues the plain MCP calls and never the proxy", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/getLayer", () => ({ mcpConfigs: ["/controller/mcp.json"], mcps: [] }));
  fake.on("evener/host/request", () => {
    throw new Error("the local hub must never route through the proxy");
  });

  render(<McpSectionHostScope useOverviewStore={overviewHook()} />);

  await screen.findByText("/controller/mcp.json");
  expect(fake.calls.some((call) => call.method === "evener/host/request")).toBe(false);
  expect(controllerLayerCalls(fake)).toContain("evener/launch/getLayer");
});

test("a remote selection shows THAT host's own MCP lists, never this hub's", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  // The controller's own handlers are booby-trapped: reaching any of them while
  // beta is selected is a host-scoping bug.
  fake.on("evener/launch/getLayer", () => {
    throw new Error("a remote selection must not read this hub's launch layer");
  });
  fake.on("evener/paths/complete", () => {
    throw new Error("a remote selection must not complete paths against this hub");
  });
  fake.on("evener/path/validate", () => {
    throw new Error("a remote selection must not validate paths against this hub");
  });
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { host: string; method: string };
    expect(forwarded.host).toBe("beta");
    if (forwarded.method === "evener/launch/getLayer") {
      return { mcpConfigs: ["/beta/mcp.json"], mcps: [] } as never;
    }
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  render(<McpSectionHostScope useOverviewStore={overviewHook()} />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  // Let the local mount's own load settle, then watch only what the remote
  // selection issues: the local calls before the switch are this hub's own.
  await waitFor(() => expect(fake.calls.length).toBeGreaterThan(0));
  fake.calls.length = 0;

  await user.selectOptions(select, "beta");

  await screen.findByText("/beta/mcp.json");
  expect(controllerLayerCalls(fake)).toEqual([]);
});

test("a remote MCP write goes through the proxy, never this hub's setLaunchLayer", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/setLayer", () => {
    throw new Error("a remote selection must not write this hub's launch layer");
  });
  fake.on("evener/path/validate", () => {
    throw new Error("a remote selection must not validate against this hub");
  });
  fake.on("evener/paths/complete", () => {
    throw new Error("a remote selection must not complete against this hub");
  });
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string };
    if (forwarded.method === "evener/launch/getLayer") return { mcps: [] } as never;
    if (forwarded.method === "evener/path/validate") return { valid: true, path: "/beta/server" } as never;
    if (forwarded.method === "evener/launch/setLayer") return {} as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<McpSectionHostScope useOverviewStore={overviewHook()} />);
  const user = userEvent.setup();
  await screen.findByPlaceholderText("name");

  await user.type(screen.getByPlaceholderText("name"), "beta-tool");
  const commandField = screen.getByPlaceholderText("command");
  await user.type(commandField, "beta-server");
  // Two Add buttons render (the config-file list's and the inline-server
  // form's), so scope the submit to the form that owns the command field.
  const form = commandField.closest("form");
  if (form === null) throw new Error("the inline-server form did not render");
  await user.click(within(form).getByRole("button", { name: "Add" }));

  await waitFor(() =>
    expect(
      fake.calls.some(
        (call) =>
          call.method === "evener/host/request" &&
          (call.params as { method: string }).method === "evener/launch/setLayer",
      ),
    ).toBe(true),
  );
  expect(fake.calls.some((call) => call.method === "evener/launch/setLayer")).toBe(false);
});
