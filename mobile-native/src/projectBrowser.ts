import type { NavigationProjectSummary, NavigationSessionSummary } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { ArchivedPages } from "./archivedPages";
import { NavigationPages, type PageSource, type PageState } from "./navigationPages";

/** The hub's three project catalogs, one per Board section: Projects,
 * Archived and Test runs. */
export type ProjectCatalog = "projects" | "archived_projects" | "test_runs";
/** A project's session tiers: current (the last 24 hours, the Board's
 * "Today"), recent, and archived. Navigation serves the first two; archived
 * rows come from the archived list of the browser's catalog. */
export type ProjectSessionTier = "current" | "recent" | "archived";
const TIERS: readonly ProjectSessionTier[] = ["current", "recent", "archived"];
export interface ProjectBrowserGroup {
	project: NavigationProjectSummary;
	expanded: boolean;
	current: PageState<NavigationSessionSummary>;
	recent: PageState<NavigationSessionSummary>;
	archived: PageState<NavigationSessionSummary>;
	/** The current and recent rows, without duplicates. */
	sessions: NavigationSessionSummary[];
}
export interface ProjectBrowserSnapshot {
	projects: PageState<NavigationProjectSummary>;
	groups: ProjectBrowserGroup[];
}
export interface ProjectBrowserController {
	getSnapshot(): ProjectBrowserSnapshot;
	subscribe(listener: () => void): () => void;
	/** Reads the catalog. A project's sessions load once it is expanded. */
	initialLoad(): Promise<void>;
	expand(projectKey: string): Promise<void>;
	/** The project's rows are no longer shown; its loaded pages stay. */
	collapse(projectKey: string): void;
	toggle(projectKey: string): Promise<void>;
	loadMoreProjects(): Promise<void>;
	loadMoreSessions(projectKey: string, tier: ProjectSessionTier): Promise<void>;
	retry(projectKey?: string, tier?: ProjectSessionTier): Promise<void>;
	refresh(): Promise<void>;
	/** Stop re-reading on the hub's behalf while the list is not shown. */
	pause(): void;
	resume(): void;
	dispose(): void;
}

type Page = PageSource<NavigationSessionSummary>;
type Group = {
	project: NavigationProjectSummary;
	expanded: boolean;
} & Record<ProjectSessionTier, Page>;
const projectKey = (row: NavigationProjectSummary) => row.key;
const sessionKey = (row: NavigationSessionSummary) => row.ref;
const tierPages = (group: Group) => TIERS.map((tier) => group[tier]);

export function createProjectBrowserController(
	client: ConversationClientLike,
	catalogName: ProjectCatalog = "projects",
): ProjectBrowserController {
	const listeners = new Set<() => void>();
	const catalog = new NavigationPages<NavigationProjectSummary>(
		client,
		{ resource: "catalog", catalog: catalogName },
		"projects",
		projectKey,
		50,
	);
	const groups = new Map<string, Group>();
	let disposed = false;
	let paused = false;
	const inFlightPages = new Set<string>();
	const unsubs = new Set<() => void>();
	let unwatchCatalog = () => {};
	let catalogWatchStarted = false;
	let cachedSnapshot: ProjectBrowserSnapshot = {
		projects: catalog.getSnapshot(),
		groups: [],
	};
	const rebuildSnapshot = () => {
		const projectSnapshot = catalog.getSnapshot();
		cachedSnapshot = {
			projects: projectSnapshot,
			groups: projectSnapshot.rows.flatMap((project) => {
				const group = groups.get(project.key);
				if (!group) return [];
				const current = group.current.getSnapshot();
				const recent = group.recent.getSnapshot();
				const seen = new Set<string>();
				const sessions = [...current.rows, ...recent.rows].filter((row) => {
					if (seen.has(row.ref)) return false;
					seen.add(row.ref);
					return true;
				});
				return [
					{
						project,
						expanded: group.expanded,
						current,
						recent,
						archived: group.archived.getSnapshot(),
						sessions,
					},
				];
			}),
		};
	};
	// Each page publishes every change to its own state, and the controller
	// subscribes to every page (in groupFor, and to the catalog at the end),
	// so it publishes on its own only for the one thing no page carries: a
	// project's expanded flag.
	const publish = () => {
		if (disposed) return;
		rebuildSnapshot();
		for (const listener of listeners) listener();
	};
	const eachPage = (visit: (page: Page | typeof catalog) => void) => {
		visit(catalog);
		for (const group of groups.values()) for (const page of tierPages(group)) visit(page);
	};
	const groupFor = (project: NavigationProjectSummary): Group => {
		const existing = groups.get(project.key);
		if (existing) return existing;
		const make = (tier: Exclude<ProjectSessionTier, "archived">) =>
			new NavigationPages<NavigationSessionSummary>(
				client,
				{ resource: "project_page", projectKey: project.key, tier },
				"sessions",
				sessionKey,
				20,
			);
		const group: Group = {
			project,
			expanded: false,
			current: make("current"),
			recent: make("recent"),
			archived: new ArchivedPages(client, catalogName, project.key),
		};
		groups.set(project.key, group);
		for (const page of tierPages(group)) {
			unsubs.add(page.subscribe(publish));
			unsubs.add(page.watch());
			if (paused) page.cancel();
		}
		return group;
	};
	const loadExpanded = async (key: string, force = false) => {
		const group = groups.get(key);
		if (!group || disposed || !group.expanded) return;
		await Promise.all(
			tierPages(group).map((page) => (force || !page.getSnapshot().loaded ? page.refresh() : Promise.resolve())),
		);
	};
	const reloadExpanded = () =>
		Promise.all(
			[...groups.values()].filter((group) => group.expanded).map((group) => loadExpanded(group.project.key, true)),
		);
	const controller: ProjectBrowserController = {
		getSnapshot: () => cachedSnapshot,
		subscribe(listener) {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		async initialLoad() {
			if (disposed || catalog.getSnapshot().loading || catalog.getSnapshot().loaded) return;
			if (!catalogWatchStarted) {
				unwatchCatalog = catalog.watch();
				catalogWatchStarted = true;
			}
			await catalog.refresh();
		},
		async expand(key) {
			if (disposed) return;
			const project = catalog.getSnapshot().rows.find((row) => row.key === key);
			if (!project) return;
			const group = groupFor(project);
			group.expanded = true;
			publish();
			await loadExpanded(key);
		},
		collapse(key) {
			const group = groups.get(key);
			if (!group?.expanded) return;
			group.expanded = false;
			publish();
		},
		async toggle(key) {
			if (groups.get(key)?.expanded) controller.collapse(key);
			else await controller.expand(key);
		},
		async loadMoreProjects() {
			if (
				disposed ||
				catalog.getSnapshot().loading ||
				catalog.getSnapshot().error ||
				!catalog.getSnapshot().loaded ||
				!catalog.getSnapshot().remaining
			)
				return;
			if (inFlightPages.has("catalog")) return;
			inFlightPages.add("catalog");
			try {
				await catalog.more();
			} finally {
				inFlightPages.delete("catalog");
			}
		},
		async loadMoreSessions(key, tier) {
			const group = groups.get(key);
			if (disposed || !group?.expanded) return;
			const page = group[tier];
			const pageKey = `${key}:${tier}`;
			if (
				page.getSnapshot().loading ||
				page.getSnapshot().error ||
				!page.getSnapshot().loaded ||
				!page.getSnapshot().remaining
			)
				return;
			if (inFlightPages.has(pageKey)) return;
			inFlightPages.add(pageKey);
			try {
				await page.more();
			} finally {
				inFlightPages.delete(pageKey);
			}
		},
		async retry(projectKey, tier) {
			if (disposed) return;
			if (projectKey) {
				const group = groups.get(projectKey);
				if (!group?.expanded) return;
				const pages = tier ? [group[tier]] : tierPages(group);
				await Promise.all(
					pages.map((page) => {
						const state = page.getSnapshot();
						if (state.loading) return Promise.resolve();
						return state.loaded ? page.more() : page.refresh();
					}),
				);
				return;
			}
			if (catalog.getSnapshot().error) {
				const state = catalog.getSnapshot();
				if (state.loading) return;
				if (state.loaded && state.rows.length > 0) await catalog.more();
				else await catalog.refresh();
				return;
			}
			await reloadExpanded();
		},
		async refresh() {
			if (disposed) return;
			await catalog.refresh();
			if (disposed) return;
			await reloadExpanded();
		},
		pause() {
			paused = true;
			eachPage((page) => page.cancel());
		},
		resume() {
			paused = false;
			eachPage((page) => page.resume());
		},
		dispose() {
			if (disposed) return;
			disposed = true;
			for (const unsubscribe of unsubs) unsubscribe();
			unwatchCatalog();
			unsubs.clear();
			eachPage((page) => page.cancel());
			listeners.clear();
		},
	};
	unsubs.add(catalog.subscribe(publish));
	return controller;
}
