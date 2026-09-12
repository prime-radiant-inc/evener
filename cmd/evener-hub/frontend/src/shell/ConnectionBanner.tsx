import { useEffect, useState } from "react";
import { checkAuthStatus, SIGN_IN_PROMPT_MESSAGE } from "../auth";
import { AppwireClient, type ConnectionState } from "../protocol/client";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import { rpcURLFromLocation } from "../protocol/transport";
import { connectionStore, useConnectionStore } from "../stores/connection";
import { Banner } from "../widgets/banner";
import { checkWebNotBuilt, NOT_BUILT_MESSAGE } from "./chrome/webNotBuilt";

export interface ConnectionBannerProps {
  state: ConnectionState;
  // Test seam: production (AppShell.tsx's call site, which passes only
  // `state`) gets the default below - a real AppwireClient pointed at
  // rpcURLFromLocation(window.location). Tests inject a factory returning a
  // FakeClient instead, so Retry's wiring can be exercised without a real
  // socket - mirrors AppShellProps.client's own real-vs-injected split
  // (see AppShell.tsx).
  createClient?: () => AppwireClientLike;
  // Called when a manual retry wires a fresh client that connected
  // successfully. The shell adopts it into ClientProvider so useClient()
  // consumers stop calling the closed original; without this the context
  // client is a dead orphan after every retry.
  onClientReplaced?: (client: AppwireClientLike) => void;
  // How long to stay hidden after the state first needs a human's attention
  // (reconnecting/closed) before the banner is revealed. A recovery before
  // this elapses means the banner never appears at all - the connection
  // hiccup was too brief to be worth interrupting the chrome for. The
  // client's own reconnect backoff caps at 5s/attempt (protocol/client.ts),
  // so the 10s default always exceeds a single wait. Tests pass 0 to reveal
  // synchronously (no timer) so existing assertions can drive the banner
  // without fake-clock plumbing.
  delayMs?: number;
}

// 10s: long enough that the usual sub-second reconnect never surfaces, short
// enough that a genuinely stuck connection tells the user promptly.
const DEFAULT_DELAY_MS = 10_000;

const RECONNECTING_MESSAGE = "Reconnecting to the server…";
const CLOSED_MESSAGE = "Connection closed.";
// A protocol close cannot be retried away: Retry builds a fresh client from
// THIS page's bundle, which is the thing the server rejected. Reloading is what
// fetches a client the server will talk to.
const PROTOCOL_MESSAGE = "This page is out of date with the server. Reload to continue.";

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
type ClosedReason = "auth" | "not-built" | "protocol" | null;

// The states that warrant a banner once the reveal delay has elapsed. Every
// other state (idle/connecting/ready) is silent - no banner, no timer.
const ATTENTION_STATES: ReadonlySet<ConnectionState> = new Set(["reconnecting", "closed"]);

/**
 * An overlay status strip reporting the connection state when it needs a
 * human's attention, floating over the top of the shell instead of pushing
 * the workspace down:
 *   - "reconnecting": informative only - the client is already retrying on
 *     its own (exponential backoff, protocol/client.ts), so a manual action
 *     here would be redundant at best and could race a second attempt at
 *     worst. Tone is "attention" (amber, heads-up): the connection is
 *     self-healing and may need you only if it stalls past the reveal delay.
 *   - "closed": terminal from the client's own perspective (it never
 *     retries past this point) - offers Retry, which constructs and
 *     connects a genuinely fresh client (see handleRetry below) rather than
 *     window.location.reload(). Also probes the hub once (checkAuthStatus +
 *     checkWebNotBuilt) to tell an unauthenticated browser or an
 *     unbuilt frontend apart from an ordinary drop, rather than leaving
 *     either behind a dead spinner / uninformative "closed" loop. Tone is
 *     "danger" (red, broken): action is required.
 *
 * The banner stays hidden for `delayMs` after the state first needs
 * attention; a recovery before then means it never appears, so a routine
 * sub-second reconnect doesn't flash a warning over the chrome. Silent the
 * rest of the time (idle/connecting/ready).
 */
export function ConnectionBanner({
  state,
  createClient = defaultCreateClient,
  delayMs = DEFAULT_DELAY_MS,
  onClientReplaced,
}: ConnectionBannerProps) {
  const client = useConnectionStore((s) => s.client);
  const [retrying, setRetrying] = useState(false);
  const [closedReason, setClosedReason] = useState<ClosedReason>(null);
  const [visible, setVisible] = useState(false);

  // Reveal delay: stay hidden until `delayMs` has elapsed with the state
  // still needing attention. A transition out of an attention state (recovery
  // to ready/idle/connecting, or a swap between reconnecting and closed) tears
  // down the pending timer and hides the banner, so a brief hiccup never
  // surfaces and a state change resets the clock. delayMs <= 0 reveals
  // synchronously (the test seam): no timer, setVisible(true) right away.
  useEffect(() => {
    if (!ATTENTION_STATES.has(state)) {
      setVisible(false);
      return;
    }
    if (delayMs <= 0) {
      setVisible(true);
      return;
    }
    setVisible(false);
    const timer = setTimeout(() => setVisible(true), delayMs);
    return () => clearTimeout(timer);
  }, [state, delayMs]);

  // Re-probes whenever `state` transitions into "closed" (from any other
  // state, same client) OR whenever the wired client's own IDENTITY changes
  // while still closed (a retry whose fresh client also ends up closed -
  // e.g. still unauthenticated - must re-check, not keep showing whatever
  // the PREVIOUS client's probe found; `state` alone can't detect that,
  // since the string can stay "closed" across the swap). `client` is
  // deliberately a trigger-only dependency here - never read inside the
  // effect body, purely so a client swap re-runs the probe.
  // biome-ignore lint/correctness/useExhaustiveDependencies: client is a deliberate trigger-only dep, see above
  useEffect(() => {
    if (state !== "closed") {
      setClosedReason(null);
      return;
    }
    // The client already knows when the close was a protocol rejection, and it
    // knows it exactly -- no probe can tell that apart from an ordinary drop.
    if (connectionStore.getState().client?.terminalReason === "protocol") {
      setClosedReason("protocol");
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
      // Wire first so replacement clears the old handshake metadata before
      // this client's response is published.
      connectionStore.getState().connect(fresh);
      if (!(fresh instanceof AppwireClient && import.meta.env.MODE === "test")) {
        try {
          const info = await fresh.connect();
          if (connectionStore.getState().client !== fresh || fresh.state === "closed") return;
          connectionStore.setState({ serverInfo: info.serverInfo, features: info.features });
          onClientReplaced?.(fresh);
        } catch {
          // Reflected via the client's own state, mirrored into
          // connectionStore by connect() above either way - nothing further
          // to do with the rejection itself (same treatment as AppShell's
          // own initial-boot connect() failure).
        }
      }
      // The store was wired before connect() so it reflects THIS attempt's
      // state even when the handshake rejects.
    } finally {
      setRetrying(false);
    }
  }

  if (!visible) return null;

  // Reads the CURRENTLY-wired client from connectionStore, not useClient()'s
  // React context: the context value is fixed to whatever AppShell
  // constructed at mount (Global Constraints: "one AppwireClient per window,
  // owned by the shell, injected via context"), but this component's OWN
  // handleRetry below can swap connectionStore's client to a fresh instance -
  // after which the context client is a dead, permanently-closed orphan
  // that would never reach the client actually reconnecting. "Retry now"
  // must reach whichever client the "reconnecting" state this banner is
  // currently showing actually BELONGS to, which is always
  // connectionStore's, never necessarily the context's - the same reason the
  // closedReason re-probe effect above already keys off this exact `client`
  // value instead of context. retryNow() itself is a no-op unless the client
  // is actually "reconnecting" (see protocol/client.ts), so a stray click
  // while the state prop and the store have briefly diverged is harmless
  // either way.
  if (state === "reconnecting") {
    return (
      <Banner
        tone="attention"
        message={RECONNECTING_MESSAGE}
        action={{ label: "Retry now", onClick: () => client?.retryNow() }}
      />
    );
  }

  if (state !== "closed") return null;

  const message =
    closedReason === "auth"
      ? SIGN_IN_PROMPT_MESSAGE
      : closedReason === "not-built"
        ? NOT_BUILT_MESSAGE
        : closedReason === "protocol"
          ? PROTOCOL_MESSAGE
          : CLOSED_MESSAGE;

  // A protocol close can only be fixed by reloading (this bundle is what the
  // server rejected); every other close can be retried with a fresh client.
  const action =
    closedReason === "protocol"
      ? { label: "Reload", onClick: () => window.location.reload() }
      : { label: "Retry", onClick: () => void handleRetry(), inFlight: retrying };

  return <Banner tone="danger" message={message} action={action} />;
}
