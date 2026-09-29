// The row grammar of the activity surfaces, on wire data: one agent row, one
// job row, one watch row, shared by the sidebar's tabs. Glyph tones carry the
// state hues (alive/attention/danger/quiet); the wording comes from the
// app's own formatters (jobStatusDisplay, watchMeta, watchName) so the
// sidebar can never drift from the rail or the activity panel.

import type { NavigationJobSummary, NavigationSessionSummary, NavigationWatchSummary } from "@evener/appwire-client";
import { humanizeState, jobStatusDisplay, watchMeta, watchName } from "@evener/appwire-client";
import { jobStatusDotState } from "../../panes/session/chrome/activityFormat";
import { requireClass } from "../../widgets/internal/requireClass";
import { cadenceStateFor, WatchGlyph } from "../rail/RailRow";
import { activeWatchCount, displayState } from "../rail/railNodes";
import styles from "./activitybar.module.css";

const CLASS = {
  row: requireClass(styles.row, "activitybar.module.css", "row"),
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

function jobTone(status: string): Tone {
  switch (jobStatusDotState(status, status !== "running")) {
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

function agentTone(sub: NavigationSessionSummary): Tone {
  // The rail's own two-step: displayState folds approval/ask into the wire
  // state (a subagent's bare "awaiting" is idle), cadenceStateFor maps that
  // to the hue family. Same composition as RailRow, so the sidebar's rows
  // can never drift from the rail's.
  switch (cadenceStateFor(displayState(sub))) {
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

function agentStateText(sub: NavigationSessionSummary): string {
  return humanizeState(displayState(sub), sub.ask_pending === true, sub.approval_pending === true);
}

/** The one-line rollup an agent row carries, so a parent row says what is
 * inside a child without expanding it: "2 agents · 1 job · 1 watch ·
 * tasks 1/4". */
export function agentRollupLine(sub: NavigationSessionSummary): string {
  const parts: string[] = [];
  // The TRUE total, like the Agents tab's fold: loaded rows plus the wire's
  // omitted remainder, or the line understates the scope beside that fold.
  const agents = (sub.children ?? []).length + (sub.more_subagents ?? 0);
  const jobs = (sub.running_jobs ?? []).length;
  const watches = activeWatchCount(sub);
  if (agents > 0) parts.push(`${agents} agent${agents === 1 ? "" : "s"}`);
  if (jobs > 0) parts.push(`${jobs} job${jobs === 1 ? "" : "s"}`);
  if (watches > 0) parts.push(`${watches} watch${watches === 1 ? "" : "es"}`);
  if (sub.tasks !== undefined && sub.tasks.total > 0) parts.push(`tasks ${sub.tasks.done}/${sub.tasks.total}`);
  return parts.join(" · ");
}

function AgentRowBody({ sub }: { sub: NavigationSessionSummary }) {
  const rollup = agentRollupLine(sub);
  return (
    <>
      <span className={`${CLASS.rowGlyph} ${TONE_CLASS[agentTone(sub)]}`} aria-hidden="true">
        ⌘
      </span>
      <span className={CLASS.rowBody}>
        <span className={CLASS.rowName}>{sub.title}</span>
        <span className={CLASS.rowMeta}>
          {agentStateText(sub)}
          {rollup ? ` · ${rollup}` : ""}
        </span>
      </span>
    </>
  );
}

export function AgentRow({ sub, onDrill }: { sub: NavigationSessionSummary; onDrill?: () => void }) {
  if (onDrill === undefined) {
    return (
      <div className={CLASS.row}>
        <AgentRowBody sub={sub} />
      </div>
    );
  }
  return (
    <button type="button" className={`${CLASS.row} ${CLASS.rowBtn}`} onClick={onDrill}>
      <AgentRowBody sub={sub} />
    </button>
  );
}

export function JobRow({ job, onOpen }: { job: NavigationJobSummary; onOpen?: () => void }) {
  const body = (
    <>
      <span className={`${CLASS.rowGlyph} ${TONE_CLASS[jobTone(job.status)]}`} aria-hidden="true">
        $
      </span>
      <span className={CLASS.rowBody}>
        <span className={`${CLASS.rowName} ${CLASS.rowMono}`}>{job.command ?? job.job_id}</span>
        <span className={CLASS.rowMeta}>{jobStatusDisplay(job.status, job.reason)}</span>
      </span>
    </>
  );
  if (onOpen === undefined) return <div className={CLASS.row}>{body}</div>;
  return (
    <button type="button" className={`${CLASS.row} ${CLASS.rowBtn}`} onClick={onOpen}>
      {body}
    </button>
  );
}

export function WatchRow({ watch, now }: { watch: NavigationWatchSummary; now: number }) {
  return (
    <div className={CLASS.row}>
      <span className={`${CLASS.rowGlyph} ${CLASS.glyphQuiet}`}>
        <WatchGlyph className={CLASS.watchGlyph} testId={`sidebar-watch-${watch.id}`} />
      </span>
      <span className={CLASS.rowBody}>
        <span className={CLASS.rowName}>{watchName(watch)}</span>
        <span className={CLASS.rowMeta}>{watchMeta(watch, now)}</span>
      </span>
    </div>
  );
}
