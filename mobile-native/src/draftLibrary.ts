import {
	type DraftCompletionDelivery,
	type CompletionDeliveryOutcome,
	DraftDocument,
	captureInFlight,
	submissionBeginUnrecorded,
	mergeCompletion,
	messageAdmissionCapture,
} from "./draftDocument";
import { type DraftDestination, type DraftRecord, type DraftRepository, type DraftTransfer } from "./draftRepository";
import {
	clearsCapture,
	type DraftSubmissionLedgerSQLite,
	type DraftSubmissionObligation,
} from "./draftSubmissionLedger";

export type DraftStorage = Pick<
	DraftRepository,
	| "read"
	| "write"
	| "transfer"
	| "removeHub"
	| "imageInputs"
	| "refsWithDrafts"
	| "beginTransfer"
	| "outstandingTransfers"
	| "pendingTransfersTo"
	| "transferById"
	| "rerouteTransfers"
	| "latestCommittedTransferFromCarrying"
	| "latestTransferCarrying"
	| "hasPendingTransferTo"
	| "forgetTransfer"
	| "markTransferSettled"
>;

/** How many committed hops a completion may follow toward the record's current
 * owner. A walk past the cap defers - it never guesses. */
const TRANSFER_WALK_LIMIT = 16;

/** One document per destination keeps pending work coherent across navigation. */
export class DraftLibrary {
	private hubs = new Map<string, Map<string, DraftDocument>>();
	/** Documents retired by a handoff, kept per hub so hub removal still
	 * invalidates a source the live document map no longer holds. */
	private retired = new Map<string, Set<DraftDocument>>();
	/** Only a drafts store too broken to record the durable pending row falls
	 * back to this: the durable handoff state lives in draft_transfer_log. */
	private pending = new Map<string, { from: DraftDestination; to: DraftDestination }>();
	private obligationListeners = new Set<() => void>();
	constructor(
		private repository: () => DraftStorage,
		private createImageId: () => string = () =>
			`reissue-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`,
		private submissionLedger?: () => DraftSubmissionLedgerSQLite,
	) {}

	open(destination: DraftDestination): DraftDocument {
		let sessions = this.hubs.get(destination.hubId);
		if (!sessions) {
			sessions = new Map();
			this.hubs.set(destination.hubId, sessions);
		}
		let document = sessions.get(destination.sessionRef);
		if (!document) {
			document = new DraftDocument(
				this.repository,
				destination,
				this.createImageId,
				undefined,
				undefined,
				this.submissionLedger,
				(completion) => this.deliverCompletion(completion),
				() => this.noteObligationResolved(),
			);
			sessions.set(destination.sessionRef, document);
		}
		return document;
	}

	/** Hand a destination's complete draft, image bytes and settlement markers
	 * to the identity a resume resolved to, before the replacement screen opens.
	 * The intent is durable before any bytes move: a restart finds the pending
	 * hop in the transfer log and finishes it. An already-open destination
	 * adopts the record into its live snapshot, and an in-flight submission on
	 * the retired document settles through the ledger. Only an empty
	 * destination may take the handoff; existing work is a permanent refusal. */
	handoff(from: DraftDestination, to: DraftDestination): boolean {
		if (from.hubId === to.hubId && from.sessionRef === to.sessionRef) return true;
		let transfer: DraftTransfer | undefined;
		try {
			transfer = this.repository()
				.outstandingTransfers()
				.find((row) => sameDestination(row.from, from) && sameDestination(row.to, to));
			if (transfer === undefined) transfer = this.repository().beginTransfer(from, to, this.createTransferId());
		} catch {
			// The drafts store cannot record the hop; keep the in-memory intent
			// so a later call retries once the store is reachable.
			this.deferHandoff(from, to);
			return false;
		}
		this.pending.delete(this.handoffKey(to));
		// A committed hop owes settlement, not another transfer: its bytes
		// already landed, and re-attempting would read the occupied destination
		// as a refusal and destroy the committed pointer.
		if (transfer.deliveredHash !== null) return this.settleTransfer(transfer);
		return this.attemptHandoff(transfer);
	}

	/** Retry what previous calls or previous processes left unfinished: the
	 * ledger writes live and retired documents still owe, the pending hops, and
	 * the completions of resolved obligations. Every step is idempotent, so a
	 * pass that finds nothing to do costs only bounded outstanding reads. */
	retryPendingHandoffs(): void {
		for (const document of this.allDocuments()) document.flushObligationResolutions();
		// Hops the drafts store could not even record: the in-memory intent
		// retries first, recording the durable row once the store answers. A
		// commit earlier in this loop can reroute a later entry to a new
		// destination: each entry runs only as the map holds it now, never
		// the stale snapshot this loop was planned from - a changed entry
		// waits for the next pass.
		for (const { from, to } of [...this.pending.values()]) {
			const current = this.pending.get(this.handoffKey(to));
			if (current === undefined || !sameDestination(current.from, from) || !sameDestination(current.to, to)) continue;
			this.handoff(from, to);
		}
		let transfers: DraftTransfer[];
		try {
			transfers = this.repository().outstandingTransfers();
		} catch {
			transfers = [];
		}
		for (const transfer of transfers) {
			// A hop earlier in this batch can reroute this row atomically with
			// its own commit: execute the durable row as it stands now, never
			// the stale object the batch was planned from.
			let current: DraftTransfer | undefined;
			try {
				current = this.repository().transferById(transfer.transferId);
			} catch {
				continue;
			}
			if (current === undefined) continue;
			if (current.deliveredHash === null) this.attemptHandoff(current);
			else this.settleTransfer(current);
		}
		this.reconcileResolvedObligations();
	}

	/** Whether a transfer is still on its way into this destination. Unknown
	 * when the store cannot answer: unknown pendingness is not no-pending. */
	hasPendingHandoff(to: DraftDestination): boolean | undefined {
		if (this.pending.has(this.handoffKey(to))) return true;
		try {
			return this.repository().hasPendingTransferTo(to);
		} catch {
			return undefined;
		}
	}

	/** Whether any resolved obligation still owes its completion's delivery:
	 * true while one exists, false when none does, undefined when the ledger
	 * cannot answer. Delivery runs in the retry pass, so a screen that sees
	 * this must keep that pass running - storage recovering alone must
	 * converge the capture with no user action. */
	hasUndeliveredCompletions(): boolean | undefined {
		const ledger = this.availableSubmissionLedger();
		if (ledger === undefined) return undefined;
		let obligations: DraftSubmissionObligation[];
		try {
			obligations = ledger.outstandingObligations();
		} catch {
			return undefined;
		}
		// An open obligation abandoned by a dead process owes its failed
		// resolution too: the retry pass releases it once the store accepts
		// writes. Ones still running in this process resolve on their own
		// transitions and never hold the timer.
		return obligations.some(
			(obligation) =>
				clearsCapture(obligation.state) || (obligation.state === "open" && !captureInFlight(obligation.obligationId)),
		);
	}

	/** The recovery ids this identity must not restore: the input of every
	 * submission still running - or resolved but not yet delivered - whose
	 * origin or durable capture sits here.
	 * Undefined when ownership cannot be read - unknown is not empty. */
	openObligationRecoveryIds(endpoint: DraftDestination): ReadonlySet<string> | undefined {
		const ledger = this.availableSubmissionLedger();
		if (ledger === undefined) return undefined;
		let obligations: DraftSubmissionObligation[];
		try {
			obligations = ledger.outstandingObligations();
		} catch {
			return undefined;
		}
		const ids = new Set<string>();
		for (const obligation of obligations) {
			// A resolved-but-undelivered completion still owns its capture:
			// its sources stay suppressed until the delivery lands, applies
			// its markers and the acknowledgment retires the obligation.
			if (obligation.state !== "open" && !clearsCapture(obligation.state)) continue;
			if (sameDestination(obligation.origin, endpoint)) {
				for (const id of obligation.recoveryIds) ids.add(id);
				continue;
			}
		}
		// A capture this endpoint durably holds belongs to whatever submission
		// owns it, wherever that submission began.
		let record: DraftRecord | undefined;
		try {
			record = this.repository().read(endpoint);
		} catch {
			return undefined;
		}
		if (record !== undefined) {
			for (const obligation of obligations) {
				if (
					(obligation.state === "open" || clearsCapture(obligation.state)) &&
					record.unconfirmedCaptureToken === obligation.obligationId
				)
					for (const id of obligation.recoveryIds) ids.add(id);
			}
			// A begin that never durably recorded still owns its capture: the
			// ledger cannot show it, so only this check keeps its sources
			// suppressed. A failed operation's capture is not suppressed -
			// its input stays restorable.
			if (record.unconfirmedCaptureToken !== undefined && submissionBeginUnrecorded(record.unconfirmedCaptureToken))
				for (const id of record.unconfirmedComposer?.recoveryIds ?? []) ids.add(id);
		}
		return ids;
	}

	/** Subscribe to obligation resolutions - completed, not-sent, or failed -
	 * so a mounted screen re-runs the reconciliation a resolution unblocks. */
	onObligationTransition(listener: () => void): () => void {
		this.obligationListeners.add(listener);
		return () => {
			this.obligationListeners.delete(listener);
		};
	}

	private noteObligationResolved(): void {
		for (const listener of this.obligationListeners) listener();
	}

	private createTransferId(): string {
		return `transfer-${this.createImageId()}`;
	}

	/** One attempt at a durably intended hop. Transient failures leave the
	 * row pending for the next pass; only a committed transfer or a proven
	 * occupied destination takes the row out of the pending set. */
	private attemptHandoff(transfer: DraftTransfer): boolean {
		const source = this.open(transfer.from);
		let snapshot = source.getSnapshot();
		if (!snapshot.loaded) {
			source.retry();
			snapshot = source.getSnapshot();
		}
		if (!snapshot.loaded) return false;
		const destination = this.open(transfer.to);
		let current = destination.getSnapshot();
		if (!current.loaded || current.error !== null) {
			destination.retry();
			current = destination.getSnapshot();
		}
		if (!current.loaded || current.error !== null || current.submitting) return false;
		if (destinationContent(current.record)) {
			this.refuseHandoff(transfer, source, destination);
			return false;
		}
		// A completion that resolved while this hop could not commit lives
		// only in its unacknowledged obligation: the source record's own
		// markers may already have been stripped by the screen cleanup whose
		// target-scoped deletion found the retargeted rows gone. The hop
		// preserves that evidence in its own savepoint, written atomically
		// with the move it evidences - or an unreadable ledger defers the
		// commit, which never writes without the evidence.
		const owed = this.resolvedObligationsAt(transfer.from);
		if (owed === undefined) return false;
		const completed = [...new Set([...(snapshot.record.completedCommandRecoveryIds ?? []), ...owed.completed])];
		const released = [...new Set([...releasedSources(snapshot.record), ...owed.released])];
		// A destination that consumed its composition keeps only its
		// historical settlement markers. Markers are evidence about work
		// already done, never work: they must not strand an incoming hop at
		// a retired identity. The incoming record merges them in, so the
		// destination still cannot restore what its own commands consumed.
		const delivered = mergeDispositionMarkers(
			completed.length ? { ...snapshot.record, completedCommandRecoveryIds: completed } : snapshot.record,
			current.record,
		);
		// The retry always moves the source's current record: work composed
		// after the first attempt travels with the hop instead of being
		// stranded at the old identity, and no stale snapshot is ever
		// transferred over newer source work.
		const images = [
			...(snapshot.record.images ?? []),
			...(snapshot.record.unconfirmedImages ?? []),
			...(snapshot.record.parkedCompositions ?? []).flatMap((parked) => parked.images ?? []),
		];
		try {
			// This hop retires its source: anything still on its way in must
			// follow the work onward, atomically with the commit, or it would
			// later deliver into the identity this hop just emptied.
			const reroutes = this.repository()
				.pendingTransfersTo(transfer.from)
				.map((incoming) => ({ transferId: incoming.transferId, to: transfer.to }));
			const reroutedIn = this.pending.get(this.handoffKey(transfer.from));
			const additions = images.map((image) => {
				const data = source.imagePreviews([image])[0]?.data;
				if (!data) throw new Error("Saved image is unavailable.");
				return { ...image, data };
			});
			this.repository().transfer(
				transfer.from,
				transfer.to,
				delivered,
				additions,
				{
					transferId: transfer.transferId,
					deliveredHash: compositionFingerprint(snapshot.record),
					carriedCaptureToken: snapshot.record.unconfirmedCaptureToken,
					completedCommandRecoveryIds: completed,
					releasedRecoveryIds: released,
				},
				reroutes,
			);
			// An in-memory intent waiting on the retired destination follows the
			// same reroute, or its next pass would re-record the old pair.
			if (reroutedIn !== undefined) {
				this.pending.delete(this.handoffKey(transfer.from));
				if (!this.pending.has(this.handoffKey(transfer.to)))
					this.pending.set(this.handoffKey(transfer.to), { from: reroutedIn.from, to: transfer.to });
			}
		} catch {
			return false;
		}
		// Only this hop's own unrecorded fallback goes: a rerouted incoming
		// intent waiting on the same key must survive until it durably records.
		const outgoing = this.pending.get(this.handoffKey(transfer.to));
		if (outgoing !== undefined && sameDestination(outgoing.from, transfer.from))
			this.pending.delete(this.handoffKey(transfer.to));
		destination.adopt(delivered);
		if (destination !== source) {
			source.retire();
			this.rememberRetired(transfer.from.hubId, source);
			const sessions = this.hubs.get(transfer.from.hubId);
			if (sessions?.get(transfer.from.sessionRef) === source) sessions.delete(transfer.from.sessionRef);
		}
		// The hop's dispositions are owed to whoever owns the record now: the
		// savepoint wrote the real arrays, so settle from them, not from the
		// pre-transfer row object.
		this.settleTransfer({
			...transfer,
			deliveredHash: compositionFingerprint(snapshot.record),
			carriedCaptureToken: snapshot.record.unconfirmedCaptureToken ?? null,
			completedCommandRecoveryIds: completed,
			releasedRecoveryIds: released,
		});
		return true;
	}

	/** The completion evidence a hop out of `origin` must carry with its
	 * commit: the dispositions of resolved obligations originating there
	 * that still owe their delivery. Their markers may already be gone from
	 * the source record - a screen cleanup whose target-scoped deletion
	 * found the retargeted rows gone strips them - so the obligation rows
	 * are the evidence. A ledger this library does not have owes nothing;
	 * one that exists but cannot answer is unknown evidence, and unknown
	 * defers the commit. */
	private resolvedObligationsAt(origin: DraftDestination): { completed: string[]; released: string[] } | undefined {
		let ledger: DraftSubmissionLedgerSQLite | undefined;
		try {
			ledger = this.submissionLedger?.();
		} catch {
			return undefined;
		}
		if (ledger === undefined) return { completed: [], released: [] };
		let obligations: DraftSubmissionObligation[];
		try {
			obligations = ledger.outstandingObligations();
		} catch {
			return undefined;
		}
		const completed: string[] = [];
		const released: string[] = [];
		for (const obligation of obligations) {
			if (!clearsCapture(obligation.state) || !sameDestination(obligation.origin, origin)) continue;
			completed.push(...obligation.completedCommandIds);
			released.push(...obligation.releasedRecoveryIds);
		}
		return { completed, released };
	}

	/** An occupied destination is a permanent refusal: nothing moved, so the
	 * row goes away entirely and can never pose as a pointer. The refused
	 * composition's cited sources are released at the destination durably; a
	 * failed release leaves the row pending so the next pass converges it. */
	private refuseHandoff(transfer: DraftTransfer, source: DraftDocument, destination: DraftDocument): void {
		const record = source.getSnapshot().record;
		// The refusal delivers everything the composition had settled so far -
		// completed commands and prior releases as much as the still-cited
		// sources - or the rows a retarget already moved could restore content
		// an executed command consumed.
		const completed = [...(record.completedCommandRecoveryIds ?? [])];
		const released = releasedSources(record);
		try {
			if (completed.length && !destination.adoptCompletedCommandRecovery(completed)) return;
			if (released.length && !destination.releaseRecoverySources(released)) return;
			const ledger = this.availableSubmissionLedger();
			if (ledger !== undefined && (completed.length || released.length))
				ledger.writeDeliveries([
					...completed.map((recoveryId) => ({
						destination: transfer.to,
						recoveryId,
						disposition: "completed" as const,
						transferId: transfer.transferId,
					})),
					...released.map((recoveryId) => ({
						destination: transfer.to,
						recoveryId,
						disposition: "released" as const,
						transferId: transfer.transferId,
					})),
				]);
			// This hop followed an identity change: the rows moved onward even
			// though the composition itself was refused, so everything still
			// on its way into the old identity must follow them - the refusal
			// stays pending until that rerouting persists.
			this.repository().rerouteTransfers(
				this.repository()
					.pendingTransfersTo(transfer.from)
					.map((incoming) => ({ transferId: incoming.transferId, to: transfer.to })),
			);
			const reroutedIn = this.pending.get(this.handoffKey(transfer.from));
			if (reroutedIn !== undefined) {
				this.pending.delete(this.handoffKey(transfer.from));
				if (!this.pending.has(this.handoffKey(transfer.to)))
					this.pending.set(this.handoffKey(transfer.to), { from: reroutedIn.from, to: transfer.to });
			}
			this.repository().forgetTransfer(transfer.transferId);
			// The same rule as the committed hop's cleanup: a rerouted
			// incoming intent on this key outlives the refused hop's fallback.
			const refused = this.pending.get(this.handoffKey(transfer.to));
			if (refused !== undefined && sameDestination(refused.from, transfer.from))
				this.pending.delete(this.handoffKey(transfer.to));
		} catch {
			// The refusal stays pending until its writes commit.
		}
	}

	/** A committed hop is settled when its dispositions are durably delivered
	 * to the identity that owns the record now - the destination, or wherever
	 * later hops carried it. Unreadable ownership defers; a settled row never
	 * comes back. */
	private settleTransfer(transfer: DraftTransfer): boolean {
		if (transfer.deliveredHash === null) return false;
		// The dispositions belong to the record this row moved: it landed at
		// the row's destination, and later hops from there carried it onward.
		const owner = this.recordOwner(transfer.to, transfer.carriedGeneration ?? null);
		if (owner === undefined) return false;
		const document = this.open(owner);
		// An owner that cannot load must not receive markers: persisting an
		// unloaded document would write its default empty record over the
		// owner's saved draft and delete its image bytes. Defer instead.
		let ownerSnapshot = document.getSnapshot();
		if (!ownerSnapshot.loaded) {
			document.retry();
			ownerSnapshot = document.getSnapshot();
		}
		if (!ownerSnapshot.loaded) return false;
		try {
			// A marker write that did not persist leaves the settlement owed:
			// the document methods answer false, and the row stays pending so
			// the next pass retries the whole settlement.
			if (
				transfer.completedCommandRecoveryIds.length &&
				!document.adoptCompletedCommandRecovery(transfer.completedCommandRecoveryIds)
			)
				return false;
			if (transfer.releasedRecoveryIds.length && !document.releaseRecoverySources(transfer.releasedRecoveryIds))
				return false;
			const ledger = this.availableSubmissionLedger();
			if (ledger !== undefined)
				ledger.writeDeliveries([
					...transfer.completedCommandRecoveryIds.map((recoveryId) => ({
						destination: owner,
						recoveryId,
						disposition: "completed" as const,
						transferId: transfer.transferId,
					})),
					...transfer.releasedRecoveryIds
						.filter((recoveryId) => !transfer.completedCommandRecoveryIds.includes(recoveryId))
						.map((recoveryId) => ({
							destination: owner,
							recoveryId,
							disposition: "released" as const,
							transferId: transfer.transferId,
						})),
				]);
			this.repository().markTransferSettled(transfer.transferId);
			return true;
		} catch {
			return false;
		}
	}

	/** The identity that owns the record generation `generation`, starting
	 * from `start`: follow the committed hops that carried that same
	 * generation, at most TRANSFER_WALK_LIMIT of them. A hop out of an
	 * identity that carried other work - earlier or later - moved a
	 * different record and is not a pointer for this settlement. A row whose
	 * generation is unrecorded predates generation routing: its destination
	 * is the last identity it provably reached. Unknown defers - the caller
	 * never guesses an owner. */
	private recordOwner(start: DraftDestination, generation: string | null): DraftDestination | undefined {
		if (generation === null) return start;
		let at = start;
		for (let hops = 0; hops < TRANSFER_WALK_LIMIT; hops++) {
			let next: DraftTransfer | undefined;
			try {
				next = this.repository().latestCommittedTransferFromCarrying(at, generation);
			} catch {
				return undefined;
			}
			if (next === undefined) return at;
			at = next.to;
		}
		return undefined;
	}

	/** The identity whose record durably holds a submission's capture. While
	 * the submission is unresolved its token travels inside the record, and
	 * every hop of that record carries it in the transfer evidence - so the
	 * latest row carrying the token names where the record went, whatever
	 * other work later moved through the identities along the way. The token
	 * match at the destination proves it; unknown defers. */
	private findCaptureOwner(origin: DraftDestination, captureToken: string): DraftDestination | undefined {
		let hop: DraftTransfer | undefined;
		try {
			hop = this.repository().latestTransferCarrying(captureToken);
		} catch {
			return undefined;
		}
		// No hop carried the token: the record never moved, so the origin is
		// where the capture lives or was cleared. The delivery's own token
		// check turns a cleared capture into a proven-absent completion.
		return hop === undefined ? origin : hop.to;
	}

	/** Deliver a resolved submission's completion at the identity that owns the
	 * record now. The obligation names the origin; the transfer pointers name
	 * the owner. A delivery that lands - or provably finds the capture already
	 * gone, with every read successful - acknowledges the obligation so the
	 * routine pass never revisits it. */
	private deliverCompletion(completion: DraftCompletionDelivery): CompletionDeliveryOutcome {
		const ledger = this.availableSubmissionLedger();
		if (ledger === undefined) return "unowned";
		let obligation: DraftSubmissionObligation | undefined;
		try {
			obligation = ledger.obligationById(completion.captureToken);
		} catch {
			return "unreadable";
		}
		if (obligation === undefined || !clearsCapture(obligation.state)) return "unreadable";
		const owner = this.findCaptureOwner(obligation.origin, completion.captureToken);
		if (owner === undefined) return "unreadable";
		const outcome = this.deliverCompletionAt(owner, completion);
		if (
			(outcome === "delivered" || outcome === "unowned") &&
			!this.deliverRoutedCompletions(owner, obligation.origin, completion)
		)
			return "unreadable";
		if (outcome === "delivered" || outcome === "unowned") {
			try {
				ledger.acknowledgeApplied(completion.captureToken);
			} catch {
				// The pass re-delivers and re-acknowledges.
			}
		}
		return outcome;
	}

	/** A refused handoff releases the still-cited sources of live submissions
	 * at the occupied destination - a retarget may have moved their rows
	 * there. When such a submission completes, its executed input's completed
	 * markers must reach every destination holding delivery evidence for them
	 * before the obligation is acknowledged: released rows stay editable,
	 * completed ones never restore. A delivery that cannot land leaves the
	 * obligation unacknowledged so the next pass retries it. */
	private deliverRoutedCompletions(
		owner: DraftDestination,
		origin: DraftDestination,
		completion: DraftCompletionDelivery,
	): boolean {
		if (completion.completedCommandIds.length === 0) return true;
		const ledger = this.availableSubmissionLedger();
		if (ledger === undefined) return false;
		let routed: DraftDestination[];
		try {
			const seen = new Map(
				completion.completedCommandIds
					.flatMap((recoveryId) => ledger.deliveryDestinations(recoveryId))
					.map((destination) => [destination.hubId + "\u0000" + destination.sessionRef, destination]),
			);
			// A committed hop still owing settlement can carry the executed
			// input onward before any delivery record exists for it: the
			// current owner of its record must receive the completed markers
			// too, or a later settlement would leave the migrated source
			// editable there. An owner that cannot be named defers the
			// acknowledgment rather than guessing.
			for (const transfer of this.repository().outstandingTransfers()) {
				if (transfer.deliveredHash === null) {
					// An uncommitted hop out of the completion's origin has
					// already taken that identity's recovery rows with it -
					// the retarget moved them when the hop was recorded. Its
					// destination must hold the executed command's markers
					// before the obligation may acknowledge: the source
					// screen's own cleanup - its target-scoped deletion finds
					// the retargeted rows gone - strips the source record's
					// markers, and the recovered hop would otherwise settle
					// from a marker-free source with the executed input
					// restorable at the destination. A destination that
					// cannot take them defers the acknowledgment to the
					// next pass.
					if (!sameDestination(transfer.from, origin)) continue;
					seen.set(transfer.to.hubId + "\u0000" + transfer.to.sessionRef, transfer.to);
					continue;
				}
				const carries = [...transfer.completedCommandRecoveryIds, ...transfer.releasedRecoveryIds].some((recoveryId) =>
					completion.completedCommandIds.includes(recoveryId),
				);
				if (!carries) continue;
				const carrying = this.recordOwner(transfer.to, transfer.carriedGeneration ?? null);
				if (carrying === undefined) return false;
				seen.set(carrying.hubId + "\u0000" + carrying.sessionRef, carrying);
			}
			routed = [...seen.values()].filter((destination) => !sameDestination(destination, owner));
		} catch {
			return false;
		}
		if (routed.length === 0) return true;
		try {
			for (const destination of routed) {
				const document = this.open(destination);
				let destinationSnapshot = document.getSnapshot();
				if (!destinationSnapshot.loaded) {
					document.retry();
					destinationSnapshot = document.getSnapshot();
				}
				if (!destinationSnapshot.loaded) return false;
				if (!document.adoptCompletedCommandRecovery(completion.completedCommandIds)) return false;
			}
			ledger.writeDeliveries(
				routed.flatMap((destination) =>
					completion.completedCommandIds.map((recoveryId) => ({
						destination,
						recoveryId,
						disposition: "completed" as const,
						transferId: completion.captureToken,
					})),
				),
			);
		} catch {
			return false;
		}
		return true;
	}

	/** Merge a completion into the live document at the owner when one is
	 * open, else into its persisted record. Reads that fail are unknown, never
	 * proof the capture is gone. */
	private deliverCompletionAt(owner: DraftDestination, completion: DraftCompletionDelivery): CompletionDeliveryOutcome {
		const open = this.hubs.get(owner.hubId)?.get(owner.sessionRef);
		const live = open?.getSnapshot();
		if (open !== undefined && live?.loaded) {
			if (open.adoptCompletion(completion)) return "delivered";
			// The adoption's own save can fail after the in-memory merge: the
			// errored live document retries the completion through its own
			// persistence path instead of a direct write behind it.
			if (open.getSnapshot().error !== null) return open.mergeCompletionThroughLiveState(completion);
		}
		let current: DraftRecord;
		try {
			current = this.repository().read(owner);
		} catch {
			return "unreadable";
		}
		if (current.unconfirmedCaptureToken !== completion.captureToken) return "unowned";
		// A recovered message's capture settles through its resend receipt,
		// which the receipt-acknowledging coordinator clears.
		if (messageAdmissionCapture(completion, current)) return "delivered";
		try {
			this.repository().write(owner, mergeCompletion(current, completion));
		} catch {
			return "unreadable";
		}
		return "delivered";
	}

	/** The recovery pass over unresolved obligations: open ones abandoned by a
	 * previous process release their obsolete in-flight ownership while their
	 * uncertain input stays recoverable; resolved ones get their completion
	 * delivered at the record's current owner. Nothing here reads history the
	 * partial index has already priced out. */
	private reconcileResolvedObligations(): void {
		const ledger = this.availableSubmissionLedger();
		if (ledger === undefined) return;
		let obligations: DraftSubmissionObligation[];
		try {
			obligations = ledger.outstandingObligations();
		} catch {
			return;
		}
		for (const obligation of obligations) {
			if (obligation.state === "open") {
				// This process holds its own in-flight submissions; anything
				// else was abandoned by a process that died mid-submission.
				if (captureInFlight(obligation.obligationId)) continue;
				try {
					ledger.resolveSubmission(obligation.obligationId, "failed", [], []);
				} catch {
					// The store is failing; the next pass retries.
				}
				continue;
			}
			if (!clearsCapture(obligation.state)) continue;
			const outcome = this.deliverCompletion({
				captureToken: obligation.obligationId,
				state: obligation.state === "completed" ? "completed" : "not-sent",
				completedCommandIds: obligation.completedCommandIds,
				releasedRecoveryIds: obligation.releasedRecoveryIds,
			});
			if (outcome === "delivered" || outcome === "unowned") {
				try {
					ledger.acknowledgeApplied(obligation.obligationId);
				} catch {
					// The next pass re-acknowledges.
				}
			}
		}
	}

	private availableSubmissionLedger(): DraftSubmissionLedgerSQLite | undefined {
		try {
			return this.submissionLedger?.();
		} catch {
			return undefined;
		}
	}

	private allDocuments(): DraftDocument[] {
		const documents = [...[...this.hubs.values()].flatMap((sessions) => [...sessions.values()])];
		for (const set of this.retired.values()) documents.push(...set);
		return documents;
	}

	private deferHandoff(from: DraftDestination, to: DraftDestination): void {
		this.pending.set(this.handoffKey(to), { from, to });
	}

	private rememberRetired(hubId: string, document: DraftDocument): void {
		let documents = this.retired.get(hubId);
		if (!documents) {
			documents = new Set();
			this.retired.set(hubId, documents);
		}
		documents.add(document);
	}

	private handoffKey(destination: DraftDestination): string {
		return JSON.stringify([destination.hubId, destination.sessionRef]);
	}

	refsWithDrafts(hubId: string): Set<string> {
		return this.repository().refsWithDrafts(hubId);
	}

	removeHub(hubId: string) {
		try {
			this.repository().removeHub(hubId);
		} finally {
			for (const [key, { from, to }] of this.pending)
				if (from.hubId === hubId || to.hubId === hubId) this.pending.delete(key);
			for (const document of this.retired.get(hubId) ?? []) document.forget();
			this.retired.delete(hubId);
			for (const document of this.hubs.get(hubId)?.values() ?? []) document.forget();
			this.hubs.delete(hubId);
		}
	}
}

function sameDestination(a: DraftDestination, b: DraftDestination): boolean {
	return a.hubId === b.hubId && a.sessionRef === b.sessionRef;
}

/** Content a user or a running submission still owns at the destination: a
 * hop must never overwrite it, so its presence is a permanent refusal. */
function destinationContent(record: DraftRecord): boolean {
	return (
		record.draft !== "" ||
		(record.images?.length ?? 0) > 0 ||
		(record.unconfirmedImages?.length ?? 0) > 0 ||
		record.unconfirmed !== null ||
		(record.composer?.recoveryIds?.length ?? 0) > 0 ||
		(record.unconfirmedComposer?.recoveryIds?.length ?? 0) > 0 ||
		(record.parkedCompositions?.length ?? 0) > 0 ||
		(record.composer?.skillNames?.length ?? 0) > 0 ||
		(record.unconfirmedComposer?.skillNames?.length ?? 0) > 0 ||
		(record.composer?.commandNames?.length ?? 0) > 0 ||
		(record.unconfirmedComposer?.commandNames?.length ?? 0) > 0
	);
}

/** The incoming record with the resident markers merged in: the union keeps
 * every disposition either side settled, so nothing either composition
 * consumed can restore at the destination. */
function mergeDispositionMarkers(incoming: DraftRecord, resident: DraftRecord): DraftRecord {
	const union = (ids?: readonly string[], more?: readonly string[]): readonly string[] | undefined =>
		ids?.length || more?.length ? [...new Set([...(ids ?? []), ...(more ?? [])])] : undefined;
	const released = union(incoming.releasedRecoveryIds, resident.releasedRecoveryIds);
	const completed = union(incoming.completedCommandRecoveryIds, resident.completedCommandRecoveryIds);
	const acks = union(incoming.recoveryResendAcks, resident.recoveryResendAcks);
	if (released === undefined && completed === undefined && acks === undefined) return incoming;
	const merged: DraftRecord = { ...incoming };
	if (released !== undefined) merged.releasedRecoveryIds = [...released];
	if (completed !== undefined) merged.completedCommandRecoveryIds = [...completed];
	if (acks !== undefined) merged.recoveryResendAcks = [...acks];
	return merged;
}

/** The recovery ids a record's composition still cites, plus the ones prior
 * settlements marked released: the dispositions a hop's transfer carries. */
function releasedSources(record: DraftRecord): string[] {
	return [
		...new Set([
			...(record.releasedRecoveryIds ?? []),
			...(record.composer?.recoveryIds ?? []),
			...(record.unconfirmedComposer?.recoveryIds ?? []),
			...(record.parkedCompositions ?? []).flatMap((parked) => parked.composer?.recoveryIds ?? []),
		]),
	];
}

/** A stable identity for a composition, independent of the settlement markers
 * and captures the destination's later life can change. */
function compositionFingerprint(record: DraftRecord): string {
	const canonical = JSON.stringify({
		draft: record.draft,
		images: (record.images ?? []).map((image) => [image.id, image.marker, image.mediaType]),
		composer: record.composer?.recoveryIds ?? [],
		parked: (record.parkedCompositions ?? []).map((parked) => [
			parked.text,
			parked.composer?.recoveryIds ?? [],
			(parked.images ?? []).map((image) => image.id),
		]),
	});
	let hash = 2166136261;
	for (let index = 0; index < canonical.length; index++) {
		hash ^= canonical.charCodeAt(index);
		hash = Math.imul(hash, 16777619);
	}
	return `${(hash >>> 0).toString(16)}-${canonical.length}`;
}
