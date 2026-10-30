// The workspace shell uses the shared browser connection lifetime, provides
// its client via context, and hosts DockHost (dockview) on desktop; renders NotFound in its
// place for a path urlToPane() can't resolve at all.

import type { AppwireClientLike, NavigationSessionLocation } from "@evener/appwire-client";
import { ACTIONS } from "@evener/appwire-client";
import { isNavigationUnavailable, isSettledGone } from "@evener/appwire-client/state/navigation";
import { useEffect, useLayoutEffect, useReducer, useRef, useState } from "react";
import { keybindingsRegistry } from "../keybindings/appRegistry";
import { isEditableTarget } from "../keybindings/dispatcher";
import { MotionProvider } from "../motion";
import { initNotifications } from "../notifications";
import { requestComposerFocus } from "../panes/session/composer/composerFocus";
import { conversationContextIncludes } from "../panes/session/transcript/openTranscript";
import { isKnownSettingsSection, resolveSettingsSection } from "../panes/settings/sections";
import { zoomParamsCoverConversation } from "../panes/zoom/cascadeCoverage";
import { parseZoomParams, type SessionZoomParams } from "../panes/zoom/intent";
import { startBackgroundTranscripts } from "../stores/backgroundTranscripts";
import { useConnectionStore } from "../stores/connection";
import { cancelConversationHandoff, conversationHandoffGeneration } from "../stores/conversationClaimHandoff";
import {
  selectLiveRows,
  selectLocation,
  selectNextSectionOffset,
  selectSectionCanDemand,
  selectSectionRemaining,
} from "../stores/navigation/selectors";
import { navigationStore, useNavigationStore } from "../stores/navigation/store";
import { syncSettingsHostToRoute } from "../stores/settingsHost";
import { initTranscriptDisplay, transcriptDisplayStore, useTranscriptDisplayStore } from "../stores/transcriptDisplay";
import { ActivitySidebar } from "./activitybar/ActivitySidebar";
import { currentAttentionEntryToken, restoreCurrentAttentionEntry } from "./attentionHistory";
import { ConnectionBanner } from "./ConnectionBanner";
import { CheatsheetOverlay } from "./cheatsheet/CheatsheetOverlay";
import { ToastRegion } from "./chrome/ToastRegion";
import { ClientProvider } from "./clientContext";
import { DockRegion } from "./DockRegion";
import { focusedSessionPaneId, focusedSessionRef, modalOpenInFocusedWindows } from "./focusedSession";
import { HoldHints } from "./holdhints/HoldHints";
import { installKeybindings } from "./installKeybindings";
import { createLiveSessionController } from "./liveSessionController";
import { StackHost } from "./mobile/StackHost";
import { NeedsYouProvider, useNeedsYou } from "./NeedsYouProvider";
import { NotFound } from "./NotFound";
import { CommandPalette } from "./palette/CommandPalette";
import { openPalette, paletteStore } from "./palette/paletteController";
import { RailHost } from "./rail";
import { navigate, observeRouteHistory, openSessionRoute, refParam, urlToPane } from "./routing";
import { cycleSessionPane } from "./sessionCycle";
import { openNestedSessionWithOwner, openTopLevelSession } from "./sessionPlacement";
import { isSinglePaneRoute } from "./singlePane";
import { useBrowserConnection } from "./useBrowserConnection";
import { isMobileViewport } from "./useIsMobile";
import { useKeyboardInset } from "./useKeyboardInset";
import {
  conversationOpenParentRef,
  documentPaneState,
  type OpenPaneRecord,
  presentedPanes,
  sourcePaneSessionRef,
  workspaceStore,
} from "./workspace";
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
import "../panes/zoom"; // registers the "sessionZoom" pane type
import "../panes/sessionPanels"; // registers the session panel pane types
import { initPrefs } from "../stores/prefs";

// Apply persisted display preferences (theme/density/font-size) during
// module evaluation - before first paint - so a saved theme never flashes
// the default. Idempotent; sections re-apply on change. (Wave-7 T4's
// documented wiring line, pre-proven against the full suite by its review.)
initPrefs();
initTranscriptDisplay();

// Wires the activity summary store to the activity panel store: the panel
// requires it before it fetches a continuation page.

// Workspace-only notification election and attention chrome start at module
// evaluation; the comic entry never imports this module.
initNotifications();

import styles from "./AppShell.module.css";

// dockview (pulled in by DockHost.tsx) is ~636kB of the main bundle on its
// own (see Task 2's own report) - dead weight for the mobile path, which
// renders StackHost instead and never touches dockview at all. DockRegion
// owns that lazy load (and the boundary around it) rather than importing
// DockHost eagerly like every other shell module, so dockview's weight lands
// in its own chunk that only a desktop session ever fetches.

export interface AppShellProps {
  // Tests inject a FakeClient so the shared connection lifetime's handshake
  // and navigation publication run without real sockets. Production constructs
  // and retires the one real client through useBrowserConnection.
  client?: AppwireClientLike;
  // Test seam: production (main.tsx) omits this, so the banner uses its
  // built-in 10s reveal delay. Tests pass 0 to drive the banner synchronously
  // without fake-clock plumbing (see ConnectionBannerProps.delayMs).
  bannerDelayMs?: number;
  // Test seam: production omits this, so the banner builds a real client on
  // retry. Tests inject a FakeClient factory to exercise the retry swap
  // without opening a socket.
  bannerCreateClient?: () => AppwireClientLike;
}

function startWorkspaceBackground() {
  return startBackgroundTranscripts(navigationStore);
}

// Hand-rolled rather than react-router (see Task 1's report for the
// justification): the routing surface here is still exactly "re-render
// (and, this task, open a pane) when the path changes" - and pushState
// doesn't fire popstate on its own, so this listens for the same synthetic
// popstate routing.ts's navigate() dispatches.
function readPathname(): string {
  const route = urlToPane(window.location.pathname);
  if (route?.type === "settings") {
    const { section } = route.params as { section?: string };
    if (section !== undefined && !isKnownSettingsSection(section)) {
      // Reconcile the URL before pane placement, so mobile URL sync never
      // receives retired params and cannot push them over this replacement.
      window.history.replaceState(
        window.history.state,
        "",
        `/settings/${resolveSettingsSection(section, null)}${window.location.search}${window.location.hash}`,
      );
    }
  }
  return window.location.pathname;
}

function usePathname(): readonly [string, number] {
  const [pathname, setPathname] = useState(readPathname);
  const [historyRevision, bumpHistoryRevision] = useReducer((revision: number) => revision + 1, 0);
  useEffect(() => {
    const onPopState = () => {
      const nextPathname = readPathname();
      // Synchronize the settings selection with the route the window just moved
      // to, in THIS tick and before React re-renders. A Back/Forward into a
      // host-scoped settings URL otherwise mounts the settings pane on the
      // previous selection - rendering, and reading, for a host the route does
      // not name - because the pane's own adoption runs only in a passive mount
      // effect. navigate() mirrors the app's own navigations; this is the door
      // the browser's Back/Forward comes through (M1, round 6).
      syncSettingsHostToRoute(window.location.pathname, window.location.search);
      setPathname(nextPathname);
      bumpHistoryRevision();
    };
    window.addEventListener("popstate", onPopState);
    const unobserve = observeRouteHistory(() => bumpHistoryRevision());
    // A child mount effect may already have canonicalized the focused pane's
    // URL. Subscribe before catching up so that first navigation is not lost.
    onPopState();
    return () => {
      window.removeEventListener("popstate", onPopState);
      unobserve();
    };
  }, []);
  return [pathname, historyRevision];
}

function sameRouteParams(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

// focusedSessionRef lives in ./focusedSession now, shared with the activity
// surfaces: the ref of the focused pane, but ONLY when that pane IS a session
// pane (never a session panel, a doc, settings, ...) - Mod+I's own "no-op
// when the focused pane isn't a session" contract, and Mod+J's own "cycle
// from the currently-focused session" starting point.

function routePlacementIsApplied(
  pathname: string,
  location: NavigationSessionLocation | null,
  locationTerminal = false,
  locationGone = false,
  allowFocusedCompanion = false,
  allowRestoredInspection = false,
  allowInitialCascade = false,
): boolean {
  const route = urlToPane(pathname);
  if (route === null || route.type === "welcome") return true;

  const workspace = workspaceStore.getState();
  const mainRecord = workspace.mainPane();
  if (mainRecord === null) return false;
  // Startup restoration and contextual drill retain the source's route role.
  // Later pathname changes must still place their ordinary session.
  const routeRole = (pane: OpenPaneRecord): OpenPaneRecord => {
    const params =
      (allowFocusedCompanion || allowInitialCascade) && pane.type === "sessionZoom"
        ? parseZoomParams(pane.params)
        : null;
    const source = params && !params.inspection ? params.source : null;
    return source ? { ...pane, ...source } : pane;
  };
  const main = routeRole(mainRecord);
  const panes = presentedPanes(workspace.panes).map(routeRole);

  if (route.type === "settings" || route.type === "spawn") {
    if (workspace.focusedPaneId !== main.id) return false;
    const matchingType = route.type;
    const matchingPanes = panes.filter((pane) => pane.type === matchingType);
    return main.type === matchingType && sameRouteParams(main.params, route.params) && matchingPanes.length === 1;
  }

  const ref = refParam(route.params);
  if (ref === null) return false;
  const sessionRefOf = (pane: { params: unknown }): string | null => {
    const paneRef = (pane.params as { ref?: unknown }).ref;
    return typeof paneRef === "string" ? paneRef : null;
  };
  if (location === null) {
    if (locationGone) return main.type === "welcome";
    if (!locationTerminal) return false;
  }
  // A terminal read cannot confirm top-level ownership - the recorded parent
  // may itself be nested - so the terminal arm below must not restructure
  // anything (openRouteAsPane's terminal arm only focuses). Placement counts
  // as applied only while the routed conversation is both held beside its
  // recorded context and the visible one: the retained context, not the
  // missing location, owns the placement until a successful read confirms
  // ownership, and a fresh navigation to that route must still focus it.
  const terminalUnconfirmed = location === null;
  const ancestorRef = location === null ? ref : location.top_level ? ref : location.top_level_ref;
  const focusedPane = panes.find((pane) => pane.id === workspace.focusedPaneId);
  const transcriptMatchesRoute = (pane: OpenPaneRecord | undefined): boolean => {
    if (pane?.type !== "transcript") return false;
    const params = pane.params as { ref?: unknown; parentRef?: unknown };
    return (
      // A job log's parent context can cross into a conversation pane's
      // recorded parent chain, so the check must follow the conversation
      // walk (both holder types plus the recorded edge), not the pure
      // transcript-params walk that stops at the first session pane.
      (typeof params.parentRef === "string" && conversationContextIncludes(params.parentRef, ref)) ||
      (ancestorRef !== ref && params.ref === ref && params.parentRef === ancestorRef)
    );
  };
  // A conversation pane carries no parent params: the opened-from context is
  // recorded beside its lifetime. A focused child of that context is a
  // companion of the routed conversation, not a violation of its placement.
  const conversationMatchesRoute = (pane: OpenPaneRecord | undefined): boolean => {
    if (pane?.type !== "session") return false;
    const parentRef = conversationOpenParentRef(pane);
    return parentRef !== undefined && conversationContextIncludes(parentRef, ref);
  };
  const rawFocused = workspace.panes.find((pane) => pane.id === workspace.focusedPaneId);
  const focusedIntent = rawFocused?.type === "sessionZoom" ? parseZoomParams(rawFocused.params) : null;
  const focusedSeparatedInspection =
    (allowFocusedCompanion || allowRestoredInspection) &&
    rawFocused?.slot === "secondary" &&
    !!focusedIntent?.inspection;
  const conversationRef = focusedPane?.type === "session" ? sessionRefOf(focusedPane) : null;
  // The cascade was opened from this route - through its own inspection, the
  // route's session as its source, or a job transcript the route's chain
  // includes - so its column conversations are companions of the route.
  const zoomCoversRouteSource = (pane: OpenPaneRecord, params: SessionZoomParams): boolean => {
    const role = routeRole(pane);
    return (
      !!params.inspection || (role.type === "session" && sessionRefOf(role) === ref) || transcriptMatchesRoute(role)
    );
  };
  const focusedCascadeConversation =
    allowFocusedCompanion &&
    conversationRef !== null &&
    workspace.panes.some((pane) => {
      if (pane.type !== "sessionZoom") return false;
      const params = parseZoomParams(pane.params);
      if (!params || !zoomCoversRouteSource(pane, params)) return false;
      return zoomParamsCoverConversation(params, conversationRef);
    });
  const documentState = focusedPane?.type === "doc" ? documentPaneState(focusedPane) : undefined;
  if (
    allowFocusedCompanion &&
    documentState?.origin === main &&
    (main.type === "session" || main.type === "transcript")
  ) {
    const sourceSession = sourcePaneSessionRef(main);
    const docParams = focusedPane?.params as { session?: unknown; path?: unknown };
    // Filename opens promote their exact source before selecting the document.
    // That living binding, not the old URL's placement, owns this settled view.
    // New/pending routes and generic or stale document bindings still reconcile.
    if (
      typeof sourceSession === "string" &&
      docParams.session === sourceSession &&
      docParams.path === documentState.reference.path
    ) {
      return true;
    }
  }
  if (allowFocusedCompanion && documentState?.origin?.type === "sessionZoom") {
    const origin = documentState.origin;
    const params = parseZoomParams(origin.params);
    // A cascade column's filename open binds its document to the hosting
    // zoom: that pane's cascade covers the routed conversation, so the
    // focused document is the route's companion, like the column
    // conversation above.
    if (params && zoomCoversRouteSource(origin, params)) return true;
  }
  const focusedCompanion =
    focusedPane?.type === "sessionTasks" ||
    focusedPane?.type === "sessionDetails" ||
    transcriptMatchesRoute(focusedPane) ||
    conversationMatchesRoute(focusedPane) ||
    focusedCascadeConversation;
  const focusIsApplied = (paneId: string): boolean =>
    workspace.focusedPaneId === paneId || (allowFocusedCompanion && focusedCompanion) || focusedSeparatedInspection;

  if (ancestorRef === null || ancestorRef === ref) {
    if (terminalUnconfirmed) {
      if (main.type === "session" && sessionRefOf(main) === ref) return focusIsApplied(main.id);
      const held = panes.find((pane) => pane.type === "session" && sessionRefOf(pane) === ref);
      const heldParent = held === undefined ? undefined : conversationOpenParentRef(held);
      return held !== undefined && heldParent !== undefined && heldParent !== ref && focusIsApplied(held.id);
    }
    return (
      focusIsApplied(main.id) &&
      main.type === "session" &&
      sessionRefOf(main) === ref &&
      panes.filter((pane) => pane.type === "session" && sessionRefOf(pane) === ref).length === 1
    );
  }

  const ownerPanes = panes.filter((pane) => pane.type === "session" && sessionRefOf(pane) === ancestorRef);
  const childPanes = panes.filter(
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
function openRouteAsPane(
  pathname: string,
  location: NavigationSessionLocation | null,
  locationTerminal: boolean,
  locationGone: boolean,
  pendingSessionRef: { current: string | null },
): void {
  const route = urlToPane(pathname);
  if (route === null) {
    pendingSessionRef.current = null;
    return;
  }

  if (route.type === "session") {
    const ref = refParam(route.params);
    if (ref === null) return;

    if (locationGone) {
      pendingSessionRef.current = null;
      workspaceStore.getState().replacePrimary("welcome", {});
      return;
    }

    if (location === null && !locationTerminal) {
      pendingSessionRef.current = ref;
      return;
    }

    pendingSessionRef.current = null;
    if (location === null) {
      // A terminal read failed with no retained data, so top-level
      // ownership is unconfirmed: the recorded parent may itself be nested,
      // and placing through it would promote an intermediate pane by
      // discarding the workspace (openNestedSessionWithOwner places its
      // owner through replacePrimary, whose cold arm drops every pane
      // whenever that owner is not already main). A conversation the
      // workspace already holds beside its recorded context keeps every
      // pane instead: focus that conversation, and let a later successful
      // read place by confirmed ownership.
      const held = presentedPanes(workspaceStore.getState().panes).find(
        (pane) => pane.type === "session" && refParam(pane.params) === ref,
      );
      const heldParent = held === undefined ? undefined : conversationOpenParentRef(held);
      if (held !== undefined && heldParent !== undefined && heldParent !== ref) {
        workspaceStore.getState().focusPane(held.id);
        return;
      }
    }
    const ancestorRef = location?.top_level ? ref : (location?.top_level_ref ?? ref);
    if (ancestorRef === ref) {
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

function reconcileWelcomeRouteWithLocation(location: NavigationSessionLocation | null): void {
  if (location === null) return;

  const main = workspaceStore.getState().mainPane();
  if (main?.type !== "session") return;

  const childRef = refParam(main.params);
  if (childRef === null) return;

  if (location.top_level || location.ref !== childRef) return;

  openNestedSessionWithOwner(childRef, location.top_level_ref);
}

function AppShellContent({ client: injectedClient, bannerDelayMs, bannerCreateClient }: AppShellProps) {
  const { client, onClientReplaced } = useBrowserConnection({
    client: injectedClient,
    startBackground: startWorkspaceBackground,
  });
  const { controller } = useNeedsYou();

  // Global chord wiring (floor §2.1 + the UX-fix chords), now driven by the
  // keybindings dispatcher (src/keybindings/, installed app-wide by
  // installKeybindings): this effect registers the three actions AppShell
  // owns - palette.open (⌘K / Ctrl-K from anywhere, through the one
  // openPalette() the whole app shares; Composer's leading-"/"-on-empty hook
  // and the [data-search-trigger] click listener below are the other entry
  // points), composer.focus (⌘I: focuses the focused session pane's composer
  // via composerFocus.ts's per-ref seam - a no-op when the focused pane isn't
  // a session), and next-needs-you (⌘J: cycles the needs-you sessions, tree
  // order, wrapping from whichever session is currently focused, opening a
  // hit through the same top-level/nested seams the rail itself uses -
  // needsYouCycle.ts). ⌘B (sidebar cycle) is RailHost's action, never
  // registered here (PIN-D).
  //
  // The chords' shared guards live on the bindings (keybindings/defaults.ts),
  // not in these runners: all three fire from editable targets
  // (allowInEditable) and yield to a keydown another handler already claimed
  // (defaultPrevented). The modal guard (the old blockedByOpenModal:
  // paletteStore.open, plus the event target sitting inside any
  // [aria-modal="true"] element - every OverlayPanel Dialog/Sheet) is the
  // dispatcher's injected isModalOpen predicate; it suppresses ⌘I/⌘J (the
  // BLOCKER fix: ⌘I stealing composer focus, or ⌘J navigating the tree, out
  // from under a modal the user is still looking at) while ⌘K stays
  // deliberately exempt via its binding's allowInModal - opening the palette
  // while it's already open is a harmless no-op reset, and ⌘K from inside a
  // Dialog is the same "open the palette" intent as anywhere else.
  useEffect(() => {
    installKeybindings();
    const registry = keybindingsRegistry.getState();
    const unregisterActions = [
      registry.registerAction(ACTIONS.paletteOpen, () => openPalette()),
      registry.registerAction(ACTIONS.composerFocus, () => {
        const ref = focusedSessionRef();
        const paneId = focusedSessionPaneId();
        if (ref !== null) requestComposerFocus(ref, paneId);
      }),
      registry.registerAction(ACTIONS.nextNeedsYou, () => void controller.next()),
    ];
    function onClick(event: MouseEvent): void {
      const target = event.target as Element | null;
      if (target?.closest("[data-search-trigger]")) {
        event.preventDefault();
        openPalette();
      }
    }
    document.addEventListener("click", onClick);
    return () => {
      for (const unregister of unregisterActions) unregister();
      document.removeEventListener("click", onClick);
    };
  }, [controller]);

  const connectionState = useConnectionStore((s) => s.state);
  const [pathname, historyRevision] = usePathname();
  const route = urlToPane(pathname);
  const sessionRouteRef = route?.type === "session" ? refParam(route.params) : null;
  const restoredSessionRef = route?.type === "welcome" ? refParam(workspaceStore.getState().mainPane()?.params) : null;
  const locationRef = sessionRouteRef ?? restoredSessionRef;
  const navigationMode = useNavigationStore((state) => state.mode);
  const locationResource = useNavigationStore(selectLocation(locationRef ?? ""));
  const locationFailed = locationResource?.error != null;
  // A stale `gone` tombstone retained across a generation reset is not
  // authoritative: the new generation may show the session present again,
  // so only a settled tombstone redirects to welcome.
  const locationGone = isSettledGone(locationResource);
  const locationNotFound = isNavigationUnavailable(locationResource?.error);
  const locationTerminal = locationFailed && (locationResource?.data === null || locationNotFound);
  const location =
    locationNotFound || locationGone
      ? null
      : ((locationResource?.data as NavigationSessionLocation | undefined) ?? null);
  useEffect(() => {
    if (locationRef === null || navigationMode !== "v3") return;
    if (locationFailed || locationGone || locationResource?.loading || (locationResource && !locationResource.stale))
      return;
    void navigationStore
      .getState()
      .lookupLocation(locationRef)
      .catch(() => undefined);
  }, [locationFailed, locationGone, locationRef, locationResource, navigationMode]);
  const [viewportReady, setViewportReady] = useState(false);
  const viewport = useTranscriptDisplayStore((state) => state.viewport);
  useLayoutEffect(() => {
    // Module initialization can precede the shell mount. Publish that initial
    // drift through preparation too, never select a host from a live snapshot.
    transcriptDisplayStore.getState().setViewport(isMobileViewport() ? "mobile" : "desktop");
    setViewportReady(true);
  }, []);
  const isMobile = viewport === "mobile";
  const handoffGeneration = conversationHandoffGeneration(viewport);
  const hasHost = route !== null;
  // A route departure can leave the workspace's retained pane record alive.
  // biome-ignore lint/correctness/useExhaustiveDependencies: pathname fences pending destination work
  useEffect(() => {
    if (!hasHost) cancelConversationHandoff(handoffGeneration);
    return () => cancelConversationHandoff(handoffGeneration);
  }, [handoffGeneration, hasHost, pathname]);
  // Route-change epoch for in-flight live demands: leaving the session (to settings, the dashboard, another
  // session) and returning BEFORE a demand resolves can restore an identical
  // focused pane id and session ref, so the press-time pane/ref guards alone
  // cannot tell that round trip apart from "never left" - and the stale
  // completion then navigates under a user who has since re-entered the app.
  // A monotonic epoch bumped on every pathname change closes it: the demand
  // completes only onto the route it was pressed on. Bumped during render
  // (not in an effect) so it lands in the same commit as the navigation
  // itself - the same render-phase-ref precedent as renderTimePanesRef
  // below. StrictMode's double render only double-bumps; monotonicity is all
  // the check needs.
  const liveNavRouteEpochRef = useRef(0);
  const liveNavSeenPathnameRef = useRef<string | null>(null);
  if (pathname !== liveNavSeenPathnameRef.current) {
    liveNavSeenPathnameRef.current = pathname;
    liveNavRouteEpochRef.current++;
  }
  // Alt+ArrowLeft/Right cycle focus through the open session panes (Phase 3;
  // cycling semantics live in sessionCycle.ts). Desktop only, following
  // RailHost's rail.toggle pattern: with no action registered on mobile the
  // bindings are inert there. The handlers CLAIM their chord even when
  // cycling no-ops (fewer than two session panes): Alt+ArrowLeft is the
  // browser's Back shortcut, and declining would navigate the SPA's history
  // underneath a user who has one session open.
  //
  // ⌘, (settings.open, Phase 4a) joins this desktop-only group per the p4
  // plan's mobile-inert constraint. Its behavior IS the palette "settings"
  // command's - the action id reuses that command's id (keybindings/
  // actions.ts), and navigate("/settings") is exactly its run body
  // (shell/palette/commands.ts); same shared-seam precedent as
  // next-needs-you above.
  useEffect(() => {
    if (!viewportReady || isMobile) return undefined;
    installKeybindings();
    const registry = keybindingsRegistry.getState();
    // Opens (or re-focuses) a live session by URL, then guarantees the session
    // pane holds focus even when the URL already named the target: navigate()
    // no-ops on an unchanged pathname, so a secondary panel or another pane
    // holding focus would otherwise survive the press. openTopLevelSession
    // never steals focus for a pane it preserves (a restored layout's saved
    // focus must survive a boot placement), so the chord refocuses whatever
    // the placement left in main itself.
    const openLiveSession = (ref: string): void => {
      openSessionRoute(ref);
      const main = workspaceStore.getState().mainPane();
      if (main === null || main.type !== "session" || refParam(main.params) !== ref) {
        openTopLevelSession(ref);
      }
      const placed = workspaceStore.getState().mainPane();
      // focusPane already no-ops for a pane that is focused or absent.
      if (placed !== null) workspaceStore.getState().focusPane(placed.id);
    };
    const liveSessions = createLiveSessionController({
      generation: () => navigationStore.getState().clientGenerationID,
      liveRefs: () => selectLiveRows(navigationStore.getState()).map((row) => row.ref),
      liveRemaining: () => selectSectionRemaining("live", navigationStore.getState()),
      canDemandUnloaded: () => {
        const state = navigationStore.getState();
        return !!state.manifest?.data && selectSectionCanDemand("live", state);
      },
      nextOffset: () => selectNextSectionOffset("live", navigationStore.getState()),
      loadPage: async (offset) => {
        const page = await navigationStore.getState().loadSection("live", offset);
        return page.error === null && page.data !== null;
      },
      focusedPaneId: () => workspaceStore.getState().focusedPaneId,
      focusedSessionRef,
      routeEpoch: () => liveNavRouteEpochRef.current,
      completionBlocked: () =>
        paletteStore.getState().open || modalOpenInFocusedWindows() || isEditableTarget(document.activeElement),
      openLiveSession,
    });
    const unregister = [
      registry.registerAction(ACTIONS.sessionNext, () => cycleSessionPane("next")),
      registry.registerAction(ACTIONS.sessionPrevious, () => cycleSessionPane("previous")),
      // Live-session navigation: unlike session.next/previous (pane focus),
      // these open the adjacent session from the rail's live section in
      // server order, wrapping. openNeedsYouSession is the shared
      // session-URL navigation seam (needsYouCycle.ts).
      registry.registerAction(ACTIONS.sessionLiveNext, () => liveSessions.next()),
      registry.registerAction(ACTIONS.sessionLivePrevious, () => liveSessions.previous()),
      registry.registerAction(ACTIONS.settingsOpen, () => navigate("/settings")),
    ];
    return () => {
      liveSessions.dispose();
      for (const dispose of unregister) dispose();
    };
  }, [isMobile, viewportReady]);
  // Keeps --keyboard-inset current for the mobile .shell rule; see
  // useKeyboardInset.ts's header for the why.
  useKeyboardInset();
  const pendingSessionRef = useRef<string | null>(null);
  // Single-pane mode (the /thread/{ref} share link): the shell strips its own
  // chrome - the rail (which carries the search/settings entry points, floor
  // §2.3) is suppressed below, and the root is marked so wave-8 T6 can finish
  // the presentation (dockview tab-strip suppression + full-viewport) off the
  // [data-single-pane] hook without re-touching this chokepoint. The routed
  // pane itself is the session pane (routing.ts), composer live.
  const singlePane = isSinglePaneRoute(pathname);

  // Re-render trigger for workspaceStore.panes changes, WITHOUT a
  // useSyncExternalStore subscription (which useWorkspaceStore uses). The
  // render-phase openRouteAsPane call below mutates workspaceStore during
  // AppShell's own render; if AppShell subscribed to that store via
  // useSyncExternalStore (zustand's useStore), that mutation would trip
  // React's "Cannot update a component while rendering a different
  // component" warning — exactly the warning kata 9r5y is about. An
  // imperative subscribe() + useReducer dispatch avoids useSyncExternalStore
  // entirely: a useReducer dispatch during render of the SAME component is
  // the documented "adjusting state during render" pattern (React re-renders
  // immediately, no warning). The subscriber only fires when the panes array
  // reference changes, mirroring the Object.is equality the useWorkspaceStore
  // selector applied — the version counter is a trigger-only dep for the
  // route-placement effect below, never read inside the effect body (which
  // reads workspaceStore.getState() directly).
  //
  // The contract this has to match is useSyncExternalStore's, which is
  // "re-render for every panes change from this render onwards", NOT "from
  // the moment the subscription effect runs". Those differ on every mount:
  // the render-phase openRouteAsPane call below mutates panes after this
  // snapshot is taken, and DockHost's layout restore is a descendant passive
  // effect, so both land BEFORE this effect ever subscribes.
  // renderTimePanesRef carries the render's own snapshot into the effect,
  // which re-checks the store against it before subscribing and bumps once
  // if it moved — restoring parity for exactly that window. The
  // route-placement effect's two-phase routePlacementInProgressRef protocol
  // depends on being re-entered after such a change, and without this check
  // its own arm-then-mutate would be swallowed too if it ever ran before
  // this effect; the snapshot makes that correctness independent of the
  // order these two hooks are declared in.
  const [workspacePanesVersion, bumpWorkspacePanesVersion] = useReducer((n: number) => n + 1, 0);
  const renderTimePanesRef = useRef(workspaceStore.getState().panes);
  renderTimePanesRef.current = workspaceStore.getState().panes;
  useEffect(() => {
    let lastPanes = renderTimePanesRef.current;
    const bumpIfPanesChanged = (panes: OpenPaneRecord[]): void => {
      if (panes === lastPanes) return;
      lastPanes = panes;
      bumpWorkspacePanesVersion();
    };
    bumpIfPanesChanged(workspaceStore.getState().panes);
    return workspaceStore.subscribe((state) => bumpIfPanesChanged(state.panes));
  }, []);

  // Opens a pathname's pane during render, not a useEffect, for as long as
  // DockHost hasn't mounted for the very first time yet - a regular effect
  // would run AFTER DockHost/dockview's own onReady (child effects fire
  // before parent effects within a commit), racing its
  // restore-or-fallback-to-welcome boot sequence and landing a deep-linked
  // pane ALONGSIDE a spurious extra welcome tab instead of in its place
  // (see DockHost.tsx's own comment on this for the full reasoning). This
  // is safe as a render-phase call because AppShell no longer subscribes to
  // workspaceStore via useSyncExternalStore (see the imperative subscription
  // above) — the mutation fires the useReducer dispatch on the same
  // component, which is React's supported "adjusting state during render"
  // pattern (re-render, no warning) rather than a useSyncExternalStore
  // snapshot-change-during-render that trips the warning. It is safe not
  // just on AppShell's very first render, but on EVERY render up through
  // whichever one first has route !== null (e.g. loading directly on an
  // unresolved path, where DockHost never mounts at all until the user
  // navigates to a real one - dockHostHasMountedRef tracks this rather than
  // assuming "first AppShell render" and "DockHost's first mount" always
  // coincide, which they don't). Once DockHost has mounted at all, every
  // later pathname change goes through the plain effect below instead -
  // calling openPane() from render with DockHost already mounted and
  // subscribed is exactly what would trip React's "Cannot update a
  // component while rendering a different component" warning if AppShell
  // itself were also subscribed (caught by this task's own test suite, not
  // by inspection, in both the "already mounted at the start" and "just
  // mounted for the first time on a later render" shapes of this race).
  const dockHostHasMountedRef = useRef(false);
  const openedForPathnameRef = useRef<string | null>(null);
  const routePlacementInProgressRef = useRef(false);
  const routePlacementPathnameRef = useRef<string | null>(null);
  const placedPathnameRef = useRef<string | null>(null);
  const restoredAttentionTokenRef = useRef<string | null>(null);
  // Restoration writes only a ref, and the workspace restore may publish no
  // state change at all (a cached location, an already focused reader). This
  // render epoch makes the completion visible to the host's `routeDeferred`.
  const [, setAttentionRestoreEpoch] = useState(0);
  const deferredDocumentRef = useRef<OpenPaneRecord | null>(null);
  const initialPathnameRef = useRef(pathname);
  if (!dockHostHasMountedRef.current && openedForPathnameRef.current !== pathname) {
    openedForPathnameRef.current = pathname;
    if (currentAttentionEntryToken() === null) {
      openRouteAsPane(pathname, location, locationTerminal, locationGone, pendingSessionRef);
    }
  }
  if (route !== null) dockHostHasMountedRef.current = true;

  // A route this shell has parsed but not yet placed: pendingSessionRef is
  // what openRouteAsPane leaves behind while a deep link waits for
  // its bounded location resource, and it is cleared by the same call that
  // finally places the pane - so it covers the whole wait, including the one
  // commit after the location arrives.
  //
  // The mobile host is told because it publishes the focused pane's URL, and
  // would otherwise overwrite the deep link with its own fallback pane's "/"
  // mid-wait (kata bbsv). Desktop needs nothing: DockHost never writes to the
  // address bar, so a deferred route simply sits there until it can be placed.
  //
  // The same wait covers an attention entry whose exact reader still has to be
  // restored: StackHost's URL sync is a child effect, so it runs before this
  // shell's restoration effect and would publish whichever pane happens to be
  // focused at mount. That entry is not always a session URL - a saved
  // layout's conversation is a valid source while the address bar says "/" -
  // so the deferral follows the token, not the route type.
  const attentionToken = currentAttentionEntryToken();
  const routeDeferred =
    (route?.type === "session" && pendingSessionRef.current !== null) ||
    (attentionToken !== null && restoredAttentionTokenRef.current !== attentionToken);

  // biome-ignore lint/correctness/useExhaustiveDependencies: workspacePanesVersion is a deliberate trigger-only dep for route-owned primary replacement ordering
  useEffect(() => {
    // StackHost publishes focused-pane navigation in its child effect, before
    // this parent effect runs. Let usePathname's next render reconcile that
    // route instead of reopening the session the user just left with Back.
    if (pathname !== window.location.pathname) return;
    // Exact runtime entries outrank every route type, not only session URLs: a
    // saved layout's conversation is a valid source reader while the address
    // bar still says "/", so Back from an attention target has to restore its
    // placement before the route branch decides what to place. A stale
    // location read must not rebuild a retained secondary as main either.
    const attentionToken = currentAttentionEntryToken();
    if (attentionToken === null) {
      if (restoredAttentionTokenRef.current !== null) {
        restoredAttentionTokenRef.current = null;
        setAttentionRestoreEpoch((epoch) => epoch + 1);
      }
    }
    if (attentionToken !== null && restoredAttentionTokenRef.current === attentionToken) {
      pendingSessionRef.current = null;
      placedPathnameRef.current = pathname;
      return;
    }
    if (
      attentionToken !== null &&
      restoredAttentionTokenRef.current !== attentionToken &&
      restoreCurrentAttentionEntry()
    ) {
      restoredAttentionTokenRef.current = attentionToken;
      setAttentionRestoreEpoch((epoch) => epoch + 1);
      pendingSessionRef.current = null;
      placedPathnameRef.current = pathname;
      return;
    }
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
      const allowFocusedCompanion =
        pendingSessionRef.current === null &&
        placedPathnameRef.current === pathname &&
        !routePlacementInProgressRef.current;
      const workspace = workspaceStore.getState();
      const focusedPane = workspace.panes.find((pane) => pane.id === workspace.focusedPaneId);
      if (
        route.type === "session" &&
        location !== null &&
        allowFocusedCompanion &&
        focusedPane?.type === "doc" &&
        documentPaneState(focusedPane)?.origin === undefined &&
        deferredDocumentRef.current !== focusedPane
      ) {
        // A native filename click can commit openPane's publication before
        // openDocBeside publishes its exact owner later in the same stack,
        // even when document hydration has already published a reference.
        // Recheck once after that stack, generic docs still yield to the route.
        deferredDocumentRef.current = focusedPane;
        queueMicrotask(bumpWorkspacePanesVersion);
        return;
      }
      const allowRestoredInspection =
        route.type === "session" &&
        openedForPathnameRef.current === pathname &&
        (placedPathnameRef.current === null ||
          (placedPathnameRef.current === pathname && pendingSessionRef.current === refParam(route.params)));
      const allowInitialCascade = placedPathnameRef.current === null && initialPathnameRef.current === pathname;
      if (
        routePlacementIsApplied(
          pathname,
          location,
          locationTerminal,
          locationGone,
          allowFocusedCompanion,
          allowRestoredInspection,
          allowInitialCascade,
        )
      ) {
        pendingSessionRef.current = null;
        placedPathnameRef.current = pathname;
        return;
      }
      openedForPathnameRef.current = pathname;
      const expectWorkspaceTransition = route.type !== "session" || location !== null || locationGone;
      routePlacementInProgressRef.current = expectWorkspaceTransition;
      routePlacementPathnameRef.current = expectWorkspaceTransition ? pathname : null;
      try {
        openRouteAsPane(pathname, location, locationTerminal, locationGone, pendingSessionRef);
      } finally {
        if (!expectWorkspaceTransition) {
          routePlacementInProgressRef.current = false;
          routePlacementPathnameRef.current = null;
        }
      }
      return;
    }
    if (route?.type === "welcome") reconcileWelcomeRouteWithLocation(location);
    if (openedForPathnameRef.current === pathname && pendingSessionRef.current === null) return; // already opened above, this render
    openedForPathnameRef.current = pathname;
    openRouteAsPane(pathname, location, locationTerminal, locationGone, pendingSessionRef);
  }, [pathname, historyRevision, route?.type, location, locationTerminal, locationGone, workspacePanesVersion]);

  return (
    <ClientProvider client={client}>
      <MotionProvider>
        <div className={styles.shell} data-single-pane={singlePane ? "" : undefined}>
          <ConnectionBanner
            state={connectionState}
            delayMs={bannerDelayMs}
            createClient={bannerCreateClient}
            onClientReplaced={onClientReplaced}
          />
          <ToastRegion />
          <CommandPalette />
          {/* The cheatsheet overlay and the hold-modifier hints (Phase 4a):
            desktop-only, so on a touch viewport no cheatsheet action is ever
            registered and its trigger chords stay inert - RailHost's
            rail.toggle no-registration pattern - and no hold-hint listener
            is ever installed. */}
          {viewportReady && !isMobile && <CheatsheetOverlay />}
          {viewportReady && !isMobile && <HoldHints />}
          <div className={styles.content}>
            {/* Desktop: the rail sits as a flex sibling of DockHost and
              collapses itself. Mobile (<900px): StackHost owns the whole
              region and hosts the rail inside its tree drawer instead
              (TreeDrawer's children slot, threaded via railSlot), so the
              flex sibling renders only on desktop. Both hosts read the
              same pane registry and workspace store. */}
            {viewportReady && !isMobile && route !== null && !singlePane && <RailHost />}
            {route === null ? (
              <NotFound />
            ) : !viewportReady ? null : isMobile ? (
              <StackHost railSlot={<RailHost />} routeDeferred={routeDeferred} singlePane={singlePane} />
            ) : (
              // The workspace column carries the dock host's row flex share by
              // class, not by :last-child: the activity sidebar mounts as a
              // sibling AFTER it, and a positional rule would either stretch
              // the sidebar or drop the column's sizing the moment it opens.
              <div className={styles.workspaceColumn} data-testid="workspace-column">
                <div className={styles.workspaceHost} data-testid="workspace-host">
                  <DockRegion />
                </div>
              </div>
            )}
            {/* Desktop: the activity sidebar is the zoom system's triage surface,
              a right shell region scoped to the session being read. Mobile
              StackHost mounts the same surface as its full-screen overlay. */}
            {viewportReady && !isMobile && route !== null && <ActivitySidebar />}
          </div>
        </div>
      </MotionProvider>
    </ClientProvider>
  );
}

export function AppShell(props: AppShellProps) {
  return (
    <NeedsYouProvider>
      <AppShellContent {...props} />
    </NeedsYouProvider>
  );
}
