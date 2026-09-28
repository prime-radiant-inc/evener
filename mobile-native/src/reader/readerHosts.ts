// The Reader's sheets (the outline, the comments, the review) are
// routes beside it, so they reach the open Reader through this host, keyed by
// sheetKey(hubId, sessionRef, path).
import { sheetHosts } from "../sheet/sheetHosts";
import type { OutlineEntry } from "./documentBlocks";
import type { DocumentComment } from "./documentMemory";

export interface ReaderHost {
	/** The document's headings. */
	outline: readonly OutlineEntry[];
	/** Scrolls a block to the top of the Reader, animated. */
	jumpTo(index: number): void;
	/** The block a comment's marker sits on now, or null once its words
	 * changed (ruling 14). */
	anchor(comment: DocumentComment): number | null;
	/** Whether the review's session can take a message now: its read
	 * advertises send or queue. A running subagent's can't (ruling 30). */
	canReview: boolean;
}

export const readerHosts = sheetHosts<ReaderHost>();
