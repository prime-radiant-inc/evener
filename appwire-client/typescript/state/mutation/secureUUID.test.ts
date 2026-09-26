import { afterEach, describe, expect, it, vi } from "vitest";

import { createSecureUUID } from "./secureUUID";

describe("createSecureUUID", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("uses getRandomValues when randomUUID is unavailable", () => {
    const crypto = {
      getRandomValues(array: Uint8Array<ArrayBuffer>): Uint8Array<ArrayBuffer> {
        const bytes = new Uint8Array(array.buffer, array.byteOffset, array.byteLength);
        bytes.forEach((_, index) => {
          bytes[index] = index;
        });
        return array;
      },
    };

    expect(createSecureUUID(crypto)).toBe("00010203-0405-4607-8809-0a0b0c0d0e0f");
  });

  it("uses native randomUUID when available", () => {
    expect(
      createSecureUUID({
        randomUUID: () => "native-id",
        getRandomValues: (array) => array,
      }),
    ).toBe("native-id");
  });

  it("falls back to a non-cryptographic id when the source has neither method", () => {
    // The fallback's uniqueness comes from Math.random and Date.now, not
    // cryptography; scripting both makes the "two calls differ" assertion
    // deterministic instead of resting on real randomness never colliding.
    vi.spyOn(Math, "random").mockReturnValueOnce(0.1).mockReturnValueOnce(0.2);
    vi.spyOn(Date, "now").mockReturnValue(1700000000000);

    const first = createSecureUUID({});
    const second = createSecureUUID({});
    expect(first).toMatch(/^insecure-/);
    expect(first).not.toBe(second);
  });

  it("falls back to a non-cryptographic id when randomUUID throws", () => {
    const source = {
      randomUUID: () => {
        throw new Error("denied");
      },
    };
    expect(() => createSecureUUID(source)).not.toThrow();
    expect(createSecureUUID(source)).toMatch(/^insecure-/);
  });

  it("falls back to a non-cryptographic id when getRandomValues throws", () => {
    const source = {
      getRandomValues: () => {
        throw new Error("denied");
      },
    };
    expect(() => createSecureUUID(source)).not.toThrow();
    expect(createSecureUUID(source)).toMatch(/^insecure-/);
  });

  it("treats a non-function truthy randomUUID as absent", () => {
    // @ts-expect-error exercising a malformed source deliberately
    expect(createSecureUUID({ randomUUID: "not-a-function" })).toMatch(/^insecure-/);
  });

  it("treats a non-function truthy getRandomValues as absent", () => {
    // @ts-expect-error exercising a malformed source deliberately
    expect(createSecureUUID({ getRandomValues: "not-a-function" })).toMatch(/^insecure-/);
  });
});
