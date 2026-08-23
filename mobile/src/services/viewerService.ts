// ViewerService wraps the native HTTP transport (nativeHttp.ts) to fetch and
// validate attachment bytes for safe in-app viewing. It enforces a strict
// media policy — only JPEG/PNG/WebP/GIF images (≤20 MiB) and UTF-8 plain text
// or Markdown (≤2 MiB) are allowed. HTML, PDF, executables, archives, and
// unknown binary are rejected with an explicit unsupported-format error that
// names the detected format.
//
// MIME sniff contract: the response Content-Type is authoritative. If it
// conflicts with a declared mediaType on the AttachmentRef, the response type
// wins and a diagnostic is emitted. If the Content-Type is missing or
// unrecognized, the service sniffs from magic bytes. Image types are always
// signature-verified: if the declared image type does not match the actual
// bytes, the sniffed type is used (when supported) or the content is rejected.

import {
  type HttpService,
  type HubHttpResponse,
  isHttpServiceError,
} from "./nativeHttp";

// --- media policy constants -------------------------------------------------

export const MAX_IMAGE_BYTES = 20 * 1024 * 1024; // 20 MiB
export const MAX_TEXT_BYTES = 2 * 1024 * 1024; // 2 MiB

const IMAGE_TYPES = new Set([
  "image/jpeg",
  "image/png",
  "image/webp",
  "image/gif",
]);
const TEXT_TYPES = new Set(["text/plain", "text/markdown"]);
const SUPPORTED_TYPES = new Set([...IMAGE_TYPES, ...TEXT_TYPES]);

// Content-Types that are explicitly rejected — never rendered, even if the
// bytes happen to be valid. The error names the type so the UI can show it.
const REJECTED_CONTENT_TYPES = new Set([
  "text/html",
  "application/pdf",
  "application/zip",
  "application/x-zip-compressed",
  "application/x-tar",
  "application/gzip",
  "application/x-gzip",
  "application/x-7z-compressed",
  "application/x-rar-compressed",
  "application/x-executable",
  "application/x-msdos-program",
  "application/x-sh",
  "application/x-shellscript",
  "application/octet-stream",
]);

// --- magic byte signatures --------------------------------------------------

const SIG_JPEG = [0xff, 0xd8] as const;
const SIG_PNG = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a] as const;
const SIG_GIF = [0x47, 0x49, 0x46, 0x38] as const; // "GIF8"
const SIG_RIFF = [0x52, 0x49, 0x46, 0x46] as const; // "RIFF" at offset 0
const SIG_WEBP = [0x57, 0x45, 0x42, 0x50] as const; // "WEBP" at offset 8
const SIG_PDF = [0x25, 0x50, 0x44, 0x46] as const; // "%PDF"
const SIG_ZIP = [0x50, 0x4b, 0x03, 0x04] as const; // "PK\x03\x04"
const SIG_ZIP_EMPTY = [0x50, 0x4b, 0x05, 0x06] as const; // empty archive end
const SIG_ELF = [0x7f, 0x45, 0x4c, 0x46] as const; // "\x7fELF"
const SIG_MZ = [0x4d, 0x5a] as const; // "MZ" (PE/DOS executable)

function bytesEqualAt(
  bytes: Uint8Array,
  sig: readonly number[],
  offset: number,
): boolean {
  if (bytes.length < offset + sig.length) return false;
  for (let i = 0; i < sig.length; i++) {
    if (bytes[offset + i] !== sig[i]) return false;
  }
  return true;
}

/**
 * Detect the format of a byte stream from its magic bytes. Returns a MIME type
 * for any recognized format (supported or rejected), or `null` if unrecognized.
 * Text content has no magic bytes and is never sniffed here — it requires an
 * explicit Content-Type to be accepted.
 */
export function sniffFormat(bytes: Uint8Array): string | null {
  if (bytesEqualAt(bytes, SIG_JPEG, 0)) return "image/jpeg";
  if (bytesEqualAt(bytes, SIG_PNG, 0)) return "image/png";
  if (bytesEqualAt(bytes, SIG_GIF, 0)) return "image/gif";
  if (
    bytes.length >= 12 &&
    bytesEqualAt(bytes, SIG_RIFF, 0) &&
    bytesEqualAt(bytes, SIG_WEBP, 8)
  ) {
    return "image/webp";
  }
  if (bytesEqualAt(bytes, SIG_PDF, 0)) return "application/pdf";
  if (bytesEqualAt(bytes, SIG_ZIP, 0) || bytesEqualAt(bytes, SIG_ZIP_EMPTY, 0))
    return "application/zip";
  if (bytesEqualAt(bytes, SIG_ELF, 0)) return "application/x-executable";
  if (bytesEqualAt(bytes, SIG_MZ, 0)) return "application/x-msdos-program";
  // HTML sniff: check first 512 bytes for common markers (no NUL = not binary).
  const head = decodeAsciiHead(bytes, 512);
  if (head) {
    if (/^\s*<!doctype\s+html/i.test(head)) return "text/html";
    if (/^\s*<html/i.test(head)) return "text/html";
  }
  return null;
}

function decodeAsciiHead(bytes: Uint8Array, maxLen: number): string {
  const len = Math.min(bytes.length, maxLen);
  let s = "";
  for (let i = 0; i < len; i++) {
    const b = bytes[i];
    if (b === undefined) return "";
    if (b === 0) return ""; // NUL → binary, not HTML
    s += String.fromCharCode(b);
  }
  return s;
}

// --- errors -----------------------------------------------------------------

export type ViewerErrorCode =
  | "unsupported_format"
  | "oversized"
  | "fetch_failed"
  | "invalid_utf8"
  | "cancelled";

export class ViewerError extends Error {
  readonly code: ViewerErrorCode;
  /** Detected format name for unsupported-format errors. */
  readonly format: string | undefined;
  constructor(code: ViewerErrorCode, message: string, format?: string) {
    super(message);
    this.name = "ViewerError";
    this.code = code;
    this.format = format;
  }
}

export function isViewerError(value: unknown): value is ViewerError {
  return value instanceof ViewerError;
}

// --- result types -----------------------------------------------------------

export type ViewerMediaKind = "image" | "text";

export interface ViewerFetchResult {
  readonly bytes: Uint8Array;
  readonly contentType: string;
  readonly kind: ViewerMediaKind;
}

export interface ViewerDiagnostic {
  readonly kind: "mime_mismatch";
  readonly declared: string;
  readonly actual: string;
  readonly url: string;
}

export type DiagnosticSink = (diagnostic: ViewerDiagnostic) => void;

// --- service interface ------------------------------------------------------

export interface ViewerService {
  /**
   * Fetch validated bytes for an attachment. Returns raw bytes, the resolved
   * content type, and the media kind (image or text). Validates HTTP status,
   * content type, magic byte signatures (images), byte count, and UTF-8
   * validity (text).
   *
   * @param url Fetch URL (Hub path or data: URL).
   * @param declaredMediaType Optional mediaType from the AttachmentRef — used
   *   only for mismatch diagnostics; the response Content-Type always wins.
   */
  fetchAttachment(
    url: string,
    declaredMediaType?: string,
  ): Promise<ViewerFetchResult>;
  /** Cancel an in-flight fetch for the given URL. */
  cancel(url: string): void;
}

export interface ViewerServiceOptions {
  readonly onDiagnostic?: DiagnosticSink;
}

// --- pure validation (exported for direct unit testing) ---------------------

/**
 * Validate raw attachment bytes against the media policy. This is the pure
 * core of the service — no HTTP, no cancellation, no side effects beyond the
 * optional diagnostic sink. The service calls this after a successful fetch.
 */
export function validateViewerBytes(
  bytes: Uint8Array,
  responseContentType: string | null,
  declaredMediaType: string | undefined,
  onDiagnostic?: DiagnosticSink,
  url?: string,
): ViewerFetchResult {
  const baseType = parseBaseContentType(responseContentType);

  let resolvedType: string;

  if (baseType && SUPPORTED_TYPES.has(baseType)) {
    if (IMAGE_TYPES.has(baseType)) {
      // Image: verify magic bytes match the declared type.
      const detected = sniffFormat(bytes);
      if (detected === baseType) {
        resolvedType = baseType;
      } else if (detected && SUPPORTED_TYPES.has(detected)) {
        // Mismatch but sniffed type is supported — use actual, log diagnostic.
        resolvedType = detected;
      } else if (detected) {
        // Sniffed a known but unsupported format (PDF, ZIP, etc.).
        throw new ViewerError(
          "unsupported_format",
          `unsupported format: ${detected}`,
          detected,
        );
      } else {
        // Unknown bytes with a supported image Content-Type — reject.
        throw new ViewerError(
          "unsupported_format",
          `content does not match declared type ${baseType}`,
          baseType,
        );
      }
    } else {
      // Text: no magic byte check; trust the Content-Type.
      resolvedType = baseType;
    }
  } else if (baseType && REJECTED_CONTENT_TYPES.has(baseType)) {
    throw new ViewerError(
      "unsupported_format",
      `unsupported format: ${baseType}`,
      baseType,
    );
  } else {
    // Content-Type missing or unrecognized — sniff from magic bytes.
    const detected = sniffFormat(bytes);
    if (detected && SUPPORTED_TYPES.has(detected)) {
      resolvedType = detected;
    } else if (detected) {
      throw new ViewerError(
        "unsupported_format",
        `unsupported format: ${detected}`,
        detected,
      );
    } else {
      throw new ViewerError(
        "unsupported_format",
        "unsupported or unknown format",
        baseType ?? "unknown",
      );
    }
  }

  // Declared mediaType mismatch diagnostic (response Content-Type wins).
  if (
    declaredMediaType &&
    declaredMediaType !== "" &&
    declaredMediaType !== resolvedType
  ) {
    onDiagnostic?.({
      kind: "mime_mismatch",
      declared: declaredMediaType,
      actual: resolvedType,
      url: url ?? "",
    });
  }

  // Size limit.
  const maxBytes = IMAGE_TYPES.has(resolvedType)
    ? MAX_IMAGE_BYTES
    : MAX_TEXT_BYTES;
  if (bytes.byteLength > maxBytes) {
    throw new ViewerError(
      "oversized",
      `content exceeds ${maxBytes} byte limit for ${resolvedType}`,
    );
  }

  // UTF-8 validation for text.
  if (TEXT_TYPES.has(resolvedType)) {
    validateUtf8(bytes);
  }

  return {
    bytes,
    contentType: resolvedType,
    kind: IMAGE_TYPES.has(resolvedType) ? "image" : "text",
  };
}

function parseBaseContentType(
  contentType: string | null | undefined,
): string | null {
  if (!contentType) return null;
  const base = contentType.split(";", 1)[0]?.trim().toLowerCase();
  return base || null;
}

function validateUtf8(bytes: Uint8Array): void {
  try {
    new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    throw new ViewerError("invalid_utf8", "text content is not valid UTF-8");
  }
}

// --- URL helpers ------------------------------------------------------------

function urlToPath(url: string): string {
  if (url.startsWith("data:")) return url;
  try {
    const parsed = new URL(url, "https://placeholder.local");
    return parsed.pathname + (parsed.search || "");
  } catch {
    return url;
  }
}

function parseDataUrl(url: string): {
  bytes: Uint8Array;
  contentType: string;
} {
  const commaIdx = url.indexOf(",");
  if (commaIdx < 0) {
    throw new ViewerError("fetch_failed", "invalid data URL");
  }
  const meta = url.slice(5, commaIdx); // after "data:"
  const data = url.slice(commaIdx + 1);
  const parts = meta.split(";");
  const baseType = parts[0]?.trim().toLowerCase() || "text/plain";
  const isBase64 = parts.includes("base64");

  let bytes: Uint8Array;
  if (isBase64) {
    try {
      const bin = atob(data);
      bytes = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) {
        bytes[i] = bin.charCodeAt(i);
      }
    } catch {
      throw new ViewerError("fetch_failed", "invalid base64 in data URL");
    }
  } else {
    bytes = new TextEncoder().encode(decodeURIComponent(data));
  }

  return { bytes, contentType: baseType };
}

// --- service factory --------------------------------------------------------

export function createViewerService(
  http: HttpService,
  activeProfileId: string,
  options: ViewerServiceOptions = {},
): ViewerService {
  const onDiagnostic = options.onDiagnostic;
  const controllers = new Map<string, AbortController>();

  return {
    async fetchAttachment(
      url: string,
      declaredMediaType?: string,
    ): Promise<ViewerFetchResult> {
      // data: URLs are decoded locally — no HTTP round-trip or cancellation.
      if (url.startsWith("data:")) {
        const { bytes, contentType } = parseDataUrl(url);
        return validateViewerBytes(
          bytes,
          contentType,
          declaredMediaType,
          onDiagnostic,
          url,
        );
      }

      // Replace any stale controller for this URL.
      controllers.get(url)?.abort();
      const controller = new AbortController();
      controllers.set(url, controller);

      try {
        const path = urlToPath(url);
        const response: HubHttpResponse = await http.request({
          activeProfileId,
          method: "GET",
          path,
          signal: controller.signal,
        });

        if (controller.signal.aborted) {
          throw new ViewerError("cancelled", "fetch cancelled");
        }

        if (response.status < 200 || response.status >= 300) {
          throw new ViewerError("fetch_failed", `HTTP ${response.status}`);
        }

        const contentType =
          response.headers.contentType ?? response.mediaType ?? null;
        return validateViewerBytes(
          response.body,
          contentType,
          declaredMediaType,
          onDiagnostic,
          url,
        );
      } catch (cause) {
        if (isViewerError(cause)) throw cause;
        if (controller.signal.aborted) {
          throw new ViewerError("cancelled", "fetch cancelled");
        }
        if (isHttpServiceError(cause) && cause.code === "cancelled") {
          throw new ViewerError("cancelled", "fetch cancelled");
        }
        throw new ViewerError(
          "fetch_failed",
          `fetch failed: ${cause instanceof Error ? cause.message : String(cause)}`,
        );
      } finally {
        controllers.delete(url);
      }
    },

    cancel(url: string): void {
      const controller = controllers.get(url);
      if (controller) {
        controller.abort();
        controllers.delete(url);
      }
    },
  };
}
