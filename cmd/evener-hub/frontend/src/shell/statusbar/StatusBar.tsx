// The status bar: the glance surface of the zoom system. The scope breadcrumb
// (what you're reading) on the left, four counters (what it's doing) on the
// right, always on, desktop only. A chip click escalates to the activity
// sidebar preselected to the matching tab; a crumb click opens that session.
// Everything renders from the navigation store through deriveScope - no
// activity state lives here.

import { useNavigationStore } from "../../stores/navigation/store";
import { activitySidebarStore } from "../activitybar/activitySidebarStore";
import { focusedActivityScopeRef } from "../focusedSession";
import { WatchGlyph } from "../rail/RailRow";
import { openSessionByRef } from "../sessionPlacement";
import { useWorkspaceStore } from "../workspace";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./statusbar.module.css";
import { type ActivityTab, deriveScope } from "./statusScope";

// The requireClass map: styles.* is string|undefined under
// noUncheckedIndexedAccess, so every class resolves once, loudly, at load.
const CLASS = {
  bar: requireClass(styles.bar, "statusbar.module.css", "bar"),
  crumbs: requireClass(styles.crumbs, "statusbar.module.css", "crumbs"),
  crumbWrap: requireClass(styles.crumbWrap, "statusbar.module.css", "crumbWrap"),
  crumbSep: requireClass(styles.crumbSep, "statusbar.module.css", "crumbSep"),
  crumbCurrent: requireClass(styles.crumbCurrent, "statusbar.module.css", "crumbCurrent"),
  crumbBtn: requireClass(styles.crumbBtn, "statusbar.module.css", "crumbBtn"),
  chips: requireClass(styles.chips, "statusbar.module.css", "chips"),
  chip: requireClass(styles.chip, "statusbar.module.css", "chip"),
  chipGlyph: requireClass(styles.chipGlyph, "statusbar.module.css", "chipGlyph"),
  watchGlyph: requireClass(styles.watchGlyph, "statusbar.module.css", "watchGlyph"),
};

function Chip({
  tab,
  label,
  count,
  glyph,
}: {
  tab: ActivityTab;
  label: string;
  count: string;
  glyph: React.ReactNode;
}) {
  return (
    <button
      type="button"
      className={CLASS.chip}
      aria-label={`${label} - open the activity sidebar`}
      onClick={() => activitySidebarStore.getState().openWith(tab)}
    >
      <span className={CLASS.chipGlyph} aria-hidden="true">
        {glyph}
      </span>
      {count}
    </button>
  );
}

export function StatusBar() {
  const navigation = useNavigationStore();
  // Subscribing to the focused pane id re-renders the bar when a drill (a
  // transcript pane focusing) changes the scope; the sticky scope ref itself
  // is subscription-maintained in focusedSession.ts.
  useWorkspaceStore((state) => state.focusedPaneId);
  const ref = focusedActivityScopeRef();
  const scope = ref === null ? null : deriveScope(navigation, ref);
  if (scope === null) return null;
  const { counts } = scope;
  return (
    <div className={CLASS.bar} data-testid="statusbar">
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
      <div className={CLASS.chips}>
        <Chip
          tab="agents"
          label={`Agents, ${counts.activeSubagents} active`}
          count={`${counts.activeSubagents}`}
          glyph="⌘"
        />
        <Chip tab="jobs" label={`Jobs, ${counts.runningJobs} running`} count={`${counts.runningJobs}`} glyph="$" />
        <Chip
          tab="watches"
          label={`Watches, ${counts.armedWatches} armed`}
          count={`${counts.armedWatches}`}
          glyph={<WatchGlyph className={CLASS.watchGlyph} testId="statusbar-watch-glyph" />}
        />
        {counts.tasksTotal > 0 ? (
          <Chip
            tab="tasks"
            label={`Tasks, ${counts.tasksDone} of ${counts.tasksTotal} done`}
            count={`${counts.tasksDone}/${counts.tasksTotal}`}
            glyph="☑"
          />
        ) : null}
      </div>
    </div>
  );
}
