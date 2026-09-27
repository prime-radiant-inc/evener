// Hub connection configuration, resolved from the environment the session's
// .mcp.json entry controls. The token is a full-hub bearer capability (see
// docs/evener-hub.md, Trust boundary); the token-file default lets a session
// configure this MCP without pasting that capability into a config file the
// model can read — the server reads it from the hub's own state root at
// runtime, exactly where the TUI and scripted clients read it.

import fs from "node:fs";
import path from "node:path";

export const DEFAULT_HUB_RPC_URL = "ws://127.0.0.1:9180/rpc";

export interface HubConfig {
  /** The hub's AppWire /rpc WebSocket URL. */
  url: string;
  /** The hub capability token, trimmed. Never logged. */
  token: string;
  /** Where the token came from, for configuration diagnostics (no secret). */
  tokenSource: string;
  /**
   * EVENER_HUB_MCP_READONLY=1 registers only the read tools — supervision
   * without mutation. Only the literal "1" counts as set: every other value
   * ("true", "yes", even "1 " with a stray space) leaves the full tool set
   * registered, visibly so in tools/list, rather than guessing at what an
   * operator might have meant.
   */
  readOnly?: boolean;
  /**
   * EVENER_HUB_MCP_PROJECT: the one project (absolute path) this server may
   * see — rows, searches, starts, and named-ref tools are narrowed to it.
   * Unset serves every project. Non-absolute values fail closed: the server
   * starts unconfigured and every hub-touching call reports the error.
   */
  projectScope?: string;
}

/**
 * stateRoot resolves the hub state root the same way the hub does: an
 * explicit override, then XDG_STATE_HOME/evener, then ~/.local/state/evener.
 */
export function stateRoot(env: Record<string, string | undefined>): string {
  if (env.EVENER_HUB_STATE_ROOT) return env.EVENER_HUB_STATE_ROOT;
  if (env.XDG_STATE_HOME) return path.join(env.XDG_STATE_HOME, "evener");
  const home = env.HOME;
  if (!home) throw new Error("cannot locate the hub state root: set HOME, XDG_STATE_HOME, or EVENER_HUB_STATE_ROOT");
  return path.join(home, ".local", "state", "evener");
}

/**
 * resolveConfig picks the hub URL and token from env. EVENER_HUB_TOKEN wins;
 * otherwise the token file is read (EVENER_HUB_TOKEN_FILE, defaulting to
 * <state root>/auth-token). readFile is injectable for tests; the trimming,
 * the empty-file refusal, and the actionable missing-file error all live
 * here so every reader gets them.
 */
export function resolveConfig(
  env: Record<string, string | undefined>,
  readFile: (file: string) => string = (file) => fs.readFileSync(file, "utf8"),
): HubConfig {
  const url = env.EVENER_HUB_RPC_URL?.trim() || DEFAULT_HUB_RPC_URL;
  const readOnly = env.EVENER_HUB_MCP_READONLY === "1";
  const projectScopeRaw = env.EVENER_HUB_MCP_PROJECT?.trim();
  if (projectScopeRaw && !path.isAbsolute(projectScopeRaw)) {
    throw new Error(
      `EVENER_HUB_MCP_PROJECT must be an absolute path (got "${projectScopeRaw}"); ` +
        `set it to the absolute path of the one project this server may see, or unset it to serve every project.`,
    );
  }
  const projectScope = projectScopeRaw ? path.resolve(projectScopeRaw) : undefined;
  if (env.EVENER_HUB_TOKEN && env.EVENER_HUB_TOKEN.trim() !== "") {
    return {
      url,
      token: env.EVENER_HUB_TOKEN.trim(),
      tokenSource: "EVENER_HUB_TOKEN",
      readOnly: readOnly || undefined,
      projectScope,
    };
  }
  const file = env.EVENER_HUB_TOKEN_FILE?.trim() || path.join(stateRoot(env), "auth-token");
  let raw: string;
  try {
    raw = readFile(file);
  } catch (err) {
    throw new Error(
      `cannot read the hub token file ${file}: ${err instanceof Error ? err.message : String(err)}. ` +
        `Set EVENER_HUB_TOKEN, point EVENER_HUB_TOKEN_FILE at the hub's auth-token file, or start the hub so it creates one.`,
    );
  }
  const token = raw.trim();
  if (!token) {
    throw new Error(`the hub token file ${file} is empty; start the hub so it writes a token, or set EVENER_HUB_TOKEN`);
  }
  return { url, token, tokenSource: `file ${file}`, readOnly: readOnly || undefined, projectScope };
}
