// One document's way to the system share sheet, for the Reader and the
// transcript's chips. The hook owns what a share needs from the connection —
// the hub's origin and its bearer token — so a caller only names the path.
import * as SecureStore from "expo-secure-store";
import { useCallback } from "react";
import { HubProfiles } from "../connection";
import { useConnection } from "../ConnectionProvider";

const hubs = new HubProfiles(SecureStore);

/** Shares one document of `sessionRef` through the hub `hubId`. Rejects when
 * the hub is unknown, the token cannot be read, or the share fails, so the
 * caller can say what happened. */
export function useShareDocument(hubId: string, sessionRef: string): (path: string) => Promise<void> {
	const { profiles } = useConnection();
	return useCallback(
		async (path: string) => {
			const origin = profiles.find((profile) => profile.id === hubId)?.origin;
			if (!origin) throw new Error("This hub is not connected.");
			const token = await hubs.token(hubId);
			// Loaded on use: the Expo file system and share modules are native
			// modules, and a screen that only draws a chip should not pull them in.
			const { shareSessionDocument } = await import("./shareDocumentNative");
			await shareSessionDocument({ origin, token, sessionRef, path });
		},
		[profiles, hubId, sessionRef],
	);
}
