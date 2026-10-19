// The real download and share sheet for shareDocument's seam. Kept apart so
// the seam itself has no native imports and tests can load it.
import { docDownloadURL } from "@evener/appwire-client/docContent";
import { Directory, File, Paths } from "expo-file-system";
import * as Sharing from "expo-sharing";
import { docAuthHeaders } from "../nativeDocPort";
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
	directory.create({ intermediates: true, idempotent: true });
	const destination = new File(directory, shareFileBaseName(target.sessionRef, target.namePath ?? target.path));
	const file = await File.downloadFileAsync(
		docDownloadURL(target.origin, target.sessionRef, target.path),
		destination,
		{
			headers: docAuthHeaders(target.token),
			idempotent: true,
		},
	);
	return file.uri;
}

const deps: ShareDocumentDeps = {
	available: () => Sharing.isAvailableAsync(),
	download: downloadForShare,
	// UIActivityViewController, whose "Open in…" reaches every installed app
	// that handles the file's type.
	share: (url) => Sharing.shareAsync(url),
};

// One share at a time. iOS cannot present a second activity sheet over the
// first, and a second download would otherwise replace a file the open sheet
// is still offering. A share that fails does not block the next one.
let current: Promise<void> = Promise.resolve();

/** Saves one session document and opens the share sheet for it, after any
 * share already in flight finishes. */
export function shareSessionDocument(target: DocumentShareTarget): Promise<void> {
	const next = current.then(() => shareDocument(target, deps));
	current = next.catch(() => undefined);
	return next;
}
