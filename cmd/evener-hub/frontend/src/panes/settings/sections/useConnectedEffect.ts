// useConnectedEffect: run an async, client-requiring store call once the
// connection is actually ready, immediately if it already is (the common
// case), or on the first connectionStore transition into "ready" otherwise.
//
// Every settings section below fetches through a store whose actions call
// requireClient() OUTSIDE their own try/catch (stores/credentials.ts,
// stores/launchConfig.ts, matching stores/threads.ts's own established
// convention) - deliberately: "no client connected" is a programmer-error
// condition each store rejects loudly on, not one it silently degrades
// from. But a settings section can be reached by a direct deep link
// (/settings/credentials, /settings/launch-evener, ...) that mounts before
// AppShell's own connect() handshake has finished - panes/session/
// Session.tsx hit this exact race first and documents it in full; this
// hook is that same fix, generalized so every section here doesn't
// reimplement the tryStart/subscribe/unsubscribe dance by hand. `attempt`'s
// own rejection is swallowed - it must be observed so it never surfaces as
// an unhandled rejection - the caller's store method is responsible for its
// own error-state handling before this hook ever sees the rejection.
//
// `attempt` receives an `isCancelled()` check for callers that populate a
// local useState from inside their own async closure (launchServer.tsx,
// project.tsx, inrepo.tsx - all 3 have their own multi-step load sequences a
// single store call can't express) - without it, a slow request whose
// component unmounted before it resolved would call setState on an
// unmounted component. Callers that just forward a single store method
// (agents.tsx, CredentialsSection.tsx - state comes from the store's own
// subscription, not a local useState) can ignore the extra parameter
// entirely; TypeScript permits passing a 0-arg function where a 1-arg one is
// expected.
import { useEffect, useRef } from "react";
import { connectionStore } from "../../../stores/connection";
import { useHostAttachEpoch } from "../../../stores/hosts";

export function useConnectedEffect(
  // The awaited value is the caller's own (fetch()'s applied verdict, a
  // section's assembled state); this hook starts the call and discards it.
  attempt: (isCancelled: () => boolean) => Promise<unknown>,
  deps: readonly unknown[],
): void {
  useEffect(() => {
    let cancelled = false;
    let started = false;
    function tryStart(): void {
      if (started || connectionStore.getState().state !== "ready") return;
      started = true;
      void attempt(() => cancelled).catch(() => {});
    }
    tryStart();
    const unsubscribe = connectionStore.subscribe(tryStart);
    return () => {
      cancelled = true;
      unsubscribe();
    };
    // deps is this hook's own generic forwarding parameter - the caller
    // lists exactly what should restart the wait (see this file's own
    // callers), which biome's static analysis can't see through a
    // wrapper hook one level removed from the real useEffect call.
    // biome-ignore lint/correctness/useExhaustiveDependencies: generic passthrough hook - see comment above
  }, deps);
}

/**
 * useHostScopedLoad is useConnectedEffect for a pane whose read belongs to the
 * host the settings route selected, and it adds the two things every one of
 * those panes needs on top of it.
 *
 * 1. The host's ATTACH EPOCH is part of what restarts the read
 *    (stores/hosts.ts). A host that is merely away refuses
 *    `evener/host/request`, so a pane that loaded in that window sits on its
 *    failure - and a host's attachment is live session state, deliberately not
 *    part of the registration identity, so nothing remounts and nothing
 *    re-reads when it comes back. The registry's own answer is what reports the
 *    transition (there is no host lifecycle notification on the wire).
 *
 * 2. `load` is told whether this run is looking at DIFFERENT content (`blank`):
 *    a host switch or a re-registration, which every pane must drop what it is
 *    showing for, because the form on screen is the host the user left. The
 *    same content re-read - a host coming back, `blank === false` - is a
 *    refresh, and a refresh must NOT blank: the data on screen belongs to this
 *    host, and whatever the user has typed into it is theirs to keep.
 *
 * "Different content" is exactly "the caller's own deps changed": the per-host
 * store instance (a new one for a switch and for a re-registration) and each
 * pane's own content key (the project pane's cwd). The attach epoch is compared
 * separately - appended to the restarted effect's deps but NOT to the content
 * comparison - which is what keeps a reconnect a refresh rather than a switch.
 *
 * A pane that renders from its store's own state (agentsDoc, mcp, dirListSetting
 * and the marketplaces pane) ignores both parameters: the store keeps its data
 * and its draft-holding components stay mounted, so re-issuing the read is all
 * that is needed.
 *
 * `reload` re-issues this pane's read for the SAME content - never a blank, so
 * nothing the user has typed is taken away - which is the retry a pane offers
 * when a read failed while its host stays attached. A pane with nothing to
 * retry (one that renders from its store's state) ignores it.
 */
export interface HostScopedLoad {
  /** Re-issues the read for the SAME content: the pane keeps what it is showing
   * (and any draft in it), which is what a failed REFRESH needs. */
  reload: () => void;
  /** Re-issues the read as a FIRST LOOK at this content: the pane blanks and
   * shows its own loading or error state, which is what a failed LOAD needs.
   * Without this, a load error's Retry took the refresh path - whose notice
   * renders only over a ready load - so it reported nothing and the error stood. */
  retryLoad: () => void;
}

export function useHostScopedLoad(
  host: string,
  load: (blank: boolean, isCancelled: () => boolean) => Promise<unknown>,
  deps: readonly unknown[],
): HostScopedLoad {
  const attachEpoch = useHostAttachEpoch(host);
  // The deps of the run that SUCCEEDED for what is on screen, so the next run can
  // tell a re-read of the same content from a look at different content.
  //
  // Claimed only once a run reports that it worked, never before the read has
  // produced anything. Claiming it up front is a trap: a second invocation with
  // the same deps (React's development double-invoke, a Fast Refresh) would then
  // look like a re-read of content the pane is already showing, take the refresh
  // branch, and leave the pane on "Loading..." for good, because the refresh
  // notice renders only over a ready load. A run that FAILED must not claim it
  // either, or the next run of the same content would take that same branch and
  // the pane would keep the error it is already showing with nothing to clear it.
  const loadedDeps = useRef<readonly unknown[] | null>(null);
  // The run currently in flight (or the last one): its own callback and its own
  // cancellation check, which is what makes the retries re-issue exactly the read
  // the pane is showing. Written from the effect body, never during render.
  const currentRun = useRef<{ load: typeof load; isCancelled: () => boolean } | null>(null);
  useConnectedEffect(
    (isCancelled) => {
      currentRun.current = { load, isCancelled };
      const previous = loadedDeps.current;
      const blank =
        previous === null ||
        previous.length !== deps.length ||
        previous.some((value, index) => !Object.is(value, deps[index]));
      return load(blank, isCancelled).then((result) => {
        // A loader returns `false` to report that its run FAILED; anything else,
        // including nothing at all, means the content is on screen.
        if (result !== false) loadedDeps.current = deps;
        return result;
      });
    },
    // The epoch rides the restarted effect's deps only: it is not content.
    [...deps, attachEpoch],
  );
  const reload = (): void => {
    const run = currentRun.current;
    if (run === null) return;
    // Same content, so the pane keeps what it is showing; the caller reports the
    // outcome itself, as it does for every other run.
    void run.load(false, run.isCancelled).catch(() => {});
  };
  const retryLoad = (): void => {
    const run = currentRun.current;
    if (run === null) return;
    // A first look at this content again: forget what was claimed, so this run is
    // a LOAD and the pane shows its own loading/error state rather than a refresh
    // notice it cannot render while the load is not ready.
    loadedDeps.current = null;
    void run.load(true, run.isCancelled).catch(() => {});
  };
  return { reload, retryLoad };
}
