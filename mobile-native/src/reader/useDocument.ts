// The Reader's document (spec 10.2), read through the hub's /doc/file with
// this hub's origin and token, as TranscriptImages reads images. It reads on
// mount, on reload(), when the connection comes back, and when the app
// returns to the front. A re-read keeps the shown document until the new one
// lands, and a failed one keeps it for good: a failure is transient, while a
// missing or forbidden file really changed.
import { filenameOf } from "@evener/appwire-client/docContent";
import * as SecureStore from "expo-secure-store";
import { useCallback, useEffect, useRef, useState } from "react";
import { AppState } from "react-native";
import { useConnection } from "../ConnectionProvider";
import { HubProfiles } from "../connection";
import { nativeDocPort } from "../nativeDocPort";
import { type LoadedDocument, loadDocument } from "./documentSource";

const hubs = new HubProfiles(SecureStore);

export function useDocument(
	hubId: string,
	sessionRef: string,
	path: string,
): { document: LoadedDocument | null; reload(): void } {
	const { profiles, state } = useConnection();
	const origin = profiles.find((profile) => profile.id === hubId)?.origin ?? "";
	const [document, setDocument] = useState<LoadedDocument | null>(null);
	// Only the newest read may land: an older one finishing late would put
	// back a version already replaced.
	const generation = useRef(0);
	const reload = useCallback(() => {
		generation.current += 1;
		const mine = generation.current;
		void (async () => {
			let next: LoadedDocument;
			try {
				next = await loadDocument(nativeDocPort(origin, await hubs.token(hubId)), sessionRef, path);
			} catch {
				// The token couldn't be read: as transient as a failed request.
				next = { kind: "failed", title: filenameOf(path) };
			}
			if (mine !== generation.current) return;
			setDocument((shown) => (next.kind === "failed" && shown !== null ? shown : next));
		})();
	}, [origin, hubId, sessionRef, path]);

	useEffect(() => {
		reload();
		// A document read after its screen closed has nowhere to land.
		return () => {
			generation.current += 1;
		};
	}, [reload]);

	const lastState = useRef(state);
	useEffect(() => {
		const was = lastState.current;
		lastState.current = state;
		if (state === "ready" && was !== "ready") reload();
	}, [state, reload]);

	useEffect(() => {
		const subscription = AppState.addEventListener("change", (next) => {
			if (next === "active") reload();
		});
		return () => subscription.remove();
	}, [reload]);

	return { document, reload };
}
