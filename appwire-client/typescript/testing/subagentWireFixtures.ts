// subagentWireFixtures reads the coordinator's tool calls as the hub sends
// them: a `delegate` call and its launch receipt, a `delegate_send`, a shell
// call with an intent and a task_list call without one, each as the
// announcing item and the result item history carries. It also reads
// domain delegates/list and the legacy tree answer for finished subagents: one
// reported, one the user stopped, one failed.
//
// agent's TestSubagentCallWireFixtures and TestSubagentOutcomeWireFixtures
// produce the fixtures from the daemon's own producers, and re-verify them on
// every Go test run; regenerate them with `make fuzz-goldens`. They are loaded
// through a `?raw` import, as notificationWireFixtures loads its own.

import subagentCalls from "../../../agent/testdata/subagentwire/calls.json?raw";
import subagentOutcomes from "../../../agent/testdata/subagentwire/outcomes.json?raw";
import type { ItemModel, ThreadModel } from "../model";
import { hydrateThread } from "../reducer";
import type { JobsListResponse, SessionDelegatesResponse, Thread, ThreadItem } from "../types.gen";
import { wireThread } from "./notifications";

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

/** The real domain delegates/list response from the same durable finishes. */
export function subagentOutcomesDelegatesResponse(): SessionDelegatesResponse {
  return (JSON.parse(subagentOutcomes) as { delegatesResponse: SessionDelegatesResponse }).delegatesResponse;
}

export function subagentResumedDelegatesResponse(): SessionDelegatesResponse {
  return (JSON.parse(subagentOutcomes) as { resumedDelegatesResponse: SessionDelegatesResponse })
    .resumedDelegatesResponse;
}
