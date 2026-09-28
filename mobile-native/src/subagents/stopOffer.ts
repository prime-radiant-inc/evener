// What a running subagent's bar offers to stop it (spec 9, ruling 10, S6).
import type { SubagentRow } from "./subagentModel";

export type StopOfferKind = "stop" | "ask" | "requested" | "none";

/**
 * - A request already sent says so.
 * - A hub that can stop a subagent directly (S6) offers it while the
 *   subagent's own run goes on: the direct stop ends that run alone.
 * - Otherwise the coordinator is asked while anything under the subagent
 *   still works (ruling 10).
 * - Until the coordinator's thread says which, nothing is offered.
 */
export function stopOffer({
	row,
	requested,
	direct,
}: {
	row: Pick<SubagentRow, "state" | "active"> | null;
	requested: boolean;
	direct: "yes" | "no" | "unknown";
}): StopOfferKind {
	if (!row) return "none";
	if (requested) return "requested";
	if (direct === "unknown") return "none";
	if (direct === "yes") return row.state === "running" ? "stop" : "none";
	return row.active ? "ask" : "none";
}
