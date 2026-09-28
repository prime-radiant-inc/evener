// Stop from the Board (spec 7.3), dispatched exactly as the Session's Stop
// is (ruling 17): a durable turn/interrupt through the process's mutation
// runtime, whose one write cancels the session's never-sent rows and bumps
// its stop epoch (NativeMutationRuntime.submit, enqueueInterruptAndCancel).
// The runtime sends only for a target a live host registered and opened with
// an authoritative read, so a Stop does what a Session screen's mount does
// (createNativeMutationHost, start, a fenced thread/read), enqueues the
// interrupt before it opens that gate, and holds the registration until the
// interrupt has left the outbox.
import type { AppwireClientLike, ThreadReadResponse } from "@evener/appwire-client";
import { sessionControls } from "@evener/appwire-client";
import { READ_ITEM_LIMIT } from "../../../mobile/src/services/conversation";
import {
	createNativeMutationHost,
	type NativeMutationHost,
} from "../nativeMutationHost";
import {
	type NativeMutationRuntime,
	nativeMutationTargetKey,
} from "../nativeMutationRuntime";

/** "stopped": the interrupt is durably admitted, the Session's "Stopped"
 * moment. "notWorking": a fresh read shows no turn to stop, so nothing was
 * sent. "unavailable": the session couldn't be read or opened for mutations. */
export type StopOutcome = "stopped" | "notWorking" | "unavailable";

export class BoardStops {
	readonly #runtime: () => NativeMutationRuntime;
	readonly #hubId: string;
	readonly #inFlight = new Map<string, Promise<StopOutcome>>();
	readonly #holds = new Map<string, () => void>();
	#disposed = false;

	/** `runtime` is called at the first Stop, so a Board that never stops never
	 * opens the mutations database (as ConversationScreen defers it). */
	constructor(runtime: () => NativeMutationRuntime, hubId: string) {
		this.#runtime = runtime;
		this.#hubId = hubId;
	}

	/** Whether a Stop for `ref` is still being read, sent or delivered. */
	stopping(ref: string): boolean {
		return this.#inFlight.has(ref) || this.#holds.has(ref);
	}

	stop(client: AppwireClientLike, ref: string): Promise<StopOutcome> {
		const running = this.#inFlight.get(ref);
		if (running) return running;
		if (this.#holds.has(ref)) return Promise.resolve("stopped");
		const run = this.#stop(client, ref).finally(() => this.#inFlight.delete(ref));
		this.#inFlight.set(ref, run);
		return run;
	}

	/** Lets go of every Stop still being delivered (the Board's client changed).
	 * An interrupt not yet sent stays in the outbox, delivered as any durable
	 * Stop is. */
	releaseAll(): void {
		for (const release of [...this.#holds.values()]) release();
	}

	dispose(): void {
		this.#disposed = true;
		this.releaseAll();
	}

	async #stop(client: AppwireClientLike, ref: string): Promise<StopOutcome> {
		if (this.#disposed || client.state !== "ready") return "unavailable";
		let runtime: NativeMutationRuntime;
		let host: NativeMutationHost;
		try {
			runtime = this.#runtime();
			host = createNativeMutationHost(runtime, this.#hubId, ref, client);
		} catch {
			return "unavailable";
		}
		try {
			await host.start();
		} catch {
			host.dispose();
			return "unavailable";
		}
		const lease = host.beginRead(ref);
		if (!lease) {
			host.dispose();
			return "unavailable";
		}
		let response: ThreadReadResponse;
		try {
			// The Session's own bounded projection read (conversation.ts
			// readProjection), without the subscription the Board doesn't hold.
			response = await client.request("thread/read", {
				ref,
				includeTurns: true,
				itemsView: "fragment",
				itemLimit: READ_ITEM_LIMIT,
			});
		} catch {
			host.dispose();
			return "unavailable";
		}
		const { thread } = response;
		const status = thread.status.type;
		// What reconcileAuthoritativeRead won't open the gate for: an interrupt
		// enqueued then would sit in the outbox until the session is opened.
		if (thread.evener.resumeRequired === true || status === "restartRequired" || status === "notLoaded") {
			host.dispose();
			return "unavailable";
		}
		// The store's requireControl(conversation, "stop", "interrupt"). The wire
		// omits a zero queue depth, as the store reads it.
		if (!sessionControls(status, thread.evener.capabilities, thread.evener.queue.depth ?? 0).stop) {
			host.dispose();
			return "notWorking";
		}
		try {
			// Enqueued before the gate opens, so its cancel of never-sent rows
			// lands before anything on this session can send.
			await host.submit({
				kind: "interrupt",
				hubId: this.#hubId,
				targetRef: ref,
				threadId: thread.id,
				instanceId: thread.evener.instanceId ?? thread.id,
				input: [],
			});
		} catch {
			host.dispose();
			return "unavailable";
		}
		this.#hold(runtime, host, client, ref);
		// Opens the dispatch gate on this read; the runtime then sends the
		// interrupt. A lease gone stale (the connection dropped in these few
		// milliseconds) leaves it in the outbox, delivered as any durable Stop is.
		await host.reconcileRead(lease, response);
		return "stopped";
	}

	/** Keeps the registration until the interrupt leaves `submitting` (sent,
	 * refused into recovery, or blocked on an unknown outcome), or until the
	 * connection drops, which blocks the target anyway. */
	#hold(runtime: NativeMutationRuntime, host: NativeMutationHost, client: AppwireClientLike, ref: string): void {
		const targetKey = nativeMutationTargetKey(this.#hubId, ref);
		let released = false;
		const release = () => {
			if (released) return;
			released = true;
			stopStorage();
			stopState();
			host.dispose();
			if (this.#holds.get(ref) === release) this.#holds.delete(ref);
		};
		const check = async () => {
			const { outbox } = await runtime.read(targetKey);
			if (!outbox.some((record) => record.method === "turn/interrupt" && record.state === "submitting")) release();
		};
		const stopStorage = runtime.subscribeStorage((targetKeys) => {
			if (targetKeys.includes(targetKey)) void check().catch(() => undefined);
		});
		const stopState = client.onStateChange((state) => {
			if (state !== "ready") release();
		});
		this.#holds.set(ref, release);
		if (this.#disposed) release();
	}
}
