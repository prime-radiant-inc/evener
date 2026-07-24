// The path picker's list shape: one flat row array combining Recent projects,
// the current directory's header, a `../` row, and the directory's own
// children. Pure (no React, no wire), so the panel component only maps rows to
// markup.
//
// Why rows and not nested sections: the panel is an ARIA listbox whose options
// must be linearly navigable by ArrowUp/Down. A flat array with a `kind`
// discriminant makes "skip the headers and the status line" a filter
// (pickableRows) instead of a tree walk - the same shape the model picker's
// pickerRows.ts uses.

export type PathRow =
  | { kind: "group"; key: string; label: string }
  | { kind: "parent"; key: string; path: string }
  | { kind: "dir"; key: string; path: string; name: string; current: boolean }
  | { kind: "file"; key: string; path: string; name: string; current: boolean }
  | { kind: "recent"; key: string; path: string; name: string }
  | { kind: "status"; key: string; text: string };

/** The pickable row kinds - the ones that become listbox options. Group
 * headers and the status line are text. */
export type PathPickableRow = Exclude<PathRow, { kind: "group" } | { kind: "status" }>;

const RECENT_GROUP = "Recent projects";

/** An empty prefix lists the home directory (the hub resolves it), so the
 * header names that rather than rendering blank. */
const HOME_LABEL = "Home";

/** The listing's own two states, as a single line each. One string for every
 * kind: with files included the list is no longer directory-specific, so
 * "No subdirectories."/"No directories here." collapse into this. */
const LOADING_TEXT = "Loading…";
const EMPTY_TEXT = "Nothing here.";

/** A completion entry is a directory when it carries a trailing "/" - the one
 * bit `serf/paths/complete` adds in includeFiles mode. Dirs-only responses are
 * unsuffixed, so a `dir` field's caller treats every entry as a directory
 * without consulting this. */
export function isDirEntry(entry: string): boolean {
  return entry.endsWith("/");
}

/** The last path component, ignoring trailing slashes. */
export function basename(path: string): string {
  const trimmed = path.replace(/\/+$/, "");
  const idx = trimmed.lastIndexOf("/");
  return idx === -1 ? trimmed : trimmed.slice(idx + 1);
}

/** One level up, bottoming out at the filesystem root. */
export function parentOf(dir: string): string {
  const trimmed = dir.replace(/\/+$/, "");
  const idx = trimmed.lastIndexOf("/");
  if (idx <= 0) return "/";
  return trimmed.slice(0, idx);
}

/** The prefix that lists a directory's own children (trailing slash), matching
 * completePaths' listDir branch. An empty dir stays empty: that's what the hub
 * resolves to $HOME. */
export function childrenPrefix(dir: string): string {
  if (dir === "") return "";
  return dir.endsWith("/") ? dir : `${dir}/`;
}

/**
 * The full list for one browse position: Recent projects first (only while
 * they're still showing), then the current directory as a header - the
 * "you are here" affordance, which is why a directory row never carries a
 * check - then `../`, then the directory's children with directories before
 * files.
 *
 * Keys are prefixed by section, so a path that appears BOTH under Recent and in
 * the listing gets two distinct DOM ids, and they are numbered WITHIN their
 * section rather than by absolute row position: the panel tracks its
 * highlighted row by key, and a Recent group arriving late must not renumber
 * the listing row the user already highlighted.
 */
export function buildPathRows(input: {
  kind: "dir" | "file" | "outputFile";
  /** The directory being listed. "" means "whatever the hub calls home". */
  currentDir: string;
  /** The completion response, or null while it's still in flight. */
  entries: string[] | null;
  value: string;
  recents: string[];
  showRecents: boolean;
}): PathRow[] {
  const { kind, currentDir, entries, value, recents, showRecents } = input;
  const rows: PathRow[] = [];

  if (showRecents && recents.length > 0) {
    rows.push({ kind: "group", key: "group:recent", label: RECENT_GROUP });
    recents.forEach((path, index) => {
      rows.push({ kind: "recent", key: `recent:${index}:${path}`, path, name: basename(path) });
    });
  }

  rows.push({
    kind: "group",
    key: "group:current",
    label: currentDir === "" ? HOME_LABEL : currentDir,
  });

  // No parent to climb to from the filesystem root, and none from the
  // unresolved empty prefix (the client doesn't know what it resolves to).
  if (currentDir !== "" && currentDir !== "/") {
    rows.push({ kind: "parent", key: "parent", path: parentOf(currentDir) });
  }

  if (entries === null) {
    rows.push({ kind: "status", key: "status", text: LOADING_TEXT });
    return rows;
  }
  if (entries.length === 0) {
    rows.push({ kind: "status", key: "status", text: EMPTY_TEXT });
    return rows;
  }

  // Only a file field marks a current row; a directory's "you are here" is the
  // header above. Matched by basename, since the listing is already scoped to
  // the one directory the value's own file lives in.
  const wantedFile = kind === "dir" || value === "" ? null : basename(value);

  // A `dir` field's response is unsuffixed (includeFiles was false), so every
  // entry is a directory; a file field's directories carry the trailing slash.
  const isDir = (entry: string) => kind === "dir" || isDirEntry(entry);
  entries.filter(isDir).forEach((entry, index) => {
    const path = entry.replace(/\/+$/, "");
    rows.push({ kind: "dir", key: `dir:${index}:${path}`, path, name: basename(path), current: false });
  });
  entries
    .filter((e) => !isDir(e))
    .forEach((entry, index) => {
      rows.push({
        kind: "file",
        key: `file:${index}:${entry}`,
        path: entry,
        name: basename(entry),
        current: wantedFile !== null && basename(entry) === wantedFile,
      });
    });

  return rows;
}

/** The rows the keyboard walks and a click can pick. */
export function pickableRows(rows: PathRow[]): PathPickableRow[] {
  return rows.filter((row): row is PathPickableRow => row.kind !== "group" && row.kind !== "status");
}
