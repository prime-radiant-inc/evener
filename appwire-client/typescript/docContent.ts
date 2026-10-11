// Doc-pane data layer (wave 8). Two data paths for the native React doc pane:
//
//   docImageURL - a pure href builder for /doc/image, which already serves
//   raw image bytes (floor §1.5), so an <img src> can use it directly. The
//   query shape (session + path, both escaped) mirrors the Go handler exactly
//   (cmd/evener-hub/output_images.go:202). It returns a string: the web feeds
//   it straight to an <img src>, and native wraps that string in its own
//   {uri, headers} source, which is a host concern, not a package one.
//
//   readDocFile - fetches RAW file bytes from /doc/file?format=raw (the raw
//   variant of handleDocFile, cmd/evener-hub/doc_serve.go:75), then builds the
//   client-side DocFileContent the doc pane renders (binary notice / sanitized
//   markdown / escaped <pre>). It is NOT the legacy /doc/file HTML page. The
//   request itself comes from the DocPort the host supplies, so this module
//   names no browser global, no origin and no credentials policy of its own.

export * from "./documentReadDemand";
export * from "./fileReferences";

export interface DocFileContent {
  text: string;
  binary: boolean;
  mediaType: string;
  truncated: boolean;
  sizeBytes: number;
  // The file's true total size in bytes, present only when the server
  // truncated it (from the X-Doc-Total-Size header). Lets the pane say
  // exactly how much was elided, not just that something was.
  totalBytes?: number;
  // The version this read served (S9): the sha256 of the whole file, from the
  // response's ETag. Absent from a hub without S9 and for a file too large to
  // hash (cmd/evener-hub/doc_serve.go docRevisionMaxBytes), so a caller falls
  // back to comparing what it was shown.
  revision?: string;
  // When the file was last modified, in Unix milliseconds (X-Doc-Modified-At).
  modifiedAt?: number;
}

// The server reads at most this many bytes into a doc pane and never streams
// more (docFileMaxBytes in cmd/evener-hub/doc_serve.go:21, enforced by the fixed
// read buffer at :185). When it truncates, it says so explicitly via the
// X-Doc-Truncated / X-Doc-Total-Size headers (writeDocFileRaw), so this cap is
// the "first N shown" figure the pane pairs with the header's true total - it
// is no longer inferred from the body length.
export const DOC_FILE_MAX_BYTES = 512 * 1024;

export type DocFileErrorKind = "forbidden" | "not-found" | "host-unsupported" | "error";

// A failed raw-file fetch, carrying the honest HTTP status so the pane maps it
// to the same guard/status contract the HTML variant enforces: 403 for a path
// that escapes the session cwd, 404 for a missing file or unknown session, 501
// for a session on a host whose hub predates remote document reads (S7,
// cmd/evener-hub/doc_proxy.go), and a generic error for anything else.
export class DocFileError extends Error {
  readonly kind: DocFileErrorKind;
  readonly status: number;
  constructor(kind: DocFileErrorKind, status: number) {
    super(`readDocFile: ${kind} (status ${status})`);
    this.name = "DocFileError";
    this.kind = kind;
    this.status = status;
  }
}

// DocResponseLike is the minimal response surface readDocFile reads. A real
// fetch Response satisfies it structurally, so a host adapter can hand one
// straight back while the package itself stays free of the DOM.
export interface DocResponseLike {
  ok: boolean;
  status: number;
  headers: { get(name: string): string | null };
  arrayBuffer(): Promise<ArrayBuffer>;
}

// DocFetch is the host's transport for doc requests: it owns transport policy -
// credentials in the browser, auth headers on native - so the package neither
// reaches for a global nor decides how the request is authenticated.
export type DocFetch = (url: string) => Promise<DocResponseLike>;

// DocPort is the whole doc seam a host installs: where the /doc routes live
// plus how to reach them. A DocFetch receives an already-resolved URL and so
// cannot say which hub that URL belongs to; pairing the origin with it keeps
// origin policy in the one place the host configures instead of in every caller.
export interface DocPort {
  // The base the /doc routes hang off, with no trailing "/doc". The empty
  // string means same-origin, which is what the browser supplies: its URLs stay
  // the bare "/doc/..." paths the page has always requested.
  origin: string;
  fetch: DocFetch;
}

function errorKindForStatus(status: number): DocFileErrorKind {
  if (status === 403) return "forbidden";
  if (status === 404) return "not-found";
  if (status === 501) return "host-unsupported";
  return "error";
}

// Hub origins are typed by hand, so a trailing slash is ordinary input; dropping
// it here is what lets both "https://hub.test" and "https://hub.test/" produce
// one well-formed href. The same-origin empty base passes through unchanged.
function docBase(origin: string): string {
  return origin.replace(/\/+$/, "");
}

// docFileRawURL builds the /doc/file?format=raw href for a session-scoped,
// cwd-relative path, hung off the host's base origin (empty for same-origin).
// Both query values are escaped, matching the Go handler's url.QueryEscape of
// session and path.
export function docFileRawURL(origin: string, session: string, path: string): string {
  return `${docBase(origin)}/doc/file?format=raw&session=${encodeURIComponent(session)}&path=${encodeURIComponent(path)}`;
}

// readDocFile fetches raw file content for the native doc pane. The response
// is honest raw bytes with a deliberately un-sniffed Content-Type
// (application/octet-stream for binary, text/plain for text - doc_serve.go's
// writeDocFileRaw), never text/html, so it is safe to consume as data.
//
// sizeBytes is the count of bytes actually received (the response body is the
// authority, since the endpoint's Content-Length is unreliable: absent under
// chunked transfer, and never larger than the server-side cap). truncated and
// totalBytes come from the server's own X-Doc-Truncated / X-Doc-Total-Size
// headers (writeDocFileRaw), present only when the file exceeded the cap - so a
// file of exactly the cap reads as complete, no longer a false positive from
// the old body>=cap inference (floor cross-cutting #9).
export async function readDocFile(session: string, path: string, port: DocPort): Promise<DocFileContent> {
  const res = await port.fetch(docFileRawURL(port.origin, session, path));
  if (!res.ok) {
    throw new DocFileError(errorKindForStatus(res.status), res.status);
  }
  const contentType = res.headers.get("Content-Type") ?? "";
  const binary = contentType.startsWith("application/octet-stream");
  const mediaType = contentType.split(";")[0]?.trim() ?? "";
  const buf = await res.arrayBuffer();
  const sizeBytes = buf.byteLength;
  const truncated = res.headers.get("X-Doc-Truncated") === "true";
  const totalHeader = res.headers.get("X-Doc-Total-Size");
  const parsedTotal = totalHeader === null ? Number.NaN : Number.parseInt(totalHeader, 10);
  const totalBytes = Number.isFinite(parsedTotal) ? parsedTotal : undefined;
  const text = binary ? "" : new TextDecoder().decode(buf);
  const revision = etagRevision(res.headers.get("ETag"));
  const modifiedAt = Number(res.headers.get("X-Doc-Modified-At") ?? Number.NaN);
  return {
    text,
    binary,
    mediaType,
    truncated,
    sizeBytes,
    totalBytes,
    ...(revision === undefined ? {} : { revision }),
    ...(Number.isSafeInteger(modifiedAt) && modifiedAt > 0 ? { modifiedAt } : {}),
  };
}

// etagRevision reads the revision out of an ETag. The hub sends the whole
// file's sha256 as a strong tag (cmd/evener-hub/doc_serve.go writeDocFileRaw),
// so only a quoted 64-character lowercase hex tag names one; a weak tag, or any
// other, is no information.
function etagRevision(etag: string | null): string | undefined {
  return /^"([0-9a-f]{64})"$/.exec(etag?.trim() ?? "")?.[1];
}

// docImageURL builds the /doc/image href for a session-scoped, cwd-relative
// path, hung off the host's base origin (empty for same-origin). Both query
// values are escaped, matching the Go handler's own url.QueryEscape of
// sessionID and rel.
export function docImageURL(origin: string, session: string, path: string): string {
  return `${docBase(origin)}/doc/image?session=${encodeURIComponent(session)}&path=${encodeURIComponent(path)}`;
}

export function docImageReadURL(origin: string, session: string, readTarget: string, generation: string): string {
  return `${docImageURL(origin, session, readTarget)}&read=${encodeURIComponent(generation)}`;
}

// filenameOf returns the last segment of a slash path: a document's name when
// it has no title of its own, on the web's doc pane tab and the phone's Reader.
export function filenameOf(path: string): string {
  return (
    path
      .split("/")
      .filter((segment) => segment.length > 0)
      .at(-1) ?? path
  );
}

// isMarkdownPath reports whether a path renders as markdown: a case-insensitive
// .md or .markdown extension only (the Go handler's rule, strings.EqualFold on
// filepath.Ext).
export function isMarkdownPath(path: string): boolean {
  return /\.(?:md|markdown)$/i.test(path);
}

// isImagePath reports whether a path is an image the hub's /doc/image route
// serves (output_images.go): png, jpeg, gif and webp. SVG is left out there
// as an XSS guard, so an .svg opens as a file, its source shown as text.
export function isImagePath(path: string): boolean {
  return /\.(?:png|jpe?g|gif|webp)$/i.test(path);
}

// fileURLToPath turns a session link's file URL (agent validation
// canonicalizes them to file:///absolute) back into the filesystem path a
// document read takes, percent-escapes decoded. It reads the string itself:
// React Native's URL parses only http and https, so its pathname would be "/"
// for every file URL. A URL naming another machine, a malformed escape, or
// anything that isn't a file URL names no path, "".
export function fileURLToPath(fileURL: string): string {
  const match = /^file:\/\/(?:localhost)?(\/[^?#]*)/i.exec(fileURL.trim());
  if (!match?.[1]) return "";
  try {
    return decodeURIComponent(match[1]);
  } catch {
    return "";
  }
}

// cwdRelative expresses a file path relative to the session's folder, or
// undefined when it isn't inside it. An absolute path loses the folder's
// prefix; a relative one is already relative, unless a ".." segment climbs
// out. The hub serves documents only from inside the folder
// (cmd/evener-hub/doc_serve.go), so a path outside it earns no affordance.
export function cwdRelative(filePath: string, cwd: string): string | undefined {
  const p = filePath.trim();
  if (p === "" || cwd === "") return undefined;
  if (!p.startsWith("/")) {
    return p.split("/").includes("..") ? undefined : p;
  }
  const prefix = cwd.endsWith("/") ? cwd : `${cwd}/`;
  if (!p.startsWith(prefix)) return undefined;
  // The folder itself (with or without its trailing slash) is not a file, and
  // a ".." segment could climb back out of it.
  const rel = p.slice(prefix.length);
  return rel === "" || rel.split("/").includes("..") ? undefined : rel;
}
