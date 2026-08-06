// ActivityRowDetail is the inline detail strip a dense activity row reveals
// from its chevron (or ArrowRight): the full command/mandate in mono, a meta
// line with the live or terminal facts the dense row has no room for, and the
// transcript action. Pure presentation - ActivityTree owns the detailID state
// and passes the row plus its ticking `now` straight through.
import type { JSX } from "react";
import { requireClass } from "../../../widgets/internal/requireClass";
import { formatClockTime } from "../transcript/messages/format";
import { OpenTranscriptButton } from "../transcript/openTranscript";
import { formatQuietAge, quietAnchorMillis } from "./activityFormat";
import styles from "./activitypanel.module.css";
import type { ActivityDelegateRow, ActivityJobRow } from "./activityRows";

const CLASS = {
  detailStrip: requireClass(styles.detailStrip, "activitypanel.module.css", "detailStrip"),
  detailCommand: requireClass(styles.detailCommand, "activitypanel.module.css", "detailCommand"),
  detailMeta: requireClass(styles.detailMeta, "activitypanel.module.css", "detailMeta"),
  detailActions: requireClass(styles.detailActions, "activitypanel.module.css", "detailActions"),
};

// detailSubject projects both row kinds onto one shape so the meta line has a
// single code path. A delegate's clock span runs from its first turn's start
// to its latest turn's end, its exit code is the latest turn's, and its
// output is the sum across turns; the quiet anchor is the latest turn (the
// delegate's own timestamps are not part of the wire shape).
interface DetailSubject {
  status: string;
  startedAt?: string;
  endedAt?: string;
  exitCode?: number;
  outputBytes: number;
  quietAnchor?: { lastOutputAt?: string; startedAt: string };
}

function subjectOf(row: ActivityJobRow | ActivityDelegateRow): DetailSubject {
  if (row.kind === "job") {
    const { job } = row;
    const subject: DetailSubject = {
      status: job.status,
      startedAt: job.startedAt,
      outputBytes: job.outputBytes,
      quietAnchor: job,
    };
    if (job.endedAt !== undefined) subject.endedAt = job.endedAt;
    if (job.exitCode !== undefined) subject.exitCode = job.exitCode;
    return subject;
  }
  const { delegate } = row;
  const firstTurn = delegate.turns[0];
  const lastTurn = delegate.turns.at(-1);
  const subject: DetailSubject = {
    // Mirrors ActivityTree's delegateStatusText: latest turn's status, else
    // the child session's aggregate, else "unknown".
    status: lastTurn?.status ?? delegate.child?.aggregate ?? "unknown",
    outputBytes: delegate.turns.reduce((sum, turn) => sum + turn.outputBytes, 0),
  };
  if (firstTurn) subject.startedAt = firstTurn.startedAt;
  if (lastTurn?.endedAt !== undefined) subject.endedAt = lastTurn.endedAt;
  if (lastTurn?.exitCode !== undefined) subject.exitCode = lastTurn.exitCode;
  if (lastTurn) subject.quietAnchor = lastTurn;
  return subject;
}

function parseMillis(value: string | undefined): number | undefined {
  if (!value) return undefined;
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? undefined : parsed;
}

// metaText renders the live contract ("running 12s · 512 output bytes ·
// started 14:58") or the terminal one ("duration 2m · exit 0 · 2048 output
// bytes"). Segments whose inputs are absent drop out instead of printing a
// guess: no "started" without a parseable start, no "exit" without an exit
// code, and a terminal row with no parseable span falls back to its status
// text rather than a fabricated duration.
function metaText(row: ActivityJobRow | ActivityDelegateRow, now: number): string {
  const subject = subjectOf(row);
  const bytes = `${subject.outputBytes} output bytes`;
  if (row.live) {
    const segments: string[] = [
      subject.quietAnchor
        ? `${subject.status} ${formatQuietAge(now - quietAnchorMillis(subject.quietAnchor))}`
        : subject.status,
      bytes,
    ];
    const started = formatClockTime(subject.startedAt);
    if (started) segments.push(`started ${started}`);
    return segments.join(" · ");
  }
  const segments: string[] = [];
  const start = parseMillis(subject.startedAt);
  const end = parseMillis(subject.endedAt);
  if (start !== undefined && end !== undefined) {
    segments.push(`duration ${formatQuietAge(end - start)}`);
  } else {
    segments.push(subject.status);
  }
  if (subject.exitCode !== undefined) segments.push(`exit ${subject.exitCode}`);
  segments.push(bytes);
  return segments.join(" · ");
}

export function ActivityRowDetail({
  row,
  now,
}: {
  row: ActivityJobRow | ActivityDelegateRow;
  now: number;
}): JSX.Element {
  const command =
    row.kind === "job"
      ? (row.job.command ?? row.job.task ?? row.job.description)
      : (row.delegate.mandate ?? row.delegate.child?.label ?? row.delegate.childSessionId);
  // Same rule as ActivityTree's transcriptTarget: a row with no ref gets no
  // transcript action at all (there is deliberately no `job:<id>` fallback).
  const ref = row.transcriptRef?.trim();
  return (
    <div className={CLASS.detailStrip}>
      <code className={CLASS.detailCommand}>{command}</code>
      <span className={CLASS.detailMeta}>{metaText(row, now)}</span>
      {ref && (
        <div className={CLASS.detailActions}>
          <OpenTranscriptButton transcriptRef={ref} parentRef={row.parentRef} />
        </div>
      )}
    </div>
  );
}
