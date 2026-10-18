// Saving a session document to the device and handing it to the system share
// sheet (iOS: UIActivityViewController). The sheet is the platform's own
// affordance: it carries "Open in another app" for every app that handles the
// file's type (VLC, QuickTime, Pages) as well as Save to Files, AirDrop,
// Messages and Mail, so the app adds no picker of its own.
//
// The two steps are injected so this module stays free of native imports: the
// screen supplies the real ones (shareDocumentNative.ts), and tests supply
// fakes.
import { filenameOf } from "@evener/appwire-client/docContent";

export interface DocumentShareTarget {
	/** The hub's base URL, with no trailing "/doc". */
	origin: string;
	/** This hub's bearer token, or "" for a hub that has none. */
	token: string;
	sessionRef: string;
	/** The session-scoped path the hub resolves inside its working directory. */
	path: string;
}

/** Saves the document to a local file and returns its file:// URL. */
export type DownloadForShare = (target: DocumentShareTarget) => Promise<string>;

/** Opens the share sheet for a local file:// URL. */
export type OpenShareSheet = (url: string) => Promise<void>;

export interface ShareDocumentDeps {
	/** Whether the platform can share files at all. */
	available: () => Promise<boolean>;
	download: DownloadForShare;
	share: OpenShareSheet;
}

/** The local file name a shared document takes. Two sessions can name the
 * same file and the cache outlives one share, so the name carries the session
 * too; characters a file system or a share target would stumble on are
 * replaced. */
export function shareFileBaseName(sessionRef: string, path: string): string {
	const session = sessionRef.replace(/[^\w.\-]+/g, "_");
	const name = filenameOf(path).replace(/[^\w.\- ]+/g, "_");
	return `${session}-${name}`;
}

/** Saves the document, then opens the share sheet for it. Rejects when the
 * download fails or the sheet is unavailable, so the caller can say what
 * happened instead of leaving a press with no visible effect. */
export async function shareDocument(target: DocumentShareTarget, deps: ShareDocumentDeps): Promise<void> {
	if (!(await deps.available())) throw new ShareUnavailable();
	const url = await deps.download(target);
	await deps.share(url);
}

/** The platform cannot share files (the web build, which cannot share a local
 * file by URI). */
export class ShareUnavailable extends Error {
	constructor() {
		super("Sharing is not available on this device.");
		this.name = "ShareUnavailable";
	}
}
