// RosterService wraps an AppwireClient's thread/list method to provide the
// session roster — a flat list of threads projected to RosterEntry with
// attention classification. It is the protocol/native service layer between
// the Zustand RosterStore and the wire.
//
// The service does NOT create or own the socket — it wraps an existing
// ConversationClientLike (structurally compatible with AppwireClient). It calls
// thread/list and projects each Thread to a RosterEntry, classifying attention
// as "needsYou" (awaiting status or askPending), "running" (active status), or
// "recent" (everything else).

import type {
  HarnessDescriptor,
  HarnessListResponse,
  MethodTypes,
  Thread,
  ThreadListResponse,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "./conversation";

// Attention classification for a roster entry.
export type Attention = "needsYou" | "running" | "recent";

export interface RosterEntry {
  ref: string;
  title: string;
  project: string;
  status: string;
  updatedAt: number;
  attention: Attention;
  askPending?: boolean;
}

export interface RosterService {
  list(
    cursor?: string,
  ): Promise<{ threads: RosterEntry[]; nextCursor?: string }>;
  refresh(): Promise<void>;
}

// Classify a thread's attention based on its status and askPending flag.
// - awaiting status or askPending=true => "needsYou" (agent needs user input)
// - active status => "running" (agent is working)
// - everything else => "recent"
function classifyAttention(thread: Thread): Attention {
  if (thread.status?.type === "awaiting") return "needsYou";
  if (thread.evener?.askPending === true) return "needsYou";
  if (thread.status?.type === "active") return "running";
  return "recent";
}

// Project a wire Thread to a RosterEntry.
function projectThread(thread: Thread): RosterEntry {
  return {
    ref: thread.evener?.ref ?? thread.id,
    title: thread.name ?? thread.preview ?? "",
    project: thread.cwd ?? "",
    status: thread.status?.type ?? "idle",
    updatedAt: thread.updatedAt,
    attention: classifyAttention(thread),
    askPending: thread.evener?.askPending === true ? true : undefined,
  };
}

export function createRosterService(
  client: ConversationClientLike,
): RosterService {
  return {
    async list(cursor) {
      const params: MethodTypes["thread/list"]["params"] = {};
      if (cursor !== undefined) {
        params.cursor = cursor;
      }
      const response: ThreadListResponse = await client.request(
        "thread/list",
        params,
      );
      const threads = (response.data ?? []).map(projectThread);
      return { threads, nextCursor: response.nextCursor };
    },

    async refresh() {
      await this.list();
    },
  };
}

// Re-export for convenience so tests and screens can import from one place.
export type { ConversationClientLike, HarnessDescriptor, HarnessListResponse };
