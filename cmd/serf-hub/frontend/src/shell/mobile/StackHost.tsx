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
import { type ReactNode, Suspense, useEffect, useRef } from "react";
import { Chevron, IconButton } from "../../widgets";
import { useChromeStore } from "../chromeStore";
import { paneFor } from "../paneRegistry";
import { navigate, paneToURL } from "../routing";
import { type OpenPaneRecord, useWorkspaceStore, workspaceStore } from "../workspace";
import styles from "./StackHost.module.css";
import { TreeDrawer } from "./TreeDrawer";

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
// stacked earlier may since have been closed) AND different from
// currentFocusedPaneId, discarding anything else along the way. Returns
// undefined once the stack is exhausted - the caller's cue to fall back to
// welcome. The currentFocusedPaneId check exists for the same reason a real
// browser back/forward doesn't push onto the stack (see
// lastPopstateWasTrusted below): after one real back/forward step, the
// stack's own top entry can be left equal to wherever that step actually
// landed - without this check, the FIRST tap of Back would silently pop
// that stale, already-current entry and do nothing visible, requiring an
// extra tap before Back does anything at all.
function popValidBackTarget(
  backStack: string[],
  panes: OpenPaneRecord[],
  currentFocusedPaneId: string | null,
): string | undefined {
  while (backStack.length > 0) {
    const candidate = backStack.pop();
    if (candidate !== undefined && candidate !== currentFocusedPaneId && panes.some((p) => p.id === candidate))
      return candidate;
  }
  return undefined;
}

// Module-level, not component state: popstate is a window-level browser
// event, not owned by any one StackHost instance (only one is ever mounted
// at a time in practice, the same one-active-host precedent as DockHost's
// own module-level dockviewApi in workspace.ts). Set by the popstate
// listener StackHost installs on mount below; read-and-consumed by the
// bookkeeping effect the first time it sees a focusedPaneId change after
// being set, so a stale `true` can only ever affect the SINGLE next focus
// change, never a later, unrelated one (see that effect's own comment).
//
// KNOWN, DISCLOSED LIMITATION (narrowed by this fix, not eliminated - see
// the wave 3 task 7 report for the live CDP reproduction this is based on):
// this flag only knows "the immediately preceding popstate was real", not
// how many steps of real history navigation actually happened. A SINGLE
// real back/forward composes correctly with the in-app Back button; a
// SECOND, consecutive real back/forward is indistinguishable from this
// component's own vantage point from any other focus change, so the
// stack's stale top entry from the ORIGINAL forward walk can resurface -
// fully eliminating that residual would mean replacing this local stack
// with one driven off window.history's own position, a bigger seam than a
// component-local fix, same conclusion the ORIGINAL version of this
// comment already reached for the whole problem before this fix narrowed
// it to just the multi-step case.
let lastPopstateWasTrusted = false;

// setLastPopstateWasTrustedForTests lets a test simulate a REAL browser
// back/forward's one distinguishing signal - isTrusted - without needing a
// genuinely trusted Event: no script, in jsdom or any real browser, can
// construct one (the DOM spec makes isTrusted a non-configurable own
// accessor on every Event instance specifically so it can never be forged
// - confirmed directly against this exact jsdom version while building
// this fix). No production code should ever call this (mirrors
// workspace.ts's resetWorkspaceStoreForTests precedent).
export function setLastPopstateWasTrustedForTests(value: boolean): void {
  lastPopstateWasTrusted = value;
}

export interface StackHostProps {
  // The rail content for the tree drawer, threaded through to TreeDrawer's
  // children slot (children ARE TreeDrawer's whole rail contract). AppShell
  // — the integrator — passes <Rail/> here; StackHost itself stays
  // rail-agnostic, exactly like TreeDrawer.
  railSlot?: ReactNode;
  // True while the shell has parsed the address bar's route but cannot place
  // it yet — a /s/{ref} deep link waits for /api/tree before it can tell a
  // nested ref from a top-level one (AppShell's openRouteAsPane). AppShell
  // owns that condition and passes it down; StackHost's only duty is not to
  // publish a URL over a route in that state. See the URL-sync effect.
  routeDeferred?: boolean;
}

export function StackHost({ railSlot, routeDeferred = false }: StackHostProps = {}) {
  const panes = useWorkspaceStore((s) => s.panes);
  const focusedPaneId = useWorkspaceStore((s) => s.focusedPaneId);
  // The focused pane's title, published host-agnostically by its
  // PaneScaffold through the chrome store (chromeStore.ts's own comment).
  // null (no pane publishing - e.g. a fixture or a pane mid-swap) leaves
  // the span empty, exactly the empty bar this component drew before the
  // channel existed.
  const paneTitle = useChromeStore((s) => s.paneTitle);
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

  // Installs the ONE popstate listener that feeds lastPopstateWasTrusted
  // (module-level - see its own comment above for why). A REAL browser
  // back/forward gesture (the hardware/gesture button, not this
  // component's own in-app one) changes focusedPaneId via a DIFFERENT path
  // than handleBack below - a popstate -> AppShell's routing glue ->
  // openPane()/focusPane() - and isTrusted is the one signal on that event
  // telling the two apart from each other (verified live: a genuine CDP-
  // driven browser back reports isTrusted=true; routing.ts's own
  // navigate(), a same-tab `new PopStateEvent()` + dispatchEvent(), always
  // reports false - see setLastPopstateWasTrustedForTests's own comment).
  useEffect(() => {
    const onPopState = (e: PopStateEvent) => {
      lastPopstateWasTrusted = e.isTrusted;
    };
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  // wentBackRef (set only by this component's own handleBack, below) and
  // lastPopstateWasTrusted (set only by a REAL browser back/forward, above)
  // are the two distinct reasons a focus change must NOT be recorded as an
  // ordinary forward step - tapping this component's own Back button, or
  // the browser doing the equivalent via real history, must never make the
  // pane just left available to pop right back into focus (that would move
  // the user FORWARD, not continue backward - see popValidBackTarget's own
  // currentFocusedPaneId check for the other half of what a real
  // back/forward needs: the stack's own top can be left matching wherever
  // that step just landed, which needs skipping too, not just not pushed
  // twice). Consumed (read then reset to false) on every actual
  // focusedPaneId change, win or lose, so a stale `true` can only ever
  // affect the SINGLE next change - see lastPopstateWasTrusted's own
  // comment for the one narrow case (a popstate that doesn't end up
  // changing focus at all) this doesn't fully close.
  useEffect(() => {
    const prev = prevFocusedIdRef.current;
    if (prev !== focusedPaneId) {
      const realBrowserNav = lastPopstateWasTrusted;
      lastPopstateWasTrusted = false;
      if (!wentBackRef.current && !realBrowserNav && prev !== null) backStackRef.current.push(prev);
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
  //
  // routeDeferred suspends the sync (kata bbsv): a deep-linked session route
  // takes a beat to place (it waits on /api/tree, which cannot resolve inside
  // the first commit), and the backstop below fills that beat with welcome.
  // Publishing welcome's "/" then would overwrite the very deep link the
  // shell is still working on, discarding it before the tree it waits for
  // arrives. The address bar already names where we are going, so leaving it
  // alone is also the honest reading of "always name the visible pane".
  useEffect(() => {
    if (routeDeferred) return;
    if (!focusedPane) return;
    const url = paneToURL(focusedPane.type, focusedPane.params);
    if (url) navigate(url);
  }, [focusedPane, routeDeferred]);

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
    const target = popValidBackTarget(backStackRef.current, panes, focusedPaneId);
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
          {showBack && (
            <IconButton
              label="Back"
              icon={<Chevron direction="left" size={16} />}
              variant="quiet"
              onClick={handleBack}
            />
          )}
        </div>
        <span className={styles.title} data-testid="topbar-title">
          {paneTitle}
        </span>
        <TreeDrawer>{railSlot}</TreeDrawer>
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
