import { describe, expect, test } from "vitest";
import { basename, buildPathRows, childrenPrefix, isDirEntry, parentOf, pickableRows } from "./pathRows";

// Compact row shape for order assertions: kind plus whatever that kind's
// identifying text is.
function shape(rows: ReturnType<typeof buildPathRows>): string[] {
  return rows.map((row) => {
    switch (row.kind) {
      case "group":
        return `group:${row.label}`;
      case "parent":
        return `parent:${row.path}`;
      case "status":
        return `status:${row.text}`;
      default:
        return `${row.kind}:${row.path}`;
    }
  });
}

describe("isDirEntry", () => {
  test("a trailing slash is what marks a completion entry a directory", () => {
    expect(isDirEntry("/etc/ssl/")).toBe(true);
    expect(isDirEntry("/etc/hosts")).toBe(false);
  });
});

describe("buildPathRows", () => {
  test("a dir field lists the current directory's header, a parent row, and its children", () => {
    const rows = buildPathRows({
      kind: "dir",
      currentDir: "/home/jesse",
      entries: ["/home/jesse/src", "/home/jesse/tmp"],
      value: "/home/jesse",
      recents: [],
      showRecents: false,
    });

    expect(shape(rows)).toEqual(["group:/home/jesse", "parent:/home", "dir:/home/jesse/src", "dir:/home/jesse/tmp"]);
  });

  test("a directory row carries its basename and is never marked current - the group header is the you-are-here", () => {
    const rows = buildPathRows({
      kind: "dir",
      currentDir: "/home",
      entries: ["/home/jesse"],
      value: "/home/jesse",
      recents: [],
      showRecents: false,
    });
    const row = rows.find((r) => r.kind === "dir");
    if (row?.kind !== "dir") throw new Error("expected a dir row");

    expect(row.name).toBe("jesse");
    expect(row.current).toBe(false);
  });

  // The hub resolves an empty prefix to $HOME, so the header can't print the
  // literal "" - and printing "/" (PathPicker's old guess) would name a
  // directory that isn't the one being listed.
  test("the empty current directory renders as Home rather than a blank header", () => {
    const rows = buildPathRows({
      kind: "dir",
      currentDir: "",
      entries: ["/home/jesse/src"],
      value: "",
      recents: [],
      showRecents: false,
    });

    expect(shape(rows)).toEqual(["group:Home", "dir:/home/jesse/src"]);
  });

  test("the parent row is suppressed at the filesystem root and for the unresolved empty dir", () => {
    for (const currentDir of ["", "/"]) {
      const rows = buildPathRows({
        kind: "dir",
        currentDir,
        entries: ["/etc"],
        value: "",
        recents: [],
        showRecents: false,
      });
      expect(rows.some((r) => r.kind === "parent")).toBe(false);
    }
  });

  test("a file field classifies trailing-slash entries as directories and bare ones as files, dirs first", () => {
    const rows = buildPathRows({
      kind: "file",
      currentDir: "/etc",
      entries: ["/etc/hosts", "/etc/ssl/", "/etc/passwd"],
      value: "/etc/hosts",
      recents: [],
      showRecents: false,
    });

    expect(shape(rows)).toEqual(["group:/etc", "parent:/", "dir:/etc/ssl", "file:/etc/hosts", "file:/etc/passwd"]);
  });

  test("a directory row's path drops the trailing slash, so it feeds straight back as the next value", () => {
    const rows = buildPathRows({
      kind: "file",
      currentDir: "/etc",
      entries: ["/etc/ssl/"],
      value: "",
      recents: [],
      showRecents: false,
    });
    const row = rows.find((r) => r.kind === "dir");
    if (row?.kind !== "dir") throw new Error("expected a dir row");

    expect(row.path).toBe("/etc/ssl");
    expect(row.name).toBe("ssl");
  });

  // The hub only suffixes directories when includeFiles was true, so a
  // dirs-only response is entirely unsuffixed - every entry is a directory.
  test("a dir field yields NO file rows: an unsuffixed dirs-only response is all directories", () => {
    const rows = buildPathRows({
      kind: "dir",
      currentDir: "/home",
      entries: ["/home/jesse", "/home/other"],
      value: "",
      recents: [],
      showRecents: false,
    });

    expect(rows.some((r) => r.kind === "file")).toBe(false);
    expect(rows.filter((r) => r.kind === "dir")).toHaveLength(2);
  });

  test("the check lands on the file row whose basename matches the value's, for both file kinds", () => {
    for (const kind of ["file", "outputFile"] as const) {
      const rows = buildPathRows({
        kind,
        currentDir: "/etc",
        entries: ["/etc/hosts", "/etc/passwd"],
        value: "/etc/passwd",
        recents: [],
        showRecents: false,
      });
      const current = rows.filter((r) => (r.kind === "file" || r.kind === "dir") && r.current);
      expect(current).toHaveLength(1);
      expect(current[0]?.kind === "file" && current[0].path).toBe("/etc/passwd");
    }
  });

  test("no row is current when nothing listed shares the value's basename", () => {
    const rows = buildPathRows({
      kind: "file",
      currentDir: "/etc",
      entries: ["/etc/hosts"],
      value: "/var/log/system.log",
      recents: [],
      showRecents: false,
    });

    expect(rows.some((r) => (r.kind === "file" || r.kind === "dir") && r.current)).toBe(false);
  });

  test("recents lead the list with basename and full path, only while showRecents", () => {
    const input = {
      kind: "dir" as const,
      currentDir: "/home/jesse",
      entries: ["/home/jesse/src"],
      value: "",
      recents: ["/home/jesse/serf", "/home/jesse/toil"],
      showRecents: true,
    };
    const rows = buildPathRows(input);

    expect(shape(rows)).toEqual([
      "group:Recent projects",
      "recent:/home/jesse/serf",
      "recent:/home/jesse/toil",
      "group:/home/jesse",
      "parent:/home",
      "dir:/home/jesse/src",
    ]);
    const recent = rows.find((r) => r.kind === "recent");
    if (recent?.kind !== "recent") throw new Error("expected a recent row");
    expect(recent.name).toBe("serf");

    expect(buildPathRows({ ...input, showRecents: false }).some((r) => r.kind === "recent")).toBe(false);
    expect(buildPathRows({ ...input, recents: [] }).some((r) => r.kind === "recent")).toBe(false);
  });

  test("a null listing is one Loading row; an empty listing is one Nothing-here row", () => {
    const base = {
      kind: "dir" as const,
      currentDir: "/home/jesse",
      value: "",
      recents: [],
      showRecents: false,
    };

    expect(shape(buildPathRows({ ...base, entries: null }))).toEqual([
      "group:/home/jesse",
      "parent:/home",
      "status:Loading…",
    ]);
    expect(shape(buildPathRows({ ...base, entries: [] }))).toEqual([
      "group:/home/jesse",
      "parent:/home",
      "status:Nothing here.",
    ]);
  });

  test("keys stay unique when a path appears in both the recents group and the listing", () => {
    const rows = buildPathRows({
      kind: "dir",
      currentDir: "/home/jesse",
      entries: ["/home/jesse/serf"],
      value: "",
      recents: ["/home/jesse/serf"],
      showRecents: true,
    });
    const keys = rows.map((r) => r.key);

    expect(new Set(keys).size).toBe(keys.length);
  });
});

describe("pickableRows", () => {
  test("keeps recents, the parent, dirs, and files - group headers and status lines are text", () => {
    const rows = buildPathRows({
      kind: "file",
      currentDir: "/etc",
      entries: ["/etc/ssl/", "/etc/hosts"],
      value: "",
      recents: ["/home/jesse/serf"],
      showRecents: true,
    });

    expect(pickableRows(rows).map((r) => r.path)).toEqual(["/home/jesse/serf", "/", "/etc/ssl", "/etc/hosts"]);
    expect(
      pickableRows(
        buildPathRows({ kind: "dir", currentDir: "/", entries: null, value: "", recents: [], showRecents: false }),
      ),
    ).toEqual([]);
  });
});

describe("path helpers", () => {
  test("basename drops trailing slashes before taking the last component", () => {
    expect(basename("/home/jesse/src/")).toBe("src");
    expect(basename("/home/jesse/src")).toBe("src");
    expect(basename("src")).toBe("src");
  });

  test("parentOf climbs one level and bottoms out at the root", () => {
    expect(parentOf("/home/jesse")).toBe("/home");
    expect(parentOf("/home/jesse/")).toBe("/home");
    expect(parentOf("/home")).toBe("/");
    expect(parentOf("/")).toBe("/");
  });

  test("childrenPrefix is the trailing-slash form that lists a directory's own children", () => {
    expect(childrenPrefix("/home/jesse")).toBe("/home/jesse/");
    expect(childrenPrefix("/home/jesse/")).toBe("/home/jesse/");
    // The hub resolves an empty prefix to $HOME, so it must stay empty.
    expect(childrenPrefix("")).toBe("");
  });
});
