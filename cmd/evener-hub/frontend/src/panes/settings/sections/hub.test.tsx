import { friendlyErrorMessage, type SettingsOverviewResponse, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test } from "vitest";
import { connectionStore } from "../../../stores/connection";
import { resetDaemonResidentsStoreForTests } from "../../../stores/daemonResidents";
import { resetSettingsOverviewStoreForTests } from "../../../stores/settingsOverview";
import { HubSection } from "./hub";
import { statusReasons } from "./reasonTestUtils";
import { settingsFieldHelp, settingsFieldIds, settingsFieldValue } from "./settingsFieldTestUtils";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

const SAMPLE_RESPONSE: SettingsOverviewResponse = {
  hub: {
    listenAddr: "127.0.0.1:9180",
    runDir: "/tmp/evener-run",
    spawnTimeout: "30s",
    bearerTokenAge: "fixture-token-age",
    daemonIdleTimeoutMillis: 3600000,
  },
};

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetSettingsOverviewStoreForTests();
  resetDaemonResidentsStoreForTests();
});

test("fetches the overview on mount and renders the runtime and authentication fields with their help text", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => SAMPLE_RESPONSE);
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);

  await userEvent.setup().click(await screen.findByText("Technical details", { selector: "summary" }));
  expect(await screen.findByText("127.0.0.1:9180")).toBeTruthy();
  expect(screen.queryByText("/tmp/evener-run")).toBeNull();
  expect(screen.getByText("30s")).toBeTruthy();
  expect(settingsFieldIds()).toEqual(["listen-address", "bearer-token", "spawn-timeout"]);
  for (const field of settingsFieldIds()) {
    expect(settingsFieldHelp(field ?? "")).not.toBeNull();
  }
  await waitFor(() =>
    expect(fake.calls).toEqual([
      { method: "evener/settings/overview", params: {} },
      { method: "evener/daemon/list", params: {} },
    ]),
  );
});

test("shows a loading skeleton before the overview resolves", () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => new Promise(() => {})); // never resolves
  // daemon/list is not called until settings/overview resolves (HubResidents mounts after data)

  render(<HubSection />);
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
});

describe("on load failure", () => {
  // A plain Error's own message is internal detail (not something the hub
  // wrote for a person to read), so friendlyErrorMessage replaces it with a
  // generic sentence - see protocol/errors.test.ts for that contract.
  test("shows an inline error state with a retry action instead of a toast", async () => {
    const fake = connectFakeClient();
    fake.on("evener/settings/overview", () => {
      throw new Error("raw-overview-failure");
    });
    // daemon/list is not called when overview fails (HubResidents never mounts)

    render(<HubSection />);

    await waitFor(() => expect(statusReasons(document.body)).toEqual(["unavailable"]));
    const notice = document.querySelector<HTMLElement>('[role="status"][data-reason="unavailable"]');
    if (notice === null) throw new Error("load failure shows no notice");
    expect(within(notice).getByRole("button", { name: "Retry" })).toBeTruthy();
    expect(screen.queryByTestId("empty-state")).toBeNull();
    expect(screen.queryByText("raw-overview-failure", { exact: false })).toBeNull();
  });

  test("Retry re-requests the overview", async () => {
    const fake = connectFakeClient();
    fake.on("evener/settings/overview", () => {
      throw new Error("raw-overview-failure");
    });
    const user = userEvent.setup();

    render(<HubSection />);
    await waitFor(() => expect(statusReasons(document.body)).toEqual(["unavailable"]));

    fake.on("evener/settings/overview", () => SAMPLE_RESPONSE);
    fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));
    await user.click(screen.getByRole("button", { name: "Retry" }));

    await userEvent.setup().click(await screen.findByText("Technical details", { selector: "summary" }));
    expect(await screen.findByText("127.0.0.1:9180")).toBeTruthy();
    await waitFor(() => expect(fake.calls).toHaveLength(3));
  });

  test("a hub-reported WireError shows that error's friendly form", async () => {
    const fake = connectFakeClient();
    const err = new WireError("overview index is rebuilding", -32000);
    fake.on("evener/settings/overview", () => {
      throw err;
    });

    render(<HubSection />);

    await waitFor(() => expect(statusReasons(document.body)).toEqual(["unavailable"]));
    const notice = document.querySelector('[role="status"][data-reason="unavailable"]');
    expect(notice?.textContent).toContain(friendlyErrorMessage(err));
  });
});

test.each(["fixture-token-age", undefined])("renders only a fixed token mask and optional age %s", async (age) => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => ({
    ...SAMPLE_RESPONSE,
    hub: { ...SAMPLE_RESPONSE.hub, daemonIdleTimeoutMillis: 3600000, bearerTokenAge: age },
  }));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));
  render(<HubSection />);
  await userEvent.setup().click(await screen.findByText("Technical details", { selector: "summary" }));
  await screen.findByText("127.0.0.1:9180");
  expect(settingsFieldValue("bearer-token").textContent?.trim()).toBe(age ? `•••••••••••• ${age}` : "••••••••••••");
});

test("hides client internals when the overview transport becomes unreachable", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => {
    throw new Error('AppwireClient: cannot call "evener/settings/overview" while state is "closed"');
  });
  render(<HubSection />);
  await waitFor(() => expect(statusReasons(document.body)).toEqual(["unavailable"]));
  expect(screen.queryByText(/AppwireClient/i)).toBeNull();
});

test("runtime details start folded while resident controls remain available", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => SAMPLE_RESPONSE);
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));
  render(<HubSection />);
  const toggle = await screen.findByText("Technical details", { selector: "summary" });
  expect(toggle.closest("details")?.open).toBe(false);
  expect(settingsFieldIds()).toEqual([]);
  await waitFor(() => expect(fake.calls.some((call) => call.method === "evener/daemon/list")).toBe(true));
  await userEvent.setup().click(toggle);
  expect(settingsFieldIds()).toContain("listen-address");
  expect(settingsFieldIds()).not.toContain("run-dir");
});
