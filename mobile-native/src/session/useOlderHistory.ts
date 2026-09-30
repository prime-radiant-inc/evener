import { HistoryPaging } from "@evener/appwire-client";
import { useEffect, useLayoutEffect, useState, useSyncExternalStore } from "react";
import { AppState } from "react-native";
import type { ConversationService } from "../../../mobile/src/services/conversation";
import { type ConversationState, olderPageKey } from "../../../mobile/src/state/conversation";

type HistoryStore = { getState(): ConversationState };
interface OlderHistoryOwner {
	store: HistoryStore;
	resetKey: string;
	binding: string | undefined;
	service: ConversationService | null;
	paging: HistoryPaging;
}

function createOwner(store: HistoryStore, resetKey: string, binding: string | undefined): OlderHistoryOwner {
	const owner: OlderHistoryOwner = {
		store,
		resetKey,
		binding,
		service: null,
		paging: new HistoryPaging(
			() => {
				const current = store.getState();
				if (current.conversation === null || current.conversation.instanceId !== owner.binding) return undefined;
				return olderPageKey(current);
			},
			async () => {
				if (!owner.service) throw new Error("Waiting for the hub connection.");
				const result = await store.getState().loadOlder(owner.service);
				if (result.status === "failed") throw result.error;
			},
		),
	};
	return owner;
}

/** The screen's reader and Find share one page demand, preserved while it is
 * inactive. The store retains cursor identity, deduplication and loaded rows. */
export function useOlderHistory({
	store,
	service,
	active,
	resetKey,
	binding,
}: {
	store: HistoryStore;
	service: ConversationService | null;
	active: boolean;
	resetKey: string;
	binding: string | undefined;
}) {
	const [foreground, setForeground] = useState(AppState.currentState === "active");
	useEffect(() => {
		const subscription = AppState.addEventListener("change", (state) => setForeground(state === "active"));
		return () => subscription.remove();
	}, []);
	const [owner, setOwner] = useState(() => createOwner(store, resetKey, binding));
	const replacement =
		owner.store !== store ||
		owner.resetKey !== resetKey ||
		(binding !== undefined && owner.binding !== undefined && binding !== owner.binding);
	// First hydration and a temporary missing model retain demand. Only a
	// committed different binding retires it; abandoned renders change nothing.
	useLayoutEffect(() => {
		if (replacement) {
			const next = createOwner(store, resetKey, binding);
			next.service = service;
			setOwner(next);
		} else {
			if (binding !== undefined) owner.binding = binding;
			owner.service = service;
		}
	}, [owner, replacement, store, resetKey, binding, service]);
	const paging = owner.paging;
	const state = useSyncExternalStore(paging.subscribe, paging.getSnapshot);
	useEffect(
		() => (!replacement && active && foreground ? paging.activate() : undefined),
		[paging, replacement, active, foreground],
	);
	return {
		state,
		loadOlder: () => {
			void paging.request("reader").catch(() => {});
		},
		findOlder: () => {
			void paging.request("find").catch(() => {});
		},
		cancelFind: () => paging.cancel("find"),
		cancelReader: () => paging.cancel("reader"),
	};
}
