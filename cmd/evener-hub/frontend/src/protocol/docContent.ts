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
}

// The server reads at most this many bytes into a doc pane and never streams
// more (docFileMaxBytes in cmd/evener-hub/doc_serve.go:21, enforced by the fixed
// read buffer at :185). When it truncates, it says so explicitly via the
// X-Doc-Truncated / X-Doc-Total-Size headers (writeDocFileRaw), so this cap is
// the "first N shown" figure the pane pairs with the header's true total - it
// is no longer inferred from the body length.
export const DOC_FILE_MAX_BYTES = 512 * 1024;

export type DocFileErrorKind = "forbidden" | "not-found" | "error";

// A failed raw-file fetch, carrying the honest HTTP status so the pane maps it
// to the same guard/status contract the HTML variant enforces: 403 for a path
// that escapes the session cwd, 404 for a missing file / unknown or non-local
// session, and a generic error for anything else (doc_serve.go:57-73).
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
  return { text, binary, mediaType, truncated, sizeBytes, totalBytes };
}

// docImageURL builds the /doc/image href for a session-scoped, cwd-relative
// path, hung off the host's base origin (empty for same-origin). Both query
// values are escaped, matching the Go handler's own url.QueryEscape of
// sessionID and rel.
export function docImageURL(origin: string, session: string, path: string): string {
  return `${docBase(origin)}/doc/image?session=${encodeURIComponent(session)}&path=${encodeURIComponent(path)}`;
}
