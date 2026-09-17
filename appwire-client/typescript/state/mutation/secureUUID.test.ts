import { describe, expect, it } from "vitest";

import { createSecureUUID } from "./secureUUID";

describe("createSecureUUID", () => {
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
});
