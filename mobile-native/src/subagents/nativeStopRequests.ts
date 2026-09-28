import { Storage } from "expo-sqlite/kv-store";
import { forgetStopRequests, StopRequests } from "./stopRequests";

// One instance per hub, so every screen sees the same requests and subscribers.
const requests = new Map<string, StopRequests>();

export function stopRequests(hubId: string): StopRequests {
	let hub = requests.get(hubId);
	if (!hub) {
		hub = new StopRequests(Storage, hubId);
		requests.set(hubId, hub);
	}
	return hub;
}

export function forgetStopRequestsForHub(hubId: string): void {
	requests.delete(hubId);
	forgetStopRequests(Storage, hubId);
}
