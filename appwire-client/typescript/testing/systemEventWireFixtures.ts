// systemEventWireFixtures reads the daemon's system events and steers as the
// hub sends them: a tool repair, a human's Allow and Deny, the two compaction
// summaries, plugin loads, compaction passes, one steer per daemon kind the
// transcript labels, and a failed turn as a reload groups it (turn.error
// plus its error systemMessage), alone and after an earlier, distinct error.
//
// agent's TestSystemEventWireFixtures produces the fixture from the builders
// the live projector and history share, and re-verifies it on every Go test
// run; regenerate it with `make fuzz-goldens`. It is loaded through a `?raw`
// import, as notificationWireFixtures loads its own.

import systemEvents from "../../../agent/testdata/systemeventwire/events.json?raw";
import type { ThreadItem, Turn } from "../types.gen";

const FIXTURE_PATH = "agent/testdata/systemeventwire/events.json";

/** The recorded case names, in the order the fixture lists them. */
export type SystemEventWireCase =
  | "tool-repair"
  | "approval-allowed"
  | "approval-denied"
  | "compaction-summary"
  | "compaction-checkpoint"
  | "plugin-loaded"
  | "plugin-loaded-unnamed"
  | "context-compaction"
  | "context-compaction-turns"
  | "context-compaction-bare"
  | "steer-hook-context"
  | "steer-precompact-hook"
  | "steer-compact-nudge"
  | "steer-no-tool-calls"
  | "steer-loop-detected"
  | "steer-provider-failure"
  | "steer-transcript-pointer"
  | "steer-current-task"
  | "steer-task-list"
  | "steer-note-handoff";

interface SystemEventWireFixture {
  items: Array<{ case: SystemEventWireCase; note: string; item: ThreadItem }>;
  failed_turn: Turn;
  failed_turn_with_other_error: Turn;
}

const fixture = (): SystemEventWireFixture => JSON.parse(systemEvents) as SystemEventWireFixture;

/** systemEventWireItem returns the item the daemon sends for one recorded case. */
export function systemEventWireItem(name: SystemEventWireCase): ThreadItem {
  const record = fixture().items.find((rec) => rec.case === name);
  if (!record) throw new Error(`no ${name} case in ${FIXTURE_PATH}`);
  return record.item;
}

/** systemEventWireItems returns every recorded item, in fixture order. */
export function systemEventWireItems(): ThreadItem[] {
  return fixture().items.map((rec) => rec.item);
}

/** systemEventWireFailedTurn returns the failed turn as a reload carries it. */
export function systemEventWireFailedTurn(): Turn {
  return fixture().failed_turn;
}

/** systemEventWireFailedTurnWithOtherError returns the failed turn with an
 * earlier, distinct error the live overlay showed during it. */
export function systemEventWireFailedTurnWithOtherError(): Turn {
  return fixture().failed_turn_with_other_error;
}
