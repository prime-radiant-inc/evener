// The Reader's sheets (the outline now; comments and the review later) are
// routes beside it, so they reach the open Reader through this host, keyed by
// sheetKey(hubId, sessionRef, path).
import { sheetHosts } from "../sheet/sheetHosts";
import type { OutlineEntry } from "./documentBlocks";

export interface ReaderHost {
	/** The document's headings. */
	outline: readonly OutlineEntry[];
	/** Scrolls a block to the top of the Reader, animated. */
	jumpTo(index: number): void;
}

export const readerHosts = sheetHosts<ReaderHost>();
