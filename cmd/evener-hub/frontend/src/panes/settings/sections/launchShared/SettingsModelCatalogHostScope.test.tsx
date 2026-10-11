import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../../../stores/connection";
import { resetCredentialsStoreForTests, resetHostInstancesForTests } from "../../../../stores/credentials";
import { SettingsModelCatalog } from "./SettingsModelCatalog";

// The launch/project model picker's host scope (component 07b): the picker
// renders inside the launch-evener and project panes, which this branch
// host-scopes. Under a remote host it must read THAT host's model/list through
// evener/host/request (and take its revision from that host's own provider
// instances); the local hub keeps today's plain model/list byte-for-byte.

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

/** True when the section forwarded `method` through evener/host/request. */
function forwarded(fake: FakeClient, method: string): boolean {
  return fake.calls.some(
    (call) => call.method === "evener/host/request" && (call.params as { method: string }).method === method,
  );
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
  resetHostInstancesForTests();
});

afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
  resetHostInstancesForTests();
});

test("with a remote host selected the picker reads THAT host's model/list through the proxy, never the controller's", async () => {
  const fake = connectFakeClient();
  // The controller's own model/list is booby-trapped: reaching it under a remote
  // selection IS the defect this pins (the picker used to offer this hub's
  // models for a remote launch/project layer).
  fake.on("model/list", () => {
    throw new Error("a remote selection must not read this hub's model list");
  });
  fake.on("evener/host/request", (params) => {
    const forwardedCall = params as { host: string; method: string };
    expect(forwardedCall.host).toBe("beta");
    if (forwardedCall.method === "model/list") {
      return { data: [{ provider: "beta", model: "beta-model", displayName: "Beta model" }] } as never;
    }
    if (forwardedCall.method === "evener/instance/list") {
      return { instances: [], availableProviders: [] } as never;
    }
    throw new Error(`unexpected forwarded method ${forwardedCall.method}`);
  });

  render(<SettingsModelCatalog host="beta" value="" onChange={() => {}} />);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: /change model/i }));

  await waitFor(() => expect(forwarded(fake, "model/list")).toBe(true));
  expect(fake.calls.some((call) => call.method === "model/list")).toBe(false);
  // The revision is loaded from that host's own instance listing, not this hub's.
  await waitFor(() => expect(forwarded(fake, "evener/instance/list")).toBe(true));
  expect(await screen.findByRole("option", { name: /Beta model/ })).toBeTruthy();
});

test("the local hub issues exactly today's plain model/list and never the proxy", async () => {
  const fake = connectFakeClient();
  fake.on("model/list", () => ({ data: [{ provider: "local", model: "local-model", displayName: "Local model" }] }));
  fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
  fake.on("evener/host/request", () => {
    throw new Error("the local hub must never route the catalog through the proxy");
  });

  render(<SettingsModelCatalog host="local" value="" onChange={() => {}} />);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: /change model/i }));

  await waitFor(() => expect(fake.calls.some((call) => call.method === "model/list")).toBe(true));
  expect(fake.calls.some((call) => call.method === "evener/host/request")).toBe(false);
  expect(await screen.findByRole("option", { name: /Local model/ })).toBeTruthy();
});

// A remote host's instance listing has exactly ONE owner: useHostInstances,
// which issues the read itself against the registry's current answer (and
// withholds a listing that answer no longer describes). The component used to
// ALSO call fetchHost in an else-branch, so a remote render read the same
// listing TWICE over the SSH proxy - and the manual read, which bypasses the
// hook's registry gates, waits for no registry read, and stamps its own
// revision, won the version race and discarded the hook's own response.
test("a remote host's instance listing is read once, not twice", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", (params) => {
    const forwardedCall = params as { host: string; method: string };
    if (forwardedCall.method === "evener/instance/list") {
      return { instances: [], availableProviders: [] } as never;
    }
    if (forwardedCall.method === "model/list") {
      return { data: [] } as never;
    }
    throw new Error(`unexpected forwarded method ${forwardedCall.method}`);
  });

  render(<SettingsModelCatalog host="beta" value="" onChange={() => {}} />);

  await waitFor(() => expect(forwarded(fake, "evener/instance/list")).toBe(true));
  const reads = fake.calls.filter(
    (call) =>
      call.method === "evener/host/request" && (call.params as { method: string }).method === "evener/instance/list",
  );
  expect(reads).toEqual([
    { method: "evener/host/request", params: { host: "beta", method: "evener/instance/list", params: {} } },
  ]);
});
