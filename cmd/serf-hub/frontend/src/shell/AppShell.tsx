// The application shell: constructs the one AppwireClient per window,
// drives its connect() handshake, provides it via context, and hosts the
// workspace - DockHost (dockview) on desktop; renders NotFound in its
// place for a path urlToPane() can't resolve at all.
import { useEffect, useRef, useState } from "react";
import { AppwireClient } from "../protocol/client";
import { rpcURLFromLocation } from "../protocol/transport";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import { connectionStore, useConnectionStore } from "../stores/connection";
import { ClientProvider } from "./clientContext";
import { ConnectionBanner } from "./ConnectionBanner";
import { ToastRegion } from "./chrome/ToastRegion";
import { DockHost } from "./DockHost";
import { StackHost } from "./mobile/StackHost";
import { NotFound } from "./NotFound";
import { Rail } from "./rail";
import { urlToPane } from "./routing";
import { useIsMobile } from "./useIsMobile";
import { workspaceStore } from "./workspace";
import "../panes/welcome"; // registers the "welcome" pane type
import "../panes/session"; // registers the "session" pane type
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
// yet (spawn panes are Wave 6) - open the welcome pane with a note instead
// of a broken lookup until then.
const SPAWN_NOT_READY_NOTE = "Starting a new session isn't available yet.";

// Hand-rolled rather than react-router (see Task 1's report for the
// justification): the routing surface here is still exactly "re-render
// (and, this task, open a pane) when the path changes" - and pushState
// doesn't fire popstate on its own, so this listens for the same synthetic
// popstate routing.ts's navigate() dispatches.
function usePathname(): string {
  const [pathname, setPathname] = useState(() => window.location.pathname);
  useEffect(() => {
    const onPopState = () => setPathname(window.location.pathname);
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);
  return pathname;
}

// Opens (or focuses) the pane a pathname resolves to. "session" maps
// directly (params carry the ref straight through); every other resolved
// type - including "welcome" itself - falls back to opening the plain
// welcome singleton, exactly as it did before this task ("spawn" keeps its
// own not-ready note; "transcript"/"settings"/"doc" aren't registered yet
// this wave, same as "spawn", and get the same untouched fallback rather
// than a new bespoke treatment). A null route (genuinely unknown path)
// opens nothing - NotFound renders in DockHost's place instead, see the
// component's return below.
function openRouteAsPane(pathname: string): void {
  const route = urlToPane(pathname);
  if (route === null) return;
  if (route.type === "session") {
    workspaceStore.getState().openPane("session", route.params);
    return;
  }
  workspaceStore.getState().openPane("welcome", route.type === "spawn" ? { note: SPAWN_NOT_READY_NOTE } : {});
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
  const isMobile = useIsMobile();

  // Opens a pathname's pane during render, not a useEffect, for as long as
  // DockHost hasn't mounted for the very first time yet - a regular effect
  // would run AFTER DockHost/dockview's own onReady (child effects fire
  // before parent effects within a commit), racing its
  // restore-or-fallback-to-welcome boot sequence and landing a deep-linked
  // pane ALONGSIDE a spurious extra welcome tab instead of in its place
  // (see DockHost.tsx's own comment on this for the full reasoning). This
  // is safe as a render-phase call ONLY because nothing is yet
  // mounted/subscribed to workspaceStore for openPane()'s update to
  // conflict with - true not just on AppShell's very first render, but on
  // EVERY render up through whichever one first has route !== null (e.g.
  // loading directly on an unresolved path, where DockHost never mounts at
  // all until the user navigates to a real one - dockHostHasMountedRef
  // tracks this rather than assuming "first AppShell render" and
  // "DockHost's first mount" always coincide, which they don't). Once
  // DockHost has mounted at all, every later pathname change goes through
  // the plain effect below instead - calling openPane() from render with
  // DockHost already mounted and subscribed is exactly what trips React's
  // "Cannot update a component while rendering a different component"
  // warning (caught by this task's own test suite, not by inspection, in
  // both the "already mounted at the start" and "just mounted for the
  // first time on a later render" shapes of this race).
  const dockHostHasMountedRef = useRef(false);
  const openedForPathnameRef = useRef<string | null>(null);
  if (!dockHostHasMountedRef.current && openedForPathnameRef.current !== pathname) {
    openedForPathnameRef.current = pathname;
    openRouteAsPane(pathname);
  }
  if (route !== null) dockHostHasMountedRef.current = true;

  useEffect(() => {
    if (openedForPathnameRef.current === pathname) return; // already opened above, this render
    openedForPathnameRef.current = pathname;
    openRouteAsPane(pathname);
  }, [pathname]);

  return (
    <ClientProvider client={client}>
      <div className={styles.shell}>
        <ConnectionBanner state={connectionState} />
        <ToastRegion />
        <div className={styles.content}>
          {/* Desktop: the rail sits as a flex sibling of DockHost and
              collapses itself. Mobile (<900px): StackHost owns the whole
              region and hosts the rail inside its tree drawer instead
              (TreeDrawer's children slot, threaded via railSlot), so the
              flex sibling renders only on desktop. Both hosts read the
              same pane registry and workspace store. */}
          {!isMobile && route !== null && <Rail />}
          {route === null ? <NotFound /> : isMobile ? <StackHost railSlot={<Rail />} /> : <DockHost />}
        </div>
      </div>
    </ClientProvider>
  );
}
