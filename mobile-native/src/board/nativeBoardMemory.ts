import { Storage } from "expo-sqlite/kv-store";
import { FoldedSections, forgetBoard, OrganizeByPreference, SeenMarkers } from "./boardMemory";
import { BoardSeen, forgetHubSeenMarks, hubSeenMarks } from "./hubSeen";

/** One instance per hub, made on first use and dropped when the hub is forgotten. */
function perHub<T>(make: (hubId: string) => T) {
	const instances = new Map<string, T>();
	return {
		get(hubId: string): T {
			let instance = instances.get(hubId);
			if (!instance) {
				instance = make(hubId);
				instances.set(hubId, instance);
			}
			return instance;
		},
		forget(hubId: string): void {
			instances.delete(hubId);
		},
	};
}

// One instance per hub, so every screen reading the Board's memory sees the
// same in-memory state and the same subscribers.
const seen = perHub((hubId) => new SeenMarkers(Storage, hubId));
const folded = perHub((hubId) => new FoldedSections(Storage, hubId));
const organize = perHub((hubId) => new OrganizeByPreference(Storage, hubId));

export function seenMarkers(hubId: string): SeenMarkers {
	return seen.get(hubId);
}

/** The Board's seen state for a hub: the hub's marker where rows carry it,
 * the device's markers elsewhere (S4). */
export function boardSeen(hubId: string): BoardSeen {
	return new BoardSeen(seenMarkers(hubId), hubSeenMarks(hubId));
}

export function foldedSections(hubId: string): FoldedSections {
	return folded.get(hubId);
}

export function organizeByPreference(hubId: string): OrganizeByPreference {
	return organize.get(hubId);
}

export function forgetBoardForHub(hubId: string): void {
	for (const memory of [seen, folded, organize]) memory.forget(hubId);
	forgetHubSeenMarks(hubId);
	forgetBoard(Storage, hubId);
}
