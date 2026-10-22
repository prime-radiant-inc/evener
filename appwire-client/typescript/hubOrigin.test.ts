// @vitest-environment node
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { HubOriginError, hubRPCURL, parseHubOrigin } from "./hubOrigin";

type Case = {
  name: string;
  raw: string;
  origin?: string;
  rpc_url?: string;
  error?: { code: string; field: string };
};
const cases = JSON.parse(
  readFileSync(fileURLToPath(new URL("../../testdata/hubendpoint/origins.json", import.meta.url).href), "utf8"),
) as Case[];

describe("shared literal hub origin corpus", () => {
  for (const entry of cases) {
    it(entry.name, () => {
      if (entry.error) {
        let thrown: unknown;
        try {
          parseHubOrigin(entry.raw);
        } catch (error) {
          thrown = error;
        }
        expect(thrown).toBeInstanceOf(HubOriginError);
        expect(thrown).toMatchObject(entry.error);
        if (entry.raw) expect(String(thrown)).not.toContain(entry.raw);
        return;
      }
      expect(parseHubOrigin(entry.raw)).toBe(entry.origin);
      expect(hubRPCURL(entry.raw)).toBe(entry.rpc_url);
      expect(hubRPCURL(entry.origin as string)).toBe(entry.rpc_url);
    });
  }
  it("does not echo credential-bearing invalid input", () => {
    for (const input of ["https://agent:secret-value@example.test", "http://hub.test?token=secret-value"]) {
      expect(() => hubRPCURL(input)).toThrow(HubOriginError);
      try {
        hubRPCURL(input);
      } catch (error) {
        expect(String(error)).not.toContain("secret-value");
        expect(String(error)).not.toContain(input);
      }
    }
  });
});
