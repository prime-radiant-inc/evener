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
	/** The same file as the person sees it, relative to the session's folder.
	 * A Reader holding an absolute target still names the share from here, so
	 * the host's own path never reaches the file name another app receives. */
	namePath?: string;
}

export interface ShareDocumentDeps {
	/** Whether the platform can share files at all. */
	available: () => Promise<boolean>;
	/** Saves the document to a local file and returns its file:// URL. */
	download: (target: DocumentShareTarget) => Promise<string>;
	/** Opens the share sheet for a local file:// URL. */
	share: (url: string) => Promise<void>;
}

/** The longest cache name this builds. iOS refuses a file name past 255 bytes,
 * and the destination is a name rather than a path, so the bound is on the name
 * alone; staying well under it leaves room for the system's own suffixing. */
const MAX_SHARE_NAME = 180;

/** The local file name a shared document takes: the session and the whole
 * path, because the cache outlives one share and a sheet can still be open on
 * the first file when the second downloads. `public/report.pdf` and
 * `private/report.pdf` must not share a destination, or the second download
 * replaces the bytes the first sheet is offering. Characters a file system or
 * a share target would stumble on are replaced, and the file keeps its own
 * extension so the receiving app still recognizes the type.
 *
 * A valid source path can be longer than one file name may be, so the folders
 * are shortened to fit rather than dropped: the basename and extension stay
 * whole, and a short hash of the full path keeps two deep paths apart even
 * after their prefixes are cut. */
export function shareFileBaseName(sessionRef: string, path: string): string {
	const session = sessionRef.replace(/[^\w.\-]+/g, "_");
	const name = filenameOf(path).replace(/[^\w.\- ]+/g, "_");
	// The folders, so one folder's report does not collide with another's:
	// `docs/a.mp4` becomes `local_abc-docs-onboarding.mp4`.
	const folders = path.slice(0, Math.max(path.lastIndexOf("/"), 0));
	const folder = folders
		.replace(/^\/+|\/+$/g, "")
		.replace(/[^\w.\- ]+/g, "_")
		.replace(/\//g, "-");
	const whole = [session, folder, name].filter((part) => part !== "").join("-");
	if (whole.length <= MAX_SHARE_NAME) return whole;

	// The extension belongs to the file the receiving app must recognize.
	const dot = name.lastIndexOf(".");
	const extension = dot > 0 ? name.slice(dot) : "";
	const stem = dot > 0 ? name.slice(0, dot) : name;
	const suffix = `-${pathHash(path)}${extension}`;
	const room = Math.max(MAX_SHARE_NAME - suffix.length, 0);
	const head = [session, folder, stem].filter((part) => part !== "").join("-");
	return `${head.slice(0, room)}${suffix}`;
}

/** A short stable digest of a path, for keeping two shortened names apart.
 * FNV-1a, in hex: small, dependency-free, and the collision risk is irrelevant
 * here because the full path still decides the name's usable prefix. */
function pathHash(path: string): string {
	let hash = 0x811c9dc5;
	for (let index = 0; index < path.length; index += 1) {
		hash ^= path.charCodeAt(index);
		hash = Math.imul(hash, 0x01000193) >>> 0;
	}
	return hash.toString(16).padStart(8, "0");
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
