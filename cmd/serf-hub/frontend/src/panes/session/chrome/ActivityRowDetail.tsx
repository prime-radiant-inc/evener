// ActivityRowDetail is the inline detail strip a dense activity row reveals
// from its chevron (or ArrowRight): the full command/mandate in mono, a meta
// line with the live or terminal facts the dense row has no room for, and -
// for shell jobs with output - the tail of the job's log. Pure presentation
// except the one output-tail fetch; ActivityTree owns the detailID state and
// passes the row plus its ticking `now` straight through.
import { Fragment, type JSX, useEffect, useState } from "react";
import { connectionStore } from "../../../stores/connection";
import { threadsStore } from "../../../stores/threads";
import { parseAnsiLines } from "../../../widgets/codeblock/ansi";
import { AnsiLineContent } from "../../../widgets/codeblock/ansiLine";
import { Disclosure } from "../../../widgets/disclosure";
import { requireClass } from "../../../widgets/internal/requireClass";
import { Markdown } from "../../../widgets/markdown";
import { formatClockTime } from "../transcript/messages/format";
import { formatQuietAge, quietAnchorMillis } from "./activityFormat";
import styles from "./activitypanel.module.css";
import type { ActivityDelegateRow, ActivityJobRow } from "./activityRows";

const CLASS = {
  detailStrip: requireClass(styles.detailStrip, "activitypanel.module.css", "detailStrip"),
  detailCommand: requireClass(styles.detailCommand, "activitypanel.module.css", "detailCommand"),
  detailMeta: requireClass(styles.detailMeta, "activitypanel.module.css", "detailMeta"),
  detailOutput: requireClass(styles.detailOutput, "activitypanel.module.css", "detailOutput"),
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

// metaText renders the live contract ("running 12s · 512b ·
// started 14:58") or the terminal one ("exit 1 · 2048b"). The
// terminal line deliberately carries no runtime and no "exit 0": the dense
// row already shows the duration, and a successful exit is the expected
// case - only a non-zero exit code earns a segment. Segments whose inputs
// are absent drop out instead of printing a guess: no "started" without a
// parseable start, and a terminal row with no parseable span falls back to
// its status text rather than a fabricated duration.
function metaText(row: ActivityJobRow | ActivityDelegateRow, now: number): string {
  const subject = subjectOf(row);
  const bytes = `${subject.outputBytes}b`;
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
  if (start === undefined || end === undefined) {
    segments.push(subject.status);
  }
  if (subject.exitCode !== undefined && subject.exitCode !== 0) segments.push(`exit ${subject.exitCode}`);
  segments.push(bytes);
  return segments.join(" · ");
}

// previewBytes bounds the output tail the strip fetches: enough to see what
// the job last said, small enough that expanding a row is never a log dump.
const previewBytes = 256;

// tailText validates the untyped serf/jobs/output data field down to the one
// member the preview needs (same wire-shape caution as JobLog's own parser).
function tailText(data: unknown): string | null {
  if (typeof data !== "object" || data === null) return null;
  const tail = (data as Record<string, unknown>).tail;
  return typeof tail === "string" ? tail : null;
}

// JobOutputPreview shows the latest bytes of a shell job's log inside the
// detail strip. It stays silent on every failure mode - a missing log, an
// old daemon, a dropped connection - because the strip's meta line already
// carries the facts; the preview is a convenience, never an error surface.
function JobOutputPreview({ ownerRef, jobId }: { ownerRef: string; jobId: string }): JSX.Element | null {
  const [tail, setTail] = useState<string | null>(null);
  useEffect(() => {
    let cancelled = false;
    let started = false;
    // Deferred until the one client is actually ready - the same handshake
    // race JobLog's own effect defers through.
    const start = () => {
      if (started || connectionStore.getState().state !== "ready") return;
      started = true;
      threadsStore
        .getState()
        .jobOutput(ownerRef, jobId, undefined, previewBytes)
        .then(
          (data) => {
            if (cancelled) return;
            const text = tailText(data);
            setTail(text !== null && text.length > 0 ? text : null);
          },
          () => {
            if (!cancelled) setTail(null);
          },
        );
    };
    start();
    const unsubscribe = connectionStore.subscribe(start);
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [ownerRef, jobId]);
  if (tail === null) return null;
  // Job output is terminal text: render it through the codeblock ANSI
  // pipeline so escape sequences become styled runs instead of literal
  // "[2m" noise (same treatment CodeBlock gives tool output).
  const lines = parseAnsiLines(tail);
  return (
    <pre className={CLASS.detailOutput}>
      {lines.map((line, index) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: the parsed lines are a static split of one fetched tail, never reordered
        <Fragment key={index}>
          {index > 0 ? "\n" : null}
          <AnsiLineContent line={line} />
        </Fragment>
      ))}
    </pre>
  );
}

export function ActivityRowDetail({
  row,
  now,
}: {
  row: ActivityJobRow | ActivityDelegateRow;
  now: number;
}): JSX.Element {
  const delegate = row.kind === "delegate" ? row.delegate : undefined;
  const mandate = delegate?.mandate;
  const command =
    row.kind === "job"
      ? (row.job.command ?? row.job.task ?? row.job.description)
      : mandate
        ? undefined
        : (row.delegate.child?.label ?? row.delegate.childSessionId);
  const paragraphs = mandate?.split(/\n\s*\n/) ?? [];
  const firstParagraph = paragraphs[0] ?? "";
  const remainingMandate = paragraphs.slice(1).join("\n\n");
  return (
    <div className={CLASS.detailStrip}>
      {delegate && mandate ? (
        <div className={CLASS.detailCommand}>
          <Markdown source={firstParagraph} />
          {remainingMandate && (
            <Disclosure id={`delegate-mandate-${delegate?.delegateId ?? "unknown"}`} summary="Show more">
              <Markdown source={remainingMandate} />
            </Disclosure>
          )}
        </div>
      ) : (
        <code className={CLASS.detailCommand}>{command}</code>
      )}
      <span className={CLASS.detailMeta}>{metaText(row, now)}</span>
      {row.kind === "job" && row.job.hasOutput && <JobOutputPreview ownerRef={row.parentRef} jobId={row.job.jobId} />}
    </div>
  );
}
