import { describe, expect, it } from "vitest";
import {
	LocationRepository,
	locationForRoute,
	restoredStack,
} from "./location";

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
		new LocationRepository(disk).save(
			locationForRoute({ name: "Fork", params }, "studio"),
		);
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
		expect(stack.routes.map((route) => route.name)).toEqual([
			"Hubs",
			"Sessions",
			"Conversation",
			"Fork",
		]);
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
			disk.setItemSync(
				"evener.last-location",
				JSON.stringify({ hubId: "studio", fork: valid, ...extra }),
			);
			expect(repository.read(["studio"])).toBeNull();
		}
	});
	it.each(["PinSections", "PinnedSection", "PinSectionEditor"])(
		"restores %s with its hub and parent destinations",
		(name) => {
			const disk = storage();
			const params =
				name === "PinSections"
					? { hubId: "studio" }
					: { hubId: "studio", sectionId: "section/a", title: "Focus" };
			new LocationRepository(disk).save(
				locationForRoute({ name, params }, "studio"),
			);
			const saved = new LocationRepository(disk).read(["studio"]);
			const expected = ["Hubs", "Sessions", "PinSections"];
			if (name !== "PinSections") expected.push("PinnedSection");
			if (name === "PinSectionEditor") expected.push("PinSectionEditor");
			const stack = restoredStack(saved);
			expect(stack.index).toBe(expected.length - 1);
			expect(stack.routes.map((route) => route.name)).toEqual(expected);
			expect(stack.routes.at(-1)?.params).toEqual(params);
			expect(new LocationRepository(disk).read(["other"])).toBeNull();
			expect(
				locationForRoute(
					{ name, params: { ...params, hubId: "other" } },
					"studio",
				),
			).toBeNull();
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
			disk.setItemSync(
				"evener.last-location",
				JSON.stringify({ hubId: "studio", pinned }),
			);
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
		expect(stack.routes.map((route) => route.name)).toEqual([
			"Hubs",
			"Sessions",
			"Conversation",
			"PinAssignment",
		]);
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
		expect(new LocationRepository(disk).read(["studio", "other"])).toEqual(
			location,
		);
		expect(new LocationRepository(disk).read(["other"])).toBeNull();
		const stack = restoredStack(location);
		expect(stack.index).toBe(2);
		expect(stack.routes.map((route) => route.name)).toEqual([
			"Hubs",
			"Sessions",
			"Conversation",
		]);
		expect(stack.routes[2]?.params).toEqual({
			hubId: "studio",
			ref: "same/ref",
			title: "Build 🛠",
		});
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
		expect(
			locationForRoute(
				{ name: "NewSession", params: { hubId: "studio" } },
				"studio",
			),
		).toEqual({ hubId: "studio" });
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
	it("rejects malformed or unsupported persisted values", () => {
		for (const raw of [
			"{",
			"null",
			"[]",
			'{"hubId":4}',
			'{"hubId":"studio","conversation":{"ref":42}}',
		]) {
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
});
