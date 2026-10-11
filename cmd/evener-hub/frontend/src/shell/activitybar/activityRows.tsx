// The row grammar of the activity surfaces, on wire data: one agent row, one
// job row, one watch row, shared by the sidebar's tabs. Glyph tones carry the
// state hues (alive/attention/danger/quiet); the wording comes from the
// app's own formatters (jobStatusDisplay, watchMeta, watchName) so the
// sidebar can never drift from the rail or the activity panel.

import type { ActivityWatchRow, JobActivityJob, SessionDelegate } from "@evener/appwire-client";
import { activityDelegateState, activityNodeID, jobStatusDisplay, watchMeta, watchName } from "@evener/appwire-client";
import { ActivityWatchDetail } from "../../panes/session/chrome/ActivityRowDetail";
import { jobStatusDotState } from "../../panes/session/chrome/activityFormat";
import { Disclosure } from "../../widgets/disclosure";
import { requireClass } from "../../widgets/internal/requireClass";
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

function agentTone(sub: SessionDelegate): Tone {
  if (sub.terminal) return "quiet";
  if (["awaiting_approval", "awaiting_input", "needs-you"].includes(sub.phase)) return "attention";
  return "alive";
}

function agentStateText(sub: SessionDelegate): string {
  return activityDelegateState({ ...sub, branch: {} }).status;
}

function AgentRowBody({ sub }: { sub: SessionDelegate }) {
  return (
    <>
      <span className={`${CLASS.rowGlyph} ${TONE_CLASS[agentTone(sub)]}`} aria-hidden="true">
        ⌘
      </span>
      <span className={CLASS.rowBody}>
        <span className={CLASS.rowName}>{sub.name?.trim() || sub.description || sub.task || sub.delegateId}</span>
        <span className={CLASS.rowMeta}>{agentStateText(sub)}</span>
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
