// The <900px workspace host: exactly one pane, full-screen, with its own
// slim back/drawer top bar - the mobile counterpart to DockHost's
// multi-pane dockview desktop layout. Both hosts read the SAME
// useWorkspaceStore and the SAME pane registry; a pane component never
// knows which one is showing it (Global Constraints: "panes never ask 'am
// I mobile?'").
//
// MOUNT CONTRACT for AppShell (wired in at merge time - this stream owns
// only src/shell/mobile/** and shell/useIsMobile.ts, never AppShell.tsx
// itself, so this is a note for whoever does that wiring, not code this
// stream can land):
//
//   import { useIsMobile } from "./useIsMobile";
//   import { StackHost } from "./mobile/StackHost";
//   ...
//   const isMobile = useIsMobile();
//   ...
//   {route === null ? <NotFound /> : isMobile ? <StackHost /> : <DockHost />}
//
// StackHost takes NO PROPS, exactly like DockHost - it needs nothing from
// AppShell beyond being mounted in DockHost's place. A breakpoint crossing
// (DockHost <-> StackHost) unmounts whichever host was showing and mounts
// the other fresh: dockview's own layout/geometry does not survive (its
// DockviewApi is torn down on unmount - see workspace.ts's
// registerDockviewApi(null) in DockHost's own effect cleanup), which is
// accepted, not a bug - layout persistence is desktop-only by design (see
// DockHost.tsx and requirement 5 below). useWorkspaceStore's own
// panes/focusedPaneId (what BOTH hosts actually render from) live above
// either host's lifecycle and survive the swap unaffected. Unlike
// DockHost, StackHost registers no module-level singleton of its own (no
// registerDockviewApi equivalent), so no explicit tear-down is needed on
// unmount beyond what React already does.
//
// Requirement 5 (layout persistence is desktop-only): StackHost has no
// dockview instance and persists nothing to localStorage - "the layout"
// here is just useWorkspaceStore.focusedPaneId, which is not itself saved/
// restored anywhere. A reload lands wherever the URL says (see the URL-
// sync effect below), the same as any other fresh navigation - there is no
// separate "last mobile screen" memory to restore independently of that.
import { Suspense, useEffect, useRef } from "react";
import { IconButton } from "../../widgets";
import { paneFor } from "../paneRegistry";
import { navigate, paneToURL } from "../routing";
import { useWorkspaceStore, workspaceStore, type OpenPaneRecord } from "../workspace";
import { TreeDrawer } from "./TreeDrawer";
import styles from "./StackHost.module.css";

function BackIcon() {
  return (
    <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
      <path
        d="M10 3 L5 8 L10 13"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </svg>
  );
}

// Renders exactly one pane, full-screen: whichever OpenPaneRecord
// useWorkspaceStore currently reports as focused. Uses the SAME registry
// (paneFor) and the SAME PaneProps contract every pane component already
// implements for DockHost - a pane never knows which host is showing it
// (Global Constraints: "panes never ask 'am I mobile?'").
function StackedPane({ pane }: { pane: OpenPaneRecord }) {
  const Component = paneFor(pane.type).component;
  return (
    <Suspense fallback={null}>
      <Component params={pane.params} paneId={pane.id} focused />
    </Suspense>
  );
}

// Pops backStack until it finds an id still present in panes (a pane
// stacked earlier may since have been closed), discarding any stale ones
// along the way. Returns undefined once the stack is exhausted - the
// caller's cue to fall back to welcome.
function popValidBackTarget(backStack: string[], panes: OpenPaneRecord[]): string | undefined {
  while (backStack.length > 0) {
    const candidate = backStack.pop();
    if (candidate !== undefined && panes.some((p) => p.id === candidate)) return candidate;
  }
  return undefined;
}

export function StackHost() {
  const panes = useWorkspaceStore((s) => s.panes);
  const focusedPaneId = useWorkspaceStore((s) => s.focusedPaneId);
  const focusedPane = panes.find((p) => p.id === focusedPaneId) ?? null;

  // This component's OWN back-stack: ids previously focused, most-recent-
  // last. Not part of useWorkspaceStore (the store only ever tracks the
  // CURRENT focus, by design - see workspace.ts's own header comment) and
  // deliberately component-local, not module-level: it resets across a
  // StackHost unmount/remount (e.g. a breakpoint crossing back to desktop
  // and later back to mobile), an accepted, narrow limitation matching
  // DockHost's own disclosed "remount re-runs boot" limitation (see that
  // task's report) - real phones don't cross the 900px breakpoint mid-
  // session, so this only matters for a resized dev browser window.
  const backStackRef = useRef<string[]>([]);
  // The id last seen focused, INCLUDING at mount (initialized from the
  // current value, not null) - AppShell's routing glue may have already
  // focused a pane before StackHost ever mounts (see the module comment
  // below), and that pre-seeded pane must not be misread as "a transition
  // from nothing," which would otherwise leave a phantom null entry out of
  // step with backStackRef.
  const prevFocusedIdRef = useRef<string | null>(focusedPaneId);
  // Set just before a back-triggered focus change, so the bookkeeping
  // effect below can tell "the user tapped back" apart from any other kind
  // of focus change - the abandoned pane must NOT be pushed back onto the
  // stack (that would make back-forward-back oscillate between the same
  // two panes forever instead of walking back through real history).
  const wentBackRef = useRef(false);

  // KNOWN, DISCLOSED COMPOSITION GAP (not fixed this wave - see requirement
  // 2's own "history back/forward works via the existing popstate wiring"
  // and this stream's report): wentBackRef is set ONLY by this component's
  // OWN handleBack below. A REAL browser back/forward action (the
  // hardware/gesture button, not this in-app one) also changes
  // focusedPaneId - via a popstate -> AppShell's routing glue ->
  // openPane()/focusPane() - but with no way for THIS effect to learn that
  // the change was itself already a "backward" step from the user's point
  // of view. The result: this effect pushes the pane the user just used
  // real-back to LEAVE onto backStackRef, same as any ordinary forward
  // navigation would - so tapping this component's OWN back button right
  // afterward pops that pane back into focus, i.e. moves the user FORWARD
  // again instead of continuing backward. Fixing this needs either
  // reading real vs. synthetic popstate events apart (PopStateEvent itself
  // carries no such flag) or replacing this local stack with one driven by
  // window.history's own position - a bigger seam than this task's own
  // component-local design calls for. Flagging for the wave gate's device
  // check (Task 7) to decide fix-vs-document, not guessing at a fix here.
  useEffect(() => {
    const prev = prevFocusedIdRef.current;
    if (prev !== focusedPaneId) {
      if (!wentBackRef.current && prev !== null) backStackRef.current.push(prev);
      wentBackRef.current = false;
      prevFocusedIdRef.current = focusedPaneId;
    }
  }, [focusedPaneId]);

  // URL sync (requirement 2): unlike desktop, exactly one pane is ever
  // visible at a time here, so it's meaningful (and, per this task's
  // spec, required) for the address bar to always name it - reload,
  // share, and bookmark all land back on the same screen. paneToURL
  // returns null for a pane type with no deep link at all (e.g. "doc" -
  // see routing.ts's own documented case); the address bar is simply left
  // alone for those, same as desktop's DockHost never had a reason to
  // touch it either. navigate() itself no-ops when already at the target
  // pathname, so this never produces a redundant history entry on mount
  // when AppShell's own routing glue already put the address bar here
  // first (the common case in the real app).
  useEffect(() => {
    if (!focusedPane) return;
    const url = paneToURL(focusedPane.type, focusedPane.params);
    if (url) navigate(url);
  }, [focusedPane]);

  // Backstop, same reasoning as DockHost's own onReady fallback: a blank
  // stack with no chrome of its own to open a new pane from is a dead end.
  // In the real app AppShell's routing glue already opens SOME pane for
  // every resolvable route before either host ever mounts (see
  // AppShell.tsx's dockHostHasMountedRef comment), so this fires only for
  // a pane that's closed out from under focus, an HMR remount, or this
  // component under test standalone (no AppShell wrapping it).
  useEffect(() => {
    if (!focusedPane) workspaceStore.getState().openPane("welcome");
  }, [focusedPane]);

  function handleBack(): void {
    const target = popValidBackTarget(backStackRef.current, panes);
    wentBackRef.current = true;
    if (target) {
      workspaceStore.getState().focusPane(target);
    } else {
      workspaceStore.getState().openPane("welcome");
    }
  }

  // welcome is the stack's root (the whole app's "nothing open" landing
  // screen - see panes/welcome/Welcome.tsx) - there is never anything
  // meaningful to go "back" to from it, regardless of what this
  // component's own backStackRef happens to hold, so it gets no back
  // affordance at all rather than one that would just loop back to itself.
  const showBack = focusedPane !== null && focusedPane.type !== "welcome";

  return (
    <div className={styles.host}>
      <div className={styles.topBar}>
        <div className={styles.leading}>
          {showBack && <IconButton label="Back" icon={<BackIcon />} variant="quiet" onClick={handleBack} />}
        </div>
        <TreeDrawer />
      </div>
      <div className={styles.body}>
        {focusedPane && (
          // key forces a fresh mount on every distinct pane id, including
          // switching BACK to a previously-seen one - without it, two
          // panes of the SAME registered type (same Component reference)
          // would just re-render in place instead of remounting, breaking
          // the unmount-not-hide contract every pane is designed around
          // (see StackedPane's own comment). Two different pane TYPES
          // already remount naturally (React unmounts on element type
          // change), but same-type-different-params (e.g. two session
          // panes) would not, without this.
          <StackedPane key={focusedPane.id} pane={focusedPane} />
        )}
      </div>
    </div>
  );
}
