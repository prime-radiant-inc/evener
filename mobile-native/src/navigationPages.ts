import type {
	NavigationInvalidatedPayload,
	NavigationMutation,
	NavigationReadParams,
} from "@evener/appwire-client";
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
import { singleFlight } from "./singleFlight";

interface PageState<T> {
	loaded: boolean;
	truncated: boolean;
	rows: T[];
	remaining: number;
	loading: boolean;
	error: string | null;
	stale: boolean;
}
export type PageStatus = "loading" | "error" | "stale" | "more";
/** What a list should say about a page beyond its rows. An error keeps its
 * retry even while the page is stale: a failed automatic re-read leaves both
 * set, and "Updating…" must never hide the way out. Views that show an
 * action's error beside the page pass that merged error in. */
export function pageStatus(
	page: Pick<PageState<unknown>, "loading" | "error" | "stale" | "remaining">,
): PageStatus | null {
	if (page.loading) return "loading";
	if (page.error) return "error";
	if (page.stale) return "stale";
	if (page.remaining > 0) return "more";
	return null;
}
/** Whether a list should say "Updating…": its rows are known to be outdated
 * and no error is on screen. Unlike the boundary row, this holds while the
 * re-read is in flight, since stale stays set until that read lands. */
export function updating(page: Parameters<typeof pageStatus>[0]) {
	return page.stale && !page.error;
}
type NativeNavigationParams = Omit<
	NavigationReadParams,
	"representationVersion"
>;
export class NavigationPages<T> {
	private state: PageState<T> = {
		loaded: false,
		truncated: false,
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
		() => this.rereadLoadedDepth(),
		() => !this.paused && !this.state.loading,
	);
	constructor(
		private client: ConversationClientLike,
		private params: NativeNavigationParams,
		private field: "projects" | "sessions" | "pin_sections",
		private key: (row: T) => string,
		private limit = 50,
	) {
		this.resourceKey = navigationParamsToResourceKey({
			...params,
			representationVersion: 2,
		});
	}
	/** Follow hub invalidations. Newer data is re-read here without user
	 * action unless an owner takes every re-read to run a wider read of its
	 * own (the pin screens also re-read a session's location). */
	watch(owner?: () => void) {
		this.owner = owner ?? null;
		return this.client.onNotification((event) => {
			if (event.method === "evener/navigation/invalidated")
				this.invalidate(event.params);
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
		const depth = this.offset;
		if ((await this.load(true)) !== true) return;
		while (this.offset < depth && this.state.remaining > 0)
			if ((await this.load(false)) !== true) return;
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
		this.requiredRevision = Math.max(
			this.requiredRevision,
			requiredRevision(this.resourceKey, p.targets),
		);
		const loaded = this.version;
		const held =
			!gap &&
			loaded?.generationId === p.generationId &&
			targets.every(
				(t) => t.revision !== undefined && t.revision <= loaded.revision,
			);
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
	 * resume(); a re-read the hub still owes waits for it. */
	cancel() {
		this.request++;
		this.paused = true;
		this.publish({ loading: false });
		if (this.state.stale) this.scheduleReread();
	}
	resume() {
		this.paused = false;
		if (this.state.stale) this.scheduleReread();
		this.rereads.drain();
	}
	/** An explicit read means the owner is active again; its result stands
	 * in for any re-read owed while paused. */
	refresh() {
		this.paused = false;
		return this.load(true);
	}
	more() {
		// Stale rows cannot be paged past; catch up from the first page instead.
		if (this.state.stale && !this.state.loading) return this.load(true);
		return this.load(false);
	}
	async refreshAfter(receipt: NavigationMutation) {
		if (this.mutationGeneration !== receipt.generation_id)
			this.mutationFloor = 0;
		this.mutationGeneration = receipt.generation_id;
		this.mutationFloor = Math.max(
			this.mutationFloor,
			requiredRevision(this.resourceKey, receipt.targets),
		);
		if (!(await this.load(true, receipt)))
			throw new Error(
				"The change was accepted, but the updated list could not be loaded. Refresh the list.",
			);
	}
	private async load(
		reset: boolean,
		receipt?: NavigationMutation,
		{ recovered = false, retried = false } = {},
	): Promise<boolean | undefined> {
		if (
			!reset &&
			(this.state.loading || this.state.stale || !this.state.remaining)
		)
			return;
		const request = ++this.request,
			uncertain = this.uncertain,
			epoch = this.notificationEpoch;
		this.publish({ loading: true, error: null });
		try {
			const offset = reset ? 0 : this.offset,
				params = {
					...this.params,
					representationVersion: 2,
					offset,
					limit: this.limit,
				},
				key = navigationParamsToResourceKey(params),
				base =
					!recovered && reset && offset === 0 && this.normalized
						? this.normalized.version
						: undefined;
			const wire = await this.client.request("evener/navigation/read", {
				...params,
				...(base ? { base } : {}),
			});
			if (request !== this.request) return;
			let decoded: DecodedNavigationResponse;
			try {
				decoded = decodeNavigationResponse(key, base, wire);
			} catch (cause) {
				if (
					!recovered &&
					cause instanceof Error &&
					cause.message.includes("installed base")
				)
					return this.load(true, receipt, { recovered: true, retried });
				throw new Error(
					"Could not read this navigation page. Refresh to try again.",
				);
			}
			const receiptRevision = receipt
				? requiredRevision(this.resourceKey, receipt.targets)
				: 0;
			if (
				reset &&
				epoch === this.notificationEpoch &&
				decoded.version.generationId !== this.notifiedGeneration
			) {
				this.notifiedGeneration = decoded.version.generationId;
				this.requiredRevision = 0;
				this.sequence = 0;
			}
			if (
				(receipt &&
					(decoded.version.generationId !== receipt.generation_id ||
						decoded.version.revision < receiptRevision)) ||
				(this.notifiedGeneration !== "" &&
					decoded.version.generationId !== this.notifiedGeneration) ||
				decoded.version.revision <
					Math.max(
						this.requiredRevision,
						decoded.version.generationId === this.mutationGeneration
							? this.mutationFloor
							: 0,
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
					return this.load(reset, receipt, { recovered, retried: true });
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
				this.rereads.settle();
				this.publish({
					loaded: true,
					rows: [],
					remaining: 0,
					truncated: false,
					loading: false,
					stale: false,
					error: null,
				});
				return true;
			}
			if (decoded.status === "not_modified") {
				// Every loaded page shares this version, so all of them stand.
				this.version = decoded.version;
				if (decoded.version.revision >= this.mutationFloor)
					this.mutationFloor = 0;
				this.rereads.settle();
				this.publish({
					loaded: true,
					loading: false,
					stale: false,
					error: null,
				});
				return true;
			}
			const incoming =
				decoded.status === "snapshot"
					? reconcileSnapshot(
							reset && offset === 0 ? this.normalized : null,
							snapshotResource(key, decoded),
						)
					: this.normalized
						? applyDelta(this.normalized, decoded.delta, decoded.version)
						: null;
			if (!incoming)
				throw new Error(
					"Could not read this navigation page. Refresh to try again.",
				);
			if (reset && offset === 0) this.normalized = incoming;
			const data = materializeNavigationResource(incoming) as Record<
					string,
					unknown
				>,
				raw = data[this.field];
			if (
				!Array.isArray(raw) ||
				!Number.isSafeInteger(data.remaining) ||
				Number(data.remaining) < 0 ||
				(!raw.length && Number(data.remaining) > 0)
			)
				throw new Error(
					"The hub returned an invalid navigation page. Refresh to try again.",
				);
			if (
				!reset &&
				(this.version?.generationId !== decoded.version.generationId ||
					this.version.revision !== decoded.version.revision)
			) {
				this.publish({ loading: false });
				this.reread();
				return;
			}
			const unique = new Map(
				(reset ? [] : this.state.rows).map((row) => [this.key(row), row]),
			);
			for (const row of raw) {
				const identity = this.key(row as T);
				if (!identity)
					throw new Error(
						"The hub returned an invalid destination. Refresh to try again.",
					);
				unique.set(identity, row as T);
			}
			this.offset = offset + raw.length;
			this.version = decoded.version;
			if (decoded.version.revision >= this.mutationFloor)
				this.mutationFloor = 0;
			this.rereads.settle();
			this.publish({
				loaded: true,
				truncated: data.truncated === true || (!reset && this.state.truncated),
				rows: [...unique.values()],
				remaining: Number(data.remaining),
				loading: false,
				stale: false,
				error: null,
			});
			return true;
		} catch (cause) {
			if (request !== this.request) return;
			this.publish({
				loading: false,
				error:
					cause instanceof Error
						? cause.message
						: "Could not load this page. Try again.",
			});
		} finally {
			this.rereads.drain();
		}
	}
}
