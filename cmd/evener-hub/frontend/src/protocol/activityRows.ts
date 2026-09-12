// Pure row-model builder for the dense activity tree: walks a parsed
// ActivityTree and returns the flat list of rows to render. Live entries keep
// their original order; terminal entries collapse behind one fold row per
// parent session (revealed in original order when the fold is expanded).
// Sessions never become rows — the panel header covers the root and a delegate
// row stands in for its child session.

import { watchCadenceLabel, watchDurationLabel } from "../shell/rail/RailRow";
import {
  type ActivityDelegate,
  type ActivityEntry,
  type ActivityJob,
  type ActivitySessionNode,
  type ActivityTree,
  activityNodeID,
  delegateHasActiveWork,
  isActivityFailure,
  isFailedDelegateOutcome,
  isFailedJobOutcome,
  isTurnContainer,
} from "./activityData";
import { formatClockTime } from "./displayFormat";
import { stableDelegateDisplayStatus } from "./stableDelegate";
import type { NavigationWatchSummary } from "./types.gen";

export interface ActivityRowBase {
  id: string;
  parentID?: string;
  level: number;
}

export interface ActivityJobRow extends ActivityRowBase {
  kind: "job";
  job: ActivityJob;
  live: boolean;
  // The detail strip's default before any chevron override: top-level rows
  // open, rows revealed by expanding the inactive fold stay collapsed (the
  // fold click means "show the list", not "expand every child").
  defaultDetailOpen: boolean;
  transcriptRef?: string;
  parentRef: string;
}

export interface ActivityDelegateRow extends ActivityRowBase {
  kind: "delegate";
  delegate: ActivityDelegate;
  live: boolean;
  defaultDetailOpen: boolean;
  transcriptRef: string;
  parentRef: string;
}

export interface ActivityFoldRow extends ActivityRowBase {
  kind: "fold";
  foldParentID: string;
  inactiveCount: number;
  failedCount: number;
}

// A watch is pending work the session is waiting on, carried on the session
// summary rather than the retained-activity tree, so it is its own row kind.
export interface ActivityWatchRow extends ActivityRowBase {
  kind: "watch";
  watch: NavigationWatchSummary;
  // Top-level watches default open, exactly like a top-level job row: the
  // note and facts are the reason a person expanded the utility at all.
  defaultDetailOpen: boolean;
}

export type ActivityRow = ActivityJobRow | ActivityDelegateRow | ActivityFoldRow | ActivityWatchRow;

export function foldRowID(sessionNodeID: string): string {
  return `${sessionNodeID}:inactive-fold`;
}

// Watch rows share the expansion-state map with tree rows, so their ids carry
// a distinct namespace and can never collide with a tree node id.
export function watchRowID(watchID: string): string {
  return `watch:${watchID}`;
}

// One top-level row per watch, in wire order. An absent list (an old daemon)
// is exactly an empty list.
export function buildWatchRows(watches?: NavigationWatchSummary[]): ActivityWatchRow[] {
  return (watches ?? []).map((watch) => ({
    kind: "watch",
    id: watchRowID(watch.id),
    level: 1,
    watch,
    defaultDetailOpen: true,
  }));
}

// The same name the rail's watch row shows: the note a person wrote down, with
// the id as the fallback for a note the wire omitted or trimmed to nothing.
export function watchName(watch: NavigationWatchSummary): string {
  return watch.note?.trim() || watch.id;
}

// Watch kind is decided from the condition fields themselves, never from
// parsing prose: a watch can carry several cadence rows at once, and an
// output/event condition is what makes it a condition watch.
type WatchKind = "output" | "event" | "scheduled";

function watchKind(watch: NavigationWatchSummary): WatchKind {
  if ((watch.output_match ?? "").trim() !== "") return "output";
  if (watch.wildcard_events === true || (watch.events?.length ?? 0) > 0) return "event";
  return "scheduled";
}

export function watchIsScheduled(watch: NavigationWatchSummary): boolean {
  return watchKind(watch) === "scheduled";
}

// The supplied delivery instants as epoch millis, oldest first. Unparseable
// entries drop out; the ring is bounded (32) upstream, so this stays cheap.
export function watchDeliveryInstants(watch: NavigationWatchSummary): number[] {
  return (watch.delivery_times ?? [])
    .map((iso) => Date.parse(iso))
    .filter((millis) => !Number.isNaN(millis))
    .sort((a, b) => a - b);
}

function cadenceLabels(watch: NavigationWatchSummary): string[] {
  return (watch.cadence ?? []).map(watchCadenceLabel).filter((label) => label !== "");
}

function armedState(watch: NavigationWatchSummary): string {
  return watch.active ? "armed" : "not armed";
}

// The age since the watch was created, through the rail's own duration
// vocabulary. An unparseable or non-positive span renders nothing rather than
// a fabricated duration.
function armedAgeLabel(watch: NavigationWatchSummary, now: number): string | undefined {
  const created = Date.parse(watch.created_at);
  if (Number.isNaN(created)) return undefined;
  const label = watchDurationLabel(Math.max(0, (now - created) / 1000));
  return label === "" ? undefined : label;
}

function eventLabel(watch: NavigationWatchSummary): string {
  if (watch.wildcard_events === true) return "session events";
  const events = (watch.events ?? []).map((event) => event.trim()).filter((event) => event !== "");
  return events.length > 0 ? events.join(", ") : "session events";
}

// The newest supplied delivery instant as local HH:MM. Instants are oldest
// first, so the last parseable one wins; none parseable renders nothing.
function lastDeliveryClock(watch: NavigationWatchSummary): string | undefined {
  const times = watch.delivery_times ?? [];
  for (let index = times.length - 1; index >= 0; index--) {
    const clock = formatClockTime(times[index]);
    if (clock !== undefined) return clock;
  }
  return undefined;
}

// A watch row's second line: what its condition is, then the count it has
// earned or its armed state. Never a next-fire or countdown - the runtime
// keeps no such instant.
export function watchMeta(watch: NavigationWatchSummary): string {
  const kind = watchKind(watch);
  if (kind === "output") return `on output · ${armedState(watch)}`;
  if (kind === "event") return `on event · ${armedState(watch)}`;
  const cadence = cadenceLabels(watch).join(" · ");
  const suffix =
    watch.deliveries > 0 ? `${watch.deliveries} fire${watch.deliveries === 1 ? "" : "s"}` : armedState(watch);
  return [cadence, suffix].filter((part) => part !== "").join(" · ");
}

// A watch's one facts sentence, built only from real fields. The armed segment
// reports the watch's real armed state: an inactive watch is never called armed.
export function watchFacts(watch: NavigationWatchSummary, now: number): string {
  const kind = watchKind(watch);
  const segments: string[] = [];
  if (kind === "output") {
    const target = watch.target?.trim() || watch.source;
    segments.push(`Waiting on ${target}, matching ${watch.output_match ?? ""}`);
  } else if (kind === "event") {
    segments.push(`Waiting on ${eventLabel(watch)}`);
  } else {
    const cadence = cadenceLabels(watch).join(" · ");
    if (cadence !== "") segments.push(`Fires ${cadence}`);
  }
  if (!watch.active) {
    segments.push(armedState(watch));
  } else {
    const age = armedAgeLabel(watch, now);
    if (age !== undefined) segments.push(`armed ${age} ago`);
  }
  if (watch.deliveries > 0) {
    segments.push(`${watch.deliveries} ${watch.deliveries === 1 ? "delivery" : "deliveries"}`);
    if (kind === "scheduled") {
      const last = lastDeliveryClock(watch);
      if (last !== undefined) segments.push(`last at ${last}`);
    }
  } else {
    segments.push("no deliveries yet");
  }
  return segments.join(" · ");
}

export interface ActivityDelegateState {
  active: boolean;
  failed: boolean;
  status: string;
}

// A terminal entry's failure is the outcome the daemon already decided, so the
// rows, the fold's failure count, and the merged badge counts stay one number.
// Work that has not ended carries no outcome and can only say so through its
// current status.
export function jobIsFailed(job: ActivityJob): boolean {
  return job.terminal ? isFailedJobOutcome(job.outcome) : isActivityFailure(job.outcome, job.status);
}

// Stable delegates describe one reusable resource; other delegate types are
// turn containers. Keep this in one place so row visibility, fold failure
// counts, and the status shown by the row all use the protocol's same truth.
export function activityDelegateState(delegate: ActivityDelegate): ActivityDelegateState {
  const childActive = delegate.child ? sessionIsActive(delegate.child) : false;
  const childFailed = (delegate.child?.counts.failed ?? 0) > 0;
  if (!isTurnContainer(delegate)) {
    const status = stableDelegateDisplayStatus(delegate) ?? delegate.child?.aggregate ?? "unknown";
    const ownFailure =
      delegate.terminal === true ? isFailedDelegateOutcome(delegate.outcome) : isActivityFailure(undefined, status);
    return {
      active: delegateHasActiveWork(delegate),
      failed: ownFailure || childFailed,
      status,
    };
  }
  const turns = delegate.turns ?? [];
  let activeTurn: ActivityJob | undefined;
  for (const turn of turns) {
    if (!turn.terminal) activeTurn = turn;
  }
  const latest = turns.at(-1);
  if (turns.length === 0) {
    return {
      active: childActive,
      failed: childFailed,
      status: delegate.child?.aggregate ?? "unknown",
    };
  }
  const failed = childFailed || turns.some(jobIsFailed);
  const active = delegateHasActiveWork(delegate);
  return {
    active,
    failed,
    status: activeTurn
      ? activeTurn.status
      : childActive
        ? (delegate.child?.aggregate ?? "working")
        : failed
          ? "failed"
          : latest
            ? latest.status
            : (delegate.child?.aggregate ?? "unknown"),
  };
}

function jobIsActive(job: ActivityJob): boolean {
  return !job.terminal;
}

function sessionIsActive(session: ActivitySessionNode): boolean {
  return session.counts.active > 0 || session.entries.some(entryIsActive);
}

function entryIsActive(entry: ActivityEntry): boolean {
  if (entry.kind === "shell") return jobIsActive(entry.job);
  return activityDelegateState(entry.delegate).active;
}

function entryIsFailed(entry: ActivityEntry): boolean {
  if (entry.kind === "shell") return jobIsFailed(entry.job);
  const delegate: ActivityDelegate = entry.delegate;
  const state = activityDelegateState(delegate);
  return state.failed;
}

export function buildActivityRows(tree: ActivityTree, expandedFolds: ReadonlySet<string>): ActivityRow[] {
  const rows: ActivityRow[] = [];

  function appendEntry(
    entry: ActivityEntry,
    session: ActivitySessionNode,
    level: number,
    parentID: string,
    fromFold: boolean,
  ): void {
    if (entry.kind === "shell") {
      rows.push({
        kind: "job",
        id: activityNodeID(entry),
        parentID,
        level,
        job: entry.job,
        live: jobIsActive(entry.job),
        defaultDetailOpen: level === 1 && !fromFold,
        transcriptRef: entry.job.transcriptRef,
        parentRef: session.ref,
      });
      return;
    }
    const delegate = entry.delegate;
    const id = activityNodeID(entry);
    rows.push({
      kind: "delegate",
      id,
      parentID,
      level,
      delegate,
      live: entryIsActive(entry),
      defaultDetailOpen: level === 1 && !fromFold,
      transcriptRef: delegate.childRef,
      parentRef: session.ref,
    });
    if (delegate.child) visitSession(delegate.child, level + 1, id);
  }

  function visitSession(session: ActivitySessionNode, level: number, parentID?: string): void {
    const entriesParentID = parentID ?? activityNodeID(session);
    const live: ActivityEntry[] = [];
    const inactive: ActivityEntry[] = [];
    for (const entry of session.entries) {
      (entryIsActive(entry) ? live : inactive).push(entry);
    }
    for (const entry of live) appendEntry(entry, session, level, entriesParentID, false);
    if (inactive.length === 0) return;
    const id = foldRowID(activityNodeID(session));
    rows.push({
      kind: "fold",
      id,
      parentID: entriesParentID,
      level,
      foldParentID: activityNodeID(session),
      inactiveCount: inactive.length,
      failedCount: inactive.filter(entryIsFailed).length,
    });
    if (!expandedFolds.has(id)) return;
    for (const entry of inactive) appendEntry(entry, session, level, entriesParentID, true);
  }

  visitSession(tree.root, 1);
  return rows;
}
