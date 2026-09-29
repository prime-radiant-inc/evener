// A plugin row's facts in the New session checklist (spec 11): what it brings,
// its marketplace group, and its preview warnings.
import type { PluginLaunchCandidate, PluginPreviewResponse } from "@evener/appwire-client";

const COUNTS = [
	["skillCount", "skill", "skills"],
	["agentCount", "agent", "agents"],
	["commandCount", "command", "commands"],
	["hookCount", "hook", "hooks"],
	["mcpCount", "MCP server", "MCP servers"],
] as const;

export function pluginCounts(plugin: PluginLaunchCandidate): string {
	return COUNTS.filter(([field]) => plugin[field] > 0)
		.map(([field, one, many]) => `${plugin[field]} ${plugin[field] === 1 ? one : many}`)
		.join(" · ");
}

/** Plugins grouped by marketplace, groups in the order their first plugin
 * appears; plugins from no marketplace (a plugin directory) group under null. */
export function pluginGroups(
	preview: PluginPreviewResponse,
): { marketplace: string | null; plugins: PluginLaunchCandidate[] }[] {
	const groups = new Map<string | null, PluginLaunchCandidate[]>();
	for (const plugin of preview.plugins) {
		const key = plugin.marketplace ?? null;
		const group = groups.get(key);
		if (group) group.push(plugin);
		else groups.set(key, [plugin]);
	}
	return [...groups].map(([marketplace, plugins]) => ({ marketplace, plugins }));
}

export function pluginWarnings(preview: PluginPreviewResponse, name: string): string[] {
	return (preview.diagnostics ?? [])
		.filter((diagnostic) => diagnostic.name === name)
		.map((diagnostic) => diagnostic.message);
}
