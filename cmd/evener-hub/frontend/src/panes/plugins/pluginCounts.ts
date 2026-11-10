// All plugin surfaces share the same per-kind counts vocabulary.

import type { PluginComponents } from "@evener/appwire-client";

export function hookTotal(components: PluginComponents): number {
  return (components.hooks ?? []).reduce((total, hook) => total + hook.count, 0);
}

function part(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

export function countsSummary(
  skills: number,
  agents: number,
  commands: number,
  hooks: number,
  mcpServers: number,
): string {
  const counts: string[] = [];
  if (skills > 0) counts.push(part(skills, "skill"));
  if (agents > 0) counts.push(part(agents, "agent"));
  if (commands > 0) counts.push(part(commands, "command"));
  if (hooks > 0) counts.push(part(hooks, "hook"));
  if (mcpServers > 0) counts.push(part(mcpServers, "MCP server"));
  return counts.length > 0 ? counts.join(" · ") : "No components";
}
