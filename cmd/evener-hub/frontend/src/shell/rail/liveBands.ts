// Live's bands on the rail: Needs you, Working, Idle, in the order the shared
// attention model gives them (liveBands in @evener/appwire-client), so the
// rail and the phone Board agree on who needs you. Everything that reads
// Live's order - the rendered tree, Alt-arrow cycling and the reveal path -
// reads it from here, so the keyboard walks the rows in the order they show.
//
// The rail differs from the shared model in two places, both so that no row
// the hub lists in Live goes missing and a row's band agrees with its dot:
// a session the rail presents as working (a resting one, or one with a
// warning that blocks nothing, whose background job still runs) sits in
// Working; and a row the shared model bands nowhere (its host is offline, or
// it has not loaded) rests at the end of Idle rather than dropping out of
// Live.
//
// The needs-you section's rows join as the hub sends them, archived ones
// included: the hub's tier never decays with age, and the header's count and
// Next needs you both walk the same rows.
import { LOCAL_HOST, type NavigationSessionSummary, type Source } from "@evener/appwire-client";
import {
  type Band,
  type BoardState,
  hostLabeler,
  LIVE_BAND_ORDER,
  liveBands,
  usualPlace,
} from "@evener/appwire-client/state/navigation";
import { cadenceStateFor } from "../sessionStatus";
import {
  type BandRailNode,
  cachedBandNode,
  effectiveSessionState,
  liveSessionNode,
  type RailSession,
} from "./railNodes";

export interface RailLiveRow<Row extends NavigationSessionSummary = RailSession> {
  session: Row;
  /** The attention class the row's band and why line are read from. */
  state: BoardState;
}

export type RailLiveBands<Row extends NavigationSessionSummary = RailSession> = Record<Band, RailLiveRow<Row>[]>;

function presentsWorking(session: NavigationSessionSummary): boolean {
  return (
    cadenceStateFor(effectiveSessionState(session), {
      askPending: session.ask_pending === true,
      approvalPending: session.approval_pending === true,
    }) === "working"
  );
}

/** Live's loaded rows, with the needs-you section's rows joined in, split
 * into the rail's bands. Every row of `live` lands in exactly one band. */
export function railLiveBands<Row extends NavigationSessionSummary>(
  live: readonly Row[],
  needsYou: readonly Row[],
): RailLiveBands<Row> {
  // The two sections are read separately, so their copies of one session can
  // disagree. The needs-you section is the hub's attention read: while it
  // lists a session, its copy is the one shown, so the band, the header's
  // count and Next needs you always tell the same story, and an older Live
  // copy never hides a session that needs a person.
  const queued = new Map(needsYou.map((row) => [row.ref, row]));
  const current = live.map((row) => queued.get(row.ref) ?? row);
  const shared = liveBands<Row>(current, needsYou, () => true);
  const bands: RailLiveBands<Row> = { needsYou: [], working: [], idle: [] };
  const placed = new Set<string>();
  const presentedWorking: RailLiveRow<Row>[] = [];
  for (const band of LIVE_BAND_ORDER) {
    for (const item of shared[band]) {
      placed.add(item.row.ref);
      // Only a row with nothing else to say yields to its running work: a
      // failure, question, approval or restart keeps its class, however the
      // model arrived at it.
      if ((item.state === "idle" || item.state === "warning") && presentsWorking(item.row))
        presentedWorking.push({ session: item.row, state: "working" });
      else bands[band].push({ session: item.row, state: item.state });
    }
  }
  bands.working.push(...presentedWorking);
  for (const row of current) if (!placed.has(row.ref)) bands.idle.push({ session: row, state: "shutDown" });
  return bands;
}

/** The bands to show while the reader is mid-interaction with a row (a dialog
 * opened from it): every row `previous` showed keeps its band and place, with
 * its fresh facts, so the row - and the dialog and draft it owns - is not
 * torn down by moving to another band or by leaving the sections for a
 * moment. New rows join the band they belong to. */
export function holdLiveBands<Row extends NavigationSessionSummary>(
  previous: RailLiveBands<Row>,
  next: RailLiveBands<Row>,
): RailLiveBands<Row> {
  const fresh = new Map<string, RailLiveRow<Row>>();
  for (const band of LIVE_BAND_ORDER) for (const row of next[band]) fresh.set(row.session.ref, row);
  const shown = new Set<string>();
  const held: RailLiveBands<Row> = { needsYou: [], working: [], idle: [] };
  for (const band of LIVE_BAND_ORDER) {
    for (const row of previous[band]) {
      // A row the sections stopped supplying stays as it last was: the open
      // dialog may be its own, and it leaves when the hold releases.
      shown.add(row.session.ref);
      held[band].push(fresh.get(row.session.ref) ?? row);
    }
  }
  for (const band of LIVE_BAND_ORDER) {
    for (const row of next[band]) if (!shown.has(row.session.ref)) held[band].push(row);
  }
  return held;
}

/** Live's refs in the order the rail shows them, band by band. */
export function liveBandRefs(bands: RailLiveBands<NavigationSessionSummary>): string[] {
  return LIVE_BAND_ORDER.flatMap((band) => bands[band].map((row) => row.session.ref));
}

/** The refs the live-session chords walk: the order on screen, or nothing
 * while Live itself has yet to load, so a press demands Live's first page
 * instead of cycling the needs-you rows that happened to arrive first. */
export function liveCycleRefs(
  live: readonly NavigationSessionSummary[],
  needsYou: readonly NavigationSessionSummary[],
  liveUnloaded: boolean,
): string[] {
  if (liveUnloaded && live.length === 0) return [];
  return liveBandRefs(railLiveBands(live, needsYou));
}

export function liveBandId(band: Band): string {
  return `liveband:${band}`;
}

/** The band fold that holds `ref`, or undefined when Live does not show it. */
export function liveBandFoldOf(bands: RailLiveBands<NavigationSessionSummary>, ref: string): string | undefined {
  const band = LIVE_BAND_ORDER.find((candidate) => bands[candidate].some((row) => row.session.ref === ref));
  return band === undefined ? undefined : liveBandId(band);
}

// The host a Live row is assumed to run on unless it says otherwise: this hub
// while any Live row runs here, else the host most of Live runs on. Undefined
// while Live sits on one host, where no row has anything to tell apart.
function assumedHost(sessions: readonly NavigationSessionSummary[]): string | undefined {
  const hosts = new Set(sessions.map((session) => session.host_id));
  if (hosts.size <= 1) return undefined;
  return hosts.has(LOCAL_HOST) ? LOCAL_HOST : usualPlace(sessions).host;
}

/** One tree branch per band that has rows. A row names its host only where
 * the reader could not guess it: Live spans hosts and the row is away from
 * the assumed one, or its host is offline. */
export function liveBandNodes(
  bands: RailLiveBands,
  sources: readonly Source[],
  isExpanded: (id: string, defaultExpanded: boolean) => boolean,
): BandRailNode[] {
  const all = LIVE_BAND_ORDER.flatMap((band) => bands[band]);
  const assumed = assumedHost(all.map((row) => row.session));
  const label = hostLabeler(sources);
  const offline = new Set(sources.filter((source) => !source.online).map((source) => source.id));
  return LIVE_BAND_ORDER.flatMap((band) => {
    const rows = bands[band];
    if (rows.length === 0) return [];
    const id = liveBandId(band);
    return [
      cachedBandNode(
        id,
        band,
        isExpanded(id, true),
        rows.map(({ session, state }) => {
          const hostOffline = offline.has(session.host_id);
          const namesHost = hostOffline || (assumed !== undefined && session.host_id !== assumed);
          return liveSessionNode(
            session,
            state,
            namesHost ? { label: label(session.host_id), online: !hostOffline } : undefined,
          );
        }),
      ),
    ];
  });
}
