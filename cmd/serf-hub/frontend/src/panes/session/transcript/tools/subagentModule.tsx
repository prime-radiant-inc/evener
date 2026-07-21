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
// deterministic signal, not a race) claims leadership via a lazy useState
// initializer and is the only one that renders the module chrome; every
// other delegate item in the same turn renders nothing further (its own
// one-line summary still shows independently - that's ToolCallItem's
// mandatory summary span, owned by T1, not this body).
//
// Scope decision: only `delegate` (the spawn) and the three follow-up
// calls above ever touch a row. job_list/job_watch do not - they're
// orientation calls over MANY jobs at once, not a check-in on one
// specific child, and correlating an arbitrary listing back to individual
// rows would need far more inference than the wire actually supports.
import { useLayoutEffect, useState } from "react";
import { registerToolRenderer } from "../toolRenderers";
import type { ToolRenderProps } from "../toolRenderers";
import { Button, Chip, type ChipTone } from "../../../../widgets";
import { workspaceStore } from "../../../../shell/workspace";
import { clip, formatToolDuration, parseJSONObject, rememberedArgs, str } from "./helpers";
import {
  claimLeader,
  releaseLeader,
  upsertSubagentRow,
  useSubagentRows,
  type SubagentRow,
  type SubagentRowKind,
} from "./subagentModuleStore";
import type { ItemModel } from "../../../../protocol/model";
import styles from "./subagentmodule.module.css";
import { requireClass } from "../../../../widgets/internal/requireClass";

const TASK_CLIP = 80;
const DONE_VISIBLE_CAP = 6;

const CLASS = {
  module: requireClass(styles.module, "subagentmodule.module.css", "module"),
  header: requireClass(styles.header, "subagentmodule.module.css", "header"),
  rows: requireClass(styles.rows, "subagentmodule.module.css", "rows"),
  row: requireClass(styles.row, "subagentmodule.module.css", "row"),
  task: requireClass(styles.task, "subagentmodule.module.css", "task"),
  meta: requireClass(styles.meta, "subagentmodule.module.css", "meta"),
  preview: requireClass(styles.preview, "subagentmodule.module.css", "preview"),
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
  // "session" stands in for a dedicated "transcript" pane type, which
  // registers in Wave 8 (per the wave-4 plan's own T3 scope note) - opening
  // a session pane against the child's own ref is the closest available
  // affordance today and shares the same underlying ThreadModel/transcript
  // rendering the child would get from a real transcript pane.
  workspaceStore.getState().openPane("session", { ref });
}

function SubagentRowView({ row }: { row: SubagentRow }) {
  const duration = durationLabel(row);
  return (
    <div className={CLASS.row} data-testid="subagent-row" data-kind={row.kind}>
      <Chip tone={KIND_TONE[row.kind]}>{KIND_LABEL[row.kind]}</Chip>
      <span className={CLASS.task}>{row.task}</span>
      {(duration ?? row.resultPreview) && (
        <span className={CLASS.meta}>
          {duration}
          {duration && row.resultPreview ? " · " : ""}
          {row.resultPreview && <span className={CLASS.preview}>{row.resultPreview}</span>}
        </span>
      )}
      {row.transcriptRef && (
        <Button variant="quiet" size="sm" onClick={() => openTranscript(row.transcriptRef!)}>
          Open transcript
        </Button>
      )}
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
  const hiddenKeys = new Set(
    expanded ? [] : doneRows.slice(DONE_VISIBLE_CAP).map((r) => r.rowKey),
  );
  const visibleRows = rows.filter((r) => !hiddenKeys.has(r.rowKey));

  if (rows.length === 0) return null;

  return (
    <div className={CLASS.module} data-testid="subagent-module" data-has-failure={hasFailure ? "true" : "false"}>
      <div className={CLASS.header}>{tally(rows)}</div>
      <div className={CLASS.rows}>
        {visibleRows.map((row) => (
          <SubagentRowView key={row.rowKey} row={row} />
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
  const args = rememberedArgs(item);
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
  const [isLeader] = useState(() => claimLeader(item.turnId, item.id));

  useLayoutEffect(() => {
    const { rowKey, row } = rowFromDelegateItem(item);
    upsertSubagentRow(item.turnId, { rowKey, ...row });
  });

  useLayoutEffect(() => {
    if (!isLeader) return;
    return () => releaseLeader(item.turnId, item.id);
  }, [isLeader, item.turnId, item.id]);

  return isLeader ? <SubagentModule turnId={item.turnId} /> : null;
}

registerToolRenderer({
  match: "delegate",
  summary(item: ItemModel) {
    const args = rememberedArgs(item);
    return `Delegated: ${clip(str(args, "task") ?? "", TASK_CLIP)}`;
  },
  body: DelegateBody,
});
