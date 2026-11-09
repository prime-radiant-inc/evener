import type { ModelCatalog } from "@evener/appwire-client";
import type { ConversationClientLike } from "./conversation/services/conversation";
import { reconnectDelay } from "./hubConnection";

interface State {
	catalog: ModelCatalog | null;
	loading: boolean;
	error: string | null;
}
/** The hub's launch catalog, independent of a running session. */
export class HubModels {
	private state: State = { catalog: null, loading: false, error: null };
	private version = 0;
	private disposed = false;
	/** The failed read's own retry, so a picker that stays open sees the list
	 * come back without touching anything (the same capped backoff the rest of
	 * the app's reads use). Null once a read lands. */
	private rereadTimer: ReturnType<typeof setTimeout> | null = null;
	private rereadFailures = 0;
	private listeners = new Set<() => void>();
	constructor(private client: ConversationClientLike | null) {}
	getSnapshot = () => this.state;
	subscribe = (listener: () => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};
	private publish(change: Partial<State>) {
		if (this.disposed) return;
		this.state = { ...this.state, ...change };
		for (const listener of this.listeners) listener();
	}
	refresh = async () => {
		if (this.disposed || !this.client) return;
		const version = ++this.version;
		this.publish({ loading: true, error: null });
		try {
			const result = await this.client.request("model/list", {});
			if (this.disposed || version !== this.version) return;
			const entries = (data: typeof result.data | undefined) =>
				(data ?? []).map((entry) => ({
					...entry,
					displayName: entry.displayName || entry.model,
				}));
			this.publish({
				catalog: {
					models: entries(result.data),
					recent: entries(result.recent),
					diagnostics: result.diagnostics ?? [],
				},
			});
			this.clearReread();
		} catch {
			if (version === this.version)
				this.publish({
					error: "Could not load this hub's models.",
				});
			if (version === this.version && !this.disposed) this.armReread();
		} finally {
			if (version === this.version) this.publish({ loading: false });
		}
	};
	private armReread() {
		this.rereadFailures += 1;
		if (this.rereadTimer !== null) clearTimeout(this.rereadTimer);
		this.rereadTimer = setTimeout(() => {
			this.rereadTimer = null;
			void this.refresh();
		}, reconnectDelay(this.rereadFailures));
	}
	private clearReread() {
		if (this.rereadTimer !== null) {
			clearTimeout(this.rereadTimer);
			this.rereadTimer = null;
		}
		this.rereadFailures = 0;
	}
	dispose() {
		this.disposed = true;
		this.version += 1;
		this.clearReread();
		this.listeners.clear();
	}
}
