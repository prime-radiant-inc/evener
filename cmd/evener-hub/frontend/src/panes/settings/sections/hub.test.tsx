import { friendlyErrorMessage, type SettingsOverviewResponse, WireError } from "@evener/appwire-client";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test } from "vitest";
import { resetConnectionStoreForTests } from "../../../stores/connection";
import { resetDaemonResidentsStoreForTests } from "../../../stores/daemonResidents";
import { resetSettingsOverviewStoreForTests } from "../../../stores/settingsOverview";
import { connectReadyFakeClient } from "../../../testing/connectionTestUtils";
import { HubSection } from "./hub";
import { statusReasons } from "./reasonTestUtils";
import { settingsFieldIds, settingsFieldValue } from "./settingsFieldTestUtils";

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
  resetConnectionStoreForTests();
  resetSettingsOverviewStoreForTests();
  resetDaemonResidentsStoreForTests();
});

test("fetches the overview on mount, mounts hub sections, and omits technical runtime details", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => SAMPLE_RESPONSE);
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);

  await waitFor(() =>
    expect(fake.calls).toEqual([
      { method: "evener/settings/overview", params: {} },
      { method: "evener/daemon/list", params: {} },
    ]),
  );
  expect(screen.getByRole("region", { name: "Updates" })).toBeTruthy();
  expect(screen.getByRole("region", { name: "Resident daemons" })).toBeTruthy();
  expect(document.querySelector("details")).toBeNull();
  expect(settingsFieldIds()).toEqual([]);
  expect(screen.queryByText("127.0.0.1:9180")).toBeNull();
});

test("shows a loading skeleton before the overview resolves", () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => new Promise(() => {})); // never resolves
  // daemon/list is not called until settings/overview resolves (HubResidents mounts after data)

  render(<HubSection />);
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
  expect(screen.getAllByTestId("skeleton-line")).toHaveLength(3);
});

describe("on load failure", () => {
  // A plain Error's own message is internal detail (not something the hub
  // wrote for a person to read), so friendlyErrorMessage replaces it with a
  // generic sentence - see protocol/errors.test.ts for that contract.
  test("shows an inline error state with a retry action instead of a toast", async () => {
    const fake = connectReadyFakeClient();
    fake.on("evener/settings/overview", () => {
      throw new Error("raw-overview-failure");
    });
    // daemon/list is not called when overview fails (HubResidents never mounts)

    render(<HubSection />);

    await waitFor(() => expect(statusReasons(document.body)).toEqual(["unavailable"]));
    const notice = document.querySelector<HTMLElement>('[role="status"][data-reason="unavailable"]');
    if (notice === null) throw new Error("load failure shows no notice");
    // The region announces the failure; the retry control stays outside it.
    expect(within(notice).queryByRole("button")).toBeNull();
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
    expect(screen.queryByTestId("empty-state")).toBeNull();
    expect(screen.queryByText("raw-overview-failure", { exact: false })).toBeNull();
  });

  test("Retry re-requests the overview", async () => {
    const fake = connectReadyFakeClient();
    fake.on("evener/settings/overview", () => {
      throw new Error("raw-overview-failure");
    });
    const user = userEvent.setup();

    render(<HubSection />);
    await waitFor(() => expect(statusReasons(document.body)).toEqual(["unavailable"]));

    fake.on("evener/settings/overview", () => SAMPLE_RESPONSE);
    fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));
    await user.click(screen.getByRole("button", { name: "Retry" }));

    expect(await screen.findByRole("region", { name: "Updates" })).toBeTruthy();
    await waitFor(() => expect(fake.calls).toHaveLength(3));
  });

  test("a hub-reported WireError shows that error's friendly form", async () => {
    const fake = connectReadyFakeClient();
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

test("hides client internals when the overview transport becomes unreachable", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => {
    throw new Error('AppwireClient: cannot call "evener/settings/overview" while state is "closed"');
  });
  render(<HubSection />);
  await waitFor(() => expect(statusReasons(document.body)).toEqual(["unavailable"]));
  expect(screen.queryByText(/AppwireClient/i)).toBeNull();
});

test("reports the operation store's quarantine signal when the hub carries one", async () => {
  const fake = connectReadyFakeClient();
  const quarantined: SettingsOverviewResponse = {
    ...SAMPLE_RESPONSE,
    hub: {
      listenAddr: "127.0.0.1:9180",
      runDir: "/tmp/evener-run",
      spawnTimeout: "30s",
      bearerTokenAge: "fixture-token-age",
      daemonIdleTimeoutMillis: 3600000,
      operationStoreQuarantine: {
        quarantinedFile: "/fixture/state/hub-ops.json",
        asideFile: "/fixture/state/hub-ops.json.quarantined-20260101T000000.000000000Z",
        custodyFile: "/fixture/state/hub-ops.json.custody-20260101T000000.000000000Z.json",
        quarantineEpoch: 3,
      },
    },
  };
  fake.on("evener/settings/overview", () => quarantined);
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);

  await waitFor(() => expect(statusReasons()).toContain("operation-store-quarantined"));
  expect(settingsFieldValue("aside-file").textContent).toBe(
    "/fixture/state/hub-ops.json.quarantined-20260101T000000.000000000Z",
  );
  expect(settingsFieldValue("quarantined-file").textContent).toBe("/fixture/state/hub-ops.json");
});

test("reveals a quarantine the hub performed while this tab was on another section", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => SAMPLE_RESPONSE);
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  const first = render(<HubSection />);
  await waitFor(() => expect(fake.calls.length).toBeGreaterThan(0));
  expect(statusReasons()).not.toContain("operation-store-quarantined");
  first.unmount();

  const quarantined: SettingsOverviewResponse = {
    hub: {
      ...SAMPLE_RESPONSE.hub,
      daemonIdleTimeoutMillis: 3600000,
      operationStoreQuarantine: {
        quarantinedFile: "/fixture/state/hub-ops.json",
        asideFile: "/fixture/state/hub-ops.json.quarantined-20260101T000000.000000000Z",
        custodyFile: "/fixture/state/hub-ops.json.custody-20260101T000000.000000000Z.json",
        quarantineEpoch: 3,
      },
    },
  };
  await act(async () => {
    connectReadyFakeClient((fake2) => {
      fake2.on("evener/settings/overview", () => quarantined);
      fake2.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));
    });
  });

  render(<HubSection />);
  await waitFor(() => expect(statusReasons()).toContain("operation-store-quarantined"));
});

test("shows no quarantine notice for a hub whose store carries no signal", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => SAMPLE_RESPONSE);
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);

  await waitFor(() => expect(fake.calls.length).toBeGreaterThan(0));
  expect(statusReasons()).not.toContain("operation-store-quarantined");
});

test("names no set-aside file for a quarantine whose aside is already gone", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => ({
    hub: {
      ...SAMPLE_RESPONSE.hub,
      daemonIdleTimeoutMillis: 3600000,
      operationStoreQuarantine: {
        quarantinedFile: "/fixture/state/hub-ops.json",
        custodyFile: "/fixture/state/hub-ops.json.custody-20260101T000000.000000000Z.json",
        quarantineEpoch: 3,
      },
    },
  }));
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);

  await waitFor(() => expect(statusReasons()).toContain("operation-store-quarantined"));
  expect(settingsFieldIds()).not.toContain("aside-file");
  expect(settingsFieldValue("custody-file").textContent).toBe(
    "/fixture/state/hub-ops.json.custody-20260101T000000.000000000Z.json",
  );
});

const QUARANTINED: SettingsOverviewResponse = {
  hub: {
    ...SAMPLE_RESPONSE.hub,
    daemonIdleTimeoutMillis: 3600000,
    operationStoreQuarantine: {
      quarantinedFile: "/fixture/state/hub-ops.json",
      asideFile: "/fixture/state/hub-ops.json.quarantined-20260101T000000.000000000Z",
      custodyFile: "/fixture/state/hub-ops.json.custody-20260101T000000.000000000Z.json",
      quarantineEpoch: 3,
    },
  },
};

test("reveals a quarantine the hub performed during a reconnect this section opened into", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => SAMPLE_RESPONSE);
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  const first = render(<HubSection />);
  await waitFor(() => expect(fake.calls.length).toBeGreaterThan(0));
  first.unmount();

  fake.emitStateChange("reconnecting");
  fake.on("evener/settings/overview", () => QUARANTINED);
  render(<HubSection />);
  expect(statusReasons()).not.toContain("operation-store-quarantined");

  await act(async () => {
    fake.emitStateChange("ready");
  });

  await waitFor(() => expect(statusReasons()).toContain("operation-store-quarantined"));
});

test("re-reads on the next open after a reconnect whose refresh failed", async () => {
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => SAMPLE_RESPONSE);
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  const first = render(<HubSection />);
  await waitFor(() => expect(fake.calls.length).toBeGreaterThan(0));
  first.unmount();

  const fake2 = connectReadyFakeClient((replacement) => {
    replacement.on("evener/settings/overview", () => {
      throw new Error("overview request timed out");
    });
    replacement.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));
  });
  const stale = render(<HubSection />);
  await waitFor(() => expect(fake2.calls.length).toBeGreaterThan(0));
  expect(statusReasons()).not.toContain("operation-store-quarantined");
  stale.unmount();

  fake2.on("evener/settings/overview", () => QUARANTINED);
  render(<HubSection />);

  await waitFor(() => expect(statusReasons()).toContain("operation-store-quarantined"));
});
