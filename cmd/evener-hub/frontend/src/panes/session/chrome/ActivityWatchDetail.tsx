// ActivityWatchDetail is the live watch detail strip. The Overview activity
// sidebar renders it under its watch rows, and the session's entity map reaches
// the output-target label through the entity-views context.

import type { SessionWatch } from "@evener/appwire-client";
import {
  type ActivityWatchRow,
  findEntityView,
  formatClockTime,
  watchDeliveryInstants,
  watchFacts,
  watchIsScheduled,
} from "@evener/appwire-client";
import { type JSX, useMemo } from "react";
import { useNavigationStore } from "../../../stores/navigation/store";
import { useThreadsStore } from "../../../stores/threads";
import { useEntityViews } from "../../../transcriptDisplay/entityViews";
import { requireClass } from "../../../widgets/internal/requireClass";
import { navigationSummaryFor } from "../threadTitle";
import styles from "./activitypanel.module.css";

const CLASS = {
  detailStrip: requireClass(styles.detailStrip, "activitypanel.module.css", "detailStrip"),
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
const WATCH_NO_SCHEDULE_LINE =
  "There is no schedule to draw here — this one fires when the job or event it watches says so, not when a clock says so.";

// The now marker is taller than a delivery dot, and a dot at the rail's far end
// would land underneath it and read as the marker. Dots therefore never pass
// this percent: the unstretched rail clamps into the reserve, and the stretched
// rail scales its span into it, so the newest delivery always stops just short
// of the rail's end.
const WATCH_TIMELINE_DOT_MAX_PERCENT = 98;

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
function ActivityWatchTimeline({ watch, now }: { watch: SessionWatch; now: number }): JSX.Element | null {
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
  const complete = watch.watch.deliveries <= instants.length;
  const caption = complete ? "Delivered" : `Last ${instants.length} of ${watch.watch.deliveries} deliveries`;
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
      <p
        className={CLASS.timelineCaption}
        data-testid="watch-timeline-caption"
        data-state={complete ? "complete" : "partial"}
      >
        {caption}
      </p>
    </div>
  );
}

// ActivityWatchDetail reveals the full note independently of the compact
// header width, one facts sentence, and - for a clock-driven watch - the
// delivery timeline. A condition watch gets the explanatory line instead:
// there is no period to draw, and the block must not pretend there is.
//
// A named delegate reading its own session is not in its own delegates
// collection, so the viewed session's proven identity arrives as `viewing`
// and names the watch only when that session is the watch's receiver. A
// mismatch keeps the ordinary title precedence and the raw-ref fallback.
//
// The sidebar passes the row's own ticking `now` on every render, so reading
// the clock HERE, rather than in the row, is what keeps a collapsed watch row
// from re-rendering on every tick with identical output. The optional `now` is
// a deterministic seam for direct tests; a caller that omits it gets a fixed
// zero, never a second production clock source.
export interface ViewingSession {
  readonly ref: string;
  readonly delegateName?: string;
}

export function ActivityWatchDetail({
  row,
  now = 0,
  viewing,
}: {
  row: ActivityWatchRow;
  now?: number;
  viewing?: ViewingSession;
}): JSX.Element {
  const { watch } = row;
  const recipientName = useThreadsStore((state) => state.threads.get(watch.receiverRef)?.name);
  const entities = useEntityViews();
  const navigationName = useNavigationStore((state) => navigationSummaryFor(watch.receiverRef, state)?.title);
  const viewingDelegateName =
    viewing !== undefined && viewing.ref === watch.receiverRef ? viewing.delegateName?.trim() : undefined;
  const notifiedDelegateName = useMemo(() => {
    for (const entity of entities?.values() ?? []) {
      if (entity.kind === "delegate" && entity.open.ref === watch.receiverRef) return entity.name?.trim();
    }
    return undefined;
  }, [entities, watch.receiverRef]);
  const targetLabel = useMemo(() => {
    if (!entities || !watch.watch.outputMatch?.trim()) return undefined;
    const target = watch.watch.target?.trim() || watch.watch.source;
    const entity = findEntityView(entities, "job", target, watch.sourceRef);
    return entity?.kind === "job" ? entity.row.job.description?.trim() || undefined : undefined;
  }, [entities, watch.sourceRef, watch.watch.target, watch.watch.source, watch.watch.outputMatch]);
  const note = watch.watch.note?.trim();
  return (
    <div className={CLASS.detailStrip}>
      {note ? (
        <p className={CLASS.watchNote} data-testid="watch-note">
          {note}
        </p>
      ) : null}
      <p className={CLASS.watchFacts} data-testid="watch-facts">
        {watchFacts(watch, now, targetLabel)}
      </p>
      <p className={CLASS.watchFacts}>
        Notifies{" "}
        {recipientName?.trim() ||
          navigationName?.trim() ||
          notifiedDelegateName ||
          viewingDelegateName ||
          watch.receiverRef}
      </p>
      {watchIsScheduled(watch) ? (
        <ActivityWatchTimeline watch={watch} now={now} />
      ) : (
        <p className={CLASS.watchNoSchedule} data-testid="watch-no-schedule">
          {WATCH_NO_SCHEDULE_LINE}
        </p>
      )}
    </div>
  );
}
