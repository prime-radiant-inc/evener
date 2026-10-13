export type FileReferenceKind = "prose" | "code" | "link";

export interface FileReference {
  readonly path: string;
  readonly cwd: string;
  readonly readTarget: string;
  readonly provenance: "relative" | "absolute";
}

export interface FileReferenceSpan {
  readonly start: number;
  readonly end: number;
  readonly reference: FileReference;
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

function parseLiteral(value: string, kind: "prose" | "code", cwd: string): FileReference | undefined {
  const surface = kind === "prose" ? proseSurface(value) : stripLocation(value);
  if (
    surface.path === "" ||
    WHITESPACE.test(value) ||
    hasAmbiguousLiteralPrefix(surface.path) ||
    AMBIGUOUS_BRACKET.test(surface.path)
  ) {
    return undefined;
  }

  const reference = bindFilePath(surface.path, cwd);
  if (!reference) return undefined;

  if (kind === "prose") {
    return value.includes("/") && hasFilenameExtension(reference.path) ? reference : undefined;
  }
  return value.includes("/") || hasFilenameExtension(reference.path) ? reference : undefined;
}

function parseLink(value: string, cwd: string): FileReference | undefined {
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
    return bindFilePath(decodeURIComponent(surface.path), cwd);
  } catch {
    return undefined;
  }
}

export function parseFileReference(value: string, kind: FileReferenceKind, cwd: string): FileReference | undefined {
  if (cwd === "") return undefined;
  return kind === "link" ? parseLink(value, cwd) : parseLiteral(value, kind, cwd);
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

export function findFileReferences(text: string, cwd: string): FileReferenceSpan[] {
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
        const reference = parseFileReference(candidate, "prose", cwd);
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
    const reference = parseFileReference(candidate, "prose", cwd);
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
  return {
    ...reference,
    cwd,
    readTarget: reference.provenance === "relative" ? joinCwd(cwd, reference.path) : reference.readTarget,
  };
}
