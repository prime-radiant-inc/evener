import { expect, it } from "vitest";
import type { LaunchOption } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { addLaunchPath } from "./launchPaths";

const option: LaunchOption = {
  field: "mcp_configs",
  wireField: "mcpConfigs",
  label: "MCP config files",
  group: "Resources",
  kind: "pathList",
  pathKind: "file",
  perLaunch: true,
};
it("validates file paths on the hub and appends their canonical spelling", async () => {
  const calls: unknown[] = [];
  const client = {
    request: async (method: string, params: unknown) => {
      calls.push({ method, params });
      return { valid: true, path: "/work/mcp config.json" };
    },
  } as unknown as ConversationClientLike;
  expect(
    await addLaunchPath(client, option, ["/other.json"], " ~/mcp config.json "),
  ).toEqual(["/other.json", "/work/mcp config.json"]);
  expect(calls).toEqual([
    {
      method: "evener/path/validate",
      params: { path: "~/mcp config.json", kind: "file" },
    },
  ]);
});
it("rejects empty, invalid and canonically duplicate paths", async () => {
  let result = { valid: false, path: "/x", error: "invalid fixture" };
  const client = {
    request: async () => result,
  } as unknown as ConversationClientLike;
  await expect(addLaunchPath(client, option, [], " ")).rejects.toThrow();
  await expect(addLaunchPath(client, option, [], "/bad")).rejects.toThrow();
  result = { valid: true, path: "/x", error: "" };
  await expect(
    addLaunchPath(client, option, ["/x"], "/alias"),
  ).rejects.toThrow();
});
it("preserves the web path collector's transport-failure behavior", async () => {
  const client = {
    request: async () => {
      throw Error("offline");
    },
  } as unknown as ConversationClientLike;
  expect(await addLaunchPath(client, option, [], "/manual.json")).toEqual([
    "/manual.json",
  ]);
});
