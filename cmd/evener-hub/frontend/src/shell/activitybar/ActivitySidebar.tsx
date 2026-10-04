// The activity sidebar: the triage surface of the zoom system. A right shell
// region (never a dockview panel, so it survives pane switches and stays out
// of the tab system) that opens from a status-bar chip or the session chrome,
// preselected to that kind's tab, and always describes the scope you're
// reading. Entrance/exit ride the spatial budget through the motion wrapper;
// reduced motion collapses them to instant layout. The kind tabs, their
// glyphs, and their counts come from the one ACTIVITY_TABS table - the same
// table the status bar's chips read, so the two surfaces cannot drift.

import { useCallback, useEffect, useMemo, useRef } from "react";
import { AnimatePresence, m, spatialTransition } from "../../motion";
import { popAgentCascade } from "../../panes/zoom/actions";
import { deriveCascadePath, parseZoomParams } from "../../panes/zoom/intent";
import { navigationStore, useNavigationStore } from "../../stores/navigation/store";
import { useSessionActivity } from "../../stores/sessionActivity";
import { IconButton, SegmentedControl } from "../../widgets";
import { DisclosurePersistenceContext } from "../../widgets/disclosure/disclosureStore";
import { FocusScope } from "../../widgets/focusscope";
import { tabbable } from "../../widgets/focusscope/tabbable";
import { requireClass } from "../../widgets/internal/requireClass";
import { useFocusedActivityScopeRef } from "../focusedSession";
import { ScopeCrumbs } from "../statusbar/ScopeCrumbs";
import { type ActivityTab, deriveScope } from "../statusbar/statusScope";
import { useWorkspaceStore } from "../workspace";
import { ActivityViewport } from "./ActivityViewport";
import styles from "./activitybar.module.css";
import {
  activitySidebarReturnFocusTarget,
  activitySidebarStore,
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
  body: requireClass(styles.body, "activitybar.module.css", "body"),
  focusBody: requireClass(styles.focusBody, "activitybar.module.css", "focusBody"),
};

export function ActivitySidebar({ mobile = false }: { mobile?: boolean }) {
  const open = useActivitySidebarStore((state) => state.open);
  const tab = useActivitySidebarStore((state) => state.tab);
  const sidebar = useRef<HTMLElement>(null);
  const returnFrame = useRef<number | null>(null);
  useEffect(
    () => () => {
      if (returnFrame.current !== null) cancelAnimationFrame(returnFrame.current);
    },
    [],
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
  const ref = useFocusedActivityScopeRef();
  const focusedPane = useWorkspaceStore((state) => state.panes.find((pane) => pane.id === state.focusedPaneId));
  useEffect(() => {
    if (open && ref !== null) activitySidebarStore.getState().retainOpenView(ref);
  }, [open, ref]);
  useEffect(() => {
    if (!open || !mobile || ref === null) return;
    const frame = requestAnimationFrame(() => {
      const element = sidebar.current;
      if (!element || !activitySidebarStore.getState().open) return;
      const selected = element.querySelector<HTMLElement>('[role="radio"][aria-checked="true"]');
      (selected ?? tabbable(element)[0])?.focus();
    });
    return () => cancelAnimationFrame(frame);
  }, [open, mobile, ref]);
  const { snapshot } = useSessionActivity(open ? ref : null);
  // Closed derives nothing: the sidebar is mounted for the whole desktop
  // session, and a location lookup plus recursive walk per polling update
  // duplicates the StatusBar's own derivation for a surface nothing shows.
  // biome-ignore lint/correctness/useExhaustiveDependencies: `resources` is the memo's invalidation key, not a value the memo reads (the store is read imperatively inside)
  const scope = useMemo(() => {
    if (!open || ref === null) return null;
    const derived = deriveScope(navigationStore.getState(), ref, snapshot);
    if (focusedPane?.type !== "sessionZoom" || !derived.ancestryKnown) return derived;
    const params = parseZoomParams(focusedPane.params);
    if (!params) return derived;
    const { scopes } = deriveCascadePath(params, snapshot?.context ?? null);
    return {
      ...derived,
      path: derived.path.map((crumb, index) => ({ ...crumb, ref: scopes[index]?.requestedRef ?? crumb.ref })),
    };
  }, [open, resources, ref, snapshot, focusedPane]);
  const Body = scope === null ? null : activityTabSpec(tab).Body;
  // Resolved once per mount: spatialTransition reads getComputedStyle (a
  // style pass), and the token changes with the theme at most, so paying
  // that read on every render taxes each scope change for nothing.
  const transition = useMemo(() => spatialTransition(), []);

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
          initial={{ x: 320 }}
          animate={{ x: 0 }}
          exit={{ x: 320 }}
          transition={transition}
          data-testid="activity-sidebar"
          aria-label="Overview"
        >
          <FocusScope trap={mobile} autoFocus={false}>
            <div className={CLASS.focusBody}>
              <div className={CLASS.head}>
                <div className={CLASS.scope}>
                  <ScopeCrumbs
                    path={scope.path}
                    hierarchy
                    onNavigate={
                      focusedPane?.type === "sessionZoom"
                        ? (ancestor) => popAgentCascade(focusedPane.id, ancestor)
                        : undefined
                    }
                  />
                  {!scope.ancestryKnown ? <span className={CLASS.pending}>Finding session context…</span> : null}
                </div>
                <IconButton label="Close Overview" icon="×" variant="quiet" size="sm" onClick={close} />
              </div>
              <div className={CLASS.tabs}>
                <SegmentedControl<ActivityTab>
                  label="Overview kind"
                  hideLabel
                  size="sm"
                  fullWidth
                  value={tab}
                  onChange={(next) => activitySidebarStore.getState().setTab(next)}
                  options={ACTIVITY_TABS.map((spec) => ({
                    value: spec.id,
                    label: spec.tabLabel(scope.counts),
                    accessibleLabel: spec.chipLabel(scope.counts),
                  }))}
                />
              </div>
              <DisclosurePersistenceContext.Provider value={JSON.stringify([scope.leaf.ref, tab])}>
                <ActivityViewport
                  key={JSON.stringify([scope.leaf.ref, tab])}
                  sessionRef={scope.leaf.ref}
                  tab={tab}
                  className={CLASS.body}
                >
                  {Body === null ? null : <Body scope={scope} />}
                </ActivityViewport>
              </DisclosurePersistenceContext.Provider>
            </div>
          </FocusScope>
        </m.aside>
      ) : null}
    </AnimatePresence>
  );
}
