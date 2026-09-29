// The Board's Projects, Test runs and Archived sections as one flat list of
// items (spec 7.1): project rows, host groups, a project's Today and Recent
// captions and its folded Archived group, and the rows that load more. Pure:
// BoardScreen owns the reads and renders the items.
//
// Organizing by host mirrors the web rail (railNodes.ts hostProjectNodes and
// projectNodesWithHostBranches) through the shared package's host grouping.
// The hub has no host-scoped project read, so a project's pages are read once
// and every row sits under the host its own host_id names: a session shows
// once, however many hosts own its project (ruling 11).
import type { NavigationProjectSummary, NavigationSessionSummary, Source } from "@evener/appwire-client";
import {
	CONTROLLER_SOURCE_ID,
	canonicalHostId,
	type HostFacts,
	orderedHosts,
	projectHostIds,
} from "@evener/appwire-client/state/navigation";
import type { ProjectBrowserSnapshot, ProjectSessionTier } from "../projectBrowser";
import type { OrganizeBy } from "./boardMemory";

export type Grouping = "flat" | OrganizeBy;
export type ProjectSection = "projects" | "test-runs" | "archived";

/** One tier's page as the project data holds it. */
export interface TierPage {
	rows: readonly NavigationSessionSummary[];
	remaining: number;
	loaded: boolean;
	error: string | null;
}
export type ProjectPages = Record<ProjectSessionTier, TierPage>;

export type ProjectTreeItem =
	| { kind: "host"; key: string; fold: string; depth: 0; host: HostFacts; liveCount: number | null; folded: boolean }
	| {
			kind: "project";
			key: string;
			fold: string;
			depth: 0 | 1;
			project: NavigationProjectSummary;
			liveCount: number | null;
			folded: boolean;
	  }
	| { kind: "branch"; key: string; fold: string; depth: 1; host: HostFacts; folded: boolean }
	| { kind: "tier"; key: string; depth: number; label: "Today" | "Recent" }
	| { kind: "archivedGroup"; key: string; fold: string; depth: number; count: number | null; folded: boolean }
	| { kind: "session"; key: string; depth: number; row: NavigationSessionSummary; archived: boolean }
	| { kind: "more"; key: string; depth: number; projectKey: string; tier: ProjectSessionTier; remaining: number }
	/** The section's catalog has more projects than it has read. */
	| { kind: "moreProjects"; key: string; depth: 0; remaining: number }
	| { kind: "loading"; key: string; depth: number }
	| { kind: "failed"; key: string; depth: number };

export interface ProjectTreeInput {
	section: ProjectSection;
	projects: readonly NavigationProjectSummary[];
	/** Each project's loaded pages; a project never expanded has none. */
	pages: ReadonlyMap<string, ProjectPages>;
	sources: readonly Source[];
	/** Applies to the Projects section alone: Test runs and Archived stay flat,
	 * as on the web (Rail.tsx projectsTierFor). */
	organizeBy: OrganizeBy;
	isFolded(fold: string, byDefault: boolean): boolean;
	/** A connected host group's live count, or null when it can't be known. */
	hostLiveCount(hostId: string): number | null;
	/** The projects the section's catalog holds past the ones read. */
	remainingProjects: number;
}

/** The sections' own folds (part 1's FoldedSections keys): Projects starts
 * unfolded, Test runs and Archived folded (spec 7.1). */
export const SECTION_FOLDS: Record<ProjectSection, { fold: string; foldedByDefault: boolean }> = {
	projects: { fold: "projects", foldedByDefault: false },
	"test-runs": { fold: "test-runs", foldedByDefault: true },
	archived: { fold: "archived", foldedByDefault: true },
};

/** "flat" until the manifest names a host besides this hub, the web's rule
 * (Rail.tsx: displaySources.some(id !== "local")); then the person's choice. */
export function grouping(sources: readonly Source[], organizeBy: OrganizeBy): Grouping {
	return sources.some((source) => source.id !== CONTROLLER_SOURCE_ID) ? organizeBy : "flat";
}

const PROJECT_FOLD_PREFIX: Record<ProjectSection, string> = {
	projects: "project",
	"test-runs": "test-run",
	archived: "archived-project",
};
/** The fold that holds a project's rows: the project row, or its copy under one host. */
export function projectFold(section: ProjectSection, projectKey: string, hostId?: string): string {
	const base = `${PROJECT_FOLD_PREFIX[section]}:${projectKey}`;
	return hostId === undefined ? base : `${base}@${hostId}`;
}
export const hostFold = (hostId: string) => `host:${hostId}`;
const branchFold = (projectKey: string, hostId: string) => `project:${projectKey}@host:${hostId}`;
const itemKey = (section: ProjectSection, fold: string) => `${section}/${fold}`;

/** A project's live count: the hub's working and waiting-on-you sessions.
 * The hub counts idle live sessions in neither rollup (ruling 12). */
export function projectLiveCount(project: NavigationProjectSummary): number | null {
	const count = (project.rollup_live ?? 0) + (project.rollup_attn ?? 0);
	return count > 0 ? count : null;
}

const allRows = (pages: ProjectPages | undefined) =>
	pages ? [...pages.current.rows, ...pages.recent.rows, ...pages.archived.rows] : [];
const activeRows = (pages: ProjectPages | undefined) => (pages ? [...pages.current.rows, ...pages.recent.rows] : []);
const pinnedFirst = (projects: readonly NavigationProjectSummary[]) =>
	[...projects].sort((a, b) => Number(!!b.favorite) - Number(!!a.favorite));
const onHost = (hostId: string | null) => (row: NavigationSessionSummary) => hostId === null || row.host_id === hostId;
const activeTiersLoaded = (pages: ProjectPages) => pages.current.loaded && pages.recent.loaded;

export function projectTreeItems(input: ProjectTreeInput): ProjectTreeItem[] {
	const out: ProjectTreeItem[] = [];
	const mode = input.section === "projects" ? grouping(input.sources, input.organizeBy) : "flat";
	if (mode === "host-project") hostFirst(out, input);
	else for (const project of pinnedFirst(input.projects)) projectFirst(out, input, project, mode === "project-host");
	if (input.remainingProjects > 0)
		out.push({
			kind: "moreProjects",
			key: `${input.section}/more-projects`,
			depth: 0,
			remaining: input.remainingProjects,
		});
	return out;
}

/** One placeholder row while the tiers a project's rows wait on haven't been
 * read once: "failed" when one of those first reads failed. */
function placeholder(out: ProjectTreeItem[], prefix: string, tiers: readonly TierPage[], depth: number): void {
	const failed = tiers.some((page) => !page.loaded && page.error !== null);
	out.push(
		failed ? { kind: "failed", key: `${prefix}/failed`, depth } : { kind: "loading", key: `${prefix}/loading`, depth },
	);
}

/** Today's and Recent's captions and rows, of one host or of every host. A
 * session the hub lists in both tiers (it crossed the 24-hour line between the
 * two reads) shows once, under Today, as projectBrowser's `sessions` does. */
function activeItems(
	out: ProjectTreeItem[],
	prefix: string,
	pages: ProjectPages,
	hostId: string | null,
	depth: number,
): void {
	const shown = new Set<string>();
	const belongs = onHost(hostId);
	for (const [tier, label] of [
		["current", "Today"],
		["recent", "Recent"],
	] as const) {
		const rows = pages[tier].rows.filter((row) => belongs(row) && !shown.has(row.ref));
		if (rows.length === 0) continue;
		out.push({ kind: "tier", key: `${prefix}/tier:${tier}`, depth, label });
		for (const row of rows) {
			shown.add(row.ref);
			out.push({ kind: "session", key: `${prefix}/${tier}/${row.ref}`, depth, row, archived: false });
		}
	}
}

/** Today's and Recent's rows that load more; they belong to the project, so
 * they appear once (ruling 12). */
function moreItems(
	out: ProjectTreeItem[],
	prefix: string,
	pages: ProjectPages,
	projectKey: string,
	depth: number,
): void {
	for (const tier of ["current", "recent"] as const)
		if (pages[tier].remaining > 0)
			out.push({
				kind: "more",
				key: `${prefix}/more:${tier}`,
				depth,
				projectKey,
				tier,
				remaining: pages[tier].remaining,
			});
}

/** The folded Archived group: its count only once every archived row is loaded. */
function archivedItems(
	out: ProjectTreeItem[],
	input: ProjectTreeInput,
	containerFold: string,
	pages: ProjectPages,
	hostId: string | null,
	depth: number,
	projectKey: string,
	carriesMore: boolean,
): void {
	const rows = pages.archived.rows.filter(onHost(hostId));
	const remaining = carriesMore ? pages.archived.remaining : 0;
	if (rows.length === 0 && remaining === 0) return;
	const fold = `${containerFold}:archived`;
	const folded = input.isFolded(fold, input.section !== "archived");
	const key = `${itemKey(input.section, containerFold)}/archived`;
	out.push({
		kind: "archivedGroup",
		key,
		fold,
		depth,
		count: pages.archived.remaining === 0 ? rows.length : null,
		folded,
	});
	if (folded) return;
	for (const row of rows)
		out.push({ kind: "session", key: `${key}/${row.ref}`, depth: depth + 1, row, archived: true });
	if (remaining > 0)
		out.push({ kind: "more", key: `${key}/more`, depth: depth + 1, projectKey, tier: "archived", remaining });
}

/** "Project, then host", or a flat section: project rows, with per-host
 * branches inside a project whose Today and Recent rows span hosts. */
function projectFirst(
	out: ProjectTreeItem[],
	input: ProjectTreeInput,
	project: NavigationProjectSummary,
	branchByHost: boolean,
): void {
	const fold = projectFold(input.section, project.key);
	const prefix = itemKey(input.section, fold);
	const folded = input.isFolded(fold, !(project.default_expanded ?? false));
	out.push({ kind: "project", key: prefix, fold, depth: 0, project, liveCount: projectLiveCount(project), folded });
	if (folded) return;
	const pages = input.pages.get(project.key);
	if (!pages || !activeTiersLoaded(pages)) {
		placeholder(out, prefix, pages ? [pages.current, pages.recent] : [], 1);
		return;
	}
	const shownBefore = out.length;
	const hosts = branchByHost
		? orderedHosts(
				activeRows(pages).map((row) => row.host_id),
				input.sources,
			)
		: [];
	if (hosts.length > 1)
		for (const host of hosts) {
			const branch = branchFold(project.key, host.id);
			const branchKey = itemKey(input.section, branch);
			const branchFolded = input.isFolded(branch, true);
			out.push({ kind: "branch", key: branchKey, fold: branch, depth: 1, host, folded: branchFolded });
			if (!branchFolded) activeItems(out, branchKey, pages, host.id, 2);
		}
	else activeItems(out, prefix, pages, null, 1);
	moreItems(out, prefix, pages, project.key, 1);
	// Today and Recent never wait on the archived page; a project with nothing
	// to show until it lands shows the archived page's placeholder.
	if (pages.archived.loaded) archivedItems(out, input, fold, pages, null, 1, project.key, true);
	else if (out.length === shownBefore) placeholder(out, prefix, [pages.archived], 1);
}

/** Where a project's copies sit when hosts come first: every host that owns
 * it, the canonical copy, which carries its counts and its Today and Recent
 * more rows (ruling 12), and the copy that carries its archived more row. */
function hostCopies(project: NavigationProjectSummary, pages: ProjectPages | undefined, sources: readonly Source[]) {
	const hostIds = projectHostIds(project.sources, allRows(pages));
	return {
		hostIds,
		canonical: canonicalHostId(hostIds, activeRows(pages), sources),
		// The first copy with archived rows loaded, so the archived more row
		// never sits alone in a copy with nothing archived (the web anchors its
		// overflow on a host with rows for the same reason).
		archivedCarrier: canonicalHostId(hostIds, pages?.archived.rows ?? [], sources),
	};
}

/** "Host, then project": a group per host that owns a project, each holding a
 * copy of every project it owns, and each copy holding only that host's rows. */
function hostFirst(out: ProjectTreeItem[], input: ProjectTreeInput): void {
	const placed = pinnedFirst(input.projects).map((project) => {
		const pages = input.pages.get(project.key);
		return { project, pages, ...hostCopies(project, pages, input.sources) };
	});
	for (const host of orderedHosts(
		placed.flatMap(({ hostIds }) => hostIds),
		input.sources,
	)) {
		const fold = hostFold(host.id);
		const folded = input.isFolded(fold, false);
		out.push({
			kind: "host",
			key: itemKey(input.section, fold),
			fold,
			depth: 0,
			host,
			liveCount: host.online ? input.hostLiveCount(host.id) : null,
			folded,
		});
		if (folded) continue;
		for (const { project, pages, hostIds, canonical, archivedCarrier } of placed) {
			if (!hostIds.includes(host.id)) continue;
			const copyFold = projectFold(input.section, project.key, host.id);
			const prefix = itemKey(input.section, copyFold);
			const copyFolded = input.isFolded(copyFold, !(project.default_expanded ?? false));
			out.push({
				kind: "project",
				key: prefix,
				fold: copyFold,
				depth: 1,
				project,
				liveCount: canonical === host.id ? projectLiveCount(project) : null,
				folded: copyFolded,
			});
			if (copyFolded) continue;
			if (!pages || !activeTiersLoaded(pages)) {
				placeholder(out, prefix, pages ? [pages.current, pages.recent] : [], 2);
				continue;
			}
			const shownBefore = out.length;
			activeItems(out, prefix, pages, host.id, 2);
			if (canonical === host.id) moreItems(out, prefix, pages, project.key, 2);
			if (pages.archived.loaded)
				archivedItems(out, input, copyFold, pages, host.id, 2, project.key, archivedCarrier === host.id);
			else if (out.length === shownBefore) placeholder(out, prefix, [pages.archived], 2);
		}
	}
}

export interface RevealTarget {
	/** Folds to open, outermost first. */
	unfold: string[];
	/** The item key to scroll to once they are open. */
	scrollTo: string;
}

/** Where search's project hit goes (spec 7.4): the project row, or, when
 * hosts come first, its canonical copy, the one carrying its counts. */
export function projectRevealTarget(input: {
	project: NavigationProjectSummary;
	pages: ProjectPages | undefined;
	sources: readonly Source[];
	organizeBy: OrganizeBy;
}): RevealTarget {
	const { project, pages, sources } = input;
	const section = SECTION_FOLDS.projects.fold;
	if (grouping(sources, input.organizeBy) !== "host-project") {
		const fold = projectFold("projects", project.key);
		return { unfold: [section, fold], scrollTo: itemKey("projects", fold) };
	}
	const host = hostCopies(project, pages, sources).canonical;
	const fold = projectFold("projects", project.key, host);
	return { unfold: [section, hostFold(host), fold], scrollTo: itemKey("projects", fold) };
}

/** The projects whose rows are on screen, so the Board reads their pages:
 * every project row or copy that is unfolded (items exist only under
 * unfolded parents), the web's projectLoadExpansionKeys. */
export function expandedProjectKeys(items: readonly ProjectTreeItem[]): Set<string> {
	const keys = new Set<string>();
	for (const item of items) if (item.kind === "project" && !item.folded) keys.add(item.project.key);
	return keys;
}

export interface ProjectsView {
	projects: readonly NavigationProjectSummary[];
	/** A catalog read has landed for this section, on this connection or an earlier one. */
	loaded: boolean;
	/** The catalog's projects past the ones read. */
	remaining: number;
	pages: ReadonlyMap<string, ProjectPages>;
}

/** What a section shows: the current connection's reads where they have
 * landed, and the rows shown before until they do, so a reconnect never
 * blanks a section (part 1's Review Focus 1). Only a tier that loaded is kept,
 * as boardData keeps pages: a failed first read is never kept, so a retry in
 * flight reads as loading. `retained` is the view shown last; pass null for a
 * new hub. */
export function projectsView(fresh: ProjectBrowserSnapshot, retained: ProjectsView | null): ProjectsView {
	const before = retained ?? { projects: [], loaded: false, remaining: 0, pages: new Map<string, ProjectPages>() };
	const pages = new Map(before.pages);
	for (const group of fresh.groups) {
		const previous = pages.get(group.project.key);
		const pick = (tier: ProjectSessionTier): TierPage =>
			group[tier].loaded || !previous?.[tier].loaded ? group[tier] : previous[tier];
		pages.set(group.project.key, { current: pick("current"), recent: pick("recent"), archived: pick("archived") });
	}
	return fresh.projects.loaded
		? { projects: fresh.projects.rows, loaded: true, remaining: fresh.projects.remaining, pages }
		: { projects: before.projects, loaded: before.loaded, remaining: before.remaining, pages };
}

/** Each host's rows in Live, once each (a session that needs you is in both
 * Live and the needs_you section), when every page of both is loaded;
 * otherwise no host has a count (ruling 12). */
export function liveCountsByHost(
	liveRows: readonly NavigationSessionSummary[],
	complete: boolean,
): (hostId: string) => number | null {
	if (!complete) return () => null;
	const counts = new Map<string, number>();
	const seen = new Set<string>();
	for (const row of liveRows) {
		if (seen.has(row.ref)) continue;
		seen.add(row.ref);
		const host = row.host_id;
		counts.set(host, (counts.get(host) ?? 0) + 1);
	}
	return (hostId) => counts.get(hostId) ?? null;
}
