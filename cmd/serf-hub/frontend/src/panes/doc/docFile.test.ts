import { describe, expect, test } from "vitest";
import { DOC_FILE_MAX_BYTES } from "../../protocol/docContent";
import { filenameOf, formatDocBytes, isMarkdownPath } from "./docFile";

describe("filenameOf", () => {
  test("returns the last path segment", () => {
    expect(filenameOf("src/panes/doc/DocPane.tsx")).toBe("DocPane.tsx");
  });

  test("returns the whole name for a top-level file", () => {
    expect(filenameOf("README.md")).toBe("README.md");
  });

  test("falls back to the raw path when there is no usable segment", () => {
    expect(filenameOf("")).toBe("");
  });
});

describe("isMarkdownPath", () => {
  test.each(["README.md", "notes.MARKDOWN", "a/b/Guide.Md", "x.markdown"])("treats %s as markdown", (path) => {
    expect(isMarkdownPath(path)).toBe(true);
  });

  test.each(["notes.txt", "script.ts", "a.md.txt", "mdfile", "Makefile"])("does not treat %s as markdown", (path) => {
    expect(isMarkdownPath(path)).toBe(false);
  });
});

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
