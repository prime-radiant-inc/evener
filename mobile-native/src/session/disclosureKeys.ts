// What a transcript row's open state is stored under, in the native
// disclosure store. Row ids change as history records what the live stream
// showed (a tool the overlay showed as tool:call:<callId>, or
// tool:<historyKey>, becomes item_tool_<entry>_<part>), so keys use what
// holds: a step's call id, and a run's place among its turn's runs.
import { scopedDisclosureId } from "@evener/appwire-client";
import type { RunStep, TimelineRow } from "../timeline";

/** The disclosure store scope one session's rows share. */
export function sessionDisclosureScope(hubId: string, sessionRef: string): string {
	return JSON.stringify([hubId, sessionRef]);
}

/** A row's (or a run's step's) disclosure id. A step keys like the activity
 * row it unfolds to, so it keeps its open state across a level change. */
export function rowDisclosureId(hubId: string, sessionRef: string, row: TimelineRow | RunStep): string {
	return scopedDisclosureId(sessionDisclosureScope(hubId, sessionRef), JSON.stringify(rowDisclosureKey(row)));
}

// Call ids are the provider's, and neither a step nor a run spans a turn, so
// the turn rides along.
function rowDisclosureKey(row: TimelineRow | RunStep): string[] {
	if (row.kind === "run" && row.ordinal !== undefined) return ["run", row.turnId ?? "", String(row.ordinal)];
	if (row.kind === "activity" && row.detail.callId) return ["activity-call", row.turnId ?? "", row.detail.callId];
	return [row.kind, row.id];
}
