import { useEffect, useState } from "react";
import { Button } from "../widgets";
import { requireClass } from "../widgets/internal/requireClass";
import { AppwireClient, type ConnectionState } from "../protocol/client";
import { rpcURLFromLocation } from "../protocol/transport";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import { connectionStore, useConnectionStore } from "../stores/connection";
import { checkAuthStatus, SIGN_IN_PROMPT_MESSAGE } from "../auth";
import { checkWebNotBuilt, NOT_BUILT_MESSAGE } from "./chrome/webNotBuilt";
import styles from "./ConnectionBanner.module.css";

export interface ConnectionBannerProps {
  state: ConnectionState;
  // Test seam: production (AppShell.tsx's call site, which passes only
  // `state`) gets the default below - a real AppwireClient pointed at
  // rpcURLFromLocation(window.location). Tests inject a factory returning a
  // FakeClient instead, so Retry's wiring can be exercised without a real
  // socket - mirrors AppShellProps.client's own real-vs-injected split
  // (see AppShell.tsx).
  createClient?: () => AppwireClientLike;
}

const CLASS = {
  banner: requireClass(styles.banner, "ConnectionBanner.module.css", "banner"),
};

const RECONNECTING_MESSAGE = "Reconnecting to the server…";
const CLOSED_MESSAGE = "Connection closed.";

function defaultCreateClient(): AppwireClientLike {
  return new AppwireClient({ url: rpcURLFromLocation(window.location) });
}

// Why a retry can't just call connect() again on the SAME client:
// AppwireClient.connect() (protocol/client.ts) caches a single
// connectPromise for the object's whole lifetime and never resets it, so
// calling connect() again on an already-"closed" client just replays its
// original rejection rather than dialing a new socket. A fresh client is
// the only thing guaranteed to actually try again - see stores/threads.ts's
// rewireClient for the other half of this (re-attaching this store's own
// notification/ready handlers to whatever client connectionStore ends up
// wired to, reactively, so a swap here doesn't strand any open pane).
//
// ClosedReason distinguishes WHY the connection needs attention, decided by
// probing the hub (see the effect below) - "auth"/"not-built" swap in a
// more specific, actionable message than the generic closed one; Retry
// stays offered in every case, since reconnecting the /rpc socket can help
// regardless (an auth cookie set in another tab, or a hub that's back up
// with nothing actually wrong).
type ClosedReason = "auth" | "not-built" | null;

/**
 * A quiet inline strip reporting the connection state when it needs a
 * human's attention:
 *   - "reconnecting": informative only - the client is already retrying on
 *     its own (exponential backoff, protocol/client.ts), so a manual action
 *     here would be redundant at best and could race a second attempt at
 *     worst.
 *   - "closed": terminal from the client's own perspective (it never
 *     retries past this point) - offers Retry, which constructs and
 *     connects a genuinely fresh client (see handleRetry below) rather than
 *     window.location.reload(). Also probes the hub once (checkAuthStatus +
 *     checkWebNotBuilt) to tell an unauthenticated browser or an
 *     unbuilt frontend apart from an ordinary drop, rather than leaving
 *     either behind a dead spinner / uninformative "closed" loop.
 * Silent the rest of the time (idle/connecting/ready).
 */
export function ConnectionBanner({ state, createClient = defaultCreateClient }: ConnectionBannerProps) {
  const client = useConnectionStore((s) => s.client);
  const [retrying, setRetrying] = useState(false);
  const [closedReason, setClosedReason] = useState<ClosedReason>(null);

  // Re-probes whenever `state` transitions into "closed" (from any other
  // state, same client) OR whenever the wired client's own IDENTITY changes
  // while still closed (a retry whose fresh client also ends up closed -
  // e.g. still unauthenticated - must re-check, not keep showing whatever
  // the PREVIOUS client's probe found; `state` alone can't detect that,
  // since the string can stay "closed" across the swap).
  useEffect(() => {
    if (state !== "closed") {
      setClosedReason(null);
      return;
    }
    let cancelled = false;
    void (async () => {
      const [auth, notBuilt] = await Promise.all([checkAuthStatus(), checkWebNotBuilt()]);
      if (cancelled) return;
      if (auth === "unauthenticated") setClosedReason("auth");
      else if (notBuilt === "not-built") setClosedReason("not-built");
      else setClosedReason(null);
    })();
    return () => {
      cancelled = true;
    };
  }, [state, client]);

  async function handleRetry(): Promise<void> {
    setRetrying(true);
    try {
      const fresh = createClient();
      // jsdom implements a global WebSocket that would otherwise dial the
      // page's own origin for real (same hazard AppShell.tsx's own bootstrap
      // guards against). Only a REAL AppwireClient needs this - an injected
      // FakeClient (tests) has no socket to open, so running its connect()
      // even under MODE==="test" is safe, and necessary to exercise the
      // serverInfo-population duty below.
      if (!(fresh instanceof AppwireClient && import.meta.env.MODE === "test")) {
        try {
          const info = await fresh.connect();
          connectionStore.setState({ serverInfo: info.serverInfo });
        } catch {
          // Reflected via the client's own state, mirrored into
          // connectionStore by connect() below either way - nothing further
          // to do with the rejection itself (same treatment as AppShell's
          // own initial-boot connect() failure).
        }
      }
      // Wires the store to this client regardless of whether connect()
      // above resolved or rejected, so connectionStore.state (and, via
      // stores/threads.ts's reactive rewireClient, every open pane's live
      // wiring) always reflects THIS attempt's real outcome.
      connectionStore.getState().connect(fresh);
    } finally {
      setRetrying(false);
    }
  }

  if (state === "reconnecting") {
    return (
      <div className={CLASS.banner}>
        <span>{RECONNECTING_MESSAGE}</span>
        {/* Reads the CURRENTLY-wired client from connectionStore, not
            useClient()'s React context: the context value is fixed to
            whatever AppShell constructed at mount (Global Constraints:
            "one AppwireClient per window, owned by the shell, injected via
            context"), but this component's OWN handleRetry below can swap
            connectionStore's client to a fresh instance - after which the
            context client is a dead, permanently-closed orphan that would
            never reach the client actually reconnecting. "Retry now" must
            reach whichever client the "reconnecting" state this banner is
            currently showing actually BELONGS to, which is always
            connectionStore's, never necessarily the context's - the same
            reason the closedReason re-probe effect above already keys off
            this exact `client` value instead of context. retryNow() itself
            is a no-op unless the client is actually "reconnecting" (see
            protocol/client.ts), so a stray click while the state prop and
            the store have briefly diverged is harmless either way. */}
        <Button variant="quiet" size="sm" onClick={() => client?.retryNow()}>
          Retry now
        </Button>
      </div>
    );
  }

  if (state !== "closed") return null;

  const message = closedReason === "auth" ? SIGN_IN_PROMPT_MESSAGE : closedReason === "not-built" ? NOT_BUILT_MESSAGE : CLOSED_MESSAGE;

  return (
    <div className={CLASS.banner}>
      <span>{message}</span>
      <Button variant="quiet" size="sm" onClick={() => void handleRetry()} disabled={retrying}>
        {retrying ? "Retrying…" : "Retry"}
      </Button>
    </div>
  );
}
