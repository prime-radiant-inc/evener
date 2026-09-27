import { describe, expect, it } from "vitest";
import type {
	NavigationProjectSummary,
	NavigationSessionSummary,
	Source,
} from "@evener/appwire-client";
import type { ProjectBrowserSnapshot } from "../projectBrowser";
import { boardState } from "./attention";
import {
	expandedProjectKeys,
	liveCountsByHost,
	morePagesToLoad,
	type ProjectPages,
	type ProjectTreeInput,
	type ProjectTreeItem,
	projectRevealTarget,
	projectTreeItems,
	type ProjectsView,
	projectsView,
} from "./projectTree";

const source = (id: string, label = id, online = true): Source => ({
	id,
	label,
	kind: id === "local" ? "local" : "appwire",
	online,
});
const LOCAL = source("local", "this host");
const PARK = source("paradise-park");
const project = (key: string, over: Partial<NavigationProjectSummary> = {}): NavigationProjectSummary => ({
	key,
	name: key,
	session_count: 3,
	...over,
});
const session = (
	ref: string,
	host_id = "local",
	over: Partial<NavigationSessionSummary> = {},
): NavigationSessionSummary => ({
	ref,
	host_id,
	session_id: ref,
	title: ref,
	project: "evener",
	state: "idle",
	kind: "session",
	live: true,
	children: [],
	...over,
});
const page = (rows: NavigationSessionSummary[] = [], remaining = 0) => ({
	rows,
	remaining,
	loaded: true,
	error: null,
});
const pages = (
	current: NavigationSessionSummary[] = [],
	recent: NavigationSessionSummary[] = [],
	archived: NavigationSessionSummary[] = [],
	remaining: Partial<Record<"current" | "recent" | "archived", number>> = {},
): ProjectPages => ({
	current: page(current, remaining.current),
	recent: page(recent, remaining.recent),
	archived: page(archived, remaining.archived),
});
const unfolded = () => false;
const defaults = (_fold: string, byDefault: boolean) => byDefault;
const evener = project("evener", { sources: ["local", "paradise-park"], rollup_live: 2, rollup_attn: 1 });

function items(over: Partial<ProjectTreeInput>): ProjectTreeItem[] {
	return projectTreeItems({
		section: "projects",
		projects: [],
		pages: new Map(),
		sources: [LOCAL, PARK],
		organizeBy: "host-project",
		isFolded: unfolded,
		hostLiveCount: () => null,
		...over,
	});
}
/** The list as a person reads it: indentation is depth. */
function outline(list: readonly ProjectTreeItem[]): string[] {
	const indent = (depth: number) => "  ".repeat(depth);
	return list.map((item) => {
		switch (item.kind) {
			case "host":
				return `host ${item.host.label}${item.host.online ? "" : " (offline)"}${item.liveCount ? ` · ${item.liveCount} live` : ""}`;
			case "project":
				return `${indent(item.depth)}project ${item.project.key}${item.liveCount ? ` · ${item.liveCount} live` : ""}`;
			case "branch":
				return `  branch ${item.host.label}`;
			case "tier":
				return `${indent(item.depth)}${item.label}`;
			case "archivedGroup":
				return `${indent(item.depth)}Archived${item.count === null ? "" : ` · ${item.count}`}`;
			case "session":
				return `${indent(item.depth)}${item.row.ref}`;
			case "more":
				return `${indent(item.depth)}${item.remaining} more ${item.tier}`;
			case "loading":
			case "failed":
				return `${indent(item.depth)}${item.kind}`;
		}
	});
}

describe("organized by host (spec 7.1, ruling 11)", () => {
	it("a project on two hosts shows each session once, under the host its row names", () => {
		const list = items({
			projects: [evener],
			pages: new Map([
				["evener", pages([session("local:a"), session("paradise-park:b", "paradise-park")], [session("local:c")])],
			]),
		});
		expect(outline(list)).toEqual([
			"host this host",
			"  project evener · 3 live",
			"    Today",
			"    local:a",
			"    Recent",
			"    local:c",
			"host paradise-park",
			"  project evener",
			"    Today",
			"    paradise-park:b",
		]);
		const refs = list.flatMap((item) => (item.kind === "session" ? [item.row.ref] : []));
		expect(refs.sort()).toEqual(["local:a", "local:c", "paradise-park:b"]);
	});

	it("an offline host shows only its own rows, and they read as shut down", () => {
		const a = session("local:a", "local", { state: "active" });
		const b = session("paradise-park:b", "paradise-park", { state: "active", offline: true });
		const list = items({
			sources: [LOCAL, source("paradise-park", "paradise-park", false)],
			projects: [evener],
			pages: new Map([["evener", pages([a, b])]]),
			hostLiveCount: () => 4,
		});
		expect(outline(list)).toEqual([
			"host this host · 4 live",
			"  project evener · 3 live",
			"    Today",
			"    local:a",
			"host paradise-park (offline)",
			"  project evener",
			"    Today",
			"    paradise-park:b",
		]);
		expect(boardState(a, false, false)).toBe("working");
		expect(boardState(b, false, false)).toBe("shutDown");
	});

	it("the project's counts and its more rows read once, on the first host with loaded rows", () => {
		const list = items({
			projects: [evener],
			pages: new Map([["evener", pages([session("paradise-park:b", "paradise-park")], [], [], { current: 12 })]]),
		});
		expect(outline(list)).toEqual([
			"host this host",
			"  project evener",
			"host paradise-park",
			"  project evener · 3 live",
			"    Today",
			"    paradise-park:b",
			"    12 more current",
		]);
	});

	it("reads an omitted or empty sources list as this hub's own project", () => {
		for (const sources of [undefined, []]) {
			const docs = project("docs", { sources });
			expect(outline(items({ projects: [docs], pages: new Map([["docs", pages([session("local:d")])]]) }))).toEqual([
				"host this host",
				"  project docs",
				"    Today",
				"    local:d",
			]);
		}
	});

	it("places a cluster under its newest member's host", () => {
		const cluster = session("cluster:x", "cluster", {
			kind: "cluster",
			children: [session("paradise-park:m", "paradise-park")],
		});
		// The project names no sources, so the one row it has decides its host.
		const list = items({ projects: [project("docs")], pages: new Map([["docs", pages([cluster])]]) });
		expect(outline(list)).toEqual(["host paradise-park", "  project docs", "    Today", "    cluster:x"]);
	});

	it("keeps a host's archived rows in that host's copy", () => {
		const list = items({
			projects: [evener],
			pages: new Map([["evener", pages([], [], [session("local:z"), session("paradise-park:y", "paradise-park")])]]),
		});
		expect(outline(list)).toEqual([
			"host this host",
			"  project evener · 3 live",
			"    Archived · 1",
			"      local:z",
			"host paradise-park",
			"  project evener",
			"    Archived · 1",
			"      paradise-park:y",
		]);
	});
	it("puts the archived group's more row on a host that has archived rows", () => {
		const list = items({
			projects: [evener],
			pages: new Map([["evener", pages([session("local:a")], [], [session("paradise-park:y", "paradise-park")], { archived: 40 })]]),
		});
		expect(outline(list)).toEqual([
			"host this host",
			"  project evener · 3 live",
			"    Today",
			"    local:a",
			"host paradise-park",
			"  project evener",
			"    Archived",
			"      paradise-park:y",
			"      40 more archived",
		]);
	});
});

describe("organized by project (spec 7.1)", () => {
	const spanning = new Map([
		["evener", pages([session("local:a"), session("paradise-park:b", "paradise-park")], [], [session("local:z")])],
	]);

	it("splits a project's rows into host branches only when they span hosts", () => {
		expect(outline(items({ organizeBy: "project-host", projects: [evener], pages: spanning }))).toEqual([
			"project evener · 3 live",
			"  branch this host",
			"    Today",
			"    local:a",
			"  branch paradise-park",
			"    Today",
			"    paradise-park:b",
			"  Archived · 1",
			"    local:z",
		]);
		expect(
			outline(items({ organizeBy: "project-host", projects: [project("docs")], pages: new Map([["docs", pages([session("local:d")])]]) })),
		).toEqual(["project docs", "  Today", "  local:d"]);
	});

	it("folds branches and the archived group by default, as the web does", () => {
		const shown = project("evener", { ...evener, default_expanded: true });
		expect(outline(items({ organizeBy: "project-host", projects: [shown], pages: spanning, isFolded: defaults }))).toEqual([
			"project evener · 3 live",
			"  branch this host",
			"  branch paradise-park",
			"  Archived · 1",
		]);
	});

	it("ignores Organize by while the hub has one host", () => {
		expect(
			outline(items({ sources: [LOCAL], projects: [project("docs")], pages: new Map([["docs", pages([session("local:d")])]]) })),
		).toEqual(["project docs", "  Today", "  local:d"]);
	});

	it("floats pinned projects to the top in both modes", () => {
		const list = [project("a"), project("b", { favorite: true })];
		const folded = () => true;
		expect(outline(items({ organizeBy: "project-host", projects: list, isFolded: folded }))).toEqual(["project b", "project a"]);
		expect(outline(items({ projects: list, isFolded: (fold) => fold.startsWith("project:") }))).toEqual([
			"host this host",
			"  project b",
			"  project a",
		]);
	});

	it("loads the next page from the project's own more rows", () => {
		expect(
			outline(
				items({
					organizeBy: "project-host",
					projects: [project("docs")],
					pages: new Map([["docs", pages([session("local:d")], [], [session("local:o")], { current: 20, recent: 3, archived: 40 })]]),
				}),
			),
		).toEqual(["project docs", "  Today", "  local:d", "  20 more current", "  3 more recent", "  Archived", "    local:o", "    40 more archived"]);
	});
	it("shows a session the hub lists in both Today and Recent once, under Today", () => {
		const both = session("local:a");
		expect(
			outline(items({ sources: [LOCAL], projects: [project("docs")], pages: new Map([["docs", pages([both], [both, session("local:b")])]]) })),
		).toEqual(["project docs", "  Today", "  local:a", "  Recent", "  local:b"]);
	});
});

describe("test runs and archived", () => {
	it("stay flat, and an archived project opens its archived group by default", () => {
		const old = project("old", { default_expanded: true });
		expect(
			outline(items({ section: "archived", projects: [old], pages: new Map([["old", pages([], [], [session("local:o")])]]), isFolded: defaults })),
		).toEqual(["project old", "  Archived · 1", "    local:o"]);
		expect(
			outline(items({ section: "test-runs", projects: [project("hub-test-env")], pages: new Map([["hub-test-env", pages([session("local:t")])]]) })),
		).toEqual(["project hub-test-env", "  Today", "  local:t"]);
	});

	it("show one placeholder until a project's pages land, and say so when a first read fails", () => {
		expect(outline(items({ organizeBy: "project-host", projects: [project("p")] }))).toEqual(["project p", "  loading"]);
		const failed = { ...pages(), current: { rows: [], remaining: 0, loaded: false, error: "offline" } };
		expect(outline(items({ organizeBy: "project-host", projects: [project("p")], pages: new Map([["p", failed]]) }))).toEqual([
			"project p",
			"  failed",
		]);
	});
});

describe("keys", () => {
	it("names every fold per section and host, and keeps every item key unique", () => {
		const list = items({ projects: [evener], pages: new Map([["evener", pages([session("local:a"), session("paradise-park:b", "paradise-park")], [], [session("local:z")])]]) });
		expect(list.flatMap((item) => ("fold" in item ? [item.fold] : []))).toEqual([
			"host:local",
			"project:evener@local",
			"project:evener@local:archived",
			"host:paradise-park",
			"project:evener@paradise-park",
		]);
		expect(new Set(list.map((item) => item.key)).size).toBe(list.length);
		const branches = items({ organizeBy: "project-host", projects: [evener], pages: new Map([["evener", pages([session("local:a"), session("paradise-park:b", "paradise-park")])]]) });
		expect(branches.flatMap((item) => ("fold" in item ? [item.fold] : []))).toEqual([
			"project:evener",
			"project:evener@host:local",
			"project:evener@host:paradise-park",
		]);
		expect(items({ section: "test-runs", projects: [project("t")], isFolded: () => true })[0]).toMatchObject({ fold: "test-run:t" });
		expect(items({ section: "archived", projects: [project("o")], isFolded: () => true })[0]).toMatchObject({ fold: "archived-project:o" });
	});
});

describe("revealing a project for search (spec 7.4)", () => {
	const unfoldOnly = (target: { unfold: string[] }) => (fold: string, byDefault: boolean) =>
		target.unfold.includes(fold) ? false : byDefault;

	it("opens the project row when projects come first", () => {
		const target = projectRevealTarget({ project: evener, pages: undefined, sources: [LOCAL, PARK], organizeBy: "project-host" });
		expect(target).toEqual({ unfold: ["projects", "project:evener"], scrollTo: "projects/project:evener" });
		expect(items({ organizeBy: "project-host", projects: [evener], isFolded: unfoldOnly(target) }).some((item) => item.key === target.scrollTo)).toBe(true);
	});

	it("opens the canonical copy of a project on several hosts when hosts come first", () => {
		const loaded = pages([session("paradise-park:b", "paradise-park")]);
		const target = projectRevealTarget({ project: evener, pages: loaded, sources: [LOCAL, PARK], organizeBy: "host-project" });
		expect(target).toEqual({
			unfold: ["projects", "host:paradise-park", "project:evener@paradise-park"],
			scrollTo: "projects/project:evener@paradise-park",
		});
		expect(
			items({ projects: [evener], pages: new Map([["evener", loaded]]), isFolded: unfoldOnly(target) }).some((item) => item.key === target.scrollTo),
		).toBe(true);
		expect(projectRevealTarget({ project: evener, pages: undefined, sources: [LOCAL, PARK], organizeBy: "host-project" }).scrollTo).toBe(
			"projects/project:evener@local",
		);
	});

	it("opens the project row on a hub with one host, whatever Organize by says", () => {
		expect(projectRevealTarget({ project: evener, pages: undefined, sources: [LOCAL], organizeBy: "host-project" }).scrollTo).toBe(
			"projects/project:evener",
		);
	});
});

describe("what the Board reads", () => {
	it("reads the pages of every project shown unfolded, under whichever host", () => {
		const list = items({ projects: [evener], isFolded: (fold) => fold === "project:evener@local" });
		expect([...expandedProjectKeys(list)]).toEqual(["evener"]);
		expect([...expandedProjectKeys(items({ projects: [evener], isFolded: () => true }))]).toEqual([]);
	});

	it("loads each visible more row's page once", () => {
		expect(
			morePagesToLoad([
				{ kind: "more", projectKey: "a", tier: "current" },
				{ kind: "more", projectKey: "a", tier: "current" },
				{ kind: "session" },
				{ kind: "more", projectKey: "a", tier: "archived" },
			]),
		).toEqual([
			{ projectKey: "a", tier: "current" },
			{ projectKey: "a", tier: "archived" },
		]);
	});

	it("counts a host's live rows once each, only when every Live page is loaded", () => {
		const rows = [session("local:a"), session("paradise-park:b", "paradise-park"), session("paradise-park:c", "paradise-park"), session("paradise-park:c", "paradise-park")];
		const counts = liveCountsByHost(rows, true);
		expect([counts("local"), counts("paradise-park"), counts("devbox")]).toEqual([1, 2, null]);
		expect(liveCountsByHost(rows, false)("local")).toBeNull();
	});
});

describe("keeping rows through a reconnect (part 1 Review Focus 1)", () => {
	const pageState = (rows: NavigationSessionSummary[], loaded = true) => ({
		loaded,
		truncated: false,
		rows,
		remaining: 0,
		loading: !loaded,
		error: null,
		stale: false,
	});
	function snapshot(catalogLoaded: boolean, groups: ProjectBrowserSnapshot["groups"] = []): ProjectBrowserSnapshot {
		return {
			projects: { ...pageState([], catalogLoaded), rows: catalogLoaded ? [project("a")] : [] },
			groups,
		};
	}
	const retained: ProjectsView = {
		projects: [project("a")],
		loaded: true,
		pages: new Map([["a", pages([session("local:old")], [session("local:older")])]]),
	};

	it("shows the earlier connection's projects and rows until the new reads land", () => {
		expect(projectsView(snapshot(false), retained)).toEqual(retained);
		const view = projectsView(
			snapshot(true, [
				{
					project: project("a"),
					expanded: true,
					current: pageState([session("local:new")]),
					recent: pageState([], false),
					archived: pageState([], false),
					sessions: [],
				},
			]),
			retained,
		);
		expect(view.projects.map((row) => row.key)).toEqual(["a"]);
		expect(view.pages.get("a")?.current.rows.map((row) => row.ref)).toEqual(["local:new"]);
		expect(view.pages.get("a")?.recent.rows.map((row) => row.ref)).toEqual(["local:older"]);
	});

	it("shows a first read as it is when nothing was shown before", () => {
		expect(projectsView(snapshot(false), null)).toEqual({ projects: [], loaded: false, pages: new Map() });
	});
});
