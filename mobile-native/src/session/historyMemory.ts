import { HistoryPaging } from "@evener/appwire-client";
import type { ConversationService } from "../../../mobile/src/services/conversation";
import { type ConversationState, olderPageKey } from "../../../mobile/src/state/conversation";
import { perHub } from "../board/perHub";
import type { FindState } from "./findInSession";

export type HistoryStore = { getState(): ConversationState };
export interface OlderHistoryOwner {
	hubId: string;
	ref: string;
	binding: string | undefined;
	store: HistoryStore | null;
	service: ConversationService | null;
	find: FindState | null;
	reader: symbol | null;
	retired: boolean;
	requestedPage: string | null;
	paging: HistoryPaging;
}

// Like the other per-hub memories, this retains intent for this app lifetime.
// Closed screens leave no store, service or running reader in this memory.
const histories = perHub(() => new Map<string, Set<OlderHistoryOwner>>());
export function historyForSession(hubId: string, ref: string): OlderHistoryOwner | undefined {
	return [...(histories.peek(hubId)?.get(ref) ?? [])].findLast((owner) => owner.reader === null);
}

function releaseHistory(owner: OlderHistoryOwner): void {
	const state = owner.paging.getSnapshot();
	if (!owner.retired && (owner.reader !== null || state.pending || state.loading || owner.find?.seeking)) return;
	const sessions = histories.peek(owner.hubId);
	const owners = sessions?.get(owner.ref);
	owners?.delete(owner);
	if (owners?.size === 0) sessions?.delete(owner.ref);
	if (sessions?.size === 0) histories.forget(owner.hubId);
}

export function createHistoryOwner(hubId: string, ref: string, binding: string | undefined): OlderHistoryOwner {
	const owner: OlderHistoryOwner = {
		hubId,
		ref,
		binding,
		store: null,
		service: null,
		find: null,
		reader: null,
		retired: false,
		requestedPage: null,
		paging: new HistoryPaging(
			() => {
				const current = owner.store?.getState();
				if (!current?.conversation || current.conversation.instanceId !== owner.binding) return undefined;
				const page = olderPageKey(current);
				if (page === null) owner.requestedPage = null;
				return page === null ? null : (owner.requestedPage ?? page);
			},
			async () => {
				const { store, service } = owner;
				if (!store || !service) return;
				const page = olderPageKey(store.getState());
				owner.requestedPage ??= page;
				const result = await store.getState().loadOlder(service);
				// An old screen's completion cannot advance a freshly hydrated
				// reader. Rebuild earlier pages until its requested boundary is read.
				if (owner.store === store && result.status === "loaded") {
					const nextPage = olderPageKey(store.getState());
					if (nextPage === null || (page === owner.requestedPage && nextPage !== page)) owner.requestedPage = null;
				}
				if (result.status === "failed") throw result.error;
			},
			() => releaseHistory(owner),
		),
	};
	return owner;
}

/** Adoption happens only after a screen commits, so an abandoned render cannot
 * retarget a retained reader or erase its previous binding. */
export function attachHistory(
	owner: OlderHistoryOwner,
	store: HistoryStore,
	service: ConversationService | null,
	reader: symbol,
): boolean {
	if (owner.retired || (owner.reader !== null && owner.reader !== reader)) return false;
	owner.reader = reader;
	owner.store = store;
	owner.service = service;
	const sessions = histories.get(owner.hubId);
	let owners = sessions.get(owner.ref);
	if (!owners) {
		owners = new Set();
		sessions.set(owner.ref, owners);
	}
	owners.add(owner);
	return true;
}

export function detachHistory(owner: OlderHistoryOwner, reader: symbol, store: HistoryStore): void {
	if (owner.reader !== reader || owner.store !== store) return;
	owner.reader = null;
	owner.store = null;
	owner.service = null;
	releaseHistory(owner);
}

export function cancelHistory(owner: OlderHistoryOwner, consumer: "reader" | "find"): void {
	owner.paging.cancel(consumer);
	if (!owner.paging.getSnapshot().pending) owner.requestedPage = null;
}

/** A confirmed new binding makes every prior binding's intent obsolete.
 * Mounted readers cannot recreate it before their own store converges. */
export function confirmHistoryBinding(owner: OlderHistoryOwner, binding: string): void {
	for (const other of histories.peek(owner.hubId)?.get(owner.ref) ?? []) {
		if (other !== owner && other.binding !== undefined && other.binding !== binding) retireHistory(other);
	}
}

export function retireHistory(owner: OlderHistoryOwner): void {
	owner.retired = true;
	owner.find = null;
	owner.requestedPage = null;
	owner.paging.cancel("reader");
	owner.paging.cancel("find");
	releaseHistory(owner);
}

export function forgetHistoryForHub(hubId: string): void {
	for (const owners of histories.forget(hubId)?.values() ?? []) {
		for (const owner of owners) retireHistory(owner);
	}
}
