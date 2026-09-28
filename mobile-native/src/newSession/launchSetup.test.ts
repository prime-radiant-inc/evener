import { describe, expect, it } from "vitest";
import {
	accessOf,
	effortLabel,
	type LaunchSetup,
	lastSetupFor,
	litChip,
	modelFromId,
	moveToHost,
	networkApplies,
	newestSetup,
	ownedOverrides,
	projectName,
	sameSetup,
	setupOf,
	setupSummary,
	withOwnedOverrides,
} from "./launchSetup";

const setup = (over: Partial<LaunchSetup> = {}): LaunchSetup => ({
	host: "local",
	cwd: "/home/jesse/git/evener",
	model: { provider: "lunaroute", model: "glm-5.3-vision" },
	effort: "xhigh",
	overrides: { enabledPlugins: ["superpowers", "go"], sandbox: "workspace-write" },
	...over,
});

describe("the overrides the sheet owns", () => {
	it("picks only the owned fields, copying lists", () => {
		const plugins = ["superpowers"];
		const owned = ownedOverrides({
			enabledPlugins: plugins,
			sandbox: "read-only",
			maxRounds: 100,
			env: { A: "1" },
			verbose: true,
		});
		expect(owned).toEqual({ enabledPlugins: ["superpowers"], sandbox: "read-only", maxRounds: 100 });
		expect(owned.enabledPlugins).not.toBe(plugins);
	});

	it("replaces the owned fields and keeps everything else", () => {
		expect(withOwnedOverrides({ sandbox: "restricted", maxRounds: 5, env: { A: "1" } }, { sandbox: "read-only" })).toEqual({
			sandbox: "read-only",
			env: { A: "1" },
		});
	});
});

describe("comparing setups", () => {
	it("ignores plugin order", () => {
		const reordered = setup({ overrides: { enabledPlugins: ["go", "superpowers"], sandbox: "workspace-write" } });
		expect(sameSetup(setup(), reordered)).toBe(true);
	});

	const differences: [string, Partial<LaunchSetup>][] = [
		["host", { host: "paradise-park" }],
		["project", { cwd: "/home/jesse/git/docs" }],
		["model", { model: { provider: "meta", model: "muse-spark-1.3" } }],
		["hub default model", { model: null }],
		["effort", { effort: "high" }],
		["plugins", { overrides: { enabledPlugins: ["superpowers"], sandbox: "workspace-write" } }],
		["default plugins", { overrides: { sandbox: "workspace-write" } }],
		["access", { overrides: { enabledPlugins: ["superpowers", "go"], sandbox: "read-only" } }],
		["network", { overrides: { enabledPlugins: ["superpowers", "go"], sandbox: "workspace-write", sandboxNet: false } }],
		["turn limit", { overrides: { enabledPlugins: ["superpowers", "go"], sandbox: "workspace-write", maxRounds: 100 } }],
	];
	it.each(differences)("tells a different %s apart", (_name, over) => {
		expect(sameSetup(setup(), setup(over))).toBe(false);
	});
});

describe("Same as last time (ruling 16)", () => {
	const history = [
		{ setup: setup({ effort: "high" }), at: 1 },
		{ setup: setup({ cwd: "/home/jesse/git/docs", effort: "max" }), at: 3 },
		{ setup: setup({ effort: "xhigh" }), at: 2 },
	];

	it("is the newest setup started for that host and project", () => {
		expect(lastSetupFor(history, "local", "/home/jesse/git/evener")?.effort).toBe("xhigh");
	});

	it("moves the newest setup on this hub to a project never started from here", () => {
		expect(lastSetupFor(history, "paradise-park", "/Users/jesse/git/evener")).toEqual({
			...setup({ cwd: "/home/jesse/git/docs", effort: "max" }),
			host: "paradise-park",
			cwd: "/Users/jesse/git/evener",
		});
	});

	it("is nothing when nothing was ever started from this phone", () => {
		expect(lastSetupFor([], "local", "/home/jesse/git/evener")).toBeNull();
		expect(newestSetup([])).toBeNull();
	});

	it("opens a new sheet on the newest start", () => {
		expect(newestSetup(history)?.cwd).toBe("/home/jesse/git/docs");
	});
});

describe("the lit chip (spec 11)", () => {
	const recipes = [{ id: "quick", setup: setup({ effort: "medium" }) }];

	it("is Same as last time when the setup is exactly the last one", () => {
		expect(litChip(setup(), setup(), recipes)).toEqual({ kind: "last" });
	});

	it("is a recipe when the setup is exactly that recipe", () => {
		expect(litChip(setup({ effort: "medium" }), setup(), recipes)).toEqual({ kind: "recipe", id: "quick" });
	});

	it("is Custom after any change that matches nothing", () => {
		expect(litChip(setup({ effort: "low" }), setup(), recipes)).toEqual({ kind: "custom" });
		expect(litChip(setup(), null, [])).toEqual({ kind: "custom" });
	});
});

describe("changing host (ruling 17)", () => {
	it("keeps a project the new host has", () => {
		expect(moveToHost("/home/jesse/git/evener", "paradise-park", true, ["/Users/jesse/git/docs"])).toEqual({
			cwd: "/home/jesse/git/evener",
			note: null,
		});
	});

	it("moves to the host's most recent project and says so", () => {
		expect(moveToHost("/home/jesse/git/evener", "paradise-park", false, ["/Users/jesse/git/docs"])).toEqual({
			cwd: "/Users/jesse/git/docs",
			note: "evener isn't on paradise-park, so the project changed to docs.",
		});
	});

	it("asks for a project when the host remembers none", () => {
		expect(moveToHost("/home/jesse/git/evener", "paradise-park", false, [])).toEqual({
			cwd: "",
			note: "evener isn't on paradise-park. Choose a project.",
		});
	});

	it("fills an empty project from the host's most recent one, quietly", () => {
		expect(moveToHost("", "paradise-park", false, ["/Users/jesse/git/docs"])).toEqual({
			cwd: "/Users/jesse/git/docs",
			note: null,
		});
	});

	it("names a project by its folder", () => {
		expect(projectName("/home/jesse/git/evener/")).toBe("evener");
	});
});

describe("labels", () => {
	it("spells efforts as spec 11's Effort control does", () => {
		expect(["low", "medium", "high", "xhigh", "max", "minimal", "none", "turbo"].map(effortLabel)).toEqual([
			"Low",
			"Med",
			"High",
			"XHigh",
			"Max",
			"Minimal",
			"Off",
			"Turbo",
		]);
	});

	it("names the access a setup runs with", () => {
		expect(accessOf("read-only", "off").label).toBe("Read-only");
		expect(accessOf(undefined, "workspace-write").label).toBe("Workspace write");
		expect(accessOf(undefined, undefined).label).toBe("Full access");
		expect(accessOf("sealed", undefined)).toEqual({ mode: "sealed", label: "sealed", detail: "" });
	});

	it("offers the network switch only inside a sandbox", () => {
		expect(networkApplies(accessOf("off", undefined))).toBe(false);
		expect(networkApplies(accessOf("restricted", undefined))).toBe(true);
	});

	it("sums a setup up in one line", () => {
		expect(setupSummary(setup(), { host: "magic-kingdom", model: "GLM 5.3 Vision", access: "Workspace write" })).toBe(
			"magic-kingdom · evener · GLM 5.3 Vision · XHigh · 2 plugins · Workspace write",
		);
		expect(
			setupSummary(setup({ effort: "", overrides: {} }), {
				host: "magic-kingdom",
				model: "Hub default model",
				access: "Full access",
			}),
		).toBe("magic-kingdom · evener · Hub default model · Default plugins · Full access");
	});
});

describe("from the form and from a session", () => {
	it("reads the creation form as a setup", () => {
		expect(
			setupOf({
				source: "paradise-park",
				cwd: " /Users/jesse/git/evener ",
				model: { provider: "lunaroute", model: "glm-5.3-vision" },
				reasoning: "high",
				launchOverrides: { sandbox: "restricted", env: { A: "1" } },
			}),
		).toEqual({
			host: "paradise-park",
			cwd: "/Users/jesse/git/evener",
			model: { provider: "lunaroute", model: "glm-5.3-vision" },
			effort: "high",
			overrides: { sandbox: "restricted" },
		});
	});

	it("finds a model named the way a session names it", () => {
		const models = [
			{ provider: "lunaroute", model: "glm-5.3" },
			{ provider: "zai", model: "glm-5.3" },
			{ provider: "meta", model: "muse-spark-1.3" },
		];
		expect(modelFromId("lunaroute/glm-5.3", models)).toBe(models[0]);
		expect(modelFromId("muse-spark-1.3", models)).toBe(models[2]);
		expect(modelFromId("glm-5.3", models)).toBeNull();
		expect(modelFromId("gpt-5.6", models)).toBeNull();
	});
});
