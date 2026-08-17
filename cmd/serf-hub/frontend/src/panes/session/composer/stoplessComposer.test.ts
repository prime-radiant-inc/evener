// The breadcrumb for kata 5gdv has one job: when a working session is drawn
// with no Stop, the next person to see it should not have to reproduce it.
// These cover the two ways that job fails -- not firing, and firing so much
// that the evidence is buried.
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { ThreadCapabilities } from "../../../protocol/types.gen";
import {
  recordStoplessComposer,
  resetStoplessComposerSightingsForTests,
  stoplessComposerSightings,
} from "./stoplessComposer";

// The set at the heart of the report: a turn is running and can be steered, and
// Stop is not on offer.
const STEER_WITHOUT_STOP: ThreadCapabilities = {
  send: false,
  steer: true,
  interrupt: false,
  compact: true,
  clear: false,
  forkFromTurn: false,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

function sighting(overrides: Partial<Parameters<typeof recordStoplessComposer>[0]> = {}) {
  return {
    ref: "ref_a",
    status: "active",
    activeTurnId: "turn_5",
    capabilities: STEER_WITHOUT_STOP,
    capabilitySource: "statusFrame" as const,
    showSteer: true,
    ended: false,
    ...overrides,
  };
}

beforeEach(() => {
  resetStoplessComposerSightingsForTests();
  vi.spyOn(console, "warn").mockImplementation(() => {});
});

afterEach(() => {
  vi.restoreAllMocks();
  resetStoplessComposerSightingsForTests();
});

test("a sighting keeps the frame that wrote the set, not just the set", () => {
  recordStoplessComposer(sighting());

  expect(stoplessComposerSightings()).toHaveLength(1);
  // capabilitySource is the whole point: the reducer has exactly two writers,
  // so this names which one put the client in this state.
  expect(stoplessComposerSightings()[0]).toMatchObject({
    ref: "ref_a",
    status: "active",
    activeTurnId: "turn_5",
    capabilitySource: "statusFrame",
    showSteer: true,
  });
  expect(console.warn).toHaveBeenCalled();
});

// A wedged session re-renders constantly. Without this the ring fills with one
// state and the console scrolls the first, most informative sighting away.
test("a session stuck in the state contributes one sighting, not one per render", () => {
  for (let i = 0; i < 50; i++) recordStoplessComposer(sighting());

  expect(stoplessComposerSightings()).toHaveLength(1);
  expect(console.warn).toHaveBeenCalledTimes(1);
});

// Different shapes are different evidence: kata 06t8 loses Steer and KEEPS
// Stop, pk2d loses the whole card, and Jesse's report keeps Steer. Collapsing
// them together would throw away the discriminator.
test("a different shape is recorded separately", () => {
  recordStoplessComposer(sighting());
  recordStoplessComposer(sighting({ showSteer: false, capabilitySource: "read" }));

  expect(stoplessComposerSightings()).toHaveLength(2);
});

// Unbounded diagnostic state in a long-lived tab is its own bug.
test("the ring is bounded", () => {
  for (let i = 0; i < 100; i++) recordStoplessComposer(sighting({ ref: `ref_${i}` }));

  expect(stoplessComposerSightings().length).toBeLessThanOrEqual(20);
});
