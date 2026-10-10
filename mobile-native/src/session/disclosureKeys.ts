// What a transcript row's open state is stored under, in the native
// disclosure store. Row ids change as history records what the live stream
// showed (a tool the overlay showed as tool:call:<callId>, or
// tool:<historyKey>, becomes item_tool_<entry>_<part>), so keys use what
// holds: a call's id. A run is its steps: it is open by its members' latest
// choice, and a choice is written to every member, so the run keeps its state
// as calls join it (a parallel call settling late, an older page extending its
// turn) and as live steps are added.
import { scopedDisclosureId } from "@evener/appwire-client";
import type { RunStep, TimelineRow } from "../timeline";

/** The disclosure store scope one session's rows share. */
export function sessionDisclosureScope(hubId: string, sessionRef: string): string {
	return JSON.stringify([hubId, sessionRef]);
}

/** The ids a row's open state is read from and written to: one per member
 * call for a run, one for any other row. A step keys like the activity row
 * it unfolds to, so it keeps its open state across a level change. */
export function rowDisclosureIds(hubId: string, sessionRef: string, row: TimelineRow | RunStep): string[] {
	const scope = sessionDisclosureScope(hubId, sessionRef);
	const keys =
		row.kind === "run" && row.steps.length > 0
			? row.steps.map((step) => ["run-member", ...memberKey(step)])
			: [row.kind === "activity" ? ["activity", ...memberKey(row)] : [row.kind, row.id]];
	return keys.map((key) => scopedDisclosureId(scope, JSON.stringify(key)));
}

/** The disclosure id for a memory refresh's nested literal Source. It shares
 * the row's hub/session scope but sits under its own key, so the Source folds
 * independently of the outer refresh and keeps its explicit choice through a
 * remount or a verbosity change. */
export function memoryContextSourceDisclosureId(hubId: string, sessionRef: string, row: TimelineRow): string {
	const scope = sessionDisclosureScope(hubId, sessionRef);
	return scopedDisclosureId(scope, JSON.stringify(["memory-context-source", row.kind, row.id]));
}

// Call ids are the provider's, and a call never spans a turn, so the turn
// rides along. A step with no call id keeps its row id.
function memberKey(step: RunStep | Extract<TimelineRow, { kind: "activity" }>): string[] {
	return step.detail.callId ? [step.turnId ?? "", step.detail.callId] : ["", step.id];
}
