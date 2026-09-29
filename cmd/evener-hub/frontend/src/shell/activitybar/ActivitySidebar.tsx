// The activity sidebar: the triage surface of the zoom system. A right shell
// region (never a dockview panel, so it survives pane switches and stays out
// of the tab system) that opens from a status-bar chip or the session chrome,
// preselected to that kind's tab, and always describes the scope you're
// reading. Entrance/exit ride the spatial budget through the motion wrapper;
// reduced motion collapses them to instant layout.

import { useEffect, useState } from "react";
import { AnimatePresence, m, spatialTransition } from "../../motion";
import { useNavigationStore } from "../../stores/navigation/store";
import { IconButton, SegmentedControl } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { focusedActivityScopeRef } from "../focusedSession";
import { openSessionByRef } from "../sessionPlacement";
import { type ActivityScope, type ActivityTab, deriveScope } from "../statusbar/statusScope";
import { useWorkspaceStore } from "../workspace";
import { AgentsTab } from "./AgentsTab";
import styles from "./activitybar.module.css";
import { activitySidebarStore, useActivitySidebarStore } from "./activitySidebarStore";
import { JobsTab } from "./JobsTab";
import { TasksTab } from "./TasksTab";
import { WatchesTab } from "./WatchesTab";

const CLASS = {
  sidebar: requireClass(styles.sidebar, "activitybar.module.css", "sidebar"),
  head: requireClass(styles.head, "activitybar.module.css", "head"),
  crumbs: requireClass(styles.crumbs, "activitybar.module.css", "crumbs"),
  crumbWrap: requireClass(styles.crumbWrap, "activitybar.module.css", "crumbWrap"),
  crumbSep: requireClass(styles.crumbSep, "activitybar.module.css", "crumbSep"),
  crumbCurrent: requireClass(styles.crumbCurrent, "activitybar.module.css", "crumbCurrent"),
  crumbBtn: requireClass(styles.crumbBtn, "activitybar.module.css", "crumbBtn"),
  tabs: requireClass(styles.tabs, "activitybar.module.css", "tabs"),
  body: requireClass(styles.body, "activitybar.module.css", "body"),
};

// A quiet clock for the watch rows' cadence wording ("every 5m · next ~2m"):
// re-renders the open sidebar on a slow interval, cheap enough for a surface
// that is usually closed.
function useNow(intervalMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(timer);
  }, [intervalMs]);
  return now;
}

function Breadcrumb({ scope }: { scope: ActivityScope }) {
  return (
    <nav className={CLASS.crumbs} aria-label="Scope">
      {scope.path.map((crumb, depth) => {
        const last = depth === scope.path.length - 1;
        return (
          <span key={crumb.ref} className={CLASS.crumbWrap}>
            {depth > 0 ? <span className={CLASS.crumbSep}>›</span> : null}
            {last ? (
              <span className={CLASS.crumbCurrent}>{crumb.title}</span>
            ) : (
              <button type="button" className={CLASS.crumbBtn} onClick={() => openSessionByRef(crumb.ref)}>
                {crumb.title}
              </button>
            )}
          </span>
        );
      })}
    </nav>
  );
}

function tabBody(scope: ActivityScope, tab: ActivityTab, now: number) {
  switch (tab) {
    case "agents":
      return <AgentsTab scope={scope} />;
    case "jobs":
      return <JobsTab scope={scope} />;
    case "watches":
      return <WatchesTab scope={scope} now={now} />;
    case "tasks":
      return <TasksTab scope={scope} />;
  }
}

export function ActivitySidebar() {
  const open = useActivitySidebarStore((state) => state.open);
  const tab = useActivitySidebarStore((state) => state.tab);
  const navigation = useNavigationStore();
  useWorkspaceStore((state) => state.focusedPaneId);
  const now = useNow();
  const ref = focusedActivityScopeRef();
  const scope = ref === null ? null : deriveScope(navigation, ref);
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
            <Breadcrumb scope={scope} />
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
              options={[
                { value: "agents", label: `Agents ${scope.counts.activeSubagents}` },
                {
                  value: "jobs",
                  label: `Jobs ${(scope.leaf.running_jobs ?? []).length + (scope.leaf.completed_jobs ?? []).length}`,
                },
                { value: "watches", label: `Watches ${scope.counts.armedWatches}` },
                { value: "tasks", label: `Tasks ${scope.counts.tasksDone}/${scope.counts.tasksTotal}` },
              ]}
            />
          </div>
          <div className={CLASS.body}>{tabBody(scope, tab, now)}</div>
        </m.aside>
      ) : null}
    </AnimatePresence>
  );
}
