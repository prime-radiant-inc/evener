import type { ModelControlSnapshot, ModelDescriptor, ThreadCapabilities } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import {
	canDeleteSavedSession,
	canOpenModelSheet,
	effortName,
	latestForkPoint,
	modelChipLabel,
	notesSummary,
	pluginsLine,
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
	const controlledCatalog: ModelDescriptor[] = [
		{ provider: "anthropic", model: "claude-sonnet-5", displayName: "Claude Sonnet 5" },
		{ provider: "openai", model: "gpt-5.5", displayName: "GPT 5.5" },
	];
	const controlledSession = (lifecycle: string) => {
		const snapshot: ModelControlSnapshot = {
			authority: { instanceId: "instance-a", bootGeneration: "7" },
			revision: 2,
			active: {
				modelProvider: "anthropic",
				model: "claude-sonnet-5",
				reasoningEffortLevels: ["low", "high"],
				supportsReasoning: true,
			},
			desired: {
				clientMutationId: "request-2",
				modelProvider: "openai",
				model: "gpt-5.5",
				lifecycle,
			},
		};
		return {
			modelProvider: "anthropic",
			model: "claude-sonnet-5",
			modelControlState: { kind: "ready" as const, snapshot },
			reasoningEffort: "high",
			reasoningEffortLevels: ["low", "high"],
			supportsReasoning: true,
		};
	};
	// A fixture catalog entry the tests name a model by. displayName is optional
	// on the wire type; these fixtures all carry one, and a case that derived
	// its expectation from a nameless entry would be comparing against nothing.
	const entryFor = (
		entries: readonly ModelDescriptor[],
		provider: string,
		model: string,
	): ModelDescriptor & { displayName: string } => {
		const entry = entries.find((candidate) => candidate.provider === provider && candidate.model === model);
		if (!entry?.displayName) throw new Error(`the fixture catalog is missing a named ${provider}/${model}`);
		return { ...entry, displayName: entry.displayName };
	};
	const activeEntry = entryFor(controlledCatalog, "anthropic", "claude-sonnet-5");
	const desiredEntry = entryFor(controlledCatalog, "openai", "gpt-5.5");
	const catalogEntry = entryFor(catalog, "lunaroute", "glm-5.3-vision");

	// A pending or applied pick shows the target it asked for; a rejected one
	// falls back to the model the session runs. The chip's effort suffix is not
	// this claim, so each case names its model by catalog identity.
	it.each(["pending", "applied", "rejected"])("shows the shared %s model-control target", (lifecycle) => {
		const selected = lifecycle === "rejected" ? activeEntry : desiredEntry;
		const other = lifecycle === "rejected" ? desiredEntry : activeEntry;
		const label = modelChipLabel(controlledSession(lifecycle), controlledCatalog, "Stale Board model");
		expect(label).toContain(selected.displayName);
		expect(label).not.toContain(other.displayName);
	});

	it("keeps the Board-row name for the active model, but not for a complete pending target", () => {
		const boardRowName = "Claude from Board";
		const onActive = modelChipLabel(controlledSession("rejected"), undefined, boardRowName);
		expect(onActive).toContain(boardRowName);
		expect(onActive).not.toContain(activeEntry.model);
		const onPending = modelChipLabel(controlledSession("pending"), undefined, boardRowName);
		expect(onPending).toContain(desiredEntry.model);
		expect(onPending).not.toContain(boardRowName);
	});
	it("names the model itself, not its provider, while a pick is in recovery", () => {
		// A recovery state carries the last safe display target: the provider
		// AND the model a pick had reached. Showing the provider alone would
		// replace the selected model's name with "openai" for the whole
		// recovery, which reads as the session having lost its model.
		const recovering = {
			modelProvider: "anthropic",
			model: "claude-sonnet-5",
			modelControlState: {
				kind: "recovering" as const,
				lastSafeDisplay: { modelProvider: "openai", model: "gpt-5.5" },
			},
			reasoningEffort: "high",
			reasoningEffortLevels: ["low", "high"],
			supportsReasoning: true,
		};
		const label = modelChipLabel(recovering, controlledCatalog, "Stale Board model");
		expect(label).toContain(desiredEntry.displayName);
		expect(label).not.toContain(activeEntry.displayName);
	});
	it("names the model the way the catalog does, with its effort", () => {
		const session = (reasoningEffort: string) => ({
			modelProvider: "lunaroute/glm-5.3-vision",
			reasoningEffort,
			reasoningEffortLevels: ["low", "high", "xhigh"],
			supportsReasoning: true,
		});
		// The catalog's name, then the effort the session runs at. What an effort
		// level reads as is effortName's contract; the chip's is that the name
		// leads, an effort follows it, and the effort follows the level.
		const atXHigh = modelChipLabel(session("xhigh"), catalog);
		const atLow = modelChipLabel(session("low"), catalog);
		expect(atXHigh.startsWith(catalogEntry.displayName)).toBe(true);
		expect(atXHigh).not.toBe(catalogEntry.displayName);
		expect(atLow.startsWith(catalogEntry.displayName)).toBe(true);
		expect(atXHigh).not.toBe(atLow);
	});
	it("names the model as the hub's session row does while the catalog is away (S17)", () => {
		// The catalog clears while it reloads, and after a failed load; the
		// Board row's model_name is the same name model/list gives.
		const session = {
			modelProvider: "deepseek/deepseek-4.1-flash",
			reasoningEffort: "xhigh",
			reasoningEffortLevels: ["high", "xhigh"],
			supportsReasoning: true,
		};
		const rowName = "DeepSeek 4.1 Flash";
		expect(modelChipLabel(session, undefined, rowName)).toContain(rowName);
		// The catalog, when it has the model, still wins.
		const entry = { provider: "deepseek", model: "deepseek-4.1-flash", displayName: "DS Flash" };
		const named = modelChipLabel(session, [entry], rowName);
		expect(named).toContain(entry.displayName);
		expect(named).not.toContain(rowName);
	});
	it("names the model when the session's provider key differs from the catalog's (#2988)", () => {
		// A gateway and its upstream can name the same model under different
		// provider-instance keys. The display name belongs to the model, so
		// the bare id still finds it rather than falling back to the id.
		const entry = { provider: "lunaroute", model: "deepseek-4.1-flash", displayName: "DeepSeek 4.1 Flash" };
		const label = modelChipLabel(
			{
				modelProvider: "deepseek/deepseek-4.1-flash",
				reasoningEffort: "xhigh",
				reasoningEffortLevels: ["high", "xhigh"],
				supportsReasoning: true,
			},
			[entry],
		);
		expect(label).toContain(entry.displayName);
		expect(label).not.toContain(entry.model);
	});
	it("does not double a saved session's id that fills both display fields", () => {
		// A saved session's legacy read has no separate model id on the wire, so
		// the shared canonical display puts the same value in both fields. Joining
		// those would repeat the id's own path in the chip.
		const id = "anthropic/claude-opus-5";
		const saved = {
			modelProvider: id,
			model: id,
			modelControlState: {
				kind: "uninitialized" as const,
				canonicalDisplay: {
					modelProvider: id,
					model: id,
				},
			},
			reasoningEffort: "high",
			reasoningEffortLevels: ["low", "high"],
			supportsReasoning: true,
		};
		const label = modelChipLabel(saved, undefined);
		const bareName = id.slice(id.indexOf("/") + 1);
		expect(label).toContain(bareName);
		// The model's own name, not the provider path the saved read repeats.
		expect(label).not.toContain(id);
	});
	it("falls back to the model id before the catalog loads, and drops effort a model lacks", () => {
		const modelProvider = "meta/muse-spark-1.3";
		const bareName = modelProvider.slice(modelProvider.indexOf("/") + 1);
		const bare = modelChipLabel(
			{ modelProvider, reasoningEffort: undefined, reasoningEffortLevels: [], supportsReasoning: false },
			undefined,
		);
		// No catalog, so the id's own model part is the whole label.
		expect(bare).toBe(bareName);
		// The same model with levels grows an effort after its name.
		const withEffort = modelChipLabel(
			{ modelProvider, reasoningEffort: "high", reasoningEffortLevels: ["high"], supportsReasoning: true },
			undefined,
		);
		expect(withEffort.startsWith(bare)).toBe(true);
		expect(withEffort).not.toBe(bare);
		// An empty effort still names one (the default), after the catalog's name.
		const defaulted = modelChipLabel(
			{
				modelProvider: "lunaroute/glm-5.3-vision",
				reasoningEffort: "",
				reasoningEffortLevels: ["high"],
				supportsReasoning: true,
			},
			catalog,
		);
		expect(defaulted.startsWith(catalogEntry.displayName)).toBe(true);
		expect(defaulted).not.toBe(catalogEntry.displayName);
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

describe("the vision model row", () => {
	const catalog: ModelDescriptor[] = [
		{ provider: "lunaroute", model: "glm-5.3-vision", displayName: "GLM 5.3 Vision" },
	];
	it("reads Off, Session model, or the model as the catalog names it", () => {
		// Off and the session's own model each read their own way, apart from any model.
		const off = visionModelLabel("off", catalog);
		const sessionModel = visionModelLabel("", catalog);
		const [named] = catalog as [ModelDescriptor & { displayName: string }];
		expect(new Set([off, sessionModel, visionModelLabel("lunaroute/glm-5.3-vision", catalog)]).size).toBe(3);
		expect(off).not.toBe("");
		expect(sessionModel).not.toBe("");
		// With the catalog it is the catalog's name; without one, the model id.
		expect(visionModelLabel("lunaroute/glm-5.3-vision", catalog)).toBe(named.displayName);
		expect(visionModelLabel("lunaroute/glm-5.3-vision", undefined)).toBe(named.model);
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
