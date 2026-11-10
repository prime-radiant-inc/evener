// All plugin surfaces share the same per-kind counts vocabulary.

import type { PluginComponents } from "@evener/appwire-client";

export function hookTotal(components: PluginComponents): number {
  return (components.hooks ?? []).reduce((total, hook) => total + hook.count, 0);
}

export function countsSummary(
  skills: number,
  agents: number,
  commands: number,
  hooks: number,
  mcpServers: number,
): string {
  const counts: string[] = [];
  if (skills > 0) counts.push(`${skills} skill${skills === 1 ? "" : "s"}`);
  if (agents > 0) counts.push(`${agents} agent${agents === 1 ? "" : "s"}`);
  if (commands > 0) counts.push(`${commands} command${commands === 1 ? "" : "s"}`);
  if (hooks > 0) counts.push(`${hooks} hook${hooks === 1 ? "" : "s"}`);
  if (mcpServers > 0) counts.push(`${mcpServers} MCP server${mcpServers === 1 ? "" : "s"}`);
  return counts.length > 0 ? counts.join(" · ") : "No components";
}
