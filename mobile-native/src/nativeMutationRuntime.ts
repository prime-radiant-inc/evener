import { openDatabaseSync } from "expo-sqlite";
import * as Crypto from "expo-crypto";
import type { AppwireClientLike, ThreadReadResponse } from "@evener/appwire-client";
import { collectAuthoritativeMutationIds } from "@evener/appwire-client";
import {
	COMPOSER_METHODS,
	DIRECT_FALLBACK_METHODS,
	createClientIdentity,
	createSecureUUID,
	canEditQueuedMessage,
	canRemoveQueuedMessage,
	validConsumedClientMutationIds,
	MutationDispatcher,
	MutationOutbox,
	type MutationAttachmentRef,
	type MutationIntent,
	type MutationOutboxRecord,
	type MutationOutboxOptions,
	type MutationStopBarrier,
	type MutationOutboxStorage,
	type MutationPersistenceSnapshot,
	type MutationRecoveryRecord,
	type QueuedMessageStorage,
	type SecureRandomSource,
} from "@evener/appwire-client/state/mutation";
import { READ_ITEM_LIMIT } from "../../mobile/src/services/conversation";
import {
	type ConversationMutationAdmission,
	type ConversationMutationKind,
	type ConversationMutationRequest,
	type ConversationMutationSubmitter,
	MutationNotSentError,
} from "../../mobile/src/state/conversationMutation";
import { MutationOutboxSQLite, type RecoveryResendCommit } from "./mutationOutboxStorage";
import type { ResumeIdentityEdge, ResumeIdentityEndpoint } from "./resumeIdentity";
import { isSqliteStorageFailure, type SqliteSync } from "./sqliteSync";

export interface NativeMutationRuntimeOptions {
	createMutationId?: () => string;
	now?: () => number;
	getOwnClientId?: () => string | undefined;
	setInterval?: MutationOutboxOptions["setInterval"];
	clearInterval?: MutationOutboxOptions["clearInterval"];
}

type NativeStorage = MutationOutboxStorage<MutationAttachmentRef> &
	QueuedMessageStorage & {
		readRecoveryResend(targetRef: string, sourceIds: readonly string[]): Promise<RecoveryResendCommit | undefined>;
		acknowledgeRecoveryResend(targetRef: string, clientMutationId: string): Promise<void>;
		listOutbox(targetRef?: string): Promise<MutationOutboxRecord<MutationAttachmentRef>[]>;
		readStopEpoch(targetRef: string): Promise<number>;
		listRecovery(targetRef: string): Promise<MutationRecoveryRecord<MutationAttachmentRef>[]>;
		discardRecovery(clientMutationId: string, targetRef: string): Promise<boolean>;
		discardUndelivered(clientMutationId: string, targetRef: string): Promise<boolean>;
		releaseCanceled(clientMutationId: string, targetRef: string, barrier: MutationStopBarrier): Promise<boolean>;
		/** Moves a resumed session's waiting rows to its resolved identity and
		 * returns the ids that moved. */
		retarget(ref: string, to: { ref: string; wireRef: string; threadId?: string; instanceId?: string }): string[];
		/** Identity-changing resumes whose draft handoff is still owed. */
		readResumeIdentities(): ResumeIdentityEdge[];
		removeResumeIdentity(from: ResumeIdentityEndpoint, to: ResumeIdentityEndpoint): void;
		redirectResumeIdentity(
			from: ResumeIdentityEndpoint,
			previousTo: ResumeIdentityEndpoint,
			to: ResumeIdentityEndpoint,
		): void;
		removeResumeIdentitiesTo(to: ResumeIdentityEndpoint): void;
		pruneUnlistedAccepted(
			targetRef: string,
			acceptedBefore: number,
			authoritativeIds: ReadonlySet<string>,
		): Promise<string[]>;
	};

export type NativeMutationStorageListener = (targetRefs: readonly string[]) => void;

export interface NativeMutationTargetIdentity {
	readonly threadId: string;
	readonly instanceId?: string;
}

export interface NativeMutationReadLease {
	readonly targetKey: string;
	readonly targetRef: string;
	readonly client: AppwireClientLike;
	readonly registrationToken: symbol;
	readonly readToken: symbol;
	readonly expectedThreadId?: string;
	// When the read began. A send accepted after this was not in the snapshot
	// the read saw, so its absence cannot prove it landed; the
	// abandon-on-absence rule skips it.
	readonly startedAt: number;
}

// Native storage scopes records by hub and conversation. The wire payload
// keeps targetRef raw because the daemon knows only the conversation ref.
export function nativeMutationTargetKey(hubId: string, targetRef: string): string {
	return JSON.stringify([hubId, targetRef]);
}

function nativeRandomSource(): SecureRandomSource {
	return { randomUUID: Crypto.randomUUID, getRandomValues: Crypto.getRandomValues };
}

function methodFor(kind: ConversationMutationKind, expectedQueueRevision: number | undefined): string {
	if (kind === "send") return COMPOSER_METHODS.send;
	if (kind === "queue") return COMPOSER_METHODS.queue;
	if (kind === "interrupt") return "turn/interrupt";
	if (kind === "promote") return COMPOSER_METHODS.promote;
	return expectedQueueRevision === undefined ? COMPOSER_METHODS.steer : COMPOSER_METHODS.drain;
}

function intentFor(request: NativeMutationRequest): MutationIntent {
	const method = methodFor(request.kind, request.expectedQueueRevision);
	const payload: Record<string, unknown> = {
		ref: request.targetRef,
		expectedInstanceId: request.instanceId,
	};
	if (request.kind !== "interrupt" && request.kind !== "promote") payload.input = request.input;
	if (method === COMPOSER_METHODS.drain) payload.expectedQueueRevision = request.expectedQueueRevision;
	if (request.kind === "promote") {
		payload.index = request.queueEntry.index;
		payload.expectedEntryId = request.queueEntry.id;
	}
	const composer = request.composer;
	const input = request.display ?? request.input;
	return {
		targetRef: nativeMutationTargetKey(request.hubId, request.targetRef),
		threadId: request.threadId,
		instanceId: request.instanceId,
		method,
		payload,
		attachments:
			composer?.attachments.map((attachment) => ({
				presentationId: createSecureUUID(nativeRandomSource()),
				marker: attachment.marker,
				name: attachment.name ?? "",
				mediaType: attachment.mediaType,
			})) ?? [],
		...(composer === undefined
			? {}
			: {
					composerText: composer.text,
					...(composer.mentions === undefined ? {} : { composerMentions: composer.mentions }),
				}),
		...(request.draftCaptureToken === undefined ? {} : { draftCaptureToken: request.draftCaptureToken }),
		optimisticDisplay: request.kind === "interrupt" ? { method } : { method, input },
	};
}

export type NativeMutationRequest = ConversationMutationRequest;

// The native durable-read contract: the shared snapshot shape, scoped to
// one composite hub/conversation target. The storage's listRecovery
// requires the exact key, so the all-targets form of the shared
// MutationPersistencePort has no native backing and is not part of this
// contract: the target is a required parameter, not an optional one the
// runtime would have to reject at runtime behind a port claim. The
// runtime still satisfies the shared port structurally - a required-arg
// method widens to the port's optional-arg method shape - which is what
// lets it feed createMutationProjectionFence with no adapter.
export interface NativeMutationPersistenceRead {
	read(targetRef: string): Promise<MutationPersistenceSnapshot<MutationAttachmentRef>>;
}

export class NativeMutationRuntime implements ConversationMutationSubmitter, NativeMutationPersistenceRead {
	readonly storage: NativeStorage;
	readonly #outbox: MutationOutbox;
	readonly #dispatcher: MutationDispatcher;
	readonly #createMutationId: () => string;
	readonly #now: () => number;
	readonly #unsettledConsumed = new Set<string>();
	readonly #targets = new Map<
		string,
		{ client: AppwireClientLike; token: symbol; readToken?: symbol; unsubscribe: () => void }
	>();
	readonly #blockedTargets = new Set<string>();
	// A cached screen identity is sufficient for local actions, even without a
	// client. Never infer the current session from the queued row being claimed.
	readonly #targetIdentities = new Map<string, NativeMutationTargetIdentity & { owner?: symbol }>();
	// Registrations this runtime created for its own work and must release when
	// that work ends. A settle that retargets a resumed session registers the
	// new identity so the moved rows can dispatch; nothing else owns that
	// registration, so it is retired with the settle rather than left pinned to
	// a client a later connection replaces.
	readonly #retireWithSettle: Array<() => void> = [];
	// The mutations this phone submitted to each target, by id, with when:
	// reconcilePendingEntries' submittedHere. An authoritative read that lists
	// a send as pending settles its durable record before any turn shows it,
	// and then only this says the send was this phone's (Review Focus 4).
	// Kept for this launch, and bounded on both levels, since the app's process
	// can live through many sessions: an id matters for one read round trip.
	// A target's ids outlive its registration, because a reconnect
	// re-registers it just when a send's provenance matters.
	readonly #submittedHere = new Map<string, Map<string, number>>();
	readonly #storageListeners = new Set<NativeMutationStorageListener>();
	#started = false;
	// Bumped by every start and stop. A start attempt's failure rollback and
	// in-flight cleanup only apply while it still owns the lifecycle: a stop or
	// a newer start that ran while it was settling has already decided the
	// runtime's state.
	#startGeneration = 0;
	// The in-flight start attempt, shared by concurrent start() callers so they
	// observe one outcome instead of one caller being told the runtime started
	// while a failed attempt's rollback leaves it stopped.
	#startup: Promise<void> | undefined;

	#getClient(targetRef?: string): AppwireClientLike | undefined {
		if (!this.#started) return undefined;
		if (targetRef !== undefined) {
			if (this.#blockedTargets.has(targetRef)) return undefined;
			return this.#targets.get(targetRef)?.client;
		}
		return [...this.#targets.entries()].find(
			([key, { client }]) => !this.#blockedTargets.has(key) && client.state === "ready",
		)?.[1].client;
	}

	constructor(database: SqliteSync, options: NativeMutationRuntimeOptions = {}) {
		const identity = createClientIdentity(undefined, nativeRandomSource());
		this.#now = options.now ?? Date.now;
		this.#createMutationId = options.createMutationId ?? (() => createSecureUUID(nativeRandomSource()));
		this.storage = new MutationOutboxSQLite(database, {
			createMutationId: this.#createMutationId,
			now: this.#now,
			getOwnClientId: options.getOwnClientId ?? identity.ownClientId,
		});
		this.#dispatcher = new MutationDispatcher(this.storage, {
			getClient: (targetRef) => this.#getClient(targetRef),
			onStorageChange: (targetRefs) => this.#notifyStorageChange(targetRefs),
			onBlockedMutation: (targetRef) => this.#blockAfterUnknownOutcome(targetRef),
		});
		const outboxOptions: MutationOutboxOptions = {
			getClient: (targetRef) => this.#getClient(targetRef),
			onDiscover: (targetRefs) => {
				void this.#dispatcher.dispatchTargets(targetRefs).catch(() => undefined);
			},
		};
		if (options.setInterval && options.clearInterval) {
			outboxOptions.setInterval = options.setInterval;
			outboxOptions.clearInterval = options.clearInterval;
		}
		this.#outbox = new MutationOutbox(this.storage, outboxOptions);
	}

	start(): Promise<void> {
		if (this.#startup) return this.#startup;
		if (this.#started) return Promise.resolve();
		this.#startup = this.#beginStart();
		return this.#startup;
	}

	async #beginStart(): Promise<void> {
		const generation = ++this.#startGeneration;
		// Publish the started state before awaiting the outbox, so a stop()
		// racing this await still sees a started runtime and releases whatever
		// the outbox acquired. If the outbox's transactional start rejects,
		// nothing was acquired and the flag is rolled back, so the next start
		// re-runs the whole setup instead of returning early and never
		// re-arming the timer that never got created.
		this.#started = true;
		try {
			await this.#outbox.start();
		} catch (error) {
			if (this.#startGeneration === generation) this.#started = false;
			throw error;
		} finally {
			if (this.#startGeneration === generation) this.#startup = undefined;
		}
	}

	async stop(): Promise<void> {
		if (!this.#started) return;
		this.#startGeneration += 1;
		this.#started = false;
		this.#startup = undefined;
		await this.#outbox.stop();
		// A stopped runtime serves nothing, so the registrations it made for its
		// own work end here. A later start that needs the identity registers it
		// again; leaving them held would pin the targets to this attempt's client.
		//
		// A registration like that can outlive its client between ordinary
		// connection replacements, since the process-lifetime runtime never stops.
		// It is harmless rather than released early: the flush reads a registration
		// whose client is gone as no holder at all (hasStaleRegistration), so the
		// replacement claims the target. Releasing it from the settle instead would
		// strand the very dispatch it exists for.
		for (const release of this.#retireWithSettle.splice(0)) release();
	}

	// The scoped durable read (NativeMutationPersistenceRead): the snapshot
	// is the package's own shape and the composite target key is required.
	// The all-targets form of the shared MutationPersistencePort has no
	// native backing - the storage's listRecovery requires the exact key -
	// so the structural widening that hands this runtime to a port-shaped
	// reference is the one route to it that remains: it fails loudly there
	// rather than returning a snapshot that silently drops recovery rows,
	// and the fence machinery already degrades a rejected read to a no-op
	// refresh.
	async read(targetRef: string): Promise<MutationPersistenceSnapshot<MutationAttachmentRef>> {
		if (targetRef === undefined) throw new Error("targetRef is required for native persistence reads");
		const [outbox, optimistic, recovery] = await Promise.all([
			this.storage.listOutbox(targetRef),
			this.storage.listOptimistic(targetRef),
			this.storage.listRecovery(targetRef),
		]);
		return { outbox, optimistic, recovery };
	}

	// The one native recovery write with no dispatcher involvement, so the
	// runtime publishes the change itself. The notify follows the write's
	// completion, not its rows-affected count - the zero-included rule the
	// discard paths carry (docs/design/stop-cancellation-outbox.md §4, and
	// §6's zero-deletion cleanups): a discard whose DELETE commits over
	// zero rows still refreshes every projection built on subscribeStorage,
	// and a failed write stays silent, its error propagating unnotified.
	async discardRecovery(clientMutationId: string, targetRef: string): Promise<boolean> {
		const discarded = await this.storage.discardRecovery(clientMutationId, targetRef);
		this.#notifyStorageChange([targetRef]);
		return discarded;
	}

	async editQueuedMessage(id: string, targetKey: string): Promise<MutationRecoveryRecord | undefined> {
		const snapshot = await this.#queuedMessageSnapshot(id, targetKey);
		const recovery =
			snapshot?.isCurrent() && canEditQueuedMessage(snapshot.record)
				? await this.storage.editQueuedMessage(snapshot.record, { stopEpoch: snapshot.stopEpoch })
				: undefined;
		this.#notifyStorageChange([targetKey]);
		return recovery;
	}

	async removeQueuedMessage(id: string, targetKey: string): Promise<boolean> {
		const snapshot = await this.#queuedMessageSnapshot(id, targetKey);
		const removed =
			snapshot?.isCurrent() && canRemoveQueuedMessage(snapshot.record)
				? await this.storage.removeQueuedMessage(snapshot.record, { stopEpoch: snapshot.stopEpoch })
				: false;
		this.#notifyStorageChange([targetKey]);
		return removed;
	}

	/** Fresh guarded row proof for a caller that must transfer local capture
	 * ownership before Edit/Remove. The eventual action takes its own new proof. */
	async queuedMessageForAction(
		id: string,
		targetKey: string,
		action: "edit" | "remove",
	): Promise<MutationOutboxRecord<MutationAttachmentRef> | undefined> {
		const snapshot = await this.#queuedMessageSnapshot(id, targetKey);
		if (!snapshot?.isCurrent()) return undefined;
		return (action === "edit" ? canEditQueuedMessage(snapshot.record) : canRemoveQueuedMessage(snapshot.record))
			? snapshot.record
			: undefined;
	}

	/** Fresh exact-identity proof for an already-claimed editing source. The
	 * returned currentness check fences a target replacement before draft clear
	 * and again before append. Cached offline identity remains sufficient. */
	async retainedRecoveryForAction(
		id: string,
		targetKey: string,
	): Promise<{ record: MutationRecoveryRecord<MutationAttachmentRef>; isCurrent: () => boolean } | undefined> {
		const target = this.#targetIdentitySnapshot(targetKey);
		const identity = target.identity;
		if (!identity) return undefined;
		const record = (await this.storage.listRecovery(targetKey)).find(
			(recovery) => recovery.clientMutationId === id && recovery.recoveryKind === "editing",
		);
		if (
			!record ||
			record.targetRef !== targetKey ||
			record.threadId !== identity.threadId ||
			(record.instanceId ?? record.threadId) !== (identity.instanceId ?? identity.threadId) ||
			!target.isCurrent()
		)
			return undefined;
		return { record, isCurrent: target.isCurrent };
	}

	#targetIdentitySnapshot(targetKey: string) {
		const registration = this.#targets.get(targetKey);
		const identity = this.#targetIdentities.get(targetKey);
		return {
			identity,
			isCurrent: () =>
				identity !== undefined &&
				this.#targets.get(targetKey) === registration &&
				this.#targetIdentities.get(targetKey) === identity,
		};
	}

	async #queuedMessageSnapshot(id: string, targetKey: string) {
		const target = this.#targetIdentitySnapshot(targetKey);
		const identity = target.identity;
		const { record, stopEpoch } = await this.storage.getOutboxWithStopEpoch(id);
		if (!record) return undefined;
		return {
			record,
			stopEpoch,
			// Evaluate in the caller immediately before SQLite's synchronous claim,
			// not here before this helper's promise adds another continuation.
			isCurrent: () =>
				record.targetRef === targetKey &&
				identity !== undefined &&
				target.isCurrent() &&
				record.threadId === identity.threadId &&
				(record.instanceId ?? record.threadId) === (identity.instanceId ?? identity.threadId),
		};
	}

	readRecoveryResend(targetKey: string, sourceIds: readonly string[]): Promise<RecoveryResendCommit | undefined> {
		return this.storage.readRecoveryResend(targetKey, sourceIds);
	}

	async acknowledgeRecoveryResend(targetKey: string, clientMutationId: string): Promise<void> {
		await this.storage.acknowledgeRecoveryResend(targetKey, clientMutationId);
		this.#notifyStorageChange([targetKey]);
	}

	// Discard on your own message a Stop held or whose delivery couldn't be
	// confirmed. It notifies as discardRecovery does, after the write, zero
	// rows included. Removing a blocked head lets the target's next message
	// go, so the target is dispatched again (the dispatcher still checks its
	// gate).
	async discardUndelivered(clientMutationId: string, targetRef: string): Promise<boolean> {
		const discarded = await this.storage.discardUndelivered(clientMutationId, targetRef);
		this.#notifyStorageChange([targetRef]);
		if (discarded) void this.#dispatcher.dispatchTargets([targetRef]).catch(() => undefined);
		return discarded;
	}

	// Send now on your own message a Stop held before it left the phone.
	async releaseCanceled(clientMutationId: string, targetRef: string): Promise<boolean> {
		// The press-time half of the stop barrier, captured as submit's is:
		// readStopEpoch's body is synchronous, so the epoch is read inside this
		// call, before any other event runs. A Stop that lands during start()
		// then moves the epoch past it, and storage refuses the release.
		const barrier: MutationStopBarrier = { stopEpoch: await this.storage.readStopEpoch(targetRef) };
		await this.start();
		const released = await this.storage.releaseCanceled(clientMutationId, targetRef, barrier);
		this.#notifyStorageChange([targetRef]);
		if (released) void this.#dispatcher.dispatchTargets([targetRef]).catch(() => undefined);
		return released;
	}

	subscribeStorage(listener: NativeMutationStorageListener): () => void {
		this.#storageListeners.add(listener);
		return () => this.#storageListeners.delete(listener);
	}

	// The durable records a live frame proves the daemon has moved past
	// (notificationMutationIdentities): a recorded item, or a queue transition.
	// Positive evidence settles an accepted record (already applied) and an
	// outbox row alike - the frame names work the daemon has applied, so
	// resending could never help. The projection follows through the storage
	// notification, so a composer's pending rows clear without waiting for a
	// read wide enough to still name the id.
	async settleIdentities(clientMutationIds: readonly string[]): Promise<void> {
		await this.#dispatcher.reconcileIdentities(clientMutationIds);
	}

	registerTarget(
		hubId: string,
		targetRef: string,
		client: AppwireClientLike | null,
		identity?: NativeMutationTargetIdentity,
	): () => void {
		const key = nativeMutationTargetKey(hubId, targetRef);
		const current = this.#targets.get(key);
		const token = Symbol();
		// Optional cached identity adds local ownership without changing a null
		// client's existing no-op transport semantics or another screen's lease.
		const knownIdentity = identity ?? (client === null ? undefined : this.#targetIdentities.get(key));
		const localIdentity = knownIdentity === undefined ? undefined : { ...knownIdentity, owner: token };
		if (localIdentity) this.#targetIdentities.set(key, localIdentity);
		// A null client is a state transition, not an owner identity. Only the
		// closure returned to the registering screen may remove its binding.
		if (client === null)
			return () => {
				if (this.#targetIdentities.get(key)?.owner === token) this.#targetIdentities.delete(key);
			};
		current?.unsubscribe();
		const unsubscribe = client.onStateChange((state) => {
			const target = this.#targets.get(key);
			if (target?.token !== token) return;
			if (state !== "ready") {
				target.readToken = undefined;
				this.#blockedTargets.add(key);
				return;
			}
			// A ready transition only permits a new authoritative read. It cannot
			// release a target by itself after a reconnect.
			void this.connectionReady();
		});
		this.#targets.set(key, { client, token, unsubscribe });
		this.#blockedTargets.add(key);
		return () => {
			unsubscribe();
			if (this.#targets.get(key)?.token === token) {
				this.#targets.delete(key);
				this.#blockedTargets.delete(key);
				if (this.#targetIdentities.get(key)?.owner === token) this.#targetIdentities.delete(key);
			}
		};
	}

	/** The client a session screen, or the flush, registered this target
	 * with; undefined while nobody holds it. */
	targetClient(hubId: string, targetRef: string): AppwireClientLike | undefined {
		return this.#targets.get(nativeMutationTargetKey(hubId, targetRef))?.client;
	}

	/** A registration exists, but nobody live is behind it: its client was
	 * replaced or closed and no screen rebinds that target any more. The flush
	 * reads this to tell "another owner is reading this" from "a stale entry is
	 * holding it", which is what an identity-changing resume leaves behind when
	 * the runtime outlives a connection. A stale entry is not an owner, so the
	 * target is claimable. */
	hasStaleRegistration(hubId: string, targetRef: string): boolean {
		const target = this.#targets.get(nativeMutationTargetKey(hubId, targetRef));
		if (target === undefined) return false;
		return target.client.state !== "ready" && target.client.state !== "reconnecting";
	}

	beginAuthoritativeRead(
		hubId: string,
		targetRef: string,
		client: AppwireClientLike,
		expectedThreadId?: string,
	): NativeMutationReadLease | undefined {
		const targetKey = nativeMutationTargetKey(hubId, targetRef);
		const target = this.#targets.get(targetKey);
		if (!target || target.client !== client || client.state !== "ready") return undefined;
		const readToken = Symbol();
		target.readToken = readToken;
		this.#blockedTargets.add(targetKey);
		return {
			targetKey,
			targetRef,
			client,
			registrationToken: target.token,
			readToken,
			expectedThreadId,
			startedAt: this.#now(),
		};
	}

	async reconcileAuthoritativeRead(
		lease: NativeMutationReadLease,
		response: ThreadReadResponse,
	): Promise<"reconciled" | "blocked" | "stale"> {
		if (!this.#isCurrentRead(lease)) return "stale";
		if (
			response.thread.evener.ref !== lease.targetRef ||
			(lease.expectedThreadId !== undefined && response.thread.id !== lease.expectedThreadId)
		)
			return "stale";

		this.#targetIdentities.set(lease.targetKey, {
			threadId: response.thread.id,
			instanceId: response.thread.evener.instanceId ?? response.thread.id,
			owner: lease.registrationToken,
		});
		const authoritativeIds = collectAuthoritativeMutationIds(response);
		if (!this.#isCurrentRead(lease)) return "stale";
		await this.#dispatcher.reconcileIdentities(authoritativeIds);
		await this.#settleRetainedConsumed();
		if (!this.#isCurrentRead(lease)) return "stale";

		const status = response.thread.status.type;
		const resumeRequired = response.thread.evener.resumeRequired === true;
		const restartRequired = status === "restartRequired";
		const notLoaded = status === "notLoaded";
		const mutationStateAuthoritative = response.thread.evener.mutationStateAuthoritative === true;
		if (mutationStateAuthoritative && !restartRequired && !notLoaded && !resumeRequired) {
			await this.#dispatcher.restoreProvenAbsent(lease.targetKey, authoritativeIds);
			if (!this.#isCurrentRead(lease)) return "stale";
			// An accepted record is already applied; it is kept only until a read
			// shows it reflected. The daemon stops naming a start or queue the
			// moment its turn is incorporated, so once that turn's item leaves a
			// bounded read's window no later read can name the id, and the record
			// would render as a send still on its way forever. This projection is
			// complete and authoritative, so an accepted record absent from it has
			// landed - the same absence rule restoreProvenAbsent applies to a
			// parked row, with the receipt's "applied" standing in for proof that
			// resending could never help.
			const pruned = await this.storage.pruneUnlistedAccepted(lease.targetKey, lease.startedAt, authoritativeIds);
			if (pruned.length > 0) this.#notifyStorageChange([lease.targetKey]);
			if (!this.#isCurrentRead(lease)) return "stale";
		} else {
			const records = await this.storage.listOutbox(lease.targetKey);
			if (!this.#isCurrentRead(lease)) return "stale";
			for (const record of records) {
				if (record.state !== "submitting" || !record.attempted) continue;
				const blocked = await this.storage.markUnknown(record.clientMutationId, "blockedUnknown", {
					onlyAttempted: true,
				});
				if (blocked) this.#notifyStorageChange([lease.targetKey]);
				if (!this.#isCurrentRead(lease)) return "stale";
			}
		}

		if (restartRequired || resumeRequired) return "blocked";
		if (!this.#isCurrentRead(lease)) return "stale";
		this.#blockedTargets.delete(lease.targetKey);
		void this.#dispatcher.dispatchTargets([lease.targetKey]).catch(() => undefined);
		return "reconciled";
	}

	/** Releases a registered target that is waiting for an authoritative read,
	 * with a read that leaves the connection's thread subscription alone.
	 *
	 * A screen stacked above a session (a subagent's stop request to its
	 * coordinator, a review from the Reader) submits to that session while the
	 * session's own screen is mounted under it but not reading. After a
	 * reconnect that target stays blocked until an authoritative read, so the
	 * message would wait for a trip back. This read has the window the
	 * session's own read uses (conversation.ts readProjection), and so the same
	 * proof of what already landed; it omits only the subscription, which
	 * belongs to whatever the screen on top is showing. An open target is left
	 * alone, and a target registered to another client can't be read for.
	 *
	 * One difference from the session's own read: on a transient failure the
	 * hub may answer a read that doesn't subscribe from the session's persisted
	 * history (allowsPastFallbackAfterLiveReadFailure, cmd/evener-hub). That
	 * answer isn't mutation-authoritative and lists no pending mutations, so
	 * it settles only records its transcript shows landed, and otherwise marks
	 * unconfirmed records blockedUnknown, never proving one absent. */
	async settleTarget(
		hubId: string,
		targetRef: string,
		client: AppwireClientLike,
		options: {
			mayResumeFenced?: () => Promise<boolean>;
			/** Called with the resolved ref when a resume changes the session's
			 * identity, so a mounted screen can follow it instead of staying on
			 * the ref the daemon retired. */
			onResolvedIdentity?: (ref: string) => void;
		} = {},
	): Promise<"open" | "reconciled" | "blocked" | "resumed" | "retry" | "stale" | "unregistered"> {
		const targetKey = nativeMutationTargetKey(hubId, targetRef);
		if (!this.#targets.has(targetKey)) return "unregistered";
		if (!this.#blockedTargets.has(targetKey)) return "open";
		const lease = this.beginAuthoritativeRead(hubId, targetRef, client);
		if (!lease) return "blocked";
		let response: ThreadReadResponse;
		try {
			response = await client.request("thread/read", {
				ref: targetRef,
				includeTurns: true,
				itemsView: "fragment",
				itemLimit: READ_ITEM_LIMIT,
			});
		} catch {
			return "blocked";
		}
		// A fenced target that still holds a waiting message resumes here, so an
		// offline send is not stranded: the resume's own read is what releases the
		// parked row, and the next settle finishes it. Off by default - an
		// ordinary read must never resume a session the person has not sent to.
		if (
			options.mayResumeFenced !== undefined &&
			// Validate before acting: a newer read may already have cleared the
			// fence and started dispatching, and resuming on this older answer
			// would recycle the transport out from under that healthy work.
			this.#isCurrentRead(lease) &&
			response.thread.evener.resumeRequired === true &&
			// A daemon that needs restarting refuses a resume; that shape stays with
			// the person, whose older process must be stopped first.
			response.thread.status.type !== "restartRequired"
		) {
			// Asked only now, on a fence the caller has actually seen: the ordinary
			// settle path pays no storage read for it.
			//
			// A refusal and a failure are different facts. Refused: the session has
			// no message worth resuming for, and "blocked" is its resting state.
			// Failed: the caller could not tell, and answering "blocked" would skip
			// the storage backoff it needs - so that answer is its own, and the
			// flush schedules the retry it already has for a failed read.
			let mayResume: boolean;
			try {
				mayResume = await options.mayResumeFenced();
			} catch {
				return "retry";
			}
			// Declining the resume must not throw away the read this settle
			// already made. That read is the only proof the caller gets about
			// what landed: skipping it would leave an attempted row "submitting"
			// forever instead of surfacing it as the uncertain send it is. The
			// target stays fenced, so reconciliation settles what the read proves
			// without dispatching anything.
			if (!mayResume) {
				if (!this.#isCurrentRead(lease)) return "stale";
				await this.reconcileAuthoritativeRead(lease, response);
				return "blocked";
			}
			// The predicate reads storage, so it is an await of its own: a newer
			// read can clear the fence and start healthy work inside it. Acting on
			// this older answer then would recycle the transport out from under that
			// work, so the lease is checked again here, not only before.
			if (!this.#isCurrentRead(lease)) return "stale";
			// A refused resume leaves the row parked and the next settle retries.
			const resumed = await client.resumeThread(targetRef).catch(() => undefined);
			if (!resumed) return "blocked";
			// A resume can return a different identity. The parked rows still name
			// the old ref and its instance, so they move to the resumed one before
			// any read below - otherwise reconciliation sees rows under a ref the
			// daemon no longer serves, or dispatches them with a stale fence.
			const resolvedRef = resumed.thread.evener.ref;
			if (resolvedRef !== targetRef) {
				// Rows are keyed by the composite hub+ref the intent was built
				// with, so the move names that same key on both sides.
				const resolvedKey = nativeMutationTargetKey(hubId, resolvedRef);
				this.storage.retarget(targetKey, {
					ref: resolvedKey,
					// The wire ref is the bare session ref; the key above is this
					// phone's storage identity for it. Conflating them would send the
					// daemon a JSON-encoded pair as a session ref.
					wireRef: resolvedRef,
					threadId: resumed.thread.id,
					instanceId: resumed.thread.evener.instanceId,
				});
				// The resumed identity needs its own registration: the dispatcher
				// reaches a client only through the key it names, so without this the
				// moved rows would sit under a key nothing can serve. The original
				// registration still belongs to its owner and is left alone.
				//
				// The one created here is scoped to this settle: it exists so the rows
				// can dispatch, and holding it longer would pin the target to this
				// connection's client - a later flush would then find the target
				// "owned" and skip it, stranding whatever moved later.
				const releaseResolved = this.registerTarget(hubId, resolvedRef, client);
				this.#retireWithSettle.push(releaseResolved);
				// Subscribers match the composite key, so a bare ref reaches none of
				// them and both projections would keep showing rows that moved.
				this.#notifyStorageChange([targetKey, resolvedKey]);
				options.onResolvedIdentity?.(resolvedRef);
			}
			try {
				// The resume cleared the fence, so read once more and reconcile in
				// this same settle. A flush with no screen on the target lets go after
				// a "blocked" answer and is woken by storage writes only, and the
				// resume writes none - so leaving the row for "the next settle" would
				// strand it.
				//
				// The resume recycles its transport, so the registration this read
				// started under may be gone by now: a lease whose registration
				// vanished reconciles as "stale". Reconcile what this tab can still
				// prove, and let the caller's own retry own the rest.
				const afterLease = this.beginAuthoritativeRead(hubId, resolvedRef, client);
				if (!afterLease) return "resumed";
				let after: ThreadReadResponse;
				try {
					after = await client.request("thread/read", {
						ref: resolvedRef,
						includeTurns: true,
						itemsView: "fragment",
						itemLimit: READ_ITEM_LIMIT,
					});
				} catch {
					return "resumed";
				}
				return await this.reconcileAuthoritativeRead(afterLease, after);
			} finally {
				// Deliberately NOT released here. The dispatch this registration
				// enables runs asynchronously and reads storage several awaits deep,
				// so any release timed from this settle either strands the dispatch
				// (too early, which a test caught) or holds the target claimed by
				// this connection (too late). Its lifetime is the runtime's, released
				// in stop(); see the KNOWN LIMITATION note there.
			}
		}
		return this.reconcileAuthoritativeRead(lease, response);
	}

	#isCurrentRead(lease: NativeMutationReadLease): boolean {
		const target = this.#targets.get(lease.targetKey);
		return (
			target?.client === lease.client &&
			target.token === lease.registrationToken &&
			target.readToken === lease.readToken &&
			lease.client.state === "ready"
		);
	}

	#blockAfterUnknownOutcome(targetKey: string): void {
		const target = this.#targets.get(targetKey);
		if (!target) return;
		target.readToken = undefined;
		this.#blockedTargets.add(targetKey);
	}

	async connectionReady(): Promise<void> {
		await this.#outbox.connectionReady();
	}

	/** Every admitted path returns the durable row it committed. The production
	 * path is acknowledged at the enqueue boundary, before the daemon answers,
	 * so an admission carries no wire receipt; the service-only caller adds
	 * one when it has it. */
	async submit(request: NativeMutationRequest): Promise<ConversationMutationAdmission> {
		const intent = intentFor(request);
		if (!this.#targetIdentities.has(intent.targetRef)) {
			this.#targetIdentities.set(intent.targetRef, {
				threadId: request.threadId,
				instanceId: request.instanceId,
				owner: this.#targets.get(intent.targetRef)?.token,
			});
		}
		if (request.kind === "interrupt") {
			// Stop's click is the cancel moment: the combined durable write
			// cancels the target's never-attempted rows and enqueues the
			// interrupt in one savepoint, bumping the ref's stop epoch with
			// them. The interrupt itself passes no barrier - it IS the click
			// the epoch records.
			await this.start();
			const record = await this.#outbox.enqueueInterruptAndCancel(intent);
			this.#notifyStorageChange([record.targetRef]);
			return { clientMutationId: record.clientMutationId };
		}
		if (request.recoverySourceIds?.length) {
			// Capture before startup, not when the all-source transaction begins.
			// This path has no ordinary enqueue or direct-send fallback: the draft
			// retains ownership until its exact durable receipt is acknowledged.
			const barrier = { stopEpoch: await this.storage.readStopEpoch(intent.targetRef) };
			await this.start();
			const record = await this.storage.resendRecovery(request.recoverySourceIds, intent, barrier);
			if (!record) {
				this.#notifyStorageChange([intent.targetRef]);
				throw new Error("Recovery sources are no longer available");
			}
			this.#rememberSubmitted(record.targetRef, record.clientMutationId, record.createdAt);
			this.#notifyStorageChange([record.targetRef]);
			void this.#dispatcher.dispatchTargets([record.targetRef]).catch(() => undefined);
			return { clientMutationId: record.clientMutationId };
		}
		try {
			// The click-time half of the stop barrier, captured before the
			// startup await like the web runtime's enqueue funnel: a Stop whose
			// cancel transaction commits while this submission is in flight makes
			// the row commit born-canceled, never dispatched.
			const barrier: MutationStopBarrier = {
				stopEpoch: await this.storage.readStopEpoch(intent.targetRef),
			};
			await this.start();
			// The last local word before anything durable exists: a Retry whose
			// view retired, whose binding moved, or whose canonical/pending facts
			// changed refuses here with a non-storage error, so no row and no
			// request are made. Ordinary Send/Queue carry no precondition.
			if (request.precondition !== undefined && !request.precondition()) throw new MutationNotSentError();
			const record = await this.#outbox.enqueueIntent(intent, undefined, barrier);
			this.#rememberSubmitted(record.targetRef, record.clientMutationId, record.createdAt);
			this.#notifyStorageChange([record.targetRef]);
			return { clientMutationId: record.clientMutationId };
		} catch (error) {
			if (!DIRECT_FALLBACK_METHODS.has(intent.method) || !isSqliteStorageFailure(error)) throw error;
			return this.#sendDirectly(request, intent, error);
		}
	}

	// Storage refused a composer send's durable write, so the send goes out as
	// a plain request the way the web's does (friction C13): nothing is
	// durable, so a failed request rejects and the draft stays. It needs the
	// target's ready client, otherwise the storage error is the send's
	// failure. The dispatch gate (#blockedTargets) does not apply: it exists
	// to order durable rows against an authoritative read. Like the web's, the
	// send carries none of the row's guarantees: no per-ref ordering against
	// earlier undelivered rows, no Stop fence, and a human retry after a lost
	// reply is a new mutation.
	async #sendDirectly(
		request: NativeMutationRequest,
		intent: MutationIntent,
		storageError: unknown,
	): Promise<ConversationMutationAdmission> {
		// Storage failed after the first check; the local facts may have moved on
		// while it did. Recheck before the plain request, which has no durable row
		// to own a later refusal.
		if (request.precondition !== undefined && !request.precondition()) throw new MutationNotSentError();
		const client = this.#targets.get(intent.targetRef)?.client;
		if (!client || client.state !== "ready") throw storageError;
		const call = client.request as unknown as (method: string, params: Record<string, unknown>) => Promise<unknown>;
		const clientMutationId = this.#createMutationId();
		const result = await call.call(client, intent.method, { ...intent.payload, clientMutationId });
		// The send is this phone's, so a read that lists it as pending must
		// not classify it as another client's.
		this.#rememberSubmitted(intent.targetRef, clientMutationId, this.#now());
		if (intent.method === COMPOSER_METHODS.drain) await this.#settleConsumedByDrain(result);
		return { clientMutationId };
	}

	// The queued messages a direct drain consumed settle as the dispatcher
	// settles them. Storage just failed, so the ids are retained until a
	// write succeeds: a later authoritative read cannot name them, because a
	// drain removes the queue ids it consumed.
	async #settleConsumedByDrain(result: unknown): Promise<void> {
		const receipt = (result as { receipt?: { consumedClientMutationIds?: unknown } } | null)?.receipt;
		for (const id of validConsumedClientMutationIds(receipt?.consumedClientMutationIds))
			this.#unsettledConsumed.add(id);
		await this.#settleRetainedConsumed();
	}

	async #settleRetainedConsumed(): Promise<void> {
		if (this.#unsettledConsumed.size === 0) return;
		const ids = [...this.#unsettledConsumed];
		try {
			await this.#dispatcher.reconcileIdentities(ids);
			for (const id of ids) this.#unsettledConsumed.delete(id);
		} catch {
			// Storage is still failing; the next authoritative read retries.
		}
	}

	/** The mutations this phone submitted to a target this launch, by id,
	 * with when (reconcilePendingEntries' submittedHere): a copy, so later
	 * submissions never change what a caller already holds. */
	submittedHere(targetKey: string): ReadonlyMap<string, number> {
		const submitted = this.#submittedHere.get(targetKey);
		return submitted ? new Map(submitted) : NOTHING_SUBMITTED;
	}

	#rememberSubmitted(targetKey: string, clientMutationId: string, createdAt: number): void {
		// Re-inserted so the targets stay in the order they were last written to.
		const submitted = this.#submittedHere.get(targetKey) ?? new Map<string, number>();
		this.#submittedHere.delete(targetKey);
		this.#submittedHere.set(targetKey, submitted);
		if (this.#submittedHere.size > SUBMITTED_TARGETS_LIMIT) {
			const longestAgo = this.#submittedHere.keys().next().value;
			if (longestAgo !== undefined) this.#submittedHere.delete(longestAgo);
		}
		submitted.set(clientMutationId, createdAt);
		// A send the daemon still lists as pending is recent; the oldest ids
		// have long since shown up in a turn, where they need no provenance.
		if (submitted.size > SUBMITTED_HERE_LIMIT) {
			const oldest = submitted.keys().next().value;
			if (oldest !== undefined) submitted.delete(oldest);
		}
	}

	#notifyStorageChange(targetRefs: readonly string[]): void {
		const refs = [...new Set(targetRefs)];
		for (const listener of this.#storageListeners) {
			try {
				listener(refs);
			} catch (error) {
				console.error("Native mutation storage listener failed", error);
			}
		}
	}
}

const NOTHING_SUBMITTED: ReadonlyMap<string, number> = new Map();
const SUBMITTED_HERE_LIMIT = 100;
const SUBMITTED_TARGETS_LIMIT = 50;

function createNativeMutationRuntime(): NativeMutationRuntime {
	// Single-writer by construction: this one handle is memoized by
	// getNativeMutationRuntime below; see sqliteSync's connection contract.
	const database = openDatabaseSync("evener-mutations.db");
	database.execSync("PRAGMA journal_mode = WAL");
	return new NativeMutationRuntime(database as unknown as SqliteSync, {
		setInterval: (callback, milliseconds) => globalThis.setInterval(callback, milliseconds) as unknown as number,
		clearInterval: (intervalId) =>
			globalThis.clearInterval(intervalId as unknown as ReturnType<typeof globalThis.setInterval>),
	});
}

let sharedNativeMutationRuntime: NativeMutationRuntime | undefined;

export function getNativeMutationRuntime(): NativeMutationRuntime {
	sharedNativeMutationRuntime ??= createNativeMutationRuntime();
	return sharedNativeMutationRuntime;
}
