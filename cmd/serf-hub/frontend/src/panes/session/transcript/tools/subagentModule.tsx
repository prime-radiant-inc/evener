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
import { openBeside } from "../../../../shell/paneActions";
import { threadsStore, useThreadsStore } from "../../../../stores/threads";
import { Button, Chip, type ChipTone } from "../../../../widgets";
import { Disclosure } from "../../../../widgets/disclosure";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { cadenceStateForStatus } from "../../liveness";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { clip, formatToolDuration, parseArgs, parseJSONObject, str } from "./helpers";
import {
  claimLeader,
  releaseLeader,
  type SubagentRow,
  type SubagentRowKind,
  upsertSubagentRow,
  useSubagentRows,
} from "./subagentModuleStore";
import styles from "./subagentmodule.module.css";
import { WatchedChildIndicator } from "./watchedChild";

const TASK_CLIP = 80;
const DONE_VISIBLE_CAP = 6;

const CLASS = {
  module: requireClass(styles.module, "subagentmodule.module.css", "module"),
  header: requireClass(styles.header, "subagentmodule.module.css", "header"),
  rows: requireClass(styles.rows, "subagentmodule.module.css", "rows"),
  row: requireClass(styles.row, "subagentmodule.module.css", "row"),
  summary: requireClass(styles.summary, "subagentmodule.module.css", "summary"),
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

// classifyJobStatus mirrors renderer.js's classifyJobStatus (parity §12):
// note cancelled/stopped land in "done", not "failed". Any status this
// codebase hasn't seen yet (including an absent one, e.g. the delegate
// call settled but the child hasn't reported a status field at all)
// degrades to "running" rather than a confusing/alarming "unknown" - an
// honest "still don't know anything bad happened" default.
export function classifyJobStatus(status: string | undefined): SubagentRowKind {
  if (status === undefined) return "running";
  if (["failed", "errored", "error", "exhausted"].includes(status)) return "failed";
  if (["completed", "done", "cancelled", "stopped", "succeeded"].includes(status)) return "done";
  if (status === "unknown") return "unknown";
  return "running";
}

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
export function rowKindFromChildStatus(type: string): SubagentRowKind {
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

// resolveRowKey prefers delegateId (stable across a delegate's whole
// lifetime, including across several jobs it may run in sequence), then
// jobId (a shell job has no delegateId at all), then a fallback (the
// originating call's own id) so every call still gets SOME row even
// before any id is known. Prefixed per kind so a delegate id can never
// collide with an unrelated job id that happens to share the same raw
// string.
export function resolveRowKey(delegateId: string | undefined, jobId: string | undefined, fallback: string): string {
  if (delegateId) return `dlg:${delegateId}`;
  if (jobId) return `job:${jobId}`;
  return `call:${fallback}`;
}

const KIND_TONE: Record<SubagentRowKind, ChipTone> = {
  running: "alive",
  done: "neutral",
  failed: "danger",
  unknown: "attention",
};

const KIND_LABEL: Record<SubagentRowKind, string> = {
  running: "running",
  done: "done",
  failed: "failed",
  unknown: "unknown",
};

function durationLabel(row: SubagentRow): string | undefined {
  if (!row.startedAt || !row.completedAt) return undefined;
  const ms = new Date(row.completedAt).getTime() - new Date(row.startedAt).getTime();
  return ms >= 0 ? formatToolDuration(ms) : undefined;
}

function openTranscript(ref: string): void {
  // The read-only "transcript" pane is the DISTINCT contextual surface for
  // viewing another thread beside the current one (plan §Ambiguities #1 / PIN-A:
  // reachable via openBeside, never a URL) - not the live SESSION pane (which is
  // the /thread/{ref} single-pane target). openBeside splits it beside the
  // focused pane on desktop and degrades to a plain full-screen open on the
  // mobile StackHost (openBeside's own no-dockview path); re-opening the same
  // child ref just focuses the existing pane (the store's same-params dedup).
  openBeside({ type: "transcript", params: { ref } });
}

// ChildActivityBody is the expanded card's three-layer body (qb8e, tv5k,
// §4.1/§4.2): Mandate (the delegation task), a live Activity feed (the child's
// tool-call purpose/description fields, latest emphasized while the child is
// still running), and Summary (the child's last agentMessage). It opens its
// OWN rich watch (Task 9's { includeTurns: true } upgrade) so the Activity feed
// has the child's turn history, and reads that turn content back out of
// watchedThreads. Mounted only while the card is expanded, so the row-dot's
// lean watch stays lean for the common never-expanded case (§4.2).
function ChildActivityBody({ row, transcriptRef }: { row: SubagentRow; transcriptRef: string }) {
  useEffect(() => {
    threadsStore
      .getState()
      .watchThread(transcriptRef, { includeTurns: true })
      .catch(() => {});
    return () => threadsStore.getState().releaseWatchedThread(transcriptRef);
  }, [transcriptRef]);

  const model = useThreadsStore((s) => s.watchedThreads.get(transcriptRef));
  const items = model ? model.turns.flatMap((t) => t.items) : [];
  const activity = items.filter((it) => it.description);
  const summaryText = items.filter((it) => it.type === "agentMessage").at(-1)?.text;
  const childRunning = model ? rowKindFromChildStatus(model.status.type) === "running" : false;

  return (
    <div className={CLASS.body}>
      <section className={CLASS.section} data-testid="subagent-mandate">
        <div className={CLASS.sectionLabel}>Mandate</div>
        <div className={CLASS.mandate}>{row.task}</div>
      </section>
      {activity.length > 0 && (
        <section className={CLASS.section} data-testid="subagent-activity">
          <div className={CLASS.sectionLabel}>Activity</div>
          <ol className={CLASS.activity}>
            {activity.map((it, idx) => {
              const latest = childRunning && idx === activity.length - 1;
              return (
                <li key={it.id} className={latest ? `${CLASS.activityItem} ${CLASS.activityLatest}` : CLASS.activityItem}>
                  {it.description}
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

function SubagentRowView({ row, turnId }: { row: SubagentRow; turnId: string }) {
  const duration = durationLabel(row);
  // Captured once so the onClick closure below references this narrowed
  // local, not row.transcriptRef re-read through a closure TS can't narrow.
  const transcriptRef = row.transcriptRef;
  // The pill prefers the live-child-status overlay written back by the watch
  // (yd16) over the frozen tool-output kind; falls back to the frozen kind
  // before any live status has arrived.
  const displayKind = row.liveKind ?? row.kind;

  // Collapsed one-liner: status pill + (live cadence while running) + task +
  // duration/preview + the always-available "Open transcript" link. Clicking
  // anywhere here toggles the disclosure except the button, which stops the
  // click from bubbling to the summary's toggle.
  const summary = (
    <span className={CLASS.summary}>
      <Chip tone={KIND_TONE[displayKind]}>{KIND_LABEL[displayKind]}</Chip>
      {/* Live watched-child indicator: only while the row is genuinely
          still running AND we know where to watch (transcriptRef) - a
          done/failed/unknown row has nothing live left to show. It also
          writes the live child status back onto the row as liveKind. */}
      {row.kind === "running" && transcriptRef && (
        <WatchedChildIndicator ref={transcriptRef} turnId={turnId} rowKey={row.rowKey} />
      )}
      <span className={CLASS.task}>{row.task}</span>
      {(duration ?? row.resultPreview) && (
        <span className={CLASS.meta}>
          {duration}
          {duration && row.resultPreview ? " · " : ""}
          {row.resultPreview && <span className={CLASS.preview}>{row.resultPreview}</span>}
        </span>
      )}
      {transcriptRef && (
        <Button
          variant="quiet"
          size="sm"
          onClick={(e) => {
            e.stopPropagation();
            openTranscript(transcriptRef);
          }}
        >
          Open transcript
        </Button>
      )}
    </span>
  );

  return (
    <div className={CLASS.row} data-testid="subagent-row" data-kind={displayKind}>
      {/* id is turn-scoped AND rowKey-scoped so it is both stable across the
          VirtualList/dockview remount (yt2q) and collision-free across turns. */}
      <Disclosure id={`subagent:${turnId}:${row.rowKey}`} summary={summary}>
        {transcriptRef ? (
          <ChildActivityBody row={row} transcriptRef={transcriptRef} />
        ) : (
          <div className={CLASS.body}>
            <section className={CLASS.section} data-testid="subagent-mandate">
              <div className={CLASS.sectionLabel}>Mandate</div>
              <div className={CLASS.mandate}>{row.task}</div>
            </section>
          </div>
        )}
      </Disclosure>
    </div>
  );
}

function tally(rows: SubagentRow[]): string {
  const counts: Record<SubagentRowKind, number> = { running: 0, done: 0, failed: 0, unknown: 0 };
  for (const row of rows) counts[row.kind] += 1;
  const parts: string[] = [];
  (["failed", "unknown", "running", "done"] as const).forEach((kind) => {
    if (counts[kind] > 0) parts.push(`${counts[kind]} ${KIND_LABEL[kind]}`);
  });
  return parts.join(" · ");
}

// SubagentModule is the leader's own rendered chrome: a tally header plus
// every row, folding done-kind rows beyond DONE_VISIBLE_CAP behind a
// "+N more" toggle - running/failed/unknown rows are ALWAYS visible
// regardless of fold state (a live or broken child must never be hidden
// by count, parity §12).
function SubagentModule({ turnId }: { turnId: string }) {
  const rows = useSubagentRows(turnId);
  const [expanded, setExpanded] = useState(false);
  const hasFailure = rows.some((r) => r.kind === "failed");

  const doneRows = rows.filter((r) => r.kind === "done");
  const foldedCount = expanded ? 0 : Math.max(0, doneRows.length - DONE_VISIBLE_CAP);
  const hiddenKeys = new Set(expanded ? [] : doneRows.slice(DONE_VISIBLE_CAP).map((r) => r.rowKey));
  const visibleRows = rows.filter((r) => !hiddenKeys.has(r.rowKey));

  if (rows.length === 0) return null;

  return (
    <div className={CLASS.module} data-testid="subagent-module" data-has-failure={hasFailure ? "true" : "false"}>
      <div className={CLASS.header}>{tally(rows)}</div>
      <div className={CLASS.rows}>
        {visibleRows.map((row) => (
          <SubagentRowView key={row.rowKey} row={row} turnId={turnId} />
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

function rowFromDelegateItem(item: ItemModel): { rowKey: string; row: Omit<SubagentRow, "spawnIndex" | "rowKey"> } {
  const args = parseArgs(item.argumentsJSON);
  const task = clip(str(args, "task") ?? "", TASK_CLIP);
  const parsed = parseJSONObject(item.output);
  const status = parsed ? str(parsed, "status") : undefined;
  const jobId = parsed ? str(parsed, "job_id") : undefined;
  const delegateId = parsed ? str(parsed, "delegate_id") : undefined;
  const transcriptRef = parsed ? str(parsed, "transcript_ref") : undefined;
  const reason = parsed ? str(parsed, "reason") : undefined;
  const rowKey = resolveRowKey(delegateId, jobId, item.callId ?? item.id);
  return {
    rowKey,
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

function DelegateBody({ item }: ToolRenderProps) {
  const [isLeader, setIsLeader] = useState(false);

  useLayoutEffect(() => {
    const { rowKey, row } = rowFromDelegateItem(item);
    upsertSubagentRow(item.turnId, { rowKey, ...row });
  });

  // Claim AND release inside the SAME effect - never a useState lazy
  // initializer, which runs at render time (store setState during render is
  // an impure-render anti-pattern on its own, StrictMode aside). This makes
  // the claim self-healing across StrictMode's dev-only mount -> cleanup ->
  // remount double-invoke: a split where claiming happened once at render
  // but releasing/re-claiming only happened in the effect meant the interim
  // cleanup pass freed the store's leader slot while this component stayed
  // mounted (its own isLeader would never be recomputed to notice) - a
  // later-mounting delegate in the same turn could then also claim the now-
  // vacant slot. Keeping both calls in one effect means the double-invoke's
  // immediately-following remount pass re-runs this exact body and
  // re-claims before anything else gets a chance to. See this file's own
  // StrictMode test for the failure mode this fixes.
  useLayoutEffect(() => {
    const leader = claimLeader(item.turnId, item.id);
    setIsLeader(leader);
    if (!leader) return;
    return () => releaseLeader(item.turnId, item.id);
  }, [item.turnId, item.id]);

  return isLeader ? <SubagentModule turnId={item.turnId} /> : null;
}

registerToolRenderer({
  match: "delegate",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    return `Delegated: ${clip(str(args, "task") ?? "", TASK_CLIP)}`;
  },
  body: DelegateBody,
});
