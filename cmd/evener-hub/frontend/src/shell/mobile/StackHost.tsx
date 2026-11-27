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
import {
  type CSSProperties,
  createContext,
  type ReactNode,
  type RefObject,
  Suspense,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { AnimatePresence, m, spatialExitTransition, spatialTransition, useReducedMotion } from "../../motion";
import { openSessionOverview, SessionInspectionActions } from "../../panes/session/chrome/SessionInspectionActions";
import { requestFindInSession } from "../../panes/session/find/findRequest";
import { readerScrollInput } from "../../panes/session/transcript/flow/useReaderGestures";
import { returnFromAgentCascade } from "../../panes/zoom/actions";
import { isCascadeReturnedOrigin } from "../../panes/zoom/inspectionOrigin";
import { parseZoomParams } from "../../panes/zoom/intent";
import {
  conversationHandoffGeneration,
  registerConversationHostVisibility,
  restrictConversationHandoff,
} from "../../stores/conversationClaimHandoff";
import { selectNeedsYouCount } from "../../stores/navigation/selectors";
import { type NavigationStoreState, useNavigationStore } from "../../stores/navigation/store";
import {
  clampSidebarWidth,
  type FontSizePref,
  prefsStore,
  SIDEBAR_WIDTH_MAX,
  SIDEBAR_WIDTH_MIN_TOUCH,
  usePrefsStore,
} from "../../stores/prefs";
import { useTopNotesExpanded } from "../../stores/topNotes";
import { Chevron, IconButton } from "../../widgets";
import { ActivitySidebar } from "../activitybar/ActivitySidebar";
import { useActivitySidebarStore } from "../activitybar/activitySidebarStore";
import { attentionEntryOwnsReader, goBackInAttentionHistory, publishFocusedPaneURL } from "../attentionHistory";
import { ConversationHandoffBoundary } from "../ConversationHandoffBoundary";
import { ReaderHeaderPinContext, useChromeStore } from "../chromeStore";
import {
  hasIPhoneSessionHeader,
  hasIPhoneSessionsToolbar,
  hasIPhoneWorkspaceControls,
  tellIPhoneHost,
  useIPhoneEdgeToolbar,
} from "../iPhoneHost";
import { paneFor } from "../paneRegistry";
import { PaneVisibilityContext } from "../paneVisibility";
import { RAIL_WIDTH_PROPERTY, RailResizeHandle } from "../rail/RailResizeHandle";
import { RailHeaderActionProvider } from "../rail/railHeaderAction";
import { SidebarIcon } from "../rail/railIcons";
import { navigate, paneToURL, refParam, urlToPane } from "../routing";
import { openSessionByRef } from "../sessionPlacement";
import { isSinglePaneRoute } from "../singlePane";
import {
  isHomeScreenWindow,
  TABLET_QUERY,
  useHomeScreenEdge,
  useMediaQuery,
  useWorkspaceViewportWidth,
} from "../useIsMobile";
import {
  conversationOpenParentRef,
  documentPaneState,
  type OpenPaneRecord,
  presentedPanes,
  transcriptOpenOrigin,
  useWorkspaceStore,
  workspaceStore,
} from "../workspace";
import { IPhoneSessionHeader } from "./IPhoneSessionHeader";
import { MobilePanel } from "./MobilePanel";
import styles from "./StackHost.module.css";
import {
  pruneDetachedOffsets,
  type ScrollOffsets,
  type StackTransitionKind,
  startStackTransition,
} from "./stackTransition";
import { TreeDrawer } from "./TreeDrawer";

const TRANSITION_CLASSES = {
  enter: styles.paneEnter ?? "",
  leavingUnder: styles.leavingUnder ?? "",
  leavingOver: styles.leavingOver ?? "",
};

// Floating-header thresholds (docs/web-ui/design-system.md §12): collapse
// once a session-family pane's body scrolls past 48px going down; reveal on
// an 8px upward step or at the very top.
const FLOAT_COLLAPSE_PAST = 48;
const FLOAT_REVEAL_STEP = 8;
const FLOAT_REVEAL_AT_TOP = 12;
type ReadingChrome = { floating: boolean; notes: "resting" | "hidden" | "overlay" };
const FULL_CHROME: ReadingChrome = { floating: false, notes: "resting" };

function readingChromeHeightChange(
  from: ReadingChrome,
  to: ReadingChrome,
  heights: { bar: number; notes: number },
): number {
  function height(chrome: ReadingChrome) {
    if (heights.notes > 0 && chrome.notes !== "resting") return 0;
    return (chrome.floating ? 0 : heights.bar) + heights.notes;
  }
  return height(to) - height(from);
}

// Only a session-family pane's primary reading scroll port (the VirtualList
// root directly inside a reading scroll host) drives the floating header;
// nested scrollers like composer menus and escalation rails stay independent.
const READING_SCROLLER_SELECTOR = "[data-reading-scroll-host] > *";

// Elastic overscroll pushes scrollTop outside [0, maxScrollTop], and the
// rebound moves in the opposite direction while still out of bounds. Clamping
// keeps overscroll and its rebound position-neutral so neither flips the top
// bar; unmeasured jsdom boxes report zero for both heights and clamp only at 0.
function clampedScrollTop(scroller: Element): number {
  const top = scroller.scrollTop;
  const scrollHeight = scroller.scrollHeight;
  const clientHeight = scroller.clientHeight;
  if (scrollHeight <= 0 && clientHeight <= 0) return Math.max(0, top);
  return Math.max(0, Math.min(top, Math.max(0, scrollHeight - clientHeight)));
}

// The header uses full touch targets for every pointer in the tablet band.
const BOARD_MIN_WIDTH = SIDEBAR_WIDTH_MIN_TOUCH;
// Leave room for the Find query and its clear button beside the match controls.
const READER_MIN_WIDTH: Record<FontSizePref, number> = { s: 392, m: 392, l: 408, xl: 432 };

const BoardPresentContext = createContext(true);

// The tablet persistent board column. AnimatePresence keeps an exiting element
// mounted on its previous props until the left-edge slide completes, so reading
// presence from context makes the leaving column inert and yields the single
// Board toggle immediately on collapse.
function TabletBoardColumn({
  boardRef,
  boardWidth,
  boardMaxWidth,
  boardToggle,
  railSlot,
  transition,
  exitTransition,
  onBoardInput,
}: {
  boardRef: RefObject<HTMLElement | null>;
  boardWidth: number;
  boardMaxWidth: number;
  boardToggle: ReactNode;
  railSlot: ReactNode;
  transition: ReturnType<typeof spatialTransition>;
  exitTransition: ReturnType<typeof spatialExitTransition>;
  onBoardInput: () => void;
}) {
  const present = useContext(BoardPresentContext);
  const reducedMotion = useReducedMotion() === true;
  const [opening, setOpening] = useState(false);
  return (
    <m.aside
      className={styles.boardColumn}
      data-testid="board-column"
      data-motion={!present ? "closing" : opening ? "opening" : undefined}
      inert={!present}
      aria-hidden={present ? undefined : true}
      ref={present ? boardRef : undefined}
      initial={reducedMotion ? false : { x: "-100%" }}
      animate={{ x: 0 }}
      exit={reducedMotion ? undefined : { x: "-100%", transition: exitTransition }}
      transition={transition}
      onAnimationStart={() => setOpening(true)}
      onAnimationComplete={() => setOpening(false)}
      onPointerDownCapture={present ? onBoardInput : undefined}
      onKeyDownCapture={present ? onBoardInput : undefined}
      onClickCapture={present ? onBoardInput : undefined}
      style={{ [RAIL_WIDTH_PROPERTY]: `${boardWidth}px` } as CSSProperties}
    >
      <RailHeaderActionProvider value={present ? boardToggle : null}>{railSlot}</RailHeaderActionProvider>
      <RailResizeHandle
        width={boardWidth}
        minWidth={BOARD_MIN_WIDTH}
        maxWidth={boardMaxWidth}
        onCommit={(width) => prefsStore.getState().setSidebarWidth(width)}
        railRef={boardRef}
      />
    </m.aside>
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
    <PaneVisibilityContext.Provider value>
      <Suspense fallback={null}>
        <ConversationHandoffBoundary generation={conversationHandoffGeneration("mobile")} paneId={pane.id}>
          <Component params={pane.params} paneId={pane.id} focused />
        </ConversationHandoffBoundary>
      </Suspense>
    </PaneVisibilityContext.Provider>
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

// True when `pathname` is a SINGLE-PANE route that already names this exact
// pane - today that is only /thread/{ref}, the share link (singlePane.ts).
//
// /thread/{ref} and /s/{ref} resolve to the same session pane (routing.ts),
// but only /thread turns single-pane mode on: AppShell reads
// isSinglePaneRoute off the PATHNAME to strip the shell's chrome, so the mode
// lives entirely in the address bar. paneToURL can only ever serialize a
// session pane back to /s/{ref}, so publishing it over a /thread route
// rewrites a URL a user was given into one that means something else. Desktop
// never does that (DockHost writes no URL at all, so single-pane mode holds
// for the whole visit); this is the sync declining to invent a mobile-only
// rewrite, not a mobile special case.
//
// Params compare by JSON, the same plain structural equality workspace.ts's
// own sameParams uses for these small always-JSON-safe param bags.
function singlePaneRouteNames(pathname: string, pane: OpenPaneRecord): boolean {
  if (!isSinglePaneRoute(pathname)) return false;
  const route = urlToPane(pathname);
  return route !== null && route.type === pane.type && JSON.stringify(route.params) === JSON.stringify(pane.params);
}

function readerSessionRef(pane: OpenPaneRecord | null | undefined): string | null {
  if (pane?.type === "session") return refParam(pane.params);
  if (pane?.type === "sessionZoom") return parseZoomParams(pane.params)?.ref ?? null;
  return null;
}

function attentionEntryOwnsPane(pane: OpenPaneRecord | null | undefined): boolean {
  const ref = readerSessionRef(pane);
  return !!pane && ref !== null && attentionEntryOwnsReader(pane.id, ref);
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

// The scroll-offsets map of the mounted StackHost, for tests only: the map
// is pure bookkeeping with no DOM footprint, so a test asserting its
// invariant - offsets for elements inside the shown pane's slot only,
// nothing detached - has nothing else to read. It tracks the latest mount;
// exactly one StackHost is alive at a time (the app has one phone shell,
// tests unmount between rows). No production code should ever call this
// (mirrors setLastPopstateWasTrustedForTests precedent).
let mountedScrollOffsets: ScrollOffsets | null = null;

export function scrollOffsetsForTests(): ScrollOffsets | null {
  return mountedScrollOffsets;
}

export interface StackHostProps {
  // The rail content, threaded through to MobilePanel's `rail` prop (the
  // panel's whole rail contract). AppShell — the integrator — passes
  // <Rail/> here; StackHost itself stays rail-agnostic, exactly like the
  // panel and the trigger-only TreeDrawer.
  railSlot?: ReactNode;
  // True while the shell has parsed the address bar's route but cannot place
  // it yet — a /s/{ref} deep link waits for its navigation location resource before it can tell a
  // nested ref from a top-level one (AppShell's openRouteAsPane). AppShell
  // owns that condition and passes it down; StackHost's only duty is not to
  // publish a URL over a route in that state. See the URL-sync effect.
  routeDeferred?: boolean;
  // True on a single-pane route (the /thread/{ref} share link). The drawer
  // opens the sidebar/search/settings chrome floor §2.3 forbids there, so its
  // trigger is not rendered at all.
  singlePane?: boolean;
}

export function StackHost({ railSlot, routeDeferred = false, singlePane = false }: StackHostProps = {}) {
  const livingPanes = useWorkspaceStore((s) => s.panes);
  const panes = useMemo(() => presentedPanes(livingPanes), [livingPanes]);
  const focusedPaneId = useWorkspaceStore((s) => s.focusedPaneId);
  // The focused pane's title, published host-agnostically by its
  // PaneScaffold through the chrome store (chromeStore.ts's own comment).
  // null (no pane publishing - e.g. a fixture or a pane mid-swap) leaves
  // the span empty, exactly the empty bar this component drew before the
  // channel existed.
  const paneTitle = useChromeStore((s) => s.paneTitle);
  // The focused pane's own "up" target, published host-agnostically through
  // the same channel (chromeStore.ts's paneBack comment): settings' section
  // drill-down is invisible to this component's focus-keyed back-stack (the
  // singleton pane's id never changes across sections), so while a pane
  // publishes one, the top-bar Back invokes IT instead of the stack walk.
  const paneBack = useChromeStore((s) => s.paneBack);
  const focusedPane = panes.find((p) => p.id === focusedPaneId) ?? null;
  const handoffGeneration = conversationHandoffGeneration("mobile");
  useEffect(() => {
    const visible = () => new Set(focusedPane ? [focusedPane.id] : []);
    restrictConversationHandoff(handoffGeneration, visible());
    return registerConversationHostVisibility("mobile", visible);
  }, [handoffGeneration, focusedPane]);
  const activityOpen = useActivitySidebarStore((s) => s.open);
  const [panelOpen, setPanelOpen] = useState(false);
  const closePanel = useCallback(() => setPanelOpen(false), []);
  const [readingChrome, setReadingChrome] = useState(FULL_CHROME);
  const topBarCollapsed = readingChrome.floating;
  const [readerControlsPinned, setReaderControlsPinned] = useState(false);
  const fontSize = usePrefsStore((s) => s.fontSize);
  const viewportWidth = useWorkspaceViewportWidth();
  const tabletViewport = useMediaQuery(TABLET_QUERY);
  const roomForHostedBoard = useMediaQuery(
    `(min-width: ${BOARD_MIN_WIDTH + READER_MIN_WIDTH[fontSize]}px) and (min-height: 600px)`,
  );
  const homeScreenEdge = useHomeScreenEdge();
  const hosted = hasIPhoneSessionHeader();
  const edgeToolbar = useIPhoneEdgeToolbar();
  const tabletBand =
    tabletViewport ||
    ((hosted || isHomeScreenWindow()) &&
      roomForHostedBoard &&
      viewportWidth >= BOARD_MIN_WIDTH + READER_MIN_WIDTH[fontSize]);
  const tabletSplit = tabletBand && !singlePane;
  const [boardCollapsed, setBoardCollapsed] = useState(false);
  const boardVisible = tabletSplit && !boardCollapsed;
  const hostedSession = hosted && !singlePane && focusedPane?.type === "session";
  const sessionHeader = hostedSession && !routeDeferred;
  const nativeHeader = sessionHeader && (!tabletBand || edgeToolbar);
  const publishHeader = nativeHeader || (sessionHeader && hasIPhoneWorkspaceControls());
  useLayoutEffect(() => {
    // A missing session can settle on a different pane after the phone starts restoring it.
    if (!routeDeferred && !publishHeader && hasIPhoneWorkspaceControls()) {
      tellIPhoneHost({ type: "sessionHeader", header: null });
    }
  }, [routeDeferred, publishHeader]);
  const welcomeLauncher =
    !singlePane && !tabletSplit && !routeDeferred && (focusedPane === null || focusedPane.type === "welcome");
  const sessionsEdge = !singlePane && edgeToolbar && hasIPhoneSessionsToolbar();
  const homeEdge =
    !singlePane && !hosted && (focusedPane?.type === "session" || welcomeLauncher) ? homeScreenEdge : null;
  const systemEdge = (hostedSession && edgeToolbar) || (welcomeLauncher && sessionsEdge);
  // An overlay can suspend edge actions, but only a change of layout moves them into the top bar.
  const actionsAtEdge = systemEdge || !!homeEdge;
  const Title = actionsAtEdge ? "h1" : "span";
  const needsYou = useNavigationStore(
    useCallback(
      (state: NavigationStoreState) =>
        publishHeader || (tabletSplit && !boardVisible) ? selectNeedsYouCount(state) : 0,
      [publishHeader, tabletSplit, boardVisible],
    ),
  );
  const sidebarWidth = usePrefsStore((s) => s.sidebarWidth);
  const boardMaxWidth = Math.min(
    SIDEBAR_WIDTH_MAX,
    Math.max(BOARD_MIN_WIDTH, viewportWidth - READER_MIN_WIDTH[fontSize]),
  );
  const boardWidth = clampSidebarWidth(sidebarWidth, BOARD_MIN_WIDTH, boardMaxWidth);
  const boardRef = useRef<HTMLElement>(null);
  const boardToggleRef = useRef<HTMLButtonElement>(null);
  const restoreToggleFocusRef = useRef(false);
  const boardTransition = useMemo(() => spatialTransition(), []);
  const boardExitTransition = useMemo(() => spatialExitTransition(), []);

  function toggleBoard() {
    restoreToggleFocusRef.current = boardToggleRef.current === document.activeElement;
    setBoardCollapsed((collapsed) => !collapsed);
  }

  useLayoutEffect(() => {
    if (!restoreToggleFocusRef.current) return;
    restoreToggleFocusRef.current = false;
    boardToggleRef.current?.focus({ preventScroll: true });
  });

  // True when no real pane is focused: either nothing at all, or the
  // welcome pane (the app's "nothing open" landing screen). The mobile
  // panel auto-opens in this state (see the effect below) so the rail —
  // the only way to open a pane on a phone — is one tap away, the same
  // role the always-docked desktop rail plays without a panel.
  const nothingFocused = focusedPaneId === null || focusedPane?.type === "welcome";

  // This component's OWN back-stack: ids previously focused, most-recent-
  // last. Not part of useWorkspaceStore (the store only ever tracks the
  // CURRENT focus, by design - see workspace.ts's own header comment) and
  // deliberately component-local, not module-level: it resets across a
  // StackHost unmount/remount (e.g. a breakpoint crossing back to desktop
  // and later back to mobile). Transcript panes retain their immediate
  // parent reference separately; handleBack uses that only when this local
  // history yields no valid target, without persisting generic history.
  const backStackRef = useRef<string[]>([]);
  const [, setBackStackRevision] = useState(0);
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

  // Pane-to-pane motion (stackTransition.ts). popPendingRef marks a Back tap
  // whose focus change has not landed yet, so that change slides as a pop; a
  // Back that changes nothing leaves it set until the next press clears it.
  // chromeWasOpenRef is whether the drawer or the Overview was open at the
  // last commit: a pane chosen there replaces the stack's top, it is not a
  // step deeper, and the same tap usually closes the surface it came from.
  // boardInputRef tracks whether the latest pointer/key/click intent landed in
  // the docked tablet board, which stays open beside the reader.
  const stackBodyRef = useRef<HTMLDivElement>(null);
  const paneSlotRef = useRef<HTMLDivElement>(null);
  const committedSlotRef = useRef<HTMLDivElement | null>(null);
  const transitionFocusRef = useRef<string | null>(focusedPaneId);
  const popPendingRef = useRef(false);
  const chromeWasOpenRef = useRef(false);
  const boardInputRef = useRef(false);
  const endTransitionRef = useRef<(() => void) | null>(null);
  const scrollOffsetsRef = useRef<ScrollOffsets>(new Map());
  const topBarRef = useRef<HTMLDivElement>(null);
  // The capture listener stays installed across reading chrome transitions.
  const readingChromeRef = useRef(FULL_CHROME);
  const lastScrollTopRef = useRef(0);
  // The scroller's scrollHeight as of the last event. A change between
  // events means content shifted under the reader - the transcript prepends
  // older turns as you scroll toward the top, and appends grow it below -
  // so the scrollTop jump that arrives with it is not a gesture.
  const lastScrollHeightRef = useRef(0);
  // Consecutive movement counts toward a deliberate reversal even on 120Hz displays.
  const scrollRunRef = useRef(0);
  // The conversation can open at either end. Its first reading direction
  // tucks Notes; a sustained scroll in the other direction brings it back.
  const notesReadingDirectionRef = useRef(0);
  // Measured before the chrome leaves the flow, used to hold the reading
  // position when navigation and the notes summary fold or return separately.
  const collapseScrollerRef = useRef<Element | null>(null);
  const chromeHeightsRef = useRef({ bar: 0, notes: 0 });
  const previousChromeRef = useRef(FULL_CHROME);
  const focusedPaneRef = useRef(focusedPane);
  focusedPaneRef.current = focusedPane;
  const headerPinnedRef = useRef(false);
  const prevFocusedTypeRef = useRef<string | undefined>(focusedPane?.type);

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
    const focusedType = focusedPane?.type;
    const typeChanged = prevFocusedTypeRef.current !== focusedType;
    prevFocusedTypeRef.current = focusedType;
    if (prev !== focusedPaneId || typeChanged) {
      // A focus or pane-type change always restores the solid top bar
      // (docs/web-ui/design-system.md §12): the next pane starts at the top of
      // its own scroll, and non-session panes never collapse it anyway.
      // Clearing the collapse scroller disarms the compensation effect for
      // reveals that are not scroll-driven (this one).
      readingChromeRef.current = FULL_CHROME;
      setReadingChrome(FULL_CHROME);
      lastScrollTopRef.current = 0;
      lastScrollHeightRef.current = 0;
      scrollRunRef.current = 0;
      notesReadingDirectionRef.current = 0;
      collapseScrollerRef.current = null;
    }
    if (prev !== focusedPaneId) {
      const realBrowserNav = lastPopstateWasTrusted;
      lastPopstateWasTrusted = false;
      const attentionRestore = attentionEntryOwnsPane(focusedPane);
      if (!wentBackRef.current && !realBrowserNav && !attentionRestore && prev !== null) {
        backStackRef.current.push(prev);
        // The stack is mutable navigation history; publishing its revision lets
        // the top bar exchange Sessions for Back as soon as a destination exists.
        setBackStackRevision((revision) => revision + 1);
      }
      wentBackRef.current = false;
      prevFocusedIdRef.current = focusedPaneId;
    }
  }, [focusedPane, focusedPaneId]);

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
  // takes a beat to place (it waits on its navigation location resource, which cannot resolve inside
  // the first commit), and the backstop below fills that beat with welcome.
  // Publishing welcome's "/" then would overwrite the very deep link the
  // shell is still working on, discarding it before the tree it waits for
  // arrives. The address bar already names where we are going, so leaving it
  // alone is also the honest reading of "always name the visible pane".
  //
  // A single-pane route that already names the focused pane is left alone for
  // the same reason, one step further: see singlePaneRouteNames above.
  useEffect(() => {
    if (routeDeferred) return;
    if (!focusedPane) return;
    if (attentionEntryOwnsPane(focusedPane)) return;
    if (singlePaneRouteNames(window.location.pathname, focusedPane)) return;
    const path = paneToURL(focusedPane.type, focusedPane.params);
    // A pane identifies its path. The active URL also owns launcher prefill,
    // the settings host and any fragment the user followed.
    const url = path === window.location.pathname ? path + window.location.search + window.location.hash : path;
    if (url) {
      if (focusedPane.slot === "secondary" && isCascadeReturnedOrigin(focusedPane)) {
        publishFocusedPaneURL(url);
      } else {
        navigate(url);
      }
    }
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

  // Opens the mobile panel whenever nothing real is focused — the rail
  // (the only pane-opener on a phone) lives inside it, so a blank or
  // welcome screen must not strand the user with no way forward. Guarded
  // by routeDeferred: a deep-linked route spends a beat parsed-but-unplaced
  // (AppShell's openRouteAsPane waiting on its navigation resource), which
  // the backstop above fills with welcome — opening the panel over that
  // would flash it for one frame before the real pane lands, so the panel
  // stays closed until the route resolves (the same beat the URL-sync effect
  // declines to publish over, see its own comment).
  useEffect(() => {
    if (tabletSplit) setPanelOpen(false);
    else if (!routeDeferred && nothingFocused) setPanelOpen(true);
  }, [nothingFocused, routeDeferred, tabletSplit]);

  useEffect(() => {
    if (activityOpen) setPanelOpen(false);
  }, [activityOpen]);

  // Records the shown pane's scroll offsets as they change: the old pane's
  // last frame is a detached subtree, which has lost them by the time it
  // slides. Only elements inside the current pane slot are recorded - the
  // leaving layer's restored offsets queue scroll events of its own, and
  // those must not put a pane the stack is discarding back in the map.
  // The map holds offsets for the shown pane only, nothing detached.
  useEffect(() => {
    const body = stackBodyRef.current;
    if (!body) return;
    const offsets = scrollOffsetsRef.current;
    mountedScrollOffsets = offsets;
    const onScroll = (event: Event) => {
      const target = event.target;
      if (target instanceof Element && paneSlotRef.current?.contains(target)) {
        pruneDetachedOffsets(offsets);
        offsets.set(target, [target.scrollTop, target.scrollLeft]);
      }
    };
    body.addEventListener("scroll", onScroll, { capture: true, passive: true });
    return () => {
      body.removeEventListener("scroll", onScroll, { capture: true });
      if (mountedScrollOffsets === offsets) mountedScrollOffsets = null;
    };
  }, []);

  // Floating top bar (docs/web-ui/design-system.md §12). A capture listener
  // next to the offsets one above, because both watch the same scroll of the
  // shown pane's slot. It collapses the bar only for session-family panes
  // (session, sessionZoom, transcript) - the board and every inspector pane
  // keep their solid header - and never for scrollers outside the slot (the
  // drawer's own list and the tablet board scroll outside this body).
  useEffect(() => {
    const body = stackBodyRef.current;
    if (!body) return;
    const onScroll = (event: Event) => {
      const target = event.target;
      if (
        !(target instanceof Element) ||
        !paneSlotRef.current?.contains(target) ||
        !target.matches(READING_SCROLLER_SELECTOR)
      )
        return;
      const scrollTop = clampedScrollTop(target);
      const prevScrollTop = lastScrollTopRef.current;
      lastScrollTopRef.current = scrollTop;
      const heightChanged = target.scrollHeight !== lastScrollHeightRef.current;
      lastScrollHeightRef.current = target.scrollHeight;
      const type = focusedPaneRef.current?.type;
      const input = readerScrollInput.get(target);
      // A content shift contributes no travel in either direction. A slow
      // reversal can span several streaming updates.
      if (heightChanged || input === "opening") return;
      if (type !== "session" && type !== "sessionZoom" && type !== "transcript") return;
      if (headerPinnedRef.current || scrollTop === prevScrollTop) return;
      collapseScrollerRef.current = target;
      const current = readingChromeRef.current;
      const step = scrollTop - prevScrollTop;
      const direction = Math.sign(step);
      if (input === "followingEnd") {
        // Measurement can extend the tail again after the first confirmed
        // arrival. A native thumb drag leaves that end at stable geometry.
        const end = target.scrollHeight - target.clientHeight;
        if (step > 0 || Math.abs(prevScrollTop - end) > 1 || scrollTop >= end) return;
        readerScrollInput.delete(target);
      }
      // WebKit can move back toward the tail when folding Notes grows the
      // viewport. Only the finger can reverse a touch scroll.
      if (typeof input === "number" && input !== direction && scrollTop >= FLOAT_REVEAL_AT_TOP) return;
      const run = Math.sign(scrollRunRef.current) === direction ? scrollRunRef.current + step : step;
      scrollRunRef.current = run;
      let { floating, notes } = current;
      if (scrollTop < FLOAT_REVEAL_AT_TOP) {
        floating = false;
        notes = "resting";
        notesReadingDirectionRef.current = 0;
      } else {
        if (step > 0 && scrollTop > FLOAT_COLLAPSE_PAST) floating = true;
        if (run <= -FLOAT_REVEAL_STEP) floating = false;
        if (
          Math.abs(run) >= FLOAT_REVEAL_STEP &&
          (notesReadingDirectionRef.current || scrollTop > FLOAT_COLLAPSE_PAST)
        ) {
          notes =
            !notesReadingDirectionRef.current || direction === notesReadingDirectionRef.current ? "hidden" : "overlay";
        }
      }
      if (floating === current.floating && notes === current.notes) return;
      const next = { floating, notes };
      const heights = chromeHeightsRef.current;
      if (current.notes !== "hidden") {
        const panel = paneSlotRef.current?.querySelector<HTMLElement>("[data-notes-panel-ref]");
        if (panel?.contains(document.activeElement) || panel?.dataset.expanded === "true") return;
        heights.notes = panel?.offsetHeight ?? 0;
      }
      if (!current.floating && topBarRef.current) heights.bar = topBarRef.current.offsetHeight;
      const delta = readingChromeHeightChange(current, next, heights);
      // A fold must leave the compensated offset clear of the at-top reveal band.
      if (delta < 0 && scrollTop + delta <= FLOAT_REVEAL_AT_TOP) return;
      if (notes === "hidden" && !notesReadingDirectionRef.current) notesReadingDirectionRef.current = direction;
      readingChromeRef.current = next;
      setReadingChrome(next);
    };
    body.addEventListener("scroll", onScroll, { capture: true, passive: true });
    return () => body.removeEventListener("scroll", onScroll, { capture: true });
  }, []);

  // Keep the same line under the reader's eye when the title or summary
  // leaves or re-enters the flow. Use the pre-layout offset because viewport
  // growth can clamp scrollTop near the bottom. Record the applied offset so
  // the resulting scroll event stays gesture-neutral.
  useLayoutEffect(() => {
    const prev = previousChromeRef.current;
    previousChromeRef.current = readingChrome;
    if (prev === readingChrome) return;
    const scroller = collapseScrollerRef.current;
    if (!scroller?.isConnected) return;
    const delta = readingChromeHeightChange(prev, readingChrome, chromeHeightsRef.current);
    // At the beginning the first line belongs below the returning controls.
    if (delta === 0 || lastScrollTopRef.current < FLOAT_REVEAL_AT_TOP) return;
    const adjusted = Math.max(0, lastScrollTopRef.current + delta);
    lastScrollTopRef.current = adjusted;
    scroller.scrollTop = adjusted;
    lastScrollTopRef.current = clampedScrollTop(scroller);
  }, [readingChrome]);

  const overlayReadingChrome = readingChrome.notes !== "resting" && chromeHeightsRef.current.notes > 0;
  useLayoutEffect(() => {
    const bar = topBarRef.current;
    const body = stackBodyRef.current;
    if (!overlayReadingChrome || topBarCollapsed || !bar || !body) return;
    const placeNotes = () => body.style.setProperty("--reading-title-height", `${bar.offsetHeight}px`);
    placeNotes();
    const resize = new ResizeObserver(placeNotes);
    resize.observe(bar);
    return () => {
      resize.disconnect();
      body.style.removeProperty("--reading-title-height");
    };
  }, [overlayReadingChrome, topBarCollapsed]);

  // Classifies each focus change and slides it (docs/web-ui/motion.md, recipe
  // 4). A pop is a focus change a Back tap caused. A push is any other change
  // the user made from inside the shown pane to a pane beyond the landing
  // screen; everything else replaces the pane without motion: the first pane,
  // a choice made in the Sessions drawer, tablet board, or Overview, browser
  // history (whose own back gesture already animates on some phones), an
  // attention entry being restored, the shown pane closing under the user, and
  // a deep link still resolving.
  useLayoutEffect(() => {
    const chromeOpen = panelOpen || activityOpen;
    const chromeWasOpen = chromeWasOpenRef.current;
    chromeWasOpenRef.current = chromeOpen;
    const outgoing = committedSlotRef.current;
    const incoming = paneSlotRef.current;
    committedSlotRef.current = incoming;
    const previousId = transitionFocusRef.current;
    if (previousId === focusedPaneId) {
      if (!boardVisible) boardInputRef.current = false;
      return;
    }
    const fromBoard = boardInputRef.current;
    boardInputRef.current = false;
    transitionFocusRef.current = focusedPaneId;
    endTransitionRef.current?.();
    const popped = popPendingRef.current;
    popPendingRef.current = false;
    const previousPane = panes.find((pane) => pane.id === previousId);
    let kind: StackTransitionKind | null = null;
    if (previousId === null || !focusedPane) kind = null;
    else if (popped) kind = "pop";
    else if (
      !chromeOpen &&
      !chromeWasOpen &&
      !fromBoard &&
      !lastPopstateWasTrusted &&
      !routeDeferred &&
      previousPane !== undefined &&
      previousPane.type !== "welcome" &&
      focusedPane.type !== "welcome" &&
      !attentionEntryOwnsPane(focusedPane)
    )
      kind = "push";
    const body = stackBodyRef.current;
    const offsets = scrollOffsetsRef.current;
    if (kind && body && outgoing && incoming && outgoing !== incoming && !outgoing.isConnected) {
      endTransitionRef.current = startStackTransition(
        body,
        kind,
        outgoing,
        incoming,
        offsets,
        TRANSITION_CLASSES,
        () => {
          endTransitionRef.current = null;
          // The leaving layer is gone, so whatever its restored scrolling
          // left in the map is discarded with it.
          pruneDetachedOffsets(offsets);
        },
      );
    }
    offsets.clear();
  }, [focusedPaneId, focusedPane, panes, panelOpen, activityOpen, routeDeferred, boardVisible]);

  useLayoutEffect(() => () => endTransitionRef.current?.(), []);

  // A press starts a new intent: a Back tap's pending pop does not outlive it,
  // and only input inside the board column marks the next focus change as a
  // board replacement.
  const clearPendingIntent = useCallback(() => {
    popPendingRef.current = false;
    boardInputRef.current = false;
  }, []);

  const clearBoardInput = useCallback(() => {
    boardInputRef.current = false;
  }, []);

  const markBoardInput = useCallback(() => {
    boardInputRef.current = true;
  }, []);

  function handleBack(): void {
    popPendingRef.current = true;
    if (focusedPane?.type === "doc") {
      const retained = documentPaneState(focusedPane);
      if (retained) {
        wentBackRef.current = true;
        if (retained.origin && panes.includes(retained.origin)) {
          workspaceStore.getState().focusPane(retained.origin.id);
        } else {
          const session = (focusedPane.params as { session?: unknown }).session;
          if (typeof session === "string" && session !== "") openSessionByRef(session);
          else workspaceStore.getState().openPane("welcome");
        }
        return;
      }
    }
    // Cascade dismissal takes precedence in the phone toolbar because Back is
    // the inspector's sole dismissal control on mobile; browser Back still
    // traverses attention history. Clear wentBackRef/popPendingRef when a
    // legacy in-place return keeps the same pane id, because the focus-change
    // effect will not run to clear them before the next forward navigation.
    if (focusedPane?.type === "sessionZoom") {
      const previousId = focusedPane.id;
      wentBackRef.current = true;
      returnFromAgentCascade(previousId);
      const after = workspaceStore.getState();
      if (after.focusedPaneId === previousId) {
        wentBackRef.current = false;
        popPendingRef.current = false;
        return;
      }
      if (after.focusedPaneId !== null) return;
      const fallback =
        popValidBackTarget(backStackRef.current, presentedPanes(after.panes), previousId) ?? after.mainPane()?.id;
      if (fallback) {
        workspaceStore.getState().focusPane(fallback);
      } else {
        workspaceStore.getState().openPane("welcome");
      }
      return;
    }
    // Attention Back belongs to the ordinary session reader the current entry
    // owns.
    if (focusedPane?.type === "session" && attentionEntryOwnsPane(focusedPane) && goBackInAttentionHistory()) return;
    let target = popValidBackTarget(backStackRef.current, panes, focusedPaneId);
    if (!target && focusedPane?.type === "transcript") {
      const params = focusedPane.params as { ref?: unknown; parentRef?: unknown };
      if (typeof params.parentRef === "string" && params.parentRef !== "" && params.parentRef !== params.ref) {
        // A host swap discards local history, not the retained parent pane.
        // Focus that exact pane: reopening a session could promote a nested
        // parent into main and discard the real owner and remaining context.
        const origin = transcriptOpenOrigin(focusedPane);
        let ancestor =
          (origin && panes.includes(origin) ? origin : undefined) ??
          panes.find(
            (pane) =>
              pane.id !== focusedPaneId && pane.type === "session" && refParam(pane.params) === params.parentRef,
          ) ??
          panes.find(
            (pane) =>
              pane.id !== focusedPaneId && pane.type === "transcript" && refParam(pane.params) === params.parentRef,
          );
        target = ancestor?.id;
        // Reject cycles, not incomplete context: an absent farther ancestor
        // must not prevent returning to the retained immediate parent.
        const visited = new Set([focusedPaneId]);
        while (ancestor?.type === "transcript") {
          if (visited.has(ancestor.id)) {
            target = undefined;
            break;
          }
          visited.add(ancestor.id);
          const parentRef = (ancestor.params as { parentRef?: unknown }).parentRef;
          ancestor =
            transcriptOpenOrigin(ancestor) ??
            (typeof parentRef === "string" && parentRef !== ""
              ? (panes.find(
                  (pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === parentRef,
                ) ??
                panes.find(
                  (pane) => pane.type === "transcript" && (pane.params as { ref?: unknown }).ref === parentRef,
                ))
              : undefined);
        }
      }
    }
    if (!target && focusedPane?.type === "session") {
      const parentRef = conversationOpenParentRef(focusedPane);
      if (parentRef !== undefined) {
        // A host swap discards local history, not the retained parent pane.
        // Focus that exact pane: reopening a session could promote a nested
        // parent into main and discard the real owner and remaining context.
        let ancestor =
          panes.find(
            (pane) =>
              pane.id !== focusedPaneId &&
              pane.type === "session" &&
              (pane.params as { ref?: unknown }).ref === parentRef,
          ) ??
          panes.find(
            (pane) =>
              pane.id !== focusedPaneId &&
              pane.type === "transcript" &&
              (pane.params as { ref?: unknown }).ref === parentRef,
          );
        target = ancestor?.id;
        // Reject cycles, not incomplete context: an absent farther ancestor
        // must not prevent returning to the retained immediate parent. A
        // conversation records each open as a parent edge, so opening `a`
        // from `b` and then `b` from `a` makes both edges reachable; without
        // this a repeated Back ping-pongs instead of reaching Welcome.
        const visited = new Set([focusedPaneId]);
        while (ancestor?.type === "session" || ancestor?.type === "transcript") {
          if (visited.has(ancestor.id)) {
            target = undefined;
            break;
          }
          visited.add(ancestor.id);
          const nextRef =
            ancestor.type === "session"
              ? conversationOpenParentRef(ancestor)
              : (ancestor.params as { parentRef?: unknown }).parentRef;
          ancestor =
            typeof nextRef === "string" && nextRef !== ""
              ? (panes.find((pane) => pane.type === "session" && (pane.params as { ref?: unknown }).ref === nextRef) ??
                panes.find((pane) => pane.type === "transcript" && (pane.params as { ref?: unknown }).ref === nextRef))
              : undefined;
        }
      }
    }
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
  //
  // Session panel panes get a DIFFERENT Back, not none: this top bar is the
  // only visible chrome for them on mobile (the scaffold header is hidden,
  // panescaffold.module.css's max-width block). Their Back targets the parent
  // session directly - the generic stack stays suppressed for them (it resets
  // across the host swap that typically lands a panel pane here, and would
  // walk somewhere unrelated).
  const panelPaneTypes = new Set(["sessionTasks", "sessionDetails"]);
  const isPanelPane = focusedPane !== null && panelPaneTypes.has(focusedPane.type);
  const focusedSessionRef = focusedPane?.type === "session" ? (refParam(focusedPane.params) ?? undefined) : undefined;
  // The focused session's notes card attaches to the solid bar's seam
  // (topnotespanel.module.css), so expanding it restores a floating bar: the
  // reveal is chrome, not a scroll gesture, and the capsules would otherwise
  // sit over the card's header row. The empty string matches no session ref.
  const focusedNotesExpanded = useTopNotesExpanded(readerSessionRef(focusedPane) ?? "");
  const headerPinned = focusedNotesExpanded || readerControlsPinned;
  headerPinnedRef.current = headerPinned;
  useLayoutEffect(() => {
    if (!headerPinned) return;
    notesReadingDirectionRef.current = 0;
    scrollRunRef.current = 0;
    readingChromeRef.current = FULL_CHROME;
    setReadingChrome(FULL_CHROME);
    // Unlike a focus change, the reader stays in this pane, so the collapse
    // scroller stays armed: the compensation effect holds the reading position
    // when the pane's layout regains the bar's height.
  }, [headerPinned]);
  const livePaneIds = useMemo(() => new Set(panes.map((pane) => pane.id)), [panes]);
  const retainedParentRef = focusedPane?.type === "session" ? conversationOpenParentRef(focusedPane) : undefined;
  const retainedParentAvailable =
    retainedParentRef !== undefined &&
    panes.some(
      (pane) => (pane.type === "session" || pane.type === "transcript") && refParam(pane.params) === retainedParentRef,
    );
  const sessionBackAvailable =
    focusedPane?.type === "session" &&
    (paneBack !== null ||
      retainedParentAvailable ||
      attentionEntryOwnsPane(focusedPane) ||
      backStackRef.current.some((id) => id !== focusedPane.id && livePaneIds.has(id)));
  const showBack =
    focusedPane !== null && focusedPane.type !== "welcome" && (focusedSessionRef === undefined || sessionBackAvailable);
  const drawer = !singlePane && !tabletSplit ? <TreeDrawer onToggle={() => setPanelOpen((v) => !v)} /> : null;
  const boardToggle = tabletSplit ? (
    <IconButton
      ref={boardToggleRef}
      data-testid="board-toggle"
      label={needsYou > 0 ? `Board (${needsYou} need attention)` : "Board"}
      aria-expanded={boardVisible}
      variant="quiet"
      icon={<SidebarIcon />}
      badge={needsYou > 0 ? { count: needsYou, tone: "attention" } : undefined}
      onClick={toggleBoard}
    />
  ) : null;

  const leading = (
    <div className={styles.leading}>
      {!boardVisible && boardToggle}
      {showBack ? (
        <IconButton
          label="Back"
          icon={<Chevron direction="left" size={24} />}
          variant="quiet"
          onClick={isPanelPane ? handlePanelBack : (paneBack ?? handleBack)}
        />
      ) : (
        focusedSessionRef !== undefined && drawer
      )}
    </div>
  );
  const trailing = (
    <div className={styles.trailing}>
      {focusedSessionRef !== undefined && (
        <SessionInspectionActions sessionRef={focusedSessionRef} paneId={focusedPane?.id} />
      )}
      {focusedSessionRef === undefined && drawer}
    </div>
  );

  function handlePanelBack(): void {
    if (!focusedPane) return;
    const { ref } = focusedPane.params as { ref: string };
    // Same "don't record this as a forward step" rule as handleBack: leaving
    // a panel pane for its parent must not make the panel poppable right back.
    wentBackRef.current = true;
    popPendingRef.current = true;
    workspaceStore.getState().openPane("session", { ref });
  }

  return (
    <div
      className={styles.host}
      onPointerDownCapture={clearPendingIntent}
      onKeyDownCapture={clearPendingIntent}
      onClickCapture={clearBoardInput}
    >
      <div
        className={styles.split}
        data-board={tabletSplit ? (boardVisible ? "visible" : "collapsed") : undefined}
        data-testid={tabletSplit ? "board-split" : undefined}
      >
        {tabletSplit && (
          <BoardPresentContext.Provider value={boardVisible}>
            <AnimatePresence initial={false}>
              {boardVisible && (
                <TabletBoardColumn
                  key="tablet-board"
                  boardRef={boardRef}
                  boardWidth={boardWidth}
                  boardMaxWidth={boardMaxWidth}
                  boardToggle={boardToggle}
                  railSlot={railSlot}
                  transition={boardTransition}
                  exitTransition={boardExitTransition}
                  onBoardInput={markBoardInput}
                />
              )}
            </AnimatePresence>
          </BoardPresentContext.Provider>
        )}
        {/* Keep the same pane slot and scroll listener on both sides of the tablet floor. */}
        <div
          className={styles.paneColumn}
          data-notes-hidden={readingChrome.notes === "hidden" || undefined}
          data-reading-overlay={overlayReadingChrome || undefined}
          data-iphone-header={nativeHeader ? (topBarCollapsed ? "floating" : "resting") : undefined}
          data-testid={tabletSplit ? "pane-column" : undefined}
        >
          <div
            ref={topBarRef}
            className={topBarCollapsed ? `${styles.topBar} ${styles.topBarCollapsed}` : styles.topBar}
            data-home-edge={homeEdge ?? undefined}
            data-system-edge-actions={systemEdge ? "" : undefined}
            data-testid="topbar"
            data-collapsed={topBarCollapsed ? "true" : undefined}
            data-session={focusedSessionRef === undefined ? undefined : "true"}
          >
            {!actionsAtEdge && leading}
            <Title className={styles.stackTitle} data-testid="topbar-title">
              {paneTitle}
            </Title>
            {!actionsAtEdge && trailing}
          </div>
          {publishHeader && focusedSessionRef && !panelOpen && !activityOpen && (
            <IPhoneSessionHeader
              bar={topBarRef}
              edgeToolbar={edgeToolbar}
              split={tabletSplit}
              id={focusedPane.id}
              sessionRef={focusedSessionRef}
              title={paneTitle ?? ""}
              leading={showBack ? "back" : boardVisible ? null : "sessions"}
              needsYou={needsYou}
              collapsed={topBarCollapsed}
              onAction={(action) => {
                switch (action) {
                  case "back":
                    (paneBack ?? handleBack)();
                    break;
                  case "sessions":
                    if (tabletSplit) toggleBoard();
                    else setPanelOpen(true);
                    break;
                  case "find":
                    requestFindInSession(focusedSessionRef, { target: focusedPane.id });
                    break;
                  case "overview":
                    openSessionOverview(focusedSessionRef, focusedPane.id, true);
                    break;
                }
              }}
            />
          )}
          <div ref={stackBodyRef} className={styles.stackBody} data-stacked-pane-type={focusedPane?.type}>
            {focusedPane && (
              // Only a distinct pane id detaches the outgoing pane's last frame.
              <div key={focusedPane.id} ref={paneSlotRef} className={styles.paneSlot} data-stack-pane={focusedPane.id}>
                <ReaderHeaderPinContext.Provider value={setReaderControlsPinned}>
                  <StackedPane pane={focusedPane} />
                </ReaderHeaderPinContext.Provider>
              </div>
            )}
          </div>
        </div>
      </div>
      {homeEdge && (
        <div
          role="toolbar"
          aria-label={welcomeLauncher ? "Workspace actions" : "Conversation actions"}
          hidden={activityOpen || panelOpen}
          data-home-toolbar={homeEdge}
          className={styles.homeToolbar}
        >
          {leading}
          {trailing}
        </div>
      )}
      <div className={styles.activityOverlay}>
        <ActivitySidebar mobile />
      </div>
      {!tabletSplit && (
        <MobilePanel
          rail={railSlot}
          open={panelOpen}
          onClose={closePanel}
          toolbarEdge={sessionsEdge ? "system" : hosted || singlePane ? null : homeScreenEdge}
          onOpen={welcomeLauncher ? () => setPanelOpen(true) : undefined}
        />
      )}
    </div>
  );
}
