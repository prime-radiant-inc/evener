// One document's way to the system share sheet, for the Reader and the
// transcript's chips. The hook owns what a share needs from the connection —
// the hub's origin and its bearer token — and it owns the failure notice, so a
// caller only names the document and never repeats the copy.
import * as SecureStore from "expo-secure-store";
import { useCallback } from "react";
import { Alert } from "react-native";
import { HubProfiles } from "../connection";
import { useConnection } from "../ConnectionProvider";
import { ShareUnavailable } from "./shareDocument";

const hubs = new HubProfiles(SecureStore);

/** Shares one document of `sessionRef` through the hub `hubId`, downloading it
 * first. `namePath` is the same file's session-relative path when `path` is an
 * absolute target, so the shared name does not carry the host's directories.
 * The returned action reports its own failure and never rejects. */
export function useShareDocument(hubId: string, sessionRef: string): (path: string, namePath?: string) => void {
	const { profiles } = useConnection();
	return useCallback(
		(path: string, namePath?: string) => {
			const origin = profiles.find((profile) => profile.id === hubId)?.origin;
			const share = async () => {
				if (!origin) throw new Error("This hub is not connected.");
				const token = await hubs.token(hubId);
				// Loaded on use: the Expo file system and share modules are native
				// modules, and a screen that only draws a chip should not pull them in.
				const { shareSessionDocument } = await import("./shareDocumentNative");
				await shareSessionDocument({ origin, token, sessionRef, path, ...(namePath ? { namePath } : {}) });
			};
			share().catch((error: unknown) =>
				Alert.alert(
					"Could not share this document",
					error instanceof ShareUnavailable ? error.message : "It could not be saved from this hub. Try again.",
				),
			);
		},
		[profiles, hubId, sessionRef],
	);
}
