import type { HostPushCredentialsResponse, HostRow, InstanceEntry } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { deferRequest, FakeClient, gateSettlements } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../../../stores/connection";
import { resetCredentialsStoreForTests, resetHostInstancesForTests } from "../../../../stores/credentials";
import { hostsStore } from "../../../../stores/hosts";
import { setMutationClientIdentityForTests } from "../../../../stores/mutationClientIdentity";
import { resetSettingsHostForTests, settingsHostStore } from "../../../../stores/settingsHost";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
import { CredentialsHostScope } from "./CredentialsHostScope";

// The credential PUSH action on the remote-credentials surface (component 07c):
// the selected remote host is the copy's TARGET, this hub's local store is its
// source, and the report rendered is the push response's own per-entry report -
// one row per result, each carrying the host's own action verbatim. These tests
// pin the two easy-to-get-wrong properties deliberately: a REPORT MIX (skipped
// beside failed beside added) is never collapsed into one aggregate success, and
// an action string the four-value doc comment does not list renders exactly as
// the host sent it rather than being mapped onto a known label.

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
// A second remote host, so a switch moves the selection to a different host
// (not this hub) and the push action for it is offered the same way beta's is.
const GAMMA_LIST = {
  instances: [instance({ name: "on-gamma", providerId: "anthropic", authModes: ["apiKey"] })],
  availableProviders: [],
};

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
  resetHostInstancesForTests();
  resetToastStoreForTests();
  hostsStore.getState().resetForTests();
  resetSettingsHostForTests();
  window.history.pushState({}, "", "/settings/credentials");
  setMutationClientIdentityForTests("test-tab");
});

afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  window.history.pushState({}, "", "/");
});

test("this hub is offered no push affordance and issues no push call", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => {
    throw new Error("the local scope must never route through the proxy");
  });
  fake.on("evener/host/pushCredentials", () => {
    throw new Error("the local hub must never be the push's target");
  });

  render(<CredentialsHostScope sectionId="credentials" />);

  expect(await screen.findByText("controller-only")).toBeTruthy();
  expect(screen.queryByRole("button", { name: /push credentials/i })).toBeNull();
  expect(fake.calls.some((call) => call.method === "evener/host/pushCredentials")).toBe(false);
  expect(fake.calls.some((call) => call.method === "evener/host/request")).toBe(false);
});

test("the push targets the route-selected host, not the controller", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);
  let seen: unknown;
  fake.on("evener/host/pushCredentials", (params) => {
    seen = params;
    return { host: "beta", results: [{ instance: "on-beta", action: "added" }] };
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByRole("heading", { name: "Providers on beta" });

  // The selection is the route's own (stores/settingsHost.ts), so the push is
  // addressed by that same value.
  expect(settingsHostStore.getState().host).toBe("beta");

  await user.click(screen.getByRole("button", { name: "Push credentials to beta" }));

  await screen.findByRole("status", { name: "Push report for beta" });
  expect(seen).toEqual({ host: "beta" });
});

test("a mixed report renders one row per entry, never one aggregate success", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);
  fake.on("evener/host/pushCredentials", () => ({
    host: "beta",
    results: [
      { instance: "fresh-key", action: "added" },
      { instance: "guarded-key", action: "skipped", reason: "a source a pushed key must not shadow" },
      { instance: "changed-key", action: "failed", reason: "stale revision" },
    ],
  }));

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByRole("heading", { name: "Providers on beta" });
  await user.click(screen.getByRole("button", { name: "Push credentials to beta" }));

  const report = await screen.findByRole("status", { name: "Push report for beta" });
  const rows = within(report).getAllByRole("listitem");
  expect(rows).toHaveLength(3);
  // Each entry is rendered on its own: the failed entry is visible beside the
  // skipped one, so the report cannot read as one aggregate success.
  expect(rows.map((row) => row.textContent)).toEqual([
    "fresh-keyadded",
    "guarded-keyskippeda source a pushed key must not shadow",
    "changed-keyfailedstale revision",
  ]);
});

test("an unrecognised action string renders exactly as the host sent it", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);
  // "deferred" is not one of the four values the doc comment lists: a newer
  // host may introduce one, so it must be shown as-is, not mapped onto "skipped"
  // (nor silently dropped).
  fake.on("evener/host/pushCredentials", () => ({
    host: "beta",
    results: [{ instance: "future-key", action: "deferred", reason: "host is draining" }],
  }));

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByRole("heading", { name: "Providers on beta" });
  await user.click(screen.getByRole("button", { name: "Push credentials to beta" }));

  const report = await screen.findByRole("status", { name: "Push report for beta" });
  expect(within(report).getByRole("listitem").textContent).toBe("future-keydeferredhost is draining");
  expect(within(report).getByText("deferred")).toBeTruthy();
});

test("a result with no reason renders without an empty label or a stray separator", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);
  // A landed write carries no reason (the wire omits it).
  fake.on("evener/host/pushCredentials", () => ({
    host: "beta",
    results: [{ instance: "landed-key", action: "updated" }],
  }));

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByRole("heading", { name: "Providers on beta" });
  await user.click(screen.getByRole("button", { name: "Push credentials to beta" }));

  const report = await screen.findByRole("status", { name: "Push report for beta" });
  const row = within(report).getByRole("listitem");
  // Exactly instance + action, with no reason element and no separator left
  // behind (a trailing " . " would show up here).
  expect(row.textContent).toBe("landed-keyupdated");
});

test("no report renders until the push response lands", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);
  const release = deferRequest<HostPushCredentialsResponse>(fake, "evener/host/pushCredentials");

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByRole("heading", { name: "Providers on beta" });
  await user.click(screen.getByRole("button", { name: "Push credentials to beta" }));

  // In flight: the report is the RESPONSE's, so nothing is shown yet.
  expect(screen.queryByRole("status", { name: "Push report for beta" })).toBeNull();

  await act(async () => release({ host: "beta", results: [{ instance: "on-beta", action: "added" }] }));
  const report = await screen.findByRole("status", { name: "Push report for beta" });
  expect(within(report).getByRole("listitem").textContent).toBe("on-betaadded");
});

test("an unattached host surfaces a real error, not an empty report", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: false })] }));
  fake.on("evener/host/request", () => {
    throw new WireError('host "beta" is not attached', -32000);
  });
  fake.on("evener/host/pushCredentials", () => {
    throw new WireError('host "beta" is not attached', -32000);
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta (offline)" });
  await user.selectOptions(select, "beta");
  await screen.findByText(/host "beta" is not attached/);
  await user.click(screen.getByRole("button", { name: "Push credentials to beta" }));

  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toContain('host "beta" is not attached');
  expect(screen.queryByRole("status", { name: "Push report for beta" })).toBeNull();
});

test("a refused push surfaces a real error rather than an empty report", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);
  fake.on("evener/host/pushCredentials", () => {
    throw new WireError("remote dispatches are refused", -32000);
  });

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByRole("heading", { name: "Providers on beta" });
  await user.click(screen.getByRole("button", { name: "Push credentials to beta" }));

  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toContain("remote dispatches are refused");
  expect(screen.queryByRole("status", { name: "Push report for beta" })).toBeNull();
});

// MEDIUM 1: the action's whole lifetime is one host's. Switching the selection
// used to keep the previous host's report on screen, because PushCredentials was
// rendered without a key and its useState outlived the host prop.
test("a push report does not carry over when the selected host changes", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({
    hosts: [hostRow({ name: "beta", attached: true }), hostRow({ name: "gamma", attached: true })],
  }));
  fake.on("evener/host/request", (params) => (params.host === "beta" ? HOST_LIST : GAMMA_LIST));
  fake.on("evener/host/pushCredentials", () => ({
    host: "beta",
    results: [{ instance: "on-beta", action: "added" }],
  }));

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await screen.findByRole("option", { name: "gamma" });
  await user.selectOptions(select, "beta");
  await screen.findByRole("heading", { name: "Providers on beta" });
  await user.click(screen.getByRole("button", { name: "Push credentials to beta" }));
  const report = await screen.findByRole("status", { name: "Push report for beta" });
  expect(within(report).getByRole("listitem").textContent).toBe("on-betaadded");

  // The selection moves to another remote host.
  await user.selectOptions(select, "gamma");
  await screen.findByRole("heading", { name: "Providers on gamma" });

  // Beta's report is gone, and gamma's action is fresh: the state did not
  // outlive the host it belongs to.
  expect(screen.queryByRole("status", { name: "Push report for beta" })).toBeNull();
  expect((screen.getByRole("button", { name: "Push credentials to gamma" }) as HTMLButtonElement).disabled).toBe(false);
});

// MEDIUM 1 (the sharp edge): a push is left in flight, the selection moves on,
// and the push then fails. The old code interpolated the CURRENT host prop into
// the failure, so beta's failure rendered as though gamma had produced it.
test("a push failure that lands after a host switch is not rendered for the new host", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({
    hosts: [hostRow({ name: "beta", attached: true }), hostRow({ name: "gamma", attached: true })],
  }));
  fake.on("evener/host/request", (params) => (params.host === "beta" ? HOST_LIST : GAMMA_LIST));
  const settle = gateSettlements(fake, "evener/host/pushCredentials");

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await screen.findByRole("option", { name: "gamma" });
  await user.selectOptions(select, "beta");
  await screen.findByRole("heading", { name: "Providers on beta" });
  await user.click(screen.getByRole("button", { name: "Push credentials to beta" }));
  // The push is in flight when the selection moves; its pending state must not
  // disable the new host's action either.
  await act(async () => {});
  await user.selectOptions(select, "gamma");
  await screen.findByRole("heading", { name: "Providers on gamma" });
  expect((screen.getByRole("button", { name: "Push credentials to gamma" }) as HTMLButtonElement).disabled).toBe(false);
  expect(settle).toHaveLength(1);

  // Beta's push now fails, after the selection moved on.
  await act(async () => settle[0]!.reject(new WireError("beta refused the push", -32000)));

  // Nothing about beta's failure is rendered, least of all naming gamma.
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.queryByText(/Couldn't push credentials to gamma/)).toBeNull();
});

// MEDIUM 2: unverifiable means the listing was withheld because the host's name
// could not be checked against the registry, so the pane must not offer to send
// this hub's keys to a name it has just said it cannot verify - every sibling
// block in the section is guarded the same way.
test("the push action is not offered while the host is unverifiable", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => {
    throw new WireError("registry unavailable", -32000);
  });
  fake.on("evener/host/request", () => HOST_LIST);
  fake.on("evener/host/pushCredentials", () => {
    throw new Error("a host that cannot be verified must never be pushed to");
  });

  settingsHostStore.setState({ host: "beta" });
  render(<CredentialsHostScope sectionId="credentials" />);

  expect(await screen.findByText(/Couldn't check beta's registration/)).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Push credentials to beta" })).toBeNull();
  expect(fake.calls.some((call) => call.method === "evener/host/pushCredentials")).toBe(false);
});

// MEDIUM 3: a push performs many sequential remote operations over SSH, so the
// client's 30s default deadline can fire while the remote mutation is still
// going. It passes the longer host-mutation bound other host operations use
// (stores/hosts.ts's HOST_GATE_TIMEOUT_MS, 35 minutes) - asserted by value, not
// by the constant's existence.
test("the push call passes the host-mutation timeout, not the client's 30s default", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);
  fake.on("evener/host/pushCredentials", () => ({
    host: "beta",
    results: [{ instance: "on-beta", action: "added" }],
  }));

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByRole("heading", { name: "Providers on beta" });
  await user.click(screen.getByRole("button", { name: "Push credentials to beta" }));
  await screen.findByRole("status", { name: "Push report for beta" });

  const call = fake.calls.find((entry) => entry.method === "evener/host/pushCredentials");
  // 35 minutes, the value stores/hosts.ts's HOST_GATE_TIMEOUT_MS carries for a
  // host RPC that queues on or holds the per-host gate.
  expect(call?.opts).toEqual({ timeoutMs: 35 * 60_000 });
});

// LOW: the zero-result report is reachable (app_host_credentials.go returns an
// empty Results when this hub holds no local keys), and every other report shape
// is pinned. It must render its own status and no rows.
test("a zero-result report says there is nothing to push and renders no rows", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);
  fake.on("evener/host/pushCredentials", () => ({ host: "beta", results: [] }));

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByRole("heading", { name: "Providers on beta" });
  await user.click(screen.getByRole("button", { name: "Push credentials to beta" }));

  const report = await screen.findByRole("status", { name: "Push report for beta" });
  expect(report.textContent).toBe("Nothing to push to beta: this hub has no provider-instance keys.");
  expect(within(report).queryAllByRole("listitem")).toHaveLength(0);
});

test("a host re-registered under the same name does not keep the previous registration's report", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({
    hosts: [hostRow({ name: "beta", attached: true, address: "old.example" })],
  }));
  fake.on("evener/host/request", () => HOST_LIST);
  fake.on("evener/host/pushCredentials", () => ({
    host: "beta",
    results: [{ instance: "on-beta", action: "added" }],
  }));

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByRole("heading", { name: "Providers on beta" });

  await user.click(screen.getByRole("button", { name: "Push credentials to beta" }));
  await screen.findByRole("status", { name: "Push report for beta" });

  // The registry re-registers "beta" under the SAME name at a different address:
  // a different registration, whose own push state is its own. The selected host
  // string does not change here, so a key on the name alone would leave the old
  // registration's report standing under the new one.
  await act(async () => {
    hostsStore.setState({
      load: { phase: "ready", hosts: [hostRow({ name: "beta", attached: true, address: "new.example" })] },
    });
  });

  expect(screen.queryByRole("status", { name: "Push report for beta" })).toBeNull();
});

// MEDIUM 4 (roborev on 851b2e8): the action's state was keyed only by the host
// registration, so it did not notice that its request had settled on a REPLACED
// connection - the old client's report, or its failure, rendered as though it
// belonged to the connection that replaced it. A push is a MUTATION, so the
// attempt is scoped to the connection generation it was issued under (the same
// notion stores/credentials.ts's host partitions are current under): a
// settlement from a generation the action is no longer on is dropped rather
// than shown, and the attempt reads as an outcome this connection never saw.
const REPLACED_CONNECTION_NOTICE = /was replaced while the push to beta was in flight/;

function connectReplacementClient(): FakeClient {
  const replacement = new FakeClient("ready");
  replacement.on("evener/instance/list", () => CONTROLLER_LIST);
  replacement.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  replacement.on("evener/host/request", () => HOST_LIST);
  connectionStore.getState().connect(replacement);
  return replacement;
}

test("a push in flight when the connection is replaced is not reported as the new connection's outcome", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);
  const settle = gateSettlements(fake, "evener/host/pushCredentials");

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByRole("heading", { name: "Providers on beta" });
  await user.click(screen.getByRole("button", { name: "Push credentials to beta" }));
  await act(async () => {});
  expect(settle).toHaveLength(1);

  // The controller's client is replaced while the push is still out.
  let replacement!: FakeClient;
  await act(async () => {
    replacement = connectReplacementClient();
  });

  // The replaced connection's own answer arrives now. It describes what THAT
  // connection's push did, and nothing on this one can vouch for it.
  await act(async () =>
    settle[0]!.resolve({ host: "beta", results: [{ instance: "old-connection-key", action: "added" }] }),
  );

  expect(screen.queryByRole("status", { name: "Push report for beta" })).toBeNull();
  expect(screen.queryByText("old-connection-key")).toBeNull();
  // The attempt is readable as what it is, rather than as a success or a
  // failure of the connection that replaced it...
  expect(screen.getByText(REPLACED_CONNECTION_NOTICE)).toBeTruthy();
  // ...and the mutation was not sent a second time behind the user's back.
  expect(settle).toHaveLength(1);
  expect(
    fake.calls.filter((call) => call.method === "evener/host/pushCredentials"),
    "the replaced connection's push is settled once and never re-issued",
  ).toHaveLength(1);
  expect(replacement.calls.some((call) => call.method === "evener/host/pushCredentials")).toBe(false);
  // The new connection's own push is offered again: an unknown outcome is not a
  // dead end the user is stuck behind.
  expect((screen.getByRole("button", { name: "Push credentials to beta" }) as HTMLButtonElement).disabled).toBe(false);
});

test("a push failure that lands on a replaced connection is not rendered as the new connection's failure", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);
  const settle = gateSettlements(fake, "evener/host/pushCredentials");

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByRole("heading", { name: "Providers on beta" });
  await user.click(screen.getByRole("button", { name: "Push credentials to beta" }));
  await act(async () => {});
  expect(settle).toHaveLength(1);

  await act(async () => {
    connectReplacementClient();
  });
  await act(async () => settle[0]!.reject(new WireError("the replaced connection refused the push", -32000)));

  // The failure belongs to the connection that is gone, so it is not rendered
  // as this one's - the attempt reads as an unknown outcome instead.
  expect(screen.queryByText(/Couldn't push credentials to beta/)).toBeNull();
  expect(screen.queryByText(/the replaced connection refused the push/)).toBeNull();
  expect(screen.getByText(REPLACED_CONNECTION_NOTICE)).toBeTruthy();
});

test("the connection that issued a push still renders that push's own report", async () => {
  const fake = connectFakeClient();
  fake.on("evener/instance/list", () => CONTROLLER_LIST);
  fake.on("evener/host/list", () => ({ hosts: [hostRow({ name: "beta", attached: true })] }));
  fake.on("evener/host/request", () => HOST_LIST);
  const settle = gateSettlements(fake, "evener/host/pushCredentials");

  render(<CredentialsHostScope sectionId="credentials" />);
  const user = userEvent.setup();
  const select = await screen.findByLabelText("Host");
  await screen.findByRole("option", { name: "beta" });
  await user.selectOptions(select, "beta");
  await screen.findByRole("heading", { name: "Providers on beta" });
  await user.click(screen.getByRole("button", { name: "Push credentials to beta" }));
  await act(async () => {});

  // The first attempt is orphaned by a replacement - the guard's own case - and
  // its answer is still out, held for the end of this test.
  let replacement!: FakeClient;
  await act(async () => {
    replacement = connectReplacementClient();
  });
  expect(screen.getByText(REPLACED_CONNECTION_NOTICE)).toBeTruthy();

  // The guard cannot pass by never showing anything: a push issued on the
  // connection that is CURRENT renders that connection's own report.
  replacement.on("evener/host/pushCredentials", () => ({
    host: "beta",
    results: [{ instance: "new-connection-key", action: "updated" }],
  }));
  await user.click(screen.getByRole("button", { name: "Push credentials to beta" }));

  const report = await screen.findByRole("status", { name: "Push report for beta" });
  expect(within(report).getByRole("listitem").textContent).toBe("new-connection-keyupdated");
  expect(screen.queryByText(REPLACED_CONNECTION_NOTICE)).toBeNull();
  expect(replacement.calls.some((call) => call.method === "evener/host/pushCredentials")).toBe(true);

  // The replaced connection's answer lands only now - after the new
  // connection's own report is on screen - and it does not displace it: it
  // belongs to an attempt this action no longer holds, and the second push went
  // to the current connection, not through the old one.
  expect(settle).toHaveLength(1);
  await act(async () =>
    settle[0]!.resolve({ host: "beta", results: [{ instance: "old-connection-key", action: "added" }] }),
  );
  const rows = within(screen.getByRole("status", { name: "Push report for beta" })).getAllByRole("listitem");
  expect(rows.map((row) => row.textContent)).toEqual(["new-connection-keyupdated"]);
  expect(screen.queryByText("old-connection-key")).toBeNull();
  expect(screen.queryByText(REPLACED_CONNECTION_NOTICE)).toBeNull();
});
