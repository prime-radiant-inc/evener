// What a document chip and a Files row show about a document before you open
// it (spec 8.2, 10.1): its own title and its length. Each (session, path,
// write time) is read once per hub and shared by every chip and row that
// shows it. A failed read is forgotten, so the next chip to mount reads again.
// useDocumentSummary.ts is the hook over this cache; the cache itself stays
// free of the connection, so ConnectionProvider can forget a removed hub's.
import type { LoadedDocument } from "./documentSource";
import { readHubDocument } from "./hubDocument";

export interface DocumentSummary {
	title: string;
	/** Lines of text, for markdown and code. */
	lines?: number;
}

export interface SummaryEntry {
	read: Promise<DocumentSummary | null>;
	summary?: DocumentSummary;
}

const hubs = new Map<string, Map<string, SummaryEntry>>();

function summarize(document: LoadedDocument): DocumentSummary | null {
	if (document.kind === "failed") return null;
	if (document.kind === "markdown" || document.kind === "code") return { title: document.title, lines: document.lines };
	return { title: document.title };
}

const summaryKey = (sessionRef: string, path: string, updatedAt?: string) =>
	JSON.stringify([sessionRef, path, updatedAt ?? ""]);

export function summaryEntry(
	hubId: string,
	origin: string,
	sessionRef: string,
	path: string,
	updatedAt?: string,
): SummaryEntry {
	let entries = hubs.get(hubId);
	if (!entries) {
		entries = new Map();
		hubs.set(hubId, entries);
	}
	const key = summaryKey(sessionRef, path, updatedAt);
	const known = entries.get(key);
	if (known) return known;
	const entry: SummaryEntry = {
		read: readHubDocument(origin, hubId, sessionRef, path).then((document) => {
			const summary = summarize(document);
			if (summary) entry.summary = summary;
			else if (hubs.get(hubId)?.get(key) === entry) hubs.get(hubId)?.delete(key);
			return summary;
		}),
	};
	entries.set(key, entry);
	// A file written again gets a new key; the older write's summary would
	// otherwise stay in the map forever. A newer write stays, though: a screen
	// still showing it keeps its summary.
	const at = updatedAt === undefined ? Number.NaN : Date.parse(updatedAt);
	const prefix = `${JSON.stringify([sessionRef, path]).slice(0, -1)},`;
	for (const other of entries.keys()) {
		if (other === key || !other.startsWith(prefix)) continue;
		const otherAt = (JSON.parse(other) as [string, string, string])[2];
		if (Date.parse(otherAt) < at) entries.delete(other);
	}
	return entry;
}

/** The summary already read for this document, if any. */
export function knownSummary(
	hubId: string,
	sessionRef: string,
	path: string,
	updatedAt?: string,
): DocumentSummary | null {
	return hubs.get(hubId)?.get(summaryKey(sessionRef, path, updatedAt))?.summary ?? null;
}

/** Forgets a removed hub's summaries. */
export function forgetDocumentSummaries(hubId: string): void {
	hubs.delete(hubId);
}
