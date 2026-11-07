import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import type { TextInput } from "react-native";
import type { ConversationClientLike } from "../conversation/services/conversation";
import { createSearchController, type SearchScope } from "./boardSearch";
import { recentSearches } from "./nativeBoardMemory";

/** The Board's search controller, bound to the ready client or none. */
function useSearch(client: ConversationClientLike | null) {
	const [controller] = useState(createSearchController);
	useEffect(() => () => controller.dispose(), [controller]);
	useEffect(() => controller.setClient(client), [controller, client]);
	const snapshot = useSyncExternalStore(controller.subscribe, controller.getSnapshot);
	return { controller, snapshot };
}

/** The Board's search state: the query and field, the scope, the hub's recent
 * searches and the controller bound to `client`. */
export function useBoardSearch(hubId: string, client: ConversationClientLike | null) {
	const search = useSearch(client);
	const searchInput = useRef<TextInput>(null);
	const [searchText, setSearchText] = useState("");
	// Searching from the moment the field takes focus until Cancel.
	const [searching, setSearching] = useState(false);
	const [scope, setScope] = useState<SearchScope>("all");
	const recent = recentSearches(hubId);
	const [recentList, setRecentList] = useState(() => recent.list());
	const typeSearch = (text: string) => {
		setSearchText(text);
		search.controller.setQuery(text);
	};
	const leaveSearch = () => {
		typeSearch("");
		setSearching(false);
		searchInput.current?.blur?.();
	};
	const clearRecent = () => {
		recent.clear();
		setRecentList(recent.list());
	};
	const rememberSearch = () => {
		recent.add(search.snapshot.query);
		setRecentList(recent.list());
	};
	return {
		search,
		searchInput,
		searchText,
		searching,
		setSearching,
		scope,
		setScope,
		recentList,
		typeSearch,
		leaveSearch,
		clearRecent,
		rememberSearch,
	};
}
