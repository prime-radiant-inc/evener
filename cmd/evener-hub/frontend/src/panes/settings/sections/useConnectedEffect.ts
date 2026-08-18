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
// (/settings/credentials, /settings/launch-serf, ...) that mounts before
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
import { useEffect } from "react";
import { connectionStore } from "../../../stores/connection";

export function useConnectedEffect(
  attempt: (isCancelled: () => boolean) => Promise<void>,
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
