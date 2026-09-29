// The hub's own update, shared by the web's Settings -> Hub -> Updates and the
// phone's Hub (Jesse, 2026-09-26: "should use a shared api"). Pick a release
// channel, ask the hub whether that channel is ahead of the running build
// (evener/update/check), and apply it (evener/update/apply). Apply is the
// interesting half: the hub answers, then execs the new binary in place, so
// the connection drops and the app has to notice the NEW hub on its own.
// Noticing it is the host's job (a health poll and a page reload on the web, a
// reconnect and a fresh check on the phone), so it is a port, awaitRestart,
// like every host API the package touches. Pure logic: no timers, no fetch.

import type { AppwireClientLike } from "./clientLike";
import { ConnectionClosedError, friendlyErrorMessage, WireError } from "./errors";
import { createFrameworkFreeStore } from "./frameworkFreeStore";
import type { UpdateCheckResponse } from "./types.gen";

export type UpdateChannel = "release" | "snapshot";

// APPLY_TIMEOUT_MS bounds the evener/update/apply RPC itself. The hub
// enforces one overall hubUpgradeTimeout deadline (4 minutes: archive +
// checksums downloads, verify, install) over the per-request 5-minute
// client timeouts that could otherwise stack, so six minutes here clears
// the server bound with headroom for the exec and response frames. A slow
// but valid download therefore resolves (not times out) and the restart
// wait always starts.
export const APPLY_TIMEOUT_MS = 6 * 60_000;

// ALREADY_UP_TO_DATE is the message both the controller's own guard and the
// server's restarting:false response surface when the channel is current.
const ALREADY_UP_TO_DATE = "Already up to date";

export interface HubUpdateState {
  // null until the app seeds it (the web from the overview's buildChannel);
  // the hub then falls back to its own upgrade channel for an empty request.
  channel: UpdateChannel | null;
  check: UpdateCheckResponse | null;
  checking: boolean;
  checkError: string | null;
  applying: boolean;
  applyError: string | null;
  restarting: boolean;
  restartTimedOut: boolean;
}

export interface HubUpdatePorts {
  /** The connected client. Throws when there is none, so a request that was
   * never sent reports as an error. */
  client(): Pick<AppwireClientLike, "request">;
  /** Runs once the hub answered that it is restarting, or its answer was
   * lost on the way. Resolves true once the new hub is up, false when it
   * didn't come back. */
  awaitRestart(previousVersion: string | null): Promise<boolean>;
}

export interface HubUpdateController {
  getState(): HubUpdateState;
  subscribe(listener: () => void): () => void;
  setChannel(channel: UpdateChannel): void;
  runCheck(): Promise<void>;
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
  applying: false,
  applyError: null,
  restarting: false,
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

  async function waitForRestart(previous: string | null): Promise<void> {
    // A wait that fails is a hub that didn't come back as far as the app can
    // tell: never leave the restart showing.
    const back = await ports.awaitRestart(previous).catch(() => false);
    set(back ? { restarting: false } : { restarting: false, restartTimedOut: true });
  }

  return {
    getState: get,
    subscribe: (listener) =>
      store.subscribe(() => {
        if (!disposed) listener();
      }),

    setChannel(channel) {
      checkSequence++;
      set({ channel, check: null, checking: false, checkError: null, applyError: null, restartTimedOut: false });
    },

    async runCheck() {
      // A disposed controller's connection was replaced: it asks nothing.
      if (disposed) return;
      // Invalidate the previous result first: a manual re-check must not
      // leave a stale positive in state while the new check is in flight
      // (apply would otherwise act on it).
      set({ checking: true, check: null, checkError: null, restartTimedOut: false });
      const seq = ++checkSequence;
      try {
        const check = await ports.client().request("evener/update/check", { channel: get().channel ?? "" });
        if (seq === checkSequence) set({ check, checking: false });
      } catch (err) {
        if (seq === checkSequence) set({ check: null, checking: false, checkError: friendlyErrorMessage(err) });
      }
    },

    async apply() {
      if (disposed || get().applying || get().restarting) return;
      const check = get().check;
      if (!check) {
        set({ applyError: "Check for updates first" });
        return;
      }
      // The check must still describe the selected channel: a channel
      // switch after the check resolved leaves a stale positive that must
      // not be applied. (The running build cannot change under the app -- a
      // restart replaces it -- so no build comparison is needed.)
      if (get().channel !== null && check.channel !== get().channel) {
        set({ applyError: "That result is stale; run a fresh check first" });
        return;
      }
      // A caller gates on updateAvailable, but apply is a controller call
      // too: never issue a download + exec restart for an up-to-date check.
      if (!check.updateAvailable) {
        set({ applyError: ALREADY_UP_TO_DATE });
        return;
      }
      set({ applying: true, applyError: null, restartTimedOut: false });
      const previous = check.currentVersion;
      // Resolve the client before entering the transport handler: a missing
      // client means no request was sent, so it must surface as an error,
      // not fall through to the restart wait.
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
        // An answer that doesn't say whether the hub is restarting is a
        // refusal the app can't read, not a lost answer: it waits for nothing.
        if (typeof (resp as { restarting?: unknown } | null)?.restarting !== "boolean") {
          set({ applying: false, applyError: friendlyErrorMessage(new Error("unreadable update answer")) });
          return;
        }
        // The server re-checks before installing and answers restarting:false
        // when the channel was already current: no download, no exec, no new
        // version to wait for. Report it instead of waiting into a timeout.
        if (!(resp as { restarting: boolean }).restarting) {
          // The kept check said an update waited; the hub says otherwise.
          set({ applying: false, applyError: ALREADY_UP_TO_DATE, check: { ...check, updateAvailable: false } });
          return;
        }
      } catch (err) {
        // A transport failure after the server received the apply request
        // may still result in a successful install + restart: the response
        // frame is lost, not the work. Classify by error type: a WireError
        // means the server responded with a failure (not a transport drop),
        // so it stays an error. A RequestTimeoutError or a plain Error
        // (socket closed, network unreachable) means the response was lost,
        // so wait for the new hub before declaring failure. Errors that mean
        // the request never reached the server (ConnectionClosedError, the
        // "cannot call … while state is closed" rejection) stay errors.
        const neverSent =
          err instanceof ConnectionClosedError || (err instanceof Error && /cannot call/i.test(err.message));
        const serverResponded = err instanceof WireError;
        if (neverSent || serverResponded) {
          set({ applying: false, applyError: friendlyErrorMessage(err) });
          return;
        }
      }
      // A controller disposed while its apply was in flight belongs to a
      // replaced connection: it waits for nothing and writes nothing, since
      // an app's shared store may already belong to its replacement.
      if (disposed) return;
      set({ applying: false, restarting: true });
      await waitForRestart(previous);
    },

    dispose() {
      disposed = true;
    },
  };
}
