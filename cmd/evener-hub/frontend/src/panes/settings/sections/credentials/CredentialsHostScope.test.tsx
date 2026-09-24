import type { HostForwardedResult, HostRequestParams, HostRow, InstanceEntry } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { deferRequest, FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../../../stores/connection";
import {
  credentialsStore,
  fetchHost,
  hostInstancesStore,
  hostPartition,
  resetCredentialsStoreForTests,
  resetHostInstancesForTests,
} from "../../../../stores/credentials";
import { hostsStore } from "../../../../stores/hosts";
import { setMutationClientIdentityForTests } from "../../../../stores/mutationClientIdentity";
import { resetSettingsHostForTests, settingsHostStore } from "../../../../stores/settingsHost";
import { getToasts, resetToastStoreForTests } from "../../../../widgets/toast/store";
import { CredentialsHostScope } from "./CredentialsHostScope";

// The credentials settings surface's host scope (component 07b's read path made
// visible): a "Host" picker over the local hub plus every configured remote
// host, and - for a remote host - that host's OWN provider listing, read-only.
// The controller's rows and a remote host's are separate partitions by
// construction, so these tests pin that a remote selection never shows the
// controller's rows (and vice versa), and that this hub is exactly the default.

function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
  return {
    protocol: "openai-chat",
    auth: "bearer",
    implicit: false,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    ...overrides,
  };
}

function hostRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return {
    origin: "sidecar",
    attached: false,
    midAttach: false,
    removed: false,
    ...overrides,
  };
}

const CONTROLLER_ROW = instance({ name: "controller-only", providerId: "anthropic", authModes: ["apiKey"] });
const HOST_ROW = instance({ name: "on-beta", providerId: "anthropic", authModes: ["apiKey"] });

const CONTROLLER_LIST = { instances: [CONTROLLER_ROW], availableProviders: [] };
const HOST_LIST = { instances: [HOST_ROW], availableProviders: [] };
// The same host answering with a newer listing after a reconnect, and a
// different host registered under the same name.
const RELOADED_HOST_LIST = {
  instances: [instance({ name: "on-beta-reloaded", providerId: "anthropic", authModes: ["apiKey"] })],
  availableProviders: [],
};
const REPLACED_HOST_LIST = {
  instances: [instance({ name: "on-beta-replaced", providerId: "anthropic", authModes: ["apiKey"] })],
  availableProviders: [],
};

// The selected remote host's own Codex instance: the one provider whose sign-in
// is the OpenAI device-code flow ("Sign in on host", component 07d).
const REMOTE_CODEX_ROW = instance({
  name: "codex",
  providerId: "openai-codex",
  protocol: "openai-responses",
  auth: "oauth-openai-codex",
  authModes: ["oauth"],
});
const CODEX_HOST_LIST = { instances: [REMOTE_CODEX_ROW], availableProviders: [] };
const REMOTE_DEVICE_START = {
  provider: "codex",
  flowId: "flow-remote",
  userCode: "REMOTE-CODE",
  verificationUrl: "https://verify.example/codex",
  intervalSeconds: 1,
};
const REMOTE_DEVICE_FALLBACK = {
  provider: "codex",
  flowId: "",
  userCode: "",
  verificationUrl: "",
  intervalSeconds: 0,
  fallback: true,
};
const REMOTE_POLL_AUTHORIZED = { state: "authorized" };
const REMOTE_POLL_PENDING = { state: "pending" };

// hostForwardedCalls reads back the params of every evener/host/request the
// browser issued, so a test can pin WHICH host and WHICH method each carried.
function hostForwardedCalls(fake: FakeClient): HostRequestParams[] {
  return fake.calls
    .filter((call) => call.method === "evener/host/request")
    .map((call) => call.params as HostRequestParams);
}

function forwardedMethodCalls(fake: FakeClient, method: string): HostRequestParams[] {
  return hostForwardedCalls(fake).filter((call) => call.method === method);
}

// A host/request handler that answers this host's listing plus the device RPCs
// the sign-in drives, and fails loudly on anything else.
function codexHostRequest(poll: () => { state: string }): (params: HostRequestParams) => HostForwardedResult {
  return (params) => {
    if (params.method === "evener/instance/list") return CODEX_HOST_LIST;
    if (params.method === "evener/auth/device/start") return REMOTE_DEVICE_START;
    if (params.method === "evener/auth/device/poll") return poll();
    throw new Error(`unexpected forwarded method ${params.method}`);
  };
}

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

// A read whose answer this test releases itself, so the state under test is the
// one while that answer is still out.
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
  resetHostInstancesForTests();
  resetToastStoreForTests();
  hostsStore.getState().resetForTests();
  // The selection is route-level now (stores/settingsHost.ts): reset it, and
  // mount on the settings route it is part of.
  resetSettingsHostForTests();
  window.history.pushState({}, "", "/settings/credentials");
  setMutationClientIdentityForTests("test-tab");
});

afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  window.history.pushState({}, "", "/");
});

test("defaults to this hub and reads the controller's own listing without any proxied call", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => {
    throw new Error("the local scope must never route through the proxy");
  });

  render(<CredentialsHostScope sectionId="credentials" />);

  expect(await screen.findByText("controller-only")).toBeTruthy();
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("local");
  expect(fake.calls.some((call) => call.method === "evener/host/request")).toBe(false);
});

test("offers this hub plus every configured remote host", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({
    hosts: [hostRow({ name: "beta", attached: true }), hostRow({ name: "gamma", attached: false })],
  }));

  render(<CredentialsHostScope sectionId="credentials" />);

  // The registry read is async, so wait for its last option before reading the
  // whole set rather than asserting against the initial (local-only) render.
  await screen.findByRole("option", { name: "gamma (offline)" });
  const options = screen.getAllByRole("option");
  expect(options.map((option) => (option as HTMLOptionElement).value)).toEqual(["local", "beta", "gamma"]);
  expect(screen.getByRole("option", { name: "This hub" })).toBeTruthy();
  expect(screen.getByRole("option", { name: "gamma (offline)" })).toBeTruthy();
  // Nothing remote is read until one is selected.
  expect(fake.calls.some((call) => call.method === "evener/host/request")).toBe(false);
});

test("selecting a remote host shows THAT host's own providers read-only, never the controller's", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", (params) => {
    expect(params).toEqual({ host: "beta", method: "evener/instance/list", params: {} });
    return HOST_LIST;
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByText("controller-only");
  await screen.findByRole("option", { name: "beta" });

  await user.selectOptions(select, "beta");

  // The host's own row is present, labelled as the host's data...
  expect(await screen.findByText("on-beta")).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Providers on beta" })).toBeTruthy();
  // ...and the controller's row is gone: the listings are never merged.
  expect(screen.queryByText("controller-only")).toBeNull();
});

test("a remote host's listing is read-only - it offers no per-instance actions", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");

  const remoteSection = await screen.findByRole("region", { name: "Providers on beta" });
  expect(await within(remoteSection).findByText("on-beta")).toBeTruthy();
  expect(within(remoteSection).queryByRole("button")).toBeNull();
});

test("a remote read in flight shows the host's own loading state, never the controller's rows", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  const release = deferRequest<unknown>(fake, "evener/host/request");

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByText("controller-only");
  await screen.findByRole("option", { name: "beta" });

  await user.selectOptions(select, "beta");

  const remoteSection = await screen.findByRole("region", { name: "Providers on beta" });
  expect(within(remoteSection).getByRole("status", { name: "Loading" })).toBeTruthy();
  expect(screen.queryByText("controller-only")).toBeNull();

  await act(async () => release(HOST_LIST));
  expect(await within(remoteSection).findByText("on-beta")).toBeTruthy();
});

test("an unattached host's own refusal is shown honestly, not this hub's listing", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: false })] }));
  fake.on("evener/host/request", () => {
    throw new WireError('host "beta" is not attached', -32000);
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta (offline)" });

  await user.selectOptions(select, "beta");

  expect(await screen.findByText(/host "beta" is not attached/)).toBeTruthy();
  expect(screen.queryByText("controller-only")).toBeNull();
});

test("a host that is no longer configured says so instead of falling back to this hub", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByText("on-beta");

  // Beta disappears from the registry (removed from another client).
  act(() => hostsStore.setState({ load: { phase: "ready", hosts: [] } }));

  expect(await screen.findByText(/is no longer configured/)).toBeTruthy();
  expect(screen.queryByText("on-beta")).toBeNull();
  expect(screen.queryByText("controller-only")).toBeNull();
});

test("switching back to this hub restores the controller's own listing", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByText("on-beta");

  await user.selectOptions(select, "local");

  expect(await screen.findByText("controller-only")).toBeTruthy();
  expect(screen.queryByText("on-beta")).toBeNull();
});

// The controller's own store must not be touched by a remote selection: the
// partition is where a remote host's rows live, and the controller's listing
// keeps its own identity across the switch (stores/credentials.ts's split).
test("a remote selection leaves the controller's own store untouched", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByText("controller-only");
  expect(credentialsStore.getState().instances).toEqual([CONTROLLER_ROW]);

  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByText("on-beta");

  expect(credentialsStore.getState().instances).toEqual([CONTROLLER_ROW]);
});

// The read-only half of host scoping: a remote selection must never issue a
// controller-scoped credential write. This view renders no write affordance at
// all, so any `evener/auth/*` mutation reaching the controller client while a
// remote host is selected is a host-scoping bug (the write would land on THIS
// hub's credential store). Routing those writes to the selected host is a
// separate unit; until it lands, the guard is that none are sent.
test("a remote selection issues no controller-scoped credential write", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);
  fake.on("evener/auth/apiKey/set", () => {
    throw new Error("a remote selection must not write to this hub's credential store");
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByText("on-beta");

  expect(fake.calls.some((call) => call.method.startsWith("evener/auth/"))).toBe(false);
});

// Medium (roborev): the read must follow the connection. useConnectedEffect's
// `started` flag is per-effect and the effect's only dependency was `host`, so a
// reconnect or a client swap left the partition exactly where the transition put
// it and nothing ever re-read the host.
test("a connection transition re-reads the selected host's own listing", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByText("on-beta");
  expect(fake.calls.filter((call) => call.method === "evener/host/request")).toHaveLength(1);

  // The connection is replaced; beta's own hub answers with a newer listing.
  const replacement = new FakeClient("ready");
  replacement.on("evener/instance/list", () => CONTROLLER_LIST);
  // The picker now re-reads the registry on the current connection (M-1), so a
  // replacement client must answer it the way a real hub does.
  replacement.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  replacement.on("evener/host/request", () => RELOADED_HOST_LIST);
  await act(async () => connectionStore.getState().connect(replacement));

  expect(await screen.findByText("on-beta-reloaded")).toBeTruthy();
  expect(replacement.calls.filter((call) => call.method === "evener/host/request")).toHaveLength(1);
});

// The concrete harm of the same gap: the transition releases the in-flight
// read's status, so an unanswered read used to settle as the frozen-empty shape
// - which reads as "never read" being false and "empty" being true, and the
// panel claimed "No provider instances" although nothing had ever answered.
test("a transition that orphans the first read never reads as 'no instances'", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  // The first read never answers; the connection is replaced underneath it.
  deferRequest<unknown>(fake, "evener/host/request");

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");

  const replacement = new FakeClient("ready");
  replacement.on("evener/instance/list", () => CONTROLLER_LIST);
  // See above: the registry is re-read on the connection that is current now.
  replacement.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  replacement.on("evener/host/request", () => HOST_LIST);
  await act(async () => connectionStore.getState().connect(replacement));

  expect(screen.queryByText(/No provider instances on beta/)).toBeNull();
  expect(await screen.findByText("on-beta")).toBeTruthy();
});

// L-1: with no successful read the partition is pending (read is false) - but an
// ERROR is an answer, and a loading skeleton beside it reads as "still working"
// when the host has already refused. The skeleton is for the state it was written
// for: nothing (and no failure) yet.
test("an error without a successful read shows no loading skeleton", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: false })] }));
  fake.on("evener/host/request", () => {
    throw new WireError('host "beta" is not attached', -32000);
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta (offline)" });
  await user.selectOptions(select, "beta");

  expect(await screen.findByText(/host "beta" is not attached/)).toBeTruthy();
  expect(screen.queryByRole("status", { name: "Loading" })).toBeNull();
});

// M-1 (round 4): attachment is live registry data, so a host that attaches
// advances the registry revision - but a read keyed on host/connection alone never
// retried, so an offline host that fails and later attaches stayed failed with no
// retry control. The attachment change is the trigger.
test("an attachment transition retries a failed remote read", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: false })] }));
  let attached = false;
  fake.on("evener/host/request", () => {
    if (!attached) throw new WireError('host "beta" is not attached', -32000);
    return HOST_LIST;
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta (offline)" });
  await user.selectOptions(select, "beta");
  await screen.findByText(/host "beta" is not attached/);
  const before = fake.calls.filter((call) => call.method === "evener/host/request").length;

  // The host attaches: only live registry data changes; the entry (the identity)
  // does not.
  attached = true;
  act(() => {
    hostsStore.setState({ load: { phase: "ready", hosts: [hostRow({ name: "beta", attached: true })] } });
  });

  expect(await screen.findByText("on-beta")).toBeTruthy();
  expect(fake.calls.filter((call) => call.method === "evener/host/request")).toHaveLength(before + 1);
});

// ---------------------------------------------------------------------------
// Component 07d: "Sign in on host" - a remote Codex instance's device-code
// sign-in, driven on the SELECTED host through evener/host/request.
// ---------------------------------------------------------------------------

test("offers 'Sign in on host' for a remote host's Codex instance, and never for this hub", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on(
    "evener/host/request",
    codexHostRequest(() => REMOTE_POLL_PENDING),
  );

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByText("controller-only");
  // This hub's own listing never offers it.
  expect(screen.queryByRole("button", { name: "Sign in on host" })).toBeNull();

  await user.selectOptions(select, "beta");
  expect(await screen.findByRole("button", { name: "Sign in on host" })).toBeTruthy();

  // Only for a remote host: back on this hub the affordance is gone.
  await user.selectOptions(select, "local");
  expect(screen.queryByRole("button", { name: "Sign in on host" })).toBeNull();
});

test("'Sign in on host' drives device/start and device/poll through evener/host/request for the selected host, showing the code and the URL", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on(
    "evener/host/request",
    codexHostRequest(() => REMOTE_POLL_AUTHORIZED),
  );

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await user.click(await screen.findByRole("button", { name: "Sign in on host" }));

  // The start is forwarded to the selected host...
  await vi.waitFor(() => {
    expect(forwardedMethodCalls(fake, "evener/auth/device/start")).toEqual([
      { host: "beta", method: "evener/auth/device/start", params: { provider: "codex" } },
    ]);
  });
  // ...and the plain, controller-scoped call is never issued for a remote host.
  expect(fake.calls.some((call) => call.method === "evener/auth/device/start")).toBe(false);

  // The user code and the verification URL are both on screen, so the sign-in
  // can be completed on another device.
  expect(await screen.findByText("REMOTE-CODE")).toBeTruthy();
  expect(screen.getByText("https://verify.example/codex")).toBeTruthy();
  // The dialog names the instance AND the host: several Codex-capable
  // instances on one host must not be confusable by the code on screen.
  expect(screen.getByRole("dialog", { name: "Sign in to codex on beta" })).toBeTruthy();

  // The poll is the same host-addressed call, at the flow's own interval.
  await vi.waitFor(
    () => {
      expect(forwardedMethodCalls(fake, "evener/auth/device/poll")).toContainEqual({
        host: "beta",
        method: "evener/auth/device/poll",
        params: { provider: "codex", flowId: "flow-remote" },
      });
    },
    { timeout: 3000 },
  );
  expect(fake.calls.some((call) => call.method === "evener/auth/device/poll")).toBe(false);
});

test("a remote device flow stops polling on success and reports it on the host", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on(
    "evener/host/request",
    codexHostRequest(() => REMOTE_POLL_AUTHORIZED),
  );

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await user.click(await screen.findByRole("button", { name: "Sign in on host" }));

  // Success closes the dialog and names the instance and the host it signed
  // in on (see oauthDialogs.tsx's DeviceCodeDialog).
  await vi.waitFor(() => expect(screen.queryByRole("dialog")).toBeNull(), { timeout: 3000 });
  expect(getToasts().some((toast) => toast.text === "Signed in to codex on beta")).toBe(true);

  const polls = forwardedMethodCalls(fake, "evener/auth/device/poll").length;
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 1500));
  });
  expect(forwardedMethodCalls(fake, "evener/auth/device/poll")).toHaveLength(polls);
});

test("a remote device flow stops polling on a terminal error and says so", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", (params) => {
    if (params.method === "evener/instance/list") return CODEX_HOST_LIST;
    if (params.method === "evener/auth/device/start") return REMOTE_DEVICE_START;
    if (params.method === "evener/auth/device/poll") throw new Error("device auth failed with status 400");
    throw new Error(`unexpected forwarded method ${params.method}`);
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await user.click(await screen.findByRole("button", { name: "Sign in on host" }));

  // The refused poll is a real, named failure with a retry, never a silent wait.
  await vi.waitFor(() => expect(screen.getByText("device auth failed with status 400")).toBeTruthy(), {
    timeout: 3000,
  });
  expect(screen.getByRole("button", { name: "Start again" })).toBeTruthy();

  const polls = forwardedMethodCalls(fake, "evener/auth/device/poll").length;
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 1500));
  });
  expect(forwardedMethodCalls(fake, "evener/auth/device/poll")).toHaveLength(polls);
});

test("unmounting during a remote device flow stops polling", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on(
    "evener/host/request",
    codexHostRequest(() => REMOTE_POLL_PENDING),
  );

  const { unmount } = render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await user.click(await screen.findByRole("button", { name: "Sign in on host" }));

  await vi.waitFor(() => expect(forwardedMethodCalls(fake, "evener/auth/device/poll").length).toBeGreaterThan(0), {
    timeout: 3000,
  });
  unmount();
  const polls = forwardedMethodCalls(fake, "evener/auth/device/poll").length;
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 1500));
  });
  expect(forwardedMethodCalls(fake, "evener/auth/device/poll")).toHaveLength(polls);
});

test("a host that offers no device flow surfaces a named failure instead of a dead end", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", (params) => {
    if (params.method === "evener/instance/list") return CODEX_HOST_LIST;
    if (params.method === "evener/auth/device/start") return REMOTE_DEVICE_FALLBACK;
    throw new Error(`unexpected forwarded method ${params.method}`);
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await user.click(await screen.findByRole("button", { name: "Sign in on host" }));

  expect((await screen.findByRole("alert")).textContent).toContain("Device-code sign-in is not enabled on beta");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("a refused remote device/start surfaces the host's own failure, never a silent wait", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", (params) => {
    if (params.method === "evener/instance/list") return CODEX_HOST_LIST;
    if (params.method === "evener/auth/device/start") {
      throw new WireError('host "beta" is not attached', -32000);
    }
    throw new Error(`unexpected forwarded method ${params.method}`);
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await user.click(await screen.findByRole("button", { name: "Sign in on host" }));

  expect((await screen.findByRole("alert")).textContent).toContain('host "beta" is not attached');
  expect(screen.queryByRole("dialog")).toBeNull();
});

// Medium (roborev): the partition map is keyed by NAME alone, so a host removed
// and re-registered under the same name could render the previous
// registration's rows - here, while the new host's own read is still out.
test("a host re-registered under the same name never shows the previous host's rows", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", address: "a.example", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByText("on-beta");

  // A DIFFERENT host now answers to "beta" (its entry, the registry's identity
  // for the name, changed), and its read is held open.
  const gate = deferred<unknown>();
  fake.on("evener/host/request", () => gate.promise as never);
  act(() => {
    hostsStore.setState({
      load: { phase: "ready", hosts: [hostRow({ name: "beta", address: "b.example", attached: true })] },
    });
  });

  expect(screen.queryByText("on-beta")).toBeNull();

  await act(async () => gate.resolve(REPLACED_HOST_LIST));
  expect(await screen.findByText("on-beta-replaced")).toBeTruthy();
  expect(screen.queryByText("on-beta")).toBeNull();
});

// L1 (roborev round 5): on a fresh deep-link the registry has not answered yet,
// so the pane has no identity. Issuing the read then is pure waste - it is never
// displayable (verified requires an identity) and the registry's own answer
// replaces it immediately. The read waits for the registry instead.
test("a fresh deep-link does not read the remote host until the registry names it", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  const registry = deferRequest<unknown>(fake, "evener/host/list");
  fake.on("evener/host/request", () => HOST_LIST);

  // The selection is the route's (a deep link), and the registry is still out.
  settingsHostStore.setState({ host: "beta" });
  render(<CredentialsHostScope sectionId="credentials" />);
  // deferRequest arms its resolver one microtask after the request is issued.
  await act(async () => {});

  // No remote read while the registry is unread: nothing it could answer is
  // displayable, and the registry's answer would replace it anyway.
  expect(fake.calls.filter((call) => call.method === "evener/host/request")).toHaveLength(0);

  await act(async () => {
    registry({ hosts: [hostRow({ name: "beta", attached: true })] });
  });

  // The registry names the host, and exactly one read follows.
  expect(await screen.findByText("on-beta")).toBeTruthy();
  expect(fake.calls.filter((call) => call.method === "evener/host/request")).toHaveLength(1);
});

// The gate must not turn a registry that never answers into an eternal skeleton:
// once the registry has FAILED, no identity is coming, so the pane reads anyway
// and shows the host's own refusal instead of hanging.
// With the registry unread there is no registration to check a listing against,
// so the pane says exactly that and offers the registry's own read to retry - it
// does not read a host whose registration it cannot check.
test("a registry read that fails shows the honest unverifiable state", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => {
    throw new WireError("registry unavailable", -32000);
  });
  fake.on("evener/host/request", () => {
    throw new WireError('host "beta" is not attached', -32000);
  });

  settingsHostStore.setState({ host: "beta" });
  render(<CredentialsHostScope sectionId="credentials" />);

  expect(await screen.findByText(/Couldn't check beta's registration/)).toBeTruthy();
  // A failed registry is never a permanent skeleton - that reads as "still
  // working" - and no remote read is issued under it.
  expect(screen.queryByRole("status", { name: "Loading" })).toBeNull();
  expect(fake.calls.filter((call) => call.method === "evener/host/request")).toHaveLength(0);
});

// The unverifiable state is not a dead end: the registry's own read (Retry)
// restores the check, and the listing is shown once it lands.
test("the unverifiable state's retry re-reads the registry and restores verification", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  let registryDown = true;
  fake.on("evener/host/list", () => {
    if (registryDown) throw new WireError("registry unavailable", -32000);
    return { hosts: [hostRow({ name: "beta", attached: true })] };
  });
  fake.on("evener/host/request", () => HOST_LIST);

  settingsHostStore.setState({ host: "beta" });
  render(<CredentialsHostScope sectionId="credentials" />);
  await screen.findByText(/hosts list/i);

  // The hosts list comes back, and Retry issues the registry's own read.
  registryDown = false;
  await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));

  expect(await screen.findByText("on-beta")).toBeTruthy();
});

// M1 (round 8): a listing read while the registry was merely idle (the spawn
// pane reads its hosts from the navigation manifest, so the registry may never
// have been asked) must NOT count as verified once the registry has been
// consulted and failed. It used to: both shared revision 0, so the pane showed
// the listing and its `unverifiable` state was suppressed.
test("a listing read before the registry ever answered is not verified when the registry fails", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => {
    throw new WireError("registry unavailable", -32000);
  });
  fake.on("evener/host/request", () => HOST_LIST);

  // The spawn pane's own read, while the registry has never been consulted.
  await fetchHost("beta");
  expect(hostPartition(hostInstancesStore.getState(), "beta").read).toBe(true);

  settingsHostStore.setState({ host: "beta" });
  render(<CredentialsHostScope sectionId="credentials" />);

  expect(await screen.findByText(/Couldn't check beta's registration/)).toBeTruthy();
  expect(screen.queryByText("on-beta")).toBeNull();
});

// M2 (round 8): a read that FAILED is a reachable dead end - the registry is
// healthy, so `unverifiable` is false, and the error branch offered no retry at
// all. It offers one now, and it re-reads the host.
test("a failed remote read offers a retry that re-reads the host", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  let hostUp = false;
  fake.on("evener/host/request", () => {
    if (!hostUp) throw new WireError("host beta unreachable", -32000);
    return HOST_LIST;
  });

  settingsHostStore.setState({ host: "beta" });
  render(<CredentialsHostScope sectionId="credentials" />);
  expect(await screen.findByText(/host beta unreachable/)).toBeTruthy();

  hostUp = true;
  await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));

  expect(await screen.findByText("on-beta")).toBeTruthy();
});

// M4 (round 6): a registry re-read that FAILS leaves the registry revision where
// it was, so a listing already read under it is still current and verified: the
// phase alone is the wrong key for "unverifiable", and the banner must not sit
// beside a listing the registry did name.
test("a retained identity keeps a verified listing shown when the registry later fails", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);

  settingsHostStore.setState({ host: "beta" });
  render(<CredentialsHostScope sectionId="credentials" />);
  await screen.findByText("on-beta");

  // The registry's next read fails. The revision does not move for a failure, so
  // the rows are still read under the current one.
  act(() => hostsStore.setState({ load: { phase: "error", message: "registry down" } }));

  expect(screen.queryByText(/Couldn't check/)).toBeNull();
  expect(screen.getByText("on-beta")).toBeTruthy();
});

// L1 (round 6): the remote listing's diagnostics were dropped, so a malformed or
// partially loaded remote provider config read as a complete listing. They belong
// on the host partition and on the read-only remote view.
test("a remote answer's diagnostics are shown on the read-only remote view", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => ({
    ...HOST_LIST,
    diagnostics: ['providers.toml: unexpected key "type"'],
  }));

  settingsHostStore.setState({ host: "beta" });
  render(<CredentialsHostScope sectionId="credentials" />);

  expect(await screen.findByText('providers.toml: unexpected key "type"')).toBeTruthy();
});

// M (roborev): the sign-in editor's state belongs to the host it was started
// for. RemoteHostInstances rendered with no host key, so a device/start answer
// that landed after the picker moved was applied to the NEW host's render: the
// dialog named the new host while the code and flowId belonged to the old one,
// and the poll - and the "Signed in to <instance> on <host>" toast - went to a
// host whose auth
// store the sign-in never touched. The editor cannot outlive its host now: the
// subtree is keyed on the host's registration identity, exactly as the push
// action below it is, so the flow's state is cleared with the host it belonged
// to and nothing of it is rendered, driven, or reported under another.
test("a picker change while device/start is outstanding never renders or polls the host that took over", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({
    hosts: [hostRow({ name: "beta", attached: true }), hostRow({ name: "gamma", attached: true })],
  }));
  // Both hosts answer with the same Codex listing, so a flow mounted under
  // gamma would have a row to poll against and a host to report a sign-in on.
  const start = deferred<unknown>();
  fake.on("evener/host/request", (params) => {
    if (params.method === "evener/instance/list") return CODEX_HOST_LIST;
    if (params.method === "evener/auth/device/start") return start.promise as never;
    if (params.method === "evener/auth/device/poll") return REMOTE_POLL_AUTHORIZED;
    throw new Error(`unexpected forwarded method ${params.method}`);
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "gamma" });
  await user.selectOptions(select, "beta");
  await user.click(await screen.findByRole("button", { name: "Sign in on host" }));
  await vi.waitFor(() => {
    expect(forwardedMethodCalls(fake, "evener/auth/device/start")).toEqual([
      { host: "beta", method: "evener/auth/device/start", params: { provider: "codex" } },
    ]);
  });

  // The picker moves to gamma while beta's start is still on the wire.
  await user.selectOptions(select, "gamma");
  expect(await screen.findByRole("heading", { name: "Providers on gamma" })).toBeTruthy();

  await act(async () => start.resolve(REMOTE_DEVICE_START));

  // The answer to beta's start is beta's: not a dialog under gamma, and not
  // even on screen as gamma's.
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.queryByText("REMOTE-CODE")).toBeNull();

  // Long enough for a dialog mounted under gamma to have ticked at the flow's own
  // 1s interval, polled gamma, and reported the sign-in there.
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 1500));
  });
  expect(forwardedMethodCalls(fake, "evener/auth/device/poll")).toEqual([]);
  // The toast a flow mounted under gamma would push names the instance AND
  // the host it ran on; neither may be produced by beta's answer.
  expect(getToasts().some((toast) => toast.text === "Signed in to codex on gamma")).toBe(false);
});

// M (roborev), the same seam on the failure path: a start failure names the host
// it happened on, so it cannot stay on screen under another host's selection.
test("a sign-in failure naming the old host does not survive a host change", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({
    hosts: [hostRow({ name: "beta", attached: true }), hostRow({ name: "gamma", attached: true })],
  }));
  fake.on("evener/host/request", (params) => {
    if (params.method === "evener/instance/list") return CODEX_HOST_LIST;
    if (params.method === "evener/auth/device/start") return REMOTE_DEVICE_FALLBACK;
    throw new Error(`unexpected forwarded method ${params.method}`);
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "gamma" });
  await user.selectOptions(select, "beta");
  await user.click(await screen.findByRole("button", { name: "Sign in on host" }));
  expect((await screen.findByRole("alert")).textContent).toContain("Device-code sign-in is not enabled on beta");

  await user.selectOptions(select, "gamma");
  expect(await screen.findByRole("heading", { name: "Providers on gamma" })).toBeTruthy();

  // Gamma's own view never wears beta's failure.
  expect(screen.queryByRole("alert")).toBeNull();
});

// L1 (roborev): "Start again" on an expired dialog set only the failure, so the
// expired editor stayed mounted with its old code and flow: the user was left
// with a dead flow on screen and a message about the restart that replaced it.
// A restart clears the editor it is replacing now, and a new dialog is opened
// only by a start that actually succeeded.
test("a failed 'Start again' clears the expired dialog instead of leaving it standing", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  let restartFails = false;
  fake.on("evener/host/request", (params) => {
    if (params.method === "evener/instance/list") return CODEX_HOST_LIST;
    if (params.method === "evener/auth/device/start") {
      if (restartFails) throw new WireError('host "beta" is not attached', -32000);
      return REMOTE_DEVICE_START;
    }
    if (params.method === "evener/auth/device/poll") return { state: "expired" };
    throw new Error(`unexpected forwarded method ${params.method}`);
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await user.click(await screen.findByRole("button", { name: "Sign in on host" }));

  // The flow expires, which is what offers "Start again".
  await screen.findByText("REMOTE-CODE");
  const startAgain = await screen.findByRole("button", { name: "Start again" }, { timeout: 3000 });

  // The restart fails. The expired editor it replaced must not stay standing.
  restartFails = true;
  await user.click(startAgain);

  expect((await screen.findByRole("alert")).textContent).toContain('host "beta" is not attached');
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.queryByText("REMOTE-CODE")).toBeNull();
});

// L1, the other half of the same rule: a restart that DOES succeed opens the new
// flow's dialog rather than leaving the pane with no editor at all.
test("a successful 'Start again' opens the new flow's dialog", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  let restarted = false;
  fake.on("evener/host/request", (params) => {
    if (params.method === "evener/instance/list") return CODEX_HOST_LIST;
    if (params.method === "evener/auth/device/start") {
      if (restarted) return { ...REMOTE_DEVICE_START, flowId: "flow-restart", userCode: "RESTART-CODE" };
      return REMOTE_DEVICE_START;
    }
    if (params.method === "evener/auth/device/poll") return restarted ? REMOTE_POLL_PENDING : { state: "expired" };
    throw new Error(`unexpected forwarded method ${params.method}`);
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await user.click(await screen.findByRole("button", { name: "Sign in on host" }));

  await screen.findByText("REMOTE-CODE");
  const startAgain = await screen.findByRole("button", { name: "Start again" }, { timeout: 3000 });

  restarted = true;
  await user.click(startAgain);

  // The new flow's own dialog, and never the expired one's code.
  expect(await screen.findByText("RESTART-CODE")).toBeTruthy();
  expect(screen.queryByText("REMOTE-CODE")).toBeNull();
  expect(screen.getByRole("dialog")).toBeTruthy();
});

// L2 (roborev): the affordance follows the host's own gate, which is the resolved
// transport auth scheme (cmd/evener-hub/app_auth.go's requiresCodex ->
// instanceIsCodex: inst.Auth == registry.AuthOAuthOpenAICodex), not the provider
// id. `auth` is authored independently of `base`, so gating on the provider id
// offers the button to an openai-codex-based instance overridden to another
// scheme - whose only outcome is the host's refusal - and withholds it from an
// instance signed in through Codex OAuth on another base, which the host accepts.
test("'Sign in on host' follows the instance's auth scheme, not its provider id", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => ({
    instances: [
      instance({
        name: "oauth-on-openai",
        base: "openai",
        providerId: "openai",
        auth: "oauth-openai-codex",
        authModes: ["oauth"],
      }),
      instance({
        name: "codex-overridden",
        base: "openai-codex",
        providerId: "openai-codex",
        auth: "bearer",
        authModes: ["apiKey"],
      }),
    ],
    availableProviders: [],
  }));

  settingsHostStore.setState({ host: "beta" });
  render(<CredentialsHostScope sectionId="credentials" />);

  const remoteSection = await screen.findByRole("region", { name: "Providers on beta" });
  await within(remoteSection).findByText("oauth-on-openai");

  // The instance the host's gate accepts is the one offered the action...
  const acceptedRow = within(remoteSection).getByText("oauth-on-openai").closest("li");
  expect(within(acceptedRow as HTMLElement).getByRole("button", { name: "Sign in on host" })).toBeTruthy();
  // ...and the one it refuses is not offered it at all: the button could only
  // produce the host's own "OAuth is not supported for instance" refusal.
  const refusedRow = within(remoteSection).getByText("codex-overridden").closest("li");
  expect(within(refusedRow as HTMLElement).queryByRole("button", { name: "Sign in on host" })).toBeNull();
  expect(within(remoteSection).getAllByRole("button", { name: "Sign in on host" })).toHaveLength(1);
});

// L1, the fallback half of the same clause: a restart the host answers with
// `fallback` cannot open a browser on that host either, so it is the same failure
// - and it must clear the expired editor it replaced just the same.
test("a restart the host answers with fallback clears the expired dialog", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  let restarted = false;
  fake.on("evener/host/request", (params) => {
    if (params.method === "evener/instance/list") return CODEX_HOST_LIST;
    if (params.method === "evener/auth/device/start") return restarted ? REMOTE_DEVICE_FALLBACK : REMOTE_DEVICE_START;
    if (params.method === "evener/auth/device/poll") return { state: "expired" };
    throw new Error(`unexpected forwarded method ${params.method}`);
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await user.click(await screen.findByRole("button", { name: "Sign in on host" }));

  await screen.findByText("REMOTE-CODE");
  const startAgain = await screen.findByRole("button", { name: "Start again" }, { timeout: 3000 });

  restarted = true;
  await user.click(startAgain);

  expect((await screen.findByRole("alert")).textContent).toContain("Device-code sign-in is not enabled on beta");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.queryByText("REMOTE-CODE")).toBeNull();
});

// The M fix's other half: the key is the REGISTRATION identity, which deliberately
// excludes the live session state the hub reports on the same row
// (stores/hosts.ts's sameHostRegistration). A host coming back online is live
// state - it advances the registry revision and re-reads this host's listing - and
// it must not unmount an editor that is mid-flow: the unmount would cancel the
// poll with it, and the sign-in the user is completing would vanish from the pane.
test("a host coming back online does not restart the sign-in it is not part of", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: false })] }));
  fake.on(
    "evener/host/request",
    codexHostRequest(() => REMOTE_POLL_PENDING),
  );

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta (offline)" });
  await user.selectOptions(select, "beta");
  await user.click(await screen.findByRole("button", { name: "Sign in on host" }));
  await screen.findByText("REMOTE-CODE");
  await vi.waitFor(() => expect(forwardedMethodCalls(fake, "evener/auth/device/poll").length).toBeGreaterThan(0), {
    timeout: 3000,
  });
  const polls = forwardedMethodCalls(fake, "evener/auth/device/poll").length;

  // The host comes back: attachment is live session state on the same
  // registration, so the registry's next answer advances and beta's listing is
  // re-read - and the sign-in the user is completing is not part of that.
  act(() => {
    hostsStore.setState({ load: { phase: "ready", hosts: [hostRow({ name: "beta", attached: true })] } });
  });

  // The same editor, still showing this flow's code, and still polling the host
  // it was started on.
  expect(screen.getByText("REMOTE-CODE")).toBeTruthy();
  await vi.waitFor(() => expect(forwardedMethodCalls(fake, "evener/auth/device/poll").length).toBeGreaterThan(polls), {
    timeout: 3000,
  });
  expect(forwardedMethodCalls(fake, "evener/auth/device/poll").every((call) => call.host === "beta")).toBe(true);
});

// M (roborev round 6): two sign-in starts can be outstanding at once - a double
// click, or a second Codex row picked before the first start answers. Every
// start used to be applied by whichever answer arrived LAST, so a superseded
// start's late answer replaced the editor the user had actually asked for, and
// the dialog showed - and polled - the older instance's flow. The start's own
// generation decides now: only the newest start may open an editor or name a
// failure, and a superseded flow is never mounted, polled, or reported.
test("a superseded sign-in start never replaces the newer one, and its flow is never polled", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  const codexRows = {
    instances: [
      instance({
        name: "codex-one",
        providerId: "openai-codex",
        protocol: "openai-responses",
        auth: "oauth-openai-codex",
        authModes: ["oauth"],
      }),
      instance({
        name: "codex-two",
        providerId: "openai-codex",
        protocol: "openai-responses",
        auth: "oauth-openai-codex",
        authModes: ["oauth"],
      }),
    ],
    availableProviders: [],
  };
  // One deferred answer per instance name, so the test owns the order the two
  // starts resolve in - and can make the FIRST one resolve LAST.
  const starts = new Map<string, (value: unknown) => void>();
  fake.on("evener/host/request", (params) => {
    if (params.method === "evener/instance/list") return codexRows;
    if (params.method === "evener/auth/device/start") {
      const provider = (params.params as { provider: string }).provider;
      return new Promise<unknown>((resolve) => starts.set(provider, resolve)) as never;
    }
    if (params.method === "evener/auth/device/poll") return REMOTE_POLL_PENDING;
    throw new Error(`unexpected forwarded method ${params.method}`);
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  const remoteSection = await screen.findByRole("region", { name: "Providers on beta" });
  const firstRow = (await within(remoteSection).findByText("codex-one")).closest("li") as HTMLElement;
  const secondRow = within(remoteSection).getByText("codex-two").closest("li") as HTMLElement;

  // Both starts are on the wire before either answers: the first row's, then
  // the second row's.
  await user.click(within(firstRow).getByRole("button", { name: "Sign in on host" }));
  await vi.waitFor(() => expect(starts.has("codex-one")).toBe(true));
  await user.click(within(secondRow).getByRole("button", { name: "Sign in on host" }));
  await vi.waitFor(() => expect(starts.has("codex-two")).toBe(true));

  // The SECOND (last-clicked) start answers first; the superseded first one
  // answers after it.
  await act(async () => {
    starts.get("codex-two")?.({ ...REMOTE_DEVICE_START, flowId: "flow-two", userCode: "CODE-TWO" });
  });
  await act(async () => {
    starts.get("codex-one")?.({ ...REMOTE_DEVICE_START, flowId: "flow-one", userCode: "CODE-ONE" });
  });

  // The editor on screen is the last-clicked instance's, and the superseded
  // start's code never appears.
  expect(await screen.findByText("CODE-TWO")).toBeTruthy();
  expect(screen.queryByText("CODE-ONE")).toBeNull();

  // Long enough for a dialog holding flow-one to have ticked at the flow's own
  // 1s interval: the superseded flow is never polled, while the winner's is.
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 1500));
  });
  const polls = forwardedMethodCalls(fake, "evener/auth/device/poll");
  expect(polls.length).toBeGreaterThan(0);
  expect(polls.every((call) => (call.params as { flowId: string }).flowId === "flow-two")).toBe(true);
  expect(screen.queryByText("CODE-ONE")).toBeNull();
});

// L1 (roborev round 6): the fallback refusal sent the operator to
// EVENER_LOGIN_HEADLESS=1 on the host - an env var the hub's own DeviceStart
// never reads (auth/openai/device.go's issuer-404 path is what sets `fallback`;
// the variable is consumed by the `evener openai login` CLI), so the retry
// failed identically. The message names the thing that actually completes the
// sign-in: the CLI on the host itself, against that instance.
test("a host that offers no device flow names the host-side CLI, never a dead-end env var", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", (params) => {
    if (params.method === "evener/instance/list") return CODEX_HOST_LIST;
    if (params.method === "evener/auth/device/start") return REMOTE_DEVICE_FALLBACK;
    throw new Error(`unexpected forwarded method ${params.method}`);
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await user.click(await screen.findByRole("button", { name: "Sign in on host" }));

  const alert = await screen.findByRole("alert");
  // The remedy is the CLI on the host, naming the instance it signs in...
  expect(alert.textContent).toContain("evener openai login --instance codex --no-device");
  // ...and never the env var the hub does not read.
  expect(alert.textContent).not.toContain("EVENER_LOGIN_HEADLESS");
  expect(screen.queryByRole("dialog")).toBeNull();
});
