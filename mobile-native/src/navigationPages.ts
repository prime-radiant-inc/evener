import type {
	NavigationInvalidatedPayload,
	NavigationInvalidationTarget,
	NavigationMutation,
	NavigationReadParams,
} from "../../appwire-client/typescript/types.gen";
import {
	type DecodedNavigationResponse,
	decodeNavigationResponse,
	materializeNavigationResource,
	type NormalizedResource,
	normalizedGraphFromSnapshot,
} from "../../cmd/evener-hub/frontend/src/stores/navigation/codec";
import {
	applyDelta,
	reconcileSnapshot,
} from "../../cmd/evener-hub/frontend/src/stores/navigation/merge";
import type { ResourceKey } from "../../cmd/evener-hub/frontend/src/stores/navigation/types";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

interface PageState<T> {
	loaded: boolean;
	truncated: boolean;
	rows: T[];
	remaining: number;
	loading: boolean;
	error: string | null;
	stale: boolean;
}
type NativeNavigationParams = Omit<
	NavigationReadParams,
	"representationVersion"
>;
export function resourceKeyFor(p: NavigationReadParams): ResourceKey {
	const offset = p.offset ?? 0,
		limit = p.limit ?? 50;
	switch (p.resource) {
		case "catalog":
			return {
				kind: "catalog",
				catalog: p.catalog as "projects" | "archived_projects" | "test_runs",
				offset,
				limit,
			};
		case "section":
			return {
				kind: "section",
				section: p.section as "live" | "needs_you",
				offset,
				limit,
			};
		case "pin_section":
			return {
				kind: "pin_section",
				sectionId: p.sectionId as string,
				offset,
				limit,
			};
		case "project_page":
			return {
				kind: "project_page",
				projectKey: p.projectKey as string,
				tier: p.tier as "current" | "recent" | "archived",
				offset,
				limit,
			};
		case "pin_catalog":
			return { kind: "pin_catalog", offset, limit };
		case "project":
			return { kind: "project", projectKey: p.projectKey as string };
		case "location":
			return { kind: "location", ref: p.ref as string };
		default:
			return { kind: "manifest" };
	}
}
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
	private version: string | null = null;
	private notifiedGeneration = "";
	private requiredRevision = 0;
	private sequence = 0;
	private uncertain = 0;
	private notificationEpoch = 0;
	private mutationFloor = 0;
	private mutationGeneration: string | null = null;
	private normalized: NormalizedResource | null = null;
	private firstPageRowCount = 0;
	private firstPageRemaining = 0;
	constructor(
		private client: ConversationClientLike,
		private params: NativeNavigationParams,
		private field: "projects" | "sessions" | "pin_sections",
		private key: (row: T) => string,
		private limit = 50,
	) {}
	watch() {
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
	private markStale() {
		this.publish({
			stale: true,
			error: "This list changed while you were browsing. Refresh to continue.",
		});
	}
	private invalidate(p: NavigationInvalidatedPayload) {
		if (this.notifiedGeneration !== p.generationId) {
			this.notifiedGeneration = p.generationId;
			this.sequence = 0;
			this.requiredRevision = 0;
		}
		if (p.sequence <= this.sequence) return;
		const gap = p.sequence > this.sequence + 1;
		this.sequence = p.sequence;
		this.notificationEpoch++;
		const targets = p.targets.filter((target) => this.matchesTarget(target));
		if (gap || targets.some((t) => t.revision === undefined)) this.uncertain++;
		for (const t of targets)
			this.requiredRevision = Math.max(this.requiredRevision, t.revision ?? 0);
		if (targets.length > 0 || gap) this.markStale();
	}
	private matchesTarget(t: NavigationInvalidationTarget) {
		const p = this.params;
		return (
			(p.resource === "section" &&
				t.kind === "section" &&
				t.section === p.section) ||
			(p.resource === "pin_catalog" && t.kind === "pin_catalog") ||
			(p.resource === "pin_section" &&
				t.kind === "pin_section" &&
				t.sectionId === p.sectionId) ||
			(p.resource === "catalog" &&
				t.kind === "catalog" &&
				t.catalog === p.catalog) ||
			(p.resource === "project_page" &&
				(t.kind === "all_loaded_projects" ||
					(t.kind === "project" && t.projectKey === p.projectKey)))
		);
	}
	private receiptRevision(receipt: NavigationMutation) {
		return Math.max(
			0,
			...receipt.targets
				.filter((target) => this.matchesTarget(target))
				.map((target) => target.revision ?? 0),
		);
	}

	cancel() {
		this.request++;
		this.publish({ loading: false });
	}
	refresh() {
		return this.load(true);
	}
	more() {
		return this.load(false);
	}
	async refreshAfter(receipt: NavigationMutation) {
		if (this.mutationGeneration !== receipt.generation_id)
			this.mutationFloor = 0;
		this.mutationGeneration = receipt.generation_id;
		this.mutationFloor = Math.max(
			this.mutationFloor,
			this.receiptRevision(receipt),
		);
		const epoch = this.notificationEpoch;
		let ok = await this.load(true, receipt);
		if (ok === false && epoch !== this.notificationEpoch)
			ok = await this.load(true, receipt);
		if (!ok)
			throw new Error(
				"The change was accepted, but the updated list could not be loaded. Refresh the list.",
			);
	}
	private async load(
		reset: boolean,
		receipt?: NavigationMutation,
		recovered = false,
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
				key = resourceKeyFor(params),
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
					return this.load(true, receipt, true);
				throw new Error(
					"Could not read this navigation page. Refresh to try again.",
				);
			}
			const receiptRevision = receipt ? this.receiptRevision(receipt) : 0;
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
				this.publish({ loading: false });
				this.markStale();
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
				if (reset) this.offset = this.firstPageRowCount;
				this.version = JSON.stringify([
					decoded.version.generationId,
					decoded.version.revision,
				]);
				if (decoded.version.revision >= this.mutationFloor)
					this.mutationFloor = 0;
				this.publish({
					loaded: true,
					rows: reset
						? this.state.rows.slice(0, this.firstPageRowCount)
						: this.state.rows,
					remaining: reset ? this.firstPageRemaining : this.state.remaining,
					loading: false,
					stale: false,
					error: null,
				});
				return true;
			}
			const incoming =
				decoded.status === "snapshot"
					? reconcileSnapshot(reset && offset === 0 ? this.normalized : null, {
							key,
							graph: normalizedGraphFromSnapshot(decoded.snapshot),
							version: decoded.version,
							presence: "present",
						})
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
			const stringVersion = JSON.stringify([
				decoded.version.generationId,
				decoded.version.revision,
			]);
			if (!reset && this.version !== stringVersion) {
				this.publish({ loading: false });
				this.markStale();
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
			this.version = stringVersion;
			if (decoded.version.revision >= this.mutationFloor)
				this.mutationFloor = 0;
			if (reset) {
				this.firstPageRowCount = raw.length;
				this.firstPageRemaining = Number(data.remaining);
			}
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
		}
	}
}
