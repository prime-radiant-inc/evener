// The Reader's document (spec 10.2), read through the hub's /doc/file with
// this hub's origin and token, as TranscriptImages reads images. It reads on
// mount, on reload(), when the connection comes back, and when the app
// returns to the front. A re-read keeps the shown document until the new one
// lands, and a re-read that fails keeps it: a failure is transient, while a
// missing or forbidden file really changed.
import { useCallback, useEffect, useRef, useState } from "react";
import { AppState } from "react-native";
import { useConnection } from "../ConnectionProvider";
import type { LoadedDocument } from "./documentSource";
import { readHubDocument } from "./hubDocument";

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
			const next = await readHubDocument(origin, hubId, sessionRef, path);
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
