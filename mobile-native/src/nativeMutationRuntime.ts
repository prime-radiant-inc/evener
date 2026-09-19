import { openDatabaseSync } from "expo-sqlite";
import * as Crypto from "expo-crypto";
import type {
	AppwireClientLike,
	MutationReceipt,
} from "@evener/appwire-client";
import {
	createClientIdentity,
	MutationDispatcher,
	MutationOutbox,
	type MutationAttachmentRef,
	type MutationIntent,
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
	type MutationOutboxDatabase,
} from "./mutationOutboxStorage";

export interface NativeMutationRuntimeOptions {
	createMutationId?: () => string;
	now?: () => number;
	getOwnClientId?: () => string | undefined;
}

type NativeStorage = MutationOutboxStorage<MutationAttachmentRef>;

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
		{ client: AppwireClientLike; token: symbol; unsubscribe: () => void }
	>();
	#started = false;

	#getClient(targetRef?: string): AppwireClientLike | undefined {
		if (targetRef !== undefined) return this.#targets.get(targetRef)?.client;
		return [...this.#targets.values()].find(({ client }) => client.state === "ready")?.client;
	}

	constructor(database: MutationOutboxDatabase, options: NativeMutationRuntimeOptions = {}) {
		const identity = createClientIdentity(undefined, nativeRandomSource());
		this.storage = new MutationOutboxSQLite(database, {
			createMutationId: options.createMutationId,
			now: options.now,
			getOwnClientId: options.getOwnClientId ?? identity.ownClientId,
		});
		this.#dispatcher = new MutationDispatcher(this.storage, {
			getClient: (targetRef) => this.#getClient(targetRef),
		});
		this.#outbox = new MutationOutbox(this.storage, {
			getClient: (targetRef) => this.#getClient(targetRef),
			onDiscover: (targetRefs) => this.#dispatcher.dispatchTargets(targetRefs),
		});
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
			if (state === "ready" && this.#targets.get(key)?.token === token)
				void this.connectionReady();
		});
		this.#targets.set(key, { client, token, unsubscribe });
		if (client.state === "ready") void this.connectionReady();
		return () => {
			unsubscribe();
			if (this.#targets.get(key)?.token === token) this.#targets.delete(key);
		};
	}

	async connectionReady(): Promise<void> {
		await this.#outbox.connectionReady();
	}

	async submit(request: NativeMutationRequest): Promise<MutationReceipt | undefined> {
		await this.start();
		await this.#outbox.enqueueIntent(intentFor(request));
		return undefined;
	}
}

function createNativeMutationRuntime(): NativeMutationRuntime {
	const database = openDatabaseSync("evener-mutations.db");
	database.execSync("PRAGMA journal_mode = WAL");
	return new NativeMutationRuntime(database as unknown as MutationOutboxDatabase);
}

let sharedNativeMutationRuntime: NativeMutationRuntime | undefined;

export function getNativeMutationRuntime(): NativeMutationRuntime {
	sharedNativeMutationRuntime ??= createNativeMutationRuntime();
	return sharedNativeMutationRuntime;
}
