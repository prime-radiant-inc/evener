// Activity counters share authoritative active/total labels across the footer
// and sidebar. Tasks use the settled count (done plus cancelled) and total
// from the selected navigation row.

import type { SessionDelegate } from "@evener/appwire-client";
import type { ComponentType, ReactNode } from "react";
import { requireClass } from "../../widgets/internal/requireClass";
import { WatchGlyph } from "../rail/RailRow";
import type { ActivityScope, ActivityTab, ScopeCounts } from "../statusbar/statusScope";
import { AboutTab } from "./AboutTab";
import { AgentsTab } from "./AgentsTab";
import styles from "./activitybar.module.css";
import { JobsTab } from "./JobsTab";
import { TasksTab } from "./TasksTab";
import { WatchesTab } from "./WatchesTab";

const WATCH_GLYPH_CLASS = requireClass(styles.watchGlyph, "activitybar.module.css", "watchGlyph");

export interface ActivityTabBodyProps {
  scope: ActivityScope;
  onDrill?: (sub: SessionDelegate) => void;
}

export interface ActivityTabSpec {
  id: ActivityTab;
  glyph: ReactNode;
  label: string;
  /** Active/total count, or settled/total for Tasks. */
  chipCount(counts: ScopeCounts): string;
  /** The chip's accessible label. */
  chipLabel(counts: ScopeCounts): string;
  /** Category and shared fraction on separate lines within the tab. */
  tabLabel(counts: ScopeCounts): string;
  /** The kind shows in the bar only when there's something to say (tasks hide
   * with no list); About has no chip. The sidebar lists every category. */
  chipVisible(counts: ScopeCounts): boolean;
  Body: ComponentType<ActivityTabBodyProps>;
}

const fraction = (active: number | null, total: number | null) =>
  active === null || total === null ? "—" : `${active}/${total}`;
const countLabel = (kind: string, active: number | null, total: number | null, state: string) =>
  active === null || total === null ? `${kind}, counts unknown` : `${kind}, ${active} of ${total} ${state}`;

export const ACTIVITY_TABS: readonly ActivityTabSpec[] = [
  {
    id: "agents",
    label: "Agents",
    glyph: "⌘",
    chipCount: (c) => fraction(c.activeSubagents, c.delegatesTotal),
    chipLabel: (c) => countLabel("Agents", c.activeSubagents, c.delegatesTotal, "active"),
    tabLabel: (c) => `Agents\n${fraction(c.activeSubagents, c.delegatesTotal)}`,
    chipVisible: () => true,
    Body: AgentsTab,
  },
  {
    id: "jobs",
    label: "Jobs",
    glyph: "$",
    chipCount: (c) => fraction(c.runningJobs, c.jobsTotal),
    chipLabel: (c) => countLabel("Jobs", c.runningJobs, c.jobsTotal, "running"),
    tabLabel: (c) => `Jobs\n${fraction(c.runningJobs, c.jobsTotal)}`,
    chipVisible: () => true,
    Body: JobsTab,
  },
  {
    id: "watches",
    label: "Watches",
    glyph: <WatchGlyph className={WATCH_GLYPH_CLASS} testId="activity-tab-watch-glyph" />,
    chipCount: (c) => fraction(c.armedWatches, c.watchesTotal),
    chipLabel: (c) => countLabel("Watches", c.armedWatches, c.watchesTotal, "armed"),
    tabLabel: (c) => `Watches\n${fraction(c.armedWatches, c.watchesTotal)}`,
    chipVisible: () => true,
    Body: WatchesTab,
  },
  {
    id: "tasks",
    label: "Tasks",
    glyph: "☑",
    chipCount: (c) => fraction(c.tasksSettled, c.tasksTotal),
    chipLabel: (c) => countLabel("Tasks", c.tasksSettled, c.tasksTotal, "settled"),
    tabLabel: (c) => `Tasks\n${fraction(c.tasksSettled, c.tasksTotal)}`,
    chipVisible: (c) => c.tasksTotal > 0,
    Body: TasksTab,
  },
  {
    id: "about",
    glyph: null,
    label: "About",
    chipCount: () => "",
    chipLabel: () => "About",
    tabLabel: () => "About",
    chipVisible: () => false,
    Body: AboutTab,
  },
];

export function activityTabSpec(id: ActivityTab): ActivityTabSpec {
  const spec = ACTIVITY_TABS.find((tab) => tab.id === id);
  if (!spec) throw new Error(`unknown activity tab "${id}"`);
  return spec;
}
