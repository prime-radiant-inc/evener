// Board search (spec 7.4): evener/search behind a debounce, the All and Live
// scopes, and each result's state mark. The queries this device searched for
// are Board memory, in boardMemory.ts. The Archived scope and "In sessions"
// hits wait for S14.
import type { NavigationProjectSummary, SearchResponse, SearchResult } from "@evener/appwire-client";
import { groupSearchResults, type GroupedSearchRow } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { reconnectDelay } from "../hubConnection";
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
	// The re-ask a failed search arms on its own, on the same capped backoff
	// the Board's failed reads retry on: nothing else would re-ask while the
	// query and client stand still. A new query, a client change or a landing
	// answer resets the count; the re-ask chain itself grows it.
	let reaskCount = 0;
	let reask: ReturnType<typeof setTimeout> | null = null;
	const clearReask = () => {
		if (reask !== null) clearTimeout(reask);
		reask = null;
	};
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
		clearReask();
		request++;
		reaskCount = 0;
	};
	const ask = () => {
		timer = null;
		clearReask();
		if (!client || !snapshot.query) return;
		const current = ++request;
		const query = snapshot.query;
		publish({ searching: true, failed: false });
		client.request("evener/search", { query }).then(
			(results) => {
				if (current === request) {
					publish({ results, searching: false });
					reaskCount = 0;
				}
			},
			() => {
				if (current !== request) return;
				publish({ searching: false, failed: true });
				// Ask again on the same backoff while the query and client stay
				// unchanged; a new query, client or dispose clears it.
				reaskCount += 1;
				reask = setTimeout(ask, reconnectDelay(reaskCount));
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

/** The Sessions group's rows, grouped through the shared client rule: live
 * rows first (a matched live parent nests its ended subagent hits directly
 * under its own row), then the past groups — each matching subagent nested
 * under its top-level session's row, which greys when only its subagents
 * matched. The hub's past index holds live sessions' records too, and an
 * older hub's past results don't leave them out, so a session that is live
 * shows once, as its live result. The Live scope keeps its meaning: live rows
 * only, so past children appear only in All. */
export function sessionResults(results: SearchResponse, scope: SearchScope): GroupedSearchRow[] {
	if (scope === "live") return groupSearchResults(results.live, []).live;
	const live = new Set(results.live.map((result) => result.ref));
	const grouped = groupSearchResults(
		results.live,
		results.past.filter((result) => !live.has(result.ref)),
	);
	return [...grouped.live, ...grouped.past];
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
 * carries no timestamp and no seen state, so it shows no unread dot: without
 * a mark that fits, it has none. */
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
