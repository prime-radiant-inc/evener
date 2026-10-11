import { Storage } from "expo-sqlite/kv-store";
import { DocumentMemory, forgetDocuments } from "./documentMemory";

// One instance per hub, so the Reader, the Files sheet, the chips and the
// Board read the same memory and the same subscribers.
const memories = new Map<string, DocumentMemory>();

export function documentMemory(hubId: string): DocumentMemory {
	let memory = memories.get(hubId);
	if (!memory) {
		memory = new DocumentMemory(Storage, hubId);
		memories.set(hubId, memory);
	}
	return memory;
}

export function forgetDocumentsForHub(hubId: string): void {
	memories.delete(hubId);
	forgetDocuments(Storage, hubId);
}
