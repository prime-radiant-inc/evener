// @vitest-environment node

import { describe, expect, test } from "vitest";
import { ConnectionClosedError, friendlyErrorMessage, GENERIC_ERROR_MESSAGE, WireError } from "./errors";
import { createFrameworkFreeStore } from "./frameworkFreeStore";
import {
  APPLY_TIMEOUT_MS,
  createHubUpdateController,
  type HubUpdatePorts,
  type HubUpdateState,
  INITIAL_HUB_UPDATE_STATE,
  RESTART_REQUEST_TIMEOUT_MS,
  type RestartBaseline,
} from "./hubUpdate";
import type { UpdateCheckResponse } from "./types.gen";

const UP_TO_DATE: UpdateCheckResponse = {
  channel: "snapshot",
  buildChannel: "snapshot",
  currentVersion: "be70029",
  currentCommit: "be70029",
  latestTag: "snapshot",
  latestCommit: "be7002918fdc60dbdeab71d9dd17e00d3d006c56",
  updateAvailable: false,
  applicable: true,
  installAvailable: false,
  restartPending: false,
  startedAt: "2026-10-21T12:00:00Z",
  restartSupported: true,
  installTargetMatchesLaunch: true,
};
const WAITING: UpdateCheckResponse = {
  ...UP_TO_DATE,
  updateAvailable: true,
  installAvailable: true,
  latestCommit: "c0ffee",
};

interface Pending {
  method: string;
  params: unknown;
  opts?: { timeoutMs?: number };
  resolve(value: unknown): void;
  reject(error: unknown): void;
}

/** A hub whose every request waits for the test to settle it, and a restart
 * wait the test settles too. */
function scripted(options: { noClient?: boolean } = {}) {
  const pending: Pending[] = [];
  const restarts: { previous: RestartBaseline; resolve(back: boolean): void }[] = [];
  const ports: HubUpdatePorts = {
    client() {
      if (options.noClient) throw new Error("hubUpdate: no connected client");
      return {
        request: ((method: string, params: unknown, opts?: { timeoutMs?: number }) =>
          new Promise((resolve, reject) => pending.push({ method, params, opts, resolve, reject }))) as never,
      };
    },
    currentIncarnation: async () => "2026-10-21T10:00:00Z",
    awaitRestart: (previous) =>
      new Promise<boolean>((resolve) => {
        restarts.push({ previous, resolve });
      }),
  };
  const controller = createHubUpdateController(ports);
  const take = (method: string): Pending => {
    const index = pending.findIndex((request) => request.method === method);
    const found = pending[index];
    if (!found) throw new Error(`no pending ${method}`);
    pending.splice(index, 1);
    return found;
  };
  return { controller, pending, restarts, take };
}

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

async function checked(check: UpdateCheckResponse) {
  const hub = scripted();
  const running = hub.controller.runCheck();
  hub.take("evener/update/check").resolve(check);
  await running;
  return hub;
}

describe("runCheck", () => {
  test("asks for the selected channel, or the hub's own with none, and keeps the answer", async () => {
    const hub = scripted();
    const first = hub.controller.runCheck();
    const plain = hub.take("evener/update/check");
    expect(plain.params).toEqual({ channel: "" });
    plain.resolve(UP_TO_DATE);
    await first;
    expect(hub.controller.getState()).toMatchObject({ check: UP_TO_DATE, checking: false, checkError: null });
    hub.controller.setChannel("release");
    const second = hub.controller.runCheck();
    expect(hub.take("evener/update/check").params).toEqual({ channel: "release" });
    await Promise.race([second, settle()]);
  });

  test("lets only the newest of two overlapping checks write, and a late failure from the older writes nothing", async () => {
    const hub = scripted();
    const older = hub.controller.runCheck();
    const olderRequest = hub.take("evener/update/check");
    const newer = hub.controller.runCheck();
    hub.take("evener/update/check").resolve(WAITING);
    await newer;
    olderRequest.reject(new WireError("stale", -32000));
    await older;
    expect(hub.controller.getState()).toMatchObject({ check: WAITING, checkError: null });
  });

  test("keeps a WireError's own message, shows the generic message for anything else, and clears the old result", async () => {
    const hub = await checked(UP_TO_DATE);
    const refused = hub.controller.runCheck();
    expect(hub.controller.getState().check).toBeNull();
    hub.take("evener/update/check").reject(new WireError("update checks are disabled", -32000));
    await refused;
    expect(hub.controller.getState()).toMatchObject({
      check: null,
      checking: false,
      checkError: "update checks are disabled",
    });
    const broken = hub.controller.runCheck();
    hub.take("evener/update/check").reject(new Error("socket hung up"));
    await broken;
    expect(hub.controller.getState().checkError).toBe(GENERIC_ERROR_MESSAGE);
  });

  test("reports a check that was never sent", async () => {
    const hub = scripted({ noClient: true });
    await hub.controller.runCheck();
    expect(hub.controller.getState().checkError).toBe(GENERIC_ERROR_MESSAGE);
    expect(hub.pending).toHaveLength(0);
  });
});

describe("setChannel", () => {
  test("clears the result and a stale timed-out restart, and retires a check in flight", async () => {
    const hub = await checked(WAITING);
    const applying = hub.controller.apply();
    hub.take("evener/update/apply").resolve({ restarting: true });
    await settle();
    hub.restarts[0]?.resolve(false);
    await applying;
    expect(hub.controller.getState().restartTimedOut).toBe(true);
    const inFlight = hub.controller.runCheck();
    const request = hub.take("evener/update/check");
    hub.controller.setChannel("release");
    expect(hub.controller.getState()).toMatchObject({
      channel: "release",
      check: null,
      checking: false,
      checkError: null,
      applyError: null,
      restartTimedOut: false,
    });
    request.resolve(WAITING);
    await inFlight;
    expect(hub.controller.getState().check).toBeNull();
  });
});

describe("apply", () => {
  test("sends nothing without a check, or when nothing is waiting", async () => {
    const hub = scripted();
    await hub.controller.apply();
    expect(hub.controller.getState().applyError).toBe("Check for updates first");
    const upToDate = await checked(UP_TO_DATE);
    await upToDate.controller.apply();
    expect(upToDate.controller.getState().applyError).toBe("Already up to date");
    expect([...hub.pending, ...upToDate.pending].some((request) => request.method === "evener/update/apply")).toBe(
      false,
    );
  });

  test("sends nothing for an update this hub can't install itself", async () => {
    const hub = await checked({ ...WAITING, applicable: false, buildChannel: "dev" });
    await hub.controller.apply();
    expect(hub.controller.getState().applyError).toBe("This hub is a development build, so it can't update itself");
    expect(hub.pending.some((request) => request.method === "evener/update/apply")).toBe(false);
  });

  test("refuses a result from another channel", async () => {
    const hub = scripted();
    hub.controller.setChannel("release");
    const running = hub.controller.runCheck();
    hub.take("evener/update/check").resolve({ ...WAITING, channel: "snapshot" });
    await running;
    await hub.controller.apply();
    expect(hub.controller.getState().applyError).toBe("That result is stale; run a fresh check first");
    expect(hub.pending.some((request) => request.method === "evener/update/apply")).toBe(false);
  });

  test("sends apply with the channel and its long deadline, waits for the restart, and clears it when the hub is back", async () => {
    const hub = await checked(WAITING);
    const applying = hub.controller.apply();
    const request = hub.take("evener/update/apply");
    expect(request.params).toEqual({ channel: "" });
    expect(request.opts).toEqual({ timeoutMs: APPLY_TIMEOUT_MS });
    expect(hub.controller.getState().applying).toBe(true);
    request.resolve({ restarting: true });
    await settle();
    expect(hub.controller.getState()).toMatchObject({ applying: false, restarting: true });
    expect(hub.restarts.map((restart) => restart.previous)).toEqual([{ version: "be70029", incarnation: null }]);
    hub.restarts[0]?.resolve(true);
    await applying;
    expect(hub.controller.getState()).toMatchObject({ restarting: false, restartTimedOut: false });
  });

  test("says the restart timed out when the hub doesn't come back", async () => {
    const hub = await checked(WAITING);
    const applying = hub.controller.apply();
    hub.take("evener/update/apply").resolve({ restarting: true });
    await settle();
    hub.restarts[0]?.resolve(false);
    await applying;
    expect(hub.controller.getState()).toMatchObject({ restarting: false, restartTimedOut: true });
  });

  test("reports an answer of restarting: false as already up to date, stops offering it, and waits for nothing", async () => {
    const hub = await checked(WAITING);
    const applying = hub.controller.apply();
    hub.take("evener/update/apply").resolve({ restarting: false });
    await applying;
    expect(hub.controller.getState()).toMatchObject({ applying: false, applyError: "Already up to date" });
    // The hub re-checked and found nothing to install, so the kept check no
    // longer offers an update.
    expect(hub.controller.getState().check?.updateAvailable).toBe(false);
    expect(hub.restarts).toHaveLength(0);
  });

  test("waits for no restart once disposed while the apply was in flight", async () => {
    const hub = await checked(WAITING);
    const applying = hub.controller.apply();
    const request = hub.take("evener/update/apply");
    hub.controller.dispose();
    request.resolve({ restarting: true });
    await applying;
    expect(hub.restarts).toHaveLength(0);
  });

  test.each([
    ["no answer", undefined],
    ["an answer without restarting", {}],
  ])("reports %s to apply as an error, not a restart", async (_name, answer) => {
    const hub = await checked(WAITING);
    const applying = hub.controller.apply();
    hub.take("evener/update/apply").resolve(answer);
    await applying;
    expect(hub.controller.getState()).toMatchObject({ applying: false, restarting: false });
    expect(hub.controller.getState().applyError).toBe(GENERIC_ERROR_MESSAGE);
    expect(hub.restarts).toHaveLength(0);
  });

  test("clears the restart when the wait itself fails", async () => {
    const controller = createHubUpdateController({
      client: () => ({
        request: (async (method: string) =>
          method === "evener/update/apply" ? { restarting: true } : WAITING) as never,
      }),
      currentIncarnation: async () => null,
      awaitRestart: async () => {
        throw new Error("the wait broke");
      },
    });
    await controller.runCheck();
    await controller.apply();
    expect(controller.getState()).toMatchObject({ restarting: false, restartTimedOut: true });
  });

  test.each([
    ["a refusal from the hub", new WireError("download failed", -32000)],
    ["a closed connection", new ConnectionClosedError("connection closed")],
    ["a request that never left", new Error("cannot call evener/update/apply while state is closed")],
  ])("keeps %s an error, in the package's own words, and waits for nothing", async (_name, error) => {
    const hub = await checked(WAITING);
    const applying = hub.controller.apply();
    hub.take("evener/update/apply").reject(error);
    await applying;
    expect(hub.controller.getState()).toMatchObject({
      applying: false,
      restarting: false,
      applyError: friendlyErrorMessage(error),
    });
    expect(hub.restarts).toHaveLength(0);
  });

  test("waits for the restart when the answer was lost on the way", async () => {
    const hub = await checked(WAITING);
    const applying = hub.controller.apply();
    hub.take("evener/update/apply").reject(new Error("socket hung up"));
    await settle();
    expect(hub.controller.getState()).toMatchObject({ applying: false, restarting: true, applyError: null });
    hub.restarts[0]?.resolve(true);
    await applying;
    expect(hub.controller.getState().restarting).toBe(false);
  });

  test("sends nothing for a second apply while one is in flight", async () => {
    const hub = await checked(WAITING);
    const first = hub.controller.apply();
    await hub.controller.apply();
    expect(hub.pending.filter((request) => request.method === "evener/update/apply")).toHaveLength(1);
    hub.take("evener/update/apply").resolve({ restarting: false });
    await first;
  });

  test("reports an apply that was never sent as an error, not a restart", async () => {
    let connected = true;
    const restarts: RestartBaseline[] = [];
    const controller = createHubUpdateController({
      client() {
        if (!connected) throw new Error("hubUpdate: no connected client");
        return { request: (async () => WAITING) as never };
      },
      currentIncarnation: async () => null,
      awaitRestart: async (previous) => {
        restarts.push(previous);
        return true;
      },
    });
    await controller.runCheck();
    connected = false;
    await controller.apply();
    expect(controller.getState()).toMatchObject({
      applying: false,
      restarting: false,
      applyError: GENERIC_ERROR_MESSAGE,
    });
    expect(restarts).toEqual([]);
  });
});

describe("install", () => {
  test("installs with evener/upgrade and re-checks without restarting", async () => {
    const hub = await checked(WAITING);
    const installing = hub.controller.install();
    const request = hub.take("evener/upgrade");
    expect(request.params).toEqual({ requested: "" });
    expect(request.opts).toEqual({ timeoutMs: APPLY_TIMEOUT_MS });
    expect(hub.controller.getState().installing).toBe(true);
    request.resolve({});
    await settle();
    const recheck = hub.take("evener/update/check");
    recheck.resolve({ ...WAITING, onDiskCommit: "c0ffee", installAvailable: false, restartPending: true });
    await installing;
    expect(hub.controller.getState()).toMatchObject({
      installing: false,
      installError: null,
      check: { installAvailable: false, restartPending: true },
    });
    expect(hub.restarts).toHaveLength(0);
  });

  test("sends nothing without a check, for a dev build, or when nothing is installable", async () => {
    const none = scripted();
    await none.controller.install();
    expect(none.controller.getState().installError).not.toBeNull();

    const dev = await checked({ ...WAITING, applicable: false, buildChannel: "dev" });
    await dev.controller.install();
    expect(dev.controller.getState().installError).not.toBeNull();

    const current = await checked(UP_TO_DATE);
    await current.controller.install();
    expect(current.controller.getState().installError).not.toBeNull();

    expect([...none.pending, ...dev.pending, ...current.pending].some((r) => r.method === "evener/upgrade")).toBe(
      false,
    );
  });

  test("refuses a stale check and reports a failed install without re-checking", async () => {
    const stale = scripted();
    stale.controller.setChannel("release");
    const running = stale.controller.runCheck();
    stale.take("evener/update/check").resolve({ ...WAITING, channel: "snapshot" });
    await running;
    await stale.controller.install();
    expect(stale.controller.getState().installError).not.toBeNull();

    const failed = await checked(WAITING);
    const installing = failed.controller.install();
    failed.take("evener/upgrade").reject(new WireError("download failed", -32000));
    await installing;
    expect(failed.controller.getState()).toMatchObject({ installing: false, installError: "download failed" });
    expect(failed.pending.some((r) => r.method === "evener/update/check")).toBe(false);
  });

  test("sends one install while another is in flight", async () => {
    const hub = await checked(WAITING);
    const first = hub.controller.install();
    await hub.controller.install();
    expect(hub.pending.filter((r) => r.method === "evener/upgrade")).toHaveLength(1);
    hub.take("evener/upgrade").resolve({});
    await settle();
    hub.take("evener/update/check").resolve({ ...WAITING, installAvailable: false });
    await first;
  });

  test("re-checks after a lost install response instead of claiming failure", async () => {
    const hub = await checked(WAITING);
    const installing = hub.controller.install();
    hub.take("evener/upgrade").reject(new Error("socket hung up"));
    await installing;
    expect(hub.controller.getState()).toMatchObject({
      installing: false,
      installError: null,
      installReconcile: true,
    });
    expect(hub.pending.some((r) => r.method === "evener/update/check")).toBe(false);

    const recheck = hub.controller.runCheck();
    hub.take("evener/update/check").resolve({ ...WAITING, installAvailable: false, restartPending: true });
    await recheck;
    expect(hub.controller.getState()).toMatchObject({
      check: { installAvailable: false, restartPending: true },
      installReconcile: false,
    });
  });
});

describe("restart", () => {
  test("sends evener/hub/restart and waits for a new incarnation, even at the same version", async () => {
    const hub = await checked(WAITING);
    const restarting = hub.controller.restart();
    await settle();
    const request = hub.take("evener/hub/restart");
    expect(request.params).toEqual({});
    expect(request.opts).toEqual({ timeoutMs: RESTART_REQUEST_TIMEOUT_MS });
    expect(hub.controller.getState().restarting).toBe(true);
    request.resolve({ restarting: true, incarnation: "2026-10-21T11:00:00Z" });
    await settle();
    expect(hub.controller.getState().restarting).toBe(true);
    expect(hub.restarts.map((r) => r.previous)).toEqual([{ version: "be70029", incarnation: "2026-10-21T11:00:00Z" }]);
    hub.restarts[0]?.resolve(true);
    await restarting;
    expect(hub.controller.getState()).toMatchObject({ restarting: false, restartTimedOut: false });
  });

  test("runs a check first when there is no baseline", async () => {
    const hub = scripted();
    const restarting = hub.controller.restart();
    hub.take("evener/update/check").resolve({ ...WAITING, restartSupported: true });
    await settle();
    await settle();
    const request = hub.take("evener/hub/restart");
    request.resolve({ restarting: true });
    await settle();
    hub.restarts[0]?.resolve(true);
    await restarting;
    expect(hub.controller.getState().restarting).toBe(false);
  });

  test("two restarts started together send one request", async () => {
    const hub = scripted();
    const first = hub.controller.restart();
    const second = hub.controller.restart();
    // The operation is claimed before the baseline check, so the second call
    // returns without starting a check of its own.
    expect(hub.pending.filter((r) => r.method === "evener/update/check")).toHaveLength(1);

    hub.take("evener/update/check").resolve({ ...WAITING, restartSupported: true });
    await settle();
    await settle();
    const request = hub.take("evener/hub/restart");
    expect(hub.pending.some((r) => r.method === "evener/hub/restart")).toBe(false);

    request.resolve({ restarting: true });
    await settle();
    hub.restarts[0]?.resolve(true);
    await Promise.all([first, second]);
    expect(hub.controller.getState()).toMatchObject({ restarting: false, restartError: null });
  });

  test("refuses when the hub does not report restart support", async () => {
    const hub = await checked({ ...WAITING, restartSupported: undefined as unknown as boolean });
    await hub.controller.restart();
    expect(hub.controller.getState().restartError).not.toBeNull();
    expect(hub.pending.some((r) => r.method === "evener/hub/restart")).toBe(false);
  });

  test("keeps a server refusal an error and waits for nothing", async () => {
    const hub = await checked(WAITING);
    const restarting = hub.controller.restart();
    await settle();
    hub.take("evener/hub/restart").reject(new WireError("no launch path", -32000));
    await restarting;
    expect(hub.controller.getState()).toMatchObject({ restarting: false, restartError: "no launch path" });
    expect(hub.restarts).toHaveLength(0);
  });

  test("waits when the response was lost, and times out when the hub does not return", async () => {
    const hub = await checked(WAITING);
    const restarting = hub.controller.restart();
    await settle();
    hub.take("evener/hub/restart").reject(new Error("socket hung up"));
    await settle();
    expect(hub.controller.getState()).toMatchObject({ restarting: true, restartError: null });
    expect(hub.restarts[0]?.previous.incarnation).toBe("2026-10-21T10:00:00Z");
    hub.restarts[0]?.resolve(false);
    await restarting;
    expect(hub.controller.getState()).toMatchObject({ restarting: false, restartTimedOut: true });
  });
});

describe("an app's own state store", () => {
  test("keeps its state there, so the app's own writes are what the controller reads", async () => {
    const store = createFrameworkFreeStore<HubUpdateState & { label: string }>(() => ({
      ...INITIAL_HUB_UPDATE_STATE,
      label: "the app's own field",
    }));
    const controller = createHubUpdateController(
      {
        client: () => ({ request: (async () => ({ ...WAITING, channel: "snapshot" })) as never }),
        currentIncarnation: async () => null,
        awaitRestart: async () => true,
      },
      store,
    );
    controller.setChannel("snapshot");
    await controller.runCheck();
    expect(store.getState()).toMatchObject({ check: { channel: "snapshot" }, label: "the app's own field" });
    store.setState({ channel: "release" });
    await controller.apply();
    expect(store.getState().applyError).toBe("That result is stale; run a fresh check first");
  });
});

describe("dispose", () => {
  test("stops telling listeners", async () => {
    const hub = scripted();
    let heard = 0;
    hub.controller.subscribe(() => {
      heard += 1;
    });
    hub.controller.setChannel("release");
    expect(heard).toBe(1);
    const running = hub.controller.runCheck();
    const request = hub.take("evener/update/check");
    const beforeDispose = heard;
    hub.controller.dispose();
    hub.controller.setChannel("snapshot");
    request.resolve(UP_TO_DATE);
    await running;
    expect(heard).toBe(beforeDispose);
  });

  test("sends nothing once disposed, so a late tap can't act on a replaced connection", async () => {
    const hub = await checked(WAITING);
    hub.controller.dispose();
    await hub.controller.apply();
    await hub.controller.runCheck();
    expect(hub.pending).toHaveLength(0);
  });
});
