import { Storage } from "expo-sqlite/kv-store";
import { FoldedSections, forgetBoard, SeenMarkers } from "./boardMemory";
import { BoardSeen, forgetHubSeenMarks, hubSeenMarks } from "./hubSeen";

// One instance per hub, so every screen reading the Board's memory sees the
// same in-memory state and the same subscribers.
const seen = new Map<string, SeenMarkers>();
const folded = new Map<string, FoldedSections>();

export function seenMarkers(hubId: string): SeenMarkers {
	let markers = seen.get(hubId);
	if (!markers) {
		markers = new SeenMarkers(Storage, hubId);
		seen.set(hubId, markers);
	}
	return markers;
}

/** The Board's seen state for a hub: the hub's marker where rows carry it,
 * the device's markers elsewhere (S4). */
export function boardSeen(hubId: string): BoardSeen {
	return new BoardSeen(seenMarkers(hubId), hubSeenMarks(hubId));
}

export function foldedSections(hubId: string): FoldedSections {
	let sections = folded.get(hubId);
	if (!sections) {
		sections = new FoldedSections(Storage, hubId);
		folded.set(hubId, sections);
	}
	return sections;
}

export function forgetBoardForHub(hubId: string): void {
	seen.delete(hubId);
	folded.delete(hubId);
	forgetHubSeenMarks(hubId);
	forgetBoard(Storage, hubId);
}
