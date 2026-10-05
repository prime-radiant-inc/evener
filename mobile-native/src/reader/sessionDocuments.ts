// Files (spec 10.1): everything the session wrote or linked, one
// row per file, newest write first, and whether each is new or changed since
// you last opened it. Artifacts join when the shared-artifacts work reaches
// main (10.3).
import type { SessionURL } from "@evener/appwire-client";
import { fileURLToPath } from "@evener/appwire-client/docContent";
import type { LastRead } from "./documentMemory";
import { type DocumentReference, documentPath } from "./documentReferences";
import { type DocumentKind, documentKind } from "./documentSource";

export interface SessionDocument {
	/** The path inside the session's folder, as the Reader reads it. */
	path: string;
	kind: DocumentKind;
	updatedAt?: string;
}

function time(value: string | undefined): number {
	const parsed = value ? Date.parse(value) : Number.NaN;
	return Number.isFinite(parsed) ? parsed : Number.NEGATIVE_INFINITY;
}

/** One row per file inside the session's folder, carrying its newest write:
 * two names for one file are one row. Newest write first; files with no write
 * time follow in the order they appeared. A file link outside the folder is
 * left out, because the hub serves documents only from inside it. */
export function sessionDocuments(
	references: readonly DocumentReference[],
	links: readonly SessionURL[],
	cwd: string,
): SessionDocument[] {
	const byFile = new Map<string, SessionDocument>();
	const add = (named: string, updatedAt: string | undefined) => {
		const path = documentPath(named, cwd);
		if (path === undefined) return;
		const known = byFile.get(path);
		const newest = time(updatedAt) > time(known?.updatedAt) ? updatedAt : known?.updatedAt;
		byFile.set(path, { path, kind: documentKind(path), ...(newest ? { updatedAt: newest } : {}) });
	};
	for (const reference of references) add(reference.path, reference.updatedAt);
	for (const link of links) if (/^file:/i.test(link.url)) add(fileURLToPath(link.url), undefined);
	return [...byFile.values()].sort((a, b) => {
		const difference = time(b.updatedAt) - time(a.updatedAt);
		return Number.isNaN(difference) ? 0 : difference;
	});
}

export type Freshness = "new" | "changed" | "read";

/** A Files row's and a document chip's blue dot (spec 10.1, 8.2): new until
 * you've opened it; changed when the session wrote it after the write you
 * last read. Both are hub times, so the phone's clock never matters; without
 * both, it can't tell, and says nothing. */
export function documentFreshness(lastRead: LastRead | null, updatedAt: string | undefined): Freshness {
	if (!lastRead) return "new";
	const seen = time(lastRead.updatedAt);
	return seen > Number.NEGATIVE_INFINITY && time(updatedAt) > seen ? "changed" : "read";
}
