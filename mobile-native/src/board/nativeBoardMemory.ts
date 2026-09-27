import { Storage } from "expo-sqlite/kv-store";
import { FoldedSections, forgetBoard, SeenMarkers } from "./boardMemory";
import { RecentSearches } from "./boardSearch";

// One instance per hub, so every screen reading the Board's memory sees the
// same in-memory state and the same subscribers.
const seen = new Map<string, SeenMarkers>();
const folded = new Map<string, FoldedSections>();
const recent = new Map<string, RecentSearches>();

export function seenMarkers(hubId: string): SeenMarkers {
	let markers = seen.get(hubId);
	if (!markers) {
		markers = new SeenMarkers(Storage, hubId);
		seen.set(hubId, markers);
	}
	return markers;
}

export function foldedSections(hubId: string): FoldedSections {
	let sections = folded.get(hubId);
	if (!sections) {
		sections = new FoldedSections(Storage, hubId);
		folded.set(hubId, sections);
	}
	return sections;
}

export function recentSearches(hubId: string): RecentSearches {
	let searches = recent.get(hubId);
	if (!searches) {
		searches = new RecentSearches(Storage, hubId);
		recent.set(hubId, searches);
	}
	return searches;
}

export function forgetBoardForHub(hubId: string): void {
	seen.delete(hubId);
	folded.delete(hubId);
	recent.delete(hubId);
	forgetBoard(Storage, hubId);
}
