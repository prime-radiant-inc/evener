import { Storage } from "expo-sqlite/kv-store";
import { useMemo, useSyncExternalStore } from "react";
import { FoldedSections, forgetBoard, OrganizeByPreference, RecentSearches, SeenMarkers } from "./boardMemory";
import { BoardSeen, forgetHubSeenMarks, hubSeenMarks } from "./hubSeen";
import { perHub } from "./perHub";

// One instance per hub, so every screen reading the Board's memory sees the
// same in-memory state and the same subscribers.
const seen = perHub((hubId) => new SeenMarkers(Storage, hubId));
const folded = perHub((hubId) => new FoldedSections(Storage, hubId));
const organize = perHub((hubId) => new OrganizeByPreference(Storage, hubId));
const recent = perHub((hubId) => new RecentSearches(Storage, hubId));

export function seenMarkers(hubId: string): SeenMarkers {
	return seen.get(hubId);
}

/** The Board's seen state for a hub: the hub's marker where rows carry it,
 * the device's markers elsewhere (S4). */
export function boardSeen(hubId: string): BoardSeen {
	return new BoardSeen(seenMarkers(hubId), hubSeenMarks(hubId));
}

/** boardSeen for a screen: a new BoardSeen with each mark or pruned mark on
 * either path, so a memo that reads isSeen lists it alone. */
export function useBoardSeen(hubId: string): BoardSeen {
	const markers = seenMarkers(hubId);
	const hub = hubSeenMarks(hubId);
	const markersRevision = useSyncExternalStore(markers.subscribe, markers.getRevision);
	const hubRevision = useSyncExternalStore(hub.subscribe, hub.getRevision);
	return useMemo(() => boardSeen(hubId), [hubId, markersRevision, hubRevision]);
}

export function foldedSections(hubId: string): FoldedSections {
	return folded.get(hubId);
}

export function organizeByPreference(hubId: string): OrganizeByPreference {
	return organize.get(hubId);
}

export function recentSearches(hubId: string): RecentSearches {
	return recent.get(hubId);
}

export function forgetBoardForHub(hubId: string): void {
	for (const memory of [seen, folded, organize, recent]) memory.forget(hubId);
	forgetHubSeenMarks(hubId);
	forgetBoard(Storage, hubId);
}
