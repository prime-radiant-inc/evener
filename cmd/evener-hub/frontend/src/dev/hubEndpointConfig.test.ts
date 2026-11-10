// @vitest-environment node
import { execFileSync } from "node:child_process";
import { env } from "node:process";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// The child runs Vite's real Node config loader and dev server. Vitest's
// vmThreads realm cannot pass Vite/Rolldown native plugin objects across VM
// contexts; this test asserts the child's observations, not a surrogate config.
const probe = fileURLToPath(new URL("./hubEndpointConfigProbe.mjs", import.meta.url).href);
function observe(command: string, values: Record<string, string | undefined> = {}, timeout = 30_000) {
  const variables = { ...env, ...values };
  for (const [key, value] of Object.entries(variables)) if (value === undefined) delete variables[key];
  const options = { encoding: "utf8" as const, env: variables, timeout };
  return JSON.parse(execFileSync("node", [probe, command], options)) as Record<string, unknown>;
}
const noOldSetting = { EVENER_HUB_ADDR: undefined };
function expectClosed(result: { vitePid: number; backendPort: number; vitePort: number }) {
  expect(
    observe("cleanup", {
      PROBE_VITE_PID: String(result.vitePid),
      PROBE_BACKEND_PORT: String(result.backendPort),
      PROBE_VITE_PORT: String(result.vitePort),
    }),
  ).toEqual({ viteAlive: false, backendAvailable: true, viteAvailable: true });
}

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

  it("bounds a silent nested config loader", () => {
    expect(observe("config", { ...noOldSetting, EVENER_PROBE_STALL_CONFIG: "1" })).toMatchObject({
      timeout: "configChild",
    });
  }, 9_000);

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
  it("times out a silent WS backend and closes both disposable listeners", () => {
    const result = observe("proxy-stall", noOldSetting) as {
      timeout: string;
      vitePid: number;
      backendPort: number;
      vitePort: number;
    };
    expect(result.timeout).toBe("wsMessage");
    expectClosed(result);
  }, 35_000);
  it("terminates a Vite child when graceful shutdown stalls", () => {
    const result = observe("proxy-shutdown-stall", noOldSetting) as {
      vitePid: number;
      backendPort: number;
      vitePort: number;
      wsMessage: string;
      shutdownForced: boolean;
    };
    expect(result.wsMessage).toBe("proxy-ok");
    expect(result.shutdownForced).toBe(true);
    expectClosed(result);
  }, 12_000);
  it.each([0, 3_500])(
    "bounds the outer probe and kills its live Vite child after %ims startup delay",
    (startupDelay) => {
      const result = observe(
        "outer-stall",
        { ...noOldSetting, EVENER_PROBE_STARTUP_DELAY: String(startupDelay) },
        45_000,
      ) as {
        timedOut: boolean;
        code: number;
        signal: string | null;
        vitePid: number;
        backendPort: number;
        vitePort: number;
      };
      expect(result.timedOut).toBe(true);
      expect(result.code).toBe(1);
      expect(result.signal).toBeNull();
      expectClosed(result);
    },
    50_000,
  );
});
