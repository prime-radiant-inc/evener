import type { ModelDescriptor, ThreadCapabilities } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import {
	accessFacts,
	canDeleteSavedSession,
	canOpenModelSheet,
	effortName,
	hostIdOf,
	latestForkPoint,
	modelChipLabel,
	notesSummary,
	pluginsLine,
	sessionHosts,
	usageFacts,
	visionModelLabel,
	whereFacts,
} from "./sessionFacts";

describe("where a session runs (spec 8.6)", () => {
	it("reads the host from the ref", () => {
		expect(hostIdOf("local:abc")).toBe("local");
		expect(hostIdOf("paradise-park:abc")).toBe("paradise-park");
		expect(hostIdOf("abc")).toBe("local");
	});

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

// Spec 8.6: the host with its connection dot. The hub calls its own machine
// "this host" in the manifest; the phone names it for the hub, as Hub > Hosts
// does (audit N6).
describe("the host a session runs on", () => {
	const sources = [
		{ id: "local", label: "this host", kind: "local", online: true },
		{ id: "paradise-park", label: "paradise-park", kind: "appwire", online: false },
	];

	it("names the hub's own machine for the hub, and any other host by its manifest label", () => {
		const host = sessionHosts(sources, "Work hub", true);
		expect(host("local")).toEqual({ label: "Work hub", online: true });
		expect(host("paradise-park")).toEqual({ label: "paradise-park", online: false });
	});

	it("reads every host as offline while the hub is away, whatever the manifest last said", () => {
		const host = sessionHosts(
			sources.map((source) => ({ ...source, online: true })),
			"Work hub",
			false,
		);
		expect(host("local")).toEqual({ label: "Work hub", online: false });
		expect(host("paradise-park")).toEqual({ label: "paradise-park", online: false });
	});

	it("reads the hub's own machine as online while the hub is, and a host the manifest doesn't list by its id", () => {
		const host = sessionHosts(undefined, null, false);
		expect(host("local")).toEqual({ label: "local", online: false });
		expect(host("studio")).toEqual({ label: "studio", online: null });
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

describe("access (S15)", () => {
	it.each([
		["off", "Full access"],
		["workspace-write", "Workspace write"],
		["read-only", "Read-only"],
		["restricted", "Restricted"],
		["container", "container"],
	])("sandbox %s reads %s", (sandbox, mode) => {
		expect(accessFacts({ access: { sandbox, network: true } })?.mode).toBe(mode);
	});

	it("says whether the network is allowed", () => {
		expect(accessFacts({ access: { sandbox: "read-only", network: true } })).toEqual({
			mode: "Read-only",
			network: "On",
		});
		expect(accessFacts({ access: { sandbox: "read-only", network: false } })?.network).toBe("Off");
	});

	it("says nothing when the hub doesn't report access", () => {
		expect(accessFacts({ access: undefined })).toBeNull();
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
				failedToolCalls: undefined,
			}),
		).toEqual({});
		expect(
			usageFacts({ usage: null, cost: undefined, workMillis: 0, contextUsed: 0, contextWindow: 0, failedToolCalls: 1 })
				.failedToolCalls,
		).toBe("1 failed tool call");
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
					reasoningEffort: "xhigh",
					reasoningEffortLevels: ["low", "high", "xhigh"],
					supportsReasoning: true,
				},
				catalog,
			),
		).toBe("GLM 5.3 Vision · XHigh");
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
	it("falls back to the model id before the catalog loads, and drops effort a model lacks", () => {
		expect(
			modelChipLabel(
				{
					modelProvider: "meta/muse-spark-1.3",
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
					reasoningEffort: "",
					reasoningEffortLevels: ["high"],
					supportsReasoning: true,
				},
				catalog,
			),
		).toBe("GLM 5.3 Vision · Default");
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
		expect(visionModelLabel("off", catalog)).toBe("Off");
		expect(visionModelLabel("", catalog)).toBe("Session model");
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
		expect(notesSummary({ humanNote: "keep it", agentNote: "", sessionUrls: [] })).toBe("Your note");
		expect(notesSummary({ humanNote: "", agentNote: "", sessionUrls: [link, link] })).toBe("2 links");
	});

	it("says None when nothing is shared", () => {
		expect(notesSummary({ humanNote: "", agentNote: "", sessionUrls: [] })).toBe("None");
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
