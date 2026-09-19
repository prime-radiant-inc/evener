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
} from "../../mobile/src/state/conversation";
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
		targetRef: request.targetRef,
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

export interface NativeMutationRequest extends ConversationMutationRequest {
	readonly service: ConversationMutationRequest["service"];
}

export class NativeMutationRuntime implements ConversationMutationSubmitter {
	readonly storage: NativeStorage;
	readonly #outbox: MutationOutbox;
	readonly #dispatcher: MutationDispatcher;
	readonly #targets = new Map<string, { hubId: string; client: AppwireClientLike }>();
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

	registerTarget(hubId: string, targetRef: string, client: AppwireClientLike | null): void {
		const current = this.#targets.get(targetRef);
		if (client === null) {
			if (current?.hubId === hubId) this.#targets.delete(targetRef);
			return;
		}
		if (current && current.hubId !== hubId) return;
		this.#targets.set(targetRef, { hubId, client });
		if (client.state === "ready") void this.connectionReady();
	}

	async connectionReady(): Promise<void> {
		await this.#outbox.connectionReady();
	}

	async submit(request: NativeMutationRequest): Promise<MutationReceipt | undefined> {
		const current = this.#targets.get(request.targetRef);
		if (current && current.hubId !== request.hubId)
			throw new Error("native mutation target changed hubs");
		await this.start();
		await this.#outbox.enqueueIntent(intentFor(request));
		return undefined;
	}
}

export function createNativeMutationRuntime(): NativeMutationRuntime {
	const database = openDatabaseSync("evener-mutations.db");
	database.execSync("PRAGMA journal_mode = WAL");
	return new NativeMutationRuntime(database as unknown as MutationOutboxDatabase);
}
