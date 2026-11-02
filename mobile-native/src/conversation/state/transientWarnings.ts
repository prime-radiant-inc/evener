import type { WarningParams } from "@evener/appwire-client";
import { foldWarningParams, joinWarningParts } from "@evener/appwire-client";
import type { MobileTimelineItem } from "../../projectedRows";
import { attachmentSourceIdentity, ownTimelineIdentities, timelineIdentity } from "../../projectedRows";
import { arrivalAnchor, splitClusterAtAnchors } from "./rowTransforms";

export interface TransientWarnings {
	/** Records a warning the model cannot hold at the position it arrived at,
	 * and returns the row it displays as. */
	record(params: WarningParams, items: MobileTimelineItem[]): MobileTimelineItem;
	/** The items with every recorded notice seated at its arrival position. */
	seat(items: MobileTimelineItem[]): MobileTimelineItem[];
	/** Moves each notice whose anchor the new rows hide to the nearest row they
	 * still display. */
	reanchor(oldItems: MobileTimelineItem[], newItems: MobileTimelineItem[]): void;
	/** Retires every notice whose row is not among the retained identities. */
	prune(retainedIdentities: ReadonlySet<string>): void;
	clear(): void;
}

// RoboRev round 34: turn-independent warnings — a warning that arrives
// with no active turn — are real server diagnostics the wire drops: the
// reducer has nowhere transcript-true to fold one (its "warning" case
// returns only the liveness restamp when no turn is active), and the
// transcript never persists warnings, so no read can recover what the
// frame carried either. Main's row applier displayed them as live-owned
// rows; this store's rows are a projection of the model, so a row the
// model cannot hold lives here, as transient display state outside it.
// Every timeline rebuild passes through capAndTruncate, the display
// boundary, which seats each notice at the position it arrived at
// (round 35: re-appended at the tail, a notice sank below rows that
// arrived later and the retained window could never evict it), and
// the conversation transitions that clear the page history clear
// these with it, so a notice never crosses threads.
export function createTransientWarnings(): TransientWarnings {
	const notices: {
		row: MobileTimelineItem;
		anchor: string | null;
	}[] = [];
	let liveNoticeSerial = 0;
	// The row an idle warning displays as: the same attention row the
	// canonical projection builds for a model warning item projectedRows.ts's
	// warningItem — kind "failure", title its own field, message and hint
	// joined as detail), under the live serial identity main's applier
	// used, since there is no model item to share an identity with.
	function idleWarningRow(params: WarningParams): MobileTimelineItem {
		const folded = foldWarningParams(params);
		return {
			kind: "failure",
			id: `warning:${(liveNoticeSerial += 1)}`,
			title: folded.title ?? "Warning",
			detail: joinWarningParts([folded.text, folded.hint]),
		};
	}

	function seat(items: MobileTimelineItem[]): MobileTimelineItem[] {
		const carriedIdentities = new Set(items.map(timelineIdentity));
		const noticesByAnchor = new Map<string, MobileTimelineItem[]>();
		const noticesBeforeEverything: MobileTimelineItem[] = [];
		for (const notice of notices) {
			if (carriedIdentities.has(timelineIdentity(notice.row))) continue;
			if (notice.anchor === null) {
				noticesBeforeEverything.push(notice.row);
				continue;
			}
			const bucket = noticesByAnchor.get(notice.anchor);
			if (bucket === undefined) {
				noticesByAnchor.set(notice.anchor, [notice.row]);
			} else {
				bucket.push(notice.row);
			}
		}
		const seated: MobileTimelineItem[] = [...noticesBeforeEverything];
		for (let index = 0; index < items.length; index += 1) {
			const item = items[index];
			seated.push(item);
			// RoboRev round 37: anchors resolve through the identities the row
			// OWNS, not just its own top-level one — pagination can seat an older
			// tool directly beside the one a notice anchored to, and the next
			// projection then clusters the two under the older's identity: the
			// anchored row stays visible as a member while its identity stops
			// matching, which used to retire the notice with the prune. The
			// size guard keeps the common no-notice publish at one check per
			// row.
			if (noticesByAnchor.size === 0) continue;
			const identities = ownTimelineIdentities(item);
			const bucketsByIdentity = new Map<string, MobileTimelineItem[]>();
			for (const identity of identities) {
				const bucket = noticesByAnchor.get(identity);
				if (bucket !== undefined) bucketsByIdentity.set(identity, bucket);
			}
			if (bucketsByIdentity.size === 0) continue;
			// A notice anchored through an attachment row's source identity
			// seats after the attachments that follow the anchored row, never
			// between them: capItems' cap cut drops a leading attachment
			// whose source fell off the cut and only scans a LEADING RUN of
			// attachments, so a notice seated inside that run would become
			// the first retained row at the cut and the orphans behind it
			// would survive — their lingering source identities then make
			// loadOlder's F10 admission rule refuse genuine older page copies
			// of those sources, forever. The run spans every attachment the
			// anchored row OWNS the source of — a clustered activity carries
			// the attachments of all its members, so a notice anchored to one
			// member seats after them all. This is also the position the
			// notice arrived at: the attachments row was the nearest row when
			// it landed (RoboRev panel round 2).
			let attachmentsEnd = index;
			while (attachmentsEnd + 1 < items.length) {
				const next = items[attachmentsEnd + 1];
				const source = attachmentSourceIdentity(next);
				if (source === null || !identities.has(source)) break;
				attachmentsEnd += 1;
			}
			// R38: the cluster grew past the anchored member — split it there
			// so the notice keeps its arrival position above the members that
			// arrived later. Only when no attachment run follows: that notice
			// arrived after the attachments, and the whole-row seat below them
			// is its arrival position.
			if (attachmentsEnd === index && item.kind === "activity") {
				const split = splitClusterAtAnchors(item, bucketsByIdentity);
				if (split !== null) {
					seated.pop();
					seated.push(...split);
					continue;
				}
			}
			for (let attach = index + 1; attach <= attachmentsEnd; attach += 1) {
				seated.push(items[attach]);
			}
			index = attachmentsEnd;
			for (const bucket of bucketsByIdentity.values()) {
				seated.push(...bucket);
			}
		}
		return seated;
	}

	// A level change hides rows without withdrawing them from the model, and
	// the prune in capAndTruncate retires a notice only when its own row left
	// the window. But the seating walk consumes a notice only at a DISPLAYED
	// anchor row, so a notice whose anchor the new level hides never seats
	// and the prune mistakes it for withdrawn. Re-anchor each such notice to
	// the nearest row the new level still displays at or above its arrival
	// position — the closest visible seat, not a retirement — and to null
	// (before everything) when nothing at or above it survives. The anchor
	// only moves up: on switch-back to the richer level the notice sits at
	// its re-anchored position, below rows that arrived after it, the
	// disclosed cost of keeping it visible through the level change.
	function reanchor(oldItems: MobileTimelineItem[], newItems: MobileTimelineItem[]): void {
		if (notices.length === 0) return;
		const displayedIdentities = new Set<string>();
		for (const row of newItems) {
			for (const identity of ownTimelineIdentities(row)) {
				displayedIdentities.add(identity);
			}
		}
		for (const notice of notices) {
			const anchor = notice.anchor;
			if (anchor === null || displayedIdentities.has(anchor)) continue;
			// The anchor's arrival position in the rows leaving the screen.
			let arrival = -1;
			for (let index = 0; index < oldItems.length; index += 1) {
				const row = oldItems[index];
				if (row === undefined) continue;
				for (const identity of ownTimelineIdentities(row)) {
					if (identity === anchor) {
						arrival = index;
						break;
					}
				}
				if (arrival >= 0) break;
			}
			// The nearest displayed row at or above it, re-anchored through
			// one of that row's own displayed identities so the next seating
			// walk resolves the anchor as written.
			let reanchor: string | null = null;
			for (let above = arrival; above >= 0; above -= 1) {
				const candidate = oldItems[above];
				if (candidate === undefined) continue;
				for (const identity of ownTimelineIdentities(candidate)) {
					if (displayedIdentities.has(identity)) {
						reanchor = identity;
						break;
					}
				}
				if (reanchor !== null) break;
			}
			notice.anchor = reanchor;
		}
	}

	return {
		record(params, items) {
			const row = idleWarningRow(params);
			notices.push({
				row,
				anchor: arrivalAnchor(items, new Set(notices.map((notice) => timelineIdentity(notice.row)))),
			});
			return row;
		},
		seat,
		reanchor,
		prune(retainedIdentities) {
			for (let index = notices.length - 1; index >= 0; index -= 1) {
				if (!retainedIdentities.has(timelineIdentity(notices[index].row))) {
					notices.splice(index, 1);
				}
			}
		},
		clear() {
			notices.length = 0;
		},
	};
}
