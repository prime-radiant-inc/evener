// Board search (spec 7.4): evener/search behind a debounce, the All and Live
// scopes, and each result's state mark. The queries this device searched for
// are Board memory, in boardMemory.ts. The Archived scope and "In sessions"
// hits wait for S14.
import type { NavigationProjectSummary, SearchResponse, SearchResult } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { type BoardState, decisiveState } from "./attention";

export const SEARCH_DEBOUNCE = 250;

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
	/** Drops the query waiting out its debounce and the request out, whose
	 * answer then never lands. */
	const abandon = () => {
		if (timer !== null) clearTimeout(timer);
		timer = null;
		request++;
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
			abandon();
			// A failure belonged to the old client; with none, the screen says
			// search waits for the hub instead.
			if (snapshot.searching || snapshot.failed) publish({ searching: false, failed: false });
			ask();
		},
		setQuery(text) {
			const query = text.trim();
			if (query === snapshot.query) return;
			abandon();
			publish({ query, results: null, searching: false, failed: false });
			if (query) timer = setTimeout(ask, SEARCH_DEBOUNCE);
		},
		dispose() {
			abandon();
			listeners.clear();
		},
	};
}

/** The Sessions group: live results, then past ones in All; live only in
 * Live. The hub's past index holds live sessions' records too, and its past
 * results don't leave them out, so a session that is live shows once, as its
 * live result. */
export function sessionResults(results: SearchResponse, scope: SearchScope): SearchResult[] {
	if (scope === "live") return results.live;
	const live = new Set(results.live.map((result) => result.ref));
	return [...results.live, ...results.past.filter((result) => !live.has(result.ref))];
}

/** The Projects group: the projects the Board has loaded whose name or
 * working directory holds the query, ignoring case, in the catalog's order.
 * The hub's search has no project hits, so this reads the Board's own
 * catalog. */
export function projectResults(
	projects: readonly NavigationProjectSummary[],
	query: string,
): NavigationProjectSummary[] {
	const needle = query.trim().toLowerCase();
	if (!needle) return [];
	return projects.filter(
		(project) =>
			project.name.toLowerCase().includes(needle) || (project.working_dir ?? "").toLowerCase().includes(needle),
	);
}

/** A result's mark, in boardState's precedence (attention.ts). A result
 * carries no timestamp and no seen state, so it is never Finished: without a
 * mark that fits, it has none. S4 brings the unseen flag. */
export function searchResultMark(result: SearchResult): BoardState {
	const decisive = decisiveState(result.state);
	if (decisive) return decisive;
	if (result.askPending) return "question";
	if (result.approvalPending) return "approval";
	// Awaiting without a question: the turn ended on needs_response (#4093).
	if (result.state === "awaiting") return "needsYou";
	if (result.state === "active") return "working";
	return "idle";
}
