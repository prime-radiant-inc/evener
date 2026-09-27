import { Storage } from "expo-sqlite/kv-store";
import { FoldedSections, forgetBoard, OrganizeByPreference, SeenMarkers } from "./boardMemory";

// One instance per hub, so every screen reading the Board's memory sees the
// same in-memory state and the same subscribers.
const seen = new Map<string, SeenMarkers>();
const folded = new Map<string, FoldedSections>();
const organize = new Map<string, OrganizeByPreference>();

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

export function organizeByPreference(hubId: string): OrganizeByPreference {
	let choice = organize.get(hubId);
	if (!choice) {
		choice = new OrganizeByPreference(Storage, hubId);
		organize.set(hubId, choice);
	}
	return choice;
}

export function forgetBoardForHub(hubId: string): void {
	seen.delete(hubId);
	folded.delete(hubId);
	organize.delete(hubId);
	forgetBoard(Storage, hubId);
}
