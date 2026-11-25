// The activity chips row: the session's glance counters (agents, jobs,
// notes) as touch targets that focus the owning pane and open the shared
// activity sidebar on the matching tab. deriveScope combines navigation
// placement with the shared session activity summary; no activity state
// lives here.

import { memo } from "react";
import { CountButton } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { activitySidebarStore } from "../activitybar/activitySidebarStore";
import { ACTIVITY_TABS } from "../activitybar/activityTabs";
import { useIsMobile } from "../useIsMobile";
import { workspaceStore } from "../workspace";
import styles from "./statusbar.module.css";
import { useActivityScope } from "./useActivityScope";

const CLASS = {
  chips: requireClass(styles.chips, "statusbar.module.css", "chips"),
};

export interface ActivityChipsProps {
  sessionRef: string;
  paneId?: string;
}

export const ActivityChips = memo(function ActivityChips({ sessionRef, paneId }: ActivityChipsProps) {
  const { counts } = useActivityScope(sessionRef);
  const mobile = useIsMobile();
  return (
    <div className={CLASS.chips}>
      {ACTIVITY_TABS.filter((tab) => tab.chipVisible(counts)).map((tab) => (
        <CountButton
          key={tab.id}
          data-activity-tab={tab.id}
          data-pane-id={paneId}
          data-session-ref={sessionRef}
          glyph={tab.glyph}
          label={tab.label}
          count={mobile ? tab.mobileCount(counts) : tab.chipCount(counts)}
          accessibleLabel={`${tab.chipLabel(counts)} - open Overview`}
          title={tab.chipLabel(counts)}
          compact={mobile}
          chrome="pill"
          onClick={(event) => {
            if (paneId) workspaceStore.getState().focusPane(paneId);
            // A chip is a focus-based request: it releases any held column
            // scope and saves against its own conversation, even when
            // focus does not move.
            activitySidebarStore.getState().openFor(sessionRef, tab.id, event.currentTarget, paneId);
          }}
        />
      ))}
    </div>
  );
});
