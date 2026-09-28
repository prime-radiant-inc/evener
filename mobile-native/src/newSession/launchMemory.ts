// What this phone remembers about starting sessions on one hub (spec 11; S8's
// fallback, ruling 16): recipes, in the order you keep them, and the setups
// sessions were started with, for "Same as last time". Both are per hub,
// because a setup names that hub's hosts and folders.
import type { LaunchConfigLayer } from "@evener/appwire-client";
import { type LaunchSetup, ownedOverrides, type RememberedSetup } from "./launchSetup";

export interface Recipe {
	id: string;
	name: string;
	setup: LaunchSetup;
}

export interface LaunchMemoryStorage {
	getItemSync(key: string): string | null;
	setItemSync(key: string, value: string): void;
	removeItemSync(key: string): void;
}

export const recipesKey = (hubId: string) => `evener.native.recipes.${hubId}`;
export const historyKey = (hubId: string) => `evener.native.launch-history.${hubId}`;

/** Starts remembered per hub: one per host and project, enough for every
 * project in use (the spec's fleet has 14) and small enough to rewrite on
 * every start. */
export const HISTORY_LIMIT = 50;
export const RECIPE_NAME_LIMIT = 40;

function record(value: unknown): Record<string, unknown> | null {
	return value !== null && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : null;
}

/** A stored setup this build can read, or null. Unknown override fields are
 * dropped: only what the sheet owns comes back. */
function toSetup(value: unknown): LaunchSetup | null {
	const setup = record(value);
	const overrides = record(setup?.overrides);
	const model = setup?.model === null ? null : record(setup?.model);
	if (
		!setup ||
		!overrides ||
		typeof setup.host !== "string" ||
		typeof setup.cwd !== "string" ||
		typeof setup.effort !== "string" ||
		(setup.model !== null && (!model || typeof model.provider !== "string" || typeof model.model !== "string"))
	)
		return null;
	const plugins = overrides.enabledPlugins;
	if (plugins !== undefined && !(Array.isArray(plugins) && plugins.every((name) => typeof name === "string"))) return null;
	for (const [field, kind] of [
		["sandbox", "string"],
		["sandboxNet", "boolean"],
		["contextStrategy", "string"],
		["maxSubagentDepth", "number"],
		["maxRounds", "number"],
	] as const)
		if (overrides[field] !== undefined && typeof overrides[field] !== kind) return null;
	return {
		host: setup.host,
		cwd: setup.cwd,
		model: model ? { provider: model.provider as string, model: model.model as string } : null,
		effort: setup.effort,
		// Every owned field was type-checked above; ownedOverrides keeps only them.
		overrides: ownedOverrides(overrides as LaunchConfigLayer),
	};
}

function toRecipe(value: unknown): Recipe | null {
	const recipe = record(value);
	const setup = toSetup(recipe?.setup);
	return recipe && setup && typeof recipe.id === "string" && typeof recipe.name === "string"
		? { id: recipe.id, name: recipe.name, setup }
		: null;
}

function toRemembered(value: unknown): RememberedSetup | null {
	const entry = record(value);
	const setup = toSetup(entry?.setup);
	return entry && setup && typeof entry.at === "number" ? { setup, at: entry.at } : null;
}

function parseList<T>(raw: string | null, item: (value: unknown) => T | null): T[] {
	if (!raw) return [];
	let value: unknown;
	try {
		value = JSON.parse(raw);
	} catch {
		return [];
	}
	if (!Array.isArray(value)) return [];
	return value.flatMap((entry) => {
		const parsed = item(entry);
		return parsed ? [parsed] : [];
	});
}

export class LaunchMemory {
	private recipeList: Recipe[];
	private historyList: RememberedSetup[];
	private revision = 0;
	private readonly listeners = new Set<() => void>();

	constructor(
		private readonly storage: LaunchMemoryStorage,
		private readonly hubId: string,
		private readonly createId: () => string,
	) {
		this.recipeList = parseList(storage.getItemSync(recipesKey(hubId)), toRecipe);
		this.historyList = parseList(storage.getItemSync(historyKey(hubId)), toRemembered);
	}

	recipes(): readonly Recipe[] {
		return this.recipeList;
	}

	history(): readonly RememberedSetup[] {
		return this.historyList;
	}

	getRevision = (): number => this.revision;

	subscribe = (listener: () => void): (() => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};

	/** Saves a setup under a name. A recipe with the same name, ignoring case
	 * and surrounding spaces, takes the new setup and keeps its place (ruling
	 * 16); a new one goes last. */
	saveRecipe(name: string, setup: LaunchSetup): Recipe {
		const trimmed = name.trim();
		const existing = this.recipeList.find((recipe) => recipe.name.toLowerCase() === trimmed.toLowerCase());
		const saved: Recipe = existing ? { ...existing, setup } : { id: this.createId(), name: trimmed, setup };
		this.writeRecipes(
			existing ? this.recipeList.map((recipe) => (recipe.id === saved.id ? saved : recipe)) : [...this.recipeList, saved],
		);
		return saved;
	}

	renameRecipe(id: string, name: string): void {
		this.writeRecipes(this.recipeList.map((recipe) => (recipe.id === id ? { ...recipe, name: name.trim() } : recipe)));
	}

	deleteRecipe(id: string): void {
		this.writeRecipes(this.recipeList.filter((recipe) => recipe.id !== id));
	}

	/** Moves a recipe one place up (-1) or down (1); at an end it stays. */
	moveRecipe(id: string, by: -1 | 1): void {
		const index = this.recipeList.findIndex((recipe) => recipe.id === id);
		const target = index + by;
		if (index < 0 || target < 0 || target >= this.recipeList.length) return;
		const next = [...this.recipeList];
		[next[index], next[target]] = [next[target] as Recipe, next[index] as Recipe];
		this.writeRecipes(next);
	}

	/** Remembers a start (ruling 16): it replaces older starts for the same
	 * host and project, and the oldest fall off past the limit. */
	recordStart(setup: LaunchSetup, at: number): void {
		const others = this.historyList.filter(
			(entry) => entry.setup.host !== setup.host || entry.setup.cwd !== setup.cwd,
		);
		this.writeHistory([{ setup, at }, ...others].slice(0, HISTORY_LIMIT));
	}

	/** Stores first, so a failed write leaves this memory as it was. */
	private writeRecipes(next: Recipe[]): void {
		this.storage.setItemSync(recipesKey(this.hubId), JSON.stringify(next));
		this.recipeList = next;
		this.changed();
	}

	private writeHistory(next: RememberedSetup[]): void {
		this.storage.setItemSync(historyKey(this.hubId), JSON.stringify(next));
		this.historyList = next;
		this.changed();
	}

	private changed(): void {
		this.revision += 1;
		for (const listener of this.listeners) listener();
	}
}

export function recipeNameIssue(name: string): string | null {
	const trimmed = name.trim();
	if (!trimmed) return "Name the recipe.";
	if (Array.from(trimmed).length > RECIPE_NAME_LIMIT) return `Keep it under ${RECIPE_NAME_LIMIT} characters.`;
	return null;
}

export function forgetLaunchMemory(storage: LaunchMemoryStorage, hubId: string): void {
	storage.removeItemSync(recipesKey(hubId));
	storage.removeItemSync(historyKey(hubId));
}
