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
  // FakeClient so the rest of AppShell's wiring runs with no real sockets.
  // AppwireClientLike (protocol/testing/fakeClient.ts) has no connect()
  // method - fakes simulate readiness directly via their own
  // constructor/emitStateChange instead of a real handshake - so AppShell
  // only ever calls .connect() on a client it constructed itself (the
  // `owned` half of ClientSlot below), never on an injected one.
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
    if (!owned) return;
    // jsdom (unlike a real browser-less environment) implements a global
    // WebSocket that would otherwise dial the page's own origin for real.
    // App.test.tsx renders <App/> - and therefore <AppShell/> with its
    // default, no-prop production wiring - with no injected client, so this
    // must never open a real socket under vitest. AppShell's own tests
    // (AppShell.test.tsx) always inject a client and never reach this
    // branch (mirrors dev/DevHarness.tsx's identical guard/rationale).
    if (import.meta.env.MODE === "test") return;
    void owned.connect().then(
      (info) => connectionStore.setState({ serverInfo: info.serverInfo }),
      () => {
        // Failure is already reflected via the client's own onStateChange
        // -> connectionStore.state transition (to "closed"); nothing
        // further to do with the rejection itself.
      },
    );
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
