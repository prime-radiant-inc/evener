import type { MCPServerSpec } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

export async function addLaunchMcp(
  client: ConversationClientLike,
  items: MCPServerSpec[],
  raw: string,
) {
  const [name, command, ...args] = raw.trim().split(/\s+/).filter(Boolean);
  if (!name || !command) throw Error("Use: name command [args...]");
  const result = await client.request("evener/path/validate", {
    path: command,
    kind: "command",
  });
  if (!result.valid) throw Error(result.error || "Invalid command.");
  return [...items, { name, command: result.path || command, args }];
}

/** Preserve argument boundaries when comparing saved server definitions. */
export function resourceKey(item: string | MCPServerSpec): string {
  return typeof item === "string"
    ? `path:${item}`
    : JSON.stringify([item.name, item.command, item.args ?? []]);
}
