import { describe, expect, it } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { NavigationActions } from "./navigationActions";

describe("navigation organization actions", () => {
  it("sends canonical project identity once and awaits projection before success", async () => {
    const sent: unknown[] = [];
    let finish!: (value: unknown) => void;
    let reloaded = false;
    const receipt = { generation_id: "g", targets: [] };
    const client = {
      request: (method: string, params: unknown) => {
        sent.push({ method, params });
        return new Promise<unknown>((resolve) => {
          finish = resolve;
        });
      },
    } as unknown as ConversationClientLike;
    const actions = new NavigationActions(
      client,
      async (value) => {
        expect(value).toEqual(receipt);
        reloaded = true;
      },
      () => true,
    );
    const run = actions.archive(
      { kind: "project", id: "project-key", workingDir: "/workspace" },
      true,
    );
    await actions.favorite("project-key", true);
    expect(sent).toEqual([
      {
        method: "evener/archive/set",
        params: {
          kind: "project",
          id: "project-key",
          workingDir: "/workspace",
          archived: true,
        },
      },
    ]);
    expect(reloaded).toBe(false);
    finish({ ok: true, navigation: receipt });
    await run;
    expect(reloaded).toBe(true);
    expect(actions.getSnapshot().pending).toBe(false);
  });
  it("rejects stale confirmations and does not refresh after disposal", async () => {
    let current = false,
      calls = 0,
      refreshes = 0;
    let finish!: (value: unknown) => void;
    const client = {
      request: () => {
        calls++;
        return new Promise<unknown>((resolve) => {
          finish = resolve;
        });
      },
    } as unknown as ConversationClientLike;
    const actions = new NavigationActions(
      client,
      async () => {
        refreshes++;
      },
      () => current,
    );
    await actions.archive({ kind: "session", id: "remote:session" }, false);
    expect(calls).toBe(0);
    current = true;
    const request = actions.favorite("project", false);
    actions.dispose();
    finish({ ok: true, navigation: { generation_id: "g", targets: [] } });
    await request;
    expect(refreshes).toBe(0);
  });
  it("retains an uncertain failure without replaying the mutation", async () => {
    let attempts = 0;
    const client = {
      request: async () => {
        attempts++;
        throw new Error("Disconnected");
      },
    } as unknown as ConversationClientLike;
    const actions = new NavigationActions(
      client,
      async () => {},
      () => true,
    );
    await actions.archive({ kind: "session", id: "local:s" }, true);
    expect(attempts).toBe(1);
    expect(actions.getSnapshot().error).not.toBeNull();
    expect(actions.getSnapshot().pending).toBe(false);
  });
});
