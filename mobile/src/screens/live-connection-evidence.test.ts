import { describe, expect, it } from "vitest";
import { digestProfileOrigin } from "./live-connection-evidence";

describe("digestProfileOrigin", () => {
  it("returns the standard SHA-256 digest without retaining source text", async () => {
    await expect(digestProfileOrigin("")).resolves.toBe(
      "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    );
  });

  it("fails closed when Web Crypto is unavailable", async () => {
    await expect(digestProfileOrigin("https://private", null)).rejects.toThrow(
      "origin digest unavailable",
    );
  });
});
