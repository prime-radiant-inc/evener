// The row grammar of the activity surfaces, on wire data: one agent row, one
// job row, one watch row, shared by the sidebar's tabs. Agent rows lead with
// the quiet-encoding subagent dot; job and watch rows keep glyph tones, and
// the wording comes from the app's own formatters (jobStatusDisplay,
// watchMeta, watchName) so the sidebar can never drift from the rail or the
// activity panel.

import type { ActivityWatchRow, JobActivityJob, SessionDelegate } from "@evener/appwire-client";
import {
  activityDelegateState,
  activityNodeID,
  DELEGATE_QUIET_AFTER_MS,
  delegateTiming,
  delegateWorkText,
  jobStatusDisplay,
  watchMeta,
  watchName,
} from "@evener/appwire-client";
import { useEffect, useState } from "react";
import { ActivityWatchDetail } from "../../panes/session/chrome/ActivityRowDetail";
import { jobStatusDotState } from "../../panes/session/chrome/activityFormat";
import { Disclosure } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { SubagentDot } from "../../widgets/statusdot";
import { WatchGlyph } from "../rail/RailRow";

import styles from "./activitybar.module.css";

const CLASS = {
  row: requireClass(styles.row, "activitybar.module.css", "row"),
  rowContent: requireClass(styles.rowContent, "activitybar.module.css", "rowContent"),
  rowGlyph: requireClass(styles.rowGlyph, "activitybar.module.css", "rowGlyph"),
  glyphAlive: requireClass(styles.glyphAlive, "activitybar.module.css", "glyphAlive"),
  glyphAttention: requireClass(styles.glyphAttention, "activitybar.module.css", "glyphAttention"),
  glyphDanger: requireClass(styles.glyphDanger, "activitybar.module.css", "glyphDanger"),
  glyphQuiet: requireClass(styles.glyphQuiet, "activitybar.module.css", "glyphQuiet"),
  rowBody: requireClass(styles.rowBody, "activitybar.module.css", "rowBody"),
  rowName: requireClass(styles.rowName, "activitybar.module.css", "rowName"),
  rowMeta: requireClass(styles.rowMeta, "activitybar.module.css", "rowMeta"),
  rowMono: requireClass(styles.rowMono, "activitybar.module.css", "rowMono"),
  rowBtn: requireClass(styles.rowBtn, "activitybar.module.css", "rowBtn"),
  watchGlyph: requireClass(styles.watchGlyph, "activitybar.module.css", "watchGlyph"),
};

type Tone = "alive" | "attention" | "danger" | "quiet";

const TONE_CLASS: Record<Tone, string> = {
  alive: CLASS.glyphAlive,
  attention: CLASS.glyphAttention,
  danger: CLASS.glyphDanger,
  quiet: CLASS.glyphQuiet,
};

function jobTone(job: JobActivityJob): Tone {
  if (job.terminal) return "quiet";
  switch (jobStatusDotState(job.status, job.terminal)) {
    case "working":
      return "alive";
    case "needs-you":
      return "attention";
    case "failed":
      return "danger";
    default:
      return "quiet";
  }
}

function agentStateText(sub: SessionDelegate): string {
  return activityDelegateState({ ...sub, branch: {} }).status;
}

// The dot must flip at the quiet threshold, not only when the next publish
// arrives: a quiet row gets no frames. One timeout per running row,
// cancelled when its activity moves or the row unmounts (the native
// useNowPastQuiet precedent).
function useDotFlip(sub: SessionDelegate): number {
  const [flippedAt, setFlippedAt] = useState(() => Date.now());
  useEffect(() => {
    if (sub.terminal === true) return;
    const { quietForMs } = delegateTiming(sub, Date.now());
    if (quietForMs === undefined) return;
    const remaining = DELEGATE_QUIET_AFTER_MS - quietForMs;
    if (remaining <= 0) return; // the dot already renders quiet
    const timer = setTimeout(() => setFlippedAt(Date.now()), remaining + 1);
    return () => clearTimeout(timer);
  }, [sub]);
  // A sibling row's publish re-renders this row without moving its anchor,
  // so never compute the dot younger than the flip already proved.
  return Math.max(flippedAt, Date.now());
}

function AgentRowBody({ sub }: { sub: SessionDelegate }) {
  const now = useDotFlip(sub);
  return (
    <>
      <SubagentDot delegate={sub} now={now} labeled />
      <span className={CLASS.rowBody}>
        <span className={CLASS.rowName} title={agentStateText(sub)}>
          {sub.name?.trim() || sub.description || sub.task || sub.delegateId}
        </span>
        {/* The intent replaces the status word here; the title above keeps the
         * word reachable, JobRow's pattern for a value the meta line hides. */}
        <span className={CLASS.rowMeta}>{delegateWorkText(sub, agentStateText(sub))}</span>
      </span>
    </>
  );
}

export function AgentRow({ sub, onDrill }: { sub: SessionDelegate; onDrill?: () => void }) {
  const anchor = activityNodeID({ ...sub, kind: "delegate" });
  if (onDrill === undefined) {
    return (
      <div className={CLASS.row} data-activity-anchor={anchor}>
        <AgentRowBody sub={sub} />
      </div>
    );
  }
  return (
    <button type="button" className={`${CLASS.row} ${CLASS.rowBtn}`} onClick={onDrill} data-activity-anchor={anchor}>
      <AgentRowBody sub={sub} />
    </button>
  );
}

export function JobRow({ job, onOpen }: { job: JobActivityJob; onOpen?: () => void }) {
  const anchor = activityNodeID({ ...job, kind: "shell" });
  const description = job.description?.trim();
  const label = description || job.command?.trim() || job.jobId;
  const body = (
    <>
      <span className={`${CLASS.rowGlyph} ${TONE_CLASS[jobTone(job)]}`} aria-hidden="true">
        $
      </span>
      <span className={CLASS.rowBody}>
        <span className={`${CLASS.rowName}${description ? "" : ` ${CLASS.rowMono}`}`} title={job.command}>
          {label}
        </span>
        <span className={CLASS.rowMeta}>{jobStatusDisplay(job.status, job.reason)}</span>
      </span>
    </>
  );
  if (onOpen === undefined)
    return (
      <div className={CLASS.row} data-activity-anchor={anchor}>
        {body}
      </div>
    );
  return (
    <button type="button" className={`${CLASS.row} ${CLASS.rowBtn}`} onClick={onOpen} data-activity-anchor={anchor}>
      {body}
    </button>
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
            <span className={`${CLASS.rowGlyph} ${CLASS.glyphQuiet}`}>
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
