// Board search (spec 7.4): evener/search behind a debounce, the All and Live
// scopes, each result's state mark, and the queries this device searched
// for. The Archived scope and "In sessions" hits wait for S14.
import type { SearchResponse, SearchResult } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import type { SyncStringStorage } from "../syncStringStorage";
import type { BoardState } from "./attention";
import { readJson, recentSearchesKey, writeJson } from "./boardMemory";

export const SEARCH_DEBOUNCE = 250;
const RECENT_LIMIT = 8;

export type SearchScope = "all" | "live";

export interface SearchSnapshot {
	/** The trimmed query; empty when there's nothing to search for. */
	query: string;
	/** The hub's answer for this query, or null until it lands. */
	results: SearchResponse | null;
	/** True while this query's request is out. */
	searching: boolean;
	failed: boolean;
}

export interface SearchController {
	getSnapshot(): SearchSnapshot;
	subscribe(listener: () => void): () => void;
	/** Bind to the hub's ready client, or null while there is none. A new
	 * client asks for the current query at once. */
	setClient(client: ConversationClientLike | null): void;
	/** The field's text. A changed query drops the last one's results and
	 * asks the hub once the typing has rested for SEARCH_DEBOUNCE. */
	setQuery(text: string): void;
	dispose(): void;
}

export function createSearchController(): SearchController {
	let client: ConversationClientLike | null = null;
	let snapshot: SearchSnapshot = { query: "", results: null, searching: false, failed: false };
	let timer: ReturnType<typeof setTimeout> | null = null;
	// Bumped by every request and whatever abandons one, so only the newest
	// request's answer lands.
	let request = 0;
	const listeners = new Set<() => void>();

	const publish = (next: Partial<SearchSnapshot>) => {
		snapshot = { ...snapshot, ...next };
		for (const listener of [...listeners]) listener();
	};
	const stopWaiting = () => {
		if (timer !== null) clearTimeout(timer);
		timer = null;
	};
	const ask = () => {
		timer = null;
		if (!client || !snapshot.query) return;
		const current = ++request;
		const query = snapshot.query;
		publish({ searching: true, failed: false });
		client.request("evener/search", { query }).then(
			(results) => {
				if (current === request) publish({ results, searching: false });
			},
			() => {
				if (current === request) publish({ searching: false, failed: true });
			},
		);
	};

	return {
		getSnapshot: () => snapshot,
		subscribe(listener) {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		setClient(next) {
			if (next === client) return;
			client = next;
			stopWaiting();
			request++;
			if (snapshot.searching) publish({ searching: false });
			ask();
		},
		setQuery(text) {
			const query = text.trim();
			if (query === snapshot.query) return;
			stopWaiting();
			request++;
			publish({ query, results: null, searching: false, failed: false });
			if (query) timer = setTimeout(ask, SEARCH_DEBOUNCE);
		},
		dispose() {
			stopWaiting();
			request++;
			listeners.clear();
		},
	};
}

/** The Sessions group: live results, then past ones in All; live only in
 * Live. */
export function sessionResults(results: SearchResponse, scope: SearchScope): SearchResult[] {
	return scope === "live" ? results.live : [...results.live, ...results.past];
}

/** A result's mark, in boardState's precedence (attention.ts). A result
 * carries no timestamp and no seen state, so it is never Finished: without a
 * mark that fits, it has none. S4 brings the unseen flag. */
export function searchResultMark(result: SearchResult): BoardState {
	switch (result.state) {
		case "errored":
			return "failed";
		case "restartRequired":
			return "restartNeeded";
		case "warning":
			return "warning";
		case "ended":
		case "notLoaded":
			return "shutDown";
	}
	if (result.askPending) return "question";
	if (result.approvalPending) return "approval";
	if (result.state === "active") return "working";
	return "idle";
}

/** The last queries you searched and opened a result from, most recent
 * first, per device and hub. */
export class RecentSearches {
	private queries: string[];

	constructor(
		private readonly storage: SyncStringStorage,
		private readonly hubId: string,
	) {
		const value = readJson(storage, recentSearchesKey(hubId));
		this.queries = Array.isArray(value)
			? value.filter((query): query is string => typeof query === "string" && query !== "").slice(0, RECENT_LIMIT)
			: [];
	}

	list(): string[] {
		return this.queries;
	}

	add(text: string): void {
		const query = text.trim();
		if (!query) return;
		this.queries = [query, ...this.queries.filter((other) => other !== query)].slice(0, RECENT_LIMIT);
		writeJson(this.storage, recentSearchesKey(this.hubId), this.queries);
	}

	clear(): void {
		this.queries = [];
		writeJson(this.storage, recentSearchesKey(this.hubId), this.queries);
	}
}
