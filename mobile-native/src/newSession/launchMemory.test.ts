import { describe, expect, it } from "vitest";
import {
	forgetLaunchMemory,
	HISTORY_LIMIT,
	historyKey,
	LaunchMemory,
	recipeNameIssue,
	recipesKey,
} from "./launchMemory";
import type { LaunchSetup } from "./launchSetup";

function memory(values = new Map<string, string>()) {
	return {
		values,
		getItemSync: (key: string) => values.get(key) ?? null,
		setItemSync: (key: string, value: string) => {
			values.set(key, value);
		},
		removeItemSync: (key: string) => {
			values.delete(key);
		},
	};
}
const ids = () => {
	let next = 0;
	return () => `recipe-${++next}`;
};
const setup = (over: Partial<LaunchSetup> = {}): LaunchSetup => ({
	host: "local",
	cwd: "/home/jesse/git/evener",
	model: { provider: "lunaroute", model: "deepseek-4.1-flash" },
	effort: "xhigh",
	overrides: { sandbox: "workspace-write" },
	...over,
});

describe("recipes", () => {
	it("keeps them in order across launches", () => {
		const storage = memory();
		const first = new LaunchMemory(storage, "hub-a", ids());
		first.saveRecipe("Evener coordinator", setup());
		first.saveRecipe("Quick question", setup({ effort: "medium" }));
		const again = new LaunchMemory(storage, "hub-a", ids());
		expect(again.recipes().map((recipe) => recipe.name)).toEqual(["Evener coordinator", "Quick question"]);
		expect(again.recipes()[1]?.setup.effort).toBe("medium");
	});

	it("replaces a recipe saved under the same name, whatever its case, and keeps its place", () => {
		const recipes = new LaunchMemory(memory(), "hub-a", ids());
		const original = recipes.saveRecipe("Evener coordinator", setup());
		recipes.saveRecipe("Quick question", setup({ effort: "medium" }));
		const replaced = recipes.saveRecipe("  evener COORDINATOR ", setup({ effort: "max" }));
		expect(replaced.id).toBe(original.id);
		expect(recipes.recipes().map((recipe) => [recipe.name, recipe.setup.effort])).toEqual([
			["Evener coordinator", "max"],
			["Quick question", "medium"],
		]);
	});

	it("renames, moves and deletes", () => {
		const recipes = new LaunchMemory(memory(), "hub-a", ids());
		const a = recipes.saveRecipe("A", setup());
		const b = recipes.saveRecipe("B", setup());
		recipes.renameRecipe(a.id, " Alpha ");
		recipes.moveRecipe(b.id, -1);
		expect(recipes.recipes().map((recipe) => recipe.name)).toEqual(["B", "Alpha"]);
		recipes.moveRecipe(b.id, -1);
		expect(recipes.recipes().map((recipe) => recipe.name)).toEqual(["B", "Alpha"]);
		recipes.deleteRecipe(b.id);
		expect(recipes.recipes().map((recipe) => recipe.name)).toEqual(["Alpha"]);
	});

	it("leaves itself unchanged when the phone can't store the change", () => {
		const storage = memory();
		const recipes = new LaunchMemory(storage, "hub-a", ids());
		recipes.saveRecipe("A", setup());
		storage.setItemSync = () => {
			throw new Error("disk full");
		};
		expect(() => recipes.saveRecipe("B", setup())).toThrow("disk full");
		expect(recipes.recipes().map((recipe) => recipe.name)).toEqual(["A"]);
	});

	it("says what's wrong with a name", () => {
		expect(recipeNameIssue("  ")).toBe("Name the recipe.");
		expect(recipeNameIssue("x".repeat(41))).toBe("Keep it under 40 characters.");
		expect(recipeNameIssue("Quick question")).toBeNull();
	});
});

describe("starts, for Same as last time", () => {
	it("keeps the newest start per host and project, newest first", () => {
		const starts = new LaunchMemory(memory(), "hub-a", ids());
		starts.recordStart(setup({ effort: "high" }), 1);
		starts.recordStart(setup({ cwd: "/home/jesse/git/docs" }), 2);
		starts.recordStart(setup({ effort: "max" }), 3);
		expect(starts.history().map((entry) => [entry.setup.cwd, entry.setup.effort, entry.at])).toEqual([
			["/home/jesse/git/evener", "max", 3],
			["/home/jesse/git/docs", "xhigh", 2],
		]);
	});

	it("forgets the oldest past the limit", () => {
		const starts = new LaunchMemory(memory(), "hub-a", ids());
		for (let index = 0; index <= HISTORY_LIMIT; index++) starts.recordStart(setup({ cwd: `/p/${index}` }), index);
		expect(starts.history()).toHaveLength(HISTORY_LIMIT);
		expect(starts.history().some((entry) => entry.setup.cwd === "/p/0")).toBe(false);
	});
});

describe("stored values", () => {
	it("drops entries this build can't read and keeps the rest", () => {
		const storage = memory(
			new Map([
				[
					recipesKey("hub-a"),
					JSON.stringify([
						{ id: "bad", name: "Bad", setup: { host: 7 } },
						{ id: "good", name: "Good", setup: setup() },
					]),
				],
				[historyKey("hub-a"), "{not json"],
			]),
		);
		const loaded = new LaunchMemory(storage, "hub-a", ids());
		expect(loaded.recipes().map((recipe) => recipe.id)).toEqual(["good"]);
		expect(loaded.history()).toEqual([]);
	});

	it("tells listeners about each change", () => {
		const recipes = new LaunchMemory(memory(), "hub-a", ids());
		let heard = 0;
		recipes.subscribe(() => {
			heard += 1;
		});
		const revision = recipes.getRevision();
		recipes.saveRecipe("A", setup());
		recipes.recordStart(setup(), 1);
		expect(heard).toBe(2);
		expect(recipes.getRevision()).toBe(revision + 2);
	});

	it("forgets one hub and keeps another's (Review Focus 5)", () => {
		const storage = memory();
		new LaunchMemory(storage, "hub-a", ids()).saveRecipe("A", setup());
		new LaunchMemory(storage, "hub-b", ids()).saveRecipe("B", setup());
		new LaunchMemory(storage, "hub-a", ids()).recordStart(setup(), 1);
		forgetLaunchMemory(storage, "hub-a");
		expect(new LaunchMemory(storage, "hub-a", ids()).recipes()).toEqual([]);
		expect(new LaunchMemory(storage, "hub-a", ids()).history()).toEqual([]);
		expect(new LaunchMemory(storage, "hub-b", ids()).recipes().map((recipe) => recipe.name)).toEqual(["B"]);
	});
});
