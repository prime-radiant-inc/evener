// The web's read progress, mirroring the phone's BoardSeen
// (mobile-native/src/board/hubSeen.ts + board/boardMemory.ts). The unread
// marker means "there are replies here you have not read", never "a person is
// needed": needs-you is the session's live state, and the two never stand in
// for each other.
//
// A row that carries a readable turn_ended_at is the hub's to decide: the hub's
// seen store clears the row's unseen mark when any client marks it seen, so the
// phone and the web agree. A row without a turn end (an older hub, or a session
// that has not ended a turn) keeps this browser's own marks, compared entirely
// on the hub's timestamps so the local clock never matters. Opening a session
// records both halves of the mark: a pending hub mark, so the row clears before
// the hub's next navigation build, and the browser's own mark for rows the hub
// cannot decide.
import { isPlainObject, type NavigationSessionSummary } from "@evener/appwire-client";
import { hubTime } from "@evener/appwire-client/state/navigation";
import { useMemo, useSyncExternalStore } from "react";

const STORAGE_KEY = "evener.session.seen.v1";
/** As on the phone: past this many browser marks the oldest go first. */
const MARK_LIMIT = 500;

/** The fields read progress needs from a navigation row. */
export type ReadProgressRow = Pick<
  NavigationSessionSummary,
  "ref" | "unseen" | "turn_ended_at" | "seen_through" | "updated_at"
>;

interface DeviceMark {
  through: string;
}
interface DeviceState {
  /** The first fleet this browser read was adopted, so its sessions do not all
   * arrive unread. */
  adopted: boolean;
  epoch: string | null;
  sessions: Record<string, DeviceMark>;
}
interface PendingMark {
  /** The hub time the opening read through, in ms. */
  seenThrough: number;
  /** The hub answered a call carrying this mark. */
  acknowledged: boolean;
}

const EMPTY_DEVICE: DeviceState = { adopted: false, epoch: null, sessions: {} };

function parseDevice(raw: string | null): DeviceState {
  const state: DeviceState = { adopted: false, epoch: null, sessions: {} };
  if (raw === null) return state;
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    return state;
  }
  if (!isPlainObject(value)) return state;
  // A garbled epoch runs first run again rather than flooding the rail with
  // unread markers.
  const epoch = typeof value.epoch === "string" && hubTime(value.epoch) !== null ? value.epoch : null;
  if (value.adopted === true && (epoch !== null || value.epoch === null)) {
    state.adopted = true;
    state.epoch = epoch;
  }
  if (isPlainObject(value.sessions))
    for (const [ref, record] of Object.entries(value.sessions)) {
      if (isPlainObject(record) && typeof record.through === "string" && hubTime(record.through) !== null)
        state.sessions[ref] = { through: record.through };
    }
  return state;
}

/** Whether an unread set elsewhere came after this browser's mark: the hub
 * answered the mark, its seen-through has reached it, yet the row still reads
 * unseen. That unread is the newer word, so it wins over the mark. */
function unreadSince(row: ReadProgressRow, entry: PendingMark): boolean {
  const mark = hubTime(row.seen_through);
  return entry.acknowledged && row.unseen === true && mark !== null && mark >= entry.seenThrough;
}

export class SessionReadProgress {
  private device: DeviceState = EMPTY_DEVICE;
  /** The raw stored value the cached device state was parsed from, so a
   * cleared or rewritten storage is picked up without a stale cache. */
  private rawDevice: string | null = null;
  private pending = new Map<string, PendingMark>();
  private revision = 0;
  private listeners = new Set<() => void>();

  /** Whether the person has read this row up to its last turn end. */
  isSeen = (row: ReadProgressRow): boolean => {
    const ended = hubTime(row.turn_ended_at);
    if (ended !== null) {
      const entry = this.pending.get(row.ref);
      if (entry && ended <= entry.seenThrough && !unreadSince(row, entry)) return true;
      return row.unseen !== true;
    }
    return this.deviceSeen(row);
  };

  /** Records an opening read: `seenThrough` is the hub time the read reached
   * (a turn end or a later motion), absent when the hub has nothing to mark. */
  markRead(row: ReadProgressRow, seenThrough?: number): void {
    let changed = false;
    if (hubTime(row.turn_ended_at) === null) {
      this.recordDevice(row);
      changed = true;
    }
    if (seenThrough !== undefined && Number.isFinite(seenThrough) && seenThrough > 0) {
      const current = this.pending.get(row.ref);
      if (current === undefined || current.seenThrough < seenThrough) {
        this.pending.set(row.ref, { seenThrough, acknowledged: false });
        changed = true;
      }
    }
    if (changed) this.changed();
  }

  /** The hub answered a call carrying this mark, so a newer unread can now
   * outrank it (unreadSince). */
  acknowledge(ref: string, seenThrough: number): void {
    const entry = this.pending.get(ref);
    if (!entry || entry.seenThrough !== seenThrough || entry.acknowledged) return;
    entry.acknowledged = true;
    this.changed();
  }

  /** First run on this browser: the newest update it sees is the epoch, so
   * sessions that ended before this browser ever showed the rail stay read. */
  adoptEpoch(rows: readonly Pick<NavigationSessionSummary, "updated_at">[]): void {
    if (this.device.adopted) return;
    const state = this.deviceState();
    if (state.adopted) return;
    let newest: { value: string; at: number } | null = null;
    for (const row of rows) {
      const at = hubTime(row.updated_at);
      if (row.updated_at !== undefined && at !== null && (newest === null || at > newest.at))
        newest = { value: row.updated_at, at };
    }
    this.device = { adopted: true, epoch: newest?.value ?? null, sessions: state.sessions };
    this.saveDevice();
    this.changed();
  }

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  getRevision = (): number => this.revision;

  /** Test seam: forget the cached state and pending marks, keeping storage. */
  reset(): void {
    this.device = EMPTY_DEVICE;
    this.rawDevice = null;
    this.pending.clear();
    this.changed();
  }

  private deviceState(): DeviceState {
    let raw: string | null = null;
    try {
      raw = globalThis.localStorage.getItem(STORAGE_KEY);
    } catch {
      raw = null; // private mode, disabled storage: behave as no marks
    }
    if (raw !== this.rawDevice) {
      this.rawDevice = raw;
      this.device = parseDevice(raw);
    }
    return this.device;
  }

  private deviceSeen(row: ReadProgressRow): boolean {
    const state = this.deviceState();
    if (!state.adopted) return true;
    const updated = hubTime(row.updated_at);
    if (updated === null) return true;
    const through = Math.max(
      hubTime(state.sessions[row.ref]?.through) ?? Number.NEGATIVE_INFINITY,
      hubTime(state.epoch) ?? Number.NEGATIVE_INFINITY,
    );
    return updated <= through;
  }

  private recordDevice(row: ReadProgressRow): void {
    const state = this.deviceState();
    const sessions = { ...state.sessions };
    if (row.updated_at !== undefined) sessions[row.ref] = { through: row.updated_at };
    else delete sessions[row.ref];
    this.device = { ...state, sessions };
    this.saveDevice();
  }

  private saveDevice(): void {
    const entries = Object.entries(this.device.sessions);
    if (entries.length > MARK_LIMIT) {
      entries.sort(([, a], [, b]) => (hubTime(b.through) ?? 0) - (hubTime(a.through) ?? 0));
      this.device = { ...this.device, sessions: Object.fromEntries(entries.slice(0, MARK_LIMIT)) };
    }
    let encoded: string | null = null;
    try {
      encoded = JSON.stringify(this.device);
      globalThis.localStorage.setItem(STORAGE_KEY, encoded);
    } catch {
      // Best-effort, like the transcript's own watermark: a full quota or
      // private mode must not break the rail.
      encoded = null;
    }
    this.rawDevice = encoded;
  }

  private changed(): void {
    this.revision++;
    for (const listener of [...this.listeners]) listener();
  }
}

export const sessionReadProgress = new SessionReadProgress();

export interface SessionSeen {
  /** The read-progress revision this reader reflects: a new mark replaces the
   * reader, so a memo listing it recomputes. */
  revision: number;
  isSeen(row: ReadProgressRow): boolean;
}

/** The rail's reader, replaced on every mark. */
export function useSessionSeen(): SessionSeen {
  const revision = useSyncExternalStore(sessionReadProgress.subscribe, sessionReadProgress.getRevision);
  return useMemo(() => ({ revision, isSeen: sessionReadProgress.isSeen }), [revision]);
}
