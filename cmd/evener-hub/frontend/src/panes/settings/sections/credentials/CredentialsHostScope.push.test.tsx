import type { HostPushCredentialsResponse, HostRow, InstanceEntry } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { deferRequest, FakeClient } from "@evener/appwire-client/testing/fakeClient";
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
