// The activity sidebar: the triage surface of the zoom system. A right shell
// region (never a dockview panel, so it survives pane switches and stays out
// of the tab system) that opens from a status-bar chip or the session chrome,
// preselected to that kind's tab, and always describes the scope you're
// reading. Entrance/exit ride the spatial budget through the motion wrapper;
// reduced motion collapses them to instant layout. The kind tabs, their
// glyphs, and their counts come from the one ACTIVITY_TABS table - the same
// table the status bar's chips read, so the two surfaces cannot drift.

import { useMemo } from "react";
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
  return (
    <AnimatePresence initial={false}>
      {open && scope !== null ? (
        <m.aside
          className={CLASS.sidebar}
          initial={{ x: 320 }}
          animate={{ x: 0 }}
          exit={{ x: 320 }}
          transition={spatialTransition()}
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
          <div className={CLASS.body}>{Body === null ? null : <Body scope={scope} />}</div>
        </m.aside>
      ) : null}
    </AnimatePresence>
  );
}
