import { Storage } from "expo-sqlite/kv-store";
import { perHub } from "../board/perHub";
import { forgetStopRequests, StopRequests } from "./stopRequests";

// One instance per hub, so every screen sees the same requests and subscribers.
const requests = perHub((hubId) => new StopRequests(Storage, hubId));

export function stopRequests(hubId: string): StopRequests {
	return requests.get(hubId);
}

export function forgetStopRequestsForHub(hubId: string): void {
	requests.forget(hubId);
	forgetStopRequests(Storage, hubId);
}
