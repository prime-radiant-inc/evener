import { Storage } from "expo-sqlite/kv-store";
import { DetailLevels, forgetDetailLevels } from "./detailLevels";

// One instance per hub, so every screen sees the same choices and subscribers.
const byHub = new Map<string, DetailLevels>();

export function detailLevels(hubId: string): DetailLevels {
	let levels = byHub.get(hubId);
	if (!levels) {
		levels = new DetailLevels(Storage, hubId);
		byHub.set(hubId, levels);
	}
	return levels;
}

export function forgetDetailLevelsForHub(hubId: string): void {
	byHub.delete(hubId);
	forgetDetailLevels(Storage, hubId);
}
