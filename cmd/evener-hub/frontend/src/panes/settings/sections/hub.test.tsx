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
import { settingsFieldHelp, settingsFieldIds, settingsFieldValue } from "./settingsFieldTestUtils";

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

test("fetches the overview on mount and renders the runtime and authentication fields with their help text", async () => {
  const fake = connectReadyFakeClient();
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

    await userEvent.setup().click(await screen.findByText("Technical details", { selector: "summary" }));
    expect(await screen.findByText("127.0.0.1:9180")).toBeTruthy();
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

test.each(["fixture-token-age", undefined])("renders only a fixed token mask and optional age %s", async (age) => {
  const fake = connectReadyFakeClient();
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
  const fake = connectReadyFakeClient();
  fake.on("evener/settings/overview", () => {
    throw new Error('AppwireClient: cannot call "evener/settings/overview" while state is "closed"');
  });
  render(<HubSection />);
  await waitFor(() => expect(statusReasons(document.body)).toEqual(["unavailable"]));
  expect(screen.queryByText(/AppwireClient/i)).toBeNull();
});

test("runtime details start folded while resident controls remain available", async () => {
  const fake = connectReadyFakeClient();
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

  // The oracle is the notice's presence and its reason, not its wording: a
  // person opening Settings -> Hub must be able to see that the store was
  // quarantined. Copy changes must not fail this.
  await waitFor(() => expect(statusReasons()).toContain("operation-store-quarantined"));
  // And the path it shows must be the one holding the quarantined bytes. A row
  // that exists but points at the replacement passes any presence-only check.
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
  // The reader moves to another Settings section, so HubSection unmounts.
  first.unmount();

  // The hub restarted and quarantined the store while HubSection was gone.
  // fetch() is fetch-once, so a subscription in the section would miss this
  // reconnect entirely and reopening would reuse the healthy cache.
  // Typed, not spread through a partial: SettingsHubOverview's
  // daemonIdleTimeoutMillis is required, and a spread through the sample's
  // optional type would drop it.
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
  // A client replacement is the reconnect the recovery hook watches for; no
  // poll.
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
  // A row for a path the hub does not have would send an operator to a file
  // that is not there; the custody file is the only one left to name.
  expect(settingsFieldIds()).not.toContain("aside-file");
  expect(settingsFieldValue("custody-file").textContent).toBe(
    "/fixture/state/hub-ops.json.custody-20260101T000000.000000000Z.json",
  );
});

// The quarantine fixture every reconnect test below answers with: the store
// path the replacement occupies, the aside file holding the corrupt bytes, the
// custody file, and the epoch.
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

  // The hub restarts and quarantines the store. This tab's socket notices the
  // gap but is still reconnecting when the reader opens Hub settings, so the
  // section mounts INTO the flap and its mount read reuses the healthy cache.
  fake.emitStateChange("reconnecting");
  fake.on("evener/settings/overview", () => QUARANTINED);
  render(<HubSection />);
  expect(statusReasons()).not.toContain("operation-store-quarantined");

  // The same client recovers. Nothing else changes - no new client, no reopen -
  // and the notice must appear as soon as the re-read answers, because an
  // operator who opened the pane during the flap is exactly the one who was not
  // looking when it happened.
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

  // A replacement connection whose first overview read times out, with the
  // socket staying ready: the pane keeps the pre-restart overview on screen,
  // and that failed read must not count as having caught up.
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

  // The RPC answers again. Reopening must retry rather than trust a cache the
  // failed read never refreshed.
  fake2.on("evener/settings/overview", () => QUARANTINED);
  render(<HubSection />);

  await waitFor(() => expect(statusReasons()).toContain("operation-store-quarantined"));
});
