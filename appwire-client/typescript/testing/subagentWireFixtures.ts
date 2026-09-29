// subagentWireFixtures reads the coordinator's tool calls as the hub sends
// them: a `delegate` call and its launch receipt, a `delegate_send`, a shell
// call with an intent and a task_list call without one, each as the
// announcing item and the result item history carries.
//
// agent's TestSubagentCallWireFixtures produces the fixture by projecting the
// recorded turns through apptranscript, and re-verifies it on every Go test
// run; regenerate it with `make fuzz-goldens`. It is loaded through a `?raw`
// import, as notificationWireFixtures loads its own.

import subagentCalls from "../../../agent/testdata/subagentwire/calls.json?raw";
import type { ItemModel, ThreadModel } from "../model";
import { hydrateThread } from "../reducer";
import type { Thread, ThreadItem } from "../types.gen";
import { wireThread } from "./notifications";

/** The recorded call and result items, in the order history carries them. */
export function subagentCallItems(): ThreadItem[] {
  return (JSON.parse(subagentCalls) as { items: ThreadItem[] }).items;
}

let model: ThreadModel | undefined;

/** One settled step: the recorded call merged with its result, as a client
 * holds it. Hydrated once; treat it as read-only. */
export function subagentWireStep(callId: string): ItemModel {
  model ??= hydrateThread(
    {
      thread: wireThread("ref-subagents", {
        turns: [{ id: "turn_1", itemsView: "full", status: "completed", items: subagentCallItems() }],
      } as Partial<Thread>),
    },
    "ref-subagents",
    0,
  );
  const item = model.turns.flatMap((turn) => turn.items).find((candidate) => candidate.callId === callId);
  if (!item) throw new Error(`no ${callId} step in agent/testdata/subagentwire/calls.json`);
  return item;
}
