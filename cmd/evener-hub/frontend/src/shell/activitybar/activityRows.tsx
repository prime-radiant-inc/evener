// The row grammar of the activity surfaces, on wire data: one agent row, one
// job row, one watch row, shared by the sidebar's tabs. An agent row leads
// with the shared subagent status dot — the working ring stays lit while the
// run is open — while job and watch rows keep glyph tones (alive/attention/
// danger; a settled row stays in the quiet glyph ink); the wording comes from
// the app's own formatters (jobStatusDisplay, watchMeta, watchName) so the
// sidebar can never drift from the rail or the activity panel.

import type { ActivityWatchRow, JobActivityJob, SessionDelegate } from "@evener/appwire-client";
import {
  activityDelegateState,
  activityNodeID,
  delegateWaitText,
  delegateWorkText,
  isFailedDelegateOutcome,
  jobIsFailed,
  jobStatusDisplay,
  watchMeta,
  watchName,
} from "@evener/appwire-client";
import type { ReactNode } from "react";
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

export function delegateRowName(sub: SessionDelegate): string {
  return sub.name?.trim() || sub.description || sub.task || sub.delegateId;
}

export function AgentRow({
  sub,
  onDrill,
  now,
  omitSubagentWait = false,
  trailingAction,
  anchorId,
}: {
  sub: SessionDelegate;
  onDrill?: () => void;
  now: number;
  omitSubagentWait?: boolean;
  trailingAction?: ReactNode;
  anchorId?: string;
}) {
  const successful = agentIsSuccessful(sub);
  const failed = sub.terminal ? isFailedDelegateOutcome(sub.outcome) : false;
  const status = agentStateText(sub);
  // A row held open by the delegate's own running job says so instead of the
  // bare running word, on the visible meta and in the accessible description.
  const wait = delegateWaitText(sub);
  const work = delegateWorkText(sub, status, omitSubagentWait ? { omitSubagents: true } : undefined);
  const name = delegateRowName(sub);
  const commonProps = {
    variant: "plain" as const,
    "aria-description": wait || status,
    leading: (
      <span className={CLASS.rowGlyph}>
        <SubagentDot delegate={sub} now={now} labeled />
      </span>
    ),
    label: (
      <span title={`${name} · ${status}`}>
        <RowStatus wireStatus={sub.status} />
        {name}
      </span>
    ),
    chips: <ActivityTime value={agentActivityTime(sub)} now={now} />,
    // The intent replaces the status word here; the label's title keeps the
    // word reachable, JobRow's pattern for a value the meta line hides.
    meta:
      successful || work === "" ? undefined : (
        <span data-activity-meta data-failed={failed || undefined}>
          {work}
        </span>
      ),
    "data-activity-anchor": anchorId ?? activityNodeID({ ...sub, kind: "delegate" }),
  };
  return onDrill ? (
    <ListRow {...commonProps} onSelect={onDrill} trailingAction={trailingAction} />
  ) : (
    <ListRow {...commonProps} />
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
