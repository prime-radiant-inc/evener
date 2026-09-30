import { useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { AppState } from "react-native";
import type { ConversationService } from "../../../mobile/src/services/conversation";

import type { FindState } from "./findInSession";
import {
	attachHistory,
	cancelHistory,
	confirmHistoryBinding,
	retireHistory,
	detachHistory,
	createHistoryOwner,
	historyForSession,
	type HistoryStore,
} from "./historyMemory";

/** The screen's reader and Find share one page demand, preserved while it is
 * inactive. The store retains cursor identity, deduplication and loaded rows. */
export function useOlderHistory({
	store,
	service,
	active,
	hubId,
	ref,
	find = null,
	setFind,
	binding,
}: {
	store: HistoryStore;
	service: ConversationService | null;
	active: boolean;
	hubId: string;
	ref: string;
	find?: FindState | null;
	setFind?: (find: FindState | null) => void;
	binding: string | undefined;
}) {
	const [foreground, setForeground] = useState(AppState.currentState === "active");
	useEffect(() => {
		const subscription = AppState.addEventListener("change", (state) => setForeground(state === "active"));
		return () => subscription.remove();
	}, []);
	const confirmedBinding = useRef<string | undefined>(undefined);
	const [reader] = useState(() => Symbol());
	const [owner, setOwner] = useState(() => historyForSession(hubId, ref) ?? createHistoryOwner(hubId, ref, binding));
	const retired = owner.retired;
	const replacement =
		owner.hubId !== hubId ||
		owner.ref !== ref ||
		(binding !== undefined && owner.binding !== undefined && binding !== owner.binding);
	// Store and service routing belongs to the committed screen. A route pop
	// detaches both, while the same session can adopt its pending intent later.
	useLayoutEffect(() => {
		if (retired && !replacement) {
			setFind?.(null);
			return;
		}
		if (replacement || (owner.reader !== null && owner.reader !== reader)) {
			if (replacement && owner.hubId === hubId && owner.ref === ref) {
				retireHistory(owner);
			}
			const held = historyForSession(hubId, ref);
			const next =
				held !== owner && held && (binding === undefined || held.binding === undefined || held.binding === binding)
					? held
					: createHistoryOwner(hubId, ref, binding);
			confirmedBinding.current = undefined;
			setFind?.(next.find);
			setOwner(next);
			return;
		}
		if (binding !== undefined) {
			if (confirmedBinding.current !== binding) confirmHistoryBinding(owner, binding);
			confirmedBinding.current = binding;
			owner.binding = binding;
		}
		attachHistory(owner, store, service, reader);
	}, [owner, reader, retired, replacement, store, hubId, ref, binding, service, setFind]);
	// Suspense may hide layout effects without disposing the reader. Only
	// a committed store replacement or actual unmount detaches its routing.
	useEffect(() => () => detachHistory(owner, reader, store), [owner, reader, store]);
	useLayoutEffect(() => {
		if (!replacement && !retired && owner.reader === reader) owner.find = find;
	}, [owner, reader, retired, replacement, find]);
	const paging = owner.paging;
	const state = useSyncExternalStore(paging.subscribe, paging.getSnapshot);
	useEffect(
		() => (!replacement && !retired && owner.reader === reader && active && foreground ? paging.activate() : undefined),
		[owner, reader, retired, paging, replacement, active, foreground],
	);
	return {
		state,
		isCurrentReader: () => !replacement && !owner.retired && owner.reader === reader && owner.store === store,
		loadOlder: () => {
			if (replacement || owner.retired || owner.reader !== reader || owner.store !== store) return;
			void paging.request("reader").catch(() => {});
		},
		findOlder: () => {
			if (replacement || owner.retired || owner.reader !== reader || owner.store !== store) return;
			void paging.request("find").catch(() => {});
		},
		cancelFind: () => {
			if (owner.reader === reader) cancelHistory(owner, "find");
		},
		cancelReader: () => {
			if (owner.reader === reader) cancelHistory(owner, "reader");
		},
	};
}
