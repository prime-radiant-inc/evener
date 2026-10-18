// The real download and share sheet for shareDocument's seam. Kept apart so
// the seam itself has no native imports and tests can load it.
import { docDownloadURL } from "@evener/appwire-client/docContent";
import { Directory, File, Paths } from "expo-file-system";
import * as Sharing from "expo-sharing";
import { type DocumentShareTarget, shareDocument, shareFileBaseName, type ShareDocumentDeps } from "./shareDocument";

/** Where a document waits while it is offered to another app. The cache, not
 * documents: the copy is disposable once the sheet closes, and iOS may purge
 * it. */
const SHARE_DIR = "evener-share";

/** Saves the document to the cache through the hub's whole-file route, with
 * this hub's bearer token. iOS writes to a temporary location and moves the
 * file into place only on success, so a failed transfer leaves nothing. */
async function downloadForShare(target: DocumentShareTarget): Promise<string> {
	const directory = new Directory(Paths.cache, SHARE_DIR);
	if (!directory.exists) directory.create({ intermediates: true });
	const destination = new File(directory, shareFileBaseName(target.sessionRef, target.path));
	const file = await File.downloadFileAsync(
		docDownloadURL(target.origin, target.sessionRef, target.path),
		destination,
		{
			headers: target.token ? { Authorization: `Bearer ${target.token}` } : {},
			idempotent: true,
		},
	);
	return file.uri;
}

/** UIActivityViewController, whose "Open in…" reaches every installed app
 * that handles the file's type. */
async function openShareSheet(url: string): Promise<void> {
	await Sharing.shareAsync(url);
}

const deps: ShareDocumentDeps = {
	available: () => Sharing.isAvailableAsync(),
	download: downloadForShare,
	share: openShareSheet,
};

/** Saves one session document and opens the share sheet for it. */
export function shareSessionDocument(target: DocumentShareTarget): Promise<void> {
	return shareDocument(target, deps);
}
