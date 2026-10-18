import type { NavigationInvalidatedPayload, NavigationMutation, NavigationReadParams } from "@evener/appwire-client";
import {
	applyDelta,
	type DecodedNavigationResponse,
	decodeNavigationResponse,
	isSequenceGap,
	matchingTargets,
	materializeNavigationResource,
	navigationParamsToResourceKey,
	type NormalizedResource,
	reconcileSnapshot,
	requiredRevision,
	type ResourceKey,
	snapshotResource,
} from "@evener/appwire-client/state/navigation";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { reconnectDelay } from "./hubConnection";
import { singleFlight } from "./singleFlight";

export interface PageState<T> {
	loaded: boolean;
	rows: T[];
	remaining: number;
	loading: boolean;
	error: string | null;
	stale: boolean;
}
/** Whether a list should say "Updating…": its rows are known to be outdated
 * and no error is on screen. A failed automatic re-read leaves stale and
 * error set together, and "Updating…" must never hide the error; views that
 * show an action's error beside the page pass that merged error in. This
 * holds while the re-read is in flight, since stale stays set until that
 * read lands. */
export function updating(page: Pick<PageState<unknown>, "loading" | "error" | "stale" | "remaining">) {
	return page.stale && !page.error;
}
/** What a page list reads and drives: NavigationPages for navigation's own
 * resources, and ArchivedPages (archivedPages.ts) for a project's archived
 * sessions, which navigation doesn't serve. */
export interface PageSource<T> {
	/** Whether navigation versions this list. A confirm step checks a change
	 * against the version it read; an unversioned source has none to check. */
	readonly navigationVersioned: boolean;
	getSnapshot(): PageState<T>;
	getResourceVersion(): NormalizedResource["version"] | null;
	subscribe(listener: () => void): () => void;
	watch(owner?: () => void): () => void;
	refresh(): Promise<unknown>;
	refreshAfter(receipt: NavigationMutation): Promise<unknown>;
	more(): Promise<unknown>;
	cancel(): void;
	resume(): void;
}
type NativeNavigationParams = Omit<NavigationReadParams, "representationVersion">;
export class NavigationPages<T> implements PageSource<T> {
	readonly navigationVersioned = true;
	private state: PageState<T> = {
		loaded: false,
		rows: [],
		remaining: 0,
		loading: false,
		error: null,
		stale: false,
	};
	private listeners = new Set<() => void>();
	private request = 0;
	private offset = 0;
	private version: { generationId: string; revision: number } | null = null;
	private notifiedGeneration = "";
	private requiredRevision = 0;
	private sequence = 0;
	private uncertain = 0;
	private notificationEpoch = 0;
	private mutationFloor = 0;
	private mutationGeneration: string | null = null;
	private normalized: NormalizedResource | null = null;
	/** The resource these pages read, as invalidation targets name it; the
	 * page offsets play no part in matching. */
	readonly resourceKey: ResourceKey;
	private owner: (() => void) | null = null;
	private paused = false;
	private rereads = singleFlight(
		() => this.recover(),
		() => !this.paused && !this.state.loading,
	);
	/** The recovery flight a joined refresh observes, resolved when the
	 * re-read settles: handing a refresh to the flight must not report done
	 * while pages are still loading. */
	private recovery: Promise<boolean> | null = null;
	private recover(): Promise<boolean> {
		const flight = (async () => {
			try {
				return await this.rereadLoadedDepth();
			} finally {
				this.recovery = null;
			}
		})();
		this.recovery = flight;
		return flight;
	}
	/** The retry a failed read arms on its own when the constructor's readRetry
	 * opts in (the same capped backoff the Board's failed reads retry on,
	 * useBoardReadRetry): grows with each failure in a row, retires when a read
	 * lands, and never runs while the page is paused (out of view) — cancel()
	 * abandons it and resume() re-arms what the page still owes. */
	private retryTimer: ReturnType<typeof setTimeout> | null = null;
	private retryCount = 0;
	/** The row depth a failed re-read still owes: the reader had reached it,
	 * so a retry re-reads to THIS depth, not to whatever survived the last
	 * partial refresh. Cleared when the re-read finally reaches it. */
	private owedDepth: number | null = null;
	/** Whether the owed depth belongs to a recovery walk (an invalidation's
	 * re-read, a manual press's continuation) rather than a plain failed page
	 * read. A cancelled recovery's pages were visible and resume restores
	 * them; a merely failed page's retry is the owner's to pace. */
	private recoveryDepth = false;

	/** Whether a failed read re-arms on its own. Off by default: a consumer
	 * whose reads another owner already retries (the Board's own rebind,
	 * useBoardReadRetry) must not double-fire it. */
	private readonly readRetry: boolean;
	/** Re-reads from the first page down to the loaded depth, so a retry of a
	 * loaded page keeps the list's length; on the shared rereads flight so an
	 * invalidation's re-read and a retry's never run twice at once. */
	private retryRead = () => {
		this.retryTimer = null;
		this.rereads.request();
	};
	private armRetry() {
		if (!this.readRetry || this.paused || this.retryTimer !== null) return;
		this.retryCount += 1;
		this.retryTimer = setTimeout(this.retryRead, reconnectDelay(this.retryCount));
	}
	private clearRetry(success: boolean) {
		if (this.retryTimer !== null) {
			clearTimeout(this.retryTimer);
			this.retryTimer = null;
		}
		if (success) this.retryCount = 0;
	}
	constructor(
		private client: ConversationClientLike,
		private params: NativeNavigationParams,
		private field: "projects" | "sessions" | "pin_sections",
		private key: (row: T) => string,
		private limit = 50,
		{ readRetry = false }: { readRetry?: boolean } = {},
	) {
		this.readRetry = readRetry;
		this.resourceKey = navigationParamsToResourceKey({
			...params,
			representationVersion: 3,
		});
	}
	/** Follow hub invalidations. Newer data is re-read here without user
	 * action unless an owner takes every re-read to run a wider read of its
	 * own (the pin screens also re-read a session's location). */
	watch(owner?: () => void) {
		this.owner = owner ?? null;
		return this.client.onNotification((event) => {
			if (event.method === "evener/navigation/invalidated") this.invalidate(event.params);
		});
	}
	getSnapshot = () => this.state;
	getResourceVersion = () => this.normalized?.version ?? null;
	subscribe = (listener: () => void) => {
		this.listeners.add(listener);
		return () => this.listeners.delete(listener);
	};
	private publish(s: Partial<PageState<T>>) {
		this.state = { ...this.state, ...s };
		for (const listener of this.listeners) listener();
	}
	/** The loaded rows predate a change the hub announced: re-read them once
	 * the store is idle, or hand the re-read to the owner. */
	private reread() {
		this.publish({ stale: true });
		this.scheduleReread();
	}
	private scheduleReread() {
		if (this.owner) this.owner();
		else this.rereads.request();
	}
	/** Re-read from the first page down to the depth the user had scrolled
	 * to, so the list keeps its length and their place in it. */
	private async rereadLoadedDepth() {
		// A partial refresh can leave fewer rows loaded than the reader had
		// reached; the owed depth remembers it until the re-read catches up.
		const depth = Math.max(this.offset, this.owedDepth ?? 0);
		// Hold the target across the reads: a failure of the reset read (page
		// one) must not shrink the depth the retry still owes to what survived.
		this.owedDepth = depth;
		this.recoveryDepth = true;
		const landed =
			(await this.load(true, undefined, { walker: true })) === true &&
			(this.offset >= depth || this.state.remaining <= 0 || (await this.walkToDepth(depth)) === true);
		// Recovery completes at the depth it owed, or when the results end;
		// a failure keeps what it owes for the next attempt to reach for.
		if (landed) {
			this.owedDepth = null;
			this.recoveryDepth = false;
			if (this.state.error !== null) this.publish({ error: null });
		}
		return landed;
	}
	/** Walk the remaining pages to the depth a recovery owes. */
	private async walkToDepth(depth: number): Promise<boolean> {
		while (this.offset < depth && this.state.remaining > 0)
			if ((await this.load(false, undefined, { walker: true })) !== true) return false;
		return true;
	}
	private invalidate(p: NavigationInvalidatedPayload) {
		if (this.notifiedGeneration !== p.generationId) {
			this.notifiedGeneration = p.generationId;
			this.sequence = 0;
			this.requiredRevision = 0;
		}
		if (p.sequence <= this.sequence) return;
		const gap = isSequenceGap(this.sequence, p.sequence);
		this.sequence = p.sequence;
		this.notificationEpoch++;
		const targets = matchingTargets(this.resourceKey, p.targets);
		if (gap || targets.some((t) => t.revision === undefined)) this.uncertain++;
		this.requiredRevision = Math.max(this.requiredRevision, requiredRevision(this.resourceKey, p.targets));
		const loaded = this.version;
		const held =
			!gap &&
			loaded?.generationId === p.generationId &&
			targets.every((t) => t.revision !== undefined && t.revision <= loaded.revision);
		if ((targets.length > 0 || gap) && !held) this.publish({ stale: true });
		// A read in flight sees this notification through its revision checks
		// and retries itself once, so only an idle store starts a re-read. That
		// bounds a hub that notifies on every read to one retry per read; a hub
		// that served an older revision than it announced gets one more read per
		// later notification, never a loop. The rows stay stale either way, so a
		// cancelled read still leaves the owed re-read behind.
		if (this.state.stale && !this.state.loading) this.scheduleReread();
	}
	/** Drop the read in flight and stop re-reading on the owner's behalf until
	 * resume(); a re-read the hub still owes waits for it. The dropped read may
	 * never settle (a request the transport silently dropped while the app was
	 * backgrounded), so the flight is abandoned here rather than left running:
	 * otherwise every later invalidation for the page would be dropped too. */
	cancel() {
		this.request++;
		this.paused = true;
		this.clearRetry(false);
		this.rereads.abandon();
		this.publish({ loading: false });
		if (this.state.stale) this.scheduleReread();
	}
	resume() {
		this.paused = false;
		// A failed read the pause held back is still owed, and so is a
		// recovery cancelled mid-walk: its earlier pages may have landed clean,
		// leaving neither stale nor error, while the depth it still owes is
		// the only record of the rows the reader had. The depth re-arms for
		// every reader — the pages were visible, and returning must restore
		// them — while the automatic retry of a merely failed read stays
		// opt-in, where the page already owns its own retry pacing. A stale
		// page schedules its own re-read; queueing the depth behind it too
		// would leave a trailing run that fires the moment the resumed walk
		// fails, never waiting out the backoff.
		if (this.state.stale) this.scheduleReread();
		else if (this.state.error !== null && this.readRetry) this.rereads.request();
		else if (this.recoveryDepth && this.owedDepth !== null) this.rereads.request();
		this.rereads.drain();
	}
	/** An explicit read means the owner is active again; its result stands
	 * in for any re-read owed while paused. */
	refresh() {
		this.paused = false;
		// A refresh while depth is owed is the reader pressing Try again: it
		// completes the recovery the automatic retry would, to the owed depth,
		// so it cannot strand what it owes on a changed first page.
		if (this.owedDepth !== null) {
			this.rereads.request();
			// The caller observes the flight's own completion — a recovery
			// already running, or one queued behind the request in flight —
			// never an immediate true while pages are loading.
			return this.rereads.settled().then(() => this.recovery === null);
		}
		return this.load(true);
	}
	more() {
		// Stale rows cannot be paged past; catch up from the first page instead.
		if (this.state.stale && !this.state.loading) return this.load(true);
		return this.load(false);
	}
	async refreshAfter(receipt: NavigationMutation) {
		if (this.mutationGeneration !== receipt.generation_id) this.mutationFloor = 0;
		this.mutationGeneration = receipt.generation_id;
		this.mutationFloor = Math.max(this.mutationFloor, requiredRevision(this.resourceKey, receipt.targets));
		if (!(await this.load(true, receipt)))
			throw new Error("The change was accepted, but the updated list could not be loaded. Try again.");
	}
	private async load(
		reset: boolean,
		receipt?: NavigationMutation,
		{ recovered = false, retried = false, walker = false } = {},
	): Promise<boolean | undefined> {
		if (!reset && (this.state.loading || this.state.stale || !this.state.remaining)) return;
		const request = ++this.request,
			uncertain = this.uncertain,
			epoch = this.notificationEpoch;
		this.publish({
			loading: true,
			// A re-read that still owes depth keeps its error: recovery is not
			// done until the depth it owes is restored.
			error: this.owedDepth !== null && this.offset < this.owedDepth ? this.state.error : null,
		});
		this.clearRetry(false);
		try {
			const offset = reset ? 0 : this.offset,
				params = {
					...this.params,
					representationVersion: 3,
					offset,
					limit: this.limit,
				},
				key = navigationParamsToResourceKey(params),
				base = !recovered && reset && offset === 0 && this.normalized ? this.normalized.version : undefined;
			const wire = await this.client.request("evener/navigation/read", {
				...params,
				...(base ? { base } : {}),
			});
			if (request !== this.request) return;
			let decoded: DecodedNavigationResponse;
			try {
				decoded = decodeNavigationResponse(key, base, wire);
			} catch (cause) {
				if (!recovered && cause instanceof Error && cause.message.includes("installed base"))
					return this.load(true, receipt, { recovered: true, retried, walker });
				throw new Error("Could not read this navigation page. Try again.");
			}
			const receiptRevision = receipt ? requiredRevision(this.resourceKey, receipt.targets) : 0;
			if (reset && epoch === this.notificationEpoch && decoded.version.generationId !== this.notifiedGeneration) {
				this.notifiedGeneration = decoded.version.generationId;
				this.requiredRevision = 0;
				this.sequence = 0;
			}
			if (
				(receipt &&
					(decoded.version.generationId !== receipt.generation_id || decoded.version.revision < receiptRevision)) ||
				(this.notifiedGeneration !== "" && decoded.version.generationId !== this.notifiedGeneration) ||
				decoded.version.revision <
					Math.max(
						this.requiredRevision,
						decoded.version.generationId === this.mutationGeneration ? this.mutationFloor : 0,
					) ||
				uncertain !== this.uncertain
			) {
				this.publish({ loading: false, stale: true });
				// A notification that arrived during this read explains the
				// mismatch: read once more. Without one the hub is serving an
				// older revision than it announced; stay stale rather than spin.
				// A first-page read retries in place so the caller's promise sees
				// the result; a deeper page funnels into a re-read from the top.
				if (!reset) this.reread();
				else if (epoch !== this.notificationEpoch && !retried)
					return this.load(reset, receipt, { recovered, retried: true, walker });
				return false;
			}
			if (decoded.version.generationId !== this.mutationGeneration) {
				this.mutationGeneration = null;
				this.mutationFloor = 0;
			}
			if (decoded.status === "gone") {
				this.normalized = null;
				this.offset = 0;
				this.version = null;
				this.mutationFloor = 0;
				// The results have ended: the depth a recovery still owed
				// cannot exist, so the recovery completes here.
				this.owedDepth = null;
				this.recoveryDepth = false;
				this.rereads.settle();
				this.clearRetry(true);
				this.publish({
					loaded: true,
					rows: [],
					remaining: 0,
					loading: false,
					stale: false,
					error: null,
				});
				return true;
			}
			if (decoded.status === "not_modified") {
				// Every loaded page shares this version, so all of them stand.
				this.version = decoded.version;
				if (decoded.version.revision >= this.mutationFloor) this.mutationFloor = 0;
				const incomplete = this.owedDepth !== null && this.offset < this.owedDepth;
				// A first page that stands can still owe deeper pages: the
				// count and the waiters retire with the recovery, not with a
				// page that merely stands, and the error the recovery owes
				// holds with it — a cleared error would leave a cancelled
				// recovery nothing to re-arm on resume.
				if (this.owedDepth === null || this.offset >= this.owedDepth || this.state.remaining <= 0) {
					this.clearRetry(true);
					this.rereads.settle();
				} else {
					this.clearRetry(false);
					// A not-modified reset with depth still owed continues
					// the recovery on the flight, the same as a direct read
					// landing short. The walker flag — not the flight's
					// running state — tells an independent readback from the
					// walk's own reset: a readback superseding the walk's
					// in-flight page leaves the flight running, but its
					// landing still owes the depth a continuation.
					if (!walker) this.rereads.request();
				}
				this.publish({
					loaded: true,
					loading: false,
					stale: false,
					error: incomplete ? this.state.error : null,
				});
				return true;
			}
			const incoming =
				decoded.status === "snapshot"
					? reconcileSnapshot(reset && offset === 0 ? this.normalized : null, snapshotResource(key, decoded))
					: this.normalized
						? applyDelta(this.normalized, decoded.delta, decoded.version)
						: null;
			if (!incoming) throw new Error("Could not read this navigation page. Try again.");
			if (reset && offset === 0) this.normalized = incoming;
			const data = materializeNavigationResource(incoming) as Record<string, unknown>,
				raw = data[this.field];
			if (
				!Array.isArray(raw) ||
				!Number.isSafeInteger(data.remaining) ||
				Number(data.remaining) < 0 ||
				(!raw.length && Number(data.remaining) > 0)
			)
				throw new Error("The hub returned an invalid navigation page. Try again.");
			if (
				!reset &&
				(this.version?.generationId !== decoded.version.generationId ||
					this.version.revision !== decoded.version.revision)
			) {
				this.publish({ loading: false });
				this.reread();
				return;
			}
			const unique = new Map((reset ? [] : this.state.rows).map((row) => [this.key(row), row]));
			for (const row of raw) {
				const identity = this.key(row as T);
				if (!identity) throw new Error("The hub returned an invalid destination. Try again.");
				unique.set(identity, row as T);
			}
			this.offset = offset + raw.length;
			this.version = decoded.version;
			if (decoded.version.revision >= this.mutationFloor) this.mutationFloor = 0;
			// Recovery completes when the read reaches the depth it owes or
			// the results end — only then does the failure count retire, so a
			// later page that keeps failing grows the backoff instead of
			// resetting it on every intermediate page that lands.
			const owed = this.owedDepth;
			const exhausted = Number(data.remaining) === 0;
			if (owed === null || this.offset >= owed || exhausted) {
				this.owedDepth = null;
				this.recoveryDepth = false;
				this.clearRetry(true);
				// Waiters see the recovery complete, never the landing of an
				// intermediate page a walk still owes depth past.
				this.rereads.settle();
			} else {
				this.clearRetry(false);
				// An independent read that landed short of the owed depth (a
				// manual Retry loading more, a mutation readback) hands the
				// rest of the depth to the flight, whose re-read walks to it;
				// a refresh joining behind observes that walk's completion.
				// A read the walk itself drives queues nothing: it drives its
				// own next page, and a queued request left behind by a failed
				// walk would restart the recovery without waiting out the
				// backoff the failure armed. The running flag alone cannot
				// tell them apart — a mutation readback can supersede the
				// walk's in-flight read while the flight is still running —
				// so the walk marks its own reads.
				if (!walker) this.rereads.request();
			}
			// The error says the page still owes its recovery: it holds until
			// the depth is restored or the results end, whichever completes it.
			const incomplete = this.owedDepth !== null && this.offset < this.owedDepth;
			this.publish({
				loaded: true,
				rows: [...unique.values()],
				remaining: Number(data.remaining),
				loading: false,
				stale: false,
				error: incomplete ? this.state.error : null,
			});
			return true;
		} catch (cause) {
			if (request !== this.request) {
				return;
			}
			// A paging read that fails owed its next page: remember the depth
			// it was reaching for, so the retry's re-read restores the reader's
			// full depth instead of ending at whatever survived.
			if (!reset) this.owedDepth = Math.max(this.owedDepth ?? 0, this.offset + this.limit);
			this.publish({
				loading: false,
				error: cause instanceof Error ? cause.message : "Could not load this page. Try again.",
			});
			// Nothing else would retry a failed read while the page stays in
			// view: an idle hub sends no invalidation, so the page arms its own
			// re-read, on the same capped backoff the Board's reads retry on.
			this.armRetry();
		} finally {
			this.rereads.drain();
		}
	}
}
