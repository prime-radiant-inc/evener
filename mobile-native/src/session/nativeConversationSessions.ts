// One app-owned conversation binding per (hub, ref). Every mounted view of a
// session borrows the same entry: its conversation store, durable mutation
// host and stable submitter, conversation service, activity store and sink,
// display/config subscription and read actor. The first committed attachment
// creates the inert entry; only a matching ready client starts a host/service
// and the entry's one authoritative read. The last release closes the entry,
// never the process mutation runtime.
//
// This is not an admission coordinator: it holds no attempts, acceptance
// witnesses, progress frontier, per-view canonical versions or refresh claims.
// Retry's owners stay the store's canonical read validity and the durable
// pending projection.
import type { AppwireClientLike, TranscriptDisplayConfigV1 } from "@evener/appwire-client";
import { createActivityStore } from "../../../mobile/src/state/activity";
import { createConversationStore, type LiveActivitySink } from "../../../mobile/src/state/conversation";
import { createConversationMutationPendingPort } from "../../../mobile/src/state/conversationMutation";
import { createConversationService, type LiveConversationService } from "../../../mobile/src/services/conversation";
import { createDurableSubmitter, createNativeMutationHost, type NativeMutationHost } from "../nativeMutationHost";
import {
	getNativeMutationRuntime,
	nativeMutationTargetKey,
	type NativeMutationReadLease,
	type NativeMutationRuntime,
} from "../nativeMutationRuntime";
import { outboxFlush } from "../outbox/nativeOutboxFlush";
import { type DetailLevels, displayForLevel } from "./detailLevels";
import { detailLevels as levelsForHub } from "./nativeDetailLevels";

export type NativeConversationStore = ReturnType<typeof createConversationStore>;
export type NativeConversationActivity = ReturnType<typeof createActivityStore>;

/** Placeholder for Task 5d: the one entry-owned read-retry actor. It owns no
 * timer yet; views never create their own. Retirement cancels it before the
 * store closes. */
export interface NativeConversationReadActor {
	/** Consecutive read failures the transcript's retry line reads. */
	readonly failures: number;
	readonly cancelled: boolean;
	cancel(): void;
}

export interface NativeConversationSession {
	readonly hubId: string;
	readonly ref: string;
	readonly key: string;
	readonly store: NativeConversationStore;
	readonly activity: NativeConversationActivity;
	/** The exact sink object every open/read of this entry binds. */
	readonly activitySink: LiveActivitySink;
	/** The one live host/target registration, or null while offline. */
	readonly liveHost: NativeMutationHost | null;
	/** The one live service for the current client binding, or null offline. */
	readonly service: LiveConversationService | null;
	readonly displayConfig: TranscriptDisplayConfigV1 | null;
	readonly readActor: NativeConversationReadActor;
	readonly retired: boolean;
	readonly attachmentCount: number;
	/** Settles when the latest binding's authoritative read settles, including
	 * one a retirement fenced: a test or the future read actor can await the
	 * exact late reply instead of draining arbitrary microtasks. */
	readSettled(): Promise<void>;
}

export interface NativeConversationAttachment {
	readonly entry: NativeConversationSession;
	/** Retention demand only: the store follows the live end while every
	 * attached reader does. */
	setFollowingLiveEnd(following: boolean): void;
	/** Idempotent. The final release retires the entry. */
	release(): void;
}

export interface NativeConversationSessionsOptions {
	/** The process-lifetime durable runtime. Read only when an entry gets a
	 * matching ready client, never on an inert attachment. */
	readonly runtime?: () => NativeMutationRuntime;
	/** The existing outbox flush handoff the final release requests. */
	readonly flushOutbox?: () => void | Promise<unknown>;
	/** The per-hub session detail choices; defaults to the app's one store. */
	readonly levels?: (hubId: string) => DetailLevels;
}

interface AttachmentState {
	handle: NativeConversationAttachment;
	following: boolean;
}

class SessionEntry implements NativeConversationSession {
	readonly hubId: string;
	readonly ref: string;
	readonly key: string;
	readonly store: NativeConversationStore;
	readonly activity: NativeConversationActivity;
	readonly activitySink: LiveActivitySink;
	readonly readActor: NativeConversationReadActor;

	#runtime: () => NativeMutationRuntime;
	#flushOutbox: () => void | Promise<unknown>;
	#onRetire: () => void;
	#levels: DetailLevels;
	#hubConfig: TranscriptDisplayConfigV1 | null;
	#displayConfig: TranscriptDisplayConfigV1 | null;
	#unsubscribeLevels: () => void;
	#unsubscribeStore: () => void;
	#attachments = new Map<symbol, AttachmentState>();
	#liveHost: NativeMutationHost | null = null;
	#service: LiveConversationService | null = null;
	#bindingClient: AppwireClientLike | null = null;
	#readSettled: Promise<void> = Promise.resolve();
	#retired = false;

	constructor(options: {
		hubId: string;
		ref: string;
		runtime: () => NativeMutationRuntime;
		flushOutbox: () => void | Promise<unknown>;
		levels: DetailLevels;
		hubDisplayConfig: TranscriptDisplayConfigV1 | null;
		onRetire: () => void;
	}) {
		this.hubId = options.hubId;
		this.ref = options.ref;
		this.key = nativeMutationTargetKey(options.hubId, options.ref);
		this.#runtime = options.runtime;
		this.#flushOutbox = options.flushOutbox;
		this.#onRetire = options.onRetire;
		this.#levels = options.levels;
		this.#hubConfig = options.hubDisplayConfig;
		this.#displayConfig = displayForLevel(this.#levels.get(this.ref), this.#hubConfig).config;
		const activity = createActivityStore();
		this.activity = activity;
		this.activitySink = activity.getState();
		this.store = createConversationStore({
			mutationHubId: this.hubId,
			mutationSubmitter: createDurableSubmitter(() => this.#liveHost),
			displayConfig: this.#displayConfig,
		});
		let cancelled = false;
		this.readActor = {
			get failures() {
				return 0;
			},
			get cancelled() {
				return cancelled;
			},
			cancel: () => {
				cancelled = true;
			},
		};
		this.#unsubscribeLevels = this.#levels.subscribe(() => this.#applyDisplayConfig());
		this.#unsubscribeStore = this.store.subscribe(() => this.#rebindPendingIfNeeded());
	}

	get liveHost(): NativeMutationHost | null {
		return this.#liveHost;
	}

	get service(): LiveConversationService | null {
		return this.#service;
	}

	get displayConfig(): TranscriptDisplayConfigV1 | null {
		return this.#displayConfig;
	}

	get retired(): boolean {
		return this.#retired;
	}

	get attachmentCount(): number {
		return this.#attachments.size;
	}

	readSettled(): Promise<void> {
		return this.#readSettled;
	}

	attach(): NativeConversationAttachment {
		const token = Symbol("native-conversation-attachment");
		const state: AttachmentState = {
			handle: undefined as unknown as NativeConversationAttachment,
			// A new reader starts conservatively as not following until its
			// restoration settles.
			following: false,
		};
		const handle: NativeConversationAttachment = {
			entry: this,
			setFollowingLiveEnd: (following) => {
				if (this.#retired || this.#attachments.get(token) !== state) return;
				state.following = following;
				this.#applyFollowing();
			},
			release: () => {
				if (this.#attachments.get(token) !== state) return;
				this.#attachments.delete(token);
				if (this.#retired) return;
				if (this.#attachments.size === 0) this.retire();
				else this.#applyFollowing();
			},
		};
		state.handle = handle;
		this.#attachments.set(token, state);
		this.#applyFollowing();
		return handle;
	}

	/** The provider drives this once per connection change; no view competes
	 * for it. A null client (or another hub's) removes live authority while the
	 * entry keeps its display rows. */
	applyConnection(client: AppwireClientLike | null): void {
		if (this.#retired) return;
		if (client === null) {
			this.#dropBinding();
			return;
		}
		if (client === this.#bindingClient && this.#liveHost !== null && client.state === "ready") return;
		this.#dropBinding();
		this.#startBinding(client);
	}

	setHubDisplayConfig(config: TranscriptDisplayConfigV1 | null): void {
		if (this.#retired) return;
		this.#hubConfig = config;
		this.#applyDisplayConfig();
	}

	/** Final release: cancel the entry's read work, fence and close its store,
	 * release the service's handler/thread lease, dispose the target
	 * registration, reset activity and settings, then hand the outbox a look.
	 * Never stops or deletes the process runtime. */
	retire(): void {
		if (this.#retired) return;
		this.#retired = true;
		this.readActor.cancel();
		this.#attachments.clear();
		this.#unsubscribeLevels();
		this.#unsubscribeStore();
		const host = this.#liveHost;
		const service = this.#service;
		this.#liveHost = null;
		this.#service = null;
		this.#bindingClient = null;
		this.store.getState().close();
		service?.close();
		host?.dispose();
		this.activity.getState().reset();
		try {
			const flushed = this.#flushOutbox();
			if (flushed !== undefined) void Promise.resolve(flushed).catch(() => undefined);
		} catch {
			// The handoff is a look, not a commit: a failed request changes nothing.
		}
		this.#onRetire();
	}

	#applyFollowing(): void {
		if (this.#retired) return;
		this.store.getState().setFollowingLiveEnd([...this.#attachments.values()].every((state) => state.following));
	}

	#applyDisplayConfig(): void {
		if (this.#retired) return;
		const next = displayForLevel(this.#levels.get(this.ref), this.#hubConfig).config;
		this.#displayConfig = next;
		this.store.getState().setDisplayConfig(next);
	}

	#dropBinding(): void {
		const host = this.#liveHost;
		const service = this.#service;
		this.#bindingClient = null;
		this.#liveHost = null;
		this.#service = null;
		this.#readSettled = Promise.resolve();
		// Fence the store binding before releasing the service/host, so a late
		// response from the old client can never reconcile the replacement.
		this.store.getState().suspendProjected();
		service?.close();
		host?.dispose();
	}

	#startBinding(client: AppwireClientLike): void {
		this.#bindingClient = client;
		let host: NativeMutationHost;
		try {
			host = createNativeMutationHost(this.#runtime(), this.hubId, this.ref, client);
		} catch {
			// The mutations database or the client binding could not be created.
			// The entry stays inert: a later connection change retries, and the
			// stable submitter refuses rather than admitting work nobody dispatches.
			this.#bindingClient = null;
			return;
		}
		this.#liveHost = host;
		void host.start().catch(() => undefined);
		// The callbacks capture this binding's exact host object, never a lookup
		// that might later name another client's host.
		const service = createConversationService<NativeMutationReadLease>(client, {
			resolveDisplayConfig: () => this.#displayConfig,
			onReadStart: (ref, expectedThreadId) => host.beginRead(ref, expectedThreadId),
			onReadComplete: (lease, response) => host.reconcileRead(lease, response),
		});
		this.#service = service;
		const state = this.store.getState();
		const read =
			state.ref === this.ref && state.conversation !== null
				? state.resumeProjected(service, this.activitySink, this.ref)
				: state.openProjected(service, this.activitySink, this.ref);
		this.#readSettled = read.catch(() => undefined);
		// openProjected/resumeProjected retire the durable pending seam; the
		// entry reinstalls it for the binding that just started (and the store
		// subscription below covers later thread opens, including /clear).
		this.#bindPending();
		void read.catch(() => undefined);
	}

	#bindPending(): void {
		if (this.#retired || this.#service === null) return;
		try {
			this.store
				.getState()
				.bindPendingMutationsIfUnbound(
					createConversationMutationPendingPort(this.#runtime(), nativeMutationTargetKey(this.hubId, this.ref)),
				);
		} catch {
			// The mutations database could not be opened. The conversation stays
			// usable; a later connection change retries the bind.
		}
	}

	#rebindPendingIfNeeded(): void {
		if (this.#retired || this.#service === null) return;
		const state = this.store.getState();
		if (state.status === "open" && state.pendingMutations === null) this.#bindPending();
	}
}

export class NativeConversationSessions {
	#entries = new Map<string, SessionEntry>();
	#runtime: () => NativeMutationRuntime;
	#flushOutbox: () => void | Promise<unknown>;
	#levelsForHub: (hubId: string) => DetailLevels;
	#connectedHubId: string | null = null;
	#client: AppwireClientLike | null = null;
	#displayHubId: string | null = null;
	#hubConfig: TranscriptDisplayConfigV1 | null = null;

	constructor(options: NativeConversationSessionsOptions = {}) {
		this.#runtime = options.runtime ?? getNativeMutationRuntime;
		this.#flushOutbox = options.flushOutbox ?? (() => outboxFlush.flush());
		this.#levelsForHub = options.levels ?? levelsForHub;
	}

	get size(): number {
		return this.#entries.size;
	}

	/** The entry for an exact hub/ref, or undefined while nobody has attached. */
	entryFor(hubId: string, ref: string): NativeConversationSession | undefined {
		return this.#entries.get(nativeMutationTargetKey(hubId, ref));
	}

	/** Attach a reader. The first committed attachment creates the inert entry;
	 * an already-connected matching hub starts its binding. */
	attach(hubId: string, ref: string): NativeConversationAttachment {
		const key = nativeMutationTargetKey(hubId, ref);
		let entry = this.#entries.get(key);
		const created = entry === undefined;
		if (entry === undefined) {
			const createdEntry = new SessionEntry({
				hubId,
				ref,
				runtime: this.#runtime,
				flushOutbox: this.#flushOutbox,
				levels: this.#levelsForHub(hubId),
				hubDisplayConfig: hubId === this.#displayHubId ? this.#hubConfig : null,
				onRetire: () => {
					// Remove the exact entry only: a successor attached after this
					// one's retirement must not be removed by a late callback.
					if (this.#entries.get(key) === createdEntry) this.#entries.delete(key);
				},
			});
			entry = createdEntry;
			this.#entries.set(key, entry);
		}
		const handle = entry.attach();
		if (created) entry.applyConnection(this.#connectionFor(hubId));
		return handle;
	}

	/** The provider's one connection feed: only entries on the selected hub get
	 * its ready client; every other entry loses its live authority. */
	setConnection(hubId: string | null, client: AppwireClientLike | null): void {
		this.#connectedHubId = hubId;
		this.#client = client;
		for (const entry of [...this.#entries.values()]) entry.applyConnection(this.#connectionFor(entry.hubId));
	}

	/** The matching hub's transcript preferences; other hubs' entries resolve
	 * the same level against no hub config. */
	setHubDisplayConfig(hubId: string | null, config: TranscriptDisplayConfigV1 | null): void {
		this.#displayHubId = hubId;
		this.#hubConfig = config;
		for (const entry of [...this.#entries.values()]) {
			entry.setHubDisplayConfig(entry.hubId === hubId ? config : null);
		}
	}

	/** Release every entry without stopping the process mutation owner. A new
	 * attachment after disposal starts independently. */
	dispose(): void {
		const entries = [...this.#entries.values()];
		this.#entries.clear();
		for (const entry of entries) entry.retire();
	}

	#connectionFor(hubId: string): AppwireClientLike | null {
		return this.#client !== null && this.#connectedHubId === hubId ? this.#client : null;
	}
}
