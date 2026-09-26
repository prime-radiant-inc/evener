import {
  type HostForwardedResult,
  type HostRow,
  type InstanceEntry,
  type InstanceListResponse,
  WireError,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests, resetHostInstancesForTests } from "../../stores/credentials";
import { hostsStore } from "../../stores/hosts";
import { useProviderSetup } from "./useProviderSetup";

const provider: InstanceEntry = {
  name: "work",
  providerId: "openai",
  protocol: "openai-responses",
  auth: "bearer",
  implicit: false,
  isDefault: true,
  activeSource: "none",
  hasStoredOAuth: false,
  credentialRequired: true,
};

beforeEach(() => {
  connectionStore.setState({ state: "idle", client: null });
  resetCredentialsStoreForTests();
  resetHostInstancesForTests();
  hostsStore.getState().resetForTests();
});
afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", client: null });
});

function connect(instances: InstanceEntry[]) {
  const client = new FakeClient("ready");
  client.on("evener/instance/list", () => ({ instances, availableProviders: [] }));
  client.on("model/list", () => ({ data: [] }));
  connectionStore.getState().connect(client);
  return client;
}

test("requires setup only after the hub confirms missing credentials", async () => {
  const client = connect([]);
  let resolve: (value: { instances: InstanceEntry[]; availableProviders: [] }) => void = () => {};
  const response = new Promise<{ instances: InstanceEntry[]; availableProviders: [] }>((r) => {
    resolve = r;
  });
  client.on("evener/instance/list", () => response);
  const { result } = renderHook(useProviderSetup);
  expect(result.current.status).toBe("loading");
  await act(async () => resolve({ instances: [provider], availableProviders: [] }));
  expect(result.current.status).toBe("missing");
});

test.each([
  { activeSource: "store" },
  { activeSource: "env:API_KEY" },
  { activeSource: "oauth" },
  { auth: "none", credentialRequired: false },
  { auth: "optional-bearer", credentialRequired: false },
])("does not onboard a configured or keyless provider: %j", async (overrides) => {
  connect([{ ...provider, ...overrides }]);
  const { result } = renderHook(useProviderSetup);
  await waitFor(() => expect(result.current.status).toBe("ready"));
});

test("a hidden provider cannot satisfy setup", async () => {
  connect([{ ...provider, activeSource: "store", hidden: true }]);
  const { result } = renderHook(useProviderSetup);
  await waitFor(() => expect(result.current.status).toBe("missing"));
});

test.each(["none", "store", "env:OLLAMA_API_KEY"])(
  "implicit keyless providers with source %s still require available models",
  async (activeSource) => {
    connect([
      { ...provider, name: "ollama", implicit: true, auth: "optional-bearer", credentialRequired: false, activeSource },
    ]);
    const { result } = renderHook(useProviderSetup);
    await waitFor(() => expect(result.current.status).toBe("missing"));
  },
);

test("an implicit keyless provider offering models is available without credentials", async () => {
  const client = connect([
    { ...provider, name: "ollama", implicit: true, auth: "optional-bearer", credentialRequired: false },
  ]);
  client.on("model/list", () => ({ data: [{ provider: "ollama", model: "local-model" }] }));
  const { result } = renderHook(useProviderSetup);
  await waitFor(() => expect(result.current.status).toBe("ready"));
});

test("failed status requests offer retry instead of claiming credentials are missing", async () => {
  const client = connect([]);
  client.on("evener/instance/list", () => {
    throw new Error("offline");
  });
  const { result } = renderHook(useProviderSetup);
  await waitFor(() => expect(result.current.status).toBe("error"));
  client.on("evener/instance/list", () => ({ instances: [provider], availableProviders: [] }));
  await act(async () => result.current.retry());
  expect(result.current.status).toBe("missing");
});

test("credential removal and reconnect both re-evaluate setup without a first-run flag", async () => {
  const client = connect([{ ...provider, activeSource: "store" }]);
  const { result } = renderHook(useProviderSetup);
  await waitFor(() => expect(result.current.status).toBe("ready"));
  client.on("evener/instance/list", () => ({ instances: [provider], availableProviders: [] }));
  await act(async () => credentialsStore.getState().fetch());
  expect(result.current.status).toBe("missing");
  act(() => client.emitStateChange("reconnecting"));
  expect(result.current.status).toBe("loading");
  client.on("evener/instance/list", () => ({
    instances: [{ ...provider, activeSource: "store" }],
    availableProviders: [],
  }));
  act(() => client.emitStateChange("ready"));
  await waitFor(() => expect(result.current.status).toBe("ready"));
});

// --- remote host routing (component 07b) ------------------------------------
//
// The provider registry is host-dependent: for a remote host the hook must read
// THAT host's listing through evener/host/request, never the controller's, and
// a controller-scoped refetch (Settings > Credentials, ConnectProviderDialog,
// evener/auth/updated) must not replace what the remote form is showing.

test("a remote host's provider status comes from that host's own partitioned listing", async () => {
  const client = new FakeClient("ready");
  client.on("evener/host/request", (params) => {
    expect(params).toEqual({ host: "buildbox", method: "evener/instance/list", params: {} });
    return {
      instances: [{ ...provider, activeSource: "store" }],
      availableProviders: [],
    } as unknown as HostForwardedResult;
  });
  connectionStore.getState().connect(client);

  const { result } = renderHook(() => useProviderSetup("buildbox"));

  await waitFor(() => expect(result.current.status).toBe("ready"));
  expect(result.current.instances).toEqual([{ ...provider, activeSource: "store" }]);
  // The controller's own slot was never written, and nothing was read
  // controller-scoped.
  expect(credentialsStore.getState().instances).toEqual([]);
  expect(client.calls.some((call) => call.method === "evener/instance/list")).toBe(false);
});

test("a controller-scoped refetch does not replace a remote host's provider list", async () => {
  const client = new FakeClient("ready");
  client.on("model/list", () => ({ data: [] }));
  client.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
  client.on("evener/host/request", () => {
    return {
      instances: [{ ...provider, activeSource: "store" }],
      availableProviders: [],
    } as unknown as HostForwardedResult;
  });
  connectionStore.getState().connect(client);
  const { result } = renderHook(() => useProviderSetup("buildbox"));
  await waitFor(() => expect(result.current.status).toBe("ready"));

  // A controller-scoped refetch (what the Settings pane and the notification
  // path issue) must leave the remote form's verdict alone.
  await act(async () => credentialsStore.getState().fetch());

  expect(result.current.status).toBe("ready");
  expect(result.current.instances).toEqual([{ ...provider, activeSource: "store" }]);
});

// M2 (round 6): the store owns the refresh when the registry invalidates a
// partition. The spawn form keys its effect on [client, connection, load] and
// knows nothing about registry identity - it must converge anyway, because the
// same defect would otherwise reappear in every other partition consumer.
function registryRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return { origin: "sidecar", attached: true, midAttach: false, removed: false, ...overrides };
}

// M1: the registry's own failure must be REACHABLE here. A remote target whose
// registry read failed used to sit on a pending state forever - status
// "loading", no verdict, and Start proceeding without the provider gate.
test("a failed registry surfaces on a remote target, and its retry recovers", async () => {
  const client = new FakeClient("ready");
  let registryDown = true;
  client.on("evener/host/list", () => {
    if (registryDown) throw new WireError("registry unavailable", -32000);
    return { hosts: [registryRow({ name: "buildbox" })] };
  });
  client.on(
    "evener/host/request",
    () =>
      ({
        instances: [{ ...provider, activeSource: "store" }],
        availableProviders: [],
      }) as unknown as HostForwardedResult,
  );
  connectionStore.getState().connect(client);
  hostsStore.getState().resetForTests();
  await hostsStore.getState().fetch(); // the registry read fails

  const { result } = renderHook(() => useProviderSetup("buildbox"));
  await waitFor(() => expect(result.current.status).toBe("error"));

  // Its retry re-reads the registry, and the listing follows.
  registryDown = false;
  await act(async () => result.current.retry());
  await waitFor(() => expect(result.current.status).toBe("ready"));
});

test("a registry-driven re-registration re-reads the host's listing without a remount", async () => {
  const client = new FakeClient("ready");
  let listing: InstanceListResponse = { instances: [{ ...provider, activeSource: "store" }], availableProviders: [] };
  client.on("evener/host/request", () => listing as unknown as HostForwardedResult);
  connectionStore.getState().connect(client);
  hostsStore.setState({
    load: { phase: "ready", hosts: [registryRow({ name: "buildbox", address: "a.example" })] },
  });

  const { result } = renderHook(() => useProviderSetup("buildbox"));
  await waitFor(() => expect(result.current.instances).toEqual([{ ...provider, activeSource: "store" }]));

  // The name is re-registered as a different machine; its own listing now
  // reports a fresh, unconfigured instance.
  listing = { instances: [{ ...provider, name: "fresh", activeSource: "none" }], availableProviders: [] };
  await act(async () => {
    hostsStore.setState({
      load: { phase: "ready", hosts: [registryRow({ name: "buildbox", address: "b.example" })] },
    });
  });

  // The store re-read the name under its new registration, so the form shows that
  // answer instead of the empty partition the drop left.
  await waitFor(() => expect(result.current.instances).toEqual([{ ...provider, name: "fresh", activeSource: "none" }]));
});
