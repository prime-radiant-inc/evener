// The hub's launch option schema for the chosen host: what this hub build can
// launch a session with. The schema is host-scoped and changes only with the
// hub's build, so it is read once per host; a project pick or a launch
// settings change re-reads nothing. A page gates the fields a newer hub owns
// on it, so a control the connected hub cannot honor never shows as a toggle
// that silently does nothing.
import type { AppwireClient, LaunchOptionSchemaResponse } from "@evener/appwire-client";
import { useStore } from "zustand";
import type { NewSessionService } from "../conversation/services/newSession";
import type { NewSessionStore } from "./newSessionContext";
import { useHostOnlyRead } from "./useHostRead";

async function readSchema(service: NewSessionService, host: string): Promise<LaunchOptionSchemaResponse> {
	return service.launchSchema(host);
}

/** Null before the hub answers and after it couldn't; a page then hides the
 * fields the answer would have named. */
export function useSheetLaunchSchema(
	store: NewSessionStore,
	client: AppwireClient | null,
	ready: boolean,
): LaunchOptionSchemaResponse | null {
	const source = useStore(store, (form) => form.source);
	return useHostOnlyRead(client, ready, source, readSchema);
}
