import { Storage } from "expo-sqlite/kv-store";
import { perHub } from "../board/perHub";
import { forgetLaunchMemory, LaunchMemory } from "./launchMemory";

const memories = perHub((hubId) => new LaunchMemory(Storage, hubId));

/** The one LaunchMemory per hub, so every New session sheet on that hub reads
 * the last start any of them recorded. */
export function launchMemory(hubId: string): LaunchMemory {
	return memories.get(hubId);
}

export function forgetLaunchMemoryForHub(hubId: string): void {
	// The cached copy goes first, so a storage failure can't leave it to write
	// the removed hub's remembered start back.
	memories.forget(hubId);
	forgetLaunchMemory(Storage, hubId);
}
