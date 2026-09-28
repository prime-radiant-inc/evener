// Changes since you last read (spec 10.2; S9's fallback, ruling 13), and
// where a comment's marker or a remembered position lands in a document that
// changed since (ruling 14).
import type { DocumentBlock } from "./documentBlocks";

/** A place in a document: a block, or a code file's line. */
export type Place = Pick<DocumentBlock, "index" | "hash">;

/** The blocks whose words weren't in the version you last read. A repeated
 * block counts once per copy you read. A first read has no changes. */
export function changedBlocks(blocks: readonly DocumentBlock[], lastRead: readonly string[] | null): number[] {
	if (lastRead === null) return [];
	const remaining = new Map<string, number>();
	for (const hash of lastRead) remaining.set(hash, (remaining.get(hash) ?? 0) + 1);
	const changed: number[] = [];
	for (const block of blocks) {
		const left = remaining.get(block.hash) ?? 0;
		if (left > 0) remaining.set(block.hash, left - 1);
		else changed.push(block.index);
	}
	return changed;
}

const WEEKDAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

function startOfDay(date: Date): number {
	return new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
}

/** When you last read it, in words that can't be mistaken for a first read,
 * by this phone's calendar. Days are rounded, so a daylight-saving change
 * doesn't shift them. */
export function whenYouRead(readAt: number, now: number): string {
	const read = new Date(readAt);
	const days = Math.round((startOfDay(new Date(now)) - startOfDay(read)) / 86_400_000);
	if (days <= 0) return "earlier today";
	if (days === 1) return "yesterday";
	if (days < 7) return `on ${WEEKDAYS[read.getDay()]}`;
	return `on ${MONTHS[read.getMonth()]} ${read.getDate()}`;
}

/** The caption's change note: "3 changes since you read it yesterday". */
export function changesCaption(count: number, readAt: number, now: number): string | null {
	if (count <= 0) return null;
	return `${count} ${count === 1 ? "change" : "changes"} since you read it ${whenYouRead(readAt, now)}`;
}

/** Where a comment's marker goes now: the block with the words it was left
 * on, the copy nearest its old place if they repeat, or nowhere once the
 * words changed. The comment itself keeps its quote either way. */
export function anchorBlock(
	anchor: { blockHash: string; blockIndex: number },
	blocks: readonly Place[],
): number | null {
	let best: number | null = null;
	for (const block of blocks)
		if (
			block.hash === anchor.blockHash &&
			(best === null || Math.abs(block.index - anchor.blockIndex) < Math.abs(best - anchor.blockIndex))
		)
			best = block.index;
	return best;
}

/** Where to reopen: the remembered block if it's still there, its words if
 * they moved, else the nearest place at the block's top. */
export function restoreBlock(
	position: { blockIndex: number; blockHash: string; offset: number },
	blocks: readonly Place[],
): { index: number; offset: number } | null {
	if (blocks.length === 0) return null;
	if (blocks[position.blockIndex]?.hash === position.blockHash)
		return { index: position.blockIndex, offset: position.offset };
	const moved = anchorBlock(position, blocks);
	if (moved !== null) return { index: moved, offset: position.offset };
	return { index: Math.min(position.blockIndex, blocks.length - 1), offset: 0 };
}
