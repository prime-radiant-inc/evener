import type { Thread, ThreadCapabilities } from "@evener/appwire-client";

/** The capability set a fully-equipped session advertises to the surfaces. */
export const FULL_CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  sharedNotes: true,
  rename: true,
};

/** The wire-shaped thread every surface fixture starts from; callers override
 *  the parts their surface exercises. */
export function fixtureThread(ref: string, overrides: Partial<Thread> = {}): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: "dev fixture",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/home/dev/project",
    cliVersion: "1.0.0",
    source: "evener",
    evener: { ref, capabilities: FULL_CAPABILITIES, queue: { revision: 0 } },
    ...overrides,
  };
}
