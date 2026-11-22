// The web's read progress mirrors the phone's BoardSeen (mobile-native's
// board/hubSeen.ts + board/boardMemory.ts): the hub decides a row that carries
// a readable turn_ended_at, this browser's own marker decides any other, and
// opening a session records a mark the row can clear from without waiting for
// the hub's next navigation build.
import { beforeAll, beforeEach, expect, test } from "vitest";
import { installLocalStorage, MemoryStorage } from "../storageTestUtils";
import { type ReadProgressRow, SessionReadProgress } from "./readProgress";

const FIRST = "2026-09-26T11:58:00.123Z";
const NEXT = "2026-09-26T12:04:00.456Z";
const OLDER = "2026-09-25T09:00:00.000Z";

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

beforeEach(() => {
  localStorage.clear();
});

function row(overrides: Partial<ReadProgressRow> = {}): ReadProgressRow {
  return { ref: "local:01READ", ...overrides };
}

test("the hub decides a row that carries a turn end", () => {
  const progress = new SessionReadProgress();
  expect(progress.isSeen(row({ turn_ended_at: FIRST, unseen: true }))).toBe(false);
  expect(progress.isSeen(row({ turn_ended_at: FIRST, unseen: false }))).toBe(true);
});

test("opening a session clears its unread mark", () => {
  const progress = new SessionReadProgress();
  const asking = row({ turn_ended_at: FIRST, unseen: true });
  expect(progress.isSeen(asking)).toBe(false);

  progress.markRead(asking, Date.parse(FIRST));

  expect(progress.isSeen(asking)).toBe(true);
});

test("a failed turn's end is a read-eligible position", () => {
  const progress = new SessionReadProgress();
  // A failed turn still ends, and the hub decides from that end: the browser's
  // first-run epoch must not read the row through, with or without an update.
  progress.adoptEpoch([{ updated_at: NEXT }]);
  const failed = row({ turn_ended_at: FIRST, unseen: true });
  expect(progress.isSeen(failed)).toBe(false);
  progress.markRead(failed, Date.parse(FIRST));
  expect(progress.isSeen(failed)).toBe(true);
});

test("a reply that lands after the mark reads unseen again", () => {
  const progress = new SessionReadProgress();
  progress.markRead(row({ turn_ended_at: FIRST, unseen: true }), Date.parse(FIRST));
  expect(progress.isSeen(row({ turn_ended_at: FIRST, unseen: true }))).toBe(true);
  expect(progress.isSeen(row({ turn_ended_at: NEXT, unseen: true }))).toBe(false);
});

test("an unread another client set after the mark landed outranks the mark", () => {
  const progress = new SessionReadProgress();
  progress.markRead(row({ turn_ended_at: FIRST, unseen: true }), Date.parse(FIRST));
  progress.acknowledge("local:01READ", Date.parse(FIRST));
  expect(progress.isSeen(row({ turn_ended_at: FIRST, unseen: true, seen_through: FIRST }))).toBe(false);
});

test("a row with no turn end falls back to this browser's own marks", () => {
  const progress = new SessionReadProgress();
  // First run adopts the newest update it sees, so a fleet that predates this
  // browser does not arrive all unread.
  progress.adoptEpoch([{ updated_at: OLDER }]);
  expect(progress.isSeen(row({ updated_at: OLDER }))).toBe(true);

  const fresh = row({ updated_at: NEXT });
  expect(progress.isSeen(fresh)).toBe(false);
  progress.markRead(fresh);
  expect(progress.isSeen(fresh)).toBe(true);
});

test("a browser's fallback marks survive a reload and stay per session", () => {
  const first = new SessionReadProgress();
  first.adoptEpoch([{ updated_at: OLDER }]);
  first.markRead(row({ ref: "local:A", updated_at: NEXT }));

  // A fresh store over the same storage is what a reload sees.
  const reloaded = new SessionReadProgress();
  expect(reloaded.isSeen(row({ ref: "local:A", updated_at: NEXT }))).toBe(true);
  expect(reloaded.isSeen(row({ ref: "local:B", updated_at: NEXT }))).toBe(false);
});
