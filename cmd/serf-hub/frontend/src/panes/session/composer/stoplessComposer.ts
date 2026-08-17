// Kata 5gdv: a working session drawn with no Stop, reported from live use and
// not reproduced in three live measurements (a pull read inside the pre-turn
// window, 4721 pull samples across a queued drain, and every pushed status
// frame across a drain -- interrupt=true in all of them).
//
// Since the state cannot be provoked on demand, it has to describe itself when
// it happens. The composer already knows: it is one boolean away from "I am
// drawing a session the user can see working, with no way to stop it". This
// records what the model looked like at that moment.
//
// It is diagnostic only. Nothing renders off it, and it never changes what the
// composer does -- a breadcrumb, not a behaviour.
//
// What matters is not the capability set on its own, which is only ever one of
// two values. It is which frame WROTE that set (capabilitySource) beside the
// status it disagrees with: the reducer has exactly two writers, so a captured
// sighting names the culprit rather than reopening the search.

import type { CapabilitySource } from "../../../protocol/model";
import type { ThreadCapabilities } from "../../../protocol/types.gen";

export interface StoplessSighting {
  ref: string;
  status: string;
  activeTurnId: string | undefined;
  capabilities: ThreadCapabilities;
  capabilitySource: CapabilitySource;
  // Which of the sibling controls WERE on screen. Jesse's report is
  // specifically Steer and Send present with Stop gone, and the other
  // combinations have separate known causes (kata 06t8 loses Steer and Send and
  // KEEPS Stop; kata pk2d loses the whole card), so recording the shape is what
  // tells a reader which family a sighting belongs to.
  showSteer: boolean;
  ended: boolean;
}

// Bounded so a wedged session cannot grow this without limit. Sightings repeat
// on every render while the state persists, and the first one is the
// informative one, so the ring keeps the earliest and drops later duplicates of
// the same shape.
const MAX_SIGHTINGS = 20;
const sightings: StoplessSighting[] = [];

function shapeOf(sighting: StoplessSighting): string {
  const caps = sighting.capabilities;
  return [
    sighting.ref,
    sighting.status,
    sighting.capabilitySource,
    `steer=${caps.steer}`,
    `interrupt=${caps.interrupt}`,
    `send=${caps.send}`,
    `showSteer=${sighting.showSteer}`,
  ].join("|");
}

// recordStoplessComposer is called from the composer's render path when it is
// about to draw a working session with no Stop. Repeats of a shape already
// recorded are dropped, so a session stuck in the state contributes one entry
// rather than one per render.
export function recordStoplessComposer(sighting: StoplessSighting): void {
  const shape = shapeOf(sighting);
  if (sightings.some((seen) => shapeOf(seen) === shape)) return;
  if (sightings.length >= MAX_SIGHTINGS) return;
  sightings.push(sighting);
  // Console as well as the ring: the ring is for anyone who thinks to look, the
  // console line is for the person who is looking at the broken pane right now
  // and would otherwise reload and destroy the evidence.
  console.warn(
    "[serf 5gdv] composer is showing a working session with no Stop. Please attach this to kata 5gdv:",
    sighting,
  );
}

// stoplessComposerSightings is the read side, for a diagnostics surface or a
// console poke by whoever is staring at the broken pane.
export function stoplessComposerSightings(): readonly StoplessSighting[] {
  return sightings;
}

export function resetStoplessComposerSightingsForTests(): void {
  sightings.length = 0;
}
