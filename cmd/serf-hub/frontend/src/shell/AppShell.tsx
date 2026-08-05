// The application shell: constructs the one AppwireClient per window,
// drives its connect() handshake, provides it via context, and hosts the
// workspace - DockHost (dockview) on desktop; renders NotFound in its
// place for a path urlToPane() can't resolve at all.
import { useEffect, useRef, useState } from "react";
import { initNotifications } from "../notifications";
import { AppwireClient } from "../protocol/client";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import { rpcURLFromLocation } from "../protocol/transport";
import { connectionStore, useConnectionStore } from "../stores/connection";
import { type TreeResponse, useTreeStore } from "../stores/tree";
import { ConnectionBanner } from "./ConnectionBanner";
import { ToastRegion } from "./chrome/ToastRegion";
import { ClientProvider } from "./clientContext";
import { DockRegion } from "./DockRegion";
import { StackHost } from "./mobile/StackHost";
import { NotFound } from "./NotFound";
import { CommandPalette } from "./palette/CommandPalette";
import { openPalette } from "./palette/paletteController";
import { RailHost } from "./rail";
import { topLevelAncestorRef } from "./rail/railNodes";
import { urlToPane } from "./routing";
import { openNestedSessionWithOwner, openTopLevelSession } from "./sessionPlacement";
import { isSinglePaneRoute } from "./singlePane";
import { useIsMobile } from "./useIsMobile";
import { useWorkspaceStore, workspaceStore } from "./workspace";
import "../panes/welcome"; // registers the "welcome" pane type
import "../panes/session"; // registers the "session" pane type
import "../panes/settings"; // registers the "settings" pane type
import "../panes/spawn"; // registers the "spawn" pane type
// doc and transcript panes open lazily (via openDoc.ts / paneActions.ts), never
// on the initial route - but a persisted dockview layout CAN contain one, and
// DockHost restores that layout at boot before any lazy opener runs. Register
// both eagerly here (their heavy components stay lazy()) so restoreLayout finds
// the pane type registered instead of discarding the whole saved workspace.
import "../panes/doc"; // registers the "doc" pane type
import "../panes/transcript"; // registers the "transcript" pane type
import "../panes/sessionPanels"; // registers the session panel pane types
import { initPrefs } from "../stores/prefs";

// Apply persisted display preferences (theme/density/font-size) during
// module evaluation - before first paint - so a saved theme never flashes
// the default. Idempotent; sections re-apply on change. (Wave-7 T4's
// documented wiring line, pre-proven against the full suite by its review.)
initPrefs();

// Start the notifications engine once, at module evaluation, beside initPrefs.
// Idempotent no-op in T1; T4 fills it (title count / favicon badge / OS
// notification / sound / single-tab election off the pinned all-OFF prefs).
initNotifications();

import styles from "./AppShell.module.css";

// dockview (pulled in by DockHost.tsx) is ~636kB of the main bundle on its
// own (see Task 2's own report) - dead weight for the mobile path, which
// renders StackHost instead and never touches dockview at all. DockRegion
// owns that lazy load (and the boundary around it) rather than importing
// DockHost eagerly like every other shell module, so dockview's weight lands
// in its own chunk that only a desktop session ever fetches.

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

function sessionRefFromRouteParams(params: unknown): string | null {
  if (typeof params !== "object" || params === null) return null;
  const ref = (params as { ref?: unknown }).ref;
  return typeof ref === "string" && ref.length > 0 ? ref : null;
}

function sameRouteParams(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

function routePlacementIsApplied(pathname: string, tree: TreeResponse | null, allowFocusedPanel = false): boolean {
  const route = urlToPane(pathname);
  if (route === null || route.type === "welcome") return true;

  const workspace = workspaceStore.getState();
  const main = workspace.mainPane();
  if (main === null) return false;

  if (route.type === "settings" || route.type === "spawn") {
    if (workspace.focusedPaneId !== main.id) return false;
    const matchingType = route.type;
    const matchingPanes = workspace.panes.filter((pane) => pane.type === matchingType);
    return main.type === matchingType && sameRouteParams(main.params, route.params) && matchingPanes.length === 1;
  }

  const ref = sessionRefFromRouteParams(route.params);
  if (ref === null || tree === null) return false;
  const ancestorRef = topLevelAncestorRef([...tree.projects, ...tree.test_runs], ref);
  const sessionRefOf = (pane: { params: unknown }): string | null => {
    const paneRef = (pane.params as { ref?: unknown }).ref;
    return typeof paneRef === "string" ? paneRef : null;
  };
  const focusedPane = workspace.panes.find((pane) => pane.id === workspace.focusedPaneId);
  const focusedPanel =
    focusedPane?.type === "sessionTasks" ||
    focusedPane?.type === "sessionActivity" ||
    focusedPane?.type === "sessionDetails";
  const focusIsApplied = (paneId: string): boolean =>
    workspace.focusedPaneId === paneId || (allowFocusedPanel && focusedPanel);

  if (ancestorRef === null || ancestorRef === ref) {
    return (
      focusIsApplied(main.id) &&
      main.type === "session" &&
      sessionRefOf(main) === ref &&
      workspace.panes.filter((pane) => pane.type === "session" && sessionRefOf(pane) === ref).length === 1
    );
  }

  const ownerPanes = workspace.panes.filter((pane) => pane.type === "session" && sessionRefOf(pane) === ancestorRef);
  const childPanes = workspace.panes.filter(
    (pane) => pane.type === "session" && pane.slot === "secondary" && sessionRefOf(pane) === ref,
  );
  return (
    ownerPanes.length === 1 &&
    ownerPanes[0]?.id === main.id &&
    childPanes.length === 1 &&
    focusIsApplied(childPanes[0]?.id ?? "")
  );
}

// Opens (or focuses) the pane a pathname resolves to, while enforcing two
// invariants:
// - nested session routes always open their top-level owner in main and the nested
//   session beside it (never a subagent in main beside an unrelated root).
// - global settings always uses the main slot.
// session deep-links defer opening until /api/tree arrives; otherwise we cannot tell
// whether a ref is nested and would open it in main before fixing, causing a full
// close/reopen cycle later. Deferred open preserves the same mount semantics.
function openRouteAsPane(
  pathname: string,
  tree: TreeResponse | null,
  _treeError: string | null,
  pendingSessionRef: { current: string | null },
): void {
  const route = urlToPane(pathname);
  if (route === null) {
    pendingSessionRef.current = null;
    return;
  }

  if (route.type === "session") {
    const ref = sessionRefFromRouteParams(route.params);
    if (ref === null) return;

    if (tree === null) {
      pendingSessionRef.current = ref;
      return;
    }

    pendingSessionRef.current = null;
    const ancestorRef = topLevelAncestorRef([...tree.projects, ...tree.test_runs], ref);
    if (ancestorRef === null || ancestorRef === ref) {
      openTopLevelSession(ref);
      return;
    }
    openNestedSessionWithOwner(ref, ancestorRef);
    return;
  }

  pendingSessionRef.current = null;
  if (route.type === "settings") {
    workspaceStore.getState().replacePrimary("settings", route.params);
    return;
  }
  if (route.type === "spawn") {
    workspaceStore.getState().replacePrimary("spawn", route.params);
    return;
  }
  workspaceStore.getState().openPane("welcome", {});
}

function reconcileWelcomeRouteWithTree(tree: TreeResponse | null): void {
  if (tree === null) return;

  const main = workspaceStore.getState().mainPane();
  if (main?.type !== "session") return;

  const childRef = sessionRefFromRouteParams(main.params);
  if (childRef === null) return;

  const ownerRef = topLevelAncestorRef([...tree.projects, ...tree.test_runs], childRef);
  if (ownerRef === null || ownerRef === childRef) return;

  openNestedSessionWithOwner(childRef, ownerRef);
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

  // Global command-palette entry points (floor §2.1): ⌘K / Ctrl-K from
  // anywhere in the app, and a click on any [data-search-trigger] element.
  // Both open the palette through the one openPalette() the whole app shares
  // (Composer's leading-"/"-on-empty hook is the third). ⌘B (sidebar cycle,
  // T5) is a separate, disjoint listener and is never added here (PIN-D).
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent): void {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        openPalette();
      }
    }
    function onClick(event: MouseEvent): void {
      const target = event.target as Element | null;
      if (target?.closest("[data-search-trigger]")) {
        event.preventDefault();
        openPalette();
      }
    }
    window.addEventListener("keydown", onKeyDown);
    document.addEventListener("click", onClick);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("click", onClick);
    };
  }, []);

  const connectionState = useConnectionStore((s) => s.state);
  const pathname = usePathname();
  const route = urlToPane(pathname);
  const tree = useTreeStore((state) => state.tree);
  const treeError = useTreeStore((state) => state.error);
  const workspacePanes = useWorkspaceStore((state) => state.panes);
  const isMobile = useIsMobile();
  const pendingSessionRef = useRef<string | null>(null);
  // Single-pane mode (the /thread/{ref} share link): the shell strips its own
  // chrome - the rail (which carries the search/settings entry points, floor
  // §2.3) is suppressed below, and the root is marked so wave-8 T6 can finish
  // the presentation (dockview tab-strip suppression + full-viewport) off the
  // [data-single-pane] hook without re-touching this chokepoint. The routed
  // pane itself is the session pane (routing.ts), composer live.
  const singlePane = isSinglePaneRoute(pathname);

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
  const routePlacementInProgressRef = useRef(false);
  const routePlacementPathnameRef = useRef<string | null>(null);
  const placedPathnameRef = useRef<string | null>(null);
  if (!dockHostHasMountedRef.current && openedForPathnameRef.current !== pathname) {
    openedForPathnameRef.current = pathname;
    openRouteAsPane(pathname, tree, treeError, pendingSessionRef);
  }
  if (route !== null) dockHostHasMountedRef.current = true;

  // A route this shell has parsed but not yet placed: pendingSessionRef is
  // what openRouteAsPane leaves behind while a session deep link waits for
  // /api/tree, and it is cleared by the same call that finally places the
  // pane - so it covers the whole wait, including the one commit AFTER the
  // tree lands (the route effect below runs after its children's effects, so
  // the tree arriving is not yet the route being placed). Read here, below
  // the render-phase open, rather than beside the other route derivations
  // above, so it accounts for whatever that call just decided.
  //
  // The mobile host is told because it publishes the focused pane's URL, and
  // would otherwise overwrite the deep link with its own fallback pane's "/"
  // mid-wait (kata bbsv). Desktop needs nothing: DockHost never writes to the
  // address bar, so a deferred route simply sits there until it can be placed.
  const routeDeferred = route?.type === "session" && pendingSessionRef.current !== null;

  // biome-ignore lint/correctness/useExhaustiveDependencies: workspacePanes is a deliberate trigger-only dep for route-owned primary replacement ordering
  useEffect(() => {
    if (route?.type === "settings" || route?.type === "spawn" || route?.type === "session") {
      if (routePlacementInProgressRef.current) {
        const armedPathname = routePlacementPathnameRef.current;
        routePlacementInProgressRef.current = false;
        routePlacementPathnameRef.current = null;
        if (armedPathname === pathname) {
          placedPathnameRef.current = pathname;
          return;
        }
      }
      const allowFocusedPanel =
        pendingSessionRef.current === null &&
        placedPathnameRef.current === pathname &&
        !routePlacementInProgressRef.current;
      if (routePlacementIsApplied(pathname, tree, allowFocusedPanel)) {
        placedPathnameRef.current = pathname;
        return;
      }
      openedForPathnameRef.current = pathname;
      const expectWorkspaceTransition = route.type !== "session" || tree !== null;
      routePlacementInProgressRef.current = expectWorkspaceTransition;
      routePlacementPathnameRef.current = expectWorkspaceTransition ? pathname : null;
      try {
        openRouteAsPane(pathname, tree, treeError, pendingSessionRef);
      } finally {
        if (!expectWorkspaceTransition) {
          routePlacementInProgressRef.current = false;
          routePlacementPathnameRef.current = null;
        }
      }
      return;
    }
    if (route?.type === "welcome") reconcileWelcomeRouteWithTree(tree);
    if (openedForPathnameRef.current === pathname && pendingSessionRef.current === null) return; // already opened above, this render
    openedForPathnameRef.current = pathname;
    openRouteAsPane(pathname, tree, treeError, pendingSessionRef);
  }, [pathname, route?.type, tree, treeError, workspacePanes]);

  return (
    <ClientProvider client={client}>
      <div className={styles.shell} data-single-pane={singlePane ? "" : undefined}>
        <ConnectionBanner state={connectionState} />
        <ToastRegion />
        <CommandPalette />
        <div className={styles.content}>
          {/* Desktop: the rail sits as a flex sibling of DockHost and
              collapses itself. Mobile (<900px): StackHost owns the whole
              region and hosts the rail inside its tree drawer instead
              (TreeDrawer's children slot, threaded via railSlot), so the
              flex sibling renders only on desktop. Both hosts read the
              same pane registry and workspace store. */}
          {!isMobile && route !== null && !singlePane && <RailHost />}
          {route === null ? (
            <NotFound />
          ) : isMobile ? (
            <StackHost railSlot={<RailHost />} routeDeferred={routeDeferred} />
          ) : (
            <DockRegion />
          )}
        </div>
      </div>
    </ClientProvider>
  );
}
