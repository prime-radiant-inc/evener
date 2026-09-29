// Sends what no open session is sending (spec 8.5's offline row, ruling 17):
// a message the phone kept while offline, in a session you have since left,
// goes out when the connection returns, as the web's handleReady does
// (cmd/evener-hub/frontend/src/stores/threads.ts); so does anything admitted
// for a session no screen holds, such as a review sent from a Reader opened
// from the Board. The flush takes only targets nobody has registered,
// settles each with a read that leaves the connection's subscription alone
// (NativeMutationRuntime.settleTarget), and lets each go once nothing on it
// is waiting to be sent.
// A session screen owns its own target; the flush only settles one that has
// something waiting, for a screen under the Reader or a subagent, which
// doesn't read while it's covered.
//
// The flush opens targets; the runtime sends. A settle's reconcile dispatches
// everything already waiting (reconcileAuthoritativeRead, dispatchTargets),
// and a record committed for a target that is open is discovered and sent on
// commit (MutationOutbox's announceCommit). So a record that lands during or
// after a settle goes either way; the flush only decides when to let a
// registration go.
import type { AppwireClientLike } from "@evener/appwire-client";
import { reconnectDelay } from "../hubConnection";

export type SettleResult = "open" | "reconciled" | "blocked" | "stale" | "unregistered";

/** The slice of NativeMutationRuntime the flush uses. */
export interface FlushRuntime {
	readonly storage: {
		listTargetRefs(): Promise<string[]>;
		listOutbox(targetRef: string): Promise<readonly { state: string }[]>;
	};
	start(): Promise<void>;
	targetClient(hubId: string, targetRef: string): AppwireClientLike | undefined;
	registerTarget(hubId: string, targetRef: string, client: AppwireClientLike | null): () => void;
	settleTarget(hubId: string, targetRef: string, client: AppwireClientLike): Promise<SettleResult>;
	subscribeStorage(listener: (targetRefs: readonly string[]) => void): () => void;
}

/** The hub and ref a composite target key names (nativeMutationTargetKey),
 * or null for a key of any other shape. */
export function parseTargetKey(key: string): { hubId: string; ref: string } | null {
	try {
		const value: unknown = JSON.parse(key);
		if (Array.isArray(value) && value.length === 2 && typeof value[0] === "string" && typeof value[1] === "string")
			return { hubId: value[0], ref: value[1] };
	} catch {
		// Not a composite key: not the flush's to send.
	}
	return null;
}

/** The flush's hold on one target it is sending for. Each claim is its own
 * object, so a late continuation can only ever let go of its own. */
interface Claim {
	release(): void;
	/** Its settle is out: only that settle's end may let it go. */
	settling: boolean;
}

export class OutboxFlush {
	private hubId: string | null = null;
	private client: AppwireClientLike | null = null;
	private generation = 0;
	private readonly owned = new Map<string, Claim>();
	/** Claimed targets whose records changed while their settle was out. */
	private readonly touched = new Set<string>();
	/** Screens' targets this connection is settling for them right now. */
	private readonly settlingForScreens = new Map<string, symbol>();
	private unsubscribe: (() => void) | null = null;
	/** A look again after storage failed, and how many failed in a row. */
	private retry: ReturnType<typeof setTimeout> | null = null;
	private failures = 0;

	/** `runtime` is read at the first ready connection: a message kept from an
	 * earlier launch can only be found in the mutations database. */
	constructor(private readonly runtime: () => FlushRuntime) {}

	/** The active hub and its client while it is ready, else nulls. A new
	 * client flushes, and watches for work no screen will send; losing it lets
	 * every target go. */
	bind(hubId: string | null, client: AppwireClientLike | null): void {
		if (hubId === this.hubId && client === this.client) return;
		for (const claim of this.owned.values()) claim.release();
		this.owned.clear();
		this.touched.clear();
		this.settlingForScreens.clear();
		this.stopRetrying();
		// The runtime calls storage listeners synchronously, and unsubscribing
		// deletes this one at once, so no callback of an earlier bind runs after
		// this line. A check one started before holds the claim it found, and
		// release() lets go of that claim only.
		this.unsubscribe?.();
		this.unsubscribe = null;
		this.hubId = hubId;
		this.client = client;
		this.generation += 1;
		if (hubId === null || client === null) return;
		const runtime = this.runtime();
		this.unsubscribe = runtime.subscribeStorage((keys) => this.changed(runtime, keys));
		void this.flush().catch(() => undefined);
	}

	/** Looks again, for a session screen that let go of its target while the
	 * connection was live. */
	async flush(): Promise<void> {
		const { hubId, client, generation } = this;
		if (hubId === null || client === null || client.state !== "ready") return;
		const runtime = this.runtime();
		let keys: string[];
		try {
			// A fresh runtime dispatches nothing until started.
			await runtime.start();
			keys = await runtime.storage.listTargetRefs();
		} catch {
			// Storage failed before anything was claimed: look again after a
			// backoff while this connection lasts, as the Board retries a read.
			this.retryLater(generation);
			return;
		}
		if (generation !== this.generation) return;
		this.stopRetrying();
		// Every target settles at once, as the web's handleReady does, so a read
		// the hub is slow to answer holds up no other. A claim is taken before a
		// settle's first await, so a flush running beside this one finds the
		// target owned and skips it. Each settle catches its own failure, which
		// leaves that target for the next ready connection or the next record.
		await Promise.all(
			keys.map((key) => {
				const target = parseTargetKey(key);
				if (target === null || target.hubId !== hubId || this.owned.has(key)) return undefined;
				return this.settle(runtime, key, hubId, target.ref, client).catch(() => undefined);
			}),
		);
	}

	dispose(): void {
		this.bind(null, null);
	}

	private async settle(
		runtime: FlushRuntime,
		key: string,
		hubId: string,
		ref: string,
		client: AppwireClientLike,
	): Promise<void> {
		const holder = runtime.targetClient(hubId, ref);
		if (holder !== undefined) {
			// A session screen holds this target. Under the Reader or a subagent
			// it doesn't read, so after a reconnect its waiting message would
			// wait for a trip back: settle it for the screen, which keeps its
			// registration. Only a screen on this connection: a target held
			// with another client is that client's. A screen's target is never
			// the flush's to claim, so a mark keeps one settle at a time, and
			// only the settle that set it clears it (bind() clears them all).
			if (holder !== client || this.settlingForScreens.has(key)) return;
			const mark = Symbol(key);
			this.settlingForScreens.set(key, mark);
			try {
				if (await this.waiting(runtime, key)) await runtime.settleTarget(hubId, ref, client);
			} finally {
				if (this.settlingForScreens.get(key) === mark) this.settlingForScreens.delete(key);
			}
			return;
		}
		const { generation } = this;
		const claim: Claim = { release: runtime.registerTarget(hubId, ref, client), settling: true };
		this.owned.set(key, claim);
		this.touched.delete(key);
		let settled = false;
		try {
			// A read an older connection started can't land here: bind() lets
			// that connection's claims go, and the runtime answers a read for a
			// registration that is gone or replaced with "stale", before it
			// reconciles or dispatches anything (isCurrentRead).
			const answer = await runtime.settleTarget(hubId, ref, client);
			settled = answer === "reconciled" || answer === "open";
		} catch {
			// A read or storage failure: let go, as for a read that failed.
		}
		claim.settling = false;
		if (settled) await this.releaseIfDone(runtime, key, claim, generation);
		else this.letGo(key, claim);
	}

	/** Lets go of a target the flush couldn't settle. A record that landed on
	 * it meanwhile was left to that settle, so look again for it; a failure
	 * with nothing new waits for the next record or connection, so a failing
	 * read never spins. */
	private letGo(key: string, claim: Claim): void {
		if (!this.release(key, claim)) return;
		if (this.touched.delete(key)) void this.flush().catch(() => undefined);
	}

	/** A target the flush holds may be done; a record for one nobody holds is
	 * work no screen will send. A claim whose settle is out only notes the
	 * change: that settle's end decides. A target a session screen holds stays
	 * that screen's here: its records come from the screen, or bring their own
	 * settle (a message sent from a screen above it), and settling it on every
	 * change would re-read after each unknown outcome and could resend in a
	 * loop. */
	private changed(runtime: FlushRuntime, keys: readonly string[]): void {
		let unclaimed = false;
		for (const key of keys) {
			const claim = this.owned.get(key);
			if (claim !== undefined) {
				if (claim.settling) this.touched.add(key);
				else void this.releaseIfDone(runtime, key, claim, this.generation);
				continue;
			}
			const target = parseTargetKey(key);
			if (
				target !== null &&
				target.hubId === this.hubId &&
				runtime.targetClient(target.hubId, target.ref) === undefined
			)
				unclaimed = true;
		}
		if (unclaimed) void this.flush().catch(() => undefined);
	}

	/** A target is done once nothing on it is waiting to be sent: what's left
	 * is settled, or waits for you in its session (a message it couldn't
	 * confirm, or one a Stop held). When storage can't say, the claim goes
	 * and the flush looks again after the backoff, rather than holding the
	 * target until the next reconnect or storage change. */
	private async releaseIfDone(runtime: FlushRuntime, key: string, claim: Claim, generation: number): Promise<void> {
		let waiting: boolean;
		try {
			waiting = await this.waiting(runtime, key);
		} catch {
			if (this.release(key, claim)) this.retryLater(generation);
			return;
		}
		if (!waiting) this.release(key, claim);
	}

	/** Looks again after the Board's backoff (reconnectDelay: 1, 2, 4, 8 and
	 * 16 seconds, then every 30), while the connection the failed look ran on
	 * is still bound; bind() stops it. */
	private retryLater(generation: number): void {
		if (generation !== this.generation || this.retry !== null) return;
		this.failures += 1;
		this.retry = setTimeout(() => {
			this.retry = null;
			void this.flush().catch(() => undefined);
		}, reconnectDelay(this.failures));
	}

	private stopRetrying(): void {
		if (this.retry !== null) clearTimeout(this.retry);
		this.retry = null;
		this.failures = 0;
	}

	private async waiting(runtime: FlushRuntime, key: string): Promise<boolean> {
		const records = await runtime.storage.listOutbox(key);
		return records.some((record) => record.state === "submitting");
	}

	/** Lets go of this claim, and only this one: a late continuation of an
	 * older claim, or an older connection's, finds another claim or none, and
	 * leaves it be. True when it let go. */
	private release(key: string, claim: Claim): boolean {
		if (this.owned.get(key) !== claim) return false;
		this.owned.delete(key);
		claim.release();
		return true;
	}
}
