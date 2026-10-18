import { mergeDraftText } from "@evener/appwire-client";
import type { DraftDocument } from "../draftDocument";

/**
 * Puts a share's text in a session's composer (spec: share intent). The write
 * is the same durable draft the composer reads, so opening the session later
 * finds the text; nothing is sent. Returns whether the write landed: a draft
 * that could not be read or saved keeps the share pending, and the caller says
 * so rather than dropping it.
 */
export function placeSharedText(document: DraftDocument, text: string): boolean {
	const before = document.getSnapshot();
	if (!before.loaded) return false;
	document.edit(mergeDraftText(before.record.draft, text));
	const after = document.getSnapshot();
	return !after.error && after.record.draft !== before.record.draft;
}
