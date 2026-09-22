import { openDatabaseSync } from "expo-sqlite";
import * as Crypto from "expo-crypto";
import type {
	AppwireClientLike,
	MutationReceipt,
	ThreadReadResponse,
} from "@evener/appwire-client";
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
	type SecureRandomSource,
} from "@evener/appwire-client/state/mutation";
import type {
	ConversationMutationKind,
	ConversationMutationRequest,
	ConversationMutationSubmitter,
} from "../../mobile/src/state/conversationMutation";
import {
	MutationOutboxSQLite,
} from "./mutationOutboxStorage";
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
};

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

function methodFor(
	kind: ConversationMutationKind,
	expectedQueueRevision: number | undefined,
): string {
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
	if (method === "turn/drainAsSteer")
		payload.expectedQueueRevision = request.expectedQueueRevision;
	return {
		targetRef: nativeMutationTargetKey(request.hubId, request.targetRef),
		threadId: request.threadId,
		instanceId: request.instanceId,
		method,
		payload,
		attachments: [],
		optimisticDisplay:
			request.kind === "interrupt"
				? { method }
				: { method, input: request.input },
	};
}

export type NativeMutationRequest = ConversationMutationRequest;

export class NativeMutationRuntime implements ConversationMutationSubmitter {
	readonly storage: NativeStorage;
	readonly #outbox: MutationOutbox;
	readonly #dispatcher: MutationDispatcher;
	readonly #targets = new Map<
		string,
		{ client: AppwireClientLike; token: symbol; readToken?: symbol; unsubscribe: () => void }
	>();
	readonly #blockedTargets = new Set<string>();
	#started = false;

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

	async start(): Promise<void> {
		if (this.#started) return;
		this.#started = true;
		await this.#outbox.start();
	}

	async stop(): Promise<void> {
		if (!this.#started) return;
		this.#started = false;
		await this.#outbox.stop();
	}

	registerTarget(
		hubId: string,
		targetRef: string,
		client: AppwireClientLike | null,
	): () => void {
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
				await this.storage.markUnknown(record.clientMutationId, "blockedUnknown", { onlyAttempted: true });
				if (!this.#isCurrentRead(lease)) return "stale";
			}
		}

		if (restartRequired || resumeRequired) return "blocked";
		if (!this.#isCurrentRead(lease)) return "stale";
		this.#blockedTargets.delete(lease.targetKey);
		void this.#dispatcher.dispatchTargets([lease.targetKey]).catch(() => undefined);
		return "reconciled";
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
			await this.#outbox.enqueueInterruptAndCancel(intent);
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
		await this.#outbox.enqueueIntent(intent, undefined, barrier);
		return undefined;
	}
}

function createNativeMutationRuntime(): NativeMutationRuntime {
	const database = openDatabaseSync("evener-mutations.db");
	database.execSync("PRAGMA journal_mode = WAL");
	return new NativeMutationRuntime(database as unknown as SqliteSync, {
		setInterval: (callback, milliseconds) =>
			globalThis.setInterval(callback, milliseconds) as unknown as number,
		clearInterval: (intervalId) =>
			globalThis.clearInterval(intervalId as unknown as ReturnType<typeof globalThis.setInterval>),
	});
}

let sharedNativeMutationRuntime: NativeMutationRuntime | undefined;

export function getNativeMutationRuntime(): NativeMutationRuntime {
	sharedNativeMutationRuntime ??= createNativeMutationRuntime();
	return sharedNativeMutationRuntime;
}
