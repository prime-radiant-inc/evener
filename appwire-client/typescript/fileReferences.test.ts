// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  bindFilePath as exportedBindFilePath,
  findFileReferences as exportedFindFileReferences,
  parseFileReference as exportedParseFileReference,
  rebindFileReference as exportedRebindFileReference,
} from "./docContent";
import {
  bindFilePath,
  type FileReference,
  findFileReferences,
  parseFileReference,
  rebindFileReference,
} from "./fileReferences";

const cwd = "/work/a";

describe("bindFilePath", () => {
  const accepted: ReadonlyArray<readonly [string, string, FileReference]> = [
    [
      "binds a raw relative path containing spaces",
      "docs/raw file.md",
      { path: "docs/raw file.md", cwd: "/work/a", readTarget: "/work/a/docs/raw file.md", provenance: "relative" },
    ],
    [
      "keeps percent, hash, and question-mark data literal",
      "docs/100%25#draft?.md",
      {
        path: "docs/100%25#draft?.md",
        cwd: "/work/a",
        readTarget: "/work/a/docs/100%25#draft?.md",
        provenance: "relative",
      },
    ],
    [
      "normalizes dot and repeated interior separators",
      "./docs//./plan.md",
      { path: "docs/plan.md", cwd: "/work/a", readTarget: "/work/a/docs/plan.md", provenance: "relative" },
    ],
    [
      "accepts an extensionless structured target",
      "LICENSE",
      { path: "LICENSE", cwd: "/work/a", readTarget: "/work/a/LICENSE", provenance: "relative" },
    ],
    [
      "derives display identity from a contained absolute target",
      "/work/a/docs//plan.md",
      { path: "docs/plan.md", cwd: "/work/a", readTarget: "/work/a/docs/plan.md", provenance: "absolute" },
    ],
  ];

  it.each(accepted)("%s", (_name, value, expected) => {
    expect(bindFilePath(value, cwd)).toEqual(expected);
  });

  it("handles the filesystem root without adding a second slash", () => {
    expect(bindFilePath("guide/readme.md", "/")).toEqual({
      path: "guide/readme.md",
      cwd: "/",
      readTarget: "/guide/readme.md",
      provenance: "relative",
    });
    expect(bindFilePath("/guide/readme.md", "/")).toEqual({
      path: "guide/readme.md",
      cwd: "/",
      readTarget: "/guide/readme.md",
      provenance: "absolute",
    });
  });

  it.each([
    ["empty path", "", cwd],
    ["missing cwd", "docs/plan.md", ""],
    ["parent directory", "..", cwd],
    ["parent traversal", "../docs/plan.md", cwd],
    ["interior parent traversal", "docs/old/../plan.md", cwd],
    ["parent traversal before alias normalization", "docs/./../plan.md", cwd],
    ["current directory", ".", cwd],
    ["syntactic directory", "docs/", cwd],
    ["repeated trailing separators", "docs///", cwd],
    ["protocol-relative path", "//host/docs/plan.md", cwd],
    ["backslash", "docs\\plan.md", cwd],
    ["NUL control", "docs/plan\u0000.md", cwd],
    ["newline control", "docs/plan\n.md", cwd],
    ["absolute target outside cwd", "/other/docs/plan.md", cwd],
    ["absolute sibling-prefix target", "/work/a-other/docs/plan.md", cwd],
    ["cwd itself", "/work/a", cwd],
    ["cwd itself with trailing separator", "/work/a/", cwd],
  ])("rejects %s", (_name, value, binding) => {
    expect(bindFilePath(value, binding)).toBeUndefined();
  });
});

describe("parseFileReference", () => {
  const accepted: ReadonlyArray<readonly [string, "prose" | "code" | "link", FileReference]> = [
    [
      "docs/plan.md",
      "prose",
      { path: "docs/plan.md", cwd: "/work/a", readTarget: "/work/a/docs/plan.md", provenance: "relative" },
    ],
    [
      "./src/main.go",
      "prose",
      { path: "src/main.go", cwd: "/work/a", readTarget: "/work/a/src/main.go", provenance: "relative" },
    ],
    [
      "/work/a/docs/plan.md",
      "prose",
      { path: "docs/plan.md", cwd: "/work/a", readTarget: "/work/a/docs/plan.md", provenance: "absolute" },
    ],
    [
      "docs//./設計.md:12:4.",
      "prose",
      { path: "docs/設計.md", cwd: "/work/a", readTarget: "/work/a/docs/設計.md", provenance: "relative" },
    ],
    [
      "docs/100%25#draft?.md",
      "prose",
      {
        path: "docs/100%25#draft?.md",
        cwd: "/work/a",
        readTarget: "/work/a/docs/100%25#draft?.md",
        provenance: "relative",
      },
    ],
    [
      "README.md:12",
      "code",
      { path: "README.md", cwd: "/work/a", readTarget: "/work/a/README.md", provenance: "relative" },
    ],
    [
      "src/Makefile",
      "code",
      { path: "src/Makefile", cwd: "/work/a", readTarget: "/work/a/src/Makefile", provenance: "relative" },
    ],
    [
      "./scripts/release:9:2",
      "code",
      { path: "scripts/release", cwd: "/work/a", readTarget: "/work/a/scripts/release", provenance: "relative" },
    ],
    [
      "docs/name#part?.md",
      "code",
      {
        path: "docs/name#part?.md",
        cwd: "/work/a",
        readTarget: "/work/a/docs/name#part?.md",
        provenance: "relative",
      },
    ],
    [
      "README.md",
      "link",
      { path: "README.md", cwd: "/work/a", readTarget: "/work/a/README.md", provenance: "relative" },
    ],
    ["LICENSE", "link", { path: "LICENSE", cwd: "/work/a", readTarget: "/work/a/LICENSE", provenance: "relative" }],
    [
      "./docs/release%20notes",
      "link",
      {
        path: "docs/release notes",
        cwd: "/work/a",
        readTarget: "/work/a/docs/release notes",
        provenance: "relative",
      },
    ],
    [
      "./docs/foo(bar)/plan.md",
      "link",
      {
        path: "docs/foo(bar)/plan.md",
        cwd: "/work/a",
        readTarget: "/work/a/docs/foo(bar)/plan.md",
        provenance: "relative",
      },
    ],
    [
      "/work/a/docs/plan.md:12?download=1#L4",
      "link",
      { path: "docs/plan.md", cwd: "/work/a", readTarget: "/work/a/docs/plan.md", provenance: "absolute" },
    ],
  ];

  it.each(accepted)("parses %s on the %s surface", (value, kind, expected) => {
    expect(parseFileReference(value, kind, cwd)).toEqual(expected);
  });

  it("keeps display identity separate from captured read authority", () => {
    const relative = parseFileReference("./docs//plan.md:12:4", "code", "/work/a");
    expect(relative).toEqual({
      path: "docs/plan.md",
      cwd: "/work/a",
      readTarget: "/work/a/docs/plan.md",
      provenance: "relative",
    });
    const absolute = parseFileReference("/work/a/docs/plan.md", "code", "/work/a");
    expect(absolute).toEqual({
      path: "docs/plan.md",
      cwd: "/work/a",
      readTarget: "/work/a/docs/plan.md",
      provenance: "absolute",
    });
    if (!relative || !absolute) throw new Error("expected accepted references");
    expect(rebindFileReference(relative, "/work/b")).toEqual({
      path: "docs/plan.md",
      cwd: "/work/b",
      readTarget: "/work/b/docs/plan.md",
      provenance: "relative",
    });
    expect(rebindFileReference(absolute, "/work/b")).toEqual({
      path: "docs/plan.md",
      cwd: "/work/b",
      readTarget: "/work/a/docs/plan.md",
      provenance: "absolute",
    });
  });

  it("decodes link pathnames once, after removing raw metadata", () => {
    expect(parseFileReference("./docs/a%3A12%23L4%3F.md#L9", "link", cwd)?.path).toBe("docs/a:12#L4?.md");
    expect(parseFileReference("./docs/100%2525.md", "link", cwd)?.path).toBe("docs/100%25.md");
    expect(parseFileReference("docs/100%25.md", "code", cwd)?.path).toBe("docs/100%25.md");
    expect(parseFileReference("README.md:12", "link", cwd)).toBeUndefined();
    expect(parseFileReference("./README.md:12", "link", cwd)?.path).toBe("README.md");
    expect(parseFileReference("./README.md%3A12", "link", cwd)?.path).toBe("README.md:12");
    expect(parseFileReference("./docs/a.md:12:4?raw=true#L9", "link", cwd)?.path).toBe("docs/a.md");
  });

  it("applies the same URI contract to explicit and resolved reference-link destinations", () => {
    expect(parseFileReference("./docs/explicit%20link.md", "link", cwd)).toEqual({
      path: "docs/explicit link.md",
      cwd: "/work/a",
      readTarget: "/work/a/docs/explicit link.md",
      provenance: "relative",
    });
    expect(parseFileReference("./docs/reference%20link.md", "link", cwd)).toEqual({
      path: "docs/reference link.md",
      cwd: "/work/a",
      readTarget: "/work/a/docs/reference link.md",
      provenance: "relative",
    });
  });

  it.each([
    ["README.md", "prose"],
    ["Makefile", "prose"],
    ["docs/guide", "prose"],
    ["docs/", "prose"],
    ["docs/foo(bar)/plan.md", "prose"],
    ["https://host/docs/plan.md", "prose"],
    ["mailto:a/docs/plan.md", "prose"],
    ["a@example.test/docs/plan.md", "prose"],
    ["example.test/docs/plan.md", "prose"],
    ["v1.2/docs/plan.md", "prose"],
    ["../docs/plan.md", "prose"],
    ["/work/a-other/docs/plan.md", "prose"],
    ["README", "code"],
    ["Makefile", "code"],
    ["npm run build", "code"],
    ["go test ./...", "code"],
    ["src/", "code"],
    ["https://host/docs/plan.md", "code"],
    ["README.md:12", "link"],
    ["https://host/docs/plan.md", "link"],
    ["HTTPS://host/docs/plan.md", "link"],
    ["mailto:a@host.test", "link"],
    ["custom+file:docs/plan.md", "link"],
    ["//host/docs/plan.md", "link"],
    ["?download=1", "link"],
    ["#L12", "link"],
    ["./docs/bad%2", "link"],
    ["./docs/bad%zz.md", "link"],
    ["./docs/%2e%2e/secret.md", "link"],
    ["./docs%2Fsecret.md", "link"],
    ["./docs%5Csecret.md", "link"],
    ["%2F%2Fhost/docs/plan.md", "link"],
    ["./docs/control%00.md", "link"],
    ["./docs/control%0A.md", "link"],
    ["./docs/raw space.md", "link"],
    ["./docs/", "link"],
  ] as const)("rejects %s on the %s surface", (value, kind) => {
    expect(parseFileReference(value, kind, cwd)).toBeUndefined();
  });

  it.each(["prose", "code", "link"] as const)("rejects a missing cwd on the %s surface", (kind) => {
    expect(parseFileReference("docs/plan.md", kind, "")).toBeUndefined();
  });
});

describe("findFileReferences", () => {
  it("recognizes Jesse's exact two plain-text examples with original offsets", () => {
    const text =
      "Spec: docs/superpowers/specs/2026-10-02-web-session-overview-design.md\n" +
      "Review: docs/superpowers/specs/2026-10-02-web-session-overview-review.md";
    expect(findFileReferences(text, cwd)).toEqual([
      {
        start: 6,
        end: 70,
        reference: {
          path: "docs/superpowers/specs/2026-10-02-web-session-overview-design.md",
          cwd: "/work/a",
          readTarget: "/work/a/docs/superpowers/specs/2026-10-02-web-session-overview-design.md",
          provenance: "relative",
        },
      },
      {
        start: 79,
        end: 143,
        reference: {
          path: "docs/superpowers/specs/2026-10-02-web-session-overview-review.md",
          cwd: "/work/a",
          readTarget: "/work/a/docs/superpowers/specs/2026-10-02-web-session-overview-review.md",
          provenance: "relative",
        },
      },
    ]);
  });

  it("finds multiple delimited references and excludes punctuation and locations from spans", () => {
    const text = "See (docs/a.md), ‘src/設計.ts’; and docs/b.go:12:4!";
    expect(findFileReferences(text, cwd)).toEqual([
      {
        start: 5,
        end: 14,
        reference: { path: "docs/a.md", cwd: "/work/a", readTarget: "/work/a/docs/a.md", provenance: "relative" },
      },
      {
        start: 18,
        end: 27,
        reference: { path: "src/設計.ts", cwd: "/work/a", readTarget: "/work/a/src/設計.ts", provenance: "relative" },
      },
      {
        start: 34,
        end: 43,
        reference: { path: "docs/b.go", cwd: "/work/a", readTarget: "/work/a/docs/b.go", provenance: "relative" },
      },
    ]);
  });

  it("never recognizes a shorter suffix of an invalid token", () => {
    for (const text of [
      "../docs/plan.md",
      "prefix:docs/plan.md",
      "https://host/docs/plan.md",
      "mailto:a/docs/plan.md",
      "a@example.test/docs/plan.md",
      "example.test/docs/plan.md",
      "v1.2/docs/plan.md",
      "docs/foo(bar)/plan.md",
      "//host/docs/plan.md",
      "../**docs/plan.md**",
      "https://host/**docs/plan.md**",
    ]) {
      expect(findFileReferences(text, cwd)).toEqual([]);
    }
  });

  it.each([
    ["period", "."],
    ["comma", ","],
    ["semicolon", ";"],
    ["colon", ":"],
    ["exclamation", "!"],
    ["question mark", "?"],
    ["Unicode ellipsis", "…"],
  ])("excludes a terminal %s from the original prose span", (_name, punctuation) => {
    expect(findFileReferences(`docs/a.md${punctuation}`, cwd)).toEqual([
      {
        start: 0,
        end: 9,
        reference: { path: "docs/a.md", cwd: "/work/a", readTarget: "/work/a/docs/a.md", provenance: "relative" },
      },
    ]);
  });

  it("keeps Unicode data while excluding curly quotes, a numeric location, sentence punctuation, and ellipses", () => {
    expect(findFileReferences("“docs/設計.md:12.” docs/a.md…", cwd)).toEqual([
      {
        start: 1,
        end: 11,
        reference: {
          path: "docs/設計.md",
          cwd: "/work/a",
          readTarget: "/work/a/docs/設計.md",
          provenance: "relative",
        },
      },
      {
        start: 17,
        end: 26,
        reference: { path: "docs/a.md", cwd: "/work/a", readTarget: "/work/a/docs/a.md", provenance: "relative" },
      },
    ]);
  });

  it("recognizes lexical aliases and contained absolute paths without confusing sibling prefixes", () => {
    expect(
      findFileReferences("docs//./a.md /work/a/src/../bad.ts /work/a/src/good.ts /work/a-other/src/no.ts", cwd),
    ).toEqual([
      {
        start: 0,
        end: 12,
        reference: { path: "docs/a.md", cwd: "/work/a", readTarget: "/work/a/docs/a.md", provenance: "relative" },
      },
      {
        start: 35,
        end: 54,
        reference: {
          path: "src/good.ts",
          cwd: "/work/a",
          readTarget: "/work/a/src/good.ts",
          provenance: "absolute",
        },
      },
    ]);
  });

  it("leaves directory forms, extensionless targets, dotted words, commands, and missing bindings untouched", () => {
    expect(findFileReferences("README.md docs/ src/Makefile docs/guide example.test 1.2.3 go test ./pkg", cwd)).toEqual(
      [],
    );
    expect(findFileReferences("docs/plan.md", "")).toEqual([]);
  });
});

describe("docContent exports", () => {
  it("publishes the file-reference contract through the existing docContent subpath", () => {
    expect(exportedBindFilePath).toBe(bindFilePath);
    expect(exportedParseFileReference).toBe(parseFileReference);
    expect(exportedFindFileReferences).toBe(findFileReferences);
    expect(exportedRebindFileReference).toBe(rebindFileReference);
  });
});
