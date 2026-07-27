// The `delegate` descriptor (parity checklist §2's delegateRenderer) and
// the subagent module it feeds - "one aggregated block per turn collecting
// that turn's job_* activity" per the wave-4 plan.
//
// Design rationale (see subagentModuleStore.ts's own header for the full
// interface-boundary reasoning): ToolRenderProps is {item, live} only, so
// a single tool call's body cannot see its sibling items to build a
// shared block by itself. Every delegate item computes its OWN row
// (spawned/running/done/failed + duration + result preview, all derived
// from that ONE item's own output/args) and upserts it into
// subagentModuleStore keyed by (turnId, rowKey); job_status/job_stop/
// delegate_send do the same via updateSubagentRowIfExists (jobTools.tsx),
// UPDATING an existing row rather than spawning one, mirroring the legacy
// reconcileSubagent's identical rule. Whichever delegate item is first to
// mount within a turn (VirtualList windows a whole TurnBlock as one row -
// see TurnBlock.tsx's own file - so a turn's items always mount/unmount
// together, making "first to render, in wire/array order" a real,
// deterministic signal, not a race) claims leadership inside a
// useLayoutEffect (symmetric with its own release - see DelegateBody's own
// comment for why claim and release must live in the same effect) and is
// the only one that renders the module chrome; every other delegate item in
// the same turn renders nothing further (its own one-line summary still
// shows independently - that's ToolCallItem's mandatory summary span, owned
// by T1, not this body).
//
// Scope decision: only `delegate` (the spawn) and the three follow-up
// calls above ever touch a row. job_list/job_watch do not - they're
// orientation calls over MANY jobs at once, not a check-in on one
// specific child, and correlating an arbitrary listing back to individual
// rows would need far more inference than the wire actually supports.
import { useEffect, useLayoutEffect, useState } from "react";
import type { ItemModel } from "../../../../protocol/model";
import { threadsStore, useThreadsStore } from "../../../../stores/threads";
import { Button, type CadenceState, StatusDot } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { cadenceStateForStatus } from "../../liveness";
import { OpenTranscriptButton } from "../openTranscript";
import { statedPurposeOf } from "../ToolRow";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { formatToolDuration, parseArgs, parseJSONObject, str } from "./helpers";
import {
  claimLeader,
  classifyJobStatus,
  effectiveRowKind,
  releaseLeader,
  resolveRowKey,
  type SubagentRow,
  type SubagentRowKind,
  setWatchedLiveKind,
  turnScopeKey,
  upsertSubagentRow,
  useLeader,
  useSubagentRows,
} from "./subagentModuleStore";
import styles from "./subagentmodule.module.css";

const DONE_VISIBLE_CAP = 6;
// mhcf: "the ~5 most recent tool-call rationales" (product ask) - see
// ChildActivityBody's own comment for the cap+ordering reasoning.
const RECENT_ACTIVITY_CAP = 5;

const CLASS = {
  module: requireClass(styles.module, "subagentmodule.module.css", "module"),
  header: requireClass(styles.header, "subagentmodule.module.css", "header"),
  rows: requireClass(styles.rows, "subagentmodule.module.css", "rows"),
  row: requireClass(styles.row, "subagentmodule.module.css", "row"),
  summary: requireClass(styles.summary, "subagentmodule.module.css", "summary"),
  status: requireClass(styles.status, "subagentmodule.module.css", "status"),
  task: requireClass(styles.task, "subagentmodule.module.css", "task"),
  meta: requireClass(styles.meta, "subagentmodule.module.css", "meta"),
  preview: requireClass(styles.preview, "subagentmodule.module.css", "preview"),
  body: requireClass(styles.body, "subagentmodule.module.css", "body"),
  section: requireClass(styles.section, "subagentmodule.module.css", "section"),
  sectionLabel: requireClass(styles.sectionLabel, "subagentmodule.module.css", "sectionLabel"),
  mandate: requireClass(styles.mandate, "subagentmodule.module.css", "mandate"),
  activity: requireClass(styles.activity, "subagentmodule.module.css", "activity"),
  activityItem: requireClass(styles.activityItem, "subagentmodule.module.css", "activityItem"),
  activityLatest: requireClass(styles.activityLatest, "subagentmodule.module.css", "activityLatest"),
  summaryText: requireClass(styles.summaryText, "subagentmodule.module.css", "summaryText"),
};

// classifyJobStatus and resolveRowKey now live in subagentModuleStore.ts (dr7e):
// applySerfJobStarted/applySerfJobFinished there need the exact same
// classification/keying a delegate item's own frozen tool output uses, and
// subagentModuleStore.ts is the layer stores/threads.ts is allowed to import
// (a plain data store, no React/UI deps) - subagentModule.tsx itself pulls in
// Button/CSS modules a core store must never transitively
// bundle. Re-exported here unchanged so every existing import site (this
// file's own uses below, subagentModule.test.tsx) keeps working.
export { classifyJobStatus, resolveRowKey } from "./subagentModuleStore";

// rowKindFromChildStatus maps the child's LIVE thread status onto a row kind
// for the status pill (yd16). model.status.type is the WIRE thread-status
// vocabulary (active/closed/systemError/awaiting/warning/idle/notLoaded), NOT
// the job-status words classifyJobStatus reads - feeding thread-status to
// classifyJobStatus misclassifies ("closed"->"running", "systemError"->
// "running"). So this reuses cadenceStateForStatus (the one canonical wire-
// status interpreter) and adapts its CadenceState to a SubagentRowKind: a
// failed child is "failed", an ended (closed) child is "done", and everything
// still live from the parent's view (working / needs-you / idle) stays
// "running".
//
// g5kf (the honest-clock bug): "notLoaded" must be carved out BEFORE that
// collapse, not after. cadenceStateForStatus deliberately folds notLoaded
// into the same "idle" family a genuinely-idle-but-still-live child gets -
// right for the Cadence dot, which only needs "nothing to animate" either
// way (liveness.ts's own doc comment, cross-checked by its test) - but wrong
// here: notLoaded means the child left the daemon's live roster entirely
// (evicted, orphaned, or lost to a hub restart - cmd/serf-hub/
// app_threadread.go's pastEntryThread stamps it), not "still going". Once
// cadenceStateForStatus has already collapsed it to "idle" this function has
// no way to tell the two apart, and a delegate call frozen at
// status:"running" by a foreground_timeout (agent/job_delegate.go's mainline
// path for any non-trivial delegate, not an edge case) would then read as
// "running" forever with no path back to honesty - this was the only
// remaining liveness check once the child stops reporting. Composer.tsx and
// StatusRow.tsx each already keep their own separate notLoaded check
// alongside cadenceStateForStatus for the identical reason; this follows the
// same, already-established pattern rather than teaching
// cadenceStateForStatus itself a state its other two callers don't want.
export function rowKindFromChildStatus(type: string): SubagentRowKind {
  if (type === "notLoaded") return "unknown";
  const state = cadenceStateForStatus(type);
  if (state === "failed") return "failed";
  if (state === "ended") return "done";
  return "running";
}

// KNOWN_JOB_STATUSES mirrors the status enum job_list's own tool schema
// declares (agent/internal/tool/definitions.go's DefJobList: "running",
// "completed", "failed", "exhausted", "cancelled", "stopped"). Used by
// statusWordFromText below to read a status back out of a plain-text
// footer (job_stop/delegate_send) whose field ORDER isn't fixed - see
// that function's own comment.
const KNOWN_JOB_STATUSES = ["completed", "failed", "cancelled", "stopped", "exhausted", "running"] as const;

// statusWordFromText finds a known status word anywhere in `text` (a
// job_stop/delegate_send footer), rather than splitting by position: both
// formatJobStop's and formatDelegateSend's footer fields are each
// conditionally present (agent/session_tools_jobs.go - verified directly,
// e.g. delegate_send's footer omits `status` entirely when the field was
// empty), so a fixed field index would silently read the wrong segment
// whenever an earlier field is missing. A word-boundary search is robust
// to that reordering/omission.
export function statusWordFromText(text: string): string | undefined {
  for (const status of KNOWN_JOB_STATUSES) {
    if (new RegExp(`\\b${status}\\b`).test(text)) return status;
  }
  return undefined;
}

const KIND_LABEL: Record<SubagentRowKind, string> = {
  running: "running",
  done: "done",
  stopped: "stopped",
  failed: "failed",
  unknown: "unknown",
};

const KIND_STATE: Record<SubagentRowKind, CadenceState> = {
  running: "working",
  done: "ended",
  stopped: "ended",
  failed: "failed",
  unknown: "needs-you",
};

function durationLabel(row: SubagentRow): string | undefined {
  if (!row.startedAt || !row.completedAt) return undefined;
  const ms = new Date(row.completedAt).getTime() - new Date(row.startedAt).getTime();
  return ms >= 0 ? formatToolDuration(ms) : undefined;
}

// ChildActivityBody is the expanded card's three-layer body (qb8e, tv5k,
// §4.1/§4.2): Mandate (the delegation task), a live Activity feed (the child's
// tool-call purpose/description fields, capped and ordered per mhcf below),
// and Summary (the child's last agentMessage). It opens its OWN rich watch
// (Task 9's { includeTurns: true } upgrade) so the Activity feed has the
// child's turn history, and reads that turn content back out of
// watchedThreads. Mounted only while the card is expanded, so the row-dot's
// lean watch stays lean for the common never-expanded case (§4.2). "Always-
// current" is satisfied by that same rich read: it carries subscribe:true, so
// the feed is current the instant the card opens and stays live afterward -
// no further watch work needed (mhcf: this is expanded-only by design, not an
// oversight - a collapsed row's status pill/preview is a different, cheaper
// surface entirely).
//
// mhcf: the feed used to render EVERY purpose-bearing item since the child's
// first turn, oldest-first, unbounded - a long-running child produces dozens
// or hundreds of lines, and the live-step emphasis (idx === length-1) sat at
// the BOTTOM of that list: the right idea, at the least reachable position in
// it. Fixed two ways: (1) capped to RECENT_ACTIVITY_CAP, since a reader doesn't
// need the full inline history when "Open transcript" already exists for
// exactly that (7f7c's same reasoning for the Mandate's full task text vs. the
// row's clipped one); (2) rendered newest-first within that window, so the
// live step is reachable with zero scrolling regardless of section height -
// oldest-first would still land it at the visual bottom, just of a shorter
// list. Each <li>'s `value` is its TRUE 1-based ordinal into the full history,
// not its position in the truncated/reversed window, so list-style:decimal
// reads e.g. "43." rather than relabeling the 43rd step "1." merely because it
// renders first - which would understate how much the child has actually done.
// JobDetailSection surfaces the exhaustion/resumable detail a
// serf/job/finished notification carries that no other UI shows (dr7e) -
// reason is already visible in the collapsed one-liner via row.resultPreview/
// liveReason, so it isn't repeated here. Renders nothing once neither field
// is present (most rows: shell jobs and any delegate that finished cleanly).
function JobDetailSection({ row }: { row: SubagentRow }) {
  if (row.resumable === undefined && row.exhaustionBudget === undefined && row.exhaustionLimit === undefined) {
    return null;
  }
  const exhaustion =
    row.exhaustionBudget !== undefined || row.exhaustionLimit !== undefined
      ? `${row.exhaustionBudget ?? "?"} of ${row.exhaustionLimit ?? "?"}`
      : undefined;
  return (
    <section className={CLASS.section} data-testid="subagent-job-detail">
      <div className={CLASS.sectionLabel}>Job</div>
      <div className={CLASS.mandate}>
        {exhaustion && <div>Exhaustion budget: {exhaustion}</div>}
        {row.resumable !== undefined && <div>{row.resumable ? "Resumable" : "Not resumable"}</div>}
      </div>
    </section>
  );
}

function ChildActivityBody({
  row,
  transcriptRef,
  showMandate,
  scopeKey,
}: {
  row: SubagentRow;
  transcriptRef?: string;
  showMandate: boolean;
  scopeKey: string;
}) {
  useEffect(() => {
    if (transcriptRef === undefined) return;
    threadsStore
      .getState()
      .watchThread(transcriptRef, { includeTurns: true })
      .catch(() => {});
    return () => threadsStore.getState().releaseWatchedThread(transcriptRef);
  }, [transcriptRef]);

  const model = useThreadsStore((s) => (transcriptRef !== undefined ? s.watchedThreads.get(transcriptRef) : undefined));
  const liveKind = model ? rowKindFromChildStatus(model.status.type) : undefined;
  useEffect(() => {
    if (liveKind && transcriptRef) {
      setWatchedLiveKind(scopeKey, row.rowKey, liveKind);
    }
  }, [scopeKey, row.rowKey, liveKind, transcriptRef]);
  const items = model ? model.turns.flatMap((t) => t.items) : [];
  // Same "does this item state a purpose" rule the main transcript's tool row
  // uses (ToolRow.tsx's statedPurposeOf) - the PRESENTATION differs deliberately
  // (a numbered feed of a child's steps here, a leading line on the row there),
  // but a whitespace-only description must not be a step in one and nothing in
  // the other.
  const activity = items.flatMap((it) => {
    const purpose = statedPurposeOf(it);
    return purpose === undefined ? [] : [{ id: it.id, purpose }];
  });
  // The RECENT_ACTIVITY_CAP most recent items, newest-first, each keeping its
  // TRUE 1-based ordinal into the full (oldest-first) `activity` array - see
  // this function's own header comment for why both the cap and the reversal
  // exist, and why the ordinal travels with the item rather than being
  // re-derived from its position in this windowed/reversed copy.
  const windowStart = Math.max(0, activity.length - RECENT_ACTIVITY_CAP);
  const recentActivity = activity
    .slice(windowStart)
    .map((it, i) => ({ ...it, ordinal: windowStart + i + 1 }))
    .reverse();
  const summaryText = items.filter((it) => it.type === "agentMessage").at(-1)?.text;
  const childRunning = model ? rowKindFromChildStatus(model.status.type) === "running" : false;

  const details = (
    <>
      {showMandate && (
        <section className={CLASS.section} data-testid="subagent-mandate">
          <div className={CLASS.sectionLabel}>Mandate</div>
          <div className={CLASS.mandate}>{row.task}</div>
        </section>
      )}
      <JobDetailSection row={row} />
    </>
  );

  return (
    <div className={CLASS.body}>
      {details}
      {activity.length > 0 && (
        <section className={CLASS.section} data-testid="subagent-activity">
          <div className={CLASS.sectionLabel}>Activity</div>
          <ol className={CLASS.activity}>
            {recentActivity.map((it) => {
              // "latest" is the chronologically LAST item (ordinal ===
              // activity.length), independent of where it renders within the
              // newest-first window above.
              const latest = childRunning && it.ordinal === activity.length;
              return (
                <li
                  key={it.id}
                  value={it.ordinal}
                  className={latest ? `${CLASS.activityItem} ${CLASS.activityLatest}` : CLASS.activityItem}
                >
                  {it.purpose}
                </li>
              );
            })}
          </ol>
        </section>
      )}
      {summaryText && (
        <section className={CLASS.section} data-testid="subagent-summary">
          <div className={CLASS.sectionLabel}>Summary</div>
          <div className={CLASS.summaryText}>{summaryText}</div>
        </section>
      )}
    </div>
  );
}

function SubagentRowView({
  row,
  turnId,
  sessionRef,
  showSummary,
}: {
  row: SubagentRow;
  turnId: string;
  sessionRef: string | undefined;
  showSummary: boolean;
}) {
  const duration = durationLabel(row);
  // Captured once so the onClick closure below references this narrowed
  // local, not row.transcriptRef re-read through a closure TS can't narrow.
  const transcriptRef = row.transcriptRef;
  // The pill prefers the live-child-status overlay written back by the watch
  // (yd16) over the frozen tool-output kind; falls back to the frozen kind
  // before any live status has arrived. Shared with sortedRows' own sort key
  // (subagentModuleStore.ts, kata hzq9) so rendering and ordering never
  // disagree about which kind a row is currently showing.
  const displayKind = effectiveRowKind(row);
  // Prefers the serf/job/finished notification's own reason (dr7e) - richer
  // and arrives independent of the delegate item's frozen tool output - over
  // the frozen output's own reason.
  const preview = row.liveReason ?? row.resultPreview;
  const openTranscriptButton = transcriptRef ? (
    <OpenTranscriptButton transcriptRef={transcriptRef} parentRef={sessionRef} />
  ) : null;

  // Collapsed one-liner: status dot + (live cadence while running) + task +
  // duration/preview + the always-available "Open transcript" link.
  const summary = (
    <span className={CLASS.summary}>
      {showSummary && (
        <span className={CLASS.status} title={KIND_LABEL[displayKind]}>
          <StatusDot state={KIND_STATE[displayKind]} />
        </span>
      )}
      {showSummary && <span className={CLASS.task}>{row.task}</span>}
      {(duration ?? preview) && (
        <span className={CLASS.meta}>
          {duration}
          {duration && preview ? " · " : ""}
          {preview && <span className={CLASS.preview}>{preview}</span>}
        </span>
      )}
      {openTranscriptButton}
    </span>
  );

  return (
    <div className={CLASS.row} data-testid="subagent-row" data-kind={displayKind}>
      {/* id is session-and-turn-scoped AND rowKey-scoped so it is both stable
          across the VirtualList/dockview remount (yt2q) and collision-free
          across turns AND sessions (78nj) - turnScopeKey, not a bare turnId,
          for the same reason every other store key in this file already
          uses it (kata 8525): turn ids restart per session, and row.rowKey's
          own last-resort fallback (call:${item.id}) is per-session
          sequential, so two sessions can otherwise land on the identical id
          and silently share one open/closed boolean. */}
      <div>
        {summary}
        <ChildActivityBody
          row={row}
          transcriptRef={transcriptRef}
          scopeKey={turnScopeKey(sessionRef, turnId)}
          showMandate={!showSummary}
        />
      </div>
    </div>
  );
}

function tally(rows: SubagentRow[]): string {
  const counts: Record<SubagentRowKind, number> = { running: 0, done: 0, stopped: 0, failed: 0, unknown: 0 };
  for (const row of rows) counts[effectiveRowKind(row)] += 1;
  const parts: string[] = [];
  // stopped is never counted among "done"'s successes (3zf8) - it gets its
  // own tally segment, ordered with the same worst-first sense as everything
  // else here (a deliberate stop is more worth a glance than a clean finish).
  (["failed", "unknown", "running", "stopped", "done"] as const).forEach((kind) => {
    if (counts[kind] > 0) parts.push(`${counts[kind]} ${KIND_LABEL[kind]}`);
  });
  return parts.join(" · ");
}

// SubagentModule is the leader's own rendered chrome: a tally header plus
// every row, folding done-kind rows beyond DONE_VISIBLE_CAP behind a
// "+N more" toggle - running/failed/unknown rows are ALWAYS visible
// regardless of fold state (a live or broken child must never be hidden
// by count, parity §12).
function SubagentModule({ turnId, sessionRef }: { turnId: string; sessionRef: string | undefined }) {
  const rows = useSubagentRows(turnScopeKey(sessionRef, turnId));
  const [expanded, setExpanded] = useState(false);
  const hasFailure = rows.some((r) => effectiveRowKind(r) === "failed");
  const showSummary = rows.length > 1;

  const doneRows = rows.filter((r) => effectiveRowKind(r) === "done");
  const foldedCount = expanded ? 0 : Math.max(0, doneRows.length - DONE_VISIBLE_CAP);
  const hiddenKeys = new Set(expanded ? [] : doneRows.slice(DONE_VISIBLE_CAP).map((r) => r.rowKey));
  const visibleRows = rows.filter((r) => !hiddenKeys.has(r.rowKey));

  if (rows.length === 0) return null;

  return (
    <div className={CLASS.module} data-testid="subagent-module" data-has-failure={hasFailure ? "true" : "false"}>
      {showSummary && <div className={CLASS.header}>{tally(rows)}</div>}
      <div className={CLASS.rows}>
        {visibleRows.map((row) => (
          <SubagentRowView
            key={row.rowKey}
            row={row}
            turnId={turnId}
            sessionRef={sessionRef}
            showSummary={showSummary}
          />
        ))}
      </div>
      {foldedCount > 0 && (
        <Button variant="quiet" size="sm" onClick={() => setExpanded(true)}>
          +{foldedCount} more
        </Button>
      )}
      {expanded && doneRows.length > DONE_VISIBLE_CAP && (
        <Button variant="quiet" size="sm" onClick={() => setExpanded(false)}>
          Collapse
        </Button>
      )}
    </div>
  );
}

export function rowFromDelegateItem(item: ItemModel): {
  rowKey: string;
  migrateFromRowKey?: string;
  row: Omit<SubagentRow, "spawnIndex" | "rowKey">;
} {
  const args = parseArgs(item.argumentsJSON);
  // Full, unclipped task text - it is the entire specification of what the
  // delegate was asked to do (7f7c). Callers that need a one-line preview
  // (SubagentRowView's collapsed summary) clip at their own render site;
  // clipping here would also clip the Mandate section, the one place a
  // reader can go to read the rest.
  const task = str(args, "task") ?? "";
  const parsed = parseJSONObject(item.output);
  const status = parsed ? str(parsed, "status") : undefined;
  const jobId = parsed ? str(parsed, "job_id") : undefined;
  const delegateId = parsed ? str(parsed, "delegate_id") : undefined;
  const transcriptRef = parsed ? str(parsed, "transcript_ref") : undefined;
  const reason = parsed ? str(parsed, "reason") : undefined;
  const fallbackRowKey = resolveRowKey(undefined, undefined, item.callId ?? item.id);
  const rowKey = resolveRowKey(delegateId, jobId, item.callId ?? item.id);
  return {
    rowKey,
    migrateFromRowKey: rowKey === fallbackRowKey ? undefined : fallbackRowKey,
    row: {
      kind: classifyJobStatus(status),
      task,
      jobId,
      transcriptRef,
      startedAt: item.startedAt,
      completedAt: item.completedAt,
      resultPreview: reason ?? "",
    },
  };
}

function DelegateBody({ item, sessionRef }: ToolRenderProps) {
  const scopeKey = turnScopeKey(sessionRef, item.turnId);
  const leaderId = useLeader(scopeKey);
  const isLeader = leaderId === item.id;

  useLayoutEffect(() => {
    const { rowKey, migrateFromRowKey, row } = rowFromDelegateItem(item);
    upsertSubagentRow(scopeKey, { rowKey, ...row }, migrateFromRowKey);
  }, [scopeKey, item]);

  // The reactive leader read lets a mounted follower retry after the current
  // leader unmounts. The effect only claims an empty slot and releases it
  // from the elected component's cleanup, keeping StrictMode's mount/cleanup
  // cycle symmetric without making followers poll or render nested details.
  useLayoutEffect(() => {
    if (leaderId === undefined) claimLeader(scopeKey, item.id);
  }, [scopeKey, item.id, leaderId]);
  useLayoutEffect(() => () => releaseLeader(scopeKey, item.id), [scopeKey, item.id]);

  return isLeader ? <SubagentModule turnId={item.turnId} sessionRef={sessionRef} /> : null;
}

registerToolRenderer({
  match: "delegate",
  summary(item: ItemModel) {
    return item.description ?? "";
  },
  body: DelegateBody,
  // A delegate call is a status card, not a fold-to-open tool row - the same
  // reasoning as task_list's own `autoExpand: () => true`. Left collapsed by
  // default, the rich module body and its activity watch never mount until
  // opened: the ToolCallItem-owned lean watch still keeps the top-level dot
  // current while collapsed (evch). Opening it at settle makes the
  // tally/status/live-cadence/result visible without a click; a manual
  // collapse afterward still sticks (ToolCallItem's own autoDefault vs.
  // store-backed toggle).
  autoExpand: () => true,
});
