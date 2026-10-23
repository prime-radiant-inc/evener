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
import {
	createConversationMutationPendingPort,
	type ConversationMutationSubmitter,
} from "../../../mobile/src/state/conversationMutation";
import { createConversationService } from "../../../mobile/src/services/conversation";
import { createDurableSubmitter, createNativeMutationHost, type NativeMutationHost } from "../nativeMutationHost";
import {
	getNativeMutationRuntime,
	nativeMutationTargetKey,
	type NativeMutationReadLease,
	type NativeMutationRuntime,
} from "../nativeMutationRuntime";
import { outboxFlush } from "../outbox/nativeOutboxFlush";
import { reconnectDelay } from "../hubConnection";
import { type DetailLevels, displayForLevel } from "./detailLevels";
import { detailLevels as levelsForHub } from "./nativeDetailLevels";

export type NativeConversationStore = ReturnType<typeof createConversationStore>;
export type NativeConversationActivity = ReturnType<typeof createActivityStore>;
/** The service factory's complete return type: screens need the recovery,
 * catalog, clear and fork actions as well as the base conversation verbs. The
 * entry hands out this exact object; no per-view adapter narrows it. */
export type NativeConversationService = ReturnType<typeof createConversationService<NativeMutationReadLease>>;

/** The one entry-owned read-retry actor. It moves the screen's former backoff
 * into the entry so every view observes one failure count and one timer.
 * Retirement cancels it before the store closes. */
export interface NativeConversationReadActor {
	/** Consecutive read failures the transcript's retry line reads. */
	readonly failures: number;
	readonly cancelled: boolean;
	readonly getFailures: () => number;
	subscribe(listener: () => void): () => void;
	cancel(): void;
}

export interface NativeConversationSession {
	readonly hubId: string;
	readonly ref: string;
	readonly key: string;
	readonly store: NativeConversationStore;
	/** The stable submitter the store admits mutations through, shared so a
	 * view's command and steer paths use the same live host lookup. */
	readonly mutationSubmitter: ConversationMutationSubmitter;
	readonly activity: NativeConversationActivity;
	/** The exact sink object every open/read of this entry binds. */
	readonly activitySink: LiveActivitySink;
	/** The one live host/target registration, or null while offline. */
	readonly liveHost: NativeMutationHost | null;
	/** The one live service for the current client binding, or null offline. */
	readonly service: NativeConversationService | null;
	readonly displayConfig: TranscriptDisplayConfigV1 | null;
	readonly readActor: NativeConversationReadActor;
	readonly retired: boolean;
	readonly attachmentCount: number;
	/** Settles when every authoritative read this entry has started settles,
	 * including one a retirement or client replacement fenced: a test or the
	 * future read actor can await the exact late reply instead of draining
	 * arbitrary microtasks. */
	readSettled(): Promise<void>;
}

export interface NativeConversationAttachment {
	readonly entry: NativeConversationSession;
	/** Whether this exact handle still owns its entry. A released handle is
	 * dead while a sibling's attachment keeps the entry alive. */
	readonly live: boolean;
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
	following: boolean;
}

class SessionEntry implements NativeConversationSession {
	readonly hubId: string;
	readonly ref: string;
	readonly key: string;
	readonly store: NativeConversationStore;
	readonly mutationSubmitter: ConversationMutationSubmitter;
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
	#service: NativeConversationService | null = null;
	#bindingClient: AppwireClientLike | null = null;
	#bindingPending = false;
	#pendingPort: ReturnType<typeof createConversationMutationPendingPort> | null = null;
	#unbindPending: (() => void) | null = null;
	#readSettled: Promise<void> = Promise.resolve();
	#retired = false;
	#readFailures = 0;
	#retryTimer: ReturnType<typeof setTimeout> | null = null;
	#retryToken: symbol | null = null;
	// Which client binding owns the in-flight retry. A replacement binding must
	// not inherit either the "one attempt out" guard or the old attempt's
	// failure accounting.
	#bindingGeneration = 0;
	#retryAttemptBinding: number | null = null;
	#readListeners = new Set<() => void>();

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
		this.mutationSubmitter = createDurableSubmitter(() => this.#liveHost);
		this.store = createConversationStore({
			mutationHubId: this.hubId,
			mutationSubmitter: this.mutationSubmitter,
			displayConfig: this.#displayConfig,
		});
		const entry = this;
		this.readActor = {
			get failures() {
				return entry.#readFailures;
			},
			get cancelled() {
				return entry.#retired;
			},
			getFailures: () => entry.#readFailures,
			subscribe: (listener) => {
				entry.#readListeners.add(listener);
				return () => {
					entry.#readListeners.delete(listener);
				};
			},
			cancel: () => entry.#clearRetry(),
		};
		this.#unsubscribeLevels = this.#levels.subscribe(() => this.#applyDisplayConfig());
		this.#unsubscribeStore = this.store.subscribe(() => {
			this.#rebindPendingIfNeeded();
			this.#observeRead();
		});
		this.#observeRead();
	}

	get liveHost(): NativeMutationHost | null {
		return this.#liveHost;
	}

	get service(): NativeConversationService | null {
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
			// A new reader starts conservatively as not following until its
			// restoration settles.
			following: false,
		};
		const attached = () => !this.#retired && this.#attachments.get(token) === state;
		const handle: NativeConversationAttachment = {
			entry: this,
			get live() {
				return attached();
			},
			setFollowingLiveEnd: (following) => {
				if (!attached()) return;
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
		if (client === this.#bindingClient && this.#service !== null && client.state === "ready") {
			// A later ready event can retry a missing host through one explicit
			// authoritative read, without replacing the healthy service.
			if (this.#liveHost === null) {
				const read = this.store.getState().ensureLatest();
				this.#readSettled = Promise.allSettled([this.#readSettled, read]).then(() => undefined);
			}
			return;
		}
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
		this.#pendingPort = null;
		this.#unbindPending = null;
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
		// Retire the binding before releasing anything: an older retry's
		// settlement compares against this generation and can no longer touch
		// the replacement's failure count.
		this.#bindingGeneration += 1;
		this.#bindingClient = null;
		this.#liveHost = null;
		this.#service = null;
		this.#pendingPort = null;
		this.#clearRetry();
		// Fence the store binding before releasing the service/host, so a late
		// response from the old client can never reconcile the replacement.
		this.store.getState().suspendProjected();
		service?.close();
		host?.dispose();
	}

	#acquireHost(client: AppwireClientLike, binding: number): NativeMutationHost | null {
		if (this.#retired || binding !== this.#bindingGeneration || client !== this.#bindingClient) return null;
		if (this.#liveHost !== null) return this.#liveHost;
		let host: NativeMutationHost;
		try {
			const runtime = this.#runtime();
			host = createNativeMutationHost(runtime, this.hubId, this.ref, client);
			this.#pendingPort = createConversationMutationPendingPort(runtime, this.key);
		} catch {
			// History is independent of local durable storage. Leave pending rows
			// unverified and retry only on a later authoritative-read demand.
			// A disconnected healthy seam normally survives for offline sends;
			// it cannot certify readiness for this failed runtime acquisition.
			this.#unbindPending?.();
			this.#unbindPending = null;
			return null;
		}
		this.#liveHost = host;
		void host.start().catch(() => undefined);
		this.#bindPending();
		return host;
	}

	#startBinding(client: AppwireClientLike): void {
		this.#bindingClient = client;
		const binding = this.#bindingGeneration;
		// Service/read ownership never depends on opening the mutations DB.
		// Once acquired, this closure keeps its exact host even if the entry
		// later replaces the client. A hostless read has no lease to reconcile.
		let host: NativeMutationHost | null = null;
		const service = createConversationService<NativeMutationReadLease>(client, {
			resolveDisplayConfig: () => this.#displayConfig,
			onReadStart: (ref, expectedThreadId) => {
				host ??= this.#acquireHost(client, binding);
				return host?.beginRead(ref, expectedThreadId);
			},
			onReadComplete: (lease, response) => host?.reconcileRead(lease, response),
		});
		this.#service = service;
		const state = this.store.getState();
		const read =
			state.ref === this.ref && state.conversation !== null
				? state.resumeProjected(service, this.activitySink, this.ref)
				: state.openProjected(service, this.activitySink, this.ref);
		// Keep every read this entry started in the settle chain: a replaced or
		// retired binding's late reply is exactly what a caller may need to await.
		this.#readSettled = Promise.allSettled([this.#readSettled, read]).then(() => undefined);
		// openProjected/resumeProjected retire the durable pending seam; the
		// entry reinstalls it for the binding that just started (and the store
		// subscription below covers later thread opens, including /clear).
		this.#bindPending();
		void read.catch(() => undefined);
	}

	#bindPending(): void {
		const port = this.#pendingPort;
		if (this.#retired || this.#service === null || port === null || this.#bindingPending) return;
		// Binding publishes null rows before the store installs its port. Its
		// synchronous subscriber must not start another bind in that interval.
		this.#bindingPending = true;
		try {
			this.#unbindPending = this.store.getState().bindPendingMutationsIfUnbound(port) ?? this.#unbindPending;
		} catch {
			// The mutations database could not be opened. The conversation stays
			// usable; a later connection change retries the bind.
		} finally {
			this.#bindingPending = false;
		}
	}

	#rebindPendingIfNeeded(): void {
		if (this.#retired || this.#service === null) return;
		const state = this.store.getState();
		if (state.status === "open" && state.pendingMutations === null) this.#bindPending();
	}

	/** Mirror the existing per-screen retry: an error starts the count at one,
	 * an open read clears it, and each timer retry counts only when the store is
	 * still in error when it settles. */
	#observeRead(): void {
		if (this.#retired) return;
		const state = this.store.getState();
		// A rehydrate failure keeps the retained display model readable and only
		// moves latestReadState to "error"; following status alone would never
		// arm the retry. Both the status failure and the read-state failure are
		// the same demand on this one actor.
		if (!this.#readFailed()) {
			// Only an accepted ready publication (or a binding that never left
			// readable history) clears the demand; "reading" keeps it armed while
			// a newer attempt is out.
			if (state.status === "open" && state.latestReadState !== "reading") {
				this.#clearRetry();
				this.#setReadFailures(0);
			}
			return;
		}
		this.#setReadFailures(this.#readFailures === 0 ? 1 : this.#readFailures);
		this.#scheduleRetry();
	}

	#readFailed(): boolean {
		const state = this.store.getState();
		return state.status === "error" || state.latestReadState === "error";
	}

	#setReadFailures(failures: number): void {
		if (this.#readFailures === failures) return;
		this.#readFailures = failures;
		for (const listener of [...this.#readListeners]) listener();
	}

	#scheduleRetry(): void {
		if (this.#retired || this.#retryToken !== null) return;
		// Only an attempt belonging to the CURRENT binding blocks a new one. A
		// retired binding's held retry must not starve the replacement client.
		if (this.#retryAttemptBinding === this.#bindingGeneration) return;
		if (this.#service === null) return;
		const delay = reconnectDelay(this.#readFailures - 1);
		const token = Symbol("native-conversation-read-retry");
		this.#retryToken = token;
		const fire = () => {
			if (this.#retryToken !== token) return;
			this.#retryToken = null;
			this.#retryTimer = null;
			this.#retryRead();
		};
		// The first retry is due at once. A microtask keeps that promise without
		// depending on a zero-delay timer turn, which a test's fake clock does
		// not owe between two reads.
		if (delay === 0) {
			queueMicrotask(fire);
			return;
		}
		this.#retryTimer = setTimeout(fire, delay);
	}

	#retryRead(): void {
		const service = this.#service;
		if (this.#retired || service === null) return;
		if (!this.#readFailed()) return;
		const attempt = this.#readFailures;
		const binding = this.#bindingGeneration;
		this.#retryAttemptBinding = binding;
		const settle = () => {
			if (this.#retryAttemptBinding === binding) this.#retryAttemptBinding = null;
			// A replaced binding's late reply changes nothing here.
			if (this.#retired || binding !== this.#bindingGeneration) return;
			// Only a retry that left the store failed counts. A newer read that
			// took over ("reading") does not; if it fails, the next observation
			// re-arms at the same step.
			if (this.#readFailed()) {
				this.#setReadFailures(attempt + 1);
				this.#scheduleRetry();
			}
		};
		const state = this.store.getState();
		// A failed refresh did not retire this open binding. Reopening it would
		// fence ordinary mutations still awaiting admission or a direct reply.
		const read =
			state.status === "open" ? state.ensureLatest() : state.resumeProjected(service, this.activitySink, this.ref);
		void read.then(settle, settle);
	}

	#clearRetry(): void {
		if (this.#retryTimer !== null) clearTimeout(this.#retryTimer);
		this.#retryTimer = null;
		// An already-queued immediate retry checks its token and drops out.
		this.#retryToken = null;
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
