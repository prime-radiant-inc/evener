// The row grammar of the activity surfaces, on wire data: one agent row, one
// job row, one watch row, shared by the sidebar's tabs. Glyph tones carry the
// state hues (alive/attention/danger; a settled row stays in the quiet
// glyph ink); the wording comes from the app's own formatters
// (jobStatusDisplay, watchMeta, watchName) so the sidebar can never drift
// from the rail or the activity panel.

import type { ActivityWatchRow, JobActivityJob, SessionDelegate } from "@evener/appwire-client";
import {
  activityDelegateState,
  activityNodeID,
  delegateWorkText,
  jobStatusDisplay,
  watchMeta,
  watchName,
} from "@evener/appwire-client";
import { ActivityWatchDetail } from "../../panes/session/chrome/ActivityRowDetail";
import { jobStatusDotState } from "../../panes/session/chrome/activityFormat";
import { Disclosure, ListRow, ToolIcon, type ToolIconTone } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { WatchGlyph } from "../rail/RailRow";

import styles from "./activitybar.module.css";

const CLASS = {
  rowContent: requireClass(styles.rowContent, "activitybar.module.css", "rowContent"),
  rowGlyph: requireClass(styles.rowGlyph, "activitybar.module.css", "rowGlyph"),
  rowBody: requireClass(styles.rowBody, "activitybar.module.css", "rowBody"),
  rowName: requireClass(styles.rowName, "activitybar.module.css", "rowName"),
  rowMeta: requireClass(styles.rowMeta, "activitybar.module.css", "rowMeta"),
  rowMono: requireClass(styles.rowMono, "activitybar.module.css", "rowMono"),
  watchGlyph: requireClass(styles.watchGlyph, "activitybar.module.css", "watchGlyph"),
};

function jobTone(job: JobActivityJob): ToolIconTone | undefined {
  if (job.terminal) return undefined;
  switch (jobStatusDotState(job.status, job.terminal)) {
    case "working":
      return "alive";
    case "needs-you":
      return "attention";
    case "failed":
      return "danger";
    default:
      return undefined;
  }
}

function agentTone(sub: SessionDelegate): ToolIconTone | undefined {
  if (sub.terminal) return undefined;
  if (["awaiting_approval", "awaiting_input", "needs-you"].includes(sub.phase)) return "attention";
  return "alive";
}

function agentStateText(sub: SessionDelegate): string {
  return activityDelegateState({ ...sub, branch: {} }).status;
}

export function AgentRow({ sub, onDrill }: { sub: SessionDelegate; onDrill?: () => void }) {
  return (
    <ListRow
      variant="plain"
      leading={
        <span className={CLASS.rowGlyph}>
          <ToolIcon kind="delegate" tone={agentTone(sub)} />
        </span>
      }
      label={
        <span title={agentStateText(sub)}>{sub.name?.trim() || sub.description || sub.task || sub.delegateId}</span>
      }
      // The intent replaces the status word here; the label's title keeps the
      // word reachable, JobRow's pattern for a value the meta line hides.
      meta={delegateWorkText(sub, agentStateText(sub))}
      onSelect={onDrill}
      data-activity-anchor={activityNodeID({ ...sub, kind: "delegate" })}
    />
  );
}

export function JobRow({ job, onOpen }: { job: JobActivityJob; onOpen?: () => void }) {
  const description = job.description?.trim();
  return (
    <ListRow
      variant="plain"
      leading={
        <span className={CLASS.rowGlyph}>
          <ToolIcon kind="job" tone={jobTone(job)} />
        </span>
      }
      label={
        <span className={description ? undefined : CLASS.rowMono} title={job.command}>
          {description || job.command?.trim() || job.jobId}
        </span>
      }
      meta={jobStatusDisplay(job.status, job.reason)}
      onSelect={onOpen}
      data-activity-anchor={activityNodeID({ ...job, kind: "shell" })}
    />
  );
}

export function WatchRow({ row, now }: { row: ActivityWatchRow; now: number }) {
  const { watch } = row;
  return (
    <div data-activity-anchor={row.id}>
      <Disclosure
        id={`sidebar-${row.id}`}
        summary={
          <span className={CLASS.rowContent}>
            <span className={CLASS.rowGlyph}>
              <WatchGlyph className={CLASS.watchGlyph} testId={`sidebar-watch-${watch.watch.id}`} />
            </span>
            <span className={CLASS.rowBody}>
              <span className={CLASS.rowName}>{watchName(watch)}</span>
              <span className={CLASS.rowMeta}>{watchMeta(watch, now)}</span>
            </span>
          </span>
        }
      >
        <ActivityWatchDetail row={row} now={now} />
      </Disclosure>
    </div>
  );
}
