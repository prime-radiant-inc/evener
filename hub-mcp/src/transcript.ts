// The transcript reader: windowed turns from the hub, rendered as readable
// text. Two detail levels:
//   - outline (default): messages in full, tool calls as one summary line
//   - full: tool calls also carry their truncated output
// The cursor is the hub's own olderCursor/nextCursor — pass it back to page
// further into history.

import type { ThreadItem, Turn } from "@evener/appwire-client";

import type { HubPort } from "./hub.js";
import { fmtDuration, truncate } from "./render.js";

export type TranscriptDetail = "outline" | "full";

export interface TranscriptWindow {
  turns: Turn[];
  /** Pass to read_transcript's cursor to read the next older page, if any. */
  nextCursor?: string;
  /** True when older history exists beyond this window. */
  hasMore: boolean;
}

// The hub's thread/read and thread/turns/list validate itemLimit against
// 1..40 (appwire paging); the default asks for the maximum window.
const DEFAULT_ITEM_LIMIT = 40;
const MAX_ITEM_LIMIT = 40;

/**
 * readTranscriptWindow fetches one window of a session's transcript. Without
 * a cursor it reads the newest turns (thread/read with turns included, which
 * also subscribes the watcher); with one it pages older via thread/turns/list.
 */
export async function readTranscriptWindow(
  port: HubPort,
  ref: string,
  opts: { cursor?: string; itemLimit?: number } = {},
): Promise<TranscriptWindow> {
  const itemLimit = Math.min(Math.max(opts.itemLimit ?? DEFAULT_ITEM_LIMIT, 1), MAX_ITEM_LIMIT);
  if (opts.cursor) {
    const page = await port.request("thread/turns/list", { ref, cursor: opts.cursor, itemsView: "full", itemLimit });
    return { turns: page.data, nextCursor: page.nextCursor, hasMore: Boolean(page.nextCursor) };
  }
  const read = await port.request("thread/read", { ref, includeTurns: true, subscribe: true, itemLimit });
  const turns = read.thread.turns ?? [];
  const older = read.olderCursor;
  return { turns, nextCursor: older || undefined, hasMore: Boolean(older) };
}

function toolCallLine(item: ThreadItem): string {
  const name = item.toolName ?? item.type;
  const args = parseFirstArg(item);
  const dur = item.durationMs ? fmtDuration(item.durationMs) : "";
  const exit = typeof item.exitCode === "number" ? ` exit ${item.exitCode}` : "";
  return `${name} ${args}`.trim() + (dur ? ` (${dur}${exit.trim()})` : exit ? ` (exit${exit})` : "");
}

function parseFirstArg(item: ThreadItem): string {
  if (!item.argumentsJson) return item.description ? truncate(item.description, 100) : "";
  try {
    const parsed: unknown = JSON.parse(item.argumentsJson);
    if (parsed && typeof parsed === "object") {
      const values = Object.values(parsed as Record<string, unknown>);
      const first = values.find((v) => typeof v === "string" && v.length > 0);
      if (typeof first === "string") return truncate(first, 100);
    }
  } catch {
    /* fall through to the raw JSON */
  }
  return truncate(item.argumentsJson, 80);
}

function renderItem(item: ThreadItem, detail: TranscriptDetail): string[] {
  const out: string[] = [];
  switch (item.type) {
    case "userMessage":
      out.push(`user: ${detail === "full" ? truncate(item.text ?? "", 2000) : truncate(item.text ?? "", 400)}`);
      break;
    case "agentMessage":
      out.push(`assistant: ${detail === "full" ? truncate(item.text ?? "", 3000) : truncate(item.text ?? "", 600)}`);
      break;
    case "steering": {
      const source =
        item.source === "user" ? "user (steer)" : item.steeringKind ? `steering/${item.steeringKind}` : "steering";
      out.push(`${source}: ${truncate(item.text ?? "", 400)}`);
      break;
    }
    case "reasoning":
      // Thinking summaries are noise for a supervisor at outline level.
      if (detail === "full" && item.text) out.push(`thinking: ${truncate(item.text, 400)}`);
      break;
    case "commandExecution":
    case "mcpToolCall":
    case "dynamicToolCall": {
      out.push(`tool: ${toolCallLine(item)}`);
      if (item.error) out.push(`  error: ${truncate(item.error, 300)}`);
      if (detail === "full" && item.output) out.push(`  output: ${truncate(item.output, 1200)}`);
      break;
    }
    case "systemMessage":
      if (item.eventKind) out.push(`system (${item.eventKind}): ${truncate(item.text ?? item.description ?? "", 240)}`);
      else if (item.error) out.push(`system error: ${truncate(item.error, 300)}`);
      else if (item.text) out.push(`system: ${truncate(item.text, 240)}`);
      break;
    default:
      if (item.error) out.push(`${item.type}: error ${truncate(item.error, 300)}`);
      else if (item.text) out.push(`${item.type}: ${truncate(item.text, 240)}`);
      break;
  }
  return out;
}

/** renderTranscript renders a window of turns as readable text. */
export function renderTranscript(turns: Turn[], detail: TranscriptDetail): string {
  const blocks: string[] = [];
  for (const turn of turns) {
    const head: string[] = [`turn ${turn.id} — ${turn.status}`];
    const dur = fmtDuration(turn.durationMs);
    if (dur) head.push(dur);
    if (turn.cost) head.push(turn.cost);
    const lines: string[] = [head.join(", ")];
    for (const item of turn.items ?? []) {
      lines.push(...renderItem(item, detail));
    }
    if (turn.error) {
      const hint = turn.error.hint ? ` (hint: ${turn.error.hint})` : "";
      lines.push(`turn failed: ${truncate(turn.error.message, 300)}${hint}`);
    }
    blocks.push(lines.join("\n"));
  }
  return blocks.join("\n\n");
}
