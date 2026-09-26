// ActivityRowDetail is the inline detail strip a dense activity row reveals
// from its chevron (or ArrowRight): the full command/mandate in mono, a meta
// line with the live or terminal facts the dense row has no room for, and -
// for shell jobs with output - the tail of the job's log. Pure presentation
// except the one output-tail fetch; ActivityTree owns the detailID state and
// passes the row plus its ticking `now` straight through. The session's entity
// map reaches the delegate line through the entity-views context, because the
// strip renders in the session CHROME - outside the transcript subtree that
// provides the same map - and the delegate line names a real entity.

import type { NavigationWatchSummary } from "@evener/appwire-client";
import {
  type ActivityDelegateRow,
  type ActivityJobRow,
  type ActivityWatchRow,
  activityDelegateDiagnostics,
  activityDelegateState,
  delegateTiming,
  formatClockTime,
  splitMandate,
  watchDeliveryInstants,
  watchFacts,
  watchIsScheduled,
} from "@evener/appwire-client";
import { Fragment, type JSX, useEffect, useMemo, useState } from "react";
import { connectionStore } from "../../../stores/connection";
import { threadsStore } from "../../../stores/threads";
import { parseAnsiLines } from "../../../widgets/codeblock/ansi";
import { AnsiLineContent } from "../../../widgets/codeblock/ansiLine";
import { Disclosure } from "../../../widgets/disclosure";
import { requireClass } from "../../../widgets/internal/requireClass";
import { Markdown } from "../../../widgets/markdown";
import { EntityRef } from "../transcript/EntityRef";
import { formatQuietAge, jobStatusDisplay, quietAnchorMillis } from "./activityFormat";
import styles from "./activitypanel.module.css";
import { useTreeNow } from "./treeNow";

const CLASS = {
  detailStrip: requireClass(styles.detailStrip, "activitypanel.module.css", "detailStrip"),
  detailCommand: requireClass(styles.detailCommand, "activitypanel.module.css", "detailCommand"),
  detailMeta: requireClass(styles.detailMeta, "activitypanel.module.css", "detailMeta"),
  detailOutput: requireClass(styles.detailOutput, "activitypanel.module.css", "detailOutput"),
  watchNote: requireClass(styles.watchNote, "activitypanel.module.css", "watchNote"),
  watchFacts: requireClass(styles.watchFacts, "activitypanel.module.css", "watchFacts"),
  watchNoSchedule: requireClass(styles.watchNoSchedule, "activitypanel.module.css", "watchNoSchedule"),
  watchTimeline: requireClass(styles.watchTimeline, "activitypanel.module.css", "watchTimeline"),
  timelineRail: requireClass(styles.timelineRail, "activitypanel.module.css", "timelineRail"),
  timelineLine: requireClass(styles.timelineLine, "activitypanel.module.css", "timelineLine"),
  timelineDot: requireClass(styles.timelineDot, "activitypanel.module.css", "timelineDot"),
  timelineNow: requireClass(styles.timelineNow, "activitypanel.module.css", "timelineNow"),
  timelineLabels: requireClass(styles.timelineLabels, "activitypanel.module.css", "timelineLabels"),
  timelineCaption: requireClass(styles.timelineCaption, "activitypanel.module.css", "timelineCaption"),
};

// The one line a condition watch shows where a timeline would go: there is no
// period to draw, because the firing is decided by a job's output or an event,
// not by a clock.
export const WATCH_NO_SCHEDULE_LINE =
  "There is no schedule to draw here — this one fires when the job or event it watches says so, not when a clock says so.";

// The row header already prints a watch's note as the row's own name, in a
// sidebar name column that fits roughly 40 characters at its narrow width.
// Repeating the note as the detail's lead paragraph would therefore print every
// row's title twice within a few pixels. The lead paragraph exists only to show
// what that column truncated, so it renders only for notes longer than this
// budget - 48, a little above the ~40-character column so a note that just fits
// the title never duplicates.
export const WATCH_NOTE_LEAD_BUDGET = 48;

// The now marker is taller than a delivery dot, and a dot at the rail's far end
// would land underneath it and read as the marker. Dots therefore never pass
// this percent: the unstretched rail clamps into the reserve, and the stretched
// rail scales its span into it, so the newest delivery always stops just short
// of the rail's end.
export const WATCH_TIMELINE_DOT_MAX_PERCENT = 98;

// Local HH:MM from an epoch instant, through the same clock formatter the rest
// of the detail uses.
function clockFromMillis(millis: number): string {
  return formatClockTime(new Date(millis).toISOString()) ?? "";
}

// Two deliveries can land in the same millisecond and the timeline draws one
// dot per retained instant, so the instant alone is not a unique React key.
// The occurrence number disambiguates duplicates without dropping a real
// delivery; the array index would also work but trips the lint rule for
// order-dependent keys.
function keyedInstants(instants: number[]): Array<{ millis: number; key: string }> {
  const occurrences = new Map<number, number>();
  return instants.map((millis) => {
    const occurrence = (occurrences.get(millis) ?? 0) + 1;
    occurrences.set(millis, occurrence);
    return { millis, key: `${millis}:${occurrence}` };
  });
}

// The delivery timeline: a rail with one dot per retained instant, positioned
// proportionally between the earliest instant and `now`. The right edge
// stretches to the newest retained instant when a browser clock sits behind the
// daemon's instants, so distinct deliveries stay distinct and the now marker
// lands where now actually is. Nothing here implies a drop or a future firing -
// the only instants drawn are the ones the wire actually carried.
function ActivityWatchTimeline({ watch, now }: { watch: NavigationWatchSummary; now: number }): JSX.Element | null {
  // The ring is bounded (32 instants) but this strip re-renders on every tick
  // while its row is open, so parse and sort it once per watch identity instead
  // of on every render. A tick hands this component the same watch object, so
  // the ring is parsed and sorted only when the watch's data actually changes.
  const instants = useMemo(() => watchDeliveryInstants(watch), [watch]);
  if (instants.length === 0) return null;
  const dots = keyedInstants(instants);
  const earliest = instants[0] ?? 0;
  const newest = instants.at(-1) ?? earliest;
  // The rail runs from whichever anchor comes first to whichever comes last.
  // Normally that is earliest -> now. When the browser clock lags the daemon's
  // far enough that EVERY retained instant is still ahead of `now`, it is
  // now -> newest: anchoring the left edge at `now` is what keeps the span
  // positive there, so the marker stays chronologically before the deliveries
  // in its future instead of collapsing onto the left edge with them.
  const left = Math.min(earliest, now);
  const right = Math.max(newest, now);
  const stretched = newest > now;
  const span = right - left;
  // A stretched rail scales its whole span into the headroom, so no two
  // instants can land on the same position; the unstretched rail keeps the
  // clamp, which only ever moves instants inside the reserved last 2%.
  const scale = stretched ? WATCH_TIMELINE_DOT_MAX_PERCENT : 100;
  const offset = (millis: number): number => (span <= 0 ? 0 : ((millis - left) / span) * scale);
  const position = (millis: number): number => {
    return Math.min(WATCH_TIMELINE_DOT_MAX_PERCENT, Math.max(0, offset(millis)));
  };
  // The marker shares the rail's scale: at its far end when `now` is the right
  // edge, and at its proportional spot when a delivery is still ahead of the
  // clock, so it never claims to sit after an instant the clock has not
  // reached. A zero span means the single retained instant IS `now`, where the
  // marker takes the opposite end so the two never draw on one position.
  const nowPosition = span <= 0 ? WATCH_TIMELINE_DOT_MAX_PERCENT : Math.max(0, offset(now));
  // Which end the marker is drawn at is one decision, and the "now" label comes
  // from it: a label on an end the marker is not at would name the wrong instant
  // as the clock. The left end is the marker's when the rail starts at `now`
  // (the clock lags, or an instant lands exactly on it); the right end is its
  // when `now` is the rail's end, including the zero span that draws there. When
  // a delivery is ahead of the clock and another is behind it, the marker sits
  // between the ends and neither one is `now`.
  const markerAtStart = span > 0 && now <= earliest;
  const markerAtEnd = span <= 0 || now >= newest;
  // Each end is labeled with the instant that sits at it.
  const startLabel = markerAtStart ? `now ${clockFromMillis(left)}` : clockFromMillis(left);
  const endLabel = markerAtEnd ? `now ${clockFromMillis(right)}` : clockFromMillis(right);
  // The marks are hidden from assistive tech, so the marker's own instant is
  // named in the label when neither end holds it.
  const floatingNow = !markerAtStart && !markerAtEnd ? `, now ${clockFromMillis(now)}` : "";
  const caption =
    watch.deliveries <= instants.length
      ? "Delivered to this session"
      : `Last ${instants.length} of ${watch.deliveries} deliveries`;
  return (
    <div className={CLASS.watchTimeline} data-testid="watch-timeline">
      <div
        className={CLASS.timelineRail}
        data-testid="watch-timeline-rail"
        role="img"
        aria-label={`${caption}, ${startLabel} to ${endLabel}${floatingNow}`}
      >
        <span className={CLASS.timelineLine} aria-hidden="true" />
        {dots.map(({ millis, key }) => (
          <span
            key={key}
            className={CLASS.timelineDot}
            data-testid="watch-timeline-dot"
            style={{ left: `${position(millis)}%` }}
            aria-hidden="true"
          />
        ))}
        {/* The marker follows the dots in DOM order because these are positioned
            siblings with no z-index: whichever is later paints on top. A dot can
            land exactly on the clock, and the marker is the reference the rail is
            read against, so burying it would make the rail read as having no
            marker at all. The dot is wider than the marker's bar, so it stays
            legible around it. */}
        <span
          className={CLASS.timelineNow}
          data-testid="watch-timeline-now"
          style={{ left: `${nowPosition}%` }}
          aria-hidden="true"
        />
      </div>
      <div className={CLASS.timelineLabels}>
        <span data-testid="watch-timeline-start">{startLabel}</span>
        <span data-testid="watch-timeline-end">{endLabel}</span>
      </div>
      <p className={CLASS.timelineCaption} data-testid="watch-timeline-caption">
        {caption}
      </p>
    </div>
  );
}

function parseMillis(value: string | undefined): number | undefined {
  if (!value) return undefined;
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? undefined : parsed;
}

// detailSubject projects both row kinds onto one shape so the meta line has a
// single code path: the quiet age already measured against the ticking `now`
// (never the snapshot's frozen number, which a quiet subject emits no frames
// to refresh), and the duration the dense row shows for a terminal subject. A
// job measures from its own anchor here; a delegate takes the shared
// delegateTiming rule.
interface DetailSubject {
  status: string;
  reason?: string;
  startedAt?: string;
  exitCode?: number;
  outputBytes: number;
  quietForMs?: number;
  durationMs?: number;
}

function subjectOf(row: ActivityJobRow | ActivityDelegateRow, now: number): DetailSubject {
  if (row.kind === "job") {
    const { job } = row;
    const subject: DetailSubject = {
      status: job.status,
      reason: job.reason,
      startedAt: job.startedAt,
      outputBytes: job.outputBytes,
      quietForMs: now - quietAnchorMillis(job),
    };
    const start = parseMillis(job.startedAt);
    const end = parseMillis(job.endedAt);
    if (start !== undefined && end !== undefined) subject.durationMs = end - start;
    if (job.exitCode !== undefined) subject.exitCode = job.exitCode;
    return subject;
  }
  const { delegate } = row;
  const timing = delegateTiming(delegate, now);
  const subject: DetailSubject = {
    status: activityDelegateState(delegate).status,
    outputBytes: 0,
  };
  if (timing.startedAt !== undefined) subject.startedAt = timing.startedAt;
  if (timing.quietForMs !== undefined) subject.quietForMs = timing.quietForMs;
  if (timing.durationMs !== undefined) subject.durationMs = timing.durationMs;
  return subject;
}

// metaText renders the live contract ("running 12s · 512b ·
// started 14:58") or the terminal one ("exit 1 · 2048b"). The
// terminal line deliberately carries no runtime and no "exit 0": the dense
// row already shows the duration, and a successful exit is the expected
// case - only a non-zero exit code earns a segment. Segments whose inputs
// are absent drop out instead of printing a guess: no "started" without a
// parseable start, and a terminal row with no duration for the dense row to
// show falls back to its status text rather than a fabricated one.
function metaText(row: ActivityJobRow | ActivityDelegateRow, now: number): string {
  const subject = subjectOf(row, now);
  const bytes = `${subject.outputBytes}b`;
  if (row.live) {
    const segments: string[] = [
      subject.quietForMs !== undefined
        ? `${jobStatusDisplay(subject.status, subject.reason)} ${formatQuietAge(subject.quietForMs)}`
        : jobStatusDisplay(subject.status, subject.reason),
      bytes,
    ];
    const started = formatClockTime(subject.startedAt);
    if (started) segments.push(`started ${started}`);
    return segments.join(" · ");
  }
  const segments: string[] = [];
  if (subject.durationMs === undefined) segments.push(jobStatusDisplay(subject.status, subject.reason));
  if (subject.exitCode !== undefined && subject.exitCode !== 0) segments.push(`exit ${subject.exitCode}`);
  segments.push(bytes);
  return segments.join(" · ");
}

// previewBytes bounds the output tail the strip fetches: enough to see what
// the job last said, small enough that expanding a row is never a log dump.
const previewBytes = 256;

// tailText validates the untyped evener/jobs/output data field down to the one
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
  const mandate = delegate?.mandate ?? delegate?.task ?? delegate?.description;
  const command =
    row.kind === "job"
      ? (row.job.command ?? row.job.task ?? row.job.description)
      : mandate
        ? undefined
        : (row.delegate.child?.label ?? row.delegate.childSessionId);
  const mandateParts = splitMandate(mandate);
  const firstParagraph = mandateParts?.first ?? "";
  const remainingMandate = mandateParts?.rest ?? "";
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
      {delegate && (
        <span className={CLASS.detailMeta}>
          Delegate{" "}
          {/* embedded: the strip sits inside the row's own treeitem control, so
              the trigger takes no tab stop of its own (ruling R13); triggerOnly:
              the row already carries its own open control, and this line's words
              stay exactly "Delegate <id> · send · stop · status". */}
          <EntityRef id={delegate.delegateId} embedded triggerOnly />
          {" · send · stop · status"}
        </span>
      )}
      {delegate?.parentWatchGranted && <span className={CLASS.detailMeta}>Watch enabled</span>}
      {delegate &&
        activityDelegateDiagnostics(delegate).map((diagnostic) => (
          <span className={CLASS.detailMeta} key={diagnostic}>
            {diagnostic}
          </span>
        ))}
      {delegate?.warnings?.map((warning) => (
        <span className={CLASS.detailMeta} key={warning}>
          {warning}
        </span>
      ))}
      {row.kind === "job" && row.job.hasOutput && <JobOutputPreview ownerRef={row.parentRef} jobId={row.job.jobId} />}
    </div>
  );
}

// ActivityWatchDetail is a watch row's expanded block: the note once more only
// when the row title's name column could not show it in full, one facts
// sentence, and - for a clock-driven watch with retained instants - the
// delivery timeline. A condition watch gets the explanatory line instead:
// there is no period to draw, and the block must not pretend there is.
//
// It is the one watch surface that reads the tree clock, and in production the
// tick has exactly one source: TreeNowContext. ActivityTree renders
// <ActivityWatchDetail row={row} /> with no clock prop, so this strip never
// follows the chrome's clock. The optional `now` is a deterministic seam for
// direct tests, not a second production clock source. Reading the clock HERE,
// rather than in the row, is what keeps a collapsed watch row from re-rendering
// on every tick with identical output.
export function ActivityWatchDetail({ row, now }: { row: ActivityWatchRow; now?: number }): JSX.Element {
  const { watch } = row;
  const contextNow = useTreeNow();
  const effectiveNow = now ?? contextNow;
  const note = watch.note?.trim();
  const leadNote = note !== undefined && note.length > WATCH_NOTE_LEAD_BUDGET ? note : undefined;
  return (
    <div className={CLASS.detailStrip}>
      {leadNote ? (
        <p className={CLASS.watchNote} data-testid="watch-note">
          {leadNote}
        </p>
      ) : null}
      <p className={CLASS.watchFacts} data-testid="watch-facts">
        {watchFacts(watch, effectiveNow)}
      </p>
      {watchIsScheduled(watch) ? (
        <ActivityWatchTimeline watch={watch} now={effectiveNow} />
      ) : (
        <p className={CLASS.watchNoSchedule} data-testid="watch-no-schedule">
          {WATCH_NO_SCHEDULE_LINE}
        </p>
      )}
    </div>
  );
}
