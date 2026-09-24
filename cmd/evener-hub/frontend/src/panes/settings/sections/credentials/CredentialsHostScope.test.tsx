import type { HostRow, InstanceEntry } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { deferRequest, FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../../../stores/connection";
import {
  credentialsStore,
  resetCredentialsStoreForTests,
  resetHostInstancesForTests,
} from "../../../../stores/credentials";
import { hostsStore } from "../../../../stores/hosts";
import { setMutationClientIdentityForTests } from "../../../../stores/mutationClientIdentity";
import { resetSettingsHostForTests, settingsHostStore } from "../../../../stores/settingsHost";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
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
  expect(fake.calls.filter((call) => call.method === "evener/host/request")).toHaveLength(0);
});

// The fallback's SUCCESS path must be honest too: with a failed registry no
// identity ever arrives, so a read that answers can never be `verified`. It used
// to sit on a permanent skeleton, which reads to a user as "still working" - the
// same lie as showing the wrong host's data, only quieter.
test("a failed registry never leaves a successful read on a permanent skeleton", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => {
    throw new WireError("registry unavailable", -32000);
  });
  fake.on("evener/host/request", () => HOST_LIST);

  settingsHostStore.setState({ host: "beta" });
  render(<CredentialsHostScope sectionId="credentials" />);

  // The read reached the host and answered, but ownership cannot be checked:
  // the pane says so instead of spinning on a skeleton.
  expect(await screen.findByText(/hosts list/i)).toBeTruthy();
  expect(screen.queryByRole("status", { name: "Loading" })).toBeNull();
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
