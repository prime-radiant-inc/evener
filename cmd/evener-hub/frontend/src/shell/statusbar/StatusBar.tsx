// The status bar: the glance surface of the zoom system. The scope breadcrumb
// (what you're reading) on the left, the activity counters on the right,
// always on, desktop only. A chip click escalates to the activity sidebar
// preselected to the matching tab. Everything renders from the navigation
// store through deriveScope - no activity state lives here.

import { useMemo } from "react";
import { navigationStore, useNavigationStore } from "../../stores/navigation/store";
import { useSessionActivity } from "../../stores/sessionActivity";
import { requireClass } from "../../widgets/internal/requireClass";
import { activitySidebarStore } from "../activitybar/activitySidebarStore";
import { ACTIVITY_TABS } from "../activitybar/activityTabs";
import { useFocusedActivityScopeRef } from "../focusedSession";
import { ScopeCrumbs } from "./ScopeCrumbs";
import styles from "./statusbar.module.css";
import { deriveScope } from "./statusScope";

const CLASS = {
  bar: requireClass(styles.bar, "statusbar.module.css", "bar"),
  chips: requireClass(styles.chips, "statusbar.module.css", "chips"),
  chip: requireClass(styles.chip, "statusbar.module.css", "chip"),
  chipGlyph: requireClass(styles.chipGlyph, "statusbar.module.css", "chipGlyph"),
};

export function StatusBar() {
  // Narrow subscriptions: re-render on the resources map (nav data) or the
  // scope's ref, not on every store touch (mode flips, expansion, attention).
  const resources = useNavigationStore((state) => state.resources);
  const ref = useFocusedActivityScopeRef();
  // The location and the subagents page are the surfaces' data; nothing else
  // fetches them for a scope (the open sidebar rides this too).
  const { snapshot } = useSessionActivity(ref);
  // biome-ignore lint/correctness/useExhaustiveDependencies: `resources` is the memo's invalidation key, not a value the memo reads (the store is read imperatively inside)
  const scope = useMemo(
    () => (ref === null ? null : deriveScope(navigationStore.getState(), ref, snapshot)),
    [resources, ref, snapshot],
  );
  if (scope === null) return null;
  const { counts } = scope;
  return (
    <div className={CLASS.bar} data-testid="statusbar">
      <ScopeCrumbs path={scope.path} />
      {!scope.ancestryKnown ? <span>Finding session context…</span> : null}
      <div className={CLASS.chips}>
        {ACTIVITY_TABS.filter((tab) => tab.chipVisible(counts)).map((tab) => (
          <button
            key={tab.id}
            type="button"
            className={CLASS.chip}
            aria-label={`${tab.chipLabel(counts)} - open the activity sidebar`}
            onClick={() => activitySidebarStore.getState().openWith(tab.id)}
          >
            <span className={CLASS.chipGlyph} aria-hidden="true">
              {tab.glyph}
            </span>
            {tab.chipCount(counts)}
          </button>
        ))}
      </div>
    </div>
  );
}
