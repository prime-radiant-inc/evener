import type { BoundText } from "../../projectedRows";
import { MAX_ITEM_BYTES, truncateText } from "../../projectedRows";

export interface BoundedTextPass {
	bound: BoundText;
	/** Makes this pass's strings the cache the next pass reads through. */
	commit(): void;
}

export interface BoundedTextCache {
	/** One publish's bounding pass over the projected rows. */
	begin(): BoundedTextPass;
	/** Drops every cached string. */
	release(): void;
}

// The phone's text bound (MAX_ITEM_BYTES, with the truncation marker) applied
// to each text-bearing field of a published row. Every path that publishes a
// conversation runs a pass, so the bound is a property of what is displayed
// rather than of the sequence of frames that produced it. The model behind the
// rows keeps its full text (#1535 asks whether the package should bound that).
//
// The rows are rebuilt from the model on every frame, but the model hands
// back the SAME string reference for text no frame touched, so each bound
// string is cached under the source string it came from and reused until
// that reference changes: a transcript of settled rows is not re-encoded
// per delta. The cache is rebuilt from the rows of each publish (the
// previous one is read through, then dropped), so it holds only what is on
// screen and needs no invalidation of its own. The one item a delta is
// streaming into does re-encode once per frame, because its text really is
// new each time — #1535 is where that stops growing.
//
// The cache belongs to the conversation whose rows it bound: every string
// in it is held by that conversation's model or its rows, so once the
// conversation is dropped the cache is the only thing retaining them. Every
// transition that drops the conversation releases it. A suspend does not:
// the rows stay on screen for the resume, which republishes the same text.
export function createBoundedTextCache(): BoundedTextCache {
	let cached = new Map<string, string>();
	return {
		begin() {
			const previous = cached;
			const next = new Map<string, string>();
			const bound: BoundText = (text) => {
				const hit = next.get(text) ?? previous.get(text);
				const bounded = hit ?? truncateText(text, MAX_ITEM_BYTES);
				next.set(text, bounded);
				// A published row carries its BOUNDED text, so the next publish binds
				// that string, not the one the model handed over. Bounding a bounded
				// string is a no-op (truncateText never returns more than the limit), so
				// record it as its own answer and the row costs one encode for its life
				// rather than one more on every publish after the first.
				if (bounded !== text) next.set(bounded, bounded);
				return bounded;
			};
			return {
				bound,
				commit() {
					cached = next;
				},
			};
		},
		release() {
			cached = new Map();
		},
	};
}
