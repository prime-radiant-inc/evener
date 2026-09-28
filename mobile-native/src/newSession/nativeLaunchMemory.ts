import * as Crypto from "expo-crypto";
import { Storage } from "expo-sqlite/kv-store";
import { forgetLaunchMemory, LaunchMemory } from "./launchMemory";

const memories = new Map<string, LaunchMemory>();

/** The one LaunchMemory per hub, shared by New session and the Hub's Recipes
 * page, so a recipe saved on one shows on the other at once. */
export function launchMemory(hubId: string): LaunchMemory {
	let memory = memories.get(hubId);
	if (!memory) {
		memory = new LaunchMemory(Storage, hubId, () => Crypto.randomUUID());
		memories.set(hubId, memory);
	}
	return memory;
}

export function forgetLaunchMemoryForHub(hubId: string): void {
	// The cached copy goes first, so a storage failure can't leave it to write
	// the removed hub's recipes back.
	memories.delete(hubId);
	forgetLaunchMemory(Storage, hubId);
}
