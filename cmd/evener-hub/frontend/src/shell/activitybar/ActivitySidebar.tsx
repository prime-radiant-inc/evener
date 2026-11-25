// The activity sidebar: the triage surface of the zoom system. A right shell
// region (never a dockview panel, so it survives pane switches and stays out
// of the tab system) that opens from a status-bar chip or the session chrome,
// preselected to that kind's tab, and always describes the scope you're
// reading. Entrance/exit ride the spatial budget through the motion wrapper;
// reduced motion collapses them to instant layout. The kind tabs, their
// glyphs, and their counts come from the one ACTIVITY_TABS table - the same
// table the status bar's chips read, so the two surfaces cannot drift.

import { type KeyboardEvent as ReactKeyboardEvent, useCallback, useEffect, useId, useMemo, useRef } from "react";
import { AnimatePresence, m, spatialExitTransition, spatialTransition } from "../../motion";
import { ActivityBodyClock } from "../../panes/session/chrome/activityTime";
import { popAgentCascade } from "../../panes/zoom/actions";
import { cascadeOrigin, cascadeOriginRef } from "../../panes/zoom/inspectionOrigin";
import { deriveCascadePath, parseZoomParams } from "../../panes/zoom/intent";
import { navigationStore, useNavigationStore } from "../../stores/navigation/store";
import { useSessionActivity, useSubagentCounts } from "../../stores/sessionActivity";
import { EntityViewsProvider, sessionEntityViews } from "../../transcriptDisplay/entityViews";
import {
  Button,
  CloseIcon,
  FocusScope,
  IconButton,
  SegmentedControl,
  type SegmentedControlOption,
} from "../../widgets";
import { DisclosurePersistenceContext } from "../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../widgets/internal/requireClass";
import { useRovingRadio } from "../../widgets/internal/rovingRadio";
import { ScopeCrumbs } from "../statusbar/ScopeCrumbs";
import { type ActivityTab, deriveScope } from "../statusbar/statusScope";
import { isMobileViewport } from "../useIsMobile";
import { useWorkspaceStore, workspaceStore } from "../workspace";
import { ActivityViewport } from "./ActivityViewport";
import styles from "./activitybar.module.css";
import {
  activitySidebarReturnFocusTarget,
  activitySidebarStore,
  useActivitySidebarScopeRef,
  useActivitySidebarStore,
} from "./activitySidebarStore";
import { ACTIVITY_TABS, activityTabSpec } from "./activityTabs";

const CLASS = {
  sidebar: requireClass(styles.sidebar, "activitybar.module.css", "sidebar"),
  sidebarMobile: requireClass(styles.sidebarMobile, "activitybar.module.css", "sidebarMobile"),
  scope: requireClass(styles.scope, "activitybar.module.css", "scope"),
  pending: requireClass(styles.pending, "activitybar.module.css", "pending"),
  head: requireClass(styles.head, "activitybar.module.css", "head"),
  tabs: requireClass(styles.tabs, "activitybar.module.css", "tabs"),
  tablist: requireClass(styles.tablist, "activitybar.module.css", "tablist"),
  tab: requireClass(styles.tab, "activitybar.module.css", "tab"),
  body: requireClass(styles.body, "activitybar.module.css", "body"),
  focusBody: requireClass(styles.focusBody, "activitybar.module.css", "focusBody"),
};

function OverviewStrip({
  value,
  options,
}: {
  value: ActivityTab;
  options: readonly SegmentedControlOption<ActivityTab>[];
}) {
  return (
    <SegmentedControl<ActivityTab>
      label="Overview kind"
      hideLabel
      size="sm"
      fullWidth
      wrap
      value={value}
      onChange={(next) => activitySidebarStore.getState().setTab(next)}
      options={options}
    />
  );
}

// The phone and tablet Overview category strip: underline tabs in a horizontally
// scrolling row. Quiet Button is the tab itself; the wrapper span draws only
// the selected underline so one tab holds one interactive control.
function MobileOverviewTabs({
  value,
  options,
  panelId,
  tabDomId,
}: {
  value: ActivityTab;
  options: readonly SegmentedControlOption<ActivityTab>[];
  panelId: string;
  tabDomId(id: ActivityTab): string;
}) {
  // AnimatePresence renders the exiting aside from the element captured while
  // `open` was still true, so subscribing here re-arms entry focus when the
  // store reopens during that exit.
  const open = useActivitySidebarStore((state) => state.open);
  const tabRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const selectedIndex = options.findIndex((option) => option.value === value);
  function reveal(tab: HTMLButtonElement | null) {
    tab?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  }
  function choose(index: number) {
    const option = options[index];
    if (!option) return;
    activitySidebarStore.getState().setTab(option.value);
    const tab = tabRefs.current[index];
    tab?.focus();
    reveal(tab ?? null);
  }
  const move = useRovingRadio({ items: options, mode: "tablist", onMove: choose });
  // biome-ignore lint/correctness/useExhaustiveDependencies: selectedIndex is read once when entry focus is armed.
  useEffect(() => {
    if (!open) return;
    const frame = requestAnimationFrame(() => {
      const tab = tabRefs.current[selectedIndex];
      tab?.focus();
      reveal(tab ?? null);
    });
    return () => cancelAnimationFrame(frame);
  }, [open]);
  function handleKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    // Modifier chords belong to the global dispatcher; claim only bare keys.
    if (event.altKey || event.ctrlKey || event.metaKey) return;
    move(event, selectedIndex >= 0 ? selectedIndex : 0);
  }
  return (
    <div
      className={CLASS.tablist}
      role="tablist"
      aria-label="Overview kind"
      aria-orientation="horizontal"
      onKeyDown={handleKeyDown}
    >
      {options.map((option, index) => {
        const selected = option.value === value;
        return (
          <span key={option.value} className={CLASS.tab} data-selected={selected ? "" : undefined}>
            <Button
              ref={(node) => {
                tabRefs.current[index] = node;
              }}
              variant="quiet"
              size="sm"
              role="tab"
              id={tabDomId(option.value)}
              aria-selected={selected}
              aria-controls={panelId}
              aria-label={option.accessibleLabel ?? option.label}
              tabIndex={selected ? 0 : -1}
              onClick={() => choose(index)}
            >
              <span>{option.label}</span>
              {option.detail !== undefined && option.detail.trim() !== "" ? <span>{option.detail}</span> : null}
            </Button>
          </span>
        );
      })}
    </div>
  );
}

export function ActivitySidebar({ mobile = false }: { mobile?: boolean }) {
  const open = useActivitySidebarStore((state) => state.open);
  const tab = useActivitySidebarStore((state) => state.tab);
  const idPrefix = useId();
  const tabPanelId = `${idPrefix}-panel`;
  const tabDomId = (id: ActivityTab) => `${idPrefix}-tab-${id}`;
  const sidebar = useRef<HTMLElement>(null);
  const returnFrame = useRef<number | null>(null);
  useEffect(
    () => () => {
      if (returnFrame.current !== null) cancelAnimationFrame(returnFrame.current);
      // The dock is desktop-only: a teardown that lands in the phone host ends
      // the inspection rather than reopening it as the phone's blocking Overview.
      if (!mobile && isMobileViewport()) activitySidebarStore.getState().close();
    },
    [mobile],
  );
  const close = useCallback(() => {
    const focusInside = sidebar.current?.contains(document.activeElement);
    activitySidebarStore.getState().close();
    if (!focusInside) return;
    if (returnFrame.current !== null) cancelAnimationFrame(returnFrame.current);
    // The phone shell must reveal its underlying pane before selecting a
    // visible return control. A newer opening keeps ownership of focus.
    const restore = () => {
      returnFrame.current = null;
      if (!activitySidebarStore.getState().open)
        activitySidebarReturnFocusTarget(activitySidebarStore.getState().tab)?.focus();
    };
    if (mobile) returnFrame.current = requestAnimationFrame(restore);
    else restore();
  }, [mobile]);
  // Narrow subscriptions: re-render on the resources map (nav data) or the
  // scope's ref, not on every store touch.
  const resources = useNavigationStore((state) => state.resources);
  const ref = useActivitySidebarScopeRef();
  const focusedPane = useWorkspaceStore((state) => state.panes.find((pane) => pane.id === state.focusedPaneId));
  useEffect(() => {
    if (open && ref !== null) activitySidebarStore.getState().retainOpenView(ref);
  }, [open, ref]);
  const { snapshot } = useSessionActivity(open ? ref : null);
  // The watch detail resolves entity names from the evidence the session
  // transcript beside this surface already loaded. Only the Watches tab
  // mounts that detail; without a provider here the live route fell back to
  // raw ids.
  const entities = useMemo(
    () => (open && tab === "watches" && ref !== null ? sessionEntityViews({ sessionRef: ref, snapshot }) : undefined),
    [open, tab, ref, snapshot],
  );
  const subagents = useSubagentCounts(open ? ref : null);
  // Closed derives nothing: the sidebar is mounted for the whole desktop
  // session, and a location lookup plus recursive walk per polling update
  // duplicates the StatusBar's own derivation for a surface nothing shows.
  // biome-ignore lint/correctness/useExhaustiveDependencies: `resources` is the memo's invalidation key, not a value the memo reads (the store is read imperatively inside)
  const scope = useMemo(() => {
    if (!open || ref === null) return null;
    const derived = deriveScope(navigationStore.getState(), ref, snapshot, subagents);
    if (focusedPane?.type !== "sessionZoom" || !derived.ancestryKnown) return derived;
    const params = parseZoomParams(focusedPane.params);
    if (!params) return derived;
    // The crumb rebase covers any held column of the focused cascade, not only
    // its leaf: an embedded conversation's Overview holds a parent column, so
    // that scope keeps its own ancestry while each crumb is rebound to the
    // cascade's requested ref for the column it names. The leaf's proven
    // activity context describes the full path; a held parent is only covered
    // by the cascade's recorded edge suffix, which begins at the cascade's own
    // source, so the suffix is aligned to the held scope's own last crumb
    // (deriveScope appends it) before rebasing: everything above the chain's
    // start keeps its authoritative ref.
    const leaf = params.ref === ref;
    const scopes = deriveCascadePath(params, leaf ? (snapshot?.context ?? null) : null).scopes;
    const held = scopes.findIndex((scope) => scope.requestedRef === ref);
    if (held < 0) return derived;
    const offset = derived.path.length - 1 - held;
    if (offset < 0) return derived;
    const originRef = cascadeOriginRef(focusedPane);
    const path = derived.path.map((crumb, index) =>
      index < offset ? crumb : { ...crumb, ref: scopes[index - offset]?.requestedRef ?? crumb.ref },
    );
    // The origin conversation never renders in the cascade, so the leaf's
    // breadcrumb cannot offer it: the rendered scopes are the clickable path.
    // A held parent column keeps its whole ancestry, whose origin crumb routes
    // to the retained origin pane instead.
    return { ...derived, path: leaf ? path.filter((crumb) => crumb.ref !== originRef) : path };
  }, [open, resources, ref, snapshot, subagents, focusedPane]);
  const Body = scope === null ? null : activityTabSpec(tab).Body;
  const tabOptions =
    scope === null
      ? []
      : ACTIVITY_TABS.map((spec) => ({
          value: spec.id,
          label: spec.label,
          detail: spec.tabDetail?.(scope.counts),
          accessibleLabel: spec.chipLabel(scope.counts),
        }));
  // Resolved once per mount: spatialTransition reads getComputedStyle (a
  // style pass), and the token changes with the theme at most, so paying
  // that read on every render taxes each scope change for nothing.
  const transition = useMemo(() => spatialTransition(), []);
  const exitTransition = useMemo(() => spatialExitTransition(), []);

  // Esc closes the sidebar while it's open - the dismiss gesture every
  // transient surface in this app honors. defaultPrevented means something
  // already claimed it, so the sidebar stands. Attached to WINDOW, not
  // document: the app's keybinding dispatcher attaches to window at boot
  // (installKeybindings), before any sidebar can open, and window listeners
  // run in attach order - so a scope-bound Esc (Settings' close chord,
  // SelectionQuote) is claimed and preventDefaulted before this listener
  // ever sees it, and one Esc never dismisses two surfaces.
  useEffect(() => {
    if (!open) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !event.defaultPrevented) close();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, close]);
  return (
    <AnimatePresence initial={false}>
      {open && scope !== null ? (
        <m.aside
          ref={sidebar}
          className={mobile ? CLASS.sidebarMobile : CLASS.sidebar}
          // The Slide recipe: in from the right edge it lives on and back out
          // to it, by the sidebar's own width.
          initial={{ x: "100%" }}
          animate={{ x: 0 }}
          exit={{ x: "100%", transition: exitTransition }}
          transition={transition}
          data-testid="activity-sidebar"
          aria-label="Overview"
          // The phone shell overlays the one stacked pane with focus trapped:
          // a modal dialog there, the non-modal Overview region on desktop.
          role={mobile ? "dialog" : undefined}
          aria-modal={mobile ? true : undefined}
        >
          <FocusScope className={CLASS.focusBody} trap={mobile} autoFocus={false}>
            <div className={CLASS.head}>
              <div className={CLASS.scope}>
                <ScopeCrumbs
                  path={scope.path}
                  hierarchy
                  onNavigate={
                    focusedPane?.type === "sessionZoom"
                      ? (ancestor) => {
                          // The origin conversation never renders inside a
                          // separated inspector's cascade, and selecting it
                          // there is refused: its crumb routes to the
                          // retained origin pane instead of doing nothing.
                          const origin = cascadeOriginRef(focusedPane) === ancestor ? cascadeOrigin(focusedPane) : null;
                          if (origin) {
                            workspaceStore.getState().focusPane(origin.id);
                            return;
                          }
                          popAgentCascade(focusedPane.id, ancestor);
                        }
                      : undefined
                  }
                />
                {!scope.ancestryKnown ? <span className={CLASS.pending}>Finding session context…</span> : null}
              </div>
              <IconButton label="Close Overview" icon={<CloseIcon />} variant="quiet" size="sm" onClick={close} />
            </div>
            {mobile ? (
              <MobileOverviewTabs
                key={scope.leaf.ref}
                value={tab}
                options={tabOptions}
                panelId={tabPanelId}
                tabDomId={tabDomId}
              />
            ) : (
              <div className={CLASS.tabs}>
                <OverviewStrip value={tab} options={tabOptions} />
              </div>
            )}
            <DisclosurePersistenceContext.Provider value={JSON.stringify([scope.leaf.ref, tab])}>
              <ActivityBodyClock>
                {(now) => (
                  <EntityViewsProvider entities={entities} ownerRef={ref ?? undefined}>
                    <ActivityViewport
                      key={JSON.stringify([scope.leaf.ref, tab])}
                      sessionRef={scope.leaf.ref}
                      tab={tab}
                      className={CLASS.body}
                      panel={mobile ? { id: tabPanelId, labelledBy: tabDomId(tab) } : undefined}
                    >
                      {Body === null ? null : <Body scope={scope} now={now} />}
                    </ActivityViewport>
                  </EntityViewsProvider>
                )}
              </ActivityBodyClock>
            </DisclosurePersistenceContext.Provider>
          </FocusScope>
        </m.aside>
      ) : null}
    </AnimatePresence>
  );
}
