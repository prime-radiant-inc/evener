import type {
	NavigationProjectSummary,
	NavigationSessionSummary,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { NavigationPages } from "./navigationPages";

export type ProjectSessionTier = "current" | "recent";
export interface ProjectBrowserGroup {
	project: NavigationProjectSummary;
	expanded: boolean;
	current: ReturnType<NavigationPages<NavigationSessionSummary>["getSnapshot"]>;
	recent: ReturnType<NavigationPages<NavigationSessionSummary>["getSnapshot"]>;
	sessions: NavigationSessionSummary[];
}
export interface ProjectBrowserSnapshot {
	projects: ReturnType<
		NavigationPages<NavigationProjectSummary>["getSnapshot"]
	>;
	groups: ProjectBrowserGroup[];
	loading: boolean;
	error: string | null;
}
export interface ProjectBrowserController {
	getSnapshot(): ProjectBrowserSnapshot;
	subscribe(listener: () => void): () => void;
	initialLoad(): Promise<void>;
	expand(projectKey: string): Promise<void>;
	toggle(projectKey: string): Promise<void>;
	loadMoreProjects(): Promise<void>;
	loadMoreSessions(projectKey: string, tier: ProjectSessionTier): Promise<void>;
	retry(projectKey?: string, tier?: ProjectSessionTier): Promise<void>;
	refresh(): Promise<void>;
	dispose(): void;
}

type Page = NavigationPages<NavigationSessionSummary>;
const projectKey = (row: NavigationProjectSummary) => row.key;
const sessionKey = (row: NavigationSessionSummary) => row.ref;

export function createProjectBrowserController(
	client: ConversationClientLike,
): ProjectBrowserController {
	const listeners = new Set<() => void>();
	const catalog = new NavigationPages<NavigationProjectSummary>(
		client,
		{ resource: "catalog", catalog: "projects" },
		"projects",
		projectKey,
		50,
	);
	const groups = new Map<
		string,
		{
			project: NavigationProjectSummary;
			expanded: boolean;
			current: Page;
			recent: Page;
		}
	>();
	let loading = false;
	let error: string | null = null;
	let disposed = false;
	let epoch = 0;
	const inFlightPages = new Set<string>();
	const unsubs = new Set<() => void>();
	let unwatchCatalog = () => {};
	let catalogWatchStarted = false;
	let cachedSnapshot: ProjectBrowserSnapshot = {
		projects: catalog.getSnapshot(),
		groups: [],
		loading: false,
		error: null,
	};
	const rebuildSnapshot = () => {
		const projectSnapshot = catalog.getSnapshot();
		cachedSnapshot = {
			projects: projectSnapshot,
			groups: projectSnapshot.rows.flatMap((project) => {
				const group = groups.get(project.key);
				if (!group) return [];
				const seen = new Set<string>();
				const sessions = [
					...group.current.getSnapshot().rows,
					...group.recent.getSnapshot().rows,
				].filter((row) => {
					if (seen.has(row.ref)) return false;
					seen.add(row.ref);
					return true;
				});
				return [
					{
						project,
						expanded: group.expanded,
						current: group.current.getSnapshot(),
						recent: group.recent.getSnapshot(),
						sessions,
					},
				];
			}),
			loading,
			error,
		};
	};
	const publish = () => {
		if (disposed) return;
		rebuildSnapshot();
		for (const listener of listeners) listener();
	};
	const pageFor = (
		project: NavigationProjectSummary,
		tier: ProjectSessionTier,
	) => {
		const existing = groups.get(project.key);
		if (existing) return existing[tier];
		const make = (pageTier: ProjectSessionTier) =>
			new NavigationPages<NavigationSessionSummary>(
				client,
				{ resource: "project_page", projectKey: project.key, tier: pageTier },
				"sessions",
				sessionKey,
				20,
			);
		const group = {
			project,
			expanded: false,
			current: make("current"),
			recent: make("recent"),
		};
		groups.set(project.key, group);
		for (const page of [group.current, group.recent]) {
			unsubs.add(page.subscribe(publish));
			unsubs.add(page.watch());
		}
		return group[tier];
	};
	const groupFor = (project: NavigationProjectSummary) => {
		pageFor(project, "current");
		const group = groups.get(project.key);
		if (!group) throw new Error("Could not create project group.");
		return group;
	};
	const loadExpanded = async (key: string, force = false) => {
		const group = groups.get(key);
		if (!group || disposed || !group.expanded) return;
		const currentEpoch = epoch;
		const loads = ([group.current, group.recent] as Page[]).map((page) =>
			force
				? page.refresh()
				: page.getSnapshot().loaded
					? Promise.resolve()
					: page.refresh(),
		);
		await Promise.all(loads);
		if (disposed || currentEpoch !== epoch) return;
		if ([group.current, group.recent].some((page) => page.getSnapshot().error))
			error = "Could not load sessions for this project. Retry to try again.";
		publish();
	};
	const controller: ProjectBrowserController = {
		getSnapshot: () => cachedSnapshot,
		subscribe(listener) {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		async initialLoad() {
			if (
				disposed ||
				catalog.getSnapshot().loading ||
				catalog.getSnapshot().loaded
			)
				return;
			loading = true;
			error = null;
			if (!catalogWatchStarted) {
				unwatchCatalog = catalog.watch();
				catalogWatchStarted = true;
			}
			const currentEpoch = epoch;
			await catalog.refresh();
			if (disposed || currentEpoch !== epoch) return;
			if (catalog.getSnapshot().error) {
				error = catalog.getSnapshot().error;
				loading = false;
				publish();
				return;
			}
			const first = catalog.getSnapshot().rows[0];
			if (first) {
				const group = groupFor(first);
				group.expanded = true;
				await loadExpanded(first.key);
			}
			loading = false;
			publish();
		},
		async expand(key) {
			if (disposed) return;
			const project = catalog.getSnapshot().rows.find((row) => row.key === key);
			if (!project) return;
			const group = groupFor(project);
			group.expanded = true;
			error = null;
			publish();
			await loadExpanded(key);
		},
		async toggle(key) {
			const group = groups.get(key);
			if (group?.expanded) {
				group.expanded = false;
				publish();
				return;
			}
			await controller.expand(key);
		},
		async loadMoreProjects() {
			if (
				disposed ||
				catalog.getSnapshot().loading ||
				catalog.getSnapshot().stale ||
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
			publish();
		},
		async loadMoreSessions(key, tier) {
			const group = groups.get(key);
			if (disposed || !group?.expanded) return;
			const page = group[tier];
			const pageKey = `${key}:${tier}`;
			if (
				page.getSnapshot().loading ||
				page.getSnapshot().stale ||
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
			publish();
		},
		async retry(projectKey, tier) {
			if (disposed) return;
			error = null;
			if (projectKey) {
				const group = groups.get(projectKey);
				if (!group?.expanded) return;
				const pages = tier ? [group[tier]] : [group.current, group.recent];
				await Promise.all(
					pages.map((page) => {
						const state = page.getSnapshot();
						if (state.loading || state.stale) return Promise.resolve();
						return state.loaded ? page.more() : page.refresh();
					}),
				);
				publish();
				return;
			}
			if (catalog.getSnapshot().error) {
				const state = catalog.getSnapshot();
				if (state.loading || state.stale) return;
				if (state.loaded && state.rows.length > 0) await catalog.more();
				else await catalog.refresh();
				publish();
				return;
			}
			await Promise.all(
				[...groups.values()]
					.filter((group) => group.expanded)
					.map((group) => loadExpanded(group.project.key, true)),
			);
			publish();
		},
		async refresh() {
			if (disposed) return;
			error = null;
			loading = true;
			await catalog.refresh();
			if (disposed) return;
			await Promise.all(
				[...groups.values()]
					.filter((group) => group.expanded)
					.map((group) => loadExpanded(group.project.key, true)),
			);
			loading = false;
			publish();
		},
		dispose() {
			if (disposed) return;
			disposed = true;
			epoch++;
			for (const unsubscribe of unsubs) unsubscribe();
			unwatchCatalog();
			unsubs.clear();
			catalog.cancel();
			for (const group of groups.values()) {
				group.current.cancel();
				group.recent.cancel();
			}
			listeners.clear();
		},
	};
	unsubs.add(catalog.subscribe(publish));
	return controller;
}
