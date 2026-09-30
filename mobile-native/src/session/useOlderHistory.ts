import { HistoryPaging } from "@evener/appwire-client";
import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { AppState } from "react-native";
import type { ConversationService } from "../../../mobile/src/services/conversation";
import { type ConversationState, olderPageKey } from "../../../mobile/src/state/conversation";

/** The screen's reader and Find share one page demand, preserved while it is
 * inactive. The store retains cursor identity, deduplication and loaded rows. */
export function useOlderHistory({
	store,
	service,
	active,
	resetKey,
}: {
	store: { getState(): ConversationState };
	service: ConversationService | null;
	active: boolean;
	resetKey: string;
}) {
	const [foreground, setForeground] = useState(AppState.currentState === "active");
	useEffect(() => {
		const subscription = AppState.addEventListener("change", (state) => setForeground(state === "active"));
		return () => subscription.remove();
	}, []);
	const serviceNow = useRef(service);
	serviceNow.current = service;
	// A different binding/session owns different demand; its late page cannot
	// complete or block the new owner.
	// biome-ignore lint/correctness/useExhaustiveDependencies: resetKey identifies the conversation binding.
	const paging = useMemo(
		() =>
			new HistoryPaging(
				() => {
					const current = store.getState();
					return current.conversation === null ? undefined : olderPageKey(current);
				},
				async () => {
					const live = serviceNow.current;
					if (!live) throw new Error("Waiting for the hub connection.");
					const result = await store.getState().loadOlder(live);
					if (result.status === "failed") throw result.error;
				},
			),
		[store, resetKey],
	);
	const state = useSyncExternalStore(paging.subscribe, paging.getSnapshot);
	useEffect(() => (active && foreground ? paging.activate() : undefined), [paging, active, foreground]);
	return {
		state,
		loadOlder: () => {
			void paging.request().catch(() => {});
		},
	};
}
