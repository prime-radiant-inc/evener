import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import type {
	RosterEntry,
	RosterService,
} from "../../mobile/src/services/roster";

interface SearchState {
	query: string;
	rows: RosterEntry[];
	hasMore: boolean;
	loading: boolean;
	error: string | null;
}

/** Own one hub's roster requests; newer searches supersede older results. */
export class RosterSearch {
	private state: SearchState = {
		query: "",
		rows: [],
		hasMore: false,
		loading: false,
		error: null,
	};
	private generation = 0;
	private listeners = new Set<() => void>();
	constructor(private service: RosterService | null) {}
	getSnapshot = () => this.state;
	subscribe = (listener: () => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};
	private publish(state: Partial<SearchState>) {
		this.state = { ...this.state, ...state };
		for (const listener of this.listeners) listener();
	}
	cancel() {
		this.generation += 1;
		this.publish({ loading: false });
	}
	watch(client: ConversationClientLike) {
		let active = true,
			dirty = false,
			refreshing = false;
		const refresh = () => {
			if (!active || !dirty || refreshing || this.state.loading) return;
			dirty = false;
			refreshing = true;
			void this.load().finally(() => {
				refreshing = false;
				refresh();
			});
		};
		// Initial loads and explicit searches may already be in flight when a
		// notification arrives. Drain the pending refresh when their state settles.
		const unobserve = this.subscribe(refresh);
		const unwatch = client.onNotification((notification) => {
			if (notification.method !== "evener/navigation/invalidated" || !active)
				return;
			dirty = true;
			refresh();
		});
		return () => {
			active = false;
			dirty = false;
			unobserve();
			unwatch();
			this.cancel();
		};
	}
	async load(query = this.state.query) {
		if (!this.service) return;
		const generation = ++this.generation;
		const normalized = query.trim();
		this.publish({
			query: normalized,
			loading: true,
			error: null,
			...(normalized !== this.state.query ? { rows: [], hasMore: false } : {}),
		});
		try {
			const result = await this.service.list(normalized);
			if (generation !== this.generation) return;
			this.publish({
				rows: result.threads,
				hasMore: result.hasMore,
				loading: false,
			});
		} catch {
			if (generation !== this.generation) return;
			this.publish({
				loading: false,
				error: "Could not load sessions. Try again.",
			});
		}
	}
}
