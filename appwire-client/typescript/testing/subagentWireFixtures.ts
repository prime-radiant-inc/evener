// subagentWireFixtures reads the coordinator's tool calls as the hub sends
// them: a `delegate` call and its launch receipt, a `delegate_send`, a shell
// call with an intent and a task_list call without one, each as the
// announcing item and the result item history carries. It also reads
// evener/jobs/list's answer for a coordinator whose subagents finished: one
// reported, one the user stopped, one failed.
//
// agent's TestSubagentCallWireFixtures and TestSubagentOutcomeWireFixtures
// produce the fixtures from the daemon's own producers, and re-verify them on
// every Go test run; regenerate them with `make fuzz-goldens`. They are loaded
// through a `?raw` import, as notificationWireFixtures loads its own.

import subagentCalls from "../../../agent/testdata/subagentwire/calls.json?raw";
import subagentOutcomes from "../../../agent/testdata/subagentwire/outcomes.json?raw";
import type { JobsListResponse, ThreadItem } from "../types.gen";

/** The recorded call and result items, in the order history carries them. */
export function subagentCallItems(): ThreadItem[] {
  return (JSON.parse(subagentCalls) as { items: ThreadItem[] }).items;
}

/** evener/jobs/list's recorded answer for a coordinator (ref "local:root",
 * thread "root") with three finished subagents: dlg_reported, dlg_stopped and
 * dlg_failed. Their child sessions aren't on disk, so each delegate carries a
 * branch.error and the tree reads as partial; each outcome is on the
 * delegate itself. */
export function subagentOutcomesResponse(): JobsListResponse {
  return (JSON.parse(subagentOutcomes) as { response: JobsListResponse }).response;
}
