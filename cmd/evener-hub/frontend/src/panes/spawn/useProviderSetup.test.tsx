import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { InstanceEntry } from "../../protocol/types.gen";
import { connectionStore } from "../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../stores/credentials";
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
});
afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", client: null });
});

function connect(instances: InstanceEntry[]) {
  const client = new FakeClient("ready");
  client.on("evener/instance/list", () => ({ instances, availableProviders: [] }));
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
