import { expect, it } from "vitest";
import type { PluginLaunchCandidate, PluginPreviewResponse } from "@evener/appwire-client";
import { pluginCounts, pluginGroups, pluginWarnings, unclaimedDiagnostics } from "./pluginFacts";

const plugin = (
	name: string,
	marketplace?: string,
	over: Partial<PluginLaunchCandidate> = {},
): PluginLaunchCandidate => ({
	name,
	source: marketplace ? "installed" : "dir",
	...(marketplace ? { marketplace } : {}),
	selected: true,
	skillCount: 0,
	agentCount: 0,
	commandCount: 0,
	hookCount: 0,
	mcpCount: 0,
	...over,
});

it("counts what a plugin brings, leaving out what it doesn't (spec 11)", () => {
	expect(
		pluginCounts(
			plugin("superpowers", "superpowers-marketplace", {
				skillCount: 38,
				agentCount: 3,
				commandCount: 6,
				hookCount: 2,
			}),
		),
	).toBe("38 skills · 3 agents · 6 commands · 2 hooks");
	expect(pluginCounts(plugin("private-journal-mcp", "superpowers-marketplace", { mcpCount: 1 }))).toBe("1 MCP server");
	expect(pluginCounts(plugin("empty"))).toBe("");
});

it("groups plugins by marketplace in the order the hub lists them", () => {
	const preview: PluginPreviewResponse = {
		plugins: [
			plugin("superpowers", "superpowers-marketplace"),
			plugin("go", "go-skills"),
			plugin("local-tool"),
			plugin("elements-of-style", "superpowers-marketplace"),
		],
	};
	expect(pluginGroups(preview).map((group) => [group.marketplace, group.plugins.map((p) => p.name)])).toEqual([
		["superpowers-marketplace", ["superpowers", "elements-of-style"]],
		["go-skills", ["go"]],
		[null, ["local-tool"]],
	]);
});

it("finds a plugin's own warnings", () => {
	const preview: PluginPreviewResponse = {
		plugins: [plugin("superpowers-chrome", "superpowers-marketplace")],
		diagnostics: [
			{ name: "superpowers-chrome", message: "Chrome isn't installed on this host" },
			{ message: "a marketplace failed to refresh" },
		],
	};
	expect(pluginWarnings(preview, "superpowers-chrome")).toEqual(["Chrome isn't installed on this host"]);
	expect(pluginWarnings(preview, "go")).toEqual([]);
});

it("keeps the preview's other diagnostics for the page: unnamed ones, and ones naming no listed plugin", () => {
	const preview: PluginPreviewResponse = {
		plugins: [plugin("superpowers-chrome", "superpowers-marketplace")],
		diagnostics: [
			{ name: "superpowers-chrome", message: "Chrome isn't installed on this host" },
			{ message: "stat /home/jesse/.evener/plugins: permission denied: installed and bundled plugins are unavailable" },
			{ name: "broken", path: "/home/jesse/.evener/plugins/broken", message: "invalid manifest" },
		],
	};
	expect(unclaimedDiagnostics(preview)).toEqual([
		"stat /home/jesse/.evener/plugins: permission denied: installed and bundled plugins are unavailable",
		"broken: invalid manifest",
	]);
	expect(unclaimedDiagnostics({ plugins: [] })).toEqual([]);
	expect(
		unclaimedDiagnostics({
			plugins: [],
			diagnostics: [{ message: "store unreadable" }, { message: "store unreadable" }],
		}),
	).toEqual(["store unreadable"]);
});
