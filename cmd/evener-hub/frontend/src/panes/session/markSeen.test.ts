import type { NavigationSessionSummary } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "../../stores/connection";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { seenThroughToMark, seenThroughWithMotion, useMarkSessionSeenOnOpen } from "./markSeen";

const REF = "local:01SEEN";
const FIRST_TURN = "2026-09-26T11:58:00.123Z";
const NEXT_TURN = "2026-09-26T12:04:00.456Z";
const SEEN_SET = "evener/session/seen/set";

let visibility: DocumentVisibilityState = "visible";

// showRow installs the pane's row as a loaded navigation location, the way the
// hub's location read would.
function showRow(fields: Partial<NavigationSessionSummary>): void {
  const key = { kind: "location", ref: REF } as const;
  const data = {
    generation_id: "generation_test",
    revision: 1,
    ref: REF,
    top_level_ref: REF,
    top_level: true,
    session: {
      ref: REF,
      host_id: "local",
      session_id: "01SEEN",
      title: "Finished work",
      project: "test-project",
      state: "idle",
      kind: "session",
      live: true,
      children: [],
      ...fields,
    },
  };
  navigationStore.setState({
    mode: "v3",
    clientGenerationID: "generation_test",
    resources: new Map([
      [
        keyID(key),
        {
          key,
          data,
          loadedRevision: 1,
          targetRevision: null,
          forceToken: 0,
          etag: "etag",
          loading: false,
          stale: false,
          error: null,
          generationID: "generation_test",
        },
      ],
    ]),
  });
}

function connectFake(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on(SEEN_SET, () => ({ ok: true, changed: true, navigation: { generation_id: "generation_test", targets: [] } }));
  connectionStore.getState().connect(fake);
  return fake;
}

const marks = (fake: FakeClient) => fake.calls.filter((call) => call.method === SEEN_SET).map((call) => call.params);
const markFor = (turn: string) => ({ sessions: [{ ref: REF, seenThrough: Date.parse(turn) }] });

function setVisibility(next: DocumentVisibilityState): void {
  visibility = next;
  act(() => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetNavigationStoreForTests();
  visibility = "visible";
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => visibility });
});

afterEach(() => {
  cleanup();
  Reflect.deleteProperty(document, "visibilityState");
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetNavigationStoreForTests();
});

test("only an unseen row that carries its turn end has a turn to mark", () => {
  expect(seenThroughToMark({ unseen: true, turn_ended_at: FIRST_TURN })).toBe(Date.parse(FIRST_TURN));
  expect(seenThroughToMark({ unseen: false, turn_ended_at: FIRST_TURN })).toBeUndefined();
  expect(seenThroughToMark({ turn_ended_at: FIRST_TURN })).toBeUndefined();
  expect(seenThroughToMark({ unseen: true })).toBeUndefined();
  expect(seenThroughToMark({ unseen: true, turn_ended_at: "not a time" })).toBeUndefined();
  expect(seenThroughToMark(undefined)).toBeUndefined();
});

test("opening the pane marks the turn its row shows, once", () => {
  showRow({ unseen: true, turn_ended_at: FIRST_TURN });
  const fake = connectFake();
  renderHook(() => useMarkSessionSeenOnOpen(REF));
  expect(marks(fake)).toEqual([markFor(FIRST_TURN)]);
  // The hub's answer arrives as a navigation update and must not mark again.
  act(() => showRow({ turn_ended_at: FIRST_TURN }));
  expect(marks(fake)).toEqual([markFor(FIRST_TURN)]);
});

test("a row that loads after the pane opens is marked when it arrives, and a later turn is not", () => {
  const fake = connectFake();
  renderHook(() => useMarkSessionSeenOnOpen(REF));
  expect(marks(fake)).toEqual([]);
  act(() => showRow({ unseen: true, turn_ended_at: FIRST_TURN }));
  expect(marks(fake)).toEqual([markFor(FIRST_TURN)]);
  // A turn that ends while the pane stays open is not "opened since" (S4 ruling 18).
  act(() => showRow({ unseen: true, turn_ended_at: NEXT_TURN }));
  expect(marks(fake)).toEqual([markFor(FIRST_TURN)]);
});

test("a seen row, or one without a turn end, sends nothing", () => {
  const fake = connectFake();
  showRow({ unseen: false, turn_ended_at: FIRST_TURN });
  renderHook(() => useMarkSessionSeenOnOpen(REF)).unmount();
  showRow({});
  renderHook(() => useMarkSessionSeenOnOpen(REF));
  expect(marks(fake)).toEqual([]);
});

test("coming back to the page marks the turn the row shows now", () => {
  showRow({ unseen: true, turn_ended_at: FIRST_TURN });
  const fake = connectFake();
  renderHook(() => useMarkSessionSeenOnOpen(REF));
  act(() => showRow({ unseen: true, turn_ended_at: NEXT_TURN }));
  setVisibility("hidden");
  expect(marks(fake)).toEqual([markFor(FIRST_TURN)]);
  setVisibility("visible");
  expect(marks(fake)).toEqual([markFor(FIRST_TURN), markFor(NEXT_TURN)]);
});

test("a pane that opens while the page is hidden waits until the page is shown", () => {
  visibility = "hidden";
  showRow({ unseen: true, turn_ended_at: FIRST_TURN });
  const fake = connectFake();
  renderHook(() => useMarkSessionSeenOnOpen(REF));
  expect(marks(fake)).toEqual([]);
  setVisibility("visible");
  expect(marks(fake)).toEqual([markFor(FIRST_TURN)]);
});

test("the mark waits for a ready connection", () => {
  showRow({ unseen: true, turn_ended_at: FIRST_TURN });
  renderHook(() => useMarkSessionSeenOnOpen(REF));
  const fake = connectFake();
  expect(marks(fake)).toEqual([markFor(FIRST_TURN)]);
});

const ACTIVITY_READ = "evener/activity/read";
const SEEN_MARK = "2026-09-26T12:00:00.000Z";
const activityFor = (lastMovedAt?: number) => ({
  sessions: [
    {
      ref: REF,
      minutes: [0, 0, 0, 0, 0, 0, 0],
      runningSubagents: 0,
      ...(lastMovedAt === undefined ? {} : { lastMovedAt }),
    },
  ],
});
const settle = () => act(async () => {});

test("the mark covers the session's last motion when it came after the hub's seen mark", () => {
  const moved = Date.parse(SEEN_MARK) + 60_000;
  expect(seenThroughWithMotion({ seen_through: SEEN_MARK }, moved)).toBe(moved);
  expect(seenThroughWithMotion({ seen_through: SEEN_MARK }, Date.parse(SEEN_MARK))).toBeUndefined();
  expect(seenThroughWithMotion({ seen_through: SEEN_MARK, unseen: true, turn_ended_at: FIRST_TURN }, moved)).toBe(
    moved,
  );
  expect(
    seenThroughWithMotion({ seen_through: SEEN_MARK, unseen: true, turn_ended_at: NEXT_TURN }, Date.parse(FIRST_TURN)),
  ).toBe(Date.parse(NEXT_TURN));
  // Without the hub's seen mark, motion can't be compared.
  expect(seenThroughWithMotion({}, moved)).toBeUndefined();
});

test("opening a pane marks a session seen through output that streamed after its seen mark", async () => {
  const moved = Date.parse(SEEN_MARK) + 60_000;
  showRow({ seen_through: SEEN_MARK, turn_ended_at: FIRST_TURN, unseen: false });
  const fake = connectFake();
  fake.on(ACTIVITY_READ, () => activityFor(moved));
  renderHook(() => useMarkSessionSeenOnOpen(REF));
  await settle();
  expect(fake.calls.find((call) => call.method === ACTIVITY_READ)?.params).toEqual({ refs: [REF] });
  expect(marks(fake)).toEqual([{ sessions: [{ ref: REF, seenThrough: moved }] }]);
  // Coming back with nothing newer sends no mark again.
  setVisibility("hidden");
  setVisibility("visible");
  await settle();
  expect(marks(fake)).toEqual([{ sessions: [{ ref: REF, seenThrough: moved }] }]);
});

test("a failed activity read still marks the unseen turn, and a session with nothing new sends nothing", async () => {
  showRow({ seen_through: SEEN_MARK, unseen: true, turn_ended_at: FIRST_TURN });
  const failing = connectFake();
  failing.on(ACTIVITY_READ, () => {
    throw new Error("request timed out");
  });
  const failed = renderHook(() => useMarkSessionSeenOnOpen(REF));
  await settle();
  expect(marks(failing)).toEqual([markFor(FIRST_TURN)]);
  failed.unmount();

  showRow({ seen_through: SEEN_MARK, unseen: false, turn_ended_at: FIRST_TURN });
  const quiet = connectFake();
  quiet.on(ACTIVITY_READ, () => activityFor(Date.parse(SEEN_MARK) - 1));
  renderHook(() => useMarkSessionSeenOnOpen(REF));
  await settle();
  expect(marks(quiet)).toEqual([]);
});
