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

/** The launch-layer writes the pane issued against the SELECTED host through
 * the proxy. */
function hostSetLayerCalls(fake: FakeClient) {
  return fake.calls.filter(
    (call) =>
      call.method === "evener/host/request" && (call.params as { method: string }).method === "evener/launch/setLayer",
  );
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

// A host switch must not submit what the user typed for the host they left.
// The pane's own load clears and the form is re-seeded from the new host's
// layer, and the frame above it remounts the body outright - so the values that
// go out are the NEW host's, whichever mechanism is doing the work.
test("switching hosts writes the NEW host's values, never the previous host's unsaved draft", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/schema", () => schema("controller schema"));
  fake.on("evener/launch/getLayer", () => ({ agent: "controller-agent" }));
  fake.on("evener/launch/resolve", () => ({ effective: { agent: "controller-agent" }, layers: {}, provenance: {} }));
  fake.on("evener/launch/setLayer", () => ({ effective: {}, layers: {}, provenance: {} }));
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

  render(<LaunchServerHostScope sectionId="launch-evener" />);
  const user = userEvent.setup();
  const agent = (await screen.findByLabelText("Agent")) as HTMLInputElement;
  await user.clear(agent);
  await user.type(agent, "typed-on-this-hub");
  expect(agent.value).toBe("typed-on-this-hub");

  const select = screen.getByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await waitFor(() => expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("beta-agent"));

  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));

  await waitFor(() => expect(hostSetLayerCalls(fake)).toHaveLength(1));
  const write = hostSetLayerCalls(fake)[0];
  if (write === undefined) throw new Error("no launch-layer write reached the selected host");
  expect((write.params as { params: { config: unknown } }).params.config).toEqual({ agent: "beta-agent" });
  // And the draft the user left behind never reached this hub's own layer.
  expect(fake.calls.some((call) => call.method === "evener/launch/setLayer")).toBe(false);
});

/** The launch calls the pane forwarded to `host` through the proxy. */
function hostLaunchCalls(fake: FakeClient, host: string): string[] {
  return fake.calls
    .filter((call) => call.method === "evener/host/request" && (call.params as { host: string }).host === host)
    .map((call) => (call.params as { method: string }).method);
}

/** How many times the pane forwarded `method` to `host` through the proxy. */
function forwardedMethodCalls(fake: FakeClient, host: string, method: string): number {
  return hostLaunchCalls(fake, host).filter((forwarded) => forwarded === method).length;
}

/** A remote host's own evener/launch/updated, as the hub relays it: wrapped in
 * evener/host/notification and tagged with the host that owns it. */
function emitHostLaunchUpdated(fake: FakeClient, host: string): void {
  fake.emitNotification({
    method: "evener/host/notification",
    params: { host, method: "evener/launch/updated", params: {} },
  });
}

/** The controller's own evener/launch/updated - the local hub's plain
 * broadcast, delivered unwrapped. */
function emitLaunchUpdated(fake: FakeClient): void {
  fake.emitNotification({ method: "evener/launch/updated", params: { cwd: "/", layer: "global" } });
}

// The launch-config panes must CONVERGE when the host's own launch config
// changes under them. A burst of notifications must be bounded, a change for
// another host must not touch this pane, and a re-read must never silently take
// away what the user is typing.
test("a launch-config change for the SELECTED host re-reads the pane and shows the host's new value", async () => {
  const fake = connectFakeClient();
  let agent = "beta-agent";
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string };
    if (forwarded.method === "evener/launch/schema") return schema("beta schema") as never;
    if (forwarded.method === "evener/launch/getLayer") return { agent } as never;
    if (forwarded.method === "evener/launch/resolve")
      return { effective: { agent }, layers: {}, provenance: {} } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<LaunchServerHostScope sectionId="launch-evener" />);
  expect(((await screen.findByLabelText("Agent")) as HTMLInputElement).value).toBe("beta-agent");
  expect(forwardedMethodCalls(fake, "beta", "evener/launch/getLayer")).toBe(1);

  // Another client saves beta's global layer; the hub re-emits beta's own
  // evener/launch/updated, wrapped and tagged with beta.
  agent = "beta-agent-2";
  emitHostLaunchUpdated(fake, "beta");

  await waitFor(() => expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("beta-agent-2"));
  // Exactly ONE more layer read - the refresh - and it was the refresh path
  // (the form never blanked), not a load.
  expect(forwardedMethodCalls(fake, "beta", "evener/launch/getLayer")).toBe(2);
  expect(screen.queryByText(/Loading launch settings/)).toBeNull();
});

test("a launch-config change for a DIFFERENT host does not re-read the selected pane", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string };
    if (forwarded.method === "evener/launch/schema") return schema("beta schema") as never;
    if (forwarded.method === "evener/launch/getLayer") return { agent: "beta-agent" } as never;
    if (forwarded.method === "evener/launch/resolve")
      return { effective: { agent: "beta-agent" }, layers: {}, provenance: {} } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<LaunchServerHostScope sectionId="launch-evener" />);
  expect(((await screen.findByLabelText("Agent")) as HTMLInputElement).value).toBe("beta-agent");
  const reads = forwardedMethodCalls(fake, "beta", "evener/launch/getLayer");

  // gamma's own change must not move beta's pane.
  emitHostLaunchUpdated(fake, "gamma");
  await new Promise((resolve) => setTimeout(resolve, 400));

  expect(forwardedMethodCalls(fake, "beta", "evener/launch/getLayer")).toBe(reads);
  expect(forwardedMethodCalls(fake, "gamma", "evener/launch/getLayer")).toBe(0);
});

test("a BURST of launch-config changes for the selected host produces one bounded re-read", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string };
    if (forwarded.method === "evener/launch/schema") return schema("beta schema") as never;
    if (forwarded.method === "evener/launch/getLayer") return { agent: "beta-agent" } as never;
    if (forwarded.method === "evener/launch/resolve")
      return { effective: { agent: "beta-agent" }, layers: {}, provenance: {} } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<LaunchServerHostScope sectionId="launch-evener" />);
  await screen.findByLabelText("Agent");
  const reads = forwardedMethodCalls(fake, "beta", "evener/launch/getLayer");

  for (let i = 0; i < 5; i++) emitHostLaunchUpdated(fake, "beta");
  await new Promise((resolve) => setTimeout(resolve, 400));

  // Five notifications inside the 250ms debounce window coalesce into exactly
  // one layer read.
  expect(forwardedMethodCalls(fake, "beta", "evener/launch/getLayer")).toBe(reads + 1);
});

test("an incoming change keeps the user's typed draft and says the host's values changed", async () => {
  const fake = connectFakeClient();
  let agent = "beta-agent";
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string };
    if (forwarded.method === "evener/launch/schema") return schema("beta schema") as never;
    if (forwarded.method === "evener/launch/getLayer") return { agent } as never;
    if (forwarded.method === "evener/launch/resolve")
      return { effective: { agent }, layers: {}, provenance: {} } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<LaunchServerHostScope sectionId="launch-evener" />);
  const user = userEvent.setup();
  const agentInput = (await screen.findByLabelText("Agent")) as HTMLInputElement;
  await user.clear(agentInput);
  await user.type(agentInput, "typed-on-beta");

  agent = "beta-agent-2";
  emitHostLaunchUpdated(fake, "beta");

  // The re-read happened...
  await waitFor(() => expect(forwardedMethodCalls(fake, "beta", "evener/launch/getLayer")).toBe(2));
  // ...the draft the user typed is still theirs...
  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("typed-on-beta");
  // ...and the pane does not silently keep it: the newer host values are named
  // beside the form, with a way to take them.
  expect(screen.getByText(/changed on the host while you were editing/)).toBeTruthy();
});

test("with the local hub selected, a change to its launch config re-reads the pane", async () => {
  const fake = connectFakeClient();
  let agent = "controller-agent";
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/schema", () => schema("controller schema"));
  fake.on("evener/launch/getLayer", () => ({ agent }));
  fake.on("evener/launch/resolve", () => ({ effective: { agent }, layers: {}, provenance: {} }));

  render(<LaunchServerHostScope sectionId="launch-evener" />);
  expect(((await screen.findByLabelText("Agent")) as HTMLInputElement).value).toBe("controller-agent");
  // The pane's own read, counted by resolve: the extensions store (a singleton
  // subscribed to the same broadcast) also refetches getLayer on this
  // notification, and its read is not this pane's.
  const reads = controllerLaunchCalls(fake).filter((method) => method === "evener/launch/resolve").length;

  agent = "controller-agent-2";
  emitLaunchUpdated(fake);

  await waitFor(() => expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("controller-agent-2"));
  expect(controllerLaunchCalls(fake).filter((method) => method === "evener/launch/resolve").length).toBe(reads + 1);
});

test("with the local hub selected, a REPLACED controller connection re-reads the pane", async () => {
  const first = connectFakeClient();
  first.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  first.on("evener/launch/schema", () => schema("controller schema"));
  first.on("evener/launch/getLayer", () => ({ agent: "controller-agent" }));
  first.on("evener/launch/resolve", () => ({ effective: { agent: "controller-agent" }, layers: {}, provenance: {} }));

  render(<LaunchServerHostScope sectionId="launch-evener" />);
  expect(((await screen.findByLabelText("Agent")) as HTMLInputElement).value).toBe("controller-agent");

  // The browser wires a NEW client (a hub swap, a reconnect onto a fresh
  // socket). This hub's launch config may have moved while the old connection
  // was gone, and the plain broadcast reached nobody: the replacement itself is
  // the signal to re-read.
  const second = new FakeClient("ready");
  second.on("evener/launch/schema", () => schema("controller schema"));
  second.on("evener/launch/getLayer", () => ({ agent: "controller-agent-2" }));
  second.on("evener/launch/resolve", () => ({
    effective: { agent: "controller-agent-2" },
    layers: {},
    provenance: {},
  }));
  act(() => {
    connectionStore.getState().connect(second);
  });

  await waitFor(() => expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("controller-agent-2"));
  // Counted by resolve: the extensions store's recovery read is not this pane's.
  expect(second.calls.filter((call) => call.method === "evener/launch/resolve").length).toBe(1);
});

test("with the local hub selected, a RECOVERED controller connection re-reads the pane", async () => {
  const fake = connectFakeClient();
  let agent = "controller-agent";
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/launch/schema", () => schema("controller schema"));
  fake.on("evener/launch/getLayer", () => ({ agent }));
  fake.on("evener/launch/resolve", () => ({ effective: { agent }, layers: {}, provenance: {} }));

  render(<LaunchServerHostScope sectionId="launch-evener" />);
  expect(((await screen.findByLabelText("Agent")) as HTMLInputElement).value).toBe("controller-agent");
  const reads = controllerLaunchCalls(fake).filter((method) => method === "evener/launch/resolve").length;

  // The connection drops and comes back on the SAME client. A change made while
  // this browser was away was broadcast to every CONNECTED client, so this one
  // saw no notification: the recovery itself is the signal to re-read.
  agent = "controller-agent-2";
  act(() => connectionStore.setState({ state: "reconnecting" }));
  act(() => connectionStore.setState({ state: "ready" }));

  await waitFor(() => expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("controller-agent-2"));
  expect(controllerLaunchCalls(fake).filter((method) => method === "evener/launch/resolve").length).toBe(reads + 1);
});

// A remote host that is merely AWAY (unattached) refuses evener/host/request, so
// a pane that loaded in that window sits on its failure - the host's attachment
// is a live session field, deliberately not part of the registration identity, so
// nothing remounts or re-reads when it comes back. The registry's own answer is
// what reports the transition (there is no host lifecycle notification on the
// wire), and the pane re-issues its read on it.
test("a pane that failed while its host was away re-reads when the host attaches", async () => {
  const fake = connectFakeClient();
  let attached = false;
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached })] }));
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string };
    // The away refusal: the hub answers nothing for a host whose channel is down.
    if (!attached) throw new Error("host beta is not attached");
    if (forwarded.method === "evener/launch/schema") return schema("beta schema") as never;
    if (forwarded.method === "evener/launch/getLayer") return { agent: "beta-agent" } as never;
    if (forwarded.method === "evener/launch/resolve")
      return { effective: { agent: "beta-agent" }, layers: {}, provenance: {} } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<LaunchServerHostScope sectionId="launch-evener" />);
  expect(await screen.findByText(/Failed to load launch settings/)).toBeTruthy();

  // The host comes back: the registry's next answer reports it attached.
  attached = true;
  await act(() => hostsStore.getState().refresh());

  expect(await screen.findByLabelText("Agent")).toBeTruthy();
  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("beta-agent");
});

// The other half, and the reason this cannot be a remount: a reconnect is not a
// host switch. The data on screen is this host's own, so re-reading it must not
// discard what the user has typed into the form above it - the form is reseeded
// on the per-host store INSTANCE, and a re-attach is not a new instance.
test("a reconnect re-reads the host's data without discarding what the user typed", async () => {
  const fake = connectFakeClient();
  let attached = true;
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached })] }));
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string };
    if (!attached) throw new Error("host beta is not attached");
    if (forwarded.method === "evener/launch/schema") return schema("beta schema") as never;
    if (forwarded.method === "evener/launch/getLayer") return { agent: "beta-agent" } as never;
    if (forwarded.method === "evener/launch/resolve")
      return { effective: { agent: "beta-agent" }, layers: {}, provenance: {} } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<LaunchServerHostScope sectionId="launch-evener" />);
  const user = userEvent.setup();
  const agent = (await screen.findByLabelText("Agent")) as HTMLInputElement;
  expect(agent.value).toBe("beta-agent");
  await user.clear(agent);
  await user.type(agent, "typed-on-beta");
  const reads = hostLaunchCalls(fake, "beta").length;

  // Away, then back: the same host, the same registration, a new connection.
  attached = false;
  await act(() => hostsStore.getState().refresh());
  attached = true;
  await act(() => hostsStore.getState().refresh());

  // The pane re-read that host's own settings...
  await waitFor(() => expect(hostLaunchCalls(fake, "beta").length).toBeGreaterThan(reads));
  // ...and the draft the user typed is still theirs, not the freshly read value.
  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("typed-on-beta");
});

// A re-read of what this pane is already showing can fail: the host is attached
// (that is why we re-read it) and can still refuse the read. Keeping the form is
// right - the data on screen is this host's own and the draft in it is the
// user's - but keeping SILENT is not: if the host stays attached there is no next
// attach to wait for, and a pane sitting on values that may be out of date with
// no way to ask again is a dead end. So the failure is reported BESIDE the form,
// with a retry that re-reads that host.
test("a failed refresh keeps the form and the draft, reports itself beside it, and a retry clears it", async () => {
  const fake = connectFakeClient();
  let attached = true;
  let refuse = false;
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached })] }));
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string };
    if (refuse) throw new Error("the host refused the read");
    if (forwarded.method === "evener/launch/schema") return schema("beta schema") as never;
    if (forwarded.method === "evener/launch/getLayer") return { agent: "beta-agent" } as never;
    if (forwarded.method === "evener/launch/resolve")
      return { effective: { agent: "beta-agent" }, layers: {}, provenance: {} } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<LaunchServerHostScope sectionId="launch-evener" />);
  const user = userEvent.setup();
  const agent = (await screen.findByLabelText("Agent")) as HTMLInputElement;
  await user.clear(agent);
  await user.type(agent, "typed-on-beta");
  // A load that succeeded says nothing about failures.
  expect(screen.queryByText(/Could not re-read/)).toBeNull();

  // Away, then back: the re-read that follows fails.
  attached = false;
  await act(() => hostsStore.getState().refresh());
  refuse = true;
  attached = true;
  await act(() => hostsStore.getState().refresh());

  expect(await screen.findByText(/Could not re-read this host's launch settings/)).toBeTruthy();
  // The form, and the draft in it, are still here.
  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("typed-on-beta");

  refuse = false;
  await user.click(screen.getByRole("button", { name: "Retry" }));

  await waitFor(() => expect(screen.queryByText(/Could not re-read/)).toBeNull());
  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("typed-on-beta");
});

// The other dead end: a pane left on its LOAD failure has no form to keep - and
// until now no way to ask again either (the shape #2202 was flagged for on the
// credentials pane). Retry re-reads that host.
test("a pane left on its load failure offers a retry instead of a dead end", async () => {
  const fake = connectFakeClient();
  let refuse = true;
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", (params) => {
    const forwarded = params as { method: string };
    if (refuse) throw new Error("the host refused the read");
    if (forwarded.method === "evener/launch/schema") return schema("beta schema") as never;
    if (forwarded.method === "evener/launch/getLayer") return { agent: "beta-agent" } as never;
    if (forwarded.method === "evener/launch/resolve")
      return { effective: { agent: "beta-agent" }, layers: {}, provenance: {} } as never;
    throw new Error(`unexpected forwarded method ${forwarded.method}`);
  });

  setSettingsHost("beta");
  render(<LaunchServerHostScope sectionId="launch-evener" />);
  const user = userEvent.setup();
  expect(await screen.findByText(/Failed to load launch settings/)).toBeTruthy();

  refuse = false;
  await user.click(screen.getByRole("button", { name: "Retry" }));

  expect(await screen.findByLabelText("Agent")).toBeTruthy();
  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("beta-agent");
  expect(screen.queryByText(/Failed to load launch settings/)).toBeNull();
});
