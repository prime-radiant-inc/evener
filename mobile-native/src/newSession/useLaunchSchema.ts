// The hub's launch option schema for the chosen host: what this hub build can
// launch a session with. The sheet reads it once for the host and project (the
// schema is host-scoped; the project only keys the re-read) and again when the
// hub announces its launch settings changed. A page gates the fields a newer
// hub owns on it, so a control the connected hub cannot honor never shows as a
// toggle that silently does nothing.
import type { AppwireClient, LaunchOptionSchemaResponse } from "@evener/appwire-client";
import { useStore } from "zustand";
import type { NewSessionService } from "../conversation/services/newSession";
import type { NewSessionStore } from "./newSessionContext";
import { useHostRead, useHubRevision } from "./useHostRead";

const LAUNCH_CHANGES = ["evener/launch/updated"] as const;

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
	const cwd = useStore(store, (form) => form.cwd);
	const revision = useHubRevision(client, LAUNCH_CHANGES);
	return useHostRead(client, ready, source, cwd, readSchema, revision);
}
