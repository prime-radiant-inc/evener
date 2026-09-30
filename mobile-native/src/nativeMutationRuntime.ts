import { openDatabaseSync } from "expo-sqlite";
import * as Crypto from "expo-crypto";
import type { AppwireClientLike, MutationReceipt, ThreadReadResponse } from "@evener/appwire-client";
import { collectAuthoritativeMutationIds } from "@evener/appwire-client";
import {
	createClientIdentity,
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
	type SecureRandomSource,
} from "@evener/appwire-client/state/mutation";
import { READ_ITEM_LIMIT } from "../../mobile/src/services/conversation";
import type {
	ConversationMutationKind,
	ConversationMutationRequest,
	ConversationMutationSubmitter,
} from "../../mobile/src/state/conversationMutation";
import { MutationOutboxSQLite } from "./mutationOutboxStorage";
import type { SqliteSync } from "./sqliteSync";

export interface NativeMutationRuntimeOptions {
	createMutationId?: () => string;
	now?: () => number;
	getOwnClientId?: () => string | undefined;
	setInterval?: MutationOutboxOptions["setInterval"];
	clearInterval?: MutationOutboxOptions["clearInterval"];
}

type NativeStorage = MutationOutboxStorage<MutationAttachmentRef> & {
	listOutbox(targetRef?: string): Promise<MutationOutboxRecord<MutationAttachmentRef>[]>;
	readStopEpoch(targetRef: string): Promise<number>;
	listRecovery(targetRef: string): Promise<MutationRecoveryRecord<MutationAttachmentRef>[]>;
	discardRecovery(clientMutationId: string, targetRef: string): Promise<boolean>;
	discardUndelivered(clientMutationId: string, targetRef: string): Promise<boolean>;
	releaseCanceled(clientMutationId: string, targetRef: string, barrier: MutationStopBarrier): Promise<boolean>;
};

export type NativeMutationStorageListener = (targetRefs: readonly string[]) => void;

export interface NativeMutationReadLease {
	readonly targetKey: string;
	readonly targetRef: string;
	readonly client: AppwireClientLike;
	readonly registrationToken: symbol;
	readonly readToken: symbol;
	readonly expectedThreadId?: string;
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
	if (kind === "send") return "turn/start";
	if (kind === "queue") return "turn/queue";
	if (kind === "interrupt") return "turn/interrupt";
	return expectedQueueRevision === undefined ? "turn/steer" : "turn/drainAsSteer";
}

function intentFor(request: NativeMutationRequest): MutationIntent {
	const method = methodFor(request.kind, request.expectedQueueRevision);
	const payload: Record<string, unknown> = {
		ref: request.targetRef,
		expectedInstanceId: request.instanceId,
	};
	if (request.kind !== "interrupt") payload.input = request.input;
	if (method === "turn/drainAsSteer") payload.expectedQueueRevision = request.expectedQueueRevision;
	return {
		targetRef: nativeMutationTargetKey(request.hubId, request.targetRef),
		threadId: request.threadId,
		instanceId: request.instanceId,
		method,
		payload,
		attachments: [],
		optimisticDisplay: request.kind === "interrupt" ? { method } : { method, input: request.input },
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
	readonly #targets = new Map<
		string,
		{ client: AppwireClientLike; token: symbol; readToken?: symbol; unsubscribe: () => void }
	>();
	readonly #blockedTargets = new Set<string>();
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
		this.storage = new MutationOutboxSQLite(database, {
			createMutationId: options.createMutationId,
			now: options.now,
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

	registerTarget(hubId: string, targetRef: string, client: AppwireClientLike | null): () => void {
		const key = nativeMutationTargetKey(hubId, targetRef);
		const current = this.#targets.get(key);
		// A null client is a state transition, not an owner identity. Only the
		// closure returned to the registering screen may remove its binding.
		if (client === null) return () => undefined;
		current?.unsubscribe();
		const token = Symbol();
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
			}
		};
	}

	/** The client a session screen, or the flush, registered this target
	 * with; undefined while nobody holds it. */
	targetClient(hubId: string, targetRef: string): AppwireClientLike | undefined {
		return this.#targets.get(nativeMutationTargetKey(hubId, targetRef))?.client;
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

		const authoritativeIds = collectAuthoritativeMutationIds(response);
		if (!this.#isCurrentRead(lease)) return "stale";
		await this.#dispatcher.reconcileIdentities(authoritativeIds);
		if (!this.#isCurrentRead(lease)) return "stale";

		const status = response.thread.status.type;
		const resumeRequired = response.thread.evener.resumeRequired === true;
		const restartRequired = status === "restartRequired";
		const notLoaded = status === "notLoaded";
		const mutationStateAuthoritative = response.thread.evener.mutationStateAuthoritative === true;
		if (mutationStateAuthoritative && !restartRequired && !notLoaded && !resumeRequired) {
			await this.#dispatcher.restoreProvenAbsent(lease.targetKey, authoritativeIds);
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
	): Promise<"open" | "reconciled" | "blocked" | "stale" | "unregistered"> {
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

	async submit(request: NativeMutationRequest): Promise<MutationReceipt | undefined> {
		const intent = intentFor(request);
		if (request.kind === "interrupt") {
			// Stop's click is the cancel moment: the combined durable write
			// cancels the target's never-attempted rows and enqueues the
			// interrupt in one savepoint, bumping the ref's stop epoch with
			// them. The interrupt itself passes no barrier - it IS the click
			// the epoch records.
			await this.start();
			const record = await this.#outbox.enqueueInterruptAndCancel(intent);
			this.#notifyStorageChange([record.targetRef]);
			return undefined;
		}
		// The click-time half of the stop barrier, captured before the
		// startup await like the web runtime's enqueue funnel: a Stop whose
		// cancel transaction commits while this submission is in flight makes
		// the row commit born-canceled, never dispatched.
		const barrier: MutationStopBarrier = {
			stopEpoch: await this.storage.readStopEpoch(intent.targetRef),
		};
		await this.start();
		const record = await this.#outbox.enqueueIntent(intent, undefined, barrier);
		this.#rememberSubmitted(record.targetRef, record.clientMutationId, record.createdAt);
		this.#notifyStorageChange([record.targetRef]);
		return undefined;
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
