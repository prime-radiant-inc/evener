// The reads behind the Board's Projects, Test runs and Archived sections
// (spec 7.1): one project browser per section and ready connection, paused
// while the Board is out of view, and what each section shows, kept across a
// reconnect so a section never blanks while the new reads are out.
import { useIsFocused } from "@react-navigation/native";
import { useEffect, useMemo, useRef, useSyncExternalStore } from "react";
import { useConnection } from "../ConnectionProvider";
import {
	createProjectBrowserController,
	type ProjectBrowserController,
	type ProjectCatalog,
	type ProjectSessionTier,
} from "../projectBrowser";
import { type ProjectSection, type ProjectsView, projectsView } from "./projectTree";

export const PROJECT_SECTIONS: readonly ProjectSection[] = ["projects", "test-runs", "archived"];
const CATALOGS: Record<ProjectSection, ProjectCatalog> = {
	projects: "projects",
	"test-runs": "test_runs",
	archived: "archived_projects",
};
const TIERS: readonly ProjectSessionTier[] = ["current", "recent", "archived"];
const EMPTY_VIEW: ProjectsView = { projects: [], loaded: false, remaining: 0, pages: new Map() };
const noSnapshot = () => null;
const noSubscription = () => () => {};

export interface ProjectSectionData {
	view: ProjectsView;
	/** Null while the connection isn't ready. */
	controller: ProjectBrowserController | null;
	/** Reads the section's catalog, unless it has loaded or is loading. */
	open(): void;
}

export function useProjectSections(hubId: string): Record<ProjectSection, ProjectSectionData> {
	const { client, activeProfile, state } = useConnection();
	const focused = useIsFocused();
	// A client rejects every request until it is ready, so only a ready one
	// gets browsers; a client ready again after a drop gets fresh ones.
	const ready = activeProfile?.id === hubId && state === "ready" ? client : null;
	const controllers = useMemo(
		() =>
			ready
				? (Object.fromEntries(
						PROJECT_SECTIONS.map((section) => [section, createProjectBrowserController(ready, CATALOGS[section])]),
					) as Record<ProjectSection, ProjectBrowserController>)
				: null,
		[ready],
	);
	useEffect(
		() => () => {
			for (const controller of Object.values(controllers ?? {})) controller.dispose();
		},
		[controllers],
	);
	useEffect(() => {
		for (const controller of Object.values(controllers ?? {}))
			if (focused) controller.resume();
			else controller.pause();
	}, [controllers, focused]);
	// Nothing asks you to refresh: back in view on a ready connection, read
	// again whatever never landed.
	useEffect(() => {
		if (focused) for (const controller of Object.values(controllers ?? {})) readWhatNeverLanded(controller);
	}, [controllers, focused]);

	const views: Record<ProjectSection, ProjectsView> = {
		projects: useSectionView(hubId, controllers?.projects ?? null),
		"test-runs": useSectionView(hubId, controllers?.["test-runs"] ?? null),
		archived: useSectionView(hubId, controllers?.archived ?? null),
	};
	const section = (name: ProjectSection): ProjectSectionData => {
		const controller = controllers?.[name] ?? null;
		return { view: views[name], controller, open: () => void controller?.initialLoad() };
	};
	return { projects: section("projects"), "test-runs": section("test-runs"), archived: section("archived") };
}

/** What one section shows: its browser's reads where they have landed, and
 * the view shown before until they do. A new hub starts from nothing. */
function useSectionView(hubId: string, controller: ProjectBrowserController | null): ProjectsView {
	const snapshot = useSyncExternalStore(
		controller?.subscribe ?? noSubscription,
		controller?.getSnapshot ?? noSnapshot,
	);
	const shown = useRef<{ hubId: string; view: ProjectsView } | null>(null);
	const retained = shown.current?.hubId === hubId ? shown.current.view : null;
	// Recomputed only when the reads change: `retained` is the view this
	// computed last, so it adds nothing new between two snapshots.
	const view = useMemo(
		() => (snapshot ? projectsView(snapshot, retained) : (retained ?? EMPTY_VIEW)),
		[snapshot, hubId],
	);
	useEffect(() => {
		shown.current = { hubId, view };
	}, [hubId, view]);
	return view;
}

/** Reads again the catalog after a failed read, and each shown project's
 * tiers whose first read failed or was cancelled when the Board went out of
 * view. */
function readWhatNeverLanded(controller: ProjectBrowserController): void {
	const { projects, groups } = controller.getSnapshot();
	if (projects.error && !projects.loading) void controller.retry();
	for (const group of groups)
		if (group.expanded)
			for (const tier of TIERS)
				if (!group[tier].loaded && !group[tier].loading) void controller.retry(group.project.key, tier);
}

/** Expands the projects whose rows are on screen and collapses the rest, so
 * the browser reads exactly the pages the section shows. */
export function showExpanded(controller: ProjectBrowserController, expanded: ReadonlySet<string>): void {
	const { projects, groups } = controller.getSnapshot();
	for (const project of projects.rows) {
		const group = groups.find((candidate) => candidate.project.key === project.key);
		if (expanded.has(project.key)) {
			if (!group?.expanded) void controller.expand(project.key);
		} else if (group?.expanded) controller.collapse(project.key);
	}
}
