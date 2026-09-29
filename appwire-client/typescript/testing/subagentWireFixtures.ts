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
import type { ThreadItem } from "../types.gen";

/** The recorded call and result items, in the order history carries them. */
export function subagentCallItems(): ThreadItem[] {
  return (JSON.parse(subagentCalls) as { items: ThreadItem[] }).items;
}
