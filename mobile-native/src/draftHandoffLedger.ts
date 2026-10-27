/** The durable shape of one identity-resume draft handoff. The mutations
 * database owns it because it is the only store written in the same savepoint
 * that moves the mutation rows to the resolved identity. Draft bytes stay in
 * the drafts database; this ledger decides who owns them and which recovery
 * dispositions left the source. */

export interface DraftHandoffEndpoint {
	hubId: string;
	sessionRef: string;
}

export type DraftHandoffEdgeState = "pending" | "settled" | "refused" | "cleaned";

/** The dispositions a destination owns are rows in `draft_handoff_deliveries`,
 * never copies on the edge, so the two cannot drift. */
export type DraftHandoffDisposition = "released" | "completed";

export interface DraftHandoffDelivery {
	chainId: string;
	destination: DraftHandoffEndpoint;
	recoveryId: string;
	disposition: DraftHandoffDisposition;
	edgeId: string;
}

export interface DraftHandoffEdge {
	edgeId: string;
	chainId: string;
	from: DraftHandoffEndpoint;
	to: DraftHandoffEndpoint;
	state: DraftHandoffEdgeState;
	/** Edges that must settle or refuse before this one may. */
	dependencies: string[];
	/** Submission obligations this edge carries. An open one blocks settlement. */
	obligations: string[];
	claimAt: number | null;
	deliveredHash: string | null;
	supersededBy: string | null;
	createdAt: number;
	stateChangedAt: number;
}

export type DraftSubmissionState = "open" | "completed" | "failed";

export interface DraftSubmissionObligation {
	obligationId: string;
	/** Null until the first retarget adopts an unclaimed submission. */
	chainId: string | null;
	origin: DraftHandoffEndpoint;
	state: DraftSubmissionState;
	completedCommandIds: string[];
	createdAt: number;
	resolvedAt: number | null;
}

export interface DraftHandoffGraph {
	edges: DraftHandoffEdge[];
	obligations: DraftSubmissionObligation[];
	deliveries: DraftHandoffDelivery[];
}

export interface CreateDraftHandoffEdgeInput {
	chainId: string;
	from: DraftHandoffEndpoint;
	to: DraftHandoffEndpoint;
	dependencies?: readonly string[];
	obligations?: readonly string[];
}

/** The submission side a draft document needs: record the obligation before a
 * transport call, resolve it when the submission settles. */
export interface DraftSubmissionLedger {
	beginSubmission(obligationId: string, origin: DraftHandoffEndpoint): void;
	resolveSubmission(
		obligationId: string,
		state: Exclude<DraftSubmissionState, "open">,
		completedCommandIds?: readonly string[],
	): void;
}

export interface DraftHandoffLedger extends DraftSubmissionLedger {
	/** Called inside the retarget savepoint that moves the mutation rows. */
	recordIdentityChange(from: DraftHandoffEndpoint, to: DraftHandoffEndpoint): { edgeId: string; chainId: string };
	readGraph(): DraftHandoffGraph;
	createEdge(input: CreateDraftHandoffEdgeInput): DraftHandoffEdge;
	/** Claims the destination for this edge. False when another live edge or
	 * chain already owns it. */
	claimDestination(edgeId: string): boolean;
	/** Record the record bytes this edge wrote, before settlement commits. A
	 * retry compares the destination against it to tell its own write from work
	 * added afterwards. */
	recordDelivered(edgeId: string, deliveredHash: string): void;
	settleEdge(edgeId: string, deliveries: readonly DraftHandoffDelivery[], deliveredHash: string): void;
	refuseEdge(edgeId: string, deliveries: readonly DraftHandoffDelivery[]): void;
	cleanEdge(edgeId: string): void;
}

/** The target key is the composite `[hubId, sessionRef]` encoding used by every
 * native mutation row. Retarget receives it on both sides. */
export function decodeDraftEndpoint(targetKey: string): DraftHandoffEndpoint | undefined {
	try {
		const parsed: unknown = JSON.parse(targetKey);
		if (Array.isArray(parsed) && parsed.length === 2 && typeof parsed[0] === "string" && typeof parsed[1] === "string")
			return { hubId: parsed[0], sessionRef: parsed[1] };
	} catch {
		// A bare ref is not a target key; no handoff edge can be built from it.
	}
	return undefined;
}
