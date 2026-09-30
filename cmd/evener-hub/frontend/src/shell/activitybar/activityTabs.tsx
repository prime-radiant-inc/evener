// The activity kind table: the four kinds' glyphs, counter labels, and tab
// bodies, declared ONCE. The status bar's chips and the sidebar's segmented
// control both read it, so a count can never mean one thing in the chip and
// another on the tab (the review caught the Jobs count drifting exactly so:
// chip counted running, the tab label counted running + completed - now both
// count ACTIVE work, the same convention the Agents tab's current-only count
// already used).

import type { ComponentType, ReactNode } from "react";
import { requireClass } from "../../widgets/internal/requireClass";
import { WatchGlyph } from "../rail/RailRow";
import type { ActivityScope, ActivityTab, ScopeCounts } from "../statusbar/statusScope";
import { AgentsTab } from "./AgentsTab";
import styles from "./activitybar.module.css";
import { JobsTab } from "./JobsTab";
import { TasksTab } from "./TasksTab";
import { WatchesTab } from "./WatchesTab";

const WATCH_GLYPH_CLASS = requireClass(styles.watchGlyph, "activitybar.module.css", "watchGlyph");

export interface ActivityTabSpec {
  id: ActivityTab;
  glyph: ReactNode;
  /** The status bar chip's count text: active work only. */
  chipCount(counts: ScopeCounts): string;
  /** The chip's accessible label. */
  chipLabel(counts: ScopeCounts): string;
  /** The sidebar tab's label: the same active-work count, same convention. */
  tabLabel(counts: ScopeCounts): string;
  /** The kind shows in the bar only when there's something to say (tasks hide
   * with no list); the sidebar's control always lists all four. */
  chipVisible(counts: ScopeCounts): boolean;
  Body: ComponentType<{ scope: ActivityScope }>;
}

const countText = (count: number | null) => (count === null ? "…" : String(count));

export const ACTIVITY_TABS: readonly ActivityTabSpec[] = [
  {
    id: "agents",
    glyph: "⌘",
    chipCount: (c) => `${countText(c.activeSubagents)}`,
    chipLabel: (c) => `Agents, ${countText(c.activeSubagents)} active`,
    tabLabel: (c) => `Agents ${countText(c.activeSubagents)}`,
    chipVisible: () => true,
    Body: AgentsTab,
  },
  {
    id: "jobs",
    glyph: "$",
    chipCount: (c) => `${countText(c.runningJobs)}`,
    chipLabel: (c) => `Jobs, ${countText(c.runningJobs)} running`,
    tabLabel: (c) => `Jobs ${countText(c.runningJobs)}`,
    chipVisible: () => true,
    Body: JobsTab,
  },
  {
    id: "watches",
    glyph: <WatchGlyph className={WATCH_GLYPH_CLASS} testId="activity-tab-watch-glyph" />,
    chipCount: (c) => `${countText(c.armedWatches)}`,
    chipLabel: (c) => `Watches, ${countText(c.armedWatches)} armed`,
    tabLabel: (c) => `Watches ${countText(c.armedWatches)}`,
    chipVisible: () => true,
    Body: WatchesTab,
  },
  {
    id: "tasks",
    glyph: "☑",
    chipCount: (c) => `${c.tasksDone}/${c.tasksTotal}`,
    chipLabel: (c) => `Tasks, ${c.tasksDone} of ${c.tasksTotal} done`,
    tabLabel: (c) => `Tasks ${c.tasksDone}/${c.tasksTotal}`,
    chipVisible: (c) => c.tasksTotal > 0,
    Body: TasksTab,
  },
];

export function activityTabSpec(id: ActivityTab): ActivityTabSpec {
  const spec = ACTIVITY_TABS.find((tab) => tab.id === id);
  if (!spec) throw new Error(`unknown activity tab "${id}"`);
  return spec;
}
