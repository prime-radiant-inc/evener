import { describe, expect, it } from "vitest";
import {
	seedFromSession,
	accessOf,
	knownAccess,
	effortLabel,
	type LaunchSetup,
	modelFromId,
	moveToHost,
	networkApplies,
	newestSetup,
	ownedOverrides,
	projectName,
	setupOf,
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
		expect(
			withOwnedOverrides({ sandbox: "restricted", maxRounds: 5, env: { A: "1" } }, { sandbox: "read-only" }),
		).toEqual({
			sandbox: "read-only",
			env: { A: "1" },
		});
	});
});

describe("the newest remembered start (spec 11)", () => {
	it("opens a new sheet on the newest start", () => {
		const history = [
			{ setup: setup({ effort: "high" }), at: 1 },
			{ setup: setup({ cwd: "/home/jesse/git/docs", effort: "max" }), at: 3 },
			{ setup: setup({ effort: "xhigh" }), at: 2 },
		];
		expect(newestSetup(history)?.cwd).toBe("/home/jesse/git/docs");
	});

	it("is nothing when nothing was ever started from this phone", () => {
		expect(newestSetup([])).toBeNull();
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

	it("names two folders of one name by the shortest ends of their paths that tell them apart", () => {
		expect(moveToHost("/src/work/evener", "paradise-park", false, ["/src/oss/evener"])).toEqual({
			cwd: "/src/oss/evener",
			note: "work/evener isn't on paradise-park, so the project changed to oss/evener.",
		});
		// Paths that differ only at their first folder are given whole.
		expect(moveToHost("/home/jesse/git/evener", "paradise-park", false, ["/Users/jesse/git/evener"])).toEqual({
			cwd: "/Users/jesse/git/evener",
			note: "/home/jesse/git/evener isn't on paradise-park, so the project changed to /Users/jesse/git/evener.",
		});
		// One path the end of the other: both are given whole.
		expect(moveToHost("/a/evener", "paradise-park", false, ["/b/a/evener"]).note).toBe(
			"/a/evener isn't on paradise-park, so the project changed to /b/a/evener.",
		);
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

	it("reads a session as a seed for New session like this (ruling 24)", () => {
		expect(
			seedFromSession("paradise-park:thread-9", {
				cwd: "/Users/jesse/git/evener",
				modelProvider: "lunaroute/glm-5.3-vision",
				reasoningEffort: "high",
			}),
		).toEqual({
			host: "paradise-park",
			cwd: "/Users/jesse/git/evener",
			model: "lunaroute/glm-5.3-vision",
			effort: "high",
		});
		// A session with no model or effort of its own leaves the sheet's.
		expect(seedFromSession("local:thread-1", { cwd: "/tmp", modelProvider: "" })).toEqual({
			host: "local",
			cwd: "/tmp",
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

describe("the access a form can name", () => {
	it("is the person's own choice, or the hub's default once the hub has said it", () => {
		expect(knownAccess("restricted", null)?.label).toBe("Restricted");
		expect(knownAccess(undefined, { sandbox: "workspace-write" })?.label).toBe("Workspace write");
		expect(knownAccess(undefined, {})?.label).toBe("Full access");
	});

	it("is nothing while the hub's default is unknown, never the least safe level", () => {
		expect(knownAccess(undefined, null)).toBeNull();
	});
});
