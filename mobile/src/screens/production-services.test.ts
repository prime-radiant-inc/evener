import { describe, expect, it, vi } from "vitest";
import type {
  ConversationClientLike,
  LiveConversationService,
} from "../services/conversation";
import type { ProfileRedacted } from "../services/nativeProfiles";
import { createProfileScopedServices } from "./production-services";

const PROFILE: ProfileRedacted = {
  id: "profile-1",
  name: "Hub",
  origin: "http://192.168.1.10:9180",
};

describe("createProfileScopedServices", () => {
  it("builds every live session service around one profile-scoped AppWire client", () => {
    const client: ConversationClientLike & {
      connect: () => Promise<unknown>;
      close: () => void;
      onStateChange: (handler: (state: string) => void) => () => void;
    } = {
      request: vi.fn(),
      onNotification: vi.fn(() => () => {}),
      connect: vi.fn(() => Promise.resolve({})),
      close: vi.fn(),
      onStateChange: vi.fn(() => () => {}),
    };
    const createClient = vi.fn(() => client);

    const scoped = createProfileScopedServices(PROFILE, createClient);
    const liveConversationService: LiveConversationService =
      scoped.conversationService;

    expect(createClient).toHaveBeenCalledWith(PROFILE);
    expect(scoped.client).toBe(client);
    expect(scoped.rosterService).toBeDefined();
    expect(scoped.rosterStore).toBeDefined();
    expect(scoped.newSessionService).toBeDefined();
    expect(liveConversationService).toBeDefined();
  });
});
