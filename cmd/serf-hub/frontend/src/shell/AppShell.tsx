// The application shell: constructs the one AppwireClient per window,
// drives its connect() handshake, provides it via context, and (Task 1
// only - Task 2 adds the real dockview/mobile hosts) renders the welcome
// pane standalone.
import { Suspense, useEffect, useState } from "react";
import { AppwireClient } from "../protocol/client";
import { rpcURLFromLocation } from "../protocol/transport";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import { connectionStore, useConnectionStore } from "../stores/connection";
import { ClientProvider } from "./clientContext";
import { ConnectionBanner } from "./ConnectionBanner";
import { paneFor } from "./paneRegistry";
import { urlToPane } from "./routing";
import type { WelcomePaneParams } from "../panes/welcome/Welcome";
import "../panes/welcome"; // registers the "welcome" pane type
import styles from "./AppShell.module.css";

export interface AppShellProps {
  // Test seam: production (main.tsx) omits this, and AppShell constructs +
  // connects the one real AppwireClient itself (below). Tests inject a
  // FakeClient (whose connect() is a separately-scripted promise, see
  // protocol/testing/fakeClient.ts) so the rest of AppShell's wiring -
  // including the serverInfo-population duty - runs with no real sockets.
  // AppShell calls .connect() on whatever `client` is, injected or not; the
  // `owned` half of ClientSlot below exists to gate the ONE thing that
  // still differs between them - never opening a real socket under a test
  // runner - and to know which client this component itself is responsible
  // for closing again on unmount.
  client?: AppwireClientLike;
}

interface ClientSlot {
  client: AppwireClientLike;
  owned: AppwireClient | null;
}

// Constructing an AppwireClient has no side effects on its own (the
// constructor only stores config; dialing a socket happens inside
// connect()), so computing this in a useState lazy initializer is safe even
// under StrictMode's double-invoke - the discarded extra instance (if any)
// never opened a socket. The actual connect() call lives in the effect
// below, which IS where a side effect belongs.
function createClientSlot(injected: AppwireClientLike | undefined): ClientSlot {
  if (injected) return { client: injected, owned: null };
  const real = new AppwireClient({ url: rpcURLFromLocation(window.location) });
  return { client: real, owned: real };
}

// /new resolves to the "spawn" pane type, which has no registered component
// yet (spawn panes are Wave 6) - render the welcome pane with a note
// instead of a broken lookup until then.
const SPAWN_NOT_READY_NOTE = "Starting a new session isn't available yet.";

// Hand-rolled rather than react-router (see this task's report for the
// justification): Task 1 needs exactly one thing - re-render when the path
// changes - and pushState doesn't fire popstate on its own, so this listens
// for the same synthetic popstate routing.ts's navigate() dispatches.
function usePathname(): string {
  const [pathname, setPathname] = useState(() => window.location.pathname);
  useEffect(() => {
    const onPopState = () => setPathname(window.location.pathname);
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);
  return pathname;
}

export function AppShell({ client: injectedClient }: AppShellProps) {
  const [{ client, owned }] = useState(() => createClientSlot(injectedClient));

  useEffect(() => {
    connectionStore.getState().connect(client);
    // jsdom (unlike a real browser-less environment) implements a global
    // WebSocket that would otherwise dial the page's own origin for real.
    // App.test.tsx renders <App/> - and therefore <AppShell/> with its
    // default, no-prop production wiring - with no injected client, so a
    // REAL client's connect() must never run under vitest. An injected
    // client (AppShellProps.client - always a FakeClient in tests) has no
    // socket to open, so it's fine - and necessary, to exercise the
    // serverInfo-population duty below - for its connect() to run even
    // under MODE==="test" (mirrors dev/DevHarness.tsx's identical
    // guard/rationale, narrowed to only the client AppShell itself dialed).
    if (!(owned && import.meta.env.MODE === "test")) {
      void client.connect().then(
        (info) => connectionStore.setState({ serverInfo: info.serverInfo }),
        () => {
          // Failure is already reflected via the client's own onStateChange
          // -> connectionStore.state transition (to "closed"); nothing
          // further to do with the rejection itself.
        },
      );
    }
    // Tear down the client we constructed ourselves on unmount (HMR
    // remount, navigating away, etc.) - the one-client-per-window invariant
    // means this component must retire what it dialed. An injected client's
    // lifecycle stays its owner's (the test's) responsibility, so this only
    // ever acts on `owned`, never the interface-typed `client`.
    return () => owned?.close();
  }, [client, owned]);

  const connectionState = useConnectionStore((s) => s.state);
  const pathname = usePathname();

  const route = urlToPane(pathname);
  const welcomeParams: WelcomePaneParams = route?.type === "spawn" ? { note: SPAWN_NOT_READY_NOTE } : {};
  const WelcomePane = paneFor("welcome").component;

  return (
    <ClientProvider client={client}>
      <div className={styles.shell}>
        <ConnectionBanner state={connectionState} />
        <div className={styles.content}>
          <Suspense fallback={null}>
            <WelcomePane params={welcomeParams} paneId="welcome" focused={true} />
          </Suspense>
        </div>
      </div>
    </ClientProvider>
  );
}
