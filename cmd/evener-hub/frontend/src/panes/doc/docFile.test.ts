// @vitest-environment node

import { DOC_FILE_MAX_BYTES } from "@evener/appwire-client";
import { describe, expect, test } from "vitest";
import { formatDocBytes } from "./docFile";

describe("formatDocBytes", () => {
  test("renders small sizes in bytes", () => {
    expect(formatDocBytes(0)).toBe("0 B");
    expect(formatDocBytes(11)).toBe("11 B");
    expect(formatDocBytes(1023)).toBe("1023 B");
  });

  test("renders kibibytes with integer (floored) division, matching the Go formatDocBytes", () => {
    expect(formatDocBytes(1024)).toBe("1 KiB");
    expect(formatDocBytes(DOC_FILE_MAX_BYTES)).toBe("512 KiB");
    expect(formatDocBytes(1048575)).toBe("1023 KiB");
  });

  test("renders mebibytes at and above 1 MiB", () => {
    expect(formatDocBytes(1048576)).toBe("1 MiB");
    expect(formatDocBytes(3 * 1048576 + 5)).toBe("3 MiB");
  });
});
