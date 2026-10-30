// The hub's own update, shared by the web's Settings -> Hub -> Updates and the
// phone's Hub. Install writes a verified artifact; restart execs the binary
// already on disk. They are separate actions: install never restarts, and
// restart works on a dev build that was rebuilt locally.
//
// A restart is confirmed by process incarnation, not build version: a rebuild
// at the same commit changes no version string. Hubs that predate
// `evener/hub/restart` report restartSupported: false, and the host keeps the
// old combined `evener/update/apply` path available through the deprecated
// apply() method.
//
// Noticing the restarted hub is the host's job (a health poll and a page
// reload on the web, a reconnect and a fresh check on the phone), so it is a
// port, awaitRestart, like every host API the package touches. Pure logic: no
// timers, no fetch.

import type { AppwireClientLike } from "./clientLike";
import { ConnectionClosedError, friendlyErrorMessage, WireError } from "./errors";
import { createFrameworkFreeStore } from "./frameworkFreeStore";
import type { UpdateCheckResponse } from "./types.gen";

export type UpdateChannel = "release" | "snapshot";

// APPLY_TIMEOUT_MS bounds the install RPC. The hub enforces one overall
// four-minute deadline over archive + checksums downloads, verify, and install,
// so six minutes here clears the server bound with headroom. The same bound
// covers the deprecated apply path.
export const APPLY_TIMEOUT_MS = 6 * 60_000;

// RESTART_REQUEST_TIMEOUT_MS bounds the restart RPC itself, which only probes
// the launch path and schedules the exec.
export const RESTART_REQUEST_TIMEOUT_MS = 30_000;

const ALREADY_UP_TO_DATE = "Already up to date";
const STALE_CHECK = "That result is stale; run a fresh check first";
const DEV_INSTALL = "This hub is a development build, so it can't install an update";
const CHECK_FIRST = "Check for updates first";
const NO_RESTART = "This hub doesn't support restart";

// RestartBaseline describes the process the app is leaving. Hosts compare the
// incarnation when the hub supplies one, and fall back to the version for an
// older hub that only has the deprecated apply path.
export interface RestartBaseline {
  version: string | null;
  incarnation: string | null;
}

export interface HubUpdateState {
  // null until the app seeds it (the web from the overview's buildChannel);
  // the hub then falls back to its own upgrade channel for an empty request.
  channel: UpdateChannel | null;
  check: UpdateCheckResponse | null;
  checking: boolean;
  checkError: string | null;
  installing: boolean;
  installError: string | null;
  /** A lost install response still needs a fresh check; the host retries it
   * when its connection is ready rather than racing a reconnect. */
  installReconcile: boolean;
  /** @deprecated State for the combined apply path. */
  applying: boolean;
  /** @deprecated State for the combined apply path. */
  applyError: string | null;
  restarting: boolean;
  restartError: string | null;
  restartTimedOut: boolean;
}

export interface HubUpdatePorts {
  /** The connected client. Throws when there is none, so a request that was
   * never sent reports as an error. */
  client(): Pick<AppwireClientLike, "request">;
  /** The current process incarnation, captured immediately before a restart
   * so a lost response can still be judged against the process it replaced.
   * Hosts without a health read may return null and fall back to the check. */
  currentIncarnation(): Promise<string | null>;
  /** Runs once the hub answered that it is restarting, or its answer was
   * lost on the way. Resolves true once a different process is up, false when
   * it didn't come back. */
  awaitRestart(previous: RestartBaseline): Promise<boolean>;
}

export interface HubUpdateController {
  getState(): HubUpdateState;
  subscribe(listener: () => void): () => void;
  setChannel(channel: UpdateChannel): void;
  runCheck(): Promise<void>;
  install(): Promise<void>;
  restart(): Promise<void>;
  /** @deprecated Use install() then restart(). Kept for older hubs and
   * out-of-tree hosts that still call the combined RPC. */
  apply(): Promise<void>;
  /** Stops telling listeners and sends nothing more; answers still in
   * flight change nothing. */
  dispose(): void;
}

/** Where the controller keeps its state. By default a store of its own; an
 * app whose view layer already owns a store (the web's zustand store, whose
 * tests write to it directly) passes that one, so there is one state, not a
 * copy to keep in step. */
export interface HubUpdateStateStore {
  getState(): HubUpdateState;
  setState(partial: Partial<HubUpdateState>): void;
  subscribe(listener: () => void): () => void;
}

export const INITIAL_HUB_UPDATE_STATE: HubUpdateState = {
  channel: null,
  check: null,
  checking: false,
  checkError: null,
  installing: false,
  installError: null,
  installReconcile: false,
  applying: false,
  applyError: null,
  restarting: false,
  restartError: null,
  restartTimedOut: false,
};

export function createHubUpdateController(
  ports: HubUpdatePorts,
  store: HubUpdateStateStore = createFrameworkFreeStore<HubUpdateState>(() => ({ ...INITIAL_HUB_UPDATE_STATE })),
): HubUpdateController {
  let disposed = false;
  const set = (partial: Partial<HubUpdateState>) => {
    if (!disposed) store.setState(partial);
  };
  const get = store.getState;
  // checkSequence orders overlapping runCheck calls: only the newest request
  // may write its result, so a slow earlier check cannot clobber a later one.
  // setChannel bumps it too, which retires any check still in flight.
  let checkSequence = 0;
  // restartClaimed covers the window before state.restarting turns true:
  // without it, two calls started together both await a baseline check and
  // then both send evener/hub/restart.
  let restartClaimed = false;

  function isStale(check: UpdateCheckResponse): boolean {
    return get().channel !== null && check.channel !== get().channel;
  }

  async function waitForRestart(previous: RestartBaseline): Promise<void> {
    // A wait that fails is a hub that didn't come back as far as the app can
    // tell: never leave the restart showing.
    const back = await ports.awaitRestart(previous).catch(() => false);
    set(back ? { restarting: false } : { restarting: false, restartTimedOut: true });
  }

  // A refusal or a request that never left is final; a transport loss may be
  // a successful exec whose response frame was lost.
  function requestOutcome(err: unknown): "never-sent" | "refused" | "lost" {
    if (err instanceof ConnectionClosedError || (err instanceof Error && /cannot call/i.test(err.message))) {
      return "never-sent";
    }
    if (err instanceof WireError) return "refused";
    return "lost";
  }

  async function runCheck(): Promise<void> {
    // A disposed controller's connection was replaced: it asks nothing.
    if (disposed) return;
    // Invalidate the previous result first: a manual re-check must not leave a
    // stale positive in state while the new check is in flight.
    set({ checking: true, check: null, checkError: null, restartTimedOut: false });
    const seq = ++checkSequence;
    try {
      const check = await ports.client().request("evener/update/check", { channel: get().channel ?? "" });
      if (seq === checkSequence) set({ check, checking: false, installReconcile: false });
    } catch (err) {
      if (seq === checkSequence) set({ check: null, checking: false, checkError: friendlyErrorMessage(err) });
    }
  }

  async function install(): Promise<void> {
    if (disposed || get().installing || get().restarting || get().applying) return;
    const check = get().check;
    if (!check) {
      set({ installError: CHECK_FIRST });
      return;
    }
    if (isStale(check)) {
      set({ installError: STALE_CHECK });
      return;
    }
    if (!check.applicable) {
      set({ installError: DEV_INSTALL });
      return;
    }
    // An older hub omits installAvailable; its updateAvailable is the fallback.
    if (!(check.installAvailable ?? check.updateAvailable)) {
      set({ installError: ALREADY_UP_TO_DATE });
      return;
    }
    set({ installing: true, installError: null, restartTimedOut: false });
    let client: Pick<AppwireClientLike, "request">;
    try {
      client = ports.client();
    } catch (err) {
      set({ installing: false, installError: friendlyErrorMessage(err) });
      return;
    }
    try {
      await client.request("evener/upgrade", { requested: get().channel ?? "" }, { timeoutMs: APPLY_TIMEOUT_MS });
    } catch (err) {
      if (requestOutcome(err) !== "lost") {
        set({ installing: false, installError: friendlyErrorMessage(err) });
        return;
      }
      // The hub may have installed the artifact before the response frame was
      // lost. Ask the host to reconcile once its connection is ready; an
      // immediate check races a reconnect and is lost with the socket.
      set({ installing: false, installReconcile: true });
      return;
    }
    if (disposed) return;
    set({ installing: false });
    // The check owns installAvailable, onDiskCommit, and restartPending; a
    // fresh one is what turns the section into "installed but not running".
    await runCheck();
  }

  async function restart(): Promise<void> {
    if (disposed || get().restarting || get().installing || get().applying) return;
    if (restartClaimed) return;
    restartClaimed = true;
    try {
      await restartOnce();
    } finally {
      restartClaimed = false;
    }
  }

  async function restartOnce(): Promise<void> {
    let check = get().check;
    if (!check) {
      // A restart needs a baseline; the check is also what tells the app
      // whether this hub supports the split at all.
      await runCheck();
      check = get().check;
    }
    if (!check) {
      set({ restartError: CHECK_FIRST });
      return;
    }
    if (check.restartSupported !== true) {
      set({ restartError: NO_RESTART });
      return;
    }
    set({ restarting: true, restartError: null, restartTimedOut: false });
    let capturedIncarnation: string | null = null;
    try {
      capturedIncarnation = await ports.currentIncarnation();
    } catch {
      capturedIncarnation = null;
    }
    if (disposed) return;
    let client: Pick<AppwireClientLike, "request">;
    try {
      client = ports.client();
    } catch (err) {
      set({ restarting: false, restartError: friendlyErrorMessage(err) });
      return;
    }
    let responseIncarnation: string | null = null;
    try {
      const resp: unknown = await client.request("evener/hub/restart", {}, { timeoutMs: RESTART_REQUEST_TIMEOUT_MS });
      const raw = (resp as { incarnation?: unknown } | null)?.incarnation;
      if (typeof raw === "string") responseIncarnation = raw;
    } catch (err) {
      if (requestOutcome(err) !== "lost") {
        set({ restarting: false, restartError: friendlyErrorMessage(err) });
        return;
      }
    }
    if (disposed) return;
    // The response names the process that is being replaced. Use it instead
    // of a possibly stale check baseline; a lost response falls back to the
    // check.
    const previous: RestartBaseline = {
      version: check.currentVersion,
      incarnation: responseIncarnation ?? capturedIncarnation ?? check.startedAt ?? null,
    };
    await waitForRestart(previous);
  }

  async function apply(): Promise<void> {
    if (disposed || get().applying || get().restarting || get().installing) return;
    const check = get().check;
    if (!check) {
      set({ applyError: CHECK_FIRST });
      return;
    }
    if (isStale(check)) {
      set({ applyError: STALE_CHECK });
      return;
    }
    if (!check.applicable) {
      set({ applyError: "This hub is a development build, so it can't update itself" });
      return;
    }
    if (!check.updateAvailable) {
      set({ applyError: ALREADY_UP_TO_DATE });
      return;
    }
    set({ applying: true, applyError: null, restartTimedOut: false });
    // The deprecated combined path keeps its version comparison: it is what
    // older hubs and hosts understand, and they may not report an incarnation.
    const previous: RestartBaseline = { version: check.currentVersion, incarnation: null };
    let client: Pick<AppwireClientLike, "request">;
    try {
      client = ports.client();
    } catch (err) {
      set({ applying: false, applyError: friendlyErrorMessage(err) });
      return;
    }
    try {
      const resp: unknown = await client.request(
        "evener/update/apply",
        { channel: get().channel ?? "" },
        { timeoutMs: APPLY_TIMEOUT_MS },
      );
      if (typeof (resp as { restarting?: unknown } | null)?.restarting !== "boolean") {
        set({ applying: false, applyError: friendlyErrorMessage(new Error("unreadable update answer")) });
        return;
      }
      if (!(resp as { restarting: boolean }).restarting) {
        set({ applying: false, applyError: ALREADY_UP_TO_DATE, check: { ...check, updateAvailable: false } });
        return;
      }
    } catch (err) {
      if (requestOutcome(err) !== "lost") {
        set({ applying: false, applyError: friendlyErrorMessage(err) });
        return;
      }
    }
    if (disposed) return;
    set({ applying: false, restarting: true });
    await waitForRestart(previous);
  }

  return {
    getState: get,
    subscribe: (listener) =>
      store.subscribe(() => {
        if (!disposed) listener();
      }),

    setChannel(channel) {
      checkSequence++;
      set({
        channel,
        check: null,
        checking: false,
        checkError: null,
        installError: null,
        applyError: null,
        restartError: null,
        restartTimedOut: false,
      });
    },

    runCheck,
    install,
    restart,
    apply,

    dispose() {
      disposed = true;
    },
  };
}
