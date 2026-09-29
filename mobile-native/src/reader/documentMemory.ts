// What this device remembers about the documents you read, per hub (spec
// 10.2, 7.1): where you were in each, the version you last read (as block
// hashes: S9's fallback for "changes since you last read"), your unsent
// review comments, and the Board's Continue reading trail. Kept in
// expo-sqlite's kv-store under per-hub keys that ConnectionProvider.removeHub
// clears.
import { isPlainObject } from "@evener/appwire-client";
import { readJson, removeKeys, writeJson } from "../deviceStorage";
import type { SyncStringStorage } from "../syncStringStorage";

export interface DocumentKey {
	/** The session whose folder holds the file. */
	sessionRef: string;
	path: string;
}

export interface ReadingPosition {
	/** The block at the top of the screen, by place and by words, so a restore
	 * survives edits above it. */
	blockIndex: number;
	blockHash: string;
	/** How far into that block the screen's top edge is, in points. */
	offset: number;
	/** How far down the document the screen's bottom edge reached, 0 to 1. */
	progress: number;
}

export interface DocumentComment {
	id: string;
	blockIndex: number;
	blockHash: string;
	/** The words it was left on: a selection, or its block's words. */
	quote: string;
	text: string;
	createdAt: number;
}

export interface LastRead {
	/** The block hashes of the version you last read. */
	blocks: readonly string[];
	/** When you left it, by this phone's clock: for "since you read it yesterday". */
	readAt: number;
	/** The write time its opener reported then (a hub time), for Files' blue dots. */
	updatedAt?: string;
}

export interface ContinueReading {
	sessionRef: string;
	path: string;
	title: string;
	/** The document's session title, where Open session and the review go (ruling 16). */
	sessionTitle: string;
	progress: number;
	leftAt: number;
	updatedAt?: string;
}

export interface Leaving {
	title: string;
	blocks: readonly string[];
	position: ReadingPosition | null;
	sessionTitle: string;
	updatedAt?: string;
}

interface DocumentRecord {
	position?: ReadingPosition;
	lastRead?: LastRead;
	comments?: DocumentComment[];
	touchedAt: number;
}

const documentsKey = (hubId: string) => `evener.native.documents.${hubId}`;
const trailKey = (hubId: string) => `evener.native.continue-reading.${hubId}`;
const recordKey = (key: DocumentKey) => JSON.stringify([key.sessionRef, key.path]);
const LIMIT = 100;
/** How long the Board offers the way back (spec 7.1). */
export const CONTINUE_READING_MS = 2 * 60 * 60 * 1000;
/** Leaving past this much of a document counts as finishing it (ruling 20). */
export const FINISHED_PROGRESS = 0.97;

const isCount = (value: unknown): value is number => typeof value === "number" && Number.isInteger(value) && value >= 0;
const isTime = (value: unknown): value is number => typeof value === "number" && Number.isFinite(value);
const isText = (value: unknown): value is string => typeof value === "string";

function parsePosition(value: unknown): ReadingPosition | undefined {
	if (!isPlainObject(value)) return undefined;
	const { blockIndex, blockHash, offset, progress } = value;
	if (!isCount(blockIndex) || !isText(blockHash) || !isTime(offset) || offset < 0) return undefined;
	if (!isTime(progress) || progress < 0 || progress > 1) return undefined;
	return { blockIndex, blockHash, offset, progress };
}

function parseLastRead(value: unknown): LastRead | undefined {
	if (!isPlainObject(value) || !Array.isArray(value.blocks) || !value.blocks.every(isText) || !isTime(value.readAt))
		return undefined;
	return {
		blocks: value.blocks,
		readAt: value.readAt,
		...(isText(value.updatedAt) ? { updatedAt: value.updatedAt } : {}),
	};
}

function parseComment(value: unknown): DocumentComment | undefined {
	if (!isPlainObject(value)) return undefined;
	const { id, blockIndex, blockHash, quote, text, createdAt } = value;
	if (
		!isText(id) ||
		!isCount(blockIndex) ||
		!isText(blockHash) ||
		!isText(quote) ||
		!isText(text) ||
		!isTime(createdAt)
	)
		return undefined;
	return { id, blockIndex, blockHash, quote, text, createdAt };
}

function parseDocuments(value: unknown): Record<string, DocumentRecord> {
	const documents: Record<string, DocumentRecord> = {};
	if (!isPlainObject(value)) return documents;
	for (const [key, raw] of Object.entries(value)) {
		if (!isPlainObject(raw) || !isTime(raw.touchedAt)) continue;
		const position = parsePosition(raw.position);
		const lastRead = parseLastRead(raw.lastRead);
		const comments = Array.isArray(raw.comments)
			? raw.comments.map(parseComment).filter((comment): comment is DocumentComment => comment !== undefined)
			: [];
		documents[key] = {
			touchedAt: raw.touchedAt,
			...(position ? { position } : {}),
			...(lastRead ? { lastRead } : {}),
			...(comments.length > 0 ? { comments } : {}),
		};
	}
	return documents;
}

function parseTrail(value: unknown): ContinueReading | null {
	if (!isPlainObject(value)) return null;
	// A trail written before #2871 named its session twice (reviewRef and
	// reviewTitle); it is dropped rather than migrated. The row is a two-hour
	// convenience, and this store drops any shape it doesn't recognize rather
	// than carrying a migration for it.
	const { sessionRef, path, title, sessionTitle, progress, leftAt, updatedAt } = value;
	if (!isText(sessionRef) || !isText(path) || !isText(title) || !isText(sessionTitle)) return null;
	if (!isTime(progress) || !isTime(leftAt)) return null;
	return {
		sessionRef,
		path,
		title,
		sessionTitle,
		progress,
		leftAt,
		...(isText(updatedAt) ? { updatedAt } : {}),
	};
}

export class DocumentMemory {
	private documents: Record<string, DocumentRecord>;
	private trail: ContinueReading | null;
	private revision = 0;
	private sequence = 0;
	private listeners = new Set<() => void>();

	constructor(
		private readonly storage: SyncStringStorage,
		private readonly hubId: string,
		private readonly clock: () => number = Date.now,
	) {
		this.documents = parseDocuments(readJson(storage, documentsKey(hubId)));
		this.trail = parseTrail(readJson(storage, trailKey(hubId)));
	}

	position(key: DocumentKey): ReadingPosition | null {
		return this.documents[recordKey(key)]?.position ?? null;
	}

	lastRead(key: DocumentKey): LastRead | null {
		return this.documents[recordKey(key)]?.lastRead ?? null;
	}

	comments(key: DocumentKey): readonly DocumentComment[] {
		return this.documents[recordKey(key)]?.comments ?? [];
	}

	/** Where you are, as you scroll. */
	savePosition(key: DocumentKey, position: ReadingPosition): void {
		this.update(key, (record) => ({ ...record, position }));
	}

	/** You opened a document: the Board's way back to it has done its job. */
	opened(key: DocumentKey): void {
		if (this.trail?.sessionRef === key.sessionRef && this.trail.path === key.path) this.setTrail(null);
	}

	/** You left a document: this version becomes the one you last read, and
	 * leaving before its end leaves the Board's Continue reading row. */
	left(key: DocumentKey, leaving: Leaving): void {
		const now = this.clock();
		const updatedAt = leaving.updatedAt === undefined ? {} : { updatedAt: leaving.updatedAt };
		this.update(key, (record) => ({
			...record,
			...(leaving.position ? { position: leaving.position } : {}),
			lastRead: { blocks: [...leaving.blocks], readAt: now, ...updatedAt },
		}));
		const progress = leaving.position?.progress ?? 1;
		if (progress < FINISHED_PROGRESS)
			this.setTrail({
				sessionRef: key.sessionRef,
				path: key.path,
				title: leaving.title,
				sessionTitle: leaving.sessionTitle,
				progress,
				leftAt: now,
				...updatedAt,
			});
		else this.opened(key);
	}

	/** The Board's Continue reading row: the last document you left before its
	 * end, for two hours (spec 7.1). */
	continueReading(): ContinueReading | null {
		return this.trail && this.clock() - this.trail.leftAt < CONTINUE_READING_MS ? this.trail : null;
	}

	addComment(
		key: DocumentKey,
		comment: Pick<DocumentComment, "blockIndex" | "blockHash" | "quote" | "text">,
	): DocumentComment {
		const now = this.clock();
		// The sequence starts over each launch, so skip any id a comment kept
		// from an earlier launch already has.
		const taken = new Set(this.comments(key).map((existing) => existing.id));
		let id: string;
		do {
			this.sequence += 1;
			id = `${now.toString(36)}-${this.sequence.toString(36)}`;
		} while (taken.has(id));
		const added: DocumentComment = { ...comment, id, createdAt: now };
		this.update(key, (record) => ({ ...record, comments: [...(record.comments ?? []), added] }));
		return added;
	}

	removeComment(key: DocumentKey, id: string): void {
		this.update(key, (record) => ({
			...record,
			comments: (record.comments ?? []).filter((comment) => comment.id !== id),
		}));
	}

	/** The review went out: its comments are no longer drafts. */
	clearComments(key: DocumentKey): void {
		this.update(key, (record) => {
			const next = { ...record };
			delete next.comments;
			return next;
		});
	}

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	getRevision = (): number => this.revision;

	private update(key: DocumentKey, change: (record: DocumentRecord) => DocumentRecord): void {
		const id = recordKey(key);
		this.documents[id] = { ...change(this.documents[id] ?? { touchedAt: 0 }), touchedAt: this.clock() };
		const entries = Object.entries(this.documents);
		if (entries.length > LIMIT) {
			// Unsent comments are your work: past the limit, the least recently
			// touched documents without comments go first.
			entries.sort(
				([, a], [, b]) =>
					Number(Boolean(b.comments?.length)) - Number(Boolean(a.comments?.length)) || b.touchedAt - a.touchedAt,
			);
			this.documents = Object.fromEntries(entries.slice(0, LIMIT));
		}
		writeJson(this.storage, documentsKey(this.hubId), this.documents);
		this.changed();
	}

	private setTrail(trail: ContinueReading | null): void {
		this.trail = trail;
		if (trail) writeJson(this.storage, trailKey(this.hubId), trail);
		else {
			// Opening or finishing a document is routine reading activity, not a
			// hub removal: unlike forgetDocuments below, a storage failure here
			// must not interrupt it. The in-memory trail is already cleared;
			// only the stored copy might outlive it.
			try {
				removeKeys(this.storage, [trailKey(this.hubId)]);
			} catch {
				// Best effort, as writeJson above already is for the other branch.
			}
		}
		this.changed();
	}

	private changed(): void {
		this.revision += 1;
		for (const listener of [...this.listeners]) listener();
	}
}

export function forgetDocuments(storage: SyncStringStorage, hubId: string): void {
	removeKeys(storage, [documentsKey(hubId), trailKey(hubId)]);
}
