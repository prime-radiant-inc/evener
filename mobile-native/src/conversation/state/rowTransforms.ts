import type { ItemModel, TurnModel } from "@evener/appwire-client";
import { copyItemTextPresence, ITEM_OMISSION_TOLERANT_FIELDS, itemTextPresence } from "@evener/appwire-client";
import type { ActivityMember, MobileTimelineItem } from "../../projectedRows";
import { activityIdentity, attachmentSourceIdentity, timelineIdentity } from "../../projectedRows";

// See applyThreadNotification's own comment: an identity `previous` held
// under one turn that this frame just reissued under ANOTHER turn stays
// positioned at its OLD turn, beside its other neighbors there, not
// wherever the live frame's own (often freshly-opened) turn happens to
// sort — absorbing the reissue's fields (ITEM_OMISSION_TOLERANT_FIELDS, the
// same omission-tolerant fallback rehydrate's own post-merge stripping
// pass reads) there, and the copy the reducer's own per-turn merge just
// landed at the new turn drops. loadOlder and rehydrate call this same
// function on their own merge output for the identical reason (this
// file's own doc comments on those callers). An identity untouched by
// this frame, or one already reconciled in place (both locations agree),
// is left alone.
export function reconcileCrossTurnReissues(
	previous: readonly TurnModel[],
	turns: readonly TurnModel[],
): readonly TurnModel[] {
	const heldTurnByIdentity = new Map<string, string>();
	for (const turn of previous) {
		for (const item of turn.items) {
			heldTurnByIdentity.set(item.transcriptKey ?? item.id, turn.id);
		}
	}
	if (heldTurnByIdentity.size === 0) return turns;
	const locationsByIdentity = new Map<string, string[]>();
	const itemByLocation = new Map<string, ItemModel>();
	for (const turn of turns) {
		for (const item of turn.items) {
			const identity = item.transcriptKey ?? item.id;
			const locations = locationsByIdentity.get(identity);
			if (locations) locations.push(turn.id);
			else locationsByIdentity.set(identity, [turn.id]);
			itemByLocation.set(`${turn.id}\u0000${identity}`, item);
		}
	}
	const reissues = new Map<string, { oldTurn: string; newTurn: string }>();
	for (const [identity, locations] of locationsByIdentity) {
		if (locations.length < 2) continue;
		const oldTurn = heldTurnByIdentity.get(identity);
		if (oldTurn === undefined || !locations.includes(oldTurn)) continue;
		const newTurn = locations.find((turnId) => turnId !== oldTurn);
		if (newTurn !== undefined) reissues.set(identity, { oldTurn, newTurn });
	}
	if (reissues.size === 0) return turns;
	let changed = false;
	const result = turns.map((turn) => {
		let turnChanged = false;
		let items = turn.items.filter((item) => {
			const reissue = reissues.get(item.transcriptKey ?? item.id);
			const drop = reissue !== undefined && reissue.newTurn === turn.id;
			if (drop) turnChanged = true;
			return !drop;
		});
		items = items.map((item) => {
			const identity = item.transcriptKey ?? item.id;
			const reissue = reissues.get(identity);
			if (reissue === undefined || reissue.oldTurn !== turn.id) return item;
			const fresh = itemByLocation.get(`${reissue.newTurn}\u0000${identity}`);
			if (fresh === undefined) return item;
			const merged = applyCrossTurnFallback(fresh, item);
			turnChanged = true;
			return merged;
		});
		if (!turnChanged) return turn;
		changed = true;
		return { ...turn, items };
	});
	return changed ? result : turns;
}

// A reissue is authoritative for every field it explicitly carries; an
// omitted omission-tolerant field, or an omitted text field, falls back to
// the superseded item's value instead of clearing it (the same rule
// reducer.ts's private mergeReplacedItem applies within one turn — see
// that function's own comment for why).
function applyCrossTurnFallback(fresh: ItemModel, held: ItemModel): ItemModel {
	let merged = fresh;
	for (const field of ITEM_OMISSION_TOLERANT_FIELDS) {
		const heldValue = (held as unknown as Record<string, unknown>)[field];
		if (heldValue === undefined || (fresh as unknown as Record<string, unknown>)[field] !== undefined) continue;
		if (merged === fresh) merged = { ...fresh };
		(merged as unknown as Record<string, unknown>)[field] = heldValue;
	}
	if (itemTextPresence(fresh) === "omitted" && itemTextPresence(held) === "provided") {
		merged = copyItemTextPresence(
			fresh,
			merged === fresh ? { ...fresh, text: held.text } : { ...merged, text: held.text },
		);
	}
	return merged;
}

// The position a notice arrived at: the identity of the last MODEL row
// on screen when the notice landed, or null when the timeline held
// nothing model-backed (the notice predates every row the model has
// produced since). Seated notices do not qualify (round 36: the second
// of two back-to-back notices anchored to the first, but the seating
// walk matches anchors against model rows only, so that notice never
// seated and the prune deleted it) — consecutive notices stack at the
// same arrival position in arrival order instead, and leave it
// together. An attachment row's own identity is GENERATED from its
// source's wire id, which a reissue (the same source under a new wire
// id, transcript key standing) re-keys — an anchor to it matched
// nothing after the reissue and the next rebuild pruned the notice
// (RoboRev panel round 2) — so attachment rows anchor through their
// STABLE source identity (attachmentSourceIdentity), which the reissue
// keeps. The seating walk resolves that anchor on the source row and
// seats the notice after the attachments that follow it (never between
// the pair — see capAndTruncate), so the notice keeps the position it
// arrived at, after the attachments row that was the nearest row then.
// RoboRev review round 1: a notice arriving over a CLUSTER anchors to
// the LAST member's identity, never the row's own top-level one — the
// top-level identity belongs to the FIRST member, and the R38 split
// resolves it there, lifting the notice above the run's later members
// the very moment it arrives. Every member of the displayed run was on
// screen when the notice landed, so the whole run stays above it; only
// members that join the run LATER split below it.
function arrivalAnchorIdentity(item: MobileTimelineItem): string {
	const source = attachmentSourceIdentity(item);
	if (source !== null) return source;
	if (item.kind === "activity" && item.members) {
		const last = item.members[item.members.length - 1];
		if (last !== undefined) return activityIdentity(last);
	}
	return timelineIdentity(item);
}

export function arrivalAnchor(items: MobileTimelineItem[], noticeIdentities: ReadonlySet<string>): string | null {
	for (let index = items.length - 1; index >= 0; index -= 1) {
		const item = items[index];
		const identity = arrivalAnchorIdentity(item);
		if (!noticeIdentities.has(identity)) return identity;
	}
	return null;
}

// The transient notices rejoin the timeline here, each seated at
// the position it arrived at (after its anchor's row) rather than
// at the tail: a notice is evidence of a moment, so it stays above
// the rows that arrived later, and the cap that slides past its
// position evicts it with it — the notice enters the window as a
// row like any other. A rebuild whose input rows already carry one
// — loadOlder merges the current items — must not duplicate it (the
// carried filter); a notice whose anchor left the model rows (a
// withdrawal, a reread window that starts past it) has no position
// to sit at and retires with the prune; and the prune back to what
// the cap kept means an evicted notice stays evicted.
// RoboRev review round 4: the seating is extracted so loadOlder can
// read the same window the publish will show — a notice consumes a
// cap slot, so the window the retained-turn bound trims against must
// count it.
// R38: the row a sub-run of a split cluster re-projects as — the same
// shape clusterActivityRun builds: a run of one is the member's own
// row, a longer run is that row with the run's aggregate state and the
// members carried for the renderer that expands them.
function activityRunRow(run: ActivityMember[]): Extract<MobileTimelineItem, { kind: "activity" }> | null {
	const first = run[0];
	if (first === undefined) return null;
	const row: Extract<MobileTimelineItem, { kind: "activity" }> = {
		kind: "activity",
		id: first.id,
		label: first.label,
		family: first.family,
		state: first.state,
		detail: first.detail,
		...(first.summaryOnly ? { summaryOnly: first.summaryOnly } : {}),
		...(first.transcriptKey ? { transcriptKey: first.transcriptKey } : {}),
		...(first.position ? { position: first.position } : {}),
		...(first.turnId ? { turnId: first.turnId } : {}),
	};
	if (run.length === 1) return row;
	return {
		...row,
		state: run.some((member) => member.state === "running") ? "running" : "completed",
		members: [...run],
	};
}

// R38: a cluster that absorbed the row a notice anchored to AND grew
// past it — later same-family members arrived after the notice — splits
// at the anchored member, so the notice seats at the position it
// arrived at, above the members that arrived later. An anchor on the
// LAST member needs no split (the whole-row seat already sits below
// it), so null keeps that seat. The caller only offers a cluster with
// no attachment run behind it: a notice anchored through an
// attachment's source identity arrived after those attachments, and a
// split would lift it above them.
export function splitClusterAtAnchors(
	item: Extract<MobileTimelineItem, { kind: "activity" }>,
	bucketsByIdentity: ReadonlyMap<string, MobileTimelineItem[]>,
): MobileTimelineItem[] | null {
	const members = item.members;
	if (members === undefined) return null;
	// Only a member with members AFTER it splits the cluster: an anchor on
	// the last member seats after the whole row as before. The common
	// anchored publish answers that without allocating the walk.
	let splitsBeforeLastMember = false;
	for (let index = 0; index < members.length - 1; index += 1) {
		const member = members[index];
		if (member === undefined) continue;
		if (bucketsByIdentity.get(activityIdentity(member)) !== undefined) {
			splitsBeforeLastMember = true;
			break;
		}
	}
	if (!splitsBeforeLastMember) return null;
	const out: MobileTimelineItem[] = [];
	let run: ActivityMember[] = [];
	for (const member of members) {
		run.push(member);
		const bucket = bucketsByIdentity.get(activityIdentity(member));
		if (bucket === undefined) continue;
		const row = activityRunRow(run);
		if (row !== null) out.push(row);
		out.push(...bucket);
		run = [];
	}
	const tail = activityRunRow(run);
	if (tail !== null) out.push(tail);
	return out;
}
