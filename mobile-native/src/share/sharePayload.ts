// Shares handed over by the iOS share extension (spec: share intent). The
// extension writes one JSON file per share into the App Group container and
// the app reads them back here. A file is deleted only once its content is
// safely in a draft, so a crash or a failed launch never loses the share.

/** A share the extension wrote: the text or URL the person shared. */
export interface SharedPayload {
	/** The payload's identity, which is also the file name's. */
	id: string;
	/** What was shared, ready to merge into a composer draft. */
	text: string;
	/** When the extension wrote it, as an ISO string. */
	createdAt: string;
}

/** The App Group directory as this module needs it. Production is the app
 * group container; tests pass a directory of their own. */
export interface SharePort {
	list(): Promise<string[]>;
	read(name: string): Promise<string>;
	remove(name: string): Promise<void>;
}

/** The one file name a share's id reads from. The extension writes and the app
 * deletes by this same name, so neither has a second spelling to keep level. */
export function shareFileName(id: string): string {
	return `share-${id}.json`;
}

/** Whether a directory entry is a share file, by the one naming rule. */
export function isShareFileName(name: string): boolean {
	return name.startsWith("share-") && name.endsWith(".json");
}

/** The payload a file holds, or null when the file is not one. A file whose id
 * and name disagree is refused: it would otherwise be deleted by a name that
 * is not its own, or offered twice. */
export function parseSharePayload(fileName: string, raw: string): SharedPayload | null {
	let value: unknown;
	try {
		value = JSON.parse(raw);
	} catch {
		return null;
	}
	if (!value || typeof value !== "object") return null;
	const { id, text, createdAt } = value as Record<string, unknown>;
	if (typeof id !== "string" || !id.trim()) return null;
	if (typeof text !== "string" || !text.trim()) return null;
	if (typeof createdAt !== "string" || !createdAt) return null;
	if (fileName !== shareFileName(id)) return null;
	return { id, text, createdAt };
}

/** Every share waiting to be placed, oldest first. A file that cannot be read
 * or parsed is left alone: it may be half-written, and deleting it would throw
 * away what the person shared. */
export async function pendingShares(port: SharePort): Promise<SharedPayload[]> {
	const shares: SharedPayload[] = [];
	for (const name of await port.list()) {
		if (!isShareFileName(name)) continue;
		let raw: string;
		try {
			raw = await port.read(name);
		} catch {
			continue;
		}
		const payload = parseSharePayload(name, raw);
		if (payload) shares.push(payload);
	}
	return shares.sort((left, right) => Date.parse(left.createdAt) - Date.parse(right.createdAt));
}

/** Drops a share that has been placed or explicitly discarded. */
export async function completeShare(port: SharePort, id: string): Promise<void> {
	await port.remove(shareFileName(id));
}
