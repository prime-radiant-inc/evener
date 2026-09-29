// toolWireFixtures reads the daemon's tool calls as the hub sends them: one
// call and its result per tool, with the arguments each tool's own definition
// names (agent/internal/tool/definitions.go), two MCP tools named the way the
// MCP manager namespaces them, and a tool no summary covers.
//
// agent's TestToolCallWireFixtures produces the fixture by projecting the
// recorded turns through apptranscript, and re-verifies it on every Go test
// run; regenerate it with `make fuzz-goldens`. It is loaded through a `?raw`
// import, as notificationWireFixtures loads its own.

import toolCalls from "../../../agent/testdata/toolwire/calls.json?raw";
import type { ItemModel, ThreadModel } from "../model";
import { hydrateThread } from "../reducer";
import type { Thread, ThreadItem } from "../types.gen";

interface ToolWireFixture {
  cwd: string;
  notes: Record<string, string>;
  items: ThreadItem[];
}

const fixture = (): ToolWireFixture => JSON.parse(toolCalls) as ToolWireFixture;

/** The recorded call ids, one per case. */
export type ToolWireCall =
  | "call_read_file"
  | "call_read_file_range"
  | "call_grep"
  | "call_glob"
  | "call_list_dir"
  | "call_edit_file"
  | "call_write_file"
  | "call_apply_patch"
  | "call_shell"
  | "call_shell_failed"
  | "call_web_fetch"
  | "call_web_search"
  | "call_use_skill"
  | "call_mcp"
  | "call_mcp_hyphenated"
  | "call_unknown";

/** The session directory the recorded shell call's cd names. */
export function toolWireCwd(): string {
  return fixture().cwd;
}

/** The recorded call and result items, in the order history carries them. */
export function toolWireItems(): ThreadItem[] {
  return fixture().items;
}

/** A thread holding every recorded call and result in one completed turn. */
export function toolWireThread(): Thread {
  return {
    id: "thread-tools",
    sessionId: "session-tools",
    preview: "",
    ephemeral: false,
    modelProvider: "anthropic",
    createdAt: 0,
    updatedAt: 0,
    status: { type: "ready" },
    cwd: toolWireCwd(),
    cliVersion: "1.0.0",
    source: "local",
    turns: [{ id: "turn_1", itemsView: "full", status: "completed", items: toolWireItems() }],
    evener: { ref: "ref-tools", queue: { revision: 0 } },
  } as unknown as Thread;
}

/** The thread as a client's model holds it: each call merged with its result. */
export function toolWireModel(): ThreadModel {
  return hydrateThread({ thread: toolWireThread() }, "ref-tools", 0);
}

/** One settled step: the call merged with its result, as a client holds it. */
export function toolWireStep(callId: ToolWireCall): ItemModel {
  const item = toolWireModel()
    .turns.flatMap((turn) => turn.items)
    .find((candidate) => candidate.callId === callId);
  if (!item) throw new Error(`no ${callId} step in agent/testdata/toolwire/calls.json`);
  return item;
}
