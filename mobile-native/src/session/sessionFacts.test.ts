import type { ModelDescriptor, ThreadCapabilities } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import {
	canDeleteSavedSession,
	canOpenModelSheet,
	effortName,
	latestForkPoint,
	modelChipLabel,
	notesSummary,
	pluginsLine,
	qualifiedModelIdentity,
	usageFacts,
	visionModelLabel,
	whereFacts,
} from "./sessionFacts";

describe("where a session runs (spec 8.6)", () => {
	it("names the host, project, directory and branch it knows", () => {
		const label = (id: string) => (id === "local" ? "magic-kingdom" : id);
		expect(
			whereFacts(
				{
					ref: "local:s1",
					cwd: "/Users/jesse/git/evener",
					projectPath: "/Users/jesse/git/evener/",
					gitBranch: "fix-settle",
				},
				label,
			),
		).toEqual({ host: "magic-kingdom", project: "evener", directory: "/Users/jesse/git/evener", branch: "fix-settle" });
		expect(whereFacts({ ref: "paradise-park:s2", cwd: "/srv/app" }, label)).toEqual({
			host: "paradise-park",
			directory: "/srv/app",
		});
	});
});

describe("plugins, chosen at start", () => {
	it("counts and lists them, and says nothing when the inventory is unknown", () => {
		expect(pluginsLine({ diagnostics: { plugins: [{ name: "superpowers" }, { name: "go" }] } })).toEqual({
			line: "2 plugins · chosen at start",
			names: ["go", "superpowers"],
		});
		expect(pluginsLine({ diagnostics: { plugins: [{ name: "go" }] } })?.line).toBe("1 plugin · chosen at start");
		expect(pluginsLine({ diagnostics: { plugins: [] } })?.line).toBe("0 plugins · chosen at start");
		expect(pluginsLine({ diagnostics: undefined })).toBeNull();
		expect(pluginsLine({ diagnostics: {} })).toBeNull();
	});
});

describe("usage", () => {
	it("says tokens, their split, cost, work time, context and failed calls", () => {
		expect(
			usageFacts({
				usage: {
					totalTokens: 46_000_000,
					inputTokens: 12_000_000,
					outputTokens: 1_200_000,
					cacheReadTokens: 33_000_000,
				},
				cost: "~$4.12",
				workMillis: 3 * 3_600_000 + 12 * 60_000,
				contextUsed: 39_800,
				contextWindow: 200_000,
				contextAssumed: false,
				failedToolCalls: 3,
			}),
		).toEqual({
			tokens: "46M tokens",
			split: "12M in · 1.2M out · 33M cached",
			cost: "~$4.12",
			workTime: "3h",
			context: { text: "39.8K of 200K", fraction: 0.199 },
			failedToolCalls: "3 failed tool calls",
		});
	});

	it("leaves out what it doesn't know", () => {
		expect(
			usageFacts({
				usage: null,
				cost: null,
				workMillis: 0,
				contextUsed: 0,
				contextWindow: 0,
				contextAssumed: false,
				failedToolCalls: undefined,
			}),
		).toEqual({});
		expect(
			usageFacts({
				usage: null,
				cost: undefined,
				workMillis: 0,
				contextUsed: 0,
				contextWindow: 0,
				contextAssumed: false,
				failedToolCalls: 1,
			}).failedToolCalls,
		).toBe("1 failed tool call");
	});

	it("marks an assumed window as an estimate, not a stated capacity", () => {
		const facts = usageFacts({
			usage: null,
			cost: null,
			workMillis: 0,
			contextUsed: 25_600,
			contextWindow: 128_000,
			contextAssumed: true,
			failedToolCalls: undefined,
		});
		// Numeric behavior and the marker's placement — not the prose around it:
		// the used figure plain, the window figure marked, the fraction exact.
		expect(facts.context?.fraction).toBe(0.2);
		expect(facts.context?.text).toContain("25.6K");
		expect(facts.context?.text).toContain("~128K");
		expect(facts.context?.text).not.toContain("~25.6K");
	});

	it("measures against an explicit compactionThreshold without exposing a synthetic assumed window", () => {
		const facts = usageFacts({
			usage: null,
			cost: null,
			workMillis: 0,
			contextUsed: 200_000,
			contextWindow: 277_778,
			contextAssumed: true,
			compactionThreshold: 250_000,
			failedToolCalls: undefined,
		});
		expect(facts.context?.fraction).toBe(0.8);
		expect(facts.context?.text).toContain("200K");
		expect(facts.context?.text).toContain("250K");
		expect(facts.context?.text).not.toContain("~");
		expect(facts.context?.text).not.toContain("278K");
	});
});

describe("the model chip (spec 8.5)", () => {
	const catalog: ModelDescriptor[] = [
		{ provider: "lunaroute", model: "glm-5.3-vision", displayName: "GLM 5.3 Vision" },
	];
	it("names the model the way the catalog does, with its effort", () => {
		expect(
			modelChipLabel(
				{
					modelProvider: "lunaroute/glm-5.3-vision",
					model: "lunaroute/glm-5.3-vision",
					reasoningEffort: "xhigh",
					reasoningEffortLevels: ["low", "high", "xhigh"],
					supportsReasoning: true,
				},
				catalog,
			),
		).toMatch(new RegExp(`^GLM 5\\.3 Vision .+${effortName("xhigh")}$`));
	});
	it("names the model as the hub's session row does while the catalog is away (S17)", () => {
		// The catalog clears while it reloads, and after a failed load; the
		// Board row's model_name is the same name model/list gives.
		const session = {
			modelProvider: "deepseek/deepseek-4.1-flash",
			model: "deepseek/deepseek-4.1-flash",
			reasoningEffort: "xhigh",
			reasoningEffortLevels: ["high", "xhigh"],
			supportsReasoning: true,
		};
		expect(modelChipLabel(session, undefined, "DeepSeek 4.1 Flash")).toBe("DeepSeek 4.1 Flash · XHigh");
		// The catalog, when it has the model, still wins.
		expect(
			modelChipLabel(
				session,
				[{ provider: "deepseek", model: "deepseek-4.1-flash", displayName: "DS Flash" }],
				"Stale",
			),
		).toBe("DS Flash · XHigh");
	});
	it("names the model when the session's provider key differs from the catalog's (#2988)", () => {
		// A gateway and its upstream can name the same model under different
		// provider-instance keys. The display name belongs to the model, so
		// the bare id still finds it rather than falling back to the id.
		expect(
			modelChipLabel(
				{
					modelProvider: "deepseek/deepseek-4.1-flash",
					model: "deepseek/deepseek-4.1-flash",
					reasoningEffort: "xhigh",
					reasoningEffortLevels: ["high", "xhigh"],
					supportsReasoning: true,
				},
				[{ provider: "lunaroute", model: "deepseek-4.1-flash", displayName: "DeepSeek 4.1 Flash" }],
			),
		).toBe("DeepSeek 4.1 Flash · XHigh");
	});
	it("falls back to the model id before the catalog loads, and drops effort a model lacks", () => {
		expect(
			modelChipLabel(
				{
					modelProvider: "meta/muse-spark-1.3",
					model: "meta/muse-spark-1.3",
					reasoningEffort: undefined,
					reasoningEffortLevels: [],
					supportsReasoning: false,
				},
				undefined,
			),
		).toBe("muse-spark-1.3");
		expect(
			modelChipLabel(
				{
					modelProvider: "lunaroute/glm-5.3-vision",
					model: "lunaroute/glm-5.3-vision",
					reasoningEffort: "",
					reasoningEffortLevels: ["high"],
					supportsReasoning: true,
				},
				catalog,
			),
		).toBe("GLM 5.3 Vision · Default");
	});
	it("names the announced model in the acceptance window, not the Board row's previous model", () => {
		// A live thread/model/changed announces its model with the provider
		// instance in modelProvider; the Board row still names the model the
		// last read saw until the next read lands.
		const session = {
			modelProvider: "lunaroute",
			model: "glm-5.3-vision",
			modelProfile: undefined,
			reasoningEffort: "xhigh",
			reasoningEffortLevels: ["low", "high", "xhigh"],
			supportsReasoning: true,
		};
		// The catalog lookup must run on the qualified identity, so the chip
		// names the announced model the way the catalog does.
		expect(modelChipLabel(session, catalog, "DeepSeek 4.1 Flash")).toBe(`GLM 5.3 Vision · ${effortName("xhigh")}`);
		// While the catalog is away, the announced id wins over the Board row,
		// which still refers to the previous model.
		expect(modelChipLabel(session, undefined, "DeepSeek 4.1 Flash")).toBe(`glm-5.3-vision · ${effortName("xhigh")}`);
	});
	it.each([
		["minimal", "Minimal"],
		["low", "Low"],
		["medium", "Medium"],
		["high", "High"],
		["xhigh", "XHigh"],
		["max", "Max"],
		["none", "Off"],
		["", "Default"],
		["turbo", "Turbo"],
	])("effort %s reads %s", (level, name) => {
		expect(effortName(level)).toBe(name);
	});
});

describe("the model's one qualified identity", () => {
	// The daemon names a model two ways, and the model id may itself contain a
	// slash. Every layout has to answer "provider/model" the same way, or the
	// running row draws unselected and a tap meant as a no-op sends a setter.
	it.each([
		// The snapshot's own layout: the bare id in both model fields and the
		// provider instance beside them. An OpenRouter session's id carries a
		// slash of its own, which is part of the id and not proof of qualification.
		["a snapshot whose model id contains a slash", "anthropic/claude-opus-5", "anthropic/claude-opus-5", "openrouter"],
		// A live thread/model/changed: modelProvider is the provider instance and
		// the profile is cleared.
		["a live model change", "openrouter", "anthropic/claude-opus-5", undefined],
		// No provider instance beside the id: a genuinely prequalified snapshot
		// names the whole identity in its one field, so nothing is joined.
		[
			"a snapshot with no separate provider",
			"openrouter/anthropic/claude-opus-5",
			"openrouter/anthropic/claude-opus-5",
			undefined,
		],
	])(
		"reads %s as openrouter/anthropic/claude-opus-5 when that is what runs",
		(_shape, modelProvider, model, modelProfile) => {
			expect(qualifiedModelIdentity({ modelProvider, model, modelProfile })).toBe("openrouter/anthropic/claude-opus-5");
		},
	);

	// The producer names the instance apart from the raw model id, and the id
	// may itself begin with the instance: the bundled catalog lists OpenRouter's
	// Auto Router as "openrouter/auto" on the "openrouter" instance. Its
	// catalog identity is therefore "openrouter/openrouter/auto", and an
	// identity that kept the id as-is would leave the running row unselected.
	it.each([
		["a snapshot", "openrouter/auto", "openrouter/auto", "openrouter"],
		["a live model change", "openrouter", "openrouter/auto", undefined],
	])("reads %s of openrouter/auto as openrouter/openrouter/auto", (_shape, modelProvider, model, modelProfile) => {
		expect(qualifiedModelIdentity({ modelProvider, model, modelProfile })).toBe("openrouter/openrouter/auto");
	});

	it("reads a direct Anthropic model as anthropic/claude-opus-5", () => {
		expect(
			qualifiedModelIdentity({
				modelProvider: "claude-opus-5",
				model: "claude-opus-5",
				modelProfile: "anthropic",
			}),
		).toBe("anthropic/claude-opus-5");
		expect(qualifiedModelIdentity({ modelProvider: "anthropic", model: "claude-opus-5" })).toBe(
			"anthropic/claude-opus-5",
		);
	});

	it("leaves a session that names no model unnamed", () => {
		expect(qualifiedModelIdentity({ modelProvider: "", model: "", modelProfile: "" })).toBe("");
	});
});

describe("the vision model row", () => {
	const catalog: ModelDescriptor[] = [
		{ provider: "lunaroute", model: "glm-5.3-vision", displayName: "GLM 5.3 Vision" },
	];
	it("reads Off, Session model, or the model as the catalog names it", () => {
		// Off and the session's own model each read their own way, apart from any model.
		const off = visionModelLabel("off", catalog);
		const sessionModel = visionModelLabel("", catalog);
		expect(new Set([off, sessionModel, visionModelLabel("lunaroute/glm-5.3-vision", catalog)]).size).toBe(3);
		expect(off).not.toBe("");
		expect(sessionModel).not.toBe("");
		expect(visionModelLabel("lunaroute/glm-5.3-vision", catalog)).toBe("GLM 5.3 Vision");
		expect(visionModelLabel("lunaroute/glm-5.3-vision", undefined)).toBe("glm-5.3-vision");
	});
});

describe("the Notes & links row", () => {
	const link = { id: "u1", url: "https://example.com" };
	it("names only the parts present", () => {
		expect(notesSummary({ humanNote: "keep it", agentNote: "on it", sessionUrls: [link, link, link] })).toBe(
			"Your note · agent note · 3 links",
		);
		expect(notesSummary({ humanNote: " ", agentNote: "on it", sessionUrls: [link] })).toBe("agent note · 1 link");
		// Your note alone names the first of the parts the whole summary names.
		const yours = notesSummary({ humanNote: "keep it", agentNote: "", sessionUrls: [] });
		expect(notesSummary({ humanNote: "keep it", agentNote: "on it", sessionUrls: [link] }).startsWith(yours)).toBe(
			true,
		);
		expect(yours).not.toBe(notesSummary({ humanNote: "", agentNote: "on it", sessionUrls: [] }));
		expect(notesSummary({ humanNote: "", agentNote: "", sessionUrls: [link, link] })).toBe("2 links");
	});

	it("says None when nothing is shared", () => {
		const none = notesSummary({ humanNote: "", agentNote: "", sessionUrls: [] });
		expect(none).not.toBe("");
		for (const some of [
			{ humanNote: "keep it", agentNote: "", sessionUrls: [] },
			{ humanNote: "", agentNote: "on it", sessionUrls: [] },
			{ humanNote: "", agentNote: "", sessionUrls: [link] },
		])
			expect(notesSummary(some)).not.toBe(none);
	});
});

describe("Fork from latest", () => {
	it("forks from your latest message the transcript can fork from", () => {
		expect(
			latestForkPoint([
				{ kind: "user", id: "a", text: "first", transcriptEntryIndex: 1 },
				{ kind: "assistant", id: "b", markdown: "ok" },
				{ kind: "user", id: "c", text: "second", transcriptEntryIndex: 4 },
				{ kind: "user", id: "d", text: "not yet written" },
			] as Parameters<typeof latestForkPoint>[0]),
		).toEqual({ entryIndex: 4, preview: "second" });
	});

	it("has nothing to fork from without such a message", () => {
		expect(
			latestForkPoint([
				{ kind: "user", id: "a", text: "unwritten" },
				{ kind: "user", id: "b", text: "the first entry", transcriptEntryIndex: 0 },
			] as Parameters<typeof latestForkPoint>[0]),
		).toBeNull();
		expect(latestForkPoint([])).toBeNull();
	});
});

describe("Delete", () => {
	it("deletes only a saved local session that isn't loaded", () => {
		const local = "local:AbCdEfGhIjKlMnOpQrStUv";
		expect(canDeleteSavedSession({ ref: local, status: { type: "notLoaded" } })).toBe(true);
		expect(canDeleteSavedSession({ ref: local, status: { type: "idle" } })).toBe(false);
		expect(canDeleteSavedSession({ ref: local, status: { type: "closed" } })).toBe(false);
		expect(canDeleteSavedSession({ ref: "paradise-park:s2", status: { type: "notLoaded" } })).toBe(false);
	});
});

describe("the model sheet has something to change", () => {
	const session = (changeModel: boolean, levels: string[], supportsReasoning = levels.length > 0) => ({
		capabilities: { changeModel } as ThreadCapabilities,
		reasoningEffortLevels: levels,
		supportsReasoning,
	});
	it("when the model or its effort can change", () => {
		expect(canOpenModelSheet(session(true, []))).toBe(true);
		expect(canOpenModelSheet(session(false, ["low", "high"]))).toBe(true);
		// Reasoning without listed levels gets the default ladder.
		expect(canOpenModelSheet(session(false, [], true))).toBe(true);
		expect(canOpenModelSheet(session(false, []))).toBe(false);
	});
});
