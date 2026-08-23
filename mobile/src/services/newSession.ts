// NewSessionService wraps an AppwireClient's thread/start,
// evener/projects/recent, and evener/harnesses/list methods to provide a
// typed API for starting a new session thread. It is the protocol/native
// service layer between the NewSessionScreen and the wire.
//
// The service does NOT create or own the socket — it wraps an existing
// ConversationClientLike (structurally compatible with AppwireClient). It
// calls thread/start with the provided params and returns the new thread and
// first turn. It also fetches recent projects and available harnesses for the
// form's autocomplete and optional fields.

import type {
  HarnessDescriptor,
  HarnessListResponse,
  InputItem,
  MethodTypes,
  ProjectsRecentResponse,
  Thread,
  ThreadStartResponse,
  Turn,
} from "../../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "./conversation";

export interface NewSessionParams {
  cwd: string;
  input?: InputItem[];
  modelProvider?: string;
  model?: string;
  harness?: string;
  reasoningEffort?: string;
}

export interface NewSessionService {
  start(params: NewSessionParams): Promise<{ thread: Thread; turn: Turn }>;
  recentProjects(): Promise<string[]>;
  harnesses(): Promise<HarnessDescriptor[]>;
}

export function createNewSessionService(
  client: ConversationClientLike,
): NewSessionService {
  return {
    async start(params) {
      const wireParams: MethodTypes["thread/start"]["params"] = {
        cwd: params.cwd,
      };
      if (params.input !== undefined) {
        wireParams.input = params.input;
      }
      if (params.modelProvider !== undefined) {
        wireParams.modelProvider = params.modelProvider;
      }
      if (params.model !== undefined) {
        wireParams.model = params.model;
      }
      if (params.harness !== undefined) {
        wireParams.harness = params.harness;
      }
      if (params.reasoningEffort !== undefined) {
        wireParams.reasoningEffort = params.reasoningEffort;
      }
      const response: ThreadStartResponse = await client.request(
        "thread/start",
        wireParams,
      );
      return { thread: response.thread, turn: response.turn };
    },

    async recentProjects() {
      const response: ProjectsRecentResponse = await client.request(
        "evener/projects/recent",
        {},
      );
      return response.data ?? [];
    },

    async harnesses() {
      const response: HarnessListResponse = await client.request(
        "evener/harnesses/list",
        {},
      );
      return response.data ?? [];
    },
  };
}

export type { ConversationClientLike };
