// The activity chips row: the session's glance counters (agents, jobs,
// notes) as touch targets that focus the owning pane and open the shared
// activity sidebar on the matching tab. deriveScope combines navigation
// placement with the shared session activity summary; no activity state
// lives here.

import { memo, useMemo } from "react";
import { navigationStore, useNavigationStore } from "../../stores/navigation/store";
import { useSessionActivity, useSubagentCounts } from "../../stores/sessionActivity";
import { requireClass } from "../../widgets/internal/requireClass";
import { activitySidebarStore } from "../activitybar/activitySidebarStore";
import { ACTIVITY_TABS } from "../activitybar/activityTabs";
import { useIsMobile } from "../useIsMobile";
import { workspaceStore } from "../workspace";
import styles from "./statusbar.module.css";
import { deriveScope } from "./statusScope";

const CLASS = {
  chips: requireClass(styles.chips, "statusbar.module.css", "chips"),
  chip: requireClass(styles.chip, "statusbar.module.css", "chip"),
  chipName: requireClass(styles.chipName, "statusbar.module.css", "chipName"),
  chipCount: requireClass(styles.chipCount, "statusbar.module.css", "chipCount"),
  chipGlyph: requireClass(styles.chipGlyph, "statusbar.module.css", "chipGlyph"),
};

export interface ActivityChipsProps {
  sessionRef: string;
  paneId?: string;
}

// Memoized: the phone composer re-renders on every keystroke, but this row
// changes only when its own store subscriptions do, so the two stable props
// hold it quiet while the editor types.
export const ActivityChips = memo(function ActivityChips({ sessionRef, paneId }: ActivityChipsProps) {
  // Narrow subscriptions: re-render on the resources map (nav data), not on
  // every store touch (mode flips, expansion, attention).
  const resources = useNavigationStore((state) => state.resources);
  // Observe the shared session summary for context and authoritative counts.
  // The sidebar shares this owner; badges do not demand collection pages.
  const { snapshot } = useSessionActivity(sessionRef);
  const subagents = useSubagentCounts(sessionRef);
  // biome-ignore lint/correctness/useExhaustiveDependencies: `resources` is the memo's invalidation key, not a value the memo reads (the store is read imperatively inside)
  const scope = useMemo(
    () => deriveScope(navigationStore.getState(), sessionRef, snapshot, subagents),
    [resources, sessionRef, snapshot, subagents],
  );
  const { counts } = scope;
  // A phone renders this row on its own line below the composer card, so a
  // chip shows the compact single active count; the full active/total
  // fraction stays in the chip's accessible name.
  const mobile = useIsMobile();
  return (
    <div className={CLASS.chips}>
      {ACTIVITY_TABS.filter((tab) => tab.chipVisible(counts)).map((tab) => (
        <button
          key={tab.id}
          type="button"
          data-activity-tab={tab.id}
          data-pane-id={paneId}
          data-session-ref={sessionRef}
          className={CLASS.chip}
          title={tab.chipLabel(counts)}
          aria-label={`${tab.chipLabel(counts)} - open Overview`}
          onClick={(event) => {
            if (paneId) workspaceStore.getState().focusPane(paneId);
            // A chip is a focus-based request: it releases any held column
            // scope and saves against its own conversation, even when
            // focus does not move.
            activitySidebarStore.getState().openFor(sessionRef, tab.id, event.currentTarget, paneId);
          }}
        >
          <span className={CLASS.chipGlyph} aria-hidden="true">
            {tab.glyph}
          </span>
          <span className={CLASS.chipName}>{tab.label}</span>
          <span className={CLASS.chipCount}>{mobile ? tab.mobileCount(counts) : tab.chipCount(counts)}</span>
        </button>
      ))}
    </div>
  );
});
