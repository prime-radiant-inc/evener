// Reading a document from a hub: its /doc/file route with this hub's origin
// and token, as TranscriptImages reads images. The Reader and the document
// summaries both read through here.
import { filenameOf } from "@evener/appwire-client/docContent";
import * as SecureStore from "expo-secure-store";
import { HubProfiles } from "../connection";
import { nativeDocPort } from "../nativeDocPort";
import { type LoadedDocument, loadDocument } from "./documentSource";

const hubs = new HubProfiles(SecureStore);

/** Reads a document through the hub's /doc/file with this hub's token. A
 * token that can't be read is as transient as a failed request. */
export async function readHubDocument(
	origin: string,
	hubId: string,
	sessionRef: string,
	path: string,
): Promise<LoadedDocument> {
	try {
		return await loadDocument(nativeDocPort(origin, await hubs.token(hubId)), sessionRef, path);
	} catch {
		return { kind: "failed", title: filenameOf(path) };
	}
}
