// The Board's list as one keyed list (spec 7.3, ruling 22): Live's bands, the
// pinned categories and the project sections, each item a key stable across
// reads and the data to draw it. The Board holds this list still while it is
// touched or moving (settledList.ts) and draws each item with the spring, so
// one list is what lets a row that changes band move instead of leaving and
// arriving. `group` names the section container an item draws inside.
import type { NavigationPinSectionDescriptor, NavigationSessionSummary } from "@evener/appwire-client";
import type { Band, ClassifiedRow, LiveBands } from "./attention";
import type { ProjectSection, ProjectTreeItem } from "./projectTree";

export type LiveBand = Exclude<Band, "idle">;

export type BoardItem = { key: string; group: string } & (
	| { kind: "band"; band: LiveBand; count: number }
	| { kind: "idleFold"; count: number; folded: boolean; unseen: boolean }
	| {
			kind: "row";
			item: ClassifiedRow;
			variant: "signal" | "quiet";
			moving: boolean;
			/** The row sits in an archived tier. */
			archived: boolean;
			/** How far a project tree indents it. */
			depth: number;
			/** A hairline separates it from a row just above it. */
			separated: boolean;
			/** Set on Live's rows only: whether it sits in Needs you. */
			needsYou?: boolean;
	  }
	| { kind: "pinHeader"; section: NavigationPinSectionDescriptor; folded: boolean }
	| { kind: "pinEmpty" }
	| { kind: "projectHeader"; section: ProjectSection; folded: boolean }
	| { kind: "tree"; section: ProjectSection; tree: Exclude<ProjectTreeItem, { kind: "session" }> }
);

const LIVE_BANDS: readonly LiveBand[] = ["needsYou", "working"];
const LIVE = "live";

/** Live: each band that has rows, as its header and its rows, then Idle's
 * fold and, unfolded, its rows. A session is keyed by its ref alone, so it
 * keeps its key when it changes band. */
export function liveItems(bands: LiveBands, idleFolded: boolean): BoardItem[] {
	const items: BoardItem[] = [];
	const rows = (band: Band) =>
		bands[band].map(
			(item): BoardItem => ({
				key: `live:${item.row.ref}`,
				group: LIVE,
				kind: "row",
				item,
				variant: band === "idle" ? "quiet" : "signal",
				moving: band === "working",
				archived: false,
				depth: 0,
				separated: true,
				needsYou: band === "needsYou",
			}),
		);
	for (const band of LIVE_BANDS) {
		if (!bands[band].length) continue;
		items.push({ key: `band:${band}`, group: LIVE, kind: "band", band, count: bands[band].length }, ...rows(band));
	}
	if (bands.idle.length) {
		items.push({
			key: "idle-fold",
			group: LIVE,
			kind: "idleFold",
			count: bands.idle.length,
			folded: idleFolded,
			unseen: bands.idle.some((item) => item.unseen),
		});
		if (!idleFolded) items.push(...rows("idle"));
	}
	return items;
}

/** Each pinned category: its header, then, unfolded and once its page has
 * loaded, its sessions as quiet, still rows (a live session's full row is
 * already in Live), or the hint that says how to pin to it. */
export function pinnedItems(
	pins: readonly NavigationPinSectionDescriptor[],
	pages: Readonly<Record<string, { loaded: boolean; rows: readonly NavigationSessionSummary[] } | undefined>>,
	isFolded: (id: string) => boolean,
	classify: (row: NavigationSessionSummary) => ClassifiedRow,
): BoardItem[] {
	return pins.flatMap((section): BoardItem[] => {
		const group = `pin:${section.id}`;
		const folded = isFolded(section.id);
		const header: BoardItem = { key: group, group, kind: "pinHeader", section, folded };
		const page = pages[section.id];
		if (folded || !page?.loaded) return [header];
		if (!page.rows.length) return [header, { key: `${group}:empty`, group, kind: "pinEmpty" }];
		return [
			header,
			...page.rows.map(
				(row): BoardItem => ({
					key: `${group}:${row.ref}`,
					group,
					kind: "row",
					item: classify(row),
					variant: "quiet",
					moving: false,
					archived: false,
					depth: 0,
					separated: true,
				}),
			),
		];
	});
}

/** Each shown project section: its header, then its tree, whose sessions
 * draw as quiet rows at their depth. */
export function projectItems(
	sections: readonly { section: ProjectSection; folded: boolean; items: readonly ProjectTreeItem[] }[],
	classify: (row: NavigationSessionSummary) => ClassifiedRow,
): BoardItem[] {
	return sections.flatMap(({ section, folded, items }): BoardItem[] => {
		const group = `project:${section}`;
		return [
			{ key: group, group, kind: "projectHeader", section, folded },
			...items.map(
				(tree): BoardItem =>
					tree.kind === "session"
						? {
								key: `${group}:${tree.key}`,
								group,
								kind: "row",
								// An archived session gets no blue dot (Jesse's ruling), though
								// it may still be running.
								item: tree.archived ? { ...classify(tree.row), unseen: false } : classify(tree.row),
								variant: "quiet",
								moving: false,
								archived: tree.archived,
								depth: tree.depth,
								separated: false,
							}
						: { key: `${group}:${tree.key}`, group, kind: "tree", section, tree },
			),
		];
	});
}

/** A list's items by group, groups in the order they first appear. */
export function groupItems(items: readonly BoardItem[]): Map<string, BoardItem[]> {
	const groups = new Map<string, BoardItem[]>();
	for (const item of items) {
		const group = groups.get(item.group);
		if (group) group.push(item);
		else groups.set(item.group, [item]);
	}
	return groups;
}
