import { describe, expect, it } from "vitest";
import { LocationRepository, locationForRoute, restoredStack, routeToSave } from "./location";

function storage() {
	const values = new Map<string, string>();
	return {
		getItemSync: (key: string) => values.get(key) ?? null,
		setItemSync: (key: string, value: string) => {
			values.set(key, value);
		},
		removeItemSync: (key: string) => {
			values.delete(key);
		},
	};
}
describe("last mobile location", () => {
	it("restores a saved shortcuts location, which is no longer a place, to that hub's Board", () => {
		const disk = storage();
		const editor = { actionId: "composer.focus", chord: "Meta+Shift+" };
		for (const keybindings of [{ editor }, {}, true]) {
			disk.setItemSync("evener.last-location", JSON.stringify({ hubId: "studio", keybindings }));
			const saved = new LocationRepository(disk).read(["studio"]);
			expect(saved).toEqual({ hubId: "studio" });
			expect(restoredStack(saved).routes).toEqual([{ name: "Hubs" }, { name: "Sessions" }]);
		}
	});
	it("restores project filters and the exact project tier after restart", () => {
		for (const archived of [false, true])
			for (const tier of ["current", "recent", "archived"]) {
				const disk = storage(),
					repository = new LocationRepository(disk);
				const params = {
					hubId: "studio",
					projectKey: "project",
					title: "Workspace",
					archived,
					tier,
				};
				repository.save(locationForRoute({ name: "Project", params }, "studio"));
				const saved = new LocationRepository(disk).read(["studio"]);
				expect(restoredStack(saved).routes).toEqual([
					{ name: "Hubs" },
					{ name: "Sessions" },
					{ name: "Projects", params: { hubId: "studio", archived } },
					{ name: "Project", params },
				]);
			}
		const disk = storage(),
			repository = new LocationRepository(disk);
		repository.save(locationForRoute({ name: "Projects", params: { hubId: "studio", archived: true } }, "studio"));
		expect(restoredStack(repository.read(["studio"])).routes.at(-1)).toEqual({
			name: "Projects",
			params: { hubId: "studio", archived: true },
		});
		expect(
			locationForRoute(
				{
					name: "Project",
					params: { hubId: "studio", projectKey: "p", title: "Workspace" },
				},
				"studio",
			),
		).toMatchObject({
			projects: { archived: false, project: { tier: "current" } },
		});
	});
	it("rejects corrupt project filters and mixed destinations", () => {
		const disk = storage(),
			repository = new LocationRepository(disk);
		for (const extra of [
			{ conversation: { ref: "local:s", title: "Session" } },
			{ pinned: {} },
			{ pinAssignment: true },
			{ deleteSession: true },
			{ fork: { instanceId: "i", entryIndex: 1, preview: "p" } },
		]) {
			disk.setItemSync(
				"evener.last-location",
				JSON.stringify({
					hubId: "studio",
					projects: { archived: false },
					...extra,
				}),
			);
			expect(repository.read(["studio"])).toBeNull();
		}
		for (const extra of [{ archived: "yes" }, { tier: "other" }, { projectKey: "" }]) {
			expect(
				locationForRoute(
					{
						name: "Project",
						params: {
							hubId: "studio",
							projectKey: "p",
							title: "Workspace",
							...extra,
						},
					},
					"studio",
				),
			).toBeNull();
		}
		expect(locationForRoute({ name: "Projects", params: { hubId: "other" } }, "studio")).toBeNull();
	});
	it("restores deletion review without confirming or sending it", () => {
		const disk = storage(),
			repository = new LocationRepository(disk);
		const params = {
			hubId: "studio",
			ref: "local:034Kc9793pXlhHyCRXdeAk",
			title: "Saved session",
		};
		repository.save(locationForRoute({ name: "SessionDeletion", params }, "studio"));
		const saved = new LocationRepository(disk).read(["studio"]);
		expect(saved).toMatchObject({
			hubId: "studio",
			deleteSession: true,
			conversation: { ref: params.ref },
		});
		expect(restoredStack(saved).routes.at(-1)).toEqual({
			name: "SessionDeletion",
			params,
		});
		expect(locationForRoute({ name: "SessionDeletion", params }, "other")).toBeNull();
		for (const extra of [
			{ pinned: {} },
			{ pinAssignment: true },
			{ fork: { instanceId: "i", entryIndex: 1, preview: "x" } },
			{ conversation: undefined },
			{ deleteSession: false },
		]) {
			disk.setItemSync("evener.last-location", JSON.stringify({ ...saved, ...extra }));
			expect(repository.read(["studio"])).toBeNull();
		}
	});
	it("restores a selected fork message over its exact parent after restart", () => {
		const disk = storage();
		const params = {
			hubId: "studio",
			ref: "same/ref",
			title: "Build",
			instanceId: "instance-1",
			entryIndex: 3,
			preview: "Selected input",
		};
		new LocationRepository(disk).save(locationForRoute({ name: "Fork", params }, "studio"));
		const saved = new LocationRepository(disk).read(["studio"]);
		expect(saved).toEqual({
			hubId: "studio",
			conversation: { ref: "same/ref", title: "Build" },
			fork: {
				instanceId: "instance-1",
				entryIndex: 3,
				preview: "Selected input",
			},
		});
		const stack = restoredStack(saved);
		expect(stack.index).toBe(3);
		expect(stack.routes.map((route) => route.name)).toEqual(["Hubs", "Sessions", "Conversation", "Fork"]);
		expect(stack.routes.at(-1)?.params).toEqual(params);
		expect(new LocationRepository(disk).read(["other"])).toBeNull();
		expect(locationForRoute({ name: "Fork", params }, "other")).toBeNull();
	});
	it("rejects invalid fork targets and mixed destinations from disk and navigation", () => {
		const valid = { instanceId: "i", entryIndex: 2, preview: "Input" };
		const disk = storage();
		const repository = new LocationRepository(disk);
		for (const fork of [
			null,
			[],
			{ ...valid, instanceId: " " },
			{ ...valid, entryIndex: 0 },
			{ ...valid, entryIndex: -1 },
			{ ...valid, entryIndex: 1.5 },
			{ ...valid, entryIndex: Number.MAX_SAFE_INTEGER + 1 },
			{ ...valid, preview: 4 },
		]) {
			disk.setItemSync(
				"evener.last-location",
				JSON.stringify({
					hubId: "studio",
					conversation: { ref: "r", title: "Build" },
					fork,
				}),
			);
			expect(repository.read(["studio"])).toBeNull();
			if (fork && !Array.isArray(fork))
				expect(
					locationForRoute(
						{
							name: "Fork",
							params: { hubId: "studio", ref: "r", title: "Build", ...fork },
						},
						"studio",
					),
				).toBeNull();
		}
		for (const extra of [
			{},
			{ conversation: { ref: "r", title: "Build" }, pinned: {} },
			{ conversation: { ref: "r", title: "Build" }, pinAssignment: true },
		]) {
			disk.setItemSync("evener.last-location", JSON.stringify({ hubId: "studio", fork: valid, ...extra }));
			expect(repository.read(["studio"])).toBeNull();
		}
	});
	it.each(["PinSections", "PinnedSection", "PinSectionEditor"])(
		"restores %s with its hub and parent destinations",
		(name) => {
			const disk = storage();
			const params =
				name === "PinSections" ? { hubId: "studio" } : { hubId: "studio", sectionId: "section/a", title: "Focus" };
			new LocationRepository(disk).save(locationForRoute({ name, params }, "studio"));
			const saved = new LocationRepository(disk).read(["studio"]);
			const expected = ["Hubs", "Sessions", "PinSections"];
			if (name !== "PinSections") expected.push("PinnedSection");
			if (name === "PinSectionEditor") expected.push("PinSectionEditor");
			const stack = restoredStack(saved);
			expect(stack.index).toBe(expected.length - 1);
			expect(stack.routes.map((route) => route.name)).toEqual(expected);
			expect(stack.routes.at(-1)?.params).toEqual(params);
			expect(new LocationRepository(disk).read(["other"])).toBeNull();
			expect(locationForRoute({ name, params: { ...params, hubId: "other" } }, "studio")).toBeNull();
		},
	);
	it("rejects malformed, mixed, and incomplete pinned destinations", () => {
		for (const pinned of [
			null,
			[],
			{ manage: true },
			{ section: { id: "", title: "Focus" } },
			{ section: { id: "a", title: 42 } },
			{ manage: false },
		]) {
			const disk = storage();
			disk.setItemSync("evener.last-location", JSON.stringify({ hubId: "studio", pinned }));
			expect(new LocationRepository(disk).read(["studio"])).toBeNull();
		}
		const disk = storage();
		disk.setItemSync(
			"evener.last-location",
			JSON.stringify({
				hubId: "studio",
				pinned: {},
				conversation: { ref: "a", title: "Session" },
			}),
		);
		expect(new LocationRepository(disk).read(["studio"])).toBeNull();
		expect(
			locationForRoute(
				{
					name: "PinSectionEditor",
					params: { hubId: "studio", title: "Focus" },
				},
				"studio",
			),
		).toBeNull();
	});
	it("restores a pin proposal screen over its exact conversation without replaying an action", () => {
		const disk = storage();
		const saved = locationForRoute(
			{
				name: "PinAssignment",
				params: { hubId: "studio", ref: "same/ref", title: "Build" },
			},
			"studio",
		);
		new LocationRepository(disk).save(saved);
		const restored = new LocationRepository(disk).read(["studio"]);
		const stack = restoredStack(restored);
		expect(stack.routes.map((route) => route.name)).toEqual(["Hubs", "Sessions", "Conversation", "PinAssignment"]);
		expect(stack.routes[3]?.params).toEqual({
			hubId: "studio",
			ref: "same/ref",
			title: "Build",
		});
		expect(
			locationForRoute(
				{
					name: "PinAssignment",
					params: { hubId: "other", ref: "same/ref", title: "Build" },
				},
				"studio",
			),
		).toBeNull();
	});
	it("restores the exact saved hub and conversation through a new repository", () => {
		const disk = storage();
		const location = {
			hubId: "studio",
			conversation: { ref: "same/ref", title: "Build 🛠" },
		};
		new LocationRepository(disk).save(location);
		expect(new LocationRepository(disk).read(["studio", "other"])).toEqual(location);
		expect(new LocationRepository(disk).read(["other"])).toBeNull();
		const stack = restoredStack(location);
		expect(stack.index).toBe(2);
		expect(stack.routes.map((route) => route.name)).toEqual(["Hubs", "Sessions", "Conversation"]);
		expect(stack.routes[2]?.params).toEqual({
			hubId: "studio",
			ref: "same/ref",
			title: "Build 🛠",
		});
	});
	it("saves a session Next opened as any other, so a relaunch restores it plainly (ruling 2)", () => {
		expect(
			locationForRoute(
				{
					name: "Conversation",
					params: { hubId: "studio", ref: "same/ref", title: "Build", openedBy: "next" },
				},
				"studio",
			),
		).toEqual({ hubId: "studio", conversation: { ref: "same/ref", title: "Build" } });
	});
	it("saves a session the title's swipe opened as any other, so a relaunch doesn't slide it in (spec 6)", () => {
		expect(
			locationForRoute(
				{
					name: "Conversation",
					params: { hubId: "studio", ref: "same/ref", title: "Build", slideFrom: "left" },
				},
				"studio",
			),
		).toEqual({ hubId: "studio", conversation: { ref: "same/ref", title: "Build" } });
	});
	it("returning to Hubs clears the saved destination", () => {
		const repo = new LocationRepository(storage());
		repo.save({ hubId: "studio" });
		repo.save(locationForRoute({ name: "Hubs" }, "studio"));
		expect(repo.read(["studio"])).toBeNull();
		expect(restoredStack(null)).toEqual({
			index: 0,
			routes: [{ name: "Hubs" }],
		});
	});
	it("does not restore a new-session form or a conversation from another selected hub", () => {
		expect(locationForRoute({ name: "NewSession", params: { hubId: "studio" } }, "studio")).toEqual({
			hubId: "studio",
		});
		expect(
			locationForRoute(
				{
					name: "Conversation",
					params: { hubId: "other", ref: "same/ref", title: "Other" },
				},
				"studio",
			),
		).toBeNull();
		expect(locationForRoute({ name: "Sessions" }, null)).toBeNull();
	});
	it("reopens the Board, not the Hub sheet, after a relaunch (ruling 27)", () => {
		const board = { key: "board", name: "Sessions" };
		const sheet = {
			key: "hub",
			name: "Hub",
			params: { screen: "HubHome", params: { hubId: "studio" } },
		};
		const saved = routeToSave({ index: 1, routes: [board, sheet] });
		expect(saved && locationForRoute(saved, "studio")).toEqual({ hubId: "studio" });
	});
	it("rejects malformed or unsupported persisted values", () => {
		for (const raw of ["{", "null", "[]", '{"hubId":4}', '{"hubId":"studio","conversation":{"ref":42}}']) {
			const repo = new LocationRepository({
				...storage(),
				getItemSync: () => raw,
			});
			expect(repo.read(["studio"])).toBeNull();
		}
	});
	it("propagates actual storage failures so the UI can explain them", () => {
		const failure = new Error("disk unavailable");
		const repo = new LocationRepository({
			...storage(),
			getItemSync: () => {
				throw failure;
			},
		});
		expect(() => repo.read(["studio"])).toThrow(failure);
	});
	it("reopens the screen under a sheet, never the sheet", () => {
		const session = {
			key: "conversation",
			name: "Conversation",
			params: { hubId: "studio", ref: "local:s1", title: "Fix it" },
		};
		const tasks = {
			key: "tasks",
			name: "TasksSheet",
			params: { hubId: "studio", ref: "local:s1", threadId: "s1", hasTasks: true },
		};
		const hubs = { key: "hubs", name: "Hubs" };
		const saved = routeToSave({ index: 2, routes: [hubs, session, tasks] });
		expect(saved).toBe(session);
		expect(saved && locationForRoute(saved, "studio")).toEqual({
			hubId: "studio",
			conversation: { ref: "local:s1", title: "Fix it" },
		});
		expect(routeToSave({ index: 1, routes: [hubs, session] })).toBe(session);
		expect(routeToSave({ index: 0, routes: [tasks] })).toBeUndefined();
	});

	it("reopens a document over its session after a relaunch", () => {
		const disk = storage();
		const repository = new LocationRepository(disk);
		const params = {
			hubId: "studio",
			sessionRef: "local:fix",
			path: "docs/superpowers/plans/settle.md",
			sessionTitle: "Fix race",
			updatedAt: "2026-09-26T11:39:00.000Z",
		};
		repository.save(locationForRoute({ name: "Reader", params }, "studio"));
		const saved = new LocationRepository(disk).read(["studio"]);
		expect(saved).toEqual({
			hubId: "studio",
			conversation: { ref: "local:fix", title: "Fix race" },
			reader: {
				sessionRef: "local:fix",
				path: "docs/superpowers/plans/settle.md",
				updatedAt: "2026-09-26T11:39:00.000Z",
			},
		});
		expect(restoredStack(saved).routes.slice(-3)).toEqual([
			{ name: "Sessions" },
			{ name: "Conversation", params: { hubId: "studio", ref: "local:fix", title: "Fix race" } },
			{ name: "Reader", params },
		]);
	});

	it("reopens a coordinator's Subagents list as its session after a relaunch (ruling 21)", () => {
		const disk = storage();
		const repository = new LocationRepository(disk);
		const params = { hubId: "studio", ref: "local:coord", threadId: "coord", title: "Coordinator" };
		const location = locationForRoute({ name: "Subagents", params }, "studio");
		expect(location).toEqual({ hubId: "studio", conversation: { ref: "local:coord", title: "Coordinator" } });
		repository.save(location);
		const saved = new LocationRepository(disk).read(["studio"]);
		expect(restoredStack(saved).routes.slice(-2)).toEqual([
			{ name: "Sessions" },
			{ name: "Conversation", params: { hubId: "studio", ref: "local:coord", title: "Coordinator" } },
		]);
	});

	it("reopens a subagent's session as its coordinator's after a relaunch (ruling 21)", () => {
		const params = {
			hubId: "studio",
			ref: "local:fix",
			title: "Fix race in tree settle",
			coordinator: { ref: "local:coord", threadId: "coord", title: "Coordinator" },
		};
		expect(locationForRoute({ name: "Subagent", params }, "studio")).toEqual({
			hubId: "studio",
			conversation: { ref: "local:coord", title: "Coordinator" },
		});
	});

	it("refuses a document with no session, an empty path, or mixed with another destination", () => {
		const disk = storage();
		const repository = new LocationRepository(disk);
		const conversation = { ref: "local:coord", title: "Coordinator" };
		for (const value of [
			{ hubId: "studio", reader: { sessionRef: "local:fix", path: "a.md" } },
			{ hubId: "studio", conversation, reader: { sessionRef: "local:fix", path: "" } },
			{ hubId: "studio", conversation, reader: { sessionRef: "local:fix", path: "a.md" }, pinAssignment: true },
		]) {
			disk.setItemSync("evener.last-location", JSON.stringify(value));
			expect(repository.read(["studio"])).toBeNull();
		}
		expect(
			locationForRoute(
				{ name: "Reader", params: { hubId: "studio", sessionRef: "local:fix", path: "a.md" } },
				"studio",
			),
		).toBeNull();
	});

	it("refuses Reader params that predate the single-ref route", () => {
		// A pre-#2871 Reader named its session twice; the route now takes one ref
		// and its title, so these params have no sessionTitle and are rejected.
		expect(
			locationForRoute(
				{
					name: "Reader",
					params: {
						hubId: "studio",
						sessionRef: "local:fix",
						path: "a.md",
						reviewRef: "local:fix",
						reviewTitle: "Fix race",
					},
				},
				"studio",
			),
		).toBeNull();
	});
});
