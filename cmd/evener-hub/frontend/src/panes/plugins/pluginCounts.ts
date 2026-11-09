// The one counts vocabulary every plugin surface speaks - the launcher's own
// pluralized form, moved here from PluginSelectionPanel's componentCounts so
// the installed list's row meta and the launcher's switch rows can never
// drift apart. An all-zero inventory reads "No components"; callers render
// the string as-is.

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
