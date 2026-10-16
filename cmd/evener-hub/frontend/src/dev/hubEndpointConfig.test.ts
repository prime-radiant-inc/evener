// @vitest-environment node
import { execFileSync } from "node:child_process";
import { env } from "node:process";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// The child runs Vite's real Node config loader and dev server. Vitest's
// vmThreads realm cannot pass Vite/Rolldown native plugin objects across VM
// contexts; this test asserts the child's observations, not a surrogate config.
const probe = fileURLToPath(new URL("./hubEndpointConfigProbe.mjs", import.meta.url).href);
function observe(command: string, values: Record<string, string | undefined> = {}) {
  const variables = { ...env, ...values };
  for (const [key, value] of Object.entries(variables)) if (value === undefined) delete variables[key];
  return JSON.parse(execFileSync("node", [probe, command], { encoding: "utf8", env: variables })) as Record<
    string,
    unknown
  >;
}
const noOldSetting = { EVENER_HUB_ADDR: undefined };

describe("Vite hub endpoint configuration", () => {
  it("uses the documented local default only when the URL is absent", () => {
    expect(observe("config", { ...noOldSetting, EVENER_HUB_URL: undefined })).toMatchObject({
      status: 0,
      data: { api: "http://127.0.0.1:9180" },
    });
  });

  it.each([
    ["empty", "", "empty", "origin"],
    ["path", "https://hub.test/a/..", "path", "path"],
    ["userinfo", "https://agent:secret-value@hub.test", "userinfo", "userinfo"],
    ["numeric host alias", "http://127.1", "host", "host"],
  ])("refuses invalid %s setting", (_name, value, code, field) => {
    expect(observe("config", { ...noOldSetting, EVENER_HUB_URL: value })).toMatchObject({
      status: 3,
      data: { code, field },
    });
  });

  it("rejects the retired setting even when the new URL is valid", () => {
    expect(
      observe("config", { EVENER_HUB_URL: "https://hub.test", EVENER_HUB_ADDR: "http://127.0.0.1:9180" }),
    ).toMatchObject({ status: 3 });
  });

  it("routes real HTTP and WS requests to the resolved origin and rewrites WS Origin", () => {
    const result = observe("proxy", noOldSetting) as {
      loaded: { api: string; rpc: { target: string; headers: { Origin: string }; ws: boolean; changeOrigin: boolean } };
      backendOrigin: string;
      backendPort: number;
      viteOrigin: string;
      http: { status: number; body: unknown };
      wsMessage: string;
      requests: { path: string; host: string; origin: string }[];
    };
    expect(result.loaded.api).toBe(result.backendOrigin);
    expect(result.loaded.rpc).toMatchObject({
      target: result.backendOrigin,
      ws: true,
      changeOrigin: true,
      headers: { Origin: result.backendOrigin },
    });
    expect(result.http).toEqual({ status: 200, body: { path: "/api/proxy-check" } });
    expect(result.wsMessage).toBe("proxy-ok");
    expect(result.requests).toEqual([
      { path: "/api/proxy-check", host: `127.0.0.1:${result.backendPort}`, origin: result.viteOrigin },
      { path: "/rpc", host: `127.0.0.1:${result.backendPort}`, origin: result.backendOrigin },
    ]);
  });
});
