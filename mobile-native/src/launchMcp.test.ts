import { expect, it } from "vitest";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { addLaunchMcp, resourceKey } from "./launchMcp";

it("uses the web whitespace syntax and validates the command on the hub", async () => {
  const calls: unknown[] = [];
  const client = {
    request: async (method: string, params: unknown) => {
      calls.push({ method, params });
      return { valid: true, path: "/usr/bin/env" };
    },
  } as unknown as ConversationClientLike;
  const original = [
    { name: "existing", command: "/bin/old", args: ["one argument"] },
  ];
  expect(
    await addLaunchMcp(client, original, " tools env  node server.js "),
  ).toEqual([
    ...original,
    { name: "tools", command: "/usr/bin/env", args: ["node", "server.js"] },
  ]);
  expect(calls).toEqual([
    {
      method: "evener/path/validate",
      params: { path: "env", kind: "command" },
    },
  ]);
  expect(original).toHaveLength(1);
});
it("rejects incomplete syntax, invalid commands and transport failures", async () => {
  const client = {
    request: async () => ({ valid: false, error: "invalid fixture" }),
  } as unknown as ConversationClientLike;
  await expect(addLaunchMcp(client, [], "name")).rejects.toThrow();
  await expect(
    addLaunchMcp(client, [], "name missing-command"),
  ).rejects.toThrow();
  const offline = {
    request: async () => {
      throw Error("offline");
    },
  } as unknown as ConversationClientLike;
  await expect(addLaunchMcp(offline, [], "name command")).rejects.toThrow();
});
it("distinguishes MCP argument boundaries and resource kinds", () => {
  expect(resourceKey({ name: "n", command: "c", args: ["a b"] })).not.toBe(
    resourceKey({ name: "n", command: "c", args: ["a", "b"] }),
  );
  expect(resourceKey({ name: "n", command: "c" })).toBe(
    resourceKey({ name: "n", command: "c", args: [] }),
  );
  expect(resourceKey("n")).not.toBe(resourceKey({ name: "n", command: "" }));
});
