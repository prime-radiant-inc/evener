import { Storage } from "expo-sqlite/kv-store";
import { forgetLaunchMemory, LaunchMemory } from "./launchMemory";

const memories = new Map<string, LaunchMemory>();

/** The one LaunchMemory per hub, so every New session sheet on that hub reads
 * the starts the last one recorded. */
export function launchMemory(hubId: string): LaunchMemory {
	let memory = memories.get(hubId);
	if (!memory) {
		memory = new LaunchMemory(Storage, hubId);
		memories.set(hubId, memory);
	}
	return memory;
}

export function forgetLaunchMemoryForHub(hubId: string): void {
	// The cached copy goes first, so a storage failure can't leave it to write
	// the removed hub's starts back.
	memories.delete(hubId);
	forgetLaunchMemory(Storage, hubId);
}
