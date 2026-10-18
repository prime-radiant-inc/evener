// The input that opened the exchange a failed turn ended, for the error row's
// Retry (spec 8.2, ruling 26): re-issuing the person's own words and images,
// never a sentence put in their mouth. A LIVE failure keeps the input in its
// own turn, where the search finds it at once. A RELOADED one does not: one
// persisted transcript entry becomes one turn (apptranscript.go's ProjectTurn),
// so a failure entry is a turn holding only the failure, and the input sits in
// an earlier one. The lookback stops AT the failed turn: a later prompt is a
// different exchange, and re-issuing it would answer a question the reader did
// not ask. Mirrors the web's TurnFailureEndCap.tsx, over the same shared
// ThreadModel.
import {
	type InputAttachment,
	type ItemImage,
	type ItemModel,
	markerPattern,
	type ThreadModel,
	type TurnModel,
	translateAttachmentMarkers,
} from "@evener/appwire-client";

// The input a Retry re-issues: composer-style text with "[image N]" anchors
// (send re-translates anchors to prose on the wire, and the outbox record keeps
// anchor text as composerText, so a retry that itself fails into recovery
// rebuilds a composer whose tiles still have their anchors), plus the images
// whose bytes survived, and how many images the originating item carried.
export interface RetryInput {
	text: string;
	attachments?: InputAttachment[];
	sourceImageCount: number;
}

// What the lookback found: something re-issuable, or an image-only input whose
// bytes did not survive (a sha-routed src with no inline data, e.g. a reloaded
// turn). The latter STOPS the lookback rather than falling through to an older,
// unrelated text prompt: re-issuing that would answer a question the reader did
// not ask.
export type OriginatingInput =
	| { kind: "retry"; input: RetryInput }
	| { kind: "images-unavailable"; sourceImageCount: number };

interface ImageOccurrence {
	marker: number;
	name?: string;
	start: number;
	end: number;
}

// parseOccurrences finds the "(attached image N[: name])" prose the submit
// boundary wrote (attachmentMarkers.ts). A filename may itself contain ")", so
// the name is not simply "up to the first closing paren": after the "N: "
// prefix, a known attachment name followed by ")" wins. But when SEVERAL known
// names fit — filenames that are prefixes of one another — the pairing must NOT
// guess: the mention parses with no name, so it falls into the positional
// unnamed path, whose exact-count/order guards refuse rather than misassign
// bytes. Only one fitting name is a safe claim. With no fitting name at all the
// span falls back to the raw text up to the first ")", which keeps foreign prose
// verbatim. An absent (or empty) name is undefined, matching the wire shape
// where an unnamed attachment's marker translates with no name clause.
function parseOccurrences(text: string, knownNames: (string | undefined)[]): ImageOccurrence[] {
	const names = knownNames.filter((name): name is string => name !== undefined && name !== "");
	const prefix = /\(attached image (\d+)/g;
	const occurrences: ImageOccurrence[] = [];
	let match = prefix.exec(text);
	while (match !== null) {
		const marker = Number(match[1]);
		const cursor = match.index + match[0].length;
		const rest = text.slice(cursor);
		if (rest.startsWith(")")) {
			// Unnamed markers never extend: the wire emits no filename tail for
			// them, so anything glued after the ")" is user text that must
			// survive verbatim. Tail consumption below applies only to named
			// mentions, where a lost filename can leave one behind.
			occurrences.push({ marker, start: match.index, end: cursor + 1 });
			prefix.lastIndex = cursor + 1;
		} else if (rest.startsWith(":")) {
			const afterColon = rest.slice(1).startsWith(" ") ? rest.slice(2) : rest.slice(1);
			const base = cursor + (rest.length - afterColon.length);
			const fitting = names.filter((name) => afterColon.startsWith(`${name})`));
			if (fitting.length === 1) {
				const best = fitting[0] as string;
				occurrences.push({ marker, name: best, start: match.index, end: base + best.length + 1 });
				prefix.lastIndex = base + best.length + 1;
			} else if (fitting.length > 1) {
				// Prefix-colliding filenames: no name, so the mention joins the
				// positional unnamed path instead of claiming bytes it may not
				// own. The span still extends through the same tight tail the
				// nameless branch consumes, so markerOnly sees one span.
				const spanEnd = base + extendSpanEnd(afterColon, afterColon.indexOf(")"));
				occurrences.push({ marker, start: match.index, end: spanEnd });
				prefix.lastIndex = spanEnd;
			} else {
				const close = afterColon.indexOf(")");
				if (close === -1) {
					prefix.lastIndex = match.index + 1;
				} else {
					const rawName = afterColon.slice(0, close);
					const spanEnd = base + extendSpanEnd(afterColon, close);
					occurrences.push({
						marker,
						...(rawName ? { name: rawName } : {}),
						start: match.index,
						end: spanEnd,
					});
					prefix.lastIndex = spanEnd;
				}
			}
		} else {
			prefix.lastIndex = match.index + 1;
		}
		match = prefix.exec(text);
	}
	return occurrences;
}

// extendSpanEnd consumes a filename tail after a marker's closing ")": while a
// later ")" is reachable whose middle holds no parens, the span extends through
// it. This is deliberately greedy: the filename is gone after reload, so any
// paren-free tail MIGHT be its remainder (filenames may hold ")", ") ", or a
// trailing ")"), and guessing "user prose" would resend marker text without the
// image bytes. A hop whose middle holds a paren is user prose, and the span
// stops: consuming it would swallow words into the marker and misclassify the
// input as image-only.
function extendSpanEnd(text: string, closeParen: number): number {
	const isTailHop = (middle: string): boolean => middle !== "" && !/[()]/.test(middle);
	let end = closeParen + 1;
	while (text[end - 1] === ")" && end < text.length) {
		const next = text.indexOf(")", end);
		if (next === -1) break;
		// An empty hop ("a))") is only a filename tail when another ")" follows
		// it or it ends the text: otherwise the second paren may simply close
		// user prose such as "(see (the plot))", and consuming it swallows their
		// words.
		const middle = text.slice(end, next);
		if (middle === "" ? text[next + 1] !== ")" && next + 1 !== text.length : !isTailHop(middle)) break;
		end = next + 1;
	}
	return end;
}

// occurrencesCoverText reports whether every non-whitespace character of text
// sits inside a parsed marker occurrence: the text is marker prose alone, with
// no user words of its own.
function occurrencesCoverText(text: string, occurrences: { start: number; end: number }[]): boolean {
	if (occurrences.length === 0) return text === "";
	let cursor = 0;
	const sorted = [...occurrences].sort((left, right) => left.start - right.start);
	for (const occurrence of sorted) {
		if (text.slice(cursor, occurrence.start).trim() !== "") return false;
		cursor = occurrence.end;
	}
	return text.slice(cursor).trim() === "";
}

// planRetryImages recovers the input's image bytes from the model's display
// shape (reducer.ts resolves the wire's inline bytes to a data: URI src, which
// is what a live userMessage item carries). Each byte-carrying image becomes an
// InputAttachment the send path already knows how to wire (buildComposerInput).
// Images whose bytes are unavailable (a sha-routed src with no inline data) are
// dropped: resending a name with no bytes would fabricate an attachment.
//
// Marker pairing is by attachment NAME first: markers are stable composer ids
// while the attachment array is acceptance order, so positional pairing corrupts
// identity when the user reordered markers in the text. Occurrences without a
// name match fall back to positional pairing over the leftovers, but only when
// their markers run in the same order as the images: an out-of-order (or
// repeated) unnamed marker sequence means the true marker-to-bytes identity was
// lost, and the pairing refuses rather than misassigning bytes. Images with no
// occurrence keep a fallback marker. A named occurrence that matches no image at
// all is foreign (user-typed prose, not a translated marker) and is never
// consumed.
//
// The returned text rewrites each resolvable occurrence back to its "[image N]"
// composer anchor: send re-translates anchors to prose on the wire (an exact
// round-trip), while the outbox record keeps anchor text as composerText, so a
// failed retry recovers tiled images instead of orphaned prose. An occurrence
// whose image has no bytes is left verbatim: rewriting it would put a raw
// marker on the wire.
//
// ambiguous is true when the pairing cannot be uniquely determined, and the
// caller must refuse the images rather than guess: a name claimed under several
// DIFFERENT markers, several attachments matching one name, or two attachments
// sharing one ASSIGNED marker means the true marker-to-bytes identity was lost
// on the wire. It also covers the belt-and-braces cases: anchor text that does
// not re-translate back to the stored prose byte-for-byte, which proves the
// pairing corrupted something, and duplicate assigned markers, which the
// round-trip cannot see for unnamed images.
function planRetryImages(
	images: ItemImage[] | undefined,
	text: string,
): { attachments: InputAttachment[]; anchorText: string; ambiguous: boolean; markerOnly: boolean } {
	const items = images ?? [];
	const occurrences = parseOccurrences(
		text,
		items.map((image) => image.name),
	);
	const decoded = items.map((image) => {
		const dataUri = /^data:([^;,]+)?;base64,(.*)$/s.exec(image.src);
		if (!dataUri) return undefined;
		return { mediaType: dataUri[1] || "image/png", data: dataUri[2] ?? "", name: image.name };
	});
	const markerForImage: (number | undefined)[] = items.map(() => undefined);
	const rewriteOccurrence: boolean[] = occurrences.map(() => false);
	const claimedImage = items.map(() => false);
	const byName = new Map<string, number[]>();
	items.forEach((image, index) => {
		if (image.name === undefined || image.name === "") return;
		const list = byName.get(image.name) ?? [];
		list.push(index);
		byName.set(image.name, list);
	});
	// Only names participating on both sides can conflict: one name under
	// several markers, or several attachments under one name. Anything else -
	// foreign prose, unnamed images, a lone repeated mention - pairs exactly.
	const markersByName = new Map<string, Set<number>>();
	occurrences.forEach((occurrence) => {
		if (occurrence.name === undefined || !byName.has(occurrence.name)) return;
		const markers = markersByName.get(occurrence.name) ?? new Set<number>();
		markers.add(occurrence.marker);
		markersByName.set(occurrence.name, markers);
	});
	let ambiguous = false;
	for (const [name, markers] of markersByName) {
		if (markers.size > 1 || (byName.get(name)?.length ?? 0) > 1) {
			ambiguous = true;
			break;
		}
	}
	const pendingOccurrences: number[] = [];
	occurrences.forEach((occurrence, occurrenceIndex) => {
		if (occurrence.name !== undefined && !byName.has(occurrence.name)) return;
		const candidate =
			occurrence.name === undefined
				? undefined
				: (byName.get(occurrence.name) ?? []).find((index) => !claimedImage[index]);
		if (candidate === undefined) {
			pendingOccurrences.push(occurrenceIndex);
			return;
		}
		claimedImage[candidate] = true;
		markerForImage[candidate] = occurrence.marker;
		if (decoded[candidate] !== undefined) rewriteOccurrence[occurrenceIndex] = true;
	});
	const unpairedImages: number[] = [];
	items.forEach((_, index) => {
		if (!claimedImage[index]) unpairedImages.push(index);
	});
	// Unnamed occurrences pair positionally with the leftover UNNAMED images in
	// order: the nth unnamed mention takes the nth unclaimed unnamed image.
	// Named images left unclaimed (their marker was removed) sit out of the
	// positional pairing entirely and take collision-free fallback markers
	// below. That pairing is only sound when the markers run in the same order
	// as the images — an out-of-order (or gapped) marker sequence means the
	// true marker-to-bytes identity was lost, and guessing would attach the
	// wrong bytes to a tile.
	// A repeated NAMED mention whose image is already claimed is the same claim
	// restated, not a new pairing: it stays unpaired (verbatim prose) rather
	// than consuming an unrelated leftover image under a duplicate marker.
	// A FOREIGN named mention (no attachment carries the name) only disturbs
	// the pairing when positional pairing is actually performed: the foreign
	// prose may be occupying the slot the real marker would have taken, so
	// positional alignment cannot be trusted and the pairing refuses. With no
	// positional pairing to disturb, foreign prose is just user words left
	// verbatim while the images ride fallback markers.
	let positionalAmbiguous = false;
	const unclaimedUnnamed = unpairedImages.filter(
		(index) => items[index]?.name === undefined || items[index]?.name === "",
	);
	const pendingUnnamed: number[] = [];
	pendingOccurrences.forEach((occurrenceIndex) => {
		const occurrence = occurrences[occurrenceIndex];
		if (occurrence !== undefined && occurrence.name === undefined) pendingUnnamed.push(occurrenceIndex);
	});
	const needsPositionalPairing = pendingUnnamed.length > 0 && unclaimedUnnamed.length > 0;
	const hasForeignMention = occurrences.some(
		(occurrence) => occurrence.name !== undefined && !byName.has(occurrence.name),
	);
	if (needsPositionalPairing && hasForeignMention) {
		positionalAmbiguous = true;
	}
	// Repeated copies of one unnamed marker are the same anchor restated, not a
	// new pairing: the marker translator maps every "[image N]" to the one
	// attachment staged under N. Collapse repeats to their first copy before
	// the order/cardinality checks, so "(attached image 1) and (attached image
	// 1)" over one unnamed image pairs instead of tripping the non-increasing
	// check. Extra copies stay unpaired (verbatim prose) rather than consuming
	// further images under a duplicate marker.
	const seenUnnamed = new Set<number>();
	const distinctUnnamed: number[] = [];
	pendingUnnamed.forEach((occurrenceIndex) => {
		const marker = (occurrences[occurrenceIndex] as ImageOccurrence).marker;
		if (seenUnnamed.has(marker)) return;
		seenUnnamed.add(marker);
		distinctUnnamed.push(occurrenceIndex);
	});
	const distinctMarkers = distinctUnnamed.map(
		(occurrenceIndex) => (occurrences[occurrenceIndex] as ImageOccurrence).marker,
	);
	distinctMarkers.forEach((marker, position) => {
		if (position > 0 && marker <= distinctMarkers[position - 1]) {
			positionalAmbiguous = true;
		}
	});
	// Positional pairing is exact one-to-one or nothing: the unnamed mention
	// count must EQUAL the unclaimed unnamed image count. A SUBSET cannot be
	// aligned — the nth mention is not necessarily the nth image — and a
	// SUPERSET is worse: pairing the first mentions positionally relabels the
	// attachments, and the round-trip cannot see it.
	if (!positionalAmbiguous && distinctUnnamed.length > 0 && distinctUnnamed.length !== unclaimedUnnamed.length) {
		positionalAmbiguous = true;
	}
	if (positionalAmbiguous) {
		ambiguous = true;
	} else {
		distinctUnnamed.forEach((occurrenceIndex, position) => {
			const imageIndex = unclaimedUnnamed[position];
			if (imageIndex === undefined) return;
			const occurrence = occurrences[occurrenceIndex];
			if (occurrence === undefined) return;
			claimedImage[imageIndex] = true;
			markerForImage[imageIndex] = occurrence.marker;
			if (decoded[imageIndex] !== undefined) rewriteOccurrence[occurrenceIndex] = true;
		});
	}
	const attachments: InputAttachment[] = [];
	// Fallback markers come from the unused set, never positional indexes: an
	// index+1 can collide with a real marker when earlier markers were removed
	// or reordered, shadowing an attachment in the translation map. Raw
	// "[image N]" anchors already in the stored text are reserved too: the user
	// may have typed one literally, and a fallback reusing its number would
	// capture it at send translation time, corrupting their prose.
	const usedMarkers = new Set<number>();
	occurrences.forEach((occurrence) => {
		usedMarkers.add(occurrence.marker);
	});
	Array.from(text.matchAll(markerPattern())).forEach((match) => {
		usedMarkers.add(Number(match[1]));
	});
	const allocFallbackMarker = (): number => {
		let marker = 1;
		while (usedMarkers.has(marker)) marker += 1;
		usedMarkers.add(marker);
		return marker;
	};
	items.forEach((_, index) => {
		const bytes = decoded[index];
		if (bytes === undefined) return;
		attachments.push({
			marker: markerForImage[index] ?? allocFallbackMarker(),
			mediaType: bytes.mediaType,
			data: bytes.data,
			...(bytes.name ? { name: bytes.name } : {}),
		});
	});
	let anchorText = "";
	let cursor = 0;
	occurrences.forEach((occurrence, occurrenceIndex) => {
		anchorText += text.slice(cursor, occurrence.start);
		anchorText += rewriteOccurrence[occurrenceIndex]
			? `[image ${occurrence.marker}]`
			: text.slice(occurrence.start, occurrence.end);
		cursor = occurrence.end;
	});
	anchorText += text.slice(cursor);
	if (!ambiguous && attachments.length > 0) {
		const assignedMarkers = new Set<number>();
		let duplicateMarker = false;
		items.forEach((_, index) => {
			const marker = markerForImage[index];
			if (marker === undefined || decoded[index] === undefined) return;
			if (assignedMarkers.has(marker)) duplicateMarker = true;
			assignedMarkers.add(marker);
		});
		if (duplicateMarker || translateAttachmentMarkers(anchorText, attachments) !== text) {
			ambiguous = true;
		}
	}
	return { attachments, anchorText, ambiguous, markerOnly: occurrencesCoverText(text, occurrences) };
}

// The turn that failed opened with the user's own input as its first item
// (EventUserInput opens a turn, then inserts the userMessage, before the agent
// works in that same turn), so it is the honest thing to re-issue on retry.
function retryItem(turn: TurnModel): ItemModel | undefined {
	return turn.items.find((it) => it.type === "userMessage");
}

function originFromItem(item: ItemModel): OriginatingInput | undefined {
	const text = item.text.trim();
	const sourceImageCount = item.images?.length ?? 0;
	const plan = planRetryImages(item.images, text);
	// Text that is nothing but translated attachment markers is not user prose:
	// a reloaded image-only submission keeps its "(attached image N[: name])"
	// spans verbatim, so the stored text is nonempty while carrying no words of
	// the user's own. Classify it as image-only: with bytes it retries (the
	// spans rebuild anchors), without bytes there is nothing honest to re-send.
	const markerOnly = sourceImageCount > 0 && plan.markerOnly;
	const effectiveText = markerOnly ? "" : text;
	// An image-only input is retryable: buildInput and the server both accept
	// empty text with attachments. Text is required only when there is nothing
	// else to send. An ambiguous pairing refuses the images instead of guessing:
	// text still retries, while an image-only input has nothing left to send.
	if (effectiveText || plan.attachments.length > 0) {
		if (plan.ambiguous) {
			if (!effectiveText) return { kind: "images-unavailable", sourceImageCount };
			return { kind: "retry", input: { text: effectiveText, sourceImageCount } };
		}
		return {
			kind: "retry",
			input: {
				text: plan.anchorText,
				...(plan.attachments.length > 0 ? { attachments: plan.attachments } : {}),
				sourceImageCount,
			},
		};
	}
	// Effectively empty text with images whose bytes are gone: the originating
	// input exists but cannot be re-issued. Report it (stopping the lookback)
	// rather than skipping on to an older prompt.
	if (sourceImageCount > 0) return { kind: "images-unavailable", sourceImageCount };
	return undefined;
}

function findOriginatingItem(turns: TurnModel[], turnId: string): ItemModel | undefined {
	const found = turns.findIndex((t) => t.id === turnId);
	const from = found === -1 ? turns.length - 1 : found;
	for (let i = from; i >= 0; i--) {
		const turn = turns[i];
		const item = turn && retryItem(turn);
		if (!item) continue;
		// A turn carrying the user's input always decides the lookback: a
		// retryable input returns, and an image-only input with lost bytes stops
		// it rather than yielding to an older prompt. Only a turn with no usable
		// input at all (whitespace-only text, no images) keeps looking backwards.
		// MUST match originFromItem's usable/empty verdict — a hot-path copy that
		// never builds the payload.
		if (item.text.trim() || (item.images?.length ?? 0) > 0) return item;
	}
	return undefined;
}

/** The input that opened the exchange `turnId` ended: something Retry may
 * re-issue, or the reason it may not (bytes that did not survive), or undefined
 * when no turn at or before the failure holds usable input. */
export function originatingInput(turns: ThreadModel["turns"], turnId: string): OriginatingInput | undefined {
	const item = findOriginatingItem(turns, turnId);
	return item && originFromItem(item);
}
