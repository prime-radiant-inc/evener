import type { HostRow, LaunchOptionSchemaResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { hostsStore } from "../../../stores/hosts";
import { resetLaunchConfigHostStoresForTests, resetLaunchConfigStoreForTests } from "../../../stores/launchConfig";
import { resetSettingsHostForTests, setSettingsHost } from "../../../stores/settingsHost";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { LaunchServerHostScope } from "./launchServer";

// The launch-evener settings surface's host scope (component 07b): the shared
// HostPicker over the selected host, whose own launch config both shows and
// changes from here. Local is today's plain-call section; a remote selection
// routes every launch call through evener/host/request and never touches the
// controller's launch config.

function hostRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return { origin: "sidecar", attached: false, midAttach: false, removed: false, ...overrides };
}

function schema(description: string): LaunchOptionSchemaResponse {
  return {
    options: [
      {
        field: "agent",
        wireField: "agent",
        label: "Agent",
        group: "Agent",
        kind: "text",
        perLaunch: true,
        defaultableLayers: ["global", "project"],
        description,
      },
    ],
  };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function controllerLaunchCalls(fake: FakeClient): string[] {
  return fake.calls.map((call) => call.method).filter((method) => method.startsWith("evener/launch/"));
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetLaunchConfigStoreForTests();
  resetLaunchConfigHostStoresForTests();
  resetToastStoreForTests();
  hostsStore.getState().resetForTests();
  resetSettingsHostForTests();
  window.history.pushState({}, "", "/settings/launch-evener");
  setSettingsHost("local");
});

afterEach(() => {
  cleanup();
  resetLaunchConfigHostStoresForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  window.history.pushState({}, "", "/");
});

test("with this hub selected the section issues the plain launch calls and never the proxy", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/schema", () => schema("controller schema"));
  fake.on("evener/launch/getLayer", () => ({ agent: "controller-agent" }));
  fake.on("evener/launch/resolve", () => ({ effective: { agent: "controller-agent" }, layers: {}, provenance: {} }));
  fake.on("evener/host/request", () => {
    throw new Error("the local hub must never route through the proxy");
  });

  render(<LaunchServerHostScope sectionId="launch-evener" />);

  await screen.findByLabelText("Agent");
  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("controller-agent");
  expect(fake.calls.some((call) => call.method === "evener/host/request")).toBe(false);
  expect(controllerLaunchCalls(fake)).toContain("evener/launch/schema");
});

test("selecting a remote host shows THAT host's own launch defaults, never the controller's", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  // The controller's own handlers are booby-trapped: any of them being reached
  // while beta is selected is a host-scoping bug.
  fake.on("evener/launch/schema", () => {
    throw new Error("a remote selection must not read this hub's launch schema");
  });
  fake.on("evener/launch/getLayer", () => {
    throw new Error("a remote selection must not read this hub's launch layer");
  });
  fake.on("evener/launch/resolve", () => {
    throw new Error("a remote selection must not resolve against this hub");
  });
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { host: string; method: string };
    expect(forwarded.host).toBe("beta");
    if (forwarded.method === "evener/launch/schema") return schema("beta schema") as never;
    if (forwarded.method === "evener/launch/getLayer") return { agent: "beta-agent" } as never;
    if (forwarded.method === "evener/launch/resolve")
      return { effective: { agent: "beta-agent" }, layers: {}, provenance: {} } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  render(<LaunchServerHostScope sectionId="launch-evener" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  // Let the local mount's own load settle, then watch only what the remote
  // selection issues: the local calls before the switch are this hub's own.
  await waitFor(() => expect(fake.calls.length).toBeGreaterThan(0));
  fake.calls.length = 0;

  await user.selectOptions(select, "beta");

  await waitFor(() => expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("beta-agent"));
  expect(controllerLaunchCalls(fake)).toEqual([]);
});

test("a remote save writes that host's layer through the proxy, never this hub's setLayer", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/setLayer", () => {
    throw new Error("a remote selection must not write this hub's launch layer");
  });
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string };
    if (forwarded.method === "evener/launch/schema") return schema("beta schema") as never;
    if (forwarded.method === "evener/launch/getLayer") return { agent: "beta-agent" } as never;
    if (forwarded.method === "evener/launch/resolve")
      return { effective: { agent: "beta-agent" }, layers: {}, provenance: {} } as never;
    if (forwarded.method === "evener/launch/setLayer")
      return { effective: { agent: "beta-agent" }, layers: {}, provenance: {} } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<LaunchServerHostScope sectionId="launch-evener" />);
  const user = userEvent.setup();
  await screen.findByLabelText("Agent");

  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));

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

test("a host that is no longer configured says so instead of falling back to this hub", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/schema", () => {
    throw new Error("a remote selection must not read this hub's launch schema");
  });
  fake.on("evener/host/request", () => schema("beta schema") as never);

  setSettingsHost("beta");
  render(<LaunchServerHostScope sectionId="launch-evener" />);
  await screen.findByLabelText("Agent");

  // Beta disappears from the registry (removed from another client).
  act(() => hostsStore.setState({ load: { phase: "ready", hosts: [] } }));

  expect(await screen.findByText(/is no longer configured/)).toBeTruthy();
});
