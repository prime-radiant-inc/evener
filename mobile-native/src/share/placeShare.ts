import { mergeDraftText } from "@evener/appwire-client";
import type { DraftDocument } from "../draftDocument";

/**
 * Puts a share's text in a session's composer (spec: share intent). The write
 * is the same durable draft the composer reads, so opening the session later
 * finds the text; nothing is sent. Returns whether the write landed: a draft
 * that could not be read or saved keeps the share pending, and the caller says
 * so rather than dropping it. Calling again after a failure never appends a
 * second copy: text already in the draft is treated as applied, whatever else
 * was typed around it since.
 */
export function placeSharedText(document: DraftDocument, share: { id: string; text: string }): boolean {
	const text = share.text.trim();
	if (!text) return false;
	const before = document.getSnapshot();
	if (!before.loaded) document.retry();
	const loaded = document.getSnapshot();
	if (!loaded.loaded) return false;
	// Already there from an earlier attempt (saved, unsaved, or edited around):
	// retry the save instead of merging again.
	const merged = loaded.record.draft.includes(text) ? loaded.record.draft : mergeDraftText(loaded.record.draft, text);
	if (merged === loaded.record.draft) document.retry();
	else document.edit(merged);
	const after = document.getSnapshot();
	return !after.error && after.record.draft.includes(text);
}
