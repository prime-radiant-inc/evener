// The row grammar of the activity surfaces, on wire data: one agent row, one
// job row, one watch row, shared by the sidebar's tabs. An agent row leads
// with the shared subagent status dot — alive while fresh, quiet ink past the
// threshold — while job and watch rows keep glyph tones (alive/attention/
// danger; a settled row stays in the quiet glyph ink); the wording comes from
// the app's own formatters (jobStatusDisplay, watchMeta, watchName) so the
// sidebar can never drift from the rail or the activity panel.

import type { ActivityWatchRow, JobActivityJob, SessionDelegate } from "@evener/appwire-client";
import {
  activityDelegateState,
  activityNodeID,
  DELEGATE_QUIET_AFTER_MS,
  delegateTiming,
  delegateWorkText,
  isFailedDelegateOutcome,
  jobIsFailed,
  jobStatusDisplay,
  watchMeta,
  watchName,
} from "@evener/appwire-client";
import { useEffect, useState } from "react";
import { ActivityWatchDetail } from "../../panes/session/chrome/ActivityRowDetail";
import { jobStatusDotState } from "../../panes/session/chrome/activityFormat";
import { ActivityTime } from "../../panes/session/chrome/activityTime";
import { Disclosure, ListRow, SubagentDot, ToolIcon, type ToolIconTone } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { WatchGlyph } from "../rail/RailRow";

import styles from "./activitybar.module.css";
import { agentActivityTime, jobActivityTime, watchActivityTime } from "./activityTime";

const CLASS = {
  rowContent: requireClass(styles.rowContent, "activitybar.module.css", "rowContent"),
  rowGlyph: requireClass(styles.rowGlyph, "activitybar.module.css", "rowGlyph"),
  rowBody: requireClass(styles.rowBody, "activitybar.module.css", "rowBody"),
  rowName: requireClass(styles.rowName, "activitybar.module.css", "rowName"),
  rowMeta: requireClass(styles.rowMeta, "activitybar.module.css", "rowMeta"),
  rowTitle: requireClass(styles.rowTitle, "activitybar.module.css", "rowTitle"),
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

function agentStateText(sub: SessionDelegate): string {
  return activityDelegateState({ ...sub, branch: {} }).status;
}

function agentIsSuccessful(sub: SessionDelegate): boolean {
  if (!sub.terminal) return false;
  const outcome = sub.outcome?.trim().toLowerCase();
  if (outcome) return ["completed", "done", "success"].includes(outcome);
  return agentStateText(sub) === "completed";
}

function RowStatus({ wireStatus }: { wireStatus: string }) {
  return <span aria-hidden="true" data-activity-status data-status={wireStatus} hidden />;
}

// The dot must flip at the quiet threshold, not only when the next publish
// arrives: a quiet row gets no frames. One timeout per running row,
// cancelled when its activity moves or the row unmounts (the native
// useNowPastQuiet precedent).
function useDotFlip(sub: SessionDelegate): number {
  // The state's value is never read - only the re-render it forces matters -
  // so it must change on every flip: a row that goes quiet twice must flip
  // twice, and a repeated value would let the second flip pass unnoticed.
  const [, setFlippedAt] = useState(0);
  useEffect(() => {
    if (sub.terminal === true) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    // The daemon's clock can run ahead of this one, clamping the measured
    // silence to zero while the anchor is still in the future: every fire
    // re-arms until the live silence has actually crossed the threshold.
    const arm = () => {
      const timing = delegateTiming(sub, Date.now());
      // A frozen snapshot never grows - waking for it would re-render the row
      // forever without ever flipping the dot, the native wake's own guard.
      if (!timing.quietLive || timing.quietForMs === undefined) return;
      const remaining = DELEGATE_QUIET_AFTER_MS - timing.quietForMs;
      if (remaining <= 0) {
        // The threshold crossed between this row's render and its effect:
        // the rendered dot still says fresh, and a quiet row gets no publish
        // that would ever re-render it. The one render this forces retells
        // the dot.
        setFlippedAt(Date.now());
        return;
      }
      timer = setTimeout(() => {
        setFlippedAt(Date.now());
        arm();
      }, remaining + 1);
    };
    arm();
    return () => clearTimeout(timer);
  }, [sub]);
  return Date.now();
}

export function AgentRow({ sub, onDrill, now }: { sub: SessionDelegate; onDrill?: () => void; now: number }) {
  const dotNow = useDotFlip(sub);
  const successful = agentIsSuccessful(sub);
  const failed = sub.terminal ? isFailedDelegateOutcome(sub.outcome) : false;
  const status = agentStateText(sub);
  const name = sub.name?.trim() || sub.description || sub.task || sub.delegateId;
  return (
    <ListRow
      variant="plain"
      aria-description={status}
      leading={
        <span className={CLASS.rowGlyph}>
          <SubagentDot delegate={sub} now={dotNow} labeled />
        </span>
      }
      label={
        <span title={`${name} · ${status}`}>
          <RowStatus wireStatus={sub.status} />
          {name}
        </span>
      }
      chips={<ActivityTime value={agentActivityTime(sub)} now={now} />}
      // The intent replaces the status word here; the label's title keeps the
      // word reachable, JobRow's pattern for a value the meta line hides.
      meta={
        successful ? undefined : (
          <span data-activity-meta data-failed={failed || undefined}>
            {delegateWorkText(sub, status)}
          </span>
        )
      }
      onSelect={onDrill}
      data-activity-anchor={activityNodeID({ ...sub, kind: "delegate" })}
    />
  );
}

export function JobRow({ job, onOpen, now }: { job: JobActivityJob; onOpen?: () => void; now: number }) {
  const description = job.description?.trim();
  const status = jobStatusDisplay(job.status, job.reason);
  const successful = job.terminal && job.status === "completed" && !jobIsFailed(job);
  const failed = jobIsFailed(job);
  return (
    <ListRow
      variant="plain"
      aria-description={status}
      leading={
        <span className={CLASS.rowGlyph}>
          <ToolIcon kind="job" tone={jobTone(job)} />
        </span>
      }
      label={
        <span className={description ? undefined : CLASS.rowMono} title={job.command}>
          <RowStatus wireStatus={job.status} />
          {description || job.command?.trim() || job.jobId}
        </span>
      }
      chips={<ActivityTime value={jobActivityTime(job)} now={now} />}
      meta={
        successful ? undefined : (
          <span data-activity-meta data-failed={failed || undefined}>
            {status}
          </span>
        )
      }
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
              <span className={CLASS.rowTitle}>
                <span className={CLASS.rowName} title={watchName(watch)}>
                  {watchName(watch)}
                </span>
                <ActivityTime value={watchActivityTime(watch)} now={now} />
              </span>
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
