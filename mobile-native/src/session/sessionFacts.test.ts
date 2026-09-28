import type { ModelDescriptor } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import {
	accessFacts,
	effortName,
	hostIdOf,
	modelChipLabel,
	pluginsLine,
	usageFacts,
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
				{ ref: "local:s1", cwd: "/Users/jesse/git/evener", projectPath: "/Users/jesse/git/evener/", gitBranch: "fix-settle" },
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
				usage: { totalTokens: 46_000_000, inputTokens: 12_000_000, outputTokens: 1_200_000, cacheReadTokens: 33_000_000 },
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
			usageFacts({ usage: null, cost: null, workMillis: 0, contextUsed: 0, contextWindow: 0, failedToolCalls: undefined }),
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
				{ modelProvider: "lunaroute/glm-5.3-vision", reasoningEffort: "xhigh", reasoningEffortLevels: ["low", "high", "xhigh"], supportsReasoning: true },
				catalog,
			),
		).toBe("GLM 5.3 Vision · XHigh");
	});
	it("falls back to the model id before the catalog loads, and drops effort a model lacks", () => {
		expect(
			modelChipLabel(
				{ modelProvider: "meta/muse-spark-1.3", reasoningEffort: undefined, reasoningEffortLevels: [], supportsReasoning: false },
				undefined,
			),
		).toBe("muse-spark-1.3");
		expect(
			modelChipLabel(
				{ modelProvider: "lunaroute/glm-5.3-vision", reasoningEffort: "", reasoningEffortLevels: ["high"], supportsReasoning: true },
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
