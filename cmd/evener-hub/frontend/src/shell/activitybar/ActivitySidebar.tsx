// The activity sidebar: the triage surface of the zoom system. A right shell
// region (never a dockview panel, so it survives pane switches and stays out
// of the tab system) that opens from a status-bar chip or the session chrome,
// preselected to that kind's tab, and always describes the scope you're
// reading. Entrance/exit ride the spatial budget through the motion wrapper;
// reduced motion collapses them to instant layout. The kind tabs, their
// glyphs, and their counts come from the one ACTIVITY_TABS table - the same
// table the status bar's chips read, so the two surfaces cannot drift.

import { useEffect, useMemo } from "react";
import { AnimatePresence, m, spatialTransition } from "../../motion";
import { navigationStore, useNavigationStore } from "../../stores/navigation/store";
import { IconButton, SegmentedControl } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { useFocusedActivityScopeRef } from "../focusedSession";
import { ScopeCrumbs } from "../statusbar/ScopeCrumbs";
import { type ActivityTab, deriveScope } from "../statusbar/statusScope";
import styles from "./activitybar.module.css";
import { activitySidebarStore, useActivitySidebarStore } from "./activitySidebarStore";
import { ACTIVITY_TABS, activityTabSpec } from "./activityTabs";

const CLASS = {
  sidebar: requireClass(styles.sidebar, "activitybar.module.css", "sidebar"),
  head: requireClass(styles.head, "activitybar.module.css", "head"),
  tabs: requireClass(styles.tabs, "activitybar.module.css", "tabs"),
  body: requireClass(styles.body, "activitybar.module.css", "body"),
};

export function ActivitySidebar() {
  const open = useActivitySidebarStore((state) => state.open);
  const tab = useActivitySidebarStore((state) => state.tab);
  // Narrow subscriptions: re-render on the resources map (nav data) or the
  // scope's ref, not on every store touch.
  const resources = useNavigationStore((state) => state.resources);
  const ref = useFocusedActivityScopeRef();
  // biome-ignore lint/correctness/useExhaustiveDependencies: `resources` is the memo's invalidation key, not a value the memo reads (the store is read imperatively inside)
  const scope = useMemo(() => (ref === null ? null : deriveScope(navigationStore.getState(), ref)), [resources, ref]);
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
      if (event.key === "Escape" && !event.defaultPrevented) activitySidebarStore.getState().close();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);
  return (
    <AnimatePresence initial={false}>
      {open && scope !== null ? (
        <m.aside
          className={CLASS.sidebar}
          initial={{ x: 320 }}
          animate={{ x: 0 }}
          exit={{ x: 320 }}
          transition={transition}
          data-testid="activity-sidebar"
        >
          <div className={CLASS.head}>
            <ScopeCrumbs path={scope.path} />
            <IconButton
              label="Close the activity sidebar"
              icon="×"
              variant="quiet"
              size="sm"
              onClick={() => activitySidebarStore.getState().close()}
            />
          </div>
          <div className={CLASS.tabs}>
            <SegmentedControl<ActivityTab>
              label="Activity kind"
              size="sm"
              fullWidth
              value={tab}
              onChange={(next) => activitySidebarStore.getState().setTab(next)}
              options={ACTIVITY_TABS.map((spec) => ({ value: spec.id, label: spec.tabLabel(scope.counts) }))}
            />
          </div>
          {/* key on the leaf: the tab's fold/paging state belongs to the
              scope, and a re-scope must not inherit the previous leaf's
              open folds and page offsets. */}
          <div className={CLASS.body}>{Body === null ? null : <Body key={scope.leaf.ref} scope={scope} />}</div>
        </m.aside>
      ) : null}
    </AnimatePresence>
  );
}
