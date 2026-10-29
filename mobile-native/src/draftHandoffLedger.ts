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
	/** Set after a successful drafts transfer, so a retry decides against the
	 * destination instead of re-composing from a retired source. */
	composedAt: number | null;
	/** The decision a settled/refused edge reached, retained through cleanup. */
	resolvedAs: "settled" | "refused" | null;
	createdAt: number;
	stateChangedAt: number;
}

/** `not-sent` is a proven local refusal whose capture must be cleared;
 * `failed` never reached a decision and leaves the capture recoverable. */
export type DraftSubmissionState = "open" | "completed" | "not-sent" | "failed";

/** A completed submission, or a proven local refusal, owns the destination
 * capture. A plain failed operation leaves it recoverable, so a resolution in
 * that state must never clear one. */
export function clearsCapture(state: DraftSubmissionState): boolean {
	return state === "completed" || state === "not-sent";
}

export interface DraftSubmissionObligation {
	obligationId: string;
	/** Null until the first retarget adopts an unclaimed submission. */
	chainId: string | null;
	origin: DraftHandoffEndpoint;
	state: DraftSubmissionState;
	/** Recovery sources this submission captured at its origin. */
	recoveryIds: string[];
	completedCommandIds: string[];
	/** Sources the completed submission also releases at the destination. */
	releasedRecoveryIds: string[];
	createdAt: number;
	resolvedAt: number | null;
	/** Null while the completion still owes routine reconciliation work; set
	 * once the completion durably applied at its capture owner - or proved
	 * durably absent - so routine passes stop revisiting it. Optional because
	 * ledgers created before it lack the column. */
	appliedAt?: number | null;
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
	beginSubmission(obligationId: string, origin: DraftHandoffEndpoint, recoveryIds?: readonly string[]): void;
	resolveSubmission(
		obligationId: string,
		state: Exclude<DraftSubmissionState, "open">,
		completedCommandIds?: readonly string[],
		releasedRecoveryIds?: readonly string[],
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
	/** Mark the drafts transfer as committed. */
	recordComposed(edgeId: string): void;
	/** The identities the mutation rows for these recovery ids currently
	 * target, read from the durable outbox and recovery tables. Retargets move
	 * rows with the identity regardless of whether the destination accepted
	 * the draft, so this - not the handoff graph - is the durable row
	 * ownership. Undefined only when the ledger's database has no mutation
	 * tables at all: ownership no ledger can name, and the caller defers
	 * rather than trust a trace the rows may have moved past. A read of
	 * tables that exist but cannot be read throws instead: that ownership is
	 * unknown, never absent, and the caller defers its action. */
	recoveryRowOwners(recoveryIds: readonly string[]): DraftHandoffEndpoint[] | undefined;
	settleEdge(edgeId: string, deliveries: readonly DraftHandoffDelivery[], deliveredHash: string): void;
	refuseEdge(edgeId: string, deliveries: readonly DraftHandoffDelivery[]): void;
	cleanEdge(edgeId: string): void;
	/** A removed hub's drafts are gone, so its edges can never deliver, settle,
	 * or refuse: drop them and their dependency and delivery rows. Obligations
	 * survive - their dispositions can still be owed to identities in other
	 * hubs. */
	removeHub(hubId: string): void;
	/** Durably acknowledge a resolved obligation whose completion has been
	 * applied, so routine reconciliation stops revisiting it. */
	acknowledgeAppliedCompletion(obligationId: string): void;
	/** The graph's outstanding work, bounded: open and unacknowledged
	 * obligations, edges pending or awaiting cleanup, and the edges reachable
	 * from every outstanding obligation's origin - capture routing needs the
	 * path a composition took even after its edges settled and cleaned. History
	 * with nothing outstanding never loads, so routine composer reconciliation
	 * stays bounded by unresolved work. Settled history's deliveries are not
	 * included. */
	readOutstandingGraph(): DraftHandoffGraph;

	/** The edges one identity pair ever carried, in creation order. The
	 * identity-change action is pair-scoped: this reads one pair's own
	 * history instead of the whole graph. */
	edgesBetween(from: DraftHandoffEndpoint, to: DraftHandoffEndpoint): DraftHandoffEdge[];
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
