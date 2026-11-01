export type FileReferenceKind = "prose" | "code" | "link";

export interface FileReference {
  /** The path inside `cwd`, which for a session file is the session's working
   * directory and for a retained artifact is that session's artifacts
   * directory (see `root`). */
  readonly path: string;
  /** The directory `path` is relative to and `readTarget` must stay inside. */
  readonly cwd: string;
  readonly readTarget: string;
  readonly provenance: "relative" | "absolute";
  /** Which of the session's two served roots `cwd` names. Absent means the
   * session's working directory, the only root this type knew before
   * artifacts were linkable. A retained-artifact reference names the
   * artifacts directory instead, so a caller holding both roots can tell
   * which one to rebind against. "scratch" names the session's scratch
   * directory the same way. */
  readonly root?: "artifacts" | "scratch";
}

export interface FileReferenceSpan {
  readonly start: number;
  readonly end: number;
  readonly reference: FileReference;
}

/** ServedRoots names the session's retained roots the hub's document routes
 * serve beside the working directory: the artifacts and scratch directories a
 * thread snapshot advertises. An absolute path the working directory refuses
 * may still bind against one of these, exactly as a file URL does through
 * bindServedPath; an undefined property or a missing object names no root. */
export interface ServedRoots {
  readonly artifactsDir?: string;
  readonly scratchDir?: string;
}

const WHITESPACE = /\s/u;
const SCHEME = /^[a-z][a-z\d+.-]*:/iu;
const DOMAIN_SEGMENT = /^(?:[a-z\d-]+\.)+[a-z]{2,}$/iu;
const VERSION_SEGMENT = /^v?\d+(?:\.\d+)+$/iu;
const PROSE_TRAILING_PUNCTUATION = /[.,;:!?…]+$/u;
const LOCATION_SUFFIX = /:\d+(?::\d+)?$/u;
const SURROUNDING_OPEN = /^[([{<]+/u;
const SURROUNDING_CLOSE_OR_PUNCTUATION = /[)\]}>.,;:!?…]+$/u;
const AMBIGUOUS_BRACKET = /[()[\]{}<>]/u;
const SURROUNDING_PAIRS = new Map([
  ["(", ")"],
  ["[", "]"],
  ["{", "}"],
  ["<", ">"],
]);
const SURROUNDING_CLOSERS = new Set(SURROUNDING_PAIRS.values());

interface NormalizedPath {
  readonly absolute: boolean;
  readonly value: string;
}

function hasControlCharacter(value: string): boolean {
  for (const character of value) {
    const codePoint = character.codePointAt(0);
    if (codePoint !== undefined && (codePoint <= 0x1f || (codePoint >= 0x7f && codePoint <= 0x9f))) return true;
  }
  return false;
}

function normalizedPath(path: string): NormalizedPath | undefined {
  if (path === "" || path.endsWith("/") || path.startsWith("//") || path.includes("\\") || hasControlCharacter(path)) {
    return undefined;
  }

  const segments = path.split("/");
  if (segments.includes("..") || segments.at(-1) === ".") return undefined;

  const absolute = path.startsWith("/");
  const normalizedSegments = segments.filter((segment) => segment !== "" && segment !== ".");
  if (normalizedSegments.length === 0) return undefined;

  return {
    absolute,
    value: `${absolute ? "/" : ""}${normalizedSegments.join("/")}`,
  };
}

function cwdRoot(cwd: string): string {
  const withoutTrailingSeparators = cwd.replace(/\/+$/, "");
  return withoutTrailingSeparators === "" ? "/" : withoutTrailingSeparators;
}

function joinCwd(cwd: string, path: string): string {
  const root = cwdRoot(cwd);
  return root === "/" ? `/${path}` : `${root}/${path}`;
}

export function bindFilePath(path: string, cwd: string): FileReference | undefined {
  if (cwd === "") return undefined;

  const normalized = normalizedPath(path);
  if (!normalized) return undefined;

  if (!normalized.absolute) {
    return {
      path: normalized.value,
      cwd,
      readTarget: joinCwd(cwd, normalized.value),
      provenance: "relative",
    };
  }

  const root = cwdRoot(cwd);
  const relativePath = root === "/" ? normalized.value.slice(1) : normalized.value.slice(root.length + 1);
  if (relativePath === "" || (root !== "/" && !normalized.value.startsWith(`${root}/`))) return undefined;

  return {
    path: relativePath,
    cwd,
    readTarget: normalized.value,
    provenance: "absolute",
  };
}

/** bindRetainedPath binds a path to one of a session's retained root
 * directories, exactly as bindFilePath does for the working directory, and
 * marks the reference so a consumer holding the roots rebinds against the
 * right one. A relative path never reaches a second root. */
function bindRetainedPath(path: string, dir: string, root: "artifacts" | "scratch"): FileReference | undefined {
  if (dir === "") return undefined;
  const bound = bindFilePath(path, dir);
  return bound === undefined ? undefined : { ...bound, root };
}

/** bindArtifactPath binds a path to a session's retained artifacts directory,
 * the second root the hub's document routes serve. */
export function bindArtifactPath(path: string, artifactsDir: string): FileReference | undefined {
  return bindRetainedPath(path, artifactsDir, "artifacts");
}

/** bindScratchPath binds a path to a session's named scratch directory, the
 * third root the hub's document routes serve. */
export function bindScratchPath(path: string, scratchDir: string): FileReference | undefined {
  return bindRetainedPath(path, scratchDir, "scratch");
}

/** bindServedPath binds a destination to whichever of the session's served
 * roots contains it: the working directory first, then the retained artifacts
 * directory, then the scratch directory, each when the snapshot advertises
 * it. A destination outside every root names no document the hub can serve. */
export function bindServedPath(path: string, cwd: string, served?: ServedRoots): FileReference | undefined {
  return (
    bindFilePath(path, cwd) ??
    bindArtifactPath(path, served?.artifactsDir ?? "") ??
    bindScratchPath(path, served?.scratchDir ?? "")
  );
}

interface LiteralSurface {
  readonly path: string;
  readonly end: number;
}

function stripLocation(value: string): LiteralSurface {
  const match = LOCATION_SUFFIX.exec(value);
  if (!match) return { path: value, end: value.length };
  return { path: value.slice(0, match.index), end: match.index };
}

function proseSurface(value: string): LiteralSurface {
  const withoutPunctuation = value.replace(PROSE_TRAILING_PUNCTUATION, "");
  return stripLocation(withoutPunctuation);
}

function hasFilenameExtension(path: string): boolean {
  const filename = path.split("/").at(-1) ?? "";
  return /^.+\.[^./]+$/u.test(filename);
}

function hasAmbiguousLiteralPrefix(value: string): boolean {
  if (SCHEME.test(value) || value.includes("@")) return true;
  if (!value.includes("/")) return false;
  const firstSegment = value.replace(/^\.\//u, "").split("/")[0] ?? "";
  return DOMAIN_SEGMENT.test(firstSegment) || VERSION_SEGMENT.test(firstSegment);
}

function parseLiteral(
  value: string,
  kind: "prose" | "code",
  cwd: string,
  served?: ServedRoots,
): FileReference | undefined {
  const surface = kind === "prose" ? proseSurface(value) : stripLocation(value);
  if (
    surface.path === "" ||
    WHITESPACE.test(value) ||
    hasAmbiguousLiteralPrefix(surface.path) ||
    AMBIGUOUS_BRACKET.test(surface.path)
  ) {
    return undefined;
  }

  // bindFilePath returns any relative path bound to the working directory, so
  // only an absolute path the working directory refuses reaches a served root
  // (a screenshot in scratch, a report in artifacts): a bare name can never
  // reach a second root.
  const reference = bindServedPath(surface.path, cwd, served);
  if (!reference) return undefined;

  if (kind === "prose") {
    return value.includes("/") && hasFilenameExtension(reference.path) ? reference : undefined;
  }
  return value.includes("/") || hasFilenameExtension(reference.path) ? reference : undefined;
}

function parseLink(value: string, cwd: string, served?: ServedRoots): FileReference | undefined {
  if (
    value === "" ||
    WHITESPACE.test(value) ||
    value.startsWith("//") ||
    value.startsWith("?") ||
    value.startsWith("#") ||
    SCHEME.test(value)
  ) {
    return undefined;
  }

  const metadataStart = value.search(/[?#]/u);
  const rawPathname = metadataStart === -1 ? value : value.slice(0, metadataStart);
  const surface = stripLocation(rawPathname);
  if (surface.path === "" || /%(?:2f|5c)/iu.test(surface.path)) return undefined;

  try {
    // Same rule as a literal: only an absolute destination the working
    // directory refuses can reach a served root.
    return bindServedPath(decodeURIComponent(surface.path), cwd, served);
  } catch {
    return undefined;
  }
}

export function parseFileReference(
  value: string,
  kind: FileReferenceKind,
  cwd: string,
  served?: ServedRoots,
): FileReference | undefined {
  if (cwd === "") return undefined;
  return kind === "link" ? parseLink(value, cwd, served) : parseLiteral(value, kind, cwd, served);
}

function isTokenBoundary(character: string): boolean {
  return WHITESPACE.test(character) || /["'“”‘’]/u.test(character);
}

interface ProseCandidate {
  readonly start: number;
  readonly end: number;
}

function bracketedCandidates(token: string): ProseCandidate[] | undefined {
  const candidates: ProseCandidate[] = [];
  let cursor = 0;
  while (cursor < token.length) {
    const expectedClosers: string[] = [];
    while (cursor < token.length) {
      const closer = SURROUNDING_PAIRS.get(token[cursor] ?? "");
      if (!closer) break;
      expectedClosers.push(closer);
      cursor += 1;
    }
    if (expectedClosers.length === 0) return undefined;

    const candidateStart = cursor;
    const surroundingDepth = expectedClosers.length;
    let candidateEnd: number | undefined;
    while (cursor < token.length && expectedClosers.length > 0) {
      const character = token[cursor] ?? "";
      const nestedCloser = SURROUNDING_PAIRS.get(character);
      if (nestedCloser) {
        expectedClosers.push(nestedCloser);
      } else if (character === expectedClosers.at(-1)) {
        if (expectedClosers.length === surroundingDepth && candidateEnd === undefined) candidateEnd = cursor;
        expectedClosers.pop();
      } else if (SURROUNDING_CLOSERS.has(character)) {
        return undefined;
      }
      cursor += 1;
    }
    if (expectedClosers.length > 0 || candidateEnd === undefined) return undefined;
    candidates.push({ start: candidateStart, end: candidateEnd });

    while (cursor < token.length && PROSE_TRAILING_PUNCTUATION.test(token[cursor] ?? "")) cursor += 1;
  }
  return candidates;
}

export function findFileReferences(text: string, cwd: string, served?: ServedRoots): FileReferenceSpan[] {
  if (cwd === "") return [];

  const spans: FileReferenceSpan[] = [];
  let cursor = 0;
  while (cursor < text.length) {
    while (cursor < text.length && isTokenBoundary(text[cursor] ?? "")) cursor += 1;
    const tokenStart = cursor;
    while (cursor < text.length && !isTokenBoundary(text[cursor] ?? "")) cursor += 1;
    if (tokenStart === cursor) continue;

    const token = text.slice(tokenStart, cursor);
    const surrounded = bracketedCandidates(token);
    if (surrounded) {
      for (const bounds of surrounded) {
        const candidate = token.slice(bounds.start, bounds.end);
        const reference = parseFileReference(candidate, "prose", cwd, served);
        if (!reference) continue;
        const surface = proseSurface(candidate);
        spans.push({
          start: tokenStart + bounds.start,
          end: tokenStart + bounds.start + surface.end,
          reference,
        });
      }
      continue;
    }

    const opening = SURROUNDING_OPEN.exec(token)?.[0].length ?? 0;
    const withoutOpening = token.slice(opening);
    const closing = SURROUNDING_CLOSE_OR_PUNCTUATION.exec(withoutOpening)?.[0].length ?? 0;
    const candidate = withoutOpening.slice(0, withoutOpening.length - closing);
    const reference = parseFileReference(candidate, "prose", cwd, served);
    if (!reference) continue;

    const surface = proseSurface(candidate);
    spans.push({
      start: tokenStart + opening,
      end: tokenStart + opening + surface.end,
      reference,
    });
  }
  return spans;
}

export function rebindFileReference(reference: FileReference, cwd: string): FileReference {
  // A retained-root reference is not bound to the working directory, so a
  // changed cwd leaves it alone: rebinding it would re-resolve a retained
  // path against a directory that never contained it.
  if (reference.root !== undefined) return reference;
  return {
    ...reference,
    cwd,
    readTarget: reference.provenance === "relative" ? joinCwd(cwd, reference.path) : reference.readTarget,
  };
}
