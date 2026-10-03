// The status bar: a session pane's glance surface. Its working location sits
// on the left and its activity counters sit on the right. A chip click first
// focuses the owning pane, then opens the shared activity sidebar on the
// matching tab. deriveScope combines navigation placement with the shared
// session activity summary; no activity state lives here.

import { type ReactNode, useMemo } from "react";
import { navigationStore, useNavigationStore } from "../../stores/navigation/store";
import { useSessionActivity } from "../../stores/sessionActivity";
import { requireClass } from "../../widgets/internal/requireClass";
import { activitySidebarStore } from "../activitybar/activitySidebarStore";
import { ACTIVITY_TABS } from "../activitybar/activityTabs";
import { workspaceStore } from "../workspace";
import styles from "./statusbar.module.css";
import { deriveScope } from "./statusScope";

const CLASS = {
  bar: requireClass(styles.bar, "statusbar.module.css", "bar"),
  leading: requireClass(styles.leading, "statusbar.module.css", "leading"),
  chips: requireClass(styles.chips, "statusbar.module.css", "chips"),
  chip: requireClass(styles.chip, "statusbar.module.css", "chip"),
  chipName: requireClass(styles.chipName, "statusbar.module.css", "chipName"),
  chipCount: requireClass(styles.chipCount, "statusbar.module.css", "chipCount"),
  chipGlyph: requireClass(styles.chipGlyph, "statusbar.module.css", "chipGlyph"),
};

export interface StatusBarProps {
  sessionRef: string;
  paneId: string;
  leading: ReactNode;
}

export function StatusBar({ sessionRef, paneId, leading }: StatusBarProps) {
  // Narrow subscriptions: re-render on the resources map (nav data) or the
  // pane's ref, not on every store touch (mode flips, expansion, attention).
  const resources = useNavigationStore((state) => state.resources);
  // Observe the shared session summary for context and authoritative counts.
  // The sidebar shares this owner; badges do not demand collection pages.
  const { snapshot } = useSessionActivity(sessionRef);
  // biome-ignore lint/correctness/useExhaustiveDependencies: `resources` is the memo's invalidation key, not a value the memo reads (the store is read imperatively inside)
  const scope = useMemo(
    () => deriveScope(navigationStore.getState(), sessionRef, snapshot),
    [resources, sessionRef, snapshot],
  );
  const { counts } = scope;
  return (
    <div className={CLASS.bar} data-testid="statusbar">
      <div className={CLASS.leading}>{leading}</div>
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
              workspaceStore.getState().focusPane(paneId);
              activitySidebarStore.getState().openWith(tab.id, event.currentTarget);
            }}
          >
            <span className={CLASS.chipGlyph} aria-hidden="true">
              {tab.glyph}
            </span>
            <span className={CLASS.chipName}>{tab.label}</span>
            <span className={CLASS.chipCount}>{tab.chipCount(counts)}</span>
          </button>
        ))}
      </div>
    </div>
  );
}
