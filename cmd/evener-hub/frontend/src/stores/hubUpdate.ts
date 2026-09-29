// hubUpdate.ts drives Settings -> Hub -> Updates: pick a release channel,
// ask the hub whether that channel is ahead of the running build
// (evener/update/check), and apply it (evener/update/apply). The check and
// apply are the package's createHubUpdateController, shared with the phone;
// this store wraps it for React and supplies the web's two ports. Apply is the
// interesting half: the hub answers, then execs the new binary in place, so
// the WebSocket drops and the page has to notice the NEW hub on its own.
// It does that by polling /api/health (auth-exempt, always registered - see
// shell/chrome/webNotBuilt.ts's note) until `version` differs from the
// value the check recorded, then reloads so the SPA bundle matches the
// backend. Fetch failures while polling are the expected mid-restart state.
//
// Like settingsOverview.ts, this store reads connectionStore's current
// client at call time and holds no subscription of its own.

import {
  createHubUpdateController,
  type HubUpdateController,
  type HubUpdateState,
  INITIAL_HUB_UPDATE_STATE,
  type UpdateChannel,
} from "@evener/appwire-client";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { reloadPage } from "../shell/pageReload";
import { connectedClientPort } from "./connection";

export type { UpdateChannel } from "@evener/appwire-client";
export { APPLY_TIMEOUT_MS } from "@evener/appwire-client";

export const RESTART_POLL_MS = 1000;
export const RESTART_TIMEOUT_MS = 30_000;

export interface HubUpdateStoreState extends HubUpdateState {
  setChannel(channel: UpdateChannel): void;
  runCheck(): Promise<void>;
  apply(): Promise<void>;
}

interface Deps {
  fetchImpl: typeof fetch;
  reload: () => void;
}

let deps: Deps = { fetchImpl: (...args) => fetch(...args), reload: reloadPage };

const { requireClient } = connectedClientPort("hubUpdate");

// healthVersion gives up after timeoutMs so a hub that accepts the
// connection and then never answers cannot outlive RESTART_TIMEOUT_MS.
async function healthVersion(timeoutMs: number): Promise<string | null> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const response = await deps.fetchImpl("/api/health", {
      credentials: "same-origin",
      cache: "no-store",
      signal: controller.signal,
    });
    if (!response.ok) return null;
    const body = (await response.json()) as { version?: string };
    return typeof body.version === "string" ? body.version : null;
  } catch {
    return null; // the hub is mid-restart or the attempt timed out; keep polling
  } finally {
    clearTimeout(timer);
  }
}

// waitForNewHub is the web's restart wait: it polls until /api/health
// reports a version other than previous, then reloads and resolves true, or
// resolves false after RESTART_TIMEOUT_MS.
async function waitForNewHub(previous: string | null): Promise<boolean> {
  const deadline = Date.now() + RESTART_TIMEOUT_MS;
  while (Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, RESTART_POLL_MS));
    const version = await healthVersion(Math.max(0, deadline - Date.now()));
    if (version !== null && version !== previous) {
      deps.reload();
      return true;
    }
  }
  return false;
}

// The controller keeps its state in this store itself, so a write to the
// store (a test's setState) is what the controller reads next.
let controller: HubUpdateController;

export const hubUpdateStore = createStore<HubUpdateStoreState>(() => ({
  ...INITIAL_HUB_UPDATE_STATE,
  setChannel: (channel) => controller.setChannel(channel),
  runCheck: () => controller.runCheck(),
  apply: () => controller.apply(),
}));

function createController(): HubUpdateController {
  return createHubUpdateController({ client: requireClient, awaitRestart: waitForNewHub }, hubUpdateStore);
}

controller = createController();

export function useHubUpdateStore(): HubUpdateStoreState;
export function useHubUpdateStore<T>(selector: (state: HubUpdateStoreState) => T): T;
export function useHubUpdateStore<T>(selector?: (state: HubUpdateStoreState) => T): T | HubUpdateStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation (zustand's useStore has a
  // `selector = identity` JS default param, so both arms run identically).
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(hubUpdateStore, selector) : useStore(hubUpdateStore);
}

// resetHubUpdateStoreForTests resets state and lets tests inject the
// health fetch and reload so the restart poll runs under fake timers with
// no real network and no real navigation. It builds a fresh controller, so no
// check or apply from an earlier test writes into this one. Production never
// calls this.
export function resetHubUpdateStoreForTests(overrides: Partial<Deps> = {}): void {
  deps = {
    fetchImpl: overrides.fetchImpl ?? ((...args) => fetch(...args)),
    reload: overrides.reload ?? reloadPage,
  };
  controller.dispose();
  controller = createController();
  hubUpdateStore.setState({ ...INITIAL_HUB_UPDATE_STATE });
}
