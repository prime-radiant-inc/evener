import { mergeDraftText } from "@evener/appwire-client";
import type { DraftDocument } from "../draftDocument";

/** The merge each document is still trying to land. A failed save keeps the
 * merged text in the document, so a retry must write that same text, not merge
 * the share a second time on top of it. */
const pending = new WeakMap<DraftDocument, string>();

/**
 * Puts a share's text in a session's composer (spec: share intent). The write
 * is the same durable draft the composer reads, so opening the session later
 * finds the text; nothing is sent. Returns whether the write landed: a draft
 * that could not be read or saved keeps the share pending, and the caller says
 * so rather than dropping it. Calling again after a failure retries the same
 * merge instead of appending a copy.
 */
export function placeSharedText(document: DraftDocument, text: string): boolean {
	const before = document.getSnapshot();
	if (!before.loaded) document.retry();
	const loaded = document.getSnapshot();
	if (!loaded.loaded) return false;
	const attempted = pending.get(document);
	const merged = attempted ?? mergeDraftText(loaded.record.draft, text);
	if (attempted === undefined) {
		pending.set(document, merged);
		document.edit(merged);
	} else {
		// The earlier attempt's text is still in the draft, unsaved or saved;
		// retrying the document's own save lands it once.
		document.retry();
	}
	const after = document.getSnapshot();
	const landed = !after.error && after.record.draft === merged;
	if (landed) pending.delete(document);
	return landed;
}
