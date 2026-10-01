import { describe, expect, it } from "vitest";
import type { NavigationInvalidationTarget } from "@evener/appwire-client";
import { boundary, type Hub, response, tick } from "./board/navigationHubTestUtils";
import { createProjectBrowserController, type ProjectCatalog, type ProjectSessionTier } from "./projectBrowser";

type Pending = Hub["requests"][number];
const project = (key: string) => ({ key, name: key, session_count: 2 });
const session = (ref: string) => ({
	ref,
	host_id: "local",
	session_id: ref,
	title: ref,
	project: "p",
	state: "idle",
	kind: "session",
	live: false,
	children: [],
});
type Row = ReturnType<typeof session>;
const ARCHIVED_LIST = "evener/archived/list";
/** Answers one project's tier reads, each with its own rows and remaining:
 * Today and Recent from navigation, Archived from the archived list. */
function answerTiers(
	pending: readonly (Pending | undefined)[],
	rows: Partial<Record<ProjectSessionTier, Row[]>> = {},
	remaining: Partial<Record<ProjectSessionTier, number>> = {},
	revision = 1,
) {
	for (const request of pending) {
		if (!request) continue;
		if (request.method === ARCHIVED_LIST) {
			const sessions = rows.archived ?? [];
			const more = remaining.archived ?? 0;
			request.resolve({
				sessions,
				total: sessions.length + more,
				...(more > 0 ? { nextCursor: "next" } : {}),
			} as never);
			continue;
		}
		const tier = request.params.tier as ProjectSessionTier;
		request.resolve(
			response(
				request.params,
				{
					key: request.params.projectKey,
					tier,
					sessions: rows[tier] ?? [],
					remaining: remaining[tier] ?? 0,
					truncated: false,
				},
				revision,
			),
		);
	}
}
const label = (request: Pending | undefined) =>
	request?.method === ARCHIVED_LIST
		? `${request.params.projectKey}:archived list`
		: `${request?.params.projectKey ?? "catalog"}:${request?.params.tier ?? ""}`;
/** A controller whose catalog holds project "a", expanded, with its tiers
 * answered with these rows and remaining counts. It reads `catalog` from
 * `fake`, a new hub unless given one; `reads` are the reads expanding made. */
async function loadedProject(
	rows: Partial<Record<ProjectSessionTier, Row[]>> = { current: [session("a1")] },
	remaining: Partial<Record<ProjectSessionTier, number>> = {},
	{ catalog, fake = boundary() }: { catalog?: ProjectCatalog; fake?: Hub } = {},
) {
	const { client, requests, listeners } = fake;
	const controller = createProjectBrowserController(client, catalog);
	const loading = controller.initialLoad();
	const catalogRead = requests[requests.length - 1];
	catalogRead?.resolve(response(catalogRead.params, { projects: [project("a")], remaining: 0 }));
	await loading;
	const from = requests.length;
	const expanding = controller.expand("a");
	const reads = requests.slice(from);
	answerTiers(reads, rows, remaining);
	await expanding;
	const invalidate = (target: NavigationInvalidationTarget, sequence = 1) => {
		for (const listener of listeners)
			listener({
				method: "evener/navigation/invalidated",
				params: { generationId: "generation-test", sequence, targets: [target] },
			});
	};
	return { controller, requests, reads, invalidate };
}

describe("project browser", () => {
	it("reads the catalog it is made for, the projects catalog by default", () => {
		for (const catalog of [undefined, "projects", "archived_projects", "test_runs"] as const) {
			const { client, requests } = boundary();
			const controller = createProjectBrowserController(client, catalog);
			void controller.initialLoad();
			expect(requests.map((request) => [request.params.resource, request.params.catalog])).toEqual([
				["catalog", catalog ?? "projects"],
			]);
			controller.dispose();
		}
	});

	it("loads the catalog alone, and a project's three tiers once it is expanded", async () => {
		const { client, requests, listeners } = boundary();
		const controller = createProjectBrowserController(client);
		expect(listeners.size).toBe(0);
		const loading = controller.initialLoad();
		expect(listeners.size).toBe(1);
		requests[0]?.resolve(response(requests[0].params, { projects: [project("a"), project("b")], remaining: 0 }));
		await loading;
		expect(requests).toHaveLength(1);
		expect(controller.getSnapshot().groups).toEqual([]);
		const expanding = controller.expand("a");
		expect(requests.map(label)).toEqual(["catalog:", "a:current", "a:recent", "a:archived list"]);
		answerTiers(requests.slice(1), {
			current: [session("a1")],
			recent: [session("a2")],
			archived: [session("a0")],
		});
		await expanding;
		const group = controller.getSnapshot().groups[0];
		expect(group?.expanded).toBe(true);
		expect(group?.sessions.map((row) => row.ref)).toEqual(["a1", "a2"]);
		expect(group?.archived.rows.map((row) => row.ref)).toEqual(["a0"]);
		expect(listeners.size).toBe(4);
		controller.dispose();
		expect(listeners.size).toBe(0);
	});

	// Navigation serves no archived rows: a project's Archived fold reads the
	// archived list of the catalog its section shows.
	it("reads an expanded project's archived sessions from its section's archived list", async () => {
		for (const catalog of ["projects", "archived_projects", "test_runs"] as const) {
			const { controller, requests } = await loadedProject({ archived: [session("a0")] }, { archived: 3 }, { catalog });
			expect(requests.filter((request) => request.method === ARCHIVED_LIST).map((request) => request.params)).toEqual([
				{ catalog, projectKey: "a" },
			]);
			expect(controller.getSnapshot().groups[0]?.archived).toMatchObject({
				loaded: true,
				remaining: 3,
				rows: [{ ref: "a0" }],
			});
			controller.dispose();
		}
	});

	it("pages a project's archived list on with its cursor", async () => {
		const { controller, requests } = await loadedProject({ archived: [session("a0")] }, { archived: 1 });
		const more = controller.loadMoreSessions("a", "archived");
		expect(requests.slice(4).map((request) => [label(request), request.params.cursor])).toEqual([
			["a:archived list", "next"],
		]);
		answerTiers(requests.slice(4), { archived: [session("a1")] });
		await more;
		expect(controller.getSnapshot().groups[0]?.archived).toMatchObject({
			remaining: 0,
			rows: [{ ref: "a0" }, { ref: "a1" }],
		});
		controller.dispose();
	});

	// The archived list outlives the browser that read it, unwatched once that
	// browser is gone, so the next one shows its rows and reads it again.
	it("reads again an archived list an earlier browser left loaded", async () => {
		const fake = boundary();
		const first = await loadedProject({ archived: [session("a0")] }, {}, { fake });
		first.controller.dispose();
		const second = await loadedProject({ archived: [session("a0")] }, {}, { fake });
		expect(second.reads.map(label)).toEqual(["a:archived list", "a:current", "a:recent"]);
		expect(second.controller.getSnapshot().groups[0]?.archived.rows.map((row) => row.ref)).toEqual(["a0"]);
		second.controller.dispose();
	});

	it("collapse keeps a project's rows and stops it loading more", async () => {
		const { controller, requests } = await loadedProject({ current: [session("a1")] }, { current: 5 });
		controller.collapse("a");
		await controller.loadMoreSessions("a", "current");
		expect(requests).toHaveLength(4);
		expect(controller.getSnapshot().groups[0]).toMatchObject({
			expanded: false,
			current: { rows: [{ ref: "a1" }] },
		});
		controller.dispose();
	});

	it("deduplicates equal session refs between current and recent tiers", async () => {
		const { controller } = await loadedProject({ current: [session("same")], recent: [session("same")] });
		expect(controller.getSnapshot().groups[0]?.sessions).toHaveLength(1);
		controller.dispose();
	});

	it("returns a stable snapshot and ignores a late catalog result after dispose", async () => {
		const { client, requests } = boundary();
		const controller = createProjectBrowserController(client);
		const loading = controller.initialLoad();
		controller.dispose();
		requests[0]?.resolve(response(requests[0].params, { projects: [project("late")], remaining: 0 }));
		await loading;
		const disposedSnapshot = controller.getSnapshot();
		expect(controller.getSnapshot()).toBe(disposedSnapshot);
		expect(disposedSnapshot.projects.rows).toHaveLength(0);
		expect(disposedSnapshot.groups).toHaveLength(0);
	});

	it("appends a 20-row session page once and keeps rows when the next page fails", async () => {
		const { controller, requests } = await loadedProject(
			{ current: Array.from({ length: 20 }, (_, i) => session(`s${i}`)) },
			{ current: 5 },
		);
		const more = controller.loadMoreSessions("a", "current");
		const duplicate = controller.loadMoreSessions("a", "current");
		await tick();
		expect(requests).toHaveLength(5);
		requests[4]?.reject(new Error("page unavailable"));
		await Promise.all([more, duplicate]);
		expect(controller.getSnapshot().groups[0]?.sessions).toHaveLength(20);
		await controller.loadMoreSessions("a", "current");
		expect(requests).toHaveLength(5);
		const retry = controller.retry("a", "current");
		await tick();
		expect(requests).toHaveLength(6);
		expect(requests[5]?.params.offset).toBe(20);
		expect(requests[5]?.params.limit).toBe(20);
		requests[5]?.resolve(
			response(requests[5].params, {
				key: "a",
				tier: "current",
				sessions: ["s20", "s21", "s22", "s23", "s24"].map(session),
				remaining: 0,
				truncated: false,
			}),
		);
		await retry;
		expect(controller.getSnapshot().groups[0]?.sessions).toHaveLength(25);
		controller.dispose();
	});

	it("re-reads an invalidated project's three tiers without a refresh call", async () => {
		const { controller, requests, invalidate } = await loadedProject();
		invalidate({ kind: "project", projectKey: "a", revision: 2 });
		expect(requests).toHaveLength(7);
		expect(requests.slice(4).map((r) => [label(r), r.params.offset ?? r.params.cursor])).toEqual([
			["a:current", 0],
			["a:recent", 0],
			["a:archived list", undefined],
		]);
		expect(controller.getSnapshot().groups[0]?.current).toMatchObject({
			stale: true,
			loading: true,
			rows: [{ ref: "a1" }],
		});
		answerTiers(requests.slice(4), { current: [session("a1"), session("a3")] }, {}, 2);
		await tick();
		expect(controller.getSnapshot().groups[0]?.current).toMatchObject({
			stale: false,
			loading: false,
			error: null,
		});
		expect(controller.getSnapshot().groups[0]?.sessions.map((row) => row.ref)).toEqual(["a1", "a3"]);
		controller.dispose();
	});

	it("retries a session page whose re-read failed by reading it again from the top", async () => {
		const { controller, requests, invalidate } = await loadedProject();
		invalidate({ kind: "project", projectKey: "a", revision: 2 });
		for (const request of requests.slice(4)) request.reject(new Error("offline"));
		await tick();
		expect(controller.getSnapshot().groups[0]?.current).toMatchObject({
			stale: true,
			error: "offline",
		});
		const retry = controller.retry("a", "current");
		await tick();
		expect(requests).toHaveLength(8);
		expect(requests[7]?.params).toMatchObject({ tier: "current", offset: 0 });
		answerTiers(requests.slice(7), { current: [session("a9")] }, {}, 2);
		await retry;
		expect(controller.getSnapshot().groups[0]?.current).toMatchObject({
			stale: false,
			error: null,
			rows: [{ ref: "a9" }],
		});
		controller.dispose();
	});

	it("retries a project catalog whose re-read failed by reading it again from the top", async () => {
		const { controller, requests, invalidate } = await loadedProject();
		invalidate({ kind: "catalog", catalog: "projects", revision: 2 });
		expect(requests).toHaveLength(5);
		requests[4]?.reject(new Error("offline"));
		await tick();
		expect(controller.getSnapshot().projects).toMatchObject({ stale: true, error: "offline" });
		const retry = controller.retry();
		await tick();
		expect(requests).toHaveLength(6);
		expect(requests[5]?.params).toMatchObject({ resource: "catalog", offset: 0 });
		requests[5]?.resolve(response(requests[5].params, { projects: [project("a"), project("b")], remaining: 0 }, 2));
		await retry;
		expect(controller.getSnapshot().projects).toMatchObject({ stale: false, error: null });
		expect(controller.getSnapshot().projects.rows.map((row) => row.key)).toEqual(["a", "b"]);
		controller.dispose();
	});

	it("holds re-reads while paused and catches up on resume", async () => {
		const { controller, requests, invalidate } = await loadedProject();
		controller.pause();
		invalidate({ kind: "project", projectKey: "a", revision: 2 });
		expect(requests).toHaveLength(4);
		expect(controller.getSnapshot().groups[0]?.current.stale).toBe(true);
		controller.resume();
		expect(requests.slice(4).map(label)).toEqual(["a:current", "a:recent", "a:archived list"]);
		controller.dispose();
	});

	it("does not refetch on repeated initialLoad after the catalog is loaded", async () => {
		const { client, requests } = boundary();
		const controller = createProjectBrowserController(client);
		const loading = controller.initialLoad();
		requests[0]?.resolve(response(requests[0].params, { projects: [], remaining: 0 }));
		await loading;
		await controller.initialLoad();
		expect(requests).toHaveLength(1);
		controller.dispose();
	});
});
