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
//
// list() requests one extra thread beyond the selected page size to detect
// hasMore. The default page size is 500; smaller screens can choose less.
// There is no cursor pagination — the roster is a single-page view.

import type {
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
  list(): Promise<{ threads: RosterEntry[]; hasMore: boolean }>;
  refresh(): Promise<void>;
}

// The maximum number of threads displayed in the roster.
export const ROSTER_PAGE_SIZE = 500;

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
  pageSize = ROSTER_PAGE_SIZE,
): RosterService {
  if (
    !Number.isInteger(pageSize) ||
    pageSize < 1 ||
    pageSize > ROSTER_PAGE_SIZE
  ) {
    throw new RangeError(
      `Roster page size must be an integer between 1 and ${ROSTER_PAGE_SIZE}`,
    );
  }
  return {
    async list() {
      const response: ThreadListResponse = await client.request("thread/list", {
        limit: pageSize + 1,
      });
      const rows = response.data ?? [];
      // Multiple server thread records can resolve to the same navigable session.
      // Keep the first projection for each canonical route, in server order.
      const sessions = new Map<string, RosterEntry>();
      for (const thread of rows) {
        const entry = projectThread(thread);
        if (!sessions.has(entry.ref)) sessions.set(entry.ref, entry);
      }
      return {
        threads: [...sessions.values()].slice(0, pageSize),
        // Overflow reflects raw server rows: unseen rows may hold more sessions.
        hasMore: rows.length > pageSize,
      };
    },

    async refresh() {
      await this.list();
    },
  };
}

export type {
  HarnessDescriptor,
  HarnessListResponse,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
// Re-export for convenience so tests and screens can import from one place.
export type { ConversationClientLike } from "./conversation";
